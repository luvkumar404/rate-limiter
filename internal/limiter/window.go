package limiter

import (
	"context"
	"errors"
	"math/bits"
	"sort"
	"sync"
	"time"
)

type WindowConfig struct {
	Limit         int
	Window        time.Duration
	Clock         func() time.Time
	SweepInterval time.Duration
}

func (c WindowConfig) Validate() error {
	return (TokenBucketConfig{Limit: c.Limit, Window: c.Window, SweepInterval: c.SweepInterval}).Validate()
}

type windowState struct {
	events []time.Time
	start  time.Time
	last   time.Time
	count  int
}

// WindowLimiter implements an exact sliding log or an epoch-aligned fixed
// window in memory. Close releases its idle-key cleanup worker.
type WindowLimiter struct {
	mu        sync.Mutex
	states    map[string]*windowState
	cfg       WindowConfig
	algorithm string
	closed    bool
	stop      chan struct{}
	done      chan struct{}
}

var _ Limiter = (*WindowLimiter)(nil)

func NewSlidingWindow(cfg WindowConfig) (*WindowLimiter, error) {
	return newWindow(cfg, SlidingWindowAlgorithm)
}

func NewFixedWindow(cfg WindowConfig) (*WindowLimiter, error) {
	return newWindow(cfg, FixedWindowAlgorithm)
}

func newWindow(cfg WindowConfig, algorithm string) (*WindowLimiter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = time.Minute
	}
	w := &WindowLimiter{states: make(map[string]*windowState), cfg: cfg, algorithm: algorithm, stop: make(chan struct{}), done: make(chan struct{})}
	go w.sweep()
	return w, nil
}

func (w *WindowLimiter) Allow(ctx context.Context, key string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if key == "" {
		return Result{}, errors.New("key must not be empty")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return Result{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := w.cfg.Clock()
	if w.algorithm == FixedWindowAlgorithm {
		// Epoch alignment is a wall-clock operation. Do not compare monotonic
		// anchors derived from separate samples of time.Now.
		now = now.Round(0)
	}
	s, ok := w.states[key]
	if !ok {
		s = &windowState{last: now}
		w.states[key] = s
	}
	if now.Before(s.last) {
		now = s.last
	}
	s.last = now
	r := Result{Limit: w.cfg.Limit}
	if w.algorithm == SlidingWindowAlgorithm {
		cutoff := now.Add(-w.cfg.Window)
		first := sort.Search(len(s.events), func(i int) bool { return s.events[i].After(cutoff) })
		if first > 0 {
			copy(s.events, s.events[first:])
			s.events = s.events[:len(s.events)-first]
		}
		if len(s.events) < w.cfg.Limit {
			s.events = append(s.events, now)
			r.Allowed = true
		} else {
			r.RetryAfter = s.events[0].Add(w.cfg.Window).Sub(now)
		}
		r.Remaining = w.cfg.Limit - len(s.events)
	} else {
		start := fixedStart(now, w.cfg.Window)
		if !s.start.Equal(start) {
			s.start, s.count = start, 0
		}
		if s.count < w.cfg.Limit {
			s.count++
			r.Allowed = true
		} else {
			r.RetryAfter = s.start.Add(w.cfg.Window).Sub(now)
		}
		r.Remaining = w.cfg.Limit - s.count
	}
	return r, nil
}

// fixedStart aligns to the Unix epoch without overflowing UnixNano for dates
// outside 1678-2262. Mul64/Div64 compute the timestamp remainder exactly.
func fixedStart(now time.Time, window time.Duration) time.Time {
	m := uint64(window)
	sec := now.Unix()
	negative := sec < 0
	var abs uint64
	if negative {
		abs = uint64(-(sec + 1)) + 1
	} else {
		abs = uint64(sec)
	}
	hi, lo := bits.Mul64(abs%m, uint64(time.Second))
	_, rem := bits.Div64(hi, lo, m)
	if negative && rem != 0 {
		rem = m - rem
	}
	rem = (rem + uint64(now.Nanosecond())) % m
	return now.Round(0).Add(-time.Duration(rem))
}

func (w *WindowLimiter) evict() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.cfg.Clock()
	for key, s := range w.states {
		if now.Sub(s.last) >= w.cfg.Window {
			delete(w.states, key)
		}
	}
}

func (w *WindowLimiter) sweep() {
	defer close(w.done)
	ticker := time.NewTicker(w.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.evict()
		case <-w.stop:
			return
		}
	}
}

func (w *WindowLimiter) Close() error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.stop)
	}
	w.mu.Unlock()
	<-w.done
	return nil
}
