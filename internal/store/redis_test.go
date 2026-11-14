package store

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/limiter"
)

func TestRedisOptions(t *testing.T) {
	o, err := (RedisOptions{}).WithDefaults()
	if err != nil || o.Addr != "localhost:6379" || o.Prefix != "ratelimiter" || o.PoolSize != 32 || o.OperationTimeout != 50*time.Millisecond {
		t.Fatalf("bad defaults: %+v, %v", o, err)
	}
	for _, o := range []RedisOptions{
		{DB: -1}, {PoolSize: -1}, {DialTimeout: -1}, {ReadTimeout: -1},
		{WriteTimeout: -1}, {PoolTimeout: -1}, {OperationTimeout: -1}, {Prefix: " "},
	} {
		if _, err := NewRedis(o); err == nil {
			t.Fatalf("accepted invalid options: %+v", o)
		}
	}
}

func TestRedisBucketValidationAndKeys(t *testing.T) {
	r, err := NewRedis(RedisOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if r.client.Options().MaxRetries != 0 || !r.client.Options().ContextTimeoutEnabled {
		t.Fatal("mutating retries must be disabled and context deadlines enabled")
	}
	for _, tc := range []struct {
		rule string
		cfg  limiter.TokenBucketConfig
	}{
		{"a", limiter.TokenBucketConfig{}},
		{"", limiter.TokenBucketConfig{Limit: 1, Window: time.Second}},
		{"a", limiter.TokenBucketConfig{Limit: 1, Window: time.Nanosecond}},
	} {
		if _, err := r.TokenBucket(tc.rule, tc.cfg); err == nil {
			t.Fatalf("accepted invalid bucket: %+v", tc)
		}
	}
	a, _ := r.TokenBucket("a:b", limiter.TokenBucketConfig{Limit: 1, Window: time.Second})
	b, _ := r.TokenBucket("a", limiter.TokenBucketConfig{Limit: 1, Window: time.Second})
	if a.redisKey("c") == b.redisKey("b:c") || a.redisKey("c") == a.redisKey("d") {
		t.Fatal("key encoding does not preserve identity")
	}
	if _, err := a.Allow(context.Background(), ""); err == nil {
		t.Fatal("empty key accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Allow(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
}

func TestRedisOperationTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		<-release // Simulate an endpoint that accepts TCP but never responds.
	}()
	r, err := NewRedis(RedisOptions{Addr: listener.Addr().String(), OperationTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	b, _ := r.TokenBucket("api", limiter.TokenBucketConfig{Limit: 1, Window: time.Second})
	start := time.Now()
	if _, err := b.Allow(context.Background(), "a"); err == nil {
		t.Fatal("unresponsive Redis did not fail")
	}
	if time.Since(start) > time.Second {
		t.Fatal("operation deadline did not bound the Redis call")
	}
}
