package resilience

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
)

type limiterFunc func(context.Context, string) (limiter.Result, error)

func (f limiterFunc) Allow(ctx context.Context, key string) (limiter.Result, error) {
	return f(ctx, key)
}

func TestGuardPoliciesAndSharedCircuit(t *testing.T) {
	for _, policy := range []string{FailOpen, FailClosed} {
		t.Run(policy, func(t *testing.T) {
			now := time.Unix(100, 0)
			b := testBreaker(t, 1, func() time.Time { return now })
			calls := 0
			next := limiterFunc(func(context.Context, string) (limiter.Result, error) {
				calls++
				return limiter.Result{}, backendFailure
			})
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			for _, rule := range []string{"first", "other"} {
				g, err := NewGuard(next, b, policy, 10, rule, logger)
				if err != nil {
					t.Fatal(err)
				}
				got, err := g.Allow(context.Background(), "user")
				if policy == FailOpen {
					if err != nil || !got.Allowed || !got.Degraded || got.Remaining != 0 || got.Limit != 10 || got.RetryAfter != 0 {
						t.Fatalf("fail-open: %+v %v", got, err)
					}
				} else if err == nil || got.Allowed {
					t.Fatalf("fail-closed allowed: %+v %v", got, err)
				}
			}
			if calls != 1 {
				t.Fatalf("breaker not shared: %d calls", calls)
			}
		})
	}
}

func TestGuardPreservesDecisionsAndCancellation(t *testing.T) {
	b := testBreaker(t, 1, time.Now)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	for _, allowed := range []bool{true, false} {
		want := limiter.Result{Allowed: allowed, Limit: 10, Remaining: 2}
		if !allowed {
			want.RetryAfter = time.Second
		}
		g, err := NewGuard(limiterFunc(func(context.Context, string) (limiter.Result, error) { return want, nil }), b, FailOpen, 10, "api", logger)
		if err != nil {
			t.Fatal(err)
		}
		got, err := g.Allow(context.Background(), "a")
		if err != nil || got != want || b.State() != Closed {
			t.Fatalf("healthy decision changed: %+v %v", got, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if r, err := g.Allow(ctx, "a"); !errors.Is(err, context.Canceled) || r.Allowed {
			t.Fatalf("canceled request allowed: %+v %v", r, err)
		}
		if _, err := g.Allow(context.Background(), " "); err == nil {
			t.Fatal("blank key accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	g, _ := NewGuard(limiterFunc(func(context.Context, string) (limiter.Result, error) {
		cancel()
		return limiter.Result{}, context.Canceled
	}), b, FailOpen, 10, "api", logger)
	if r, err := g.Allow(ctx, "a"); !errors.Is(err, context.Canceled) || r.Allowed || b.State() != Closed {
		t.Fatalf("in-flight cancellation counted or allowed: %+v %v", r, err)
	}
}

func TestGuardValidation(t *testing.T) {
	b := testBreaker(t, 1, time.Now)
	if _, err := NewGuard(nil, b, FailOpen, 1, "api", nil); err == nil {
		t.Fatal("nil limiter accepted")
	}
	f := limiterFunc(func(context.Context, string) (limiter.Result, error) { return limiter.Result{}, nil })
	if _, err := NewGuard(f, nil, FailOpen, 1, "api", nil); err == nil {
		t.Fatal("nil breaker accepted")
	}
	if _, err := NewGuard(f, b, "bad", 1, "api", nil); err == nil {
		t.Fatal("bad policy accepted")
	}
}
