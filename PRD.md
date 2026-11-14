# PRD: Distributed Rate Limiter Service (Go)

| | |
|---|---|
| **Project** | `ratelimiter`: a distributed rate limiting service and middleware library |
| **Language** | Go 1.22+ |
| **Backing store** | Redis 7 |
| **Status** | Draft v1 |
| **Estimated effort** | ~2 weeks (resume-ready) to 3-4 weeks (full) |

---

## 1. Overview

A service that decides whether an incoming request should be **allowed or rejected** based on configurable limits (for example, "100 requests per minute per API key"). Shared state lives in Redis, so limits hold exactly even when many application instances run behind a load balancer. Other applications use it either through an HTTP API or by importing a Go middleware package.

## 2. Problem Statement

In-memory rate limiters keep counters per process. When an app scales to N instances, each has its own counter, so a user limited to 100 req/min can effectively make 100 x N. Naive Redis implementations ("read, check, increment" as separate calls) suffer from race conditions that let extra requests through. This project solves both with atomic, Redis-backed algorithms.

## 3. Goals

- **G1:** Enforce limits correctly across multiple service instances (the "distributed" claim).
- **G2:** Make enforcement atomic using Redis Lua scripts.
- **G3:** Support multiple algorithms (token bucket, sliding window, fixed window) behind one interface.
- **G4:** Stay fast: target 10k+ checks/sec and p99 latency under 10 ms on a 3-instance local cluster.
- **G5:** Degrade predictably when Redis is unavailable (configurable fail-open / fail-closed).
- **G6:** Be observable (metrics, logs) and easy to run (`docker compose up`).
- **G7:** Prove claims with reproducible load tests and benchmark numbers.

## 4. Non-Goals

- Multi-region replication or Redis Cluster support (single Redis node, optionally with a replica, is enough).
- Authentication / user management for the service itself.
- A billing or quota-management system (long-term monthly quotas).
- A full admin UI for editing rules (rules are file-driven).

## 5. Target Users

| User | Need |
|---|---|
| Backend developer | Drop in middleware to protect an API in a few lines |
| Platform / infra engineer | Run a central limiter service that many apps call |
| Recruiter / interviewer | See a clear, benchmarked, well-documented systems project |

## 6. Functional Requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-1 | `POST /check` accepts `{key, rule}` and returns `{allowed, remaining, limit, retry_after_ms}` | P0 |
| FR-2 | Rules define algorithm, limit, window, and key scope (per route, user, or IP) | P0 |
| FR-3 | Token bucket algorithm with burst capacity and steady refill | P0 |
| FR-4 | Sliding window algorithm | P1 |
| FR-5 | Fixed window algorithm (baseline for comparison) | P2 |
| FR-6 | All Redis operations execute atomically via Lua scripts | P0 |
| FR-7 | Time source is Redis `TIME`, not application clocks | P0 |
| FR-8 | Configurable failure policy: fail-open or fail-closed | P0 |
| FR-9 | `GET /healthz` and `GET /readyz` endpoints | P0 |
| FR-10 | Prometheus `/metrics` endpoint | P1 |
| FR-11 | Standard headers: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `Retry-After` | P1 |
| FR-12 | Importable `net/http` middleware with pluggable key extractors | P1 |
| FR-13 | Graceful shutdown on SIGINT/SIGTERM | P0 |
| FR-14 | Dashboard showing live allowed vs blocked traffic | P2 |

## 7. Non-Functional Requirements

| ID | Requirement | Target |
|---|---|---|
| NFR-1 | Throughput | 10k+ checks/sec across 3 instances (stretch: 20k+) |
| NFR-2 | Latency | p99 under 10 ms on local Docker network |
| NFR-3 | Correctness | Under load, allowed count never exceeds limit + burst across all instances |
| NFR-4 | Reliability | No crashes when Redis is killed; behavior matches configured policy |
| NFR-5 | Code quality | `go test -race` clean, `golangci-lint` clean |
| NFR-6 | Memory | Idle keys expire (Redis TTL) so storage does not grow unbounded |
| NFR-7 | Portability | Single static binary, small Docker image |

