// Package limiter defines rate limiting contracts and algorithms.
package limiter

import (
	"context"
	"time"
)

// Result describes a single rate limit decision. RetryAfter is zero when allowed.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	// Degraded marks a fail-open decision made without authoritative store state.
	Degraded bool
}

// Limiter enforces a configured rule for independently tracked keys.
type Limiter interface {
	Allow(ctx context.Context, key string) (Result, error)
}
