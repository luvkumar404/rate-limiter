
# Docker deployment and CI

From the repository root:

```powershell
docker compose up -d --build --wait
```

The first build downloads dependencies. Later builds reuse BuildKit caches.
Plain `docker compose up` also builds the application automatically; `-d --wait`
waits for health checks and returns control to the terminal. Use a current
Docker Desktop/Compose supporting health dependencies, BuildKit cache mounts,
and `pull_policy: build`.

| Service | Access | Role |
|---|---|---|
| Nginx | `http://localhost:8090` | Round-robin API gateway |
| Three Go instances | Docker network only, port 8080 | Shared atomic enforcement |
| Redis 7 | Docker network only, port 6379 | Shared counters and event logs |
| Prometheus | `http://localhost:9090` | Scrapes all three Go instances |

Host ports bind only to localhost and avoid the earlier application ports
8080–8083 and development Redis port 16379. Override them without editing YAML:

```powershell
$env:RATELIMITER_HTTP_PORT = '8091'
$env:RATELIMITER_PROMETHEUS_PORT = '9091'
docker compose up -d --build --wait
```

Remove the overrides to restore defaults. Redis and individual Go instances
have no published host ports.

Try `POST http://localhost:8090/check` using the existing JSON API, or run:

```powershell
go run -buildvcs=false ./cmd/smoke
```

The smoke tool sends 12 checks with a unique key to the stock `login` rule. It
verifies five allowed, seven blocked, three distinct upstream instances, and
three healthy Prometheus targets. Degraded decisions fail the check. This
expects the supplied cluster configuration and is not a load benchmark. With
custom ports, add `-url http://localhost:8091 -prometheus http://localhost:9091`.
Host Go is optional for running the cluster and only needed for this smoke tool.

Nginx adds `X-RateLimit-Instance` with the selected upstream address. Its
`/metrics` endpoint shows one selected instance; use Prometheus to aggregate
all three. [Observability queries](observability.md) work across those targets.

The application image is built once and shared by all instances. It compiles
with Go 1.26, CGO disabled, trimmed paths, and stripped debug symbols, then runs
from `scratch` as UID/GID 65532. The final image includes CA certificates and
default memory rules. Compose mounts `deploy/rules.cluster.yaml` read-only to
select Redis mode and gives the app a read-only filesystem. It allows 15 seconds
for shutdown, covering the server's 10-second request drain.

The binary's `-healthcheck URL` mode probes HTTP and exits. Docker calls its
readiness endpoint without adding a shell, curl, or wget to the server image.
Redis becomes healthy before apps start; Nginx and Prometheus wait for all apps.
Runtime Redis outages follow the configured [failure policy](resilience.md).
Container health alone does not restart apps or discard fail-open decisions.

Nginx refreshes Docker DNS names for recreated instances. It disables upstream
retries so an ambiguous quota check is never replayed. See the
[Nginx upstream documentation](https://nginx.org/en/docs/http/ngx_http_upstream_module.html)
for DNS resolution behavior. All instances use the same rules, prefix, and Redis.

Inspect and stop the stack:

```powershell
docker compose ps
docker compose logs -f
docker compose down
```

Redis persistence is disabled for this local deployment; restarting Redis loses
quota state. Prometheus uses the named `prometheus-data` volume. Ordinary `down`
preserves it; `docker compose down --volumes` also discards metrics history.
The earlier Redis-only development stack uses a separate file and project.

`.github/workflows/ci.yml` runs on pushes, pull requests, and manual dispatch:

- The quality job verifies modules and formatting, builds and vets all code,
  runs unit/Redis integration tests with the race detector, and runs
  golangci-lint 2.13.1. It uses a Redis 7 service container. The linter includes
  opt-in test files, but Docker-controlling chaos and standalone Prometheus
  tests are not part of the race command.
- The containers job builds the image, starts the healthy cluster, runs the
  smoke tool, prints diagnostics on failure, and removes its cluster and test
  metrics volume. This tests real Prometheus scraping as part of deployment.

The workflow uses Go 1.26 and read-only repository permissions. It does not push
images or deploy externally. The README badge links to the real workflow; its
hosted result becomes available after these changes are pushed. Local checks
do not establish a green hosted run.

Local Make targets include `lint` (golangci-lint installed), `vet`, `test`,
`test-integration`, `up`, `down`, and `smoke`. The pinned linter also runs in
Docker from PowerShell:

```powershell
docker run --rm --mount "type=bind,source=$($PWD.Path),target=/src,readonly" `
  --workdir /src golangci/golangci-lint:v2.13.1 golangci-lint run
```

For Windows without a C compiler, use the [Linux race command](redis.md) with
the full stack's network and `REDIS_TEST_ADDR=redis:6379`. Tests use isolated
key prefixes. The module now matches `github.com/luvkumar404/rate-limiter`;
consumers of the placeholder path need to update imports.

Local verification on 2026-11-14 passed build, vet, module verification,
formatting, golangci-lint (zero issues), and race-enabled Redis integration
tests. All six containers were healthy, and the final rebuilt cluster passed
the smoke command. Docker reported an image size of 5,859,565 bytes and runtime
user 65532:65532. Hosted CI verification remains pending workflow publication.
