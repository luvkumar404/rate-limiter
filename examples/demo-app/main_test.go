package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/api"
	core "github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/ratelimiter/ratelimiter/pkg/limiter"
	"github.com/ratelimiter/ratelimiter/pkg/middleware"
)

type listeningHandler struct {
	slog.Handler
	addresses chan string
}

func (h listeningHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "demo listening" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "address" {
				h.addresses <- a.Value.String()
			}
			return true
		})
	}
	return h.Handler.Handle(ctx, r)
}

func TestDemoModes(t *testing.T) {
	for _, mode := range []string{"embedded", "remote"} {
		t.Run(mode, func(t *testing.T) {
			baseURL := ""
			allowed := 5
			if mode == "remote" {
				bucket, err := limiter.NewTokenBucket(limiter.TokenBucketConfig{Limit: 2, Window: 24 * time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				defer bucket.Close()
				service := httptest.NewServer(api.New(map[string]core.Limiter{"api": bucket}, slog.New(slog.NewJSONHandler(io.Discard, nil))))
				defer service.Close()
				baseURL = service.URL
				allowed = 2
			}
			addresses := make(chan string, 1)
			logger := slog.New(listeningHandler{Handler: slog.NewJSONHandler(io.Discard, nil), addresses: addresses})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- run(ctx, "127.0.0.1:0", baseURL, "api", middleware.FailClosed, "", logger) }()
			var address string
			select {
			case address = <-addresses:
			case <-time.After(3 * time.Second):
				t.Fatal("demo did not listen")
			}
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			for i := 0; i <= allowed; i++ {
				resp, err := client.Get("http://" + address + "/hello")
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				want := 200
				if i == allowed {
					want = 429
				}
				if resp.StatusCode != want {
					t.Fatalf("%s request %d: got %d want %d", mode, i, resp.StatusCode, want)
				}
				if resp.Header.Get("X-Request-ID") == "" || resp.Header.Get("X-RateLimit-Limit") == "" {
					t.Fatal("demo lacks correlation or quota headers")
				}
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("demo did not stop")
			}
		})
	}
}
