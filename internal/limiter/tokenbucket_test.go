package limiter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestBucket(t testing.TB, cfg TokenBucketConfig) *TokenBucket {
	t.Helper()
	b, err := NewTokenBucket(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestTokenBucketDecisions(t *testing.T) {
	now := time.Unix(100, 0)
	b := newTestBucket(t, TokenBucketConfig{Limit: 2, Burst: 1, Window: time.Second, Clock: func() time.Time { return now }})
	for _, tc := range []struct {
		name      string
		advance   time.Duration
		key       string
		allowed   bool
		remaining int
		retry     time.Duration
	}{
		{"initial capacity", 0, "a", true, 2, 0},
		{"second token", 0, "a", true, 1, 0},
		{"burst token", 0, "a", true, 0, 0},
		{"exhausted", 0, "a", false, 0, 500 * time.Millisecond},
		{"partial refill", 250 * time.Millisecond, "a", false, 0, 250 * time.Millisecond},
		{"steady refill", 250 * time.Millisecond, "a", true, 0, 0},
		{"independent key", 0, "b", true, 2, 0},
		{"refill capped", 10 * time.Second, "a", true, 2, 0},
		{"clock regression", -time.Second, "a", true, 1, 0},
		{"clock returns", time.Second, "a", true, 0, 0},
		{"no duplicate refill", 0, "a", false, 0, 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now = now.Add(tc.advance)
			r, err := b.Allow(context.Background(), tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if r.Allowed != tc.allowed || r.Limit != 2 || r.Remaining != tc.remaining || r.RetryAfter != tc.retry {
				t.Fatalf("got %+v; want allowed=%v remaining=%d retry=%v", r, tc.allowed, tc.remaining, tc.retry)
			}
		})
	}
}

func TestTokenBucketValidation(t *testing.T) {
	for _, cfg := range []TokenBucketConfig{
		{}, {Limit: -1, Window: time.Second}, {Limit: 1},
		{Limit: 1, Window: -time.Second}, {Limit: 1, Window: time.Second, Burst: -1},
		{Limit: 1, Window: time.Second, SweepInterval: -1},
		{Limit: 1, Window: time.Duration(1<<63 - 1), Burst: 1},
	} {
		if b, err := NewTokenBucket(cfg); err == nil {
			_ = b.Close()
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
}

func TestTokenBucketConcurrent(t *testing.T) {
	b := newTestBucket(t, TokenBucketConfig{Limit: 100, Burst: 20, Window: time.Minute, Clock: func() time.Time { return time.Unix(100, 0) }})
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := b.Allow(context.Background(), "shared")
			if err != nil {
				t.Error(err)
			}
			if r.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 120 {
		t.Fatalf("allowed %d; want exactly 120", got)
	}
}

func TestTokenBucketEviction(t *testing.T) {
	now := time.Unix(100, 0)
	b := newTestBucket(t, TokenBucketConfig{Limit: 1, Burst: 1, Window: time.Second, Clock: func() time.Time { return now }})
	_, _ = b.Allow(context.Background(), "a")
	_, _ = b.Allow(context.Background(), "a")
	now = now.Add(time.Second)
	b.evict()
	if len(b.buckets) != 1 {
		t.Fatal("evicted before full capacity could refill")
	}
	now = now.Add(time.Second)
	b.evict()
	if len(b.buckets) != 0 {
		t.Fatal("idle key was not evicted")
	}
	r, _ := b.Allow(context.Background(), "a")
	if !r.Allowed || r.Remaining != 1 {
		t.Fatalf("evicted key did not start full: %+v", r)
	}
}

func TestTokenBucketLifecycle(t *testing.T) {
	b := newTestBucket(t, TokenBucketConfig{Limit: 1, Window: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Allow(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context; got %v", err)
	}
	if _, err := b.Allow(context.Background(), ""); err == nil {
		t.Fatal("empty key accepted")
	}
	r, _ := b.Allow(context.Background(), "a")
	if !r.Allowed {
		t.Fatal("canceled call consumed capacity")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = b.Close() }()
	}
	wg.Wait()
	if _, err := b.Allow(context.Background(), "a"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed; got %v", err)
	}
}

func TestTokenBucketBackgroundEviction(t *testing.T) {
	var nanos atomic.Int64
	nanos.Store(time.Unix(100, 0).UnixNano())
	b := newTestBucket(t, TokenBucketConfig{
		Limit: 1, Window: time.Second, SweepInterval: time.Millisecond,
		Clock: func() time.Time { return time.Unix(0, nanos.Load()) },
	})
	_, _ = b.Allow(context.Background(), "idle")
	nanos.Add(int64(2 * time.Second))
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		b.mu.Lock()
		count := len(b.buckets)
		b.mu.Unlock()
		if count == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("background worker did not evict idle key")
		case <-poll.C:
		}
	}
}

func BenchmarkTokenBucket(b *testing.B) {
	tb := newTestBucket(b, TokenBucketConfig{Limit: 1000000, Window: time.Second})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = tb.Allow(ctx, "shared")
		}
	})
}
