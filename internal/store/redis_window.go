package store

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/redis/go-redis/v9"
)

//go:embed scripts/sliding_window.lua
var slidingWindowSource string

//go:embed scripts/fixed_window.lua
var fixedWindowSource string

var slidingWindowScript = redis.NewScript(slidingWindowSource)
var fixedWindowScript = redis.NewScript(fixedWindowSource)

// RedisWindow enforces an exact sliding log or epoch-aligned fixed window.
type RedisWindow struct {
	store     *Redis
	rule      string
	algorithm string
	limit     int
	windowUS  int64
	ttlMS     int64
	script    *redis.Script
}

var _ limiter.Limiter = (*RedisWindow)(nil)

func (r *Redis) SlidingWindow(rule string, cfg limiter.WindowConfig) (*RedisWindow, error) {
	return r.newWindow(rule, cfg, limiter.SlidingWindowAlgorithm, slidingWindowScript)
}

func (r *Redis) FixedWindow(rule string, cfg limiter.WindowConfig) (*RedisWindow, error) {
	return r.newWindow(rule, cfg, limiter.FixedWindowAlgorithm, fixedWindowScript)
}

func (r *Redis) newWindow(rule string, cfg limiter.WindowConfig, algorithm string, script *redis.Script) (*RedisWindow, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rule) == "" {
		return nil, errors.New("redis: rule name must not be empty")
	}
	if cfg.Window%time.Microsecond != 0 {
		return nil, errors.New("redis: window must be a whole number of microseconds")
	}
	return &RedisWindow{store: r, rule: rule, algorithm: algorithm, limit: cfg.Limit,
		windowUS: cfg.Window.Microseconds(), ttlMS: int64(math.Ceil(float64(cfg.Window)/float64(time.Millisecond))) + 1, script: script}, nil
}

func (w *RedisWindow) redisKey(key string) string {
	return w.store.redisKey(w.algorithm, w.rule, key)
}

func (w *RedisWindow) Allow(ctx context.Context, key string) (limiter.Result, error) {
	if strings.TrimSpace(key) == "" {
		return limiter.Result{}, errors.New("key must not be empty")
	}
	ctx, cancel := context.WithTimeout(ctx, w.store.opts.OperationTimeout)
	defer cancel()
	keys := []string{w.redisKey(key)}
	member := ""
	if w.algorithm == limiter.SlidingWindowAlgorithm {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return limiter.Result{}, fmt.Errorf("sliding window event ID: %w", err)
		}
		member = hex.EncodeToString(id[:])
		keys = append(keys, keys[0]+":clock")
	}
	values, err := w.script.Run(ctx, w.store.client, keys, w.limit, w.windowUS, w.ttlMS, member).Int64Slice()
	if err != nil {
		return limiter.Result{}, fmt.Errorf("redis %s: %w", w.algorithm, err)
	}
	return parseResult(values, w.limit, w.limit)
}
