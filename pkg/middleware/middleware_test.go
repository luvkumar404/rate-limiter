package middleware_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luvkumar404/rate-limiter/pkg/limiter"
	"github.com/luvkumar404/rate-limiter/pkg/middleware"
)

type limiterFunc func(context.Context, string) (middleware.Result, error)

func (f limiterFunc) Allow(ctx context.Context, key string) (middleware.Result, error) {
	return f(ctx, key)
}
func quiet() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestAllowedPreservesApplicationRequest(t *testing.T) {
	var checkID string
	base := limiterFunc(func(ctx context.Context, key string) (middleware.Result, error) {
		if key != "custom-key" {
			t.Errorf("key=%s", key)
		}
		checkID = middleware.RequestID(ctx)
		return middleware.Result{Allowed: true, Limit: 10, Remaining: 9}, nil
	})
	protect, err := middleware.New(base, middleware.Options{Key: func(*http.Request) (string, error) { return "custom-key", nil }, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	h := protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "application body" || r.Context().Err() != nil {
			t.Error("application body or context changed")
		}
		result, ok := middleware.ResultFromContext(r.Context())
		if !ok || !result.Allowed || result.Remaining != 9 {
			t.Errorf("missing result: %+v", result)
		}
		if middleware.RequestID(r.Context()) != checkID {
			t.Error("ID lost")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "business response")
	}))
	r := httptest.NewRequest("POST", "/orders", strings.NewReader("application body"))
	r.Header.Set("X-Request-ID", "app-request-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 || w.Body.String() != "business response" || w.Header().Get("X-RateLimit-Limit") != "10" || w.Header().Get("X-RateLimit-Remaining") != "9" || w.Header().Get("X-Request-ID") != "app-request-1" || w.Header().Get("Retry-After") != "" {
		t.Fatalf("unexpected response: %d %s %v", w.Code, w.Body, w.Header())
	}
}

func TestDeniedAndFailurePolicies(t *testing.T) {
	for _, tc := range []struct {
		name, policy     string
		result           middleware.Result
		err              error
		status           int
		called, degraded bool
	}{
		{"denied", "", middleware.Result{Limit: 5, RetryAfter: 1500 * time.Millisecond}, nil, 429, false, false},
		{"default closed", "", middleware.Result{}, errors.New("private backend failure"), 503, false, false},
		{"open", middleware.FailOpen, middleware.Result{}, errors.New("private backend failure"), 204, true, true},
		{"upstream degraded", "", middleware.Result{Allowed: true, Limit: 5, Degraded: true}, nil, 204, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			protect, err := middleware.New(limiterFunc(func(context.Context, string) (middleware.Result, error) { return tc.result, tc.err }), middleware.Options{FailurePolicy: tc.policy, Logger: quiet()})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			h := protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				result, _ := middleware.ResultFromContext(r.Context())
				if result.Degraded != tc.degraded {
					t.Error("degraded decision lost")
				}
				w.WriteHeader(204)
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Code != tc.status || called != tc.called || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("status=%d called=%v body=%s", w.Code, called, w.Body)
			}
			if tc.status == 429 && (w.Header().Get("Retry-After") != "2" || !strings.Contains(w.Body.String(), `"retry_after_ms":1500`)) {
				t.Fatal("bad retry response")
			}
			if (w.Header().Get("X-RateLimit-Degraded") == "true") != tc.degraded {
				t.Fatal("missing degraded header")
			}
			if tc.err != nil && w.Header().Get("X-RateLimit-Limit") != "" {
				t.Fatal("unknown quota emitted as a configured limit")
			}
		})
	}
}

func TestKeyErrorsAndCancellation(t *testing.T) {
	for _, key := range []middleware.KeyFunc{
		func(*http.Request) (string, error) { return "", errors.New("private") },
		func(*http.Request) (string, error) { return " ", nil },
		func(*http.Request) (string, error) { return strings.Repeat("x", 513), nil },
	} {
		protect, _ := middleware.New(limiterFunc(func(context.Context, string) (middleware.Result, error) {
			t.Fatal("called limiter with invalid key")
			return middleware.Result{}, nil
		}), middleware.Options{Key: key, FailurePolicy: middleware.FailOpen, Logger: quiet()})
		w := httptest.NewRecorder()
		protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid key reached handler") })).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	protect, _ := middleware.New(limiterFunc(func(context.Context, string) (middleware.Result, error) {
		cancel()
		return middleware.Result{}, context.Canceled
	}), middleware.Options{FailurePolicy: middleware.FailOpen, Logger: quiet()})
	h := protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("canceled request reached application") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestMiddlewareTimeoutAndValidation(t *testing.T) {
	protect, err := middleware.New(limiterFunc(func(ctx context.Context, _ string) (middleware.Result, error) {
		<-ctx.Done()
		return middleware.Result{}, ctx.Err()
	}), middleware.Options{Timeout: 5 * time.Millisecond, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("timed out request reached app") })).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if _, err := middleware.New(nil, middleware.Options{}); err == nil {
		t.Fatal("nil limiter accepted")
	}
	l, _ := limiter.NewTokenBucket(limiter.TokenBucketConfig{Limit: 1, Window: time.Second})
	defer func() { _ = l.Close() }()
	for _, opts := range []middleware.Options{{Timeout: -1}, {FailurePolicy: "unknown"}} {
		if _, err := middleware.New(l, opts); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
