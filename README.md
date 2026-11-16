# ratelimiter

[![CI](https://github.com/luvkumar404/rate-limiter/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/luvkumar404/rate-limiter/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A Go rate limiting service and middleware library with in-memory and Redis
backends. Run it as an HTTP service, embed a limiter in a Go application, or
protect `net/http` handlers with middleware that calls the central service.

The project is under development. See [PRD.md](PRD.md) for the requirements and
[implementation progress](docs/progress.md) for completed work and outstanding
milestones.

## Features

- Token bucket with configurable burst, exact sliding window, and fixed window.
- Independent named rules and per-key budgets, with idle cleanup in memory.
- Shared Redis enforcement using atomic Lua scripts and Redis server time.
- Configurable fail-open/fail-closed behavior and a shared Redis circuit breaker.
- Prometheus metrics, JSON logs, request IDs, and rate limit response headers.
- Public Go packages for embedded limiters, remote checks, and HTTP middleware.
- Docker Compose deployment with three Go instances, Nginx, Redis, and Prometheus.

## Quick start

Run commands from the repository root.

Requires Go 1.22 or later. YAML parsing uses `gopkg.in/yaml.v3`; Redis mode uses
`github.com/redis/go-redis/v9` and Redis 7.

### Docker cluster

Start the full local cluster with Docker and a current Compose version:

```powershell
docker compose up -d --build --wait
go run -buildvcs=false ./cmd/smoke
```

The API is at `http://localhost:8090`; Prometheus is at
`http://localhost:9090`. Three Go instances share Redis behind Nginx, and
Prometheus scrapes each instance directly. Redis is private to the Docker
network. No host Go installation is needed to run the cluster; the optional
smoke command requires Go. See [deployment and CI](docs/deployment.md) for
port overrides, configuration, and cleanup.

Use port **8090** for the check requests below when running the cluster. Stop
the stack with `docker compose down`. Redis persistence is disabled in this
local setup, so restarting Redis loses quota state.

### Local Go server

Run a single instance using the in-memory rules in [rules.yaml](rules.yaml):

```sh
go run ./cmd/server -config rules.yaml -addr :8080
```

The server logs JSON to stdout and drains requests for up to 10 seconds on SIGINT or
SIGTERM. Press Ctrl+C to stop it. Config errors stop startup with a nonzero exit.
If port 8080 is occupied, select another address, such as `-addr :8082`, and
use that port for requests.

In another terminal, try a check from PowerShell:

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
The first check for a fresh key using `api-default` returns:

```json
{"allowed":true,"limit":100,"remaining":119,"retry_after_ms":0}
```

The default rule starts with 120 tokens and refills 100 per minute. To see
blocking quickly, send six immediate calls using the `login` rule and one key;
the first five pass and the sixth returns 429.

## HTTP API

| Endpoint | Purpose |
|---|---|
| `POST /check` | Consume one request from a named rule for a caller-supplied key |
| `GET /healthz` | Process health |
| `GET /readyz` | Backend readiness; Redis mode uses a bounded `PING` |
| `GET /metrics` | Per-instance Prometheus metrics |

| Status | Meaning |
|---|---|
| `200` | Allowed, including fail-open degraded decisions |
| `429` | Quota exhausted |
| `400` | Invalid JSON, invalid key/rule, or unknown rule |
| `413` | Request body exceeds 4 KiB |
| `415` | Unsupported content type |
| `503` | Limiter unavailable under fail-closed behavior, or backend not ready on `/readyz` |

Keys are limited to 512 bytes and rule names to 128 bytes. Valid `/check`
decisions include `X-RateLimit-Limit` and `X-RateLimit-Remaining`; quota denials
also include `Retry-After` rounded up to whole seconds. Every HTTP response
includes `X-Request-ID`, which also appears in request-related logs.

## Rules and algorithms

Edit [rules.yaml](rules.yaml) and restart to change rules. Supported fields are
`algorithm` (`token_bucket`, `sliding_window`, or `fixed_window`), positive
`limit` and `window` (Go durations such as `1m`), nonnegative `burst` for token
buckets, and optional `scope` (`key`, `user`, `route`, or `ip`). Window algorithms
reject nonzero `burst`. The samples use a token bucket for `api-default`, an
exact sliding log for `login`, and a fixed window for `fixed-demo`.
Each rule tracks keys separately. Scope describes the caller-supplied key: send
a user identifier, route, or IP address as appropriate; `/check` does not extract
these from headers. For combined limits, use a composite key such as
`user:42:route:/orders`. Unknown fields, duplicate YAML keys, multiple documents,
invalid values, and unsupported algorithms fail validation.

See [algorithm tradeoffs](docs/algorithms.md) for rolling quota semantics,
memory costs, and the boundary bursts permitted by fixed windows.

The token bucket implements lazy refill, configurable burst, per-key isolation,
and background idle eviction. Its capacity is `limit + burst`, and its refill
rate is `limit / window`. Memory mode only shares state within one process.

## Failure handling and observability

`failure_policy` accepts `fail_open` (default) or `fail_closed`. During a Redis
outage, fail-open returns 200 with `allowed: true`, `degraded: true`, and
`remaining: 0` (quota unknown); fail-closed returns 503. Normal quota rejection
remains 429. The service also starts during an outage, while `/readyz` reports
503 and `/healthz` stays 200. A shared circuit breaker opens after five
consecutive Redis failures by default, waits five seconds, and permits one
recovery probe. YAML `circuit_breaker.failure_threshold` and `open_timeout`
configure it. See [failure handling and chaos tests](docs/resilience.md).
`GET /metrics` exposes per-instance Prometheus check counts, a latency histogram,
Redis errors, and circuit state. See
[observability setup, semantics, and queries](docs/observability.md).

## Redis backend

Run the Redis backend:

```sh
docker compose -p ratelimiter-phase3 -f deploy/compose.redis.yaml up -d --wait
go run ./cmd/server -config rules.redis.yaml -addr :8080
```

This development Redis listens on `127.0.0.1:16379` and has persistence disabled.
Start another server on `:8081` with the same config to share the limit. Each
check uses one Lua script for decision, state update, and expiry;
the script reads Redis `TIME`. All instances must use identical rule settings,
database, and prefix. Redis windows must be whole microseconds. Token bucket
keys expire after enough idle time to refill the full `limit + burst` capacity;
window keys expire when their tracked events or interval no longer matter.

Redis connection settings include address, database, pool size, timeouts,
username/password, and key prefix. `REDIS_PASSWORD` overrides the config password.
Operations default to a 50 ms deadline; automatic network retries are disabled.
See [Redis setup and integration testing](docs/redis.md) for details and the
race-detector command that runs in Docker.

## Go library and middleware

Use `pkg/middleware` to protect another Go app with an embedded limiter or the
remote service. Public constructors and contracts are in `pkg/limiter`; no
internal imports are needed. The module path is
`github.com/luvkumar404/rate-limiter`. Key extractors support socket IPs, explicit
trusted proxy CIDRs, headers, and custom functions. See
[middleware usage](docs/middleware.md).

Share limiter instances across requests and call `Close` on owned memory
limiters and Redis pools when shutting down. Middleware defaults to fail-closed
on check errors; the service defaults to fail-open, so configure both layers
for the desired failure behavior.

Run the middleware demo without Redis:

```sh
go run ./examples/demo-app -addr :8083
```

Request `http://localhost:8083/hello` six times promptly: five calls pass, then
one returns 429. To use the Docker cluster instead:

```sh
go run ./examples/demo-app -addr :8083 -limiter-url http://localhost:8090 -rule login
```

See the [demo instructions](examples/demo-app/README.md) for both modes.
For a standalone token bucket example, run `go run ./examples/token-bucket`.
It allows three immediate calls, blocks the next two, and allows another after
a 500 ms refill.

## Development and verification

```sh
go build ./...
go vet ./...
go test ./...
```

| Make target | Action |
|---|---|
| `build`, `run` | Build or start the service |
| `test` | Run tests with the race detector |
| `test-integration` | Run Redis integration tests; requires Redis |
| `test-chaos` | Test Redis failure/recovery; requires the dedicated chaos container |
| `test-prometheus` | Verify live scraping; requires Docker Desktop |
| `lint`, `vet` | Run golangci-lint or Go vet |
| `bench` | Run in-memory algorithm microbenchmarks |
| `loadtest` | Run the cluster load suite; requires PowerShell 7 and Docker |
| `up`, `down`, `smoke` | Manage and verify the local Docker cluster |

The race detector requires CGO and a compatible C compiler on Windows. If Go
commands encounter Git's ownership check during VCS metadata stamping, add
`-buildvcs=false`, for example `go test -buildvcs=false ./...`.

## Measurements and limitations

Integration tests verify all algorithms across two HTTP server instances: per 1,000 checks,
token buckets allow 120 with limit 100/burst 20, while sliding and fixed windows
allow 100 with limit 100/burst 0. All remaining checks are blocked with zero
backend errors. The Docker smoke check verifies three upstream instances,
a shared five-request sliding quota, and three healthy Prometheus targets.
Local Docker measurements on 2026-11-15 sustain an offered 100 checks/s for 20 seconds without
invalid decisions or dropped iterations on the local Docker cluster:

| Algorithm | Offered checks/s | p50 ms | p95 ms | p99 ms |
|---|---:|---:|---:|---:|
| Token bucket | 100 | 1.98 | 5.98 | 8.64 |
| Sliding window | 100 | 1.90 | 5.69 | 10.35 |
| Fixed window | 100 | 2.20 | 6.05 | 11.38 |

These are short, single-run, allow-only local measurements, not production
capacity estimates. The 1k/5k/10k offered-rate tests hit Redis timeouts, invalid
decisions, and dropped iterations; **10k checks/s with p99 under 10 ms is not
demonstrated**. A separate load-balancer correctness test admits exactly 100 of
1,000 shared-key sliding-window checks across all three instances. See
[raw results, methodology and limitations](docs/benchmarks.md), and run
`./loadtest/run.ps1` to reproduce the suite.

The benchmark report also includes allowed/denied microbenchmarks for all three
in-memory algorithms. Run `make loadtest` with PowerShell 7 and Docker for the
cluster suite, or follow the [load-test instructions](loadtest/README.md).

## Documentation

- [Requirements](PRD.md) and [implementation progress](docs/progress.md)
- [Algorithm semantics](docs/algorithms.md)
- [Redis setup and integration tests](docs/redis.md)
- [Failure handling and chaos tests](docs/resilience.md)
- [Metrics, logging, and PromQL](docs/observability.md)
- [Go middleware](docs/middleware.md)
- [Deployment and CI](docs/deployment.md)
- [Benchmarks](docs/benchmarks.md) and [load testing](loadtest/README.md)

## License

[MIT](LICENSE)
