# ratelimiter

A Go rate limiting service under development. See [PRD.md](PRD.md) for the
requirements and [implementation progress](docs/progress.md) for current scope.

Requires Go 1.22 or later. YAML parsing uses `gopkg.in/yaml.v3`; Redis mode uses
`github.com/redis/go-redis/v9` and Redis 7.

```sh
go build ./...
go vet ./...
go test ./...
go run ./cmd/server
go run ./examples/token-bucket
```

Start the HTTP service with `go run ./cmd/server -config rules.yaml -addr :8080`.
It logs JSON to stdout and drains requests for up to 10 seconds on SIGINT or
SIGTERM. Press Ctrl+C to stop it. Config errors stop startup with a nonzero exit.

Try a check from PowerShell:

```powershell
Invoke-RestMethod -Method Post -Uri http://localhost:8080/check `
  -ContentType 'application/json' -Body '{"key":"user:42","rule":"api-default"}'
```

Or from a shell with curl:

```sh
curl -i http://localhost:8080/check -H 'Content-Type: application/json' \
  -d '{"key":"user:42","rule":"api-default"}'
```

`POST /check` returns `{allowed, limit, remaining, retry_after_ms}` with status
200 when allowed and 429 when blocked. Retry durations round up to milliseconds.
The default rule starts with 120 tokens and refills 100 per minute. To see
blocking quickly, send six immediate calls using the `login` rule and one key;
the first five pass and the sixth returns 429.

`GET /healthz` reports process health. `GET /readyz` reports backend readiness;
Redis mode issues a bounded `PING` and returns 503 when Redis is unreachable.
Unknown rules and invalid JSON return 400;
oversized bodies return 413, unsupported content types return 415, and limiter
errors return 503. Bodies are limited to 4 KiB, keys to 512 bytes, and rule names
to 128 bytes.

Edit [rules.yaml](rules.yaml) and restart to change rules. Supported fields are
`algorithm: token_bucket`, positive `limit` and `window` (Go durations such as
`1m`), nonnegative `burst`, and optional `scope` (`key`, `user`, `route`, or `ip`).
Each rule tracks keys separately. Scope describes the caller-supplied key: send
a user identifier, route, or IP address as appropriate; `/check` does not extract
these from headers. For combined limits, use a composite key such as
`user:42:route:/orders`. Unknown fields, duplicate YAML keys, multiple documents,
invalid values, and unsupported algorithms fail validation.

`failure_policy` accepts `fail_open` (default) or `fail_closed`; policy enforcement
and the circuit breaker are scheduled for Phase 5. Currently Redis must be
reachable at startup, and backend errors after startup return 503 regardless of
this setting. Standard rate limit headers
and Prometheus metrics are scheduled for Phase 6.

Make targets: `build`, `test` (race detector), `test-integration` (requires Redis),
`lint` (go vet), `run`, and `bench`.
The race detector requires CGO and a compatible C compiler on Windows.

The token bucket implements lazy refill, configurable burst, per-key isolation,
and background idle eviction. Its capacity is `limit + burst`, and its refill
rate is `limit / window`. The demo allows three immediate calls, blocks the next
two, and allows another after a 500 ms refill. Call `Close` on each bucket to
stop its cleanup worker. Memory mode only shares state within one process.

Run the Redis backend:

```sh
docker compose -p ratelimiter-phase3 -f deploy/compose.redis.yaml up -d --wait
go run ./cmd/server -config rules.redis.yaml -addr :8080
```

This development Redis listens on `127.0.0.1:16379` and has persistence disabled.
Start another server on `:8081` with the same config to share the limit. Each
check uses one Lua script for refill, decision, state update, and idle expiry;
the script reads Redis `TIME`. All instances must use identical rule settings,
database, and prefix. Redis windows must be whole microseconds. Idle keys expire
after enough time to refill the full `limit + burst` capacity.

Redis connection settings include address, database, pool size, timeouts,
username/password, and key prefix. `REDIS_PASSWORD` overrides the config password.
Operations default to a 50 ms deadline; automatic network retries are disabled.
See [Redis setup and integration testing](docs/redis.md) for details and the
race-detector command that runs in Docker.

On this Windows workspace, elevated builds hit Git's ownership check. Add
`-buildvcs=false` to Go commands if you encounter that error, for example
`go test -buildvcs=false ./...`. This disables optional VCS metadata stamping.

The module path `github.com/ratelimiter/ratelimiter` is a placeholder. Integration
tests verify a shared limit across two HTTP server instances: 1,000 checks,
120 allowed, 880 blocked, zero errors with limit 100 and burst 20. The three-node
cluster, throughput targets, and latency percentiles remain unmeasured.

See [benchmark notes](docs/benchmarks.md) for the measured local microbenchmark.
