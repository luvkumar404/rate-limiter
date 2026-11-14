package metrics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/ratelimiter/ratelimiter/internal/resilience"
)

func sample(t *testing.T, m *Metrics, name, label string) float64 {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == name {
			for _, metric := range family.Metric {
				if label != "" {
					if len(metric.Label) != 1 || metric.Label[0].GetValue() != label {
						continue
					}
				}
				if metric.Counter != nil {
					return metric.Counter.GetValue()
				}
				if metric.Gauge != nil {
					return metric.Gauge.GetValue()
				}
				if metric.Histogram != nil {
					return float64(metric.Histogram.GetSampleCount())
				}
			}
		}
	}
	t.Fatalf("missing metric %s/%s", name, label)
	return 0
}

func TestCheckOutcomesAndRegistryIsolation(t *testing.T) {
	m := New(nil)
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, tc := range []struct {
		ctx    context.Context
		result limiter.Result
		err    error
	}{
		{ctx, limiter.Result{Allowed: true}, nil},
		{ctx, limiter.Result{Allowed: false}, nil},
		{ctx, limiter.Result{Allowed: true, Degraded: true}, nil},
		{ctx, limiter.Result{}, errors.New("backend")},
		{canceled, limiter.Result{}, context.Canceled},
	} {
		m.ObserveCheck(tc.ctx, tc.result, tc.err, time.Millisecond)
	}
	for _, label := range []string{"allowed", "blocked", "degraded", "error", "canceled"} {
		if n := sample(t, m, "checks_total", label); n != 1 {
			t.Fatalf("%s=%v", label, n)
		}
	}
	if n := sample(t, m, "check_duration_seconds", ""); n != 5 {
		t.Fatalf("histogram count=%v", n)
	}
	other := New(nil)
	if n := sample(t, other, "checks_total", "allowed"); n != 0 {
		t.Fatal("instances share metrics")
	}
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Body.String(), "# TYPE check_duration_seconds histogram") {
		t.Fatalf("bad exposition: %d %s", w.Code, w.Body.String())
	}
}

type limiterFunc func(context.Context, string) (limiter.Result, error)

func (f limiterFunc) Allow(ctx context.Context, key string) (limiter.Result, error) {
	return f(ctx, key)
}

func TestRedisErrorsAndCircuitGauge(t *testing.T) {
	for _, policy := range []string{resilience.FailOpen, resilience.FailClosed} {
		t.Run(policy, func(t *testing.T) {
			var nanos atomic.Int64
			nanos.Store(time.Unix(100, 0).UnixNano())
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			b, err := resilience.NewBreaker(resilience.Options{FailureThreshold: 1, OpenTimeout: time.Second, Clock: func() time.Time { return time.Unix(0, nanos.Load()) }}, logger)
			if err != nil {
				t.Fatal(err)
			}
			m := New(b.State)
			if n := sample(t, m, "circuit_state", ""); n != 0 {
				t.Fatalf("initial circuit=%v", n)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int64
			next := limiterFunc(func(context.Context, string) (limiter.Result, error) {
				if calls.Add(1) == 1 {
					return limiter.Result{}, errors.New("Redis failed")
				}
				close(entered)
				<-release
				return limiter.Result{Allowed: true, Limit: 10, Remaining: 9}, nil
			})
			g, err := resilience.NewGuard(m.WrapRedis(next), b, policy, 10, "api", logger)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				r, err := g.Allow(context.Background(), "private-user-key")
				m.ObserveCheck(context.Background(), r, err, time.Millisecond)
			}
			label := "error"
			if policy == resilience.FailOpen {
				label = "degraded"
			}
			if n := sample(t, m, "checks_total", label); n != 2 {
				t.Fatalf("outcome count=%v", n)
			}
			if n := sample(t, m, "redis_errors_total", ""); n != 1 || calls.Load() != 1 {
				t.Fatalf("short-circuit counted as Redis error: %v calls=%d", n, calls.Load())
			}
			if n := sample(t, m, "circuit_state", ""); n != 1 {
				t.Fatalf("open circuit=%v", n)
			}
			nanos.Add(int64(time.Second))
			done := make(chan error, 1)
			go func() {
				r, err := g.Allow(context.Background(), "private-user-key")
				m.ObserveCheck(context.Background(), r, err, time.Millisecond)
				done <- err
			}()
			<-entered
			if n := sample(t, m, "circuit_state", ""); n != 2 {
				t.Fatalf("half-open circuit=%v", n)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if n := sample(t, m, "circuit_state", ""); n != 0 {
				t.Fatalf("recovered circuit=%v", n)
			}
			if n := sample(t, m, "checks_total", "allowed"); n != 1 {
				t.Fatalf("probe was not an allowed check: %v", n)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_ = m.RedisOperation(ctx, func(context.Context) error { return context.Canceled })
			if n := sample(t, m, "redis_errors_total", ""); n != 1 {
				t.Fatal("caller cancellation counted as Redis failure")
			}
		})
	}
}
