package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/api"
	"github.com/ratelimiter/ratelimiter/internal/config"
	"github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/ratelimiter/ratelimiter/internal/store"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	rules := flag.String("config", "rules.yaml", "YAML rules file")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *addr, *rules, logger); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr, path string, logger *slog.Logger) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	rules := make(map[string]limiter.Limiter, len(cfg.Rules))
	var buckets []io.Closer
	defer func() {
		for _, b := range buckets {
			_ = b.Close()
		}
	}()
	var redisStore *store.Redis
	var ready func(context.Context) error
	if cfg.Backend == "redis" {
		if password, ok := os.LookupEnv("REDIS_PASSWORD"); ok {
			cfg.Redis.Password = password
		}
		redisStore, err = store.NewRedis(cfg.Redis)
		if err != nil {
			return err
		}
		buckets = append(buckets, redisStore)
		if err := redisStore.Ping(ctx); err != nil {
			return fmt.Errorf("redis startup ping: %w", err)
		}
		ready = redisStore.Ping
	}
	for name, rule := range cfg.Rules {
		l, err := store.NewLimiter(name, rule.RuleConfig, redisStore)
		if err != nil {
			return fmt.Errorf("rule %q: %w", name, err)
		}
		if closer, ok := l.(io.Closer); ok {
			buckets = append(buckets, closer)
		}
		rules[name] = l
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("server listening", "address", listener.Addr().String(), "backend", cfg.Backend, "rules", len(rules))
	return serve(ctx, listener, api.New(rules, logger, api.WithBackend(cfg.Backend, ready)), logger)
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger) error {
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		logger.Info("server stopped")
		return nil
	}
}
