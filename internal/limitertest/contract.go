// Package limitertest provides the shared behavioral suite for all backends.
package limitertest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
)

func Run(t *testing.T, create func(*testing.T, limiter.RuleConfig) limiter.Limiter) {
	t.Helper()
	for _, algorithm := range []string{limiter.TokenBucketAlgorithm, limiter.SlidingWindowAlgorithm, limiter.FixedWindowAlgorithm} {
		t.Run(algorithm, func(t *testing.T) {
			// Long windows keep refill and fixed boundaries out of this quota test.
			cfg := limiter.RuleConfig{Algorithm: algorithm, Limit: 5, Window: 200 * 365 * 24 * time.Hour}
			t.Run("quota_and_keys", func(t *testing.T) {
				l := create(t, cfg)
				for i := 0; i < 8; i++ {
					r, err := l.Allow(context.Background(), "shared")
					if err != nil {
						t.Fatal(err)
					}
					remaining := cfg.Limit - i - 1
					if remaining < 0 {
						remaining = 0
					}
					if r.Allowed != (i < 5) || r.Limit != cfg.Limit || r.Remaining != remaining || (r.Allowed && r.RetryAfter != 0) || (!r.Allowed && (r.RetryAfter <= 0 || r.RetryAfter > cfg.Window)) {
						t.Fatalf("decision %d: %+v", i, r)
					}
				}
				r, err := l.Allow(context.Background(), "independent")
				if err != nil || !r.Allowed || r.Remaining != 4 {
					t.Fatalf("keys share capacity: %+v, %v", r, err)
				}
			})
			t.Run("cancellation", func(t *testing.T) {
				l := create(t, cfg)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := l.Allow(ctx, "a"); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
				r, err := l.Allow(context.Background(), "a")
				if err != nil || !r.Allowed || r.Remaining != 4 {
					t.Fatalf("canceled check consumed quota: %+v, %v", r, err)
				}
			})
			t.Run("concurrency", func(t *testing.T) {
				cfg.Limit = 100
				l := create(t, cfg)
				var allowed, failures atomic.Int64
				var wg sync.WaitGroup
				start := make(chan struct{})
				for i := 0; i < 1000; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						r, err := l.Allow(context.Background(), "shared")
						if err != nil {
							failures.Add(1)
							return
						}
						if r.Allowed {
							allowed.Add(1)
						} else if r.RetryAfter <= 0 {
							failures.Add(1)
						}
					}()
				}
				close(start)
				wg.Wait()
				if allowed.Load() != 100 || failures.Load() != 0 {
					t.Fatalf("allowed=%d failures=%d; want 100 and 0", allowed.Load(), failures.Load())
				}
			})
		})
	}
}
