package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
)

type checkRequest struct {
	Key  string `json:"key"`
	Rule string `json:"rule"`
}

type checkResponse struct {
	Allowed      bool  `json:"allowed"`
	Limit        int   `json:"limit"`
	Remaining    int   `json:"remaining"`
	RetryAfterMS int64 `json:"retry_after_ms"`
}

type settings struct {
	backend string
	ready   func(context.Context) error
}

type Option func(*settings)

// WithBackend makes readiness probe the active backend on every request.
func WithBackend(name string, ready func(context.Context) error) Option {
	return func(s *settings) { s.backend, s.ready = name, ready }
}

// New returns an HTTP handler with independent limiters for each named rule.
// The supplied limiters must be safe for concurrent calls.
func New(rules map[string]limiter.Limiter, logger *slog.Logger, options ...Option) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	cfg := settings{backend: "memory"}
	for _, option := range options {
		option(&cfg)
	}
	owned := make(map[string]limiter.Limiter, len(rules))
	for name, l := range rules {
		owned[name] = l
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /check", func(w http.ResponseWriter, r *http.Request) {
		if contentType := r.Header.Get("Content-Type"); contentType != "" {
			mediaType, _, err := mime.ParseMediaType(contentType)
			if err != nil || mediaType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		defer r.Body.Close()
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var req checkRequest
		err := d.Decode(&req)
		if err == nil {
			var extra any
			if nextErr := d.Decode(&extra); nextErr != io.EOF {
				err = nextErr
				if err == nil {
					err = errors.New("multiple JSON values")
				}
			}
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 4096 bytes")
			} else {
				writeError(w, http.StatusBadRequest, "expected one JSON object containing key and rule")
			}
			return
		}
		if strings.TrimSpace(req.Key) == "" || len(req.Key) > 512 || strings.TrimSpace(req.Rule) == "" || len(req.Rule) > 128 {
			writeError(w, http.StatusBadRequest, "key and rule are required; maximum lengths are 512 and 128 bytes")
			return
		}
		l, ok := owned[req.Rule]
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown rule")
			return
		}
		result, err := l.Allow(r.Context(), req.Key)
		if err != nil {
			logger.ErrorContext(r.Context(), "rate limit check failed", "rule", req.Rule, "error", err)
			writeError(w, http.StatusServiceUnavailable, "rate limiter unavailable")
			return
		}
		status := http.StatusOK
		if !result.Allowed {
			status = http.StatusTooManyRequests
		}
		retry := result.RetryAfter.Milliseconds()
		if result.RetryAfter > 0 && result.RetryAfter%time.Millisecond != 0 {
			retry++
		}
		writeJSON(w, status, checkResponse{result.Allowed, result.Limit, result.Remaining, retry})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if cfg.ready != nil {
			if err := cfg.ready(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "backend": cfg.backend})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "backend": cfg.backend})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		mux.ServeHTTP(rec, r)
		logger.InfoContext(r.Context(), "http request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start))
	})
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

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
