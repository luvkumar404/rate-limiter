package config

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ratelimiter/ratelimiter/internal/limiter"
	"github.com/ratelimiter/ratelimiter/internal/store"
	"gopkg.in/yaml.v3"
)

type Rule struct {
	Algorithm string        `yaml:"algorithm"`
	Limit     int           `yaml:"limit"`
	Window    time.Duration `yaml:"window"`
	Burst     int           `yaml:"burst"`
	Scope     string        `yaml:"scope"`
}

type Config struct {
	Rules         map[string]Rule    `yaml:"rules"`
	FailurePolicy string             `yaml:"failure_policy"`
	Backend       string             `yaml:"backend"`
	Redis         store.RedisOptions `yaml:"redis"`
}

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open rules: %w", err)
	}
	defer f.Close()
	return Decode(f)
}

// Decode rejects unknown fields, duplicate keys, and multiple YAML documents.
func Decode(r io.Reader) (Config, error) {
	var cfg Config
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode rules: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("rules must contain exactly one YAML document")
	}
	if cfg.FailurePolicy == "" {
		cfg.FailurePolicy = "fail_open"
	}
	if cfg.FailurePolicy != "fail_open" && cfg.FailurePolicy != "fail_closed" {
		return Config{}, fmt.Errorf("failure_policy must be fail_open or fail_closed")
	}
	if cfg.Backend == "" {
		cfg.Backend = "memory"
	}
	if cfg.Backend != "memory" && cfg.Backend != "redis" {
		return Config{}, fmt.Errorf("backend must be memory or redis")
	}
	redisOptions, err := cfg.Redis.WithDefaults()
	if err != nil {
		return Config{}, err
	}
	cfg.Redis = redisOptions
	if len(cfg.Rules) == 0 {
		return Config{}, fmt.Errorf("at least one rule is required")
	}
	for name, rule := range cfg.Rules {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || len(name) > 128 {
			return Config{}, fmt.Errorf("rule names must be nonempty, at most 128 bytes, with no surrounding whitespace")
		}
		if rule.Algorithm != "token_bucket" {
			return Config{}, fmt.Errorf("rule %q: algorithm must be token_bucket (other algorithms are not implemented yet)", name)
		}
		if err := (limiter.TokenBucketConfig{Limit: rule.Limit, Burst: rule.Burst, Window: rule.Window}).Validate(); err != nil {
			return Config{}, fmt.Errorf("rule %q: %w", name, err)
		}
		if cfg.Backend == "redis" && rule.Window%time.Microsecond != 0 {
			return Config{}, fmt.Errorf("rule %q: redis window must be a whole number of microseconds", name)
		}
		if rule.Scope == "" {
			rule.Scope = "key"
		}
		switch rule.Scope {
		case "key", "user", "route", "ip":
		default:
			return Config{}, fmt.Errorf("rule %q: scope must be key, user, route, or ip", name)
		}
		cfg.Rules[name] = rule
	}
	return cfg, nil
}
