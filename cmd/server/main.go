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

	"github.com/luvkumar404/rate-limiter/internal/api"
	"github.com/luvkumar404/rate-limiter/internal/config"
	"github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/luvkumar404/rate-limiter/internal/metrics"
	"github.com/luvkumar404/rate-limiter/internal/resilience"
	"github.com/luvkumar404/rate-limiter/internal/store"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	rules := flag.String("config", "rules.yaml", "YAML rules file")
	healthcheck := flag.String("healthcheck", "", "probe an HTTP health URL and exit (for shell-free containers)")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *healthcheck != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := probe(ctx, *healthcheck); err != nil {
			logger.Error("healthcheck failed", "error", err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *addr, *rules, logger); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func probe(ctx context.Context, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
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
	var breaker *resilience.Breaker
	var ready func(context.Context) error
	if cfg.Backend == "redis" {
		breaker, err = resilience.NewBreaker(cfg.CircuitBreaker, logger)
		if err != nil {
			return err
		}
	}
	observation := metrics.New(func() resilience.State {
		if breaker == nil {
			return resilience.Closed
		}
		return breaker.State()
	})
	if cfg.Backend == "redis" {
		if password, ok := os.LookupEnv("REDIS_PASSWORD"); ok {
			cfg.Redis.Password = password
		}
		redisStore, err = store.NewRedis(cfg.Redis)
		if err != nil {
			return err
		}
		buckets = append(buckets, redisStore)
		if err := observation.RedisOperation(ctx, redisStore.Ping); err != nil {
			logger.Warn("redis unavailable at startup; serving with failure policy", "policy", cfg.FailurePolicy, "error", err)
		}
		ready = func(ctx context.Context) error { return observation.RedisOperation(ctx, redisStore.Ping) }
	}
	for name, rule := range cfg.Rules {
		l, err := store.NewLimiter(name, rule.RuleConfig, redisStore)
		if err != nil {
			return fmt.Errorf("rule %q: %w", name, err)
		}
		if closer, ok := l.(io.Closer); ok {
			buckets = append(buckets, closer)
		}
		if breaker != nil {
			l = observation.WrapRedis(l)
			l, err = resilience.NewGuard(l, breaker, cfg.FailurePolicy, rule.Limit, name, logger)
			if err != nil {
				return err
			}
		}
		rules[name] = l
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("server listening", "address", listener.Addr().String(), "backend", cfg.Backend, "rules", len(rules))
	return serve(ctx, listener, api.New(rules, logger, api.WithBackend(cfg.Backend, ready), api.WithMetrics(observation)), logger)
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
