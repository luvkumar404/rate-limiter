package limiter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowBoundaries(t *testing.T) {
	for _, algorithm := range []string{SlidingWindowAlgorithm, FixedWindowAlgorithm} {
		t.Run(algorithm, func(t *testing.T) {
			now := time.Unix(10, 0)
			cfg := WindowConfig{Limit: 2, Window: 10 * time.Second, Clock: func() time.Time { return now }}
			w, err := newWindow(cfg, algorithm)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			check := func(allowed bool, remaining int, retry time.Duration) {
				t.Helper()
				r, err := w.Allow(context.Background(), "a")
				if err != nil || r.Allowed != allowed || r.Remaining != remaining || r.RetryAfter != retry {
					t.Fatalf("at %s: %+v, %v; want %v %d %s", now, r, err, allowed, remaining, retry)
				}
			}
			check(true, 1, 0)
			now = now.Add(4 * time.Second)
			check(true, 0, 0)
			now = now.Add(time.Second)
			check(false, 0, 5*time.Second)
			// At the exact boundary the oldest event leaves the sliding window.
			now = time.Unix(20, 0)
			if algorithm == SlidingWindowAlgorithm {
				check(true, 0, 0)
				check(false, 0, 4*time.Second)
				now = time.Unix(24, 0)
				check(true, 0, 0)
			} else {
				check(true, 1, 0)
				check(true, 0, 0)
				check(false, 0, 10*time.Second)
			}
		})
	}
}

func TestWindowLifecycleAndEviction(t *testing.T) {
	for _, algorithm := range []string{SlidingWindowAlgorithm, FixedWindowAlgorithm} {
		t.Run(algorithm, func(t *testing.T) {
			now := time.Unix(10, 0)
			w, err := newWindow(WindowConfig{Limit: 1, Window: 10 * time.Second, Clock: func() time.Time { return now }}, algorithm)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := w.Allow(ctx, "a"); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if _, err := w.Allow(context.Background(), ""); err == nil {
				t.Fatal("empty key accepted")
			}
			first, _ := w.Allow(context.Background(), "a")
			if !first.Allowed {
				t.Fatal("canceled call consumed quota")
			}
			now = time.Unix(9, 0)
			if r, _ := w.Allow(context.Background(), "a"); r.Allowed || r.RetryAfter != 10*time.Second {
				t.Fatalf("clock regression changed quota: %+v", r)
			}
			now = time.Unix(19, 0)
			w.evict()
			if len(w.states) != 1 {
				t.Fatal("evicted active state early")
			}
			now = time.Unix(20, 0)
			w.evict()
			if len(w.states) != 0 {
				t.Fatal("idle state not evicted")
			}
			if r, _ := w.Allow(context.Background(), "a"); !r.Allowed {
				t.Fatal("evicted state did not reset")
			}
			_ = w.Close()
			_ = w.Close()
			if _, err := w.Allow(context.Background(), "a"); !errors.Is(err, ErrClosed) {
				t.Fatalf("closed limiter returned %v", err)
			}
		})
	}
}

func TestFixedWindowAlignment(t *testing.T) {
	for _, tc := range []struct {
		now, want time.Time
		window    time.Duration
	}{
		{time.Unix(13, 7), time.Unix(10, 0), 10 * time.Second},
		{time.Unix(10, 0), time.Unix(7, 0), 7 * time.Second},
		{time.Unix(-1, 500000000), time.Unix(-1, 0), time.Second},
		{time.Date(2500, 1, 1, 0, 0, 13, 7, time.UTC), time.Date(2500, 1, 1, 0, 0, 10, 0, time.UTC), 10 * time.Second},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Unix(0, 0), 200 * 365 * 24 * time.Hour},
	} {
		if got := fixedStart(tc.now, tc.window); !got.Equal(tc.want) {
			t.Fatalf("alignment(%s,%s)=%s want %s", tc.now, tc.window, got, tc.want)
		}
	}
}

func TestWindowBackgroundEviction(t *testing.T) {
	for _, algorithm := range []string{SlidingWindowAlgorithm, FixedWindowAlgorithm} {
		t.Run(algorithm, func(t *testing.T) {
			var nanos atomic.Int64
			nanos.Store(time.Unix(100, 0).UnixNano())
			w, err := newWindow(WindowConfig{Limit: 1, Window: time.Second, SweepInterval: time.Millisecond, Clock: func() time.Time { return time.Unix(0, nanos.Load()) }}, algorithm)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			_, _ = w.Allow(context.Background(), "idle")
			nanos.Add(int64(2 * time.Second))
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				w.mu.Lock()
				count := len(w.states)
				w.mu.Unlock()
				if count == 0 {
					return
				}
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("background worker did not evict idle state")
				}
			}
		})
	}
}
