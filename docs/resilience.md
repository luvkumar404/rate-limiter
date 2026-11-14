# Redis failure handling

Redis rules run through a failure guard and one circuit breaker shared across
the instance's rules. Memory mode uses its local algorithms directly. The
default policy is `fail_open`; change it in YAML and restart the service:

```yaml
failure_policy: fail_closed
circuit_breaker:
  failure_threshold: 5
  open_timeout: 5s
redis:
  operation_timeout: 50ms
```

Merge these fields into your existing config, keeping its backend, connection,
and rules. `failure_threshold` counts consecutive completed backend failures.
Both breaker options accept zero for their defaults and reject negative values.

| Situation | HTTP response | Meaning |
|---|---|---|
| Redis allows | 200, normal check result | Quota was checked |
| Redis denies | 429, normal check result | Quota was checked and exhausted |
| Backend fails or circuit blocks, `fail_open` | 200, `allowed: true`, `degraded: true` | Availability is prioritized; quota could not be checked |
| Backend fails or circuit blocks, `fail_closed` | 503, `{"error":"rate limiter unavailable"}` | Request is rejected because the backend is unavailable |

Fail-open results report the configured `limit`, `remaining: 0`, and
`retry_after_ms: 0`. Remaining quota is unknown; zero is a conservative
placeholder rather than a Redis measurement. Healthy responses omit
`degraded`. Fail-open decisions do not update a separate in-memory budget and
cannot enforce a distributed quota during an outage. Fail-closed responses use
503 so callers can distinguish dependency failures from ordinary quota denials.
Caller cancellation is propagated and never converted into an allowed result.
Malformed requests and unknown rules still return 400.

The breaker starts closed and admits checks normally. A successful Redis
operation, including a normal denied decision, resets the failure streak.
Connection failures, backend timeouts, and script errors count as failures.
Cancellation of the parent request does not count as a Redis failure.

After five consecutive failures by default, the circuit opens for five seconds.
Requests during cooldown receive the configured fallback without touching
Redis. Calls already in flight can still complete; the breaker does not cancel
them. Their late results cannot change a newer circuit generation.

At the end of cooldown, the next check becomes the single half-open probe.
Other callers keep receiving fallback while that probe is running. Success
closes the circuit and restores normal enforcement; failure opens it for
another full cooldown. A canceled probe releases its slot so a later caller
can retry immediately. Recovery is driven by incoming checks and needs no
background worker or service restart. Circuit transitions and actual backend
failures are logged and exposed through the Phase 6 metrics.

Every Redis check and readiness ping has an overall context deadline, default
50 ms, covering pool waits, connection initialization, and script-cache
fallback. Read/write/pool timeouts can shorten it. Network retries remain
disabled: a lost reply may follow a committed check, so replaying it can consume
quota twice. Failure policy also applies to these ambiguous failures.

Redis unavailability at startup logs a warning and the HTTP service starts
using the configured policy. Configuration errors still fail startup.
`/healthz` remains 200 during an outage; `/readyz` independently pings Redis
and returns 503 even when fail-open allows check requests. Readiness never
resets the breaker's failure streak or probes through its state. After Redis
returns, readiness may be 200 while checks are still in circuit cooldown;
the next eligible check performs recovery.

Run unit tests without Redis:

```sh
go test -buildvcs=false ./...
```

The deterministic suite covers policy results, successful quota decisions,
cooldown, failed probes, a single probe under 1,000 concurrent callers,
cancellation, and stale completions during and after recovery. See
[Redis testing](redis.md) for integration and Linux race-detector commands.

The opt-in chaos test kills and restarts a real Redis 7 container. It checks
both policies and all algorithms through startup outages, recovery, a kill
after healthy traffic, and a second recovery. It verifies health/readiness,
degraded responses, fail-closed rejection, and resumed 429 quota enforcement.
It refuses to stop containers outside the exact `ratelimiter-phase5-chaos`
Compose project and Redis service. Ordinary integration tests never stop Redis.

Run it from the repository root in PowerShell:

```powershell
$env:REDIS_PORT = '36379'
docker compose -p ratelimiter-phase5-chaos -f deploy/compose.redis.yaml up -d --wait
$env:REDIS_CHAOS_ADDR = 'localhost:36379'
$env:REDIS_CHAOS_CONTAINER = 'ratelimiter-phase5-chaos-redis-1'
go test -buildvcs=false -tags 'integration,chaos' -run TestRedisKillAndRecovery -v ./cmd/server
docker compose -p ratelimiter-phase5-chaos -f deploy/compose.redis.yaml down
Remove-Item Env:REDIS_PORT
```

`make test-chaos` runs the same test after the environment is set. The dedicated
container has persistence disabled, and restarting it resets Redis state; the
test confirms the service resumes enforcement using fresh keys. It does not
claim durability across a Redis restart.

Verified on 2026-11-14: Windows build, vet, ordinary integration tests, and the
real Redis kill/recovery test pass. The complete unit and ordinary integration
suite also passes with `-race` in `golang:1.26-bookworm`. Resilience statement
coverage is 98.8%. The Docker-controlling chaos test runs separately on Windows;
the Linux race container does not receive access to the Docker socket.

Phase 6 exposes `circuit_state`, Redis errors, and check outcomes through
`/metrics`. Degraded decisions have their own outcome counter, and circuit
short-circuits do not count as new Redis errors. Request IDs correlate HTTP
access logs with backend failures and circuit transitions. See
[observability](observability.md) for exact metric and header semantics.
