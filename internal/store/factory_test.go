package store

import (
	"io"
	"testing"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/ratelimiter/ratelimiter/internal/limitertest"
)

func TestMemoryContract(t *testing.T) {
	limitertest.Run(t, func(t *testing.T, cfg limiter.RuleConfig) limiter.Limiter {
		l, err := NewLimiter("api", cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.(io.Closer).Close() })
		return l
	})
}

func TestFactoryRejectsInvalidRules(t *testing.T) {
	for _, cfg := range []limiter.RuleConfig{
		{Algorithm: "unknown", Limit: 1, Window: time.Second},
		{Algorithm: limiter.SlidingWindowAlgorithm, Limit: 1, Burst: 1, Window: time.Second},
		{Algorithm: limiter.FixedWindowAlgorithm, Limit: 1, Burst: 1, Window: time.Second},
	} {
		if l, err := NewLimiter("api", cfg, nil); err == nil {
			_ = l.(io.Closer).Close()
			t.Fatalf("invalid rule accepted: %+v", cfg)
		}
	}
}
