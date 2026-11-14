// Command demo-app protects /hello with an embedded or remote limiter.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ratelimiter/ratelimiter/pkg/limiter"
	"github.com/ratelimiter/ratelimiter/pkg/middleware"
)

func main() {
	addr := flag.String("addr", ":8083", "demo listen address")
	limiterURL := flag.String("limiter-url", "", "remote limiter base URL; empty selects an embedded bucket")
	rule := flag.String("rule", "login", "remote rule name")
	policy := flag.String("failure-policy", middleware.FailClosed, "fail_open or fail_closed")
	header := flag.String("key-header", "", "use this header instead of the socket client IP")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *addr, *limiterURL, *rule, *policy, *header, logger); err != nil {
		logger.Error("demo failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr, baseURL, rule, policy, header string, logger *slog.Logger) error {
	var l limiter.Limiter
	if baseURL == "" {
		bucket, err := limiter.NewTokenBucket(limiter.TokenBucketConfig{Limit: 5, Window: 10 * time.Second})
		if err != nil {
			return err
		}
		defer bucket.Close()
		l = bucket
	} else {
		remote, err := middleware.NewRemote(baseURL, rule, middleware.RemoteOptions{})
		if err != nil {
			return err
		}
		l = remote
	}
	key := middleware.ClientIP()
	if header != "" {
		key = middleware.Header(header)
	}
	protect, err := middleware.New(l, middleware.Options{Key: key, FailurePolicy: policy, Logger: logger})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /hello", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, _ := middleware.ResultFromContext(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "hello", "request_id": middleware.RequestID(r.Context()), "degraded": result.Degraded})
	})))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	logger.Info("demo listening", "address", listener.Addr().String(), "remote", baseURL != "", "route", "/hello")
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