## 8. System Architecture

```
Client -> App / API Gateway -> Rate Limiter Service -> Redis
                                      |
                              Prometheus metrics -> Dashboard
```

**Components**

- **API layer** (`internal/api`): HTTP handlers, request validation, response shaping.
- **Limiter core** (`internal/limiter`): the `Limiter` interface and algorithm implementations.
- **Store layer** (`internal/store`): Redis backend (Lua scripts) and in-memory backend (dev/testing).
- **Config** (`internal/config`): YAML rule loading and validation.
- **Middleware** (`pkg/middleware`): reusable `net/http` middleware.
- **Observability** (`internal/metrics`): Prometheus collectors, structured logging.

**Core interface**

```go
type Result struct {
    Allowed    bool
    Limit      int
    Remaining  int
    RetryAfter time.Duration
}

type Limiter interface {
    Allow(ctx context.Context, key string) (Result, error)
}
```

**Folder structure**

```
ratelimiter/
  cmd/server/            # main.go
  internal/
    api/                 # HTTP handlers
    config/              # rule loading
    limiter/             # interface + algorithms
    metrics/             # Prometheus
    store/               # redis + memory backends
  pkg/middleware/        # importable middleware
  examples/demo-app/     # sample app using the middleware
  dashboard/             # React + TS (optional)
  deploy/                # Dockerfile, docker-compose.yml, nginx.conf
  loadtest/              # k6 / vegeta scripts
  docs/                  # diagrams, benchmark results
  PRD.md
  README.md
```

## 9. API Specification

**`POST /check`**

Request:
```json
{ "key": "user:42", "rule": "api-default" }
```
Response `200`:
```json
{ "allowed": true, "limit": 100, "remaining": 87, "retry_after_ms": 0 }
```
Response `429` (blocked) returns the same body with `allowed: false` and a non-zero `retry_after_ms`.

**`GET /healthz`**: process is alive. **`GET /readyz`**: Redis reachable. **`GET /metrics`**: Prometheus format.

**Example rule config (`rules.yaml`)**
```yaml
rules:
  api-default:
    algorithm: token_bucket
    limit: 100
    window: 1m
    burst: 20
  login:
    algorithm: sliding_window
    limit: 5
    window: 1m
failure_policy: fail_open
```

## 10. Build Phases

Each phase ends with something runnable. Do not start a phase until the previous phase's "Done when" criteria are met.

---

### Phase 0: Project Setup
**Duration:** 0.5 day

**Objective:** Create a compiling skeleton with the core contract defined.

**Tasks**
- [x] `go mod init`, create folder structure from section 8 (optional dashboard deferred)
- [x] Add `.gitignore`, `LICENSE`, README stub
- [x] Define `Limiter` interface and `Result` struct
- [x] Add a `Makefile` with `build`, `test`, `lint`, `run` targets
- [ ] Initialize git and push to GitHub

**Deliverables:** compiling repo with empty packages and core types.

**Done when:** `go build ./...` and `go vet ./...` pass.

---

### Phase 1: In-Memory Token Bucket
**Duration:** 1-2 days | **Covers:** FR-3

**Objective:** Prove the algorithm locally before adding Redis.

**Tasks**
- [x] Implement token bucket with `sync.Mutex` and a `map[string]*bucket`
- [x] Lazy refill: compute tokens from elapsed time on each call (no ticker)
- [x] Inject the clock (`func() time.Time`) so tests are deterministic
- [x] Background goroutine evicting idle keys
- [x] Table-driven tests: burst, steady state, refill, concurrent access
- [x] First `go test -bench` benchmark

**Deliverables:** `internal/limiter/tokenbucket.go` plus tests.

**Done when:** all tests pass, `go test -race ./...` is clean, benchmark runs.

---

### Phase 2: HTTP API and Config Rules
**Duration:** 2 days | **Covers:** FR-1, FR-2, FR-9, FR-13

**Objective:** Make the limiter usable over the network.

