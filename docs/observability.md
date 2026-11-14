# Metrics, headers, and request IDs

Every service instance exposes `GET /metrics` in Prometheus format using its own
registry. The endpoint is available in both memory and Redis modes, including
during Redis outages. Scraping reads in-process counters and circuit state;
it does not call Redis or consume quota. Metrics reset when the instance
restarts. Go runtime and process collectors are included where supported.

| Metric | Meaning |
|---|---|
| `checks_total{result}` | Completed limiter calls, with fixed outcomes `allowed`, `blocked`, `degraded`, `error`, or `canceled` |
| `check_duration_seconds` | Histogram of completed `Allow` calls, including backend/pool waits, circuit checks, and failure policy |
| `redis_errors_total` | Failed logical Redis checks and startup/readiness pings |
| `circuit_state` | `0` closed, `1` open, `2` half-open; always `0` in memory mode |

`allowed` counts authoritative allowed decisions. `blocked` counts normal quota
denials. `degraded` counts allowed fail-open decisions without authoritative
quota state. `error` counts failed checks, including fail-closed rejection.
`canceled` counts calls returning an error after caller cancellation. Each
completed call increments exactly one outcome and one histogram observation.
Malformed requests, unknown rules, health probes, scrapes, and unsupported
methods do not increment check counters or the latency histogram.

Redis instrumentation sits inside the failure guard. Circuit short-circuits,
parent-request cancellation, normal denied decisions, and successful NOSCRIPT
fallbacks do not increment `redis_errors_total`. A failed readiness ping does
increment it even when the circuit is open. The counter measures failed logical
operations, not failed individual wire commands. The gauge reads current circuit
state without advancing it or probing Redis.

Histogram buckets are 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 250, 500,
and 1000 milliseconds, plus infinity. Observations use seconds. Timing excludes
HTTP parsing, response serialization, and client/network transit, so this is
not a measurement of end-to-end HTTP p99. Histogram quantiles are estimates
from buckets; see [Prometheus histogram guidance](https://prometheus.io/docs/practices/histograms/).

Only the fixed outcome label is application-defined. Keys, users, IP addresses,
routes, rule names, errors, and request IDs are never metric labels. Prometheus
adds its target labels such as `job` and `instance`. This keeps time-series
counts bounded; see [Prometheus instrumentation guidance](https://prometheus.io/docs/practices/instrumentation/).

Every successful `/check` evaluation, including a 429 or a degraded fail-open
decision, returns `X-RateLimit-Limit` and `X-RateLimit-Remaining` matching its
JSON body. A blocked decision also returns `Retry-After` as whole seconds,
rounded up. For example, 1500 ms becomes `Retry-After: 2`; one nanosecond becomes
`Retry-After: 1`. JSON `retry_after_ms` is independently rounded up to milliseconds.
Allowed responses omit `Retry-After`. Validation errors, 503 errors, health,
and metrics responses omit quota headers because they have no quota decision.
During fail-open, remaining zero is an unknown-quota placeholder; the body
includes `degraded: true`. See [failure handling](resilience.md).

All HTTP responses return `X-Request-ID`. A supplied ID is accepted when it is
one header value of 1–128 ASCII letters, digits, dots, underscores, colons, or
hyphens. Missing, malformed, oversized, and multiple values are replaced by a
generated ID. The ID reaches the limiter through request context and appears
in access, check-error, fallback, and circuit-transition logs caused by that
request. Startup/shutdown events have no request ID. IDs are correlation data,
not identity or authentication.

Start an updated server on an available port:

```powershell
go run -buildvcs=false ./cmd/server -config rules.redis.yaml -addr :8082
```

The development Redis must be running for normal Redis decisions; the service
still starts and applies its configured policy if Redis is unavailable. Inspect
metrics and correlate a request:

```powershell
Invoke-WebRequest -Uri http://localhost:8082/metrics
Invoke-WebRequest -Method Post -Uri http://localhost:8082/check `
  -Headers @{'X-Request-ID'='demo-check-001'} -ContentType 'application/json' `
  -Body '{"key":"user:42","rule":"api-default"}'
```

The Docker Desktop sample [prometheus.yaml](../deploy/prometheus.yaml) scrapes
`host.docker.internal:8082` every five seconds. With the server running, start
a standalone scraper from the repository root (image version used in testing):

```powershell
docker run --rm --name ratelimiter-prometheus -p 127.0.0.1:9090:9090 `
  --mount "type=bind,source=$($PWD.Path)/deploy/prometheus.yaml,target=/etc/prometheus/prometheus.yml,readonly" `
  prom/prometheus:v3.2.1 --config.file=/etc/prometheus/prometheus.yml
```

Open `http://localhost:9090` to query metrics and view target health. Edit the
target if your service uses another port. This sample targets Docker Desktop;
Linux Docker hosts need their own reachable service address. The complete
three-instance deployment is still Phase 8 work.

Example PromQL for charts:

```promql
sum(rate(checks_total{result=~"allowed|degraded"}[1m]))
sum(rate(checks_total{result="blocked"}[1m]))
sum(rate(redis_errors_total[1m]))
histogram_quantile(0.99, sum by (le) (rate(check_duration_seconds_bucket[5m])))
```

Run unit tests with `go test -buildvcs=false ./...`, and ordinary Redis
integration/race tests using the [Redis test commands](redis.md). Tests check
headers, rounding, ID propagation through logs and context, per-instance
registries, all decision outcomes, actual Redis failures versus short-circuits,
and live closed/open/half-open circuit gauges. Two-server Redis tests parse
the exposed counters and histogram, verifying they match 1,000 HTTP decisions
for every algorithm, with 500 checks recorded by each instance.

An additional opt-in Docker Desktop test starts a temporary memory-mode server,
sends two allowed and one blocked request, and launches a real Prometheus. It
queries Prometheus for `up=1`, allowed=2, blocked=1, histogram count=3, Redis
errors=0, and a closed circuit. It removes its scraper container and stops its
test server afterward:

```powershell
docker pull prom/prometheus:v3.2.1
go test -buildvcs=false -tags 'integration,prometheus' -run TestPrometheusLiveScrape -v ./cmd/server
```

`make test-prometheus` runs the same opt-in test. No user service is stopped or
modified. The test server briefly binds an ephemeral host port so Docker
Desktop can reach it. Verification on 2026-11-14 passed against Prometheus
3.2.1, with metrics statement coverage of 100% and request-ID coverage of 92.3%.
