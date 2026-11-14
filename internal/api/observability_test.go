package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/luvkumar404/rate-limiter/internal/requestid"
	"github.com/luvkumar404/rate-limiter/internal/resilience"
)

func TestRateLimitHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result limiter.Result
		err    error
		retry  string
	}{
		{"allowed", limiter.Result{Allowed: true, Limit: 10, Remaining: 9}, nil, ""},
		{"blocked fractional second", limiter.Result{Limit: 10, RetryAfter: 1500 * time.Millisecond}, nil, "2"},
		{"blocked exact second", limiter.Result{Limit: 10, RetryAfter: time.Second}, nil, "1"},
		{"blocked nanosecond", limiter.Result{Limit: 10, RetryAfter: time.Nanosecond}, nil, "1"},
		{"degraded", limiter.Result{Allowed: true, Limit: 10, Degraded: true}, nil, ""},
		{"backend error", limiter.Result{}, errors.New("backend"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(map[string]limiter.Limiter{"api": stubLimiter(func(context.Context, string) (limiter.Result, error) { return tc.result, tc.err })}, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/check", strings.NewReader(`{"key":"a","rule":"api"}`)))
			if w.Header().Get("Retry-After") != tc.retry {
				t.Fatalf("Retry-After=%q want %q", w.Header().Get("Retry-After"), tc.retry)
			}
			if tc.err != nil {
				if w.Header().Get("X-RateLimit-Limit") != "" {
					t.Fatal("error claims quota state")
				}
				return
			}
			if w.Header().Get("X-RateLimit-Limit") != "10" {
				t.Fatal("missing limit header")
			}
			var body checkResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Header().Get("X-RateLimit-Remaining") != strconv.Itoa(body.Remaining) {
				t.Fatal("remaining header differs from body")
			}
		})
	}
}

func TestRequestIDsAndLogs(t *testing.T) {
	for _, id := range []string{"client-123", "", "bad id", strings.Repeat("x", 129)} {
		t.Run(id, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			b, err := resilience.NewBreaker(resilience.Options{FailureThreshold: 1}, logger)
			if err != nil {
				t.Fatal(err)
			}
			var backendID string
			g, err := resilience.NewGuard(stubLimiter(func(ctx context.Context, _ string) (limiter.Result, error) {
				backendID = requestid.FromContext(ctx)
				return limiter.Result{}, errors.New("backend unavailable")
			}), b, resilience.FailOpen, 10, "api", logger)
			if err != nil {
				t.Fatal(err)
			}
			h := New(map[string]limiter.Limiter{"api": g}, logger)
			r := httptest.NewRequest("POST", "/check", strings.NewReader(`{"key":"private-key","rule":"api"}`))
			if id != "" {
				r.Header.Set("X-Request-ID", id)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			returned := w.Header().Get("X-Request-ID")
			if !requestid.Valid(returned) || backendID != returned {
				t.Fatal("request ID did not propagate")
			}
			if requestid.Valid(id) && returned != id {
				t.Fatal("valid caller ID replaced")
			}
			if !requestid.Valid(id) && returned == id {
				t.Fatal("invalid caller ID reused")
			}
			d := json.NewDecoder(&logs)
			count := 0
			for d.More() {
				var record map[string]any
				if err := d.Decode(&record); err != nil {
					t.Fatal(err)
				}
				if record["request_id"] != returned {
					t.Fatalf("uncorrelated log: %+v", record)
				}
				count++
			}
			if count != 3 {
				t.Fatalf("expected circuit, fallback, and access logs; got %d", count)
			}
		})
	}
}

func TestMetricsMatchHTTPDecisions(t *testing.T) {
	h := testHandler(t)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/check", strings.NewReader(`{"key":"a","rule":"api"}`)))
	}
	invalid := httptest.NewRecorder()
	h.ServeHTTP(invalid, httptest.NewRequest("POST", "/check", strings.NewReader(`{}`)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{`checks_total{result="allowed"} 3`, `checks_total{result="blocked"} 2`, "check_duration_seconds_count 5", "redis_errors_total 0", "circuit_state 0"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s in metrics", want)
		}
	}
	if !requestid.Valid(w.Header().Get("X-Request-ID")) {
		t.Fatal("metrics response lacks request ID")
	}
	for _, path := range []string{"/healthz", "/readyz", "/missing"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Add("X-Request-ID", "first")
		r.Header.Add("X-Request-ID", "second")
		h.ServeHTTP(w, r)
		if id := w.Header().Get("X-Request-ID"); !requestid.Valid(id) || id == "first" || id == "second" {
			t.Fatal("multiple IDs were accepted or response lacks ID")
		}
	}
}
