package resilience

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errBackendFailure = errors.New("backend unavailable")

func testBreaker(t *testing.T, threshold int, clock func() time.Time) *Breaker {
	t.Helper()
	b, err := NewBreaker(Options{FailureThreshold: threshold, OpenTimeout: time.Second, Clock: clock}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBreakerTransitions(t *testing.T) {
	now := time.Unix(100, 0)
	b := testBreaker(t, 2, func() time.Time { return now })
	var calls int
	failed := func(context.Context) error { calls++; return errBackendFailure }
	healthy := func(context.Context) error { calls++; return nil }
	_ = b.Do(context.Background(), failed)
	if b.State() != Closed {
		t.Fatal("opened before threshold")
	}
	_ = b.Do(context.Background(), healthy) // Resets consecutive failure streak.
	_ = b.Do(context.Background(), failed)
	if b.State() != Closed {
		t.Fatal("success did not reset failures")
	}
	_ = b.Do(context.Background(), failed)
	if b.State() != Open {
		t.Fatal("did not open at threshold")
	}
	for i := 0; i < 100; i++ {
		if err := b.Do(context.Background(), healthy); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("open circuit called backend: %v", err)
		}
	}
	if calls != 4 {
		t.Fatalf("backend called %d times during cooldown", calls)
	}
	now = now.Add(time.Second)
	_ = b.Do(context.Background(), failed)
	if b.State() != Open || calls != 5 {
		t.Fatal("failed probe did not reopen")
	}
	now = now.Add(time.Second)
	if err := b.Do(context.Background(), healthy); err != nil || b.State() != Closed {
		t.Fatalf("healthy probe did not recover: %v", err)
	}
}

func TestBreakerSingleConcurrentProbe(t *testing.T) {
	var nanos atomic.Int64
	nanos.Store(time.Unix(100, 0).UnixNano())
	b := testBreaker(t, 1, func() time.Time { return time.Unix(0, nanos.Load()) })
	_ = b.Do(context.Background(), func(context.Context) error { return errBackendFailure })
	nanos.Add(int64(time.Second))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- b.Do(context.Background(), func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	if b.State() != HalfOpen {
		t.Fatal("probe not half-open")
	}
	var called atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := b.Do(context.Background(), func(context.Context) error { called.Add(1); return nil })
			if !errors.Is(err, ErrCircuitOpen) {
				t.Errorf("concurrent probe admitted: %v", err)
			}
		}()
	}
	wg.Wait()
	if called.Load() != 0 {
		t.Fatalf("admitted %d extra probes", called.Load())
	}
	unblock()
	if err := <-done; err != nil || b.State() != Closed {
		t.Fatalf("probe recovery: %v", err)
	}
}

func TestBreakerIgnoresStaleCompletions(t *testing.T) {
	for _, lateErr := range []error{nil, errBackendFailure} {
		t.Run(fmtError(lateErr), func(t *testing.T) {
			var nanos atomic.Int64
			nanos.Store(time.Unix(100, 0).UnixNano())
			b := testBreaker(t, 1, func() time.Time { return time.Unix(0, nanos.Load()) })
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- b.Do(context.Background(), func(context.Context) error { close(entered); <-release; return lateErr })
			}()
			<-entered
			_ = b.Do(context.Background(), func(context.Context) error { return errBackendFailure })
			nanos.Add(int64(time.Second))
			_ = b.Do(context.Background(), func(context.Context) error { return nil })
			close(release)
			<-done
			if b.State() != Closed {
				t.Fatal("stale result changed recovered circuit")
			}
			// A stale failure cannot contribute to the next failure streak.
			if err := b.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fmtError(err error) string {
	if err == nil {
		return "late_success"
	}
	return "late_failure"
}

func TestBreakerCancellation(t *testing.T) {
	now := time.Unix(100, 0)
	b := testBreaker(t, 1, func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Do(ctx, func(context.Context) error { t.Fatal("called with canceled context"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.State() != Closed {
		t.Fatal("cancellation counted as failure")
	}
	_ = b.Do(context.Background(), func(context.Context) error { return errBackendFailure })
	now = now.Add(time.Second)
	ctx, cancel = context.WithCancel(context.Background())
	err := b.Do(ctx, func(context.Context) error { cancel(); return context.Canceled })
	if !errors.Is(err, context.Canceled) || b.State() != Open {
		t.Fatalf("canceled probe: %s %v", b.State(), err)
	}
	if err := b.Do(context.Background(), func(context.Context) error { return nil }); err != nil || b.State() != Closed {
		t.Fatalf("canceled probe left circuit stuck: %v", err)
	}
	// A backend's internal deadline counts even if its parent is still active.
	_ = b.Do(context.Background(), func(context.Context) error { return context.DeadlineExceeded })
	if b.State() != Open {
		t.Fatal("backend deadline did not count as failure")
	}
}

func TestBreakerOptions(t *testing.T) {
	for _, opts := range []Options{{FailureThreshold: -1}, {OpenTimeout: -1}} {
		if _, err := NewBreaker(opts, nil); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	b, err := NewBreaker(Options{}, nil)
	if err != nil || b.opts.FailureThreshold != 5 || b.opts.OpenTimeout != 5*time.Second {
		t.Fatalf("defaults: %+v %v", b, err)
	}
}

func TestOldSuccessCannotFinishActiveProbe(t *testing.T) {
	var nanos atomic.Int64
	nanos.Store(time.Unix(100, 0).UnixNano())
	b := testBreaker(t, 1, func() time.Time { return time.Unix(0, nanos.Load()) })
	oldEntered, oldRelease := make(chan struct{}), make(chan struct{})
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- b.Do(context.Background(), func(context.Context) error { close(oldEntered); <-oldRelease; return nil })
	}()
	<-oldEntered
	_ = b.Do(context.Background(), func(context.Context) error { return errBackendFailure })
	nanos.Add(int64(time.Second))
	probeEntered, probeRelease := make(chan struct{}), make(chan struct{})
	probeDone := make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(probeRelease) }) }
	defer unblock()
	go func() {
		probeDone <- b.Do(context.Background(), func(context.Context) error { close(probeEntered); <-probeRelease; return errBackendFailure })
	}()
	<-probeEntered
	close(oldRelease)
	<-oldDone
	if b.State() != HalfOpen {
		t.Fatal("old success closed an active recovery probe")
	}
	unblock()
	<-probeDone
	if b.State() != Open {
		t.Fatal("failed recovery probe did not reopen circuit")
	}
}
