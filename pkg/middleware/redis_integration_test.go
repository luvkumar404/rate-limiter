//go:build integration

package middleware_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/api"
	core "github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/ratelimiter/ratelimiter/pkg/limiter"
	"github.com/ratelimiter/ratelimiter/pkg/middleware"
	"github.com/redis/go-redis/v9"
)

func TestMiddlewareRedisBackends(t *testing.T) {
	for _, mode := range []string{"embedded", "remote"} {
		for _, algorithm := range []string{limiter.TokenBucketAlgorithm, limiter.SlidingWindowAlgorithm, limiter.FixedWindowAlgorithm} {
			t.Run(mode+"/"+algorithm, func(t *testing.T) {
				addr := os.Getenv("REDIS_TEST_ADDR")
				if addr == "" {
					addr = "localhost:6379"
				}
				prefix := fmt.Sprintf("ratelimiter-middleware-test:%s:%d", t.Name(), time.Now().UnixNano())
				cleanup := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
				t.Cleanup(func() {
					defer cleanup.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					iter := cleanup.Scan(ctx, 0, prefix+":*", 100).Iterator()
					for iter.Next(ctx) {
						_ = cleanup.Del(ctx, iter.Val()).Err()
					}
				})
				var apps []http.Handler
				var hits atomic.Int64
				for i := 0; i < 2; i++ {
					store, err := limiter.NewRedis(limiter.RedisOptions{Addr: addr, Prefix: prefix, OperationTimeout: 5 * time.Second})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = store.Close() })
					if err := store.Ping(context.Background()); err != nil {
						t.Fatalf("integration tests require Redis: %v", err)
					}
					rule := limiter.RuleConfig{Algorithm: algorithm, Limit: 20, Window: 24 * time.Hour}
					if algorithm == limiter.TokenBucketAlgorithm {
						rule.Burst = 5
					}
					base, err := limiter.New("api", rule, store)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "remote" {
						service := httptest.NewServer(api.New(map[string]core.Limiter{"api": base}, quiet()))
						t.Cleanup(service.Close)
						base, err = middleware.NewRemote(service.URL, "api", middleware.RemoteOptions{Timeout: 5 * time.Second})
						if err != nil {
							t.Fatal(err)
						}
					}
					protect, err := middleware.New(base, middleware.Options{Timeout: 5 * time.Second, Key: middleware.Header("X-API-Key"), Logger: quiet()})
					if err != nil {
						t.Fatal(err)
					}
					apps = append(apps, protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(204) })))
				}
				jobs := make(chan int, 200)
				for i := 0; i < 200; i++ {
					jobs <- i
				}
				close(jobs)
				var blocked, failures atomic.Int64
				var wg sync.WaitGroup
				for i := 0; i < 16; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for n := range jobs {
							r := httptest.NewRequest("GET", "/orders", nil)
							r.Header.Set("X-API-Key", "shared-user")
							w := httptest.NewRecorder()
							apps[n%2].ServeHTTP(w, r)
							switch w.Code {
							case 204:
							case 429:
								blocked.Add(1)
							default:
								failures.Add(1)
							}
						}
					}()
				}
				wg.Wait()
				want := int64(20)
				if algorithm == limiter.TokenBucketAlgorithm {
					want = 25
				}
				if hits.Load() != want || blocked.Load() != 200-want || failures.Load() != 0 {
					t.Fatalf("app hits=%d blocked=%d failures=%d want %d admitted", hits.Load(), blocked.Load(), failures.Load(), want)
				}
				t.Logf("two application instances: %d admitted, %d blocked, zero errors", want, 200-want)
			})
		}
	}
}
