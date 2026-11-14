package limiter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var ErrClosed = errors.New("limiter is closed")

// TokenBucketConfig configures a process-local token bucket. Capacity is
// Limit + Burst; Limit tokens refill over each Window. Clock must be safe for
// concurrent use. SweepInterval defaults to one minute.
type TokenBucketConfig struct {
	Limit         int
	Burst         int
	Window        time.Duration
	Clock         func() time.Time
	SweepInterval time.Duration
}

type bucket struct {
	tokens float64
	last   time.Time
}

// TokenBucket is an in-memory Limiter. Call Close to release its cleanup worker.
// Each instance has independent state; distributed enforcement requires Redis.
type TokenBucket struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	capacity int
	limit    int
	rate     float64 // tokens per nanosecond
	idleTTL  time.Duration
	clock    func() time.Time
	stop     chan struct{}
	done     chan struct{}
	closed   bool
}

var _ Limiter = (*TokenBucket)(nil)

func NewTokenBucket(cfg TokenBucketConfig) (*TokenBucket, error) {
	if cfg.Limit <= 0 || cfg.Burst < 0 || cfg.Window <= 0 || cfg.SweepInterval < 0 {
		return nil, fmt.Errorf("limit and window must be positive; burst and sweep interval must be nonnegative")
	}
	// Floating point token counts must represent every integral capacity exactly.
	const maxExact = 1 << 53
	capacity64 := uint64(cfg.Limit) + uint64(cfg.Burst)
	if capacity64 > maxExact || capacity64 > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("limit + burst must not exceed %d", maxExact)
	}
	capacity := cfg.Limit + cfg.Burst
	ttl := math.Ceil(float64(cfg.Window) * (float64(capacity) / float64(cfg.Limit)))
	if ttl >= float64(math.MaxInt64) {
		return nil, fmt.Errorf("time to refill capacity exceeds supported duration")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = time.Minute
	}
	t := &TokenBucket{
		buckets: make(map[string]*bucket), capacity: capacity, limit: cfg.Limit,
		rate: float64(cfg.Limit) / float64(cfg.Window), idleTTL: time.Duration(ttl),
		clock: cfg.Clock, stop: make(chan struct{}), done: make(chan struct{}),
	}
	go t.sweep(cfg.SweepInterval)
	return t, nil
}

func (t *TokenBucket) Allow(ctx context.Context, key string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if key == "" {
		return Result{}, errors.New("key must not be empty")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return Result{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := t.clock()
	b, ok := t.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(t.capacity), last: now}
		t.buckets[key] = b
	}
	// Never move the refill timestamp backwards if an injected clock regresses.
	if now.After(b.last) {
		b.tokens = math.Min(float64(t.capacity), b.tokens+float64(now.Sub(b.last))*t.rate)
		b.last = now
	}
	r := Result{Limit: t.limit}
	if b.tokens >= 1 {
		b.tokens--
		r.Allowed = true
	} else {
		r.RetryAfter = time.Duration(math.Ceil((1 - b.tokens) / t.rate))
	}
	r.Remaining = int(math.Floor(b.tokens))
	return r, nil
}

// evict only forgets keys idle long enough to have completely refilled.
func (t *TokenBucket) evict() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	for key, b := range t.buckets {
		if now.Sub(b.last) >= t.idleTTL {
			delete(t.buckets, key)
		}
	}
}

func (t *TokenBucket) sweep(interval time.Duration) {
	defer close(t.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.evict()
		case <-t.stop:
			return
		}
	}
}

// Close stops cleanup and rejects future checks. It is safe to call repeatedly.
func (t *TokenBucket) Close() error {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.stop)
	}
	t.mu.Unlock()
	<-t.done
	return nil
}
