// Package resilience applies failure policies and limits calls to failing stores.
package resilience

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/requestid"
)

type State string

const (
	Closed   State = "closed"
	Open     State = "open"
	HalfOpen State = "half_open"
)

var ErrCircuitOpen = errors.New("redis circuit is open")

type Options struct {
	FailureThreshold int              `yaml:"failure_threshold"`
	OpenTimeout      time.Duration    `yaml:"open_timeout"`
	Clock            func() time.Time `yaml:"-"`
}

func (o Options) WithDefaults() (Options, error) {
	if o.FailureThreshold < 0 || o.OpenTimeout < 0 {
		return o, errors.New("circuit_breaker: failure_threshold and open_timeout must be nonnegative")
	}
	if o.FailureThreshold == 0 {
		o.FailureThreshold = 5
	}
	if o.OpenTimeout == 0 {
		o.OpenTimeout = 5 * time.Second
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o, nil
}

// Breaker is shared by all rules that use a single Redis pool. Only one caller
// can probe an open circuit after cooldown. State needs no background goroutine.
type Breaker struct {
	mu         sync.Mutex
	opts       Options
	logger     *slog.Logger
	state      State
	failures   int
	generation uint64
	until      time.Time
}

func NewBreaker(opts Options, logger *slog.Logger) (*Breaker, error) {
	opts, err := opts.WithDefaults()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Breaker{opts: opts, logger: logger, state: Closed}, nil
}

func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Breaker) changeLocked(ctx context.Context, next State) {
	prior := b.state
	b.state = next
	b.generation++
	if prior != next {
		b.logger.InfoContext(ctx, "redis circuit state changed", "request_id", requestid.FromContext(ctx), "from", prior, "to", next)
	}
}

func (b *Breaker) begin(ctx context.Context) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Open:
		if b.opts.Clock().Before(b.until) {
			return 0, ErrCircuitOpen
		}
		b.changeLocked(ctx, HalfOpen)
	case HalfOpen:
		return 0, ErrCircuitOpen
	}
	return b.generation, nil
}

func (b *Breaker) finish(ctx context.Context, generation uint64, err error, canceled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A late result from before an open/probe transition must not change the
	// current circuit. This includes late successes while Redis is failing.
	if generation != b.generation {
		return
	}
	if canceled {
		if b.state == HalfOpen {
			b.until = b.opts.Clock() // Release the canceled probe without a cooldown.
			b.changeLocked(ctx, Open)
		}
		return
	}
	if err == nil {
		b.failures = 0
		if b.state == HalfOpen {
			b.changeLocked(ctx, Closed)
		}
		return
	}
	b.failures++
	if b.state == HalfOpen || b.failures >= b.opts.FailureThreshold {
		b.until = b.opts.Clock().Add(b.opts.OpenTimeout)
		b.changeLocked(ctx, Open)
	}
}

// Do counts backend errors, but ignores cancellation of the caller's context.
// Deadlines imposed inside the backend still count as backend failures.
func (b *Breaker) Do(ctx context.Context, call func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	generation, err := b.begin(ctx)
	if err != nil {
		return err
	}
	err = call(ctx)
	if parentErr := ctx.Err(); parentErr != nil {
		b.finish(ctx, generation, parentErr, true)
		return parentErr
	}
	b.finish(ctx, generation, err, false)
	return err
}
