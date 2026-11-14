package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/requestid"
	"github.com/ratelimiter/ratelimiter/pkg/limiter"
)

type Result = limiter.Result
type Limiter = limiter.Limiter

const (
	FailOpen   = "fail_open"
	FailClosed = "fail_closed"
)

type Options struct {
	// Key defaults to ClientIP. Custom extractors can use authenticated context.
	Key KeyFunc
	// FailurePolicy defaults to fail_closed, unlike the service's fail_open default.
	FailurePolicy string
	// Timeout bounds Allow, not the downstream handler. Zero selects 100ms.
	Timeout time.Duration
	Logger  *slog.Logger
}

type resultKey struct{}

// ResultFromContext returns the decision passed to a downstream handler.
func ResultFromContext(ctx context.Context) (Result, bool) {
	result, ok := ctx.Value(resultKey{}).(Result)
	return result, ok
}
func RequestID(ctx context.Context) string { return requestid.FromContext(ctx) }

// New returns a standard net/http middleware. It never closes the caller's
// limiter or modifies the request body. The returned function requires a handler.
func New(l Limiter, opts Options) (func(http.Handler) http.Handler, error) {
	if l == nil {
		return nil, errors.New("middleware: limiter is required")
	}
	if opts.Timeout < 0 {
		return nil, errors.New("middleware: timeout must be nonnegative")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 100 * time.Millisecond
	}
	if opts.FailurePolicy == "" {
		opts.FailurePolicy = FailClosed
	}
	if opts.FailurePolicy != FailOpen && opts.FailurePolicy != FailClosed {
		return nil, errors.New("middleware: failure policy must be fail_open or fail_closed")
	}
	if opts.Key == nil {
		opts.Key = ClientIP()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if len(r.Header.Values("X-Request-ID")) != 1 || !requestid.Valid(id) {
				id = requestid.New()
			}
			r = r.WithContext(requestid.WithContext(r.Context(), id))
			w.Header().Set("X-Request-ID", id)
			if r.Context().Err() != nil {
				writeError(w, http.StatusServiceUnavailable, "request canceled")
				return
			}
			key, err := opts.Key(r)
			if err != nil || strings.TrimSpace(key) == "" || len(key) > 512 {
				writeError(w, http.StatusBadRequest, "unable to determine a rate limit key (maximum 512 bytes)")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), opts.Timeout)
			result, err := l.Allow(ctx, key)
			cancel()
			if r.Context().Err() != nil {
				writeError(w, http.StatusServiceUnavailable, "request canceled")
				return
			}
			if err != nil {
				opts.Logger.WarnContext(r.Context(), "middleware rate limit check failed", "request_id", id, "policy", opts.FailurePolicy, "error", err)
				if opts.FailurePolicy == FailClosed {
					writeError(w, http.StatusServiceUnavailable, "rate limiter unavailable")
					return
				}
				result = Result{Allowed: true, Degraded: true} // Limit and remaining are unknown.
			}
			if result.Limit > 0 {
				w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
			}
			if result.Degraded {
				w.Header().Set("X-RateLimit-Degraded", "true")
			}
			if !result.Allowed {
				if result.RetryAfter > 0 {
					w.Header().Set("Retry-After", strconv.FormatInt(ceil(result.RetryAfter, time.Second), 10))
				}
				writeJSON(w, http.StatusTooManyRequests, struct {
					Allowed      bool  `json:"allowed"`
					Limit        int   `json:"limit"`
					Remaining    int   `json:"remaining"`
					RetryAfterMS int64 `json:"retry_after_ms"`
				}{false, result.Limit, result.Remaining, ceil(result.RetryAfter, time.Millisecond)})
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), resultKey{}, result))
			next.ServeHTTP(w, r)
		})
	}, nil
}

func ceil(d, unit time.Duration) int64 {
	n := int64(d / unit)
	if d > 0 && d%unit != 0 {
		n++
	}
	return n
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
