package limiter

import (
	"context"
	"testing"
	"time"
)

// BenchmarkAlgorithms holds the clock still to separate allowed and denied
// paths. Shared-key contention is included; HTTP, Redis and clock reads are not.
func BenchmarkAlgorithms(b *testing.B) {
	for _, algorithm := range []string{TokenBucketAlgorithm, SlidingWindowAlgorithm, FixedWindowAlgorithm} {
		for _, allowed := range []bool{true, false} {
			name := "denied"
			if allowed {
				name = "allowed"
			}
			b.Run(algorithm+"/"+name, func(b *testing.B) {
				now := time.Unix(100, 0)
				clock := func() time.Time { return now }
				// Large enough for all iterations on the allowed path. Sliding
				// logs retain each event, so their allocation cost is included.
				limit := 1
				if allowed {
					limit = b.N + 1
				}
				var l Limiter
				var closeLimiter func() error
				if algorithm == TokenBucketAlgorithm {
					tb, err := NewTokenBucket(TokenBucketConfig{Limit: limit, Window: time.Minute, Clock: clock})
					if err != nil {
						b.Fatal(err)
					}
					l, closeLimiter = tb, tb.Close
				} else {
					w, err := newWindow(WindowConfig{Limit: limit, Window: time.Minute, Clock: clock}, algorithm)
					if err != nil {
						b.Fatal(err)
					}
					l, closeLimiter = w, w.Close
				}
				defer func() { _ = closeLimiter() }()
				ctx := context.Background()
				if !allowed {
					_, _ = l.Allow(ctx, "shared")
				}
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						r, err := l.Allow(ctx, "shared")
						if err != nil || r.Allowed != allowed {
							b.Errorf("decision = %+v, %v", r, err)
						}
					}
				})
			})
		}
	}
}
