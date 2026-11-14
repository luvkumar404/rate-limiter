//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/redis/go-redis/v9"
)

func integrationStore(t *testing.T) *Redis {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	r, err := NewRedis(RedisOptions{Addr: addr, Prefix: fmt.Sprintf("ratelimiter-test:%s:%d", t.Name(), time.Now().UnixNano()), OperationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Only remove this test's keys; never flush a shared Redis database.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		iter := r.client.Scan(ctx, 0, r.opts.Prefix+":*", 100).Iterator()
		for iter.Next(ctx) {
			_ = r.client.Del(ctx, iter.Val()).Err()
		}
		_ = r.Close()
	})
	if err := r.Ping(context.Background()); err != nil {
		t.Fatalf("Redis integration tests require Redis 7 at %s: %v", addr, err)
	}
	return r
}

func mustBucket(t *testing.T, r *Redis, name string, cfg limiter.TokenBucketConfig) *RedisTokenBucket {
	t.Helper()
	b, err := r.TokenBucket(name, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRedisConcurrentSharedLimit(t *testing.T) {
	a := integrationStore(t)
	b, err := NewRedis(a.opts) // Independent connection pool, identical namespace.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	cfg := limiter.TokenBucketConfig{Limit: 100, Burst: 20, Window: 24 * time.Hour}
	buckets := []*RedisTokenBucket{mustBucket(t, a, "api", cfg), mustBucket(t, b, "api", cfg)}
	var allowed, failures atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := buckets[i%2].Allow(context.Background(), "shared")
			if err != nil {
				failures.Add(1)
				return
			}
			if r.Allowed {
				allowed.Add(1)
			} else if r.RetryAfter <= 0 {
				failures.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if allowed.Load() != 120 || failures.Load() != 0 {
		t.Fatalf("allowed=%d failures=%d; want 120 allowed and zero failures", allowed.Load(), failures.Load())
	}
	t.Log("1,000 concurrent checks across two independent Redis clients: exactly 120 allowed")
}

func TestRedisRefillAndServerTime(t *testing.T) {
	r := integrationStore(t)
	b := mustBucket(t, r, "api", limiter.TokenBucketConfig{
		Limit: 2, Burst: 1, Window: 10 * time.Second,
		Clock: func() time.Time { panic("Redis must not use the application clock") },
	})
	ctx := context.Background()
	before, err := r.client.Time(ctx).Result()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		got, err := b.Allow(ctx, "a")
		if err != nil || got.Allowed != (i < 3) || got.Limit != 2 {
			t.Fatalf("check %d: %+v, %v", i, got, err)
		}
		if i == 3 && (got.RetryAfter <= 0 || got.RetryAfter > 5*time.Second) {
			t.Fatalf("unexpected retry: %+v", got)
		}
	}
	last, err := r.client.HGet(ctx, b.redisKey("a"), "last_refill").Int64()
	if err != nil {
		t.Fatal(err)
	}
	after, err := r.client.Time(ctx).Result()
	if err != nil || last < before.UnixMicro() || last > after.UnixMicro() {
		t.Fatalf("last_refill=%d outside Redis TIME bounds [%d,%d]: %v", last, before.UnixMicro(), after.UnixMicro(), err)
	}
	// Seed past state using Redis TIME, not an application timestamp or a sleep.
	seed := `local t=redis.call('TIME'); local now=t[1]*1000000+t[2]; redis.call('HSET',KEYS[1],'tokens',0,'last_refill',string.format('%.0f',now-20000000)); return 1`
	if err := r.client.Eval(ctx, seed, []string{b.redisKey("a")}).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := b.Allow(ctx, "a")
	if err != nil || !got.Allowed || got.Remaining != 2 {
		t.Fatalf("refill must cap at full capacity: %+v, %v", got, err)
	}
	other := mustBucket(t, r, "other-rule", limiter.TokenBucketConfig{Limit: 2, Window: time.Second})
	for _, check := range []struct {
		b   *RedisTokenBucket
		key string
	}{{b, "other-key"}, {other, "a"}} {
		got, err := check.b.Allow(ctx, check.key)
		if err != nil || !got.Allowed {
			t.Fatalf("keys or rules share state: %+v, %v", got, err)
		}
	}
}

func TestRedisFractionalTokensAndClockRegression(t *testing.T) {
	r := integrationStore(t)
	b := mustBucket(t, r, "api", limiter.TokenBucketConfig{Limit: 1, Window: time.Hour})
	ctx := context.Background()
	seed := `local t=redis.call('TIME'); local now=t[1]*1000000+t[2]; redis.call('HSET',KEYS[1],'tokens','0.75','last_refill',string.format('%.0f',now)); return 1`
	if err := r.client.Eval(ctx, seed, []string{b.redisKey("a")}).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := b.Allow(ctx, "a")
	if err != nil || got.Allowed || got.RetryAfter < 14*time.Minute || got.RetryAfter > 15*time.Minute {
		t.Fatalf("fractional tokens lost: %+v, %v", got, err)
	}
	tokens, err := r.client.HGet(ctx, b.redisKey("a"), "tokens").Float64()
	if err != nil || tokens < 0.75 || tokens >= 1 {
		t.Fatalf("fractional token state=%f, %v", tokens, err)
	}
	seed = `local t=redis.call('TIME'); local now=t[1]*1000000+t[2]; local future=string.format('%.0f',now+3600000000); redis.call('HSET',KEYS[1],'tokens',0,'last_refill',future); return future`
	future, err := r.client.Eval(ctx, seed, []string{b.redisKey("a")}).Text()
	if err != nil {
		t.Fatal(err)
	}
	got, err = b.Allow(ctx, "a")
	last, lastErr := r.client.HGet(ctx, b.redisKey("a"), "last_refill").Result()
	if err != nil || lastErr != nil || got.Allowed || got.RetryAfter != time.Hour || last != future {
		t.Fatalf("clock regression granted refill: %+v, last=%s future=%s, %v, %v", got, last, future, err, lastErr)
	}
}

func TestRedisTTLRefreshAndExpiration(t *testing.T) {
	r := integrationStore(t)
	ctx := context.Background()
	b := mustBucket(t, r, "refresh", limiter.TokenBucketConfig{Limit: 1, Window: 10 * time.Second})
	if _, err := b.Allow(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := r.client.PExpire(ctx, b.redisKey("a"), time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := b.Allow(ctx, "a")
	if err != nil || got.Allowed {
		t.Fatalf("expected denied refresh: %+v, %v", got, err)
	}
	ttl, err := r.client.PTTL(ctx, b.redisKey("a")).Result()
	if err != nil || ttl < 9*time.Second || ttl > 10001*time.Millisecond {
		t.Fatalf("denied check did not refresh TTL: %v, %v", ttl, err)
	}
	short := mustBucket(t, r, "expire", limiter.TokenBucketConfig{Limit: 1, Window: 50 * time.Millisecond})
	if _, err := short.Allow(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := r.client.Exists(ctx, short.redisKey("a")).Result()
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle key did not expire")
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, err = short.Allow(ctx, "a")
	if err != nil || !got.Allowed || got.Remaining != 0 {
		t.Fatalf("expired key did not start fresh: %+v, %v", got, err)
	}
}

func TestRedisNoScriptFallback(t *testing.T) {
	r := integrationStore(t)
	b := mustBucket(t, r, "api", limiter.TokenBucketConfig{Limit: 2, Window: time.Hour})
	// Unique source ensures a cache miss without flushing anyone else's scripts.
	b.script = redis.NewScript(tokenBucketSource + "\n-- " + r.opts.Prefix)
	ctx := context.Background()
	exists, err := b.script.Exists(ctx, r.client).Result()
	if err != nil || exists[0] {
		t.Fatalf("expected a fresh script hash: %v, %v", exists, err)
	}
	for i := 0; i < 3; i++ {
		got, err := b.Allow(ctx, "a")
		if err != nil || got.Allowed != (i < 2) {
			t.Fatalf("fallback must consume once per call: %+v, %v", got, err)
		}
	}
	exists, err = b.script.Exists(ctx, r.client).Result()
	if err != nil || !exists[0] {
		t.Fatalf("fallback did not cache script: %v, %v", exists, err)
	}
}

func TestRedisCorruptStateFails(t *testing.T) {
	r := integrationStore(t)
	b := mustBucket(t, r, "api", limiter.TokenBucketConfig{Limit: 1, Window: time.Second})
	ctx := context.Background()
	if err := r.client.HSet(ctx, b.redisKey("a"), "tokens", "broken", "last_refill", "broken").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Allow(ctx, "a"); err == nil {
		t.Fatal("corrupt state silently granted a fresh quota")
	}
}
