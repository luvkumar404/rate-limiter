// Package limiter exposes built-in rate limiters for embedding in other Go apps.
// Instances are safe for concurrent use. Callers own their cleanup lifecycle.
package limiter

import (
	core "github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/luvkumar404/rate-limiter/internal/store"
)

type Result = core.Result
type Limiter = core.Limiter
type TokenBucketConfig = core.TokenBucketConfig
type TokenBucket = core.TokenBucket
type WindowConfig = core.WindowConfig
type WindowLimiter = core.WindowLimiter
type RuleConfig = core.RuleConfig
type RedisOptions = store.RedisOptions
type Redis = store.Redis

const (
	TokenBucketAlgorithm   = core.TokenBucketAlgorithm
	SlidingWindowAlgorithm = core.SlidingWindowAlgorithm
	FixedWindowAlgorithm   = core.FixedWindowAlgorithm
)

var ErrClosed = core.ErrClosed

func NewTokenBucket(cfg TokenBucketConfig) (*TokenBucket, error) { return core.NewTokenBucket(cfg) }
func NewSlidingWindow(cfg WindowConfig) (*WindowLimiter, error)  { return core.NewSlidingWindow(cfg) }
func NewFixedWindow(cfg WindowConfig) (*WindowLimiter, error)    { return core.NewFixedWindow(cfg) }
func NewRedis(opts RedisOptions) (*Redis, error)                 { return store.NewRedis(opts) }

// New builds a named rule, using memory if redis is nil. Memory results also
// implement io.Closer. Redis results share the caller-owned Redis pool.
func New(name string, rule RuleConfig, redis *Redis) (Limiter, error) {
	return store.NewLimiter(name, rule, redis)
}