**Tasks**
- [x] `POST /check`, `GET /healthz`
- [x] YAML rule loader with validation (clear errors on bad config)
- [x] Rule lookup by name; unknown rule returns `400`
- [x] Structured logging with `log/slog`
- [x] Graceful shutdown using `context` and `os/signal`
- [x] Handler tests with `httptest`

**Deliverables:** runnable server using the in-memory backend.

**Done when:** `curl` requests start returning `429` after the limit is exceeded, and Ctrl+C shuts down cleanly.

---

### Phase 3: Redis Backend with Lua
**Duration:** 2-3 days | **Covers:** FR-6, FR-7

**Objective:** Make limits shared and atomic across instances. This is the heart of the project.

**Tasks**
- [x] Add `go-redis` and a configurable connection (address, pool size, timeouts)
- [x] Write token bucket as a Lua script: hash with `tokens` and `last_refill`, TTL set on every call
- [x] Use Redis `TIME` inside the script (no app clock dependency)
- [x] Load via `EVALSHA`, fall back to `EVAL` on `NOSCRIPT`
- [x] Return `{allowed, remaining, retry_after}` from the script
- [x] Integration tests with `testcontainers-go` or Docker Compose Redis
- [x] Concurrency test: N goroutines hammer one key, assert allowed <= limit + burst

**Deliverables:** `internal/store/redis.go`, `scripts/token_bucket.lua`, integration tests.

**Done when:** two server instances pointed at one Redis enforce a single shared limit.

---

### Phase 4: Additional Algorithms and Pluggability
**Duration:** 2 days | **Covers:** FR-4, FR-5

**Objective:** Support multiple algorithms chosen by config.

**Tasks**
- [ ] Sliding window (sorted set, or the sliding-window-counter approximation)
- [ ] Fixed window baseline
- [ ] Factory function that builds a `Limiter` from rule config
- [ ] Shared test suite run against every implementation
- [ ] Document accuracy vs memory tradeoffs of each algorithm

**Deliverables:** three algorithms behind one interface.

**Done when:** changing `algorithm:` in YAML switches behavior with no code changes, and every implementation passes the shared suite.

---

### Phase 5: Resilience
**Duration:** 1-2 days | **Covers:** FR-8

**Objective:** Behave predictably when Redis misbehaves.

**Tasks**
- [ ] `context.WithTimeout` on every Redis call (default 50 ms)
- [ ] Fail-open / fail-closed policy from config
- [ ] Simple circuit breaker (closed, open, half-open) to stop hammering a dead Redis
- [ ] `/readyz` reflects Redis health
- [ ] Chaos test: kill Redis mid-run and verify configured behavior

**Deliverables:** failure-handling layer wrapping the Redis limiter.

**Done when:** stopping Redis never crashes the service; responses follow the configured policy; recovery is automatic when Redis returns.

---

### Phase 6: Observability
**Duration:** 1-2 days | **Covers:** FR-10, FR-11

**Objective:** Make behavior visible.

