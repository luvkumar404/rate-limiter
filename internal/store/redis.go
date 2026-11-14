package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/redis/go-redis/v9"
)

//go:embed scripts/token_bucket.lua
var tokenBucketSource string

var tokenBucketScript = redis.NewScript(tokenBucketSource)

// RedisOptions configures one shared connection pool. Zero values use defaults.
type RedisOptions struct {
	Addr             string        `yaml:"addr"`
	Username         string        `yaml:"username"`
	Password         string        `yaml:"password"`
	DB               int           `yaml:"db"`
	PoolSize         int           `yaml:"pool_size"`
	DialTimeout      time.Duration `yaml:"dial_timeout"`
	ReadTimeout      time.Duration `yaml:"read_timeout"`
	WriteTimeout     time.Duration `yaml:"write_timeout"`
	PoolTimeout      time.Duration `yaml:"pool_timeout"`
	OperationTimeout time.Duration `yaml:"operation_timeout"`
	Prefix           string        `yaml:"prefix"`
}

func (o RedisOptions) WithDefaults() (RedisOptions, error) {
	if o.DB < 0 || o.PoolSize < 0 || o.DialTimeout < 0 || o.ReadTimeout < 0 || o.WriteTimeout < 0 || o.PoolTimeout < 0 || o.OperationTimeout < 0 {
		return o, errors.New("redis: database, pool size, and timeouts must be nonnegative")
	}
	if o.Addr == "" {
		o.Addr = "localhost:6379"
	}
	if o.Prefix == "" {
		o.Prefix = "ratelimiter"
	}
	if strings.TrimSpace(o.Prefix) == "" || len(o.Prefix) > 128 {
		return o, errors.New("redis: prefix must be nonblank and at most 128 bytes")
	}
	if o.PoolSize == 0 {
		o.PoolSize = 32
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = time.Second
	}
	if o.OperationTimeout == 0 {
		o.OperationTimeout = 50 * time.Millisecond
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = o.OperationTimeout
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = o.OperationTimeout
	}
	if o.PoolTimeout == 0 {
		o.PoolTimeout = o.OperationTimeout
	}
	return o, nil
}

// Redis owns the connection pool, shared by every rule in a service instance.
type Redis struct {
	client *redis.Client
	opts   RedisOptions
}

func NewRedis(opts RedisOptions) (*Redis, error) {
	opts, err := opts.WithDefaults()
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(&redis.Options{
		Addr: opts.Addr, Username: opts.Username, Password: opts.Password, DB: opts.DB,
		PoolSize: opts.PoolSize, DialTimeout: opts.DialTimeout, ReadTimeout: opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout, PoolTimeout: opts.PoolTimeout,
		ContextTimeoutEnabled: true,
		// Retrying a mutating script after a lost response could consume twice.
		MaxRetries: -1,
	})
	return &Redis{client: client, opts: opts}, nil
}

func (r *Redis) Close() error { return r.client.Close() }

func (r *Redis) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.opts.OperationTimeout)
	defer cancel()
	return r.client.Ping(ctx).Err()
}

type RedisTokenBucket struct {
	store    *Redis
	rule     string
	limit    int
	capacity int
	windowUS int64
	ttlMS    int64
	script   *redis.Script
}

var _ limiter.Limiter = (*RedisTokenBucket)(nil)

// TokenBucket binds a rule name to the store. All instances must use the same
// rule parameters, Redis database, and prefix to enforce one shared policy.
func (r *Redis) TokenBucket(rule string, cfg limiter.TokenBucketConfig) (*RedisTokenBucket, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rule) == "" {
		return nil, errors.New("redis: rule name must not be empty")
	}
	if cfg.Window%time.Microsecond != 0 {
		return nil, errors.New("redis: window must be a whole number of microseconds")
	}
	windowUS := cfg.Window.Microseconds()
	capacity := cfg.Limit + cfg.Burst
	// One extra millisecond avoids premature expiry due to Redis TTL rounding.
	ttlMS := int64(math.Ceil(float64(windowUS)*float64(capacity)/float64(cfg.Limit)/1000)) + 1
	return &RedisTokenBucket{store: r, rule: rule, limit: cfg.Limit, capacity: capacity,
		windowUS: windowUS, ttlMS: ttlMS, script: tokenBucketScript}, nil
}

func (b *RedisTokenBucket) redisKey(key string) string {
	return b.store.redisKey(limiter.TokenBucketAlgorithm, b.rule, key)
}

func (r *Redis) redisKey(algorithm, rule, key string) string {
	hash := sha256.Sum256([]byte(key))
	return r.opts.Prefix + ":" + algorithm + ":" + base64.RawURLEncoding.EncodeToString([]byte(rule)) + ":" + hex.EncodeToString(hash[:])
}

func (b *RedisTokenBucket) Allow(ctx context.Context, key string) (limiter.Result, error) {
	if strings.TrimSpace(key) == "" {
		return limiter.Result{}, errors.New("key must not be empty")
	}
	ctx, cancel := context.WithTimeout(ctx, b.store.opts.OperationTimeout)
	defer cancel()
	// Run uses EVALSHA first and EVAL only on NOSCRIPT (which did not mutate state).
	values, err := b.script.Run(ctx, b.store.client, []string{b.redisKey(key)},
		b.limit, b.capacity, b.windowUS, b.ttlMS).Int64Slice()
	if err != nil {
		return limiter.Result{}, fmt.Errorf("redis token bucket: %w", err)
	}
	return parseResult(values, b.limit, b.capacity)
}

func parseResult(values []int64, limit, capacity int) (limiter.Result, error) {
	if len(values) != 3 || (values[0] != 0 && values[0] != 1) || values[1] < 0 || values[1] > int64(capacity) || values[2] < 0 || values[2] > math.MaxInt64/int64(time.Microsecond) {
		return limiter.Result{}, errors.New("redis limiter: invalid script response")
	}
	return limiter.Result{
		Allowed: values[0] == 1, Limit: limit, Remaining: int(values[1]),
		RetryAfter: time.Duration(values[2]) * time.Microsecond,
	}, nil
}
