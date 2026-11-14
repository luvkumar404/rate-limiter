package store

import "github.com/ratelimiter/ratelimiter/internal/limiter"

// NewLimiter builds a named rule. A nil Redis selects process-local memory.
// Memory results implement Close; Redis results share the caller-owned pool.
func NewLimiter(name string, rule limiter.RuleConfig, r *Redis) (limiter.Limiter, error) {
	if err := rule.Validate(); err != nil {
		return nil, err
	}
	if rule.Algorithm == limiter.TokenBucketAlgorithm {
		cfg := limiter.TokenBucketConfig{Limit: rule.Limit, Burst: rule.Burst, Window: rule.Window}
		if r != nil {
			return r.TokenBucket(name, cfg)
		}
		return limiter.NewTokenBucket(cfg)
	}
	cfg := limiter.WindowConfig{Limit: rule.Limit, Window: rule.Window}
	if rule.Algorithm == limiter.SlidingWindowAlgorithm {
		if r != nil {
			return r.SlidingWindow(name, cfg)
		}
		return limiter.NewSlidingWindow(cfg)
	}
	if r != nil {
		return r.FixedWindow(name, cfg)
	}
	return limiter.NewFixedWindow(cfg)
}
