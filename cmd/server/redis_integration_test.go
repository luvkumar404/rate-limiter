//go:build integration

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/redis/go-redis/v9"
)

func startRedisInstance(t *testing.T, path string) string {
	t.Helper()
	addresses := make(chan string, 1)
	logger := slog.New(addressHandler{Handler: slog.NewJSONHandler(io.Discard, nil), address: addresses})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, "127.0.0.1:0", path, logger) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server shutdown timed out")
		}
	})
	select {
	case addr := <-addresses:
		return "http://" + addr
	case <-time.After(6 * time.Second):
		t.Fatal("Redis-backed server did not start; check the Redis integration endpoint")
		return ""
	}
}

func TestRedisTwoHTTPInstancesShareLimit(t *testing.T) {
	for _, tc := range []struct {
		algorithm      string
		burst, allowed int
	}{
		{limiter.TokenBucketAlgorithm, 20, 120},
		{limiter.SlidingWindowAlgorithm, 0, 100},
		{limiter.FixedWindowAlgorithm, 0, 100},
	} {
		t.Run(tc.algorithm, func(t *testing.T) { testRedisTwoInstances(t, tc.algorithm, tc.burst, tc.allowed) })
	}
}

func testRedisTwoInstances(t *testing.T, algorithm string, burst, wantAllowed int) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	prefix := fmt.Sprintf("ratelimiter-http-test:%d", time.Now().UnixNano())
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	t.Cleanup(func() {
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		iter := client.Scan(ctx, 0, prefix+":*", 100).Iterator()
		for iter.Next(ctx) {
			_ = client.Del(ctx, iter.Val()).Err()
		}
	})
	path := filepath.Join(t.TempDir(), "rules.yaml")
	data := fmt.Sprintf("backend: redis\nredis:\n  addr: %q\n  prefix: %q\n  operation_timeout: 5s\nrules:\n  api:\n    algorithm: %s\n    limit: 100\n    burst: %d\n    window: 24h\n", addr, prefix, algorithm, burst)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	servers := []string{startRedisInstance(t, path), startRedisInstance(t, path)}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	defer httpClient.CloseIdleConnections()
	for _, server := range servers {
		resp, err := httpClient.Get(server + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || !strings.Contains(string(body), `"backend":"redis"`) {
			t.Fatalf("readiness: %d %s %v", resp.StatusCode, body, err)
		}
	}
	jobs := make(chan int, 1000)
	for i := 0; i < 1000; i++ {
		jobs <- i
	}
	close(jobs)
	var allowed, blocked, failures atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				resp, err := httpClient.Post(servers[n%2]+"/check", "application/json", strings.NewReader(`{"key":"shared-user","rule":"api"}`))
				if err != nil {
					failures.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusOK:
					allowed.Add(1)
				case http.StatusTooManyRequests:
					blocked.Add(1)
				default:
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != int64(wantAllowed) || blocked.Load() != int64(1000-wantAllowed) || failures.Load() != 0 {
		t.Fatalf("across two HTTP instances: allowed=%d blocked=%d failures=%d", allowed.Load(), blocked.Load(), failures.Load())
	}
	t.Logf("1,000 HTTP checks across two server instances: %d allowed, %d blocked, zero errors", wantAllowed, 1000-wantAllowed)
}
