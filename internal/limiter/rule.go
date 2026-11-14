package limiter

import (
	"fmt"
	"time"
)

const (
	TokenBucketAlgorithm   = "token_bucket"
	SlidingWindowAlgorithm = "sliding_window"
	FixedWindowAlgorithm   = "fixed_window"
)

// RuleConfig contains the algorithm parameters shared by both backends.
type RuleConfig struct {
	Algorithm string        `yaml:"algorithm"`
	Limit     int           `yaml:"limit"`
	Window    time.Duration `yaml:"window"`
	Burst     int           `yaml:"burst"`
}

func (r RuleConfig) Validate() error {
	switch r.Algorithm {
	case TokenBucketAlgorithm:
		return (TokenBucketConfig{Limit: r.Limit, Burst: r.Burst, Window: r.Window}).Validate()
	case SlidingWindowAlgorithm, FixedWindowAlgorithm:
		if r.Burst != 0 {
			return fmt.Errorf("burst is only supported by token_bucket")
		}
		return (WindowConfig{Limit: r.Limit, Window: r.Window}).Validate()
	default:
		return fmt.Errorf("algorithm must be token_bucket, sliding_window, or fixed_window")
	}
}
