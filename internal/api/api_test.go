package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/luvkumar404/rate-limiter/internal/resilience"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	rules := make(map[string]limiter.Limiter)
	for _, name := range []string{"api", "login"} {
		b, err := limiter.NewTokenBucket(limiter.TokenBucketConfig{
			Limit: 2, Burst: 1, Window: time.Second,
			Clock: func() time.Time { return time.Unix(100, 0) },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		rules[name] = b
	}
	return New(rules, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestCheck(t *testing.T) {
	h := testHandler(t)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/check", strings.NewReader(`{"key":"user:42","rule":"api"}`))
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		h.ServeHTTP(w, r)
		var body checkResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		wantStatus, remaining, retry := 200, 2-i, int64(0)
		if i >= 3 {
			wantStatus, remaining, retry = 429, 0, 500
		}
		if w.Code != wantStatus || body.Allowed != (i < 3) || body.Limit != 2 || body.Remaining != remaining || body.RetryAfterMS != retry {
			t.Fatalf("check %d: status=%d body=%+v", i, w.Code, body)
		}
		if w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("missing JSON content type")
		}
	}
	for _, body := range []string{`{"key":"other","rule":"api"}`, `{"key":"user:42","rule":"login"}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/check", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("keys or rules share capacity: %s", w.Body.String())
		}
	}
}

func TestValidation(t *testing.T) {
	h := testHandler(t)
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"empty", "", "", 400}, {"null", "null", "", 400},
		{"malformed", "{", "", 400}, {"array", "[]", "", 400},
		{"missing key", `{"rule":"api"}`, "", 400},
		{"missing rule", `{"key":"a"}`, "", 400},
		{"blank key", `{"key":"  ","rule":"api"}`, "", 400},
		{"unknown rule", `{"key":"a","rule":"unknown"}`, "", 400},
		{"wrong type", `{"key":12,"rule":"api"}`, "", 400},
		{"unknown field", `{"key":"a","rule":"api","extra":1}`, "", 400},
		{"multiple objects", `{"key":"a","rule":"api"} {}`, "", 400},
		{"trailing junk", `{"key":"a","rule":"api"} nope`, "", 400},
		{"long key", `{"key":"` + strings.Repeat("x", 513) + `","rule":"api"}`, "", 400},
		{"oversized", `{"key":"` + strings.Repeat("x", 4096) + `","rule":"api"}`, "", 413},
		{"trailing oversized", `{"key":"a","rule":"api"}` + strings.Repeat(" ", 4096), "", 413},
		{"content type", `{}`, "text/plain", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/check", strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d (%s), want %d", w.Code, w.Body.String(), tc.status)
			}
		})
	}
}

func TestRoutes(t *testing.T) {
	h := testHandler(t)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200}, {"GET", "/readyz", 200},
		{"GET", "/check", 405}, {"POST", "/healthz", 405},
		{"GET", "/missing", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}

type stubLimiter func(context.Context, string) (limiter.Result, error)

func (f stubLimiter) Allow(ctx context.Context, key string) (limiter.Result, error) {
	return f(ctx, key)
}

func TestBackendErrorAndRetryRounding(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   stubLimiter
		code int
	}{
		{"failure", func(context.Context, string) (limiter.Result, error) {
			return limiter.Result{}, errors.New("sensitive backend detail")
		}, 503},
		{"rounding", func(context.Context, string) (limiter.Result, error) {
			return limiter.Result{Limit: 1, RetryAfter: time.Nanosecond}, nil
		}, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(map[string]limiter.Limiter{"api": tc.fn}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/check", strings.NewReader(`{"key":"a","rule":"api"}`)))
			if w.Code != tc.code || strings.Contains(w.Body.String(), "sensitive") {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			if tc.code == 429 && !strings.Contains(w.Body.String(), `"retry_after_ms":1`) {
				t.Fatal("retry duration was not rounded up")
			}
		})
	}
}

func TestReadinessReflectsBackendHealth(t *testing.T) {
	for _, available := range []bool{true, false} {
		h := New(nil, slog.New(slog.NewJSONHandler(io.Discard, nil)), WithBackend("redis", func(ctx context.Context) error {
			if !available {
				return errors.New("private connection details")
			}
			return ctx.Err()
		}))
		for _, path := range []string{"/healthz", "/readyz"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			want := 200
			if path == "/readyz" && !available {
				want = 503
			}
			if w.Code != want || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("available=%t path=%s: %d %s", available, path, w.Code, w.Body.String())
			}
		}
	}
}

func TestFailurePolicyHTTPResponses(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for _, policy := range []string{resilience.FailOpen, resilience.FailClosed} {
		t.Run(policy, func(t *testing.T) {
			b, err := resilience.NewBreaker(resilience.Options{FailureThreshold: 1}, logger)
			if err != nil {
				t.Fatal(err)
			}
			g, err := resilience.NewGuard(stubLimiter(func(context.Context, string) (limiter.Result, error) {
				return limiter.Result{}, errors.New("private Redis failure")
			}), b, policy, 7, "api", logger)
			if err != nil {
				t.Fatal(err)
			}
			h := New(map[string]limiter.Limiter{"api": g}, logger)
			// Exercise both the original backend failure and the open circuit.
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/check", strings.NewReader(`{"key":"a","rule":"api"}`)))
				if strings.Contains(w.Body.String(), "private") {
					t.Fatal("backend details leaked")
				}
				if policy == resilience.FailOpen {
					var got checkResponse
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if w.Code != 200 || !got.Allowed || !got.Degraded || got.Limit != 7 || got.Remaining != 0 || got.RetryAfterMS != 0 {
						t.Fatalf("fail-open response: %d %+v", w.Code, got)
					}
				} else if w.Code != 503 || !strings.Contains(w.Body.String(), `"error"`) {
					t.Fatalf("fail-closed response: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}
