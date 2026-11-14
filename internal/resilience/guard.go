package resilience

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/ratelimiter/ratelimiter/internal/requestid"
)

const (
	FailOpen   = "fail_open"
	FailClosed = "fail_closed"
)

type Guard struct {
	next    limiter.Limiter
	breaker *Breaker
	policy  string
	limit   int
	rule    string
	logger  *slog.Logger
}

var _ limiter.Limiter = (*Guard)(nil)

func NewGuard(next limiter.Limiter, breaker *Breaker, policy string, limit int, rule string, logger *slog.Logger) (*Guard, error) {
	if next == nil || breaker == nil || limit <= 0 || (policy != FailOpen && policy != FailClosed) {
		return nil, errors.New("failure guard requires a limiter, breaker, positive limit, and valid failure policy")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Guard{next: next, breaker: breaker, policy: policy, limit: limit, rule: rule, logger: logger}, nil
}

func (g *Guard) Allow(ctx context.Context, key string) (limiter.Result, error) {
	if err := ctx.Err(); err != nil {
		return limiter.Result{}, err
	}
	if strings.TrimSpace(key) == "" {
		return limiter.Result{}, errors.New("key must not be empty")
	}
	var result limiter.Result
	err := g.breaker.Do(ctx, func(ctx context.Context) error {
		var err error
		result, err = g.next.Allow(ctx, key)
		return err
	})
	if err == nil {
		return result, nil
	}
	// Caller cancellation must never turn into an allowed fallback decision.
	if parentErr := ctx.Err(); parentErr != nil {
		return limiter.Result{}, parentErr
	}
	if !errors.Is(err, ErrCircuitOpen) {
		g.logger.WarnContext(ctx, "redis check failed; applying failure policy", "request_id", requestid.FromContext(ctx), "rule", g.rule, "policy", g.policy, "error", err)
	}
	if g.policy == FailOpen {
		return limiter.Result{Allowed: true, Limit: g.limit, Remaining: 0, Degraded: true}, nil
	}
	return limiter.Result{}, err
}
