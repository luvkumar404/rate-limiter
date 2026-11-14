//go:build integration

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/luvkumar404/rate-limiter/internal/limitertest"
	"github.com/redis/go-redis/v9"
)

func TestRedisAlgorithmContract(t *testing.T) {
	limitertest.Run(t, func(t *testing.T, cfg limiter.RuleConfig) limiter.Limiter {
		r := integrationStore(t)
		l, err := NewLimiter("api", cfg, r)
		if err != nil {
			t.Fatal(err)
		}
		return l
	})
}

func TestRedisSlidingLogBoundaries(t *testing.T) {
	r := integrationStore(t)
	b, err := r.SlidingWindow("api", limiter.WindowConfig{Limit: 2, Window: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A fixed future Redis timestamp lets the script's monotonic guard freeze
	// time while verifying exact microsecond cutoffs, without sleeps.
	seed := `local t=redis.call('TIME'); local now=t[1]*1000000+t[2]+3600000000;
redis.call('SET',KEYS[2],string.format('%.0f',now));
redis.call('ZADD',KEYS[1],string.format('%.0f',now-10000000),'expired',string.format('%.0f',now-9999999),'active');
return string.format('%.0f',now)`
	clock, err := r.client.Eval(ctx, seed, []string{b.redisKey("a"), b.redisKey("a") + ":clock"}).Int64()
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Allow(ctx, "a")
	if err != nil || !got.Allowed || got.Remaining != 0 {
		t.Fatalf("cutoff pruning: %+v %v", got, err)
	}
	got, err = b.Allow(ctx, "a")
	if err != nil || got.Allowed || got.RetryAfter != time.Microsecond {
		t.Fatalf("oldest retry: %+v %v", got, err)
	}
	count, err := r.client.ZCard(ctx, b.redisKey("a")).Result()
	if err != nil || count != 2 {
		t.Fatalf("denied call added an event: %d %v", count, err)
	}
	if err := r.client.Set(ctx, b.redisKey("a")+":clock", fmt.Sprint(clock+1), time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	got, err = b.Allow(ctx, "a")
	if err != nil || !got.Allowed || got.Remaining != 0 {
		t.Fatalf("rolling renewal: %+v %v", got, err)
	}
	for _, key := range []string{b.redisKey("a"), b.redisKey("a") + ":clock"} {
		ttl, err := r.client.PTTL(ctx, key).Result()
		if err != nil || ttl < 9*time.Second || ttl > 10001*time.Millisecond {
			t.Fatalf("missing expiry on %s: %s %v", key, ttl, err)
		}
	}
}

func TestRedisFixedWindowBoundaryAndRegression(t *testing.T) {
	r := integrationStore(t)
	b, err := r.FixedWindow("api", limiter.WindowConfig{Limit: 2, Window: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed := `local t=redis.call('TIME'); local now=t[1]*1000000+t[2]+3600000000;
local start=now-now%10000000; local last=start+5000000;
redis.call('HSET',KEYS[1],'window_start',string.format('%.0f',start),'count',2,'last_seen',string.format('%.0f',last));
return string.format('%.0f',start)`
	start, err := r.client.Eval(ctx, seed, []string{b.redisKey("a")}).Int64()
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Allow(ctx, "a")
	if err != nil || got.Allowed || got.RetryAfter != 5*time.Second {
		t.Fatalf("fixed boundary retry: %+v %v", got, err)
	}
	// Move the saved clock exactly onto the next boundary. Redis TIME remains
	// behind it, proving backward clock movement cannot reopen an old window.
	if err := r.client.HSet(ctx, b.redisKey("a"), "last_seen", start+10000000).Err(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, err := b.Allow(ctx, "a")
		if err != nil || got.Allowed != (i < 2) {
			t.Fatalf("boundary decision %d: %+v %v", i, got, err)
		}
	}
	ttl, err := r.client.PTTL(ctx, b.redisKey("a")).Result()
	if err != nil || ttl < 9*time.Second || ttl > 10001*time.Millisecond {
		t.Fatalf("fixed TTL: %s %v", ttl, err)
	}
}

func TestRedisWindowScriptCacheAndCorruption(t *testing.T) {
	for _, algorithm := range []string{limiter.SlidingWindowAlgorithm, limiter.FixedWindowAlgorithm} {
		t.Run(algorithm, func(t *testing.T) {
			r := integrationStore(t)
			l, err := NewLimiter("api", limiter.RuleConfig{Algorithm: algorithm, Limit: 1, Window: time.Hour}, r)
			if err != nil {
				t.Fatal(err)
			}
			b := l.(*RedisWindow)
			source := fixedWindowSource
			if algorithm == limiter.SlidingWindowAlgorithm {
				source = slidingWindowSource
			}
			b.script = redis.NewScript(source + "\n-- " + r.opts.Prefix)
			for i := 0; i < 2; i++ {
				got, err := b.Allow(context.Background(), "a")
				if err != nil || got.Allowed != (i == 0) {
					t.Fatalf("cache fallback: %+v %v", got, err)
				}
			}
			if algorithm == limiter.SlidingWindowAlgorithm {
				err = r.client.Set(context.Background(), b.redisKey("bad")+":clock", "broken", time.Minute).Err()
			} else {
				err = r.client.HSet(context.Background(), b.redisKey("bad"), "count", "broken").Err()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Allow(context.Background(), "bad"); err == nil {
				t.Fatal("corrupt window state granted quota")
			}
		})
	}
}

func TestRedisWindowIdleExpiration(t *testing.T) {
	for _, algorithm := range []string{limiter.SlidingWindowAlgorithm, limiter.FixedWindowAlgorithm} {
		t.Run(algorithm, func(t *testing.T) {
			r := integrationStore(t)
			l, err := NewLimiter("api", limiter.RuleConfig{Algorithm: algorithm, Limit: 1, Window: 50 * time.Millisecond}, r)
			if err != nil {
				t.Fatal(err)
			}
			b := l.(*RedisWindow)
			if _, err := b.Allow(context.Background(), "a"); err != nil {
				t.Fatal(err)
			}
			keys := []string{b.redisKey("a")}
			if algorithm == limiter.SlidingWindowAlgorithm {
				keys = append(keys, keys[0]+":clock")
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				count, err := r.client.Exists(context.Background(), keys...).Result()
				if err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("idle Redis window keys did not expire")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if got, err := b.Allow(context.Background(), "a"); err != nil || !got.Allowed {
				t.Fatalf("expired key did not renew: %+v %v", got, err)
			}
		})
	}
}