**Tasks**
- [ ] Prometheus metrics: `checks_total{result}`, `check_duration_seconds` histogram, `redis_errors_total`, `circuit_state`
- [ ] `/metrics` endpoint
- [ ] Add `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `Retry-After` headers
- [ ] Request ID in logs

**Deliverables:** metrics package and headers.

**Done when:** Prometheus scrapes live numbers that match observed traffic.

---

### Phase 7: Middleware Package
**Duration:** 1 day | **Covers:** FR-12

**Objective:** Let other Go apps adopt the limiter with minimal code.

**Tasks**
- [ ] `pkg/middleware` for `net/http` (optional adapters for chi / gin)
- [ ] Key extractors: client IP (with trusted-proxy handling), header, custom func
- [ ] Support both modes: call remote service, or embed a limiter directly
- [ ] `examples/demo-app` showing usage

**Deliverables:** public, documented package.

**Done when:** a separate demo app is rate limited with roughly three lines of code.

---

### Phase 8: Docker and CI
**Duration:** 1-2 days

**Objective:** One-command cluster and automated quality gates.

**Tasks**
- [ ] Multi-stage Dockerfile producing a small static image
- [ ] `docker-compose.yml`: 3 service instances, Redis, Nginx load balancer, Prometheus
- [ ] GitHub Actions: `golangci-lint`, `go test -race`, integration tests, Docker build
- [ ] Status badges in README

**Deliverables:** `deploy/` directory and `.github/workflows/ci.yml`.

**Done when:** `docker compose up` starts the full cluster and CI is green on push.

---

### Phase 9: Load Testing and Benchmarks
**Duration:** 2 days | **Covers:** G4, G7, NFR-1 to NFR-3

**Objective:** Back every claim with numbers.

**Tasks**
- [ ] k6 or vegeta scripts targeting the load balancer
- [ ] Correctness test: limit 100/min, send 1000 requests across 3 instances, confirm about 100 allowed
- [ ] Measure throughput and p50 / p95 / p99 latency per algorithm
- [ ] `go test -bench` results for algorithm internals
- [ ] Save raw results and a summary table in `docs/benchmarks.md`

**Deliverables:** reproducible scripts and a results table.

**Done when:** README can cite real throughput and latency numbers and the correctness claim is verified.

---

### Phase 10: Dashboard (Optional)
**Duration:** 2-3 days | **Covers:** FR-14

**Objective:** A clean visual demo.

**Tasks**
- [ ] Add `GET /stats` endpoint (or read from Prometheus)
- [ ] React + TypeScript page: live allowed vs blocked chart, top blocked keys, current circuit state
- [ ] Minimal, editorial styling consistent with your portfolio
- [ ] Add to Docker Compose

**Deliverables:** `dashboard/` app.

**Done when:** it looks good in a screen recording while a load test runs.

---

### Phase 11: Polish and Ship
**Duration:** 1-2 days

**Objective:** Make the project presentable.

**Tasks**
- [ ] README: overview, architecture diagram, quickstart, API reference, benchmark table
- [ ] Design decisions section: why Lua, why Redis `TIME`, fail-open vs fail-closed tradeoffs, algorithm comparison
- [ ] 60-90 second demo GIF or video
- [ ] Tag `v1.0.0` release
- [ ] Add to portfolio site and GitHub profile README
- [ ] Write the resume bullet using real measured numbers

**Deliverables:** public, polished repository.

**Done when:** a stranger can clone the repo and run the full cluster in under 5 minutes.

---

## 11. Timeline

| Scope | Phases | Estimate |
|---|---|---|
| Minimum resume-ready | 0-3, 6, 8, 9, 11 | ~2 weeks |
| Full version | 0-11 | ~3-4 weeks |

**If time is tight:** cut Phase 4 (extra algorithms) and Phase 10 (dashboard) first. Phase 3 (atomic Redis) and Phase 9 (load-test proof) matter most.

## 12. Success Metrics

- Correctness test passes: allowed requests never exceed limit + burst across 3 instances.
- Measured throughput of at least 10k checks/sec and p99 under 10 ms.
- `go test -race` and linting are clean in CI.
- Killing Redis does not crash the service.
- README with benchmark table, architecture diagram, and demo GIF is published.

## 13. Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Lua script bugs cause off-by-one limits | Wrong enforcement | Heavy table-driven tests plus the concurrency test in Phase 3 |
| Redis becomes the bottleneck | Latency spikes | Pipelining where possible, local cache for blocked keys, benchmark early |
| Clock drift between instances | Inconsistent refill | Use Redis `TIME` inside scripts |
| Hot keys overload one Redis slot | Throughput ceiling | Document as a known limit; consider key sharding as future work |
| Scope creep | Project never ships | Follow the phase gates; defer P2 items |

## 14. Future Enhancements

- Redis Cluster support and key sharding
- gRPC API alongside HTTP
- Dynamic rule updates via API without restart
- Per-tenant quotas and tiered plans
- Adaptive limiting based on backend load
- OpenTelemetry tracing

## 15. Resume Line (fill in real numbers)

> Built a distributed rate limiter in Go using Redis Lua scripts for atomic token-bucket and sliding-window enforcement; sustained **[X]k checks/sec with p99 under [Y] ms** across 3 instances, with configurable fail-open/fail-closed resilience and Prometheus observability.
