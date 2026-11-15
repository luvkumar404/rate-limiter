
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
  smoke tool, switches to benchmark rules, and runs the k6 100/minute shared-quota
  correctness check. It preserves the k6 JSON artifact, prints diagnostics on
  failure, and removes its cluster and test metrics volume. This tests real
  Prometheus scraping as part of deployment.

The workflow uses Go 1.26 and read-only repository permissions. It does not push
images or deploy externally. Both jobs pin `ubuntu-26.04`, the OS already used
by the inspected hosted run. Checkout v6, setup-go v6 and golangci-lint-action v9
already use Node.js 24. Upload-artifact is pinned to the verified v7.0.2 commit,
which also uses Node.js 24 and preserves the existing archived artifact inputs.
The README badge links to the real workflow; local checks do not establish a
green hosted run for new changes.

The complete logs of [run 43683313187](https://github.com/luvkumar404/rate-limiter/actions/runs/43683313187)
show that quality, image build, startup and smoke all passed. The k6 step failed
in `setup()` with `Expected 3 upstreams, saw 172.18.0.4:8080,172.18.0.3:8080`,
then exited 107 without running quota iterations. The benchmark override
recreated all three apps while Nginx stayed running. At 05:59:17 UTC its health
probe timed out connecting to `172.18.0.5:8080`, and Nginx temporarily disabled
that peer. k6's 30 discovery requests arrived at 05:59:25 UTC, during the
[default ten-second upstream failure timeout](https://nginx.org/en/docs/http/ngx_http_upstream_module.html#server).
Two healthy peers kept the proxy's overall health check green. This was a
configuration-rollout race, not an image-build, artifact-permission or quota
assertion failure.

CI now stops Nginx **before** replacing app containers. The following Compose
`up --wait` starts it again after all three backends are healthy, with fresh
upstream state. No sleeps, quota checks, application behavior or proxy retry
settings were changed. The artifact-action upgrade also removes the old
Node 20 runtime declaration that caused the forced-Node-24 deprecation notice.

To reproduce the corrected container sequence locally from PowerShell, use a
separate project and free ports:

```powershell
$env:COMPOSE_PROJECT_NAME = 'ratelimiter-ci-check'
$env:RATELIMITER_HTTP_PORT = '28090'
$env:RATELIMITER_PROMETHEUS_PORT = '29090'
New-Item -ItemType Directory -Force loadtest/results | Out-Null
docker compose config --quiet
docker compose up -d --build --wait --wait-timeout 120
go run -buildvcs=false ./cmd/smoke -url http://localhost:28090 -prometheus http://localhost:29090
docker compose stop nginx
docker compose -f docker-compose.yml -f loadtest/compose.yaml up -d --no-build --wait --wait-timeout 120
docker compose -f docker-compose.yml -f loadtest/compose.yaml run --rm --no-deps -e MODE=correctness -e OUTPUT=/results/correctness-ci-local.json k6 run /scripts/check.js
docker compose -f docker-compose.yml -f loadtest/compose.yaml down --volumes
Remove-Item Env:COMPOSE_PROJECT_NAME, Env:RATELIMITER_HTTP_PORT, Env:RATELIMITER_PROMETHEUS_PORT
```

On Linux, create `loadtest/results` and grant the k6 container write access as
the workflow does (`chmod 777 loadtest/results`). The expected result is 100
allowed, 900 blocked, zero invalid decisions and nonzero counts on all three
upstreams. A local run of the corrected rollout produced upstream counts
333/333/334 and zero dropped iterations; the image build and stock smoke also
passed. Validate workflow syntax with:

```sh
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yml
```

`.github/actionlint.yaml` adds the official `ubuntu-26.04` label to actionlint's
allowlist because v1.7.12 predates that hosted image. Its configuration key is
named `self-hosted-runner`, but this only extends static label validation; the
jobs still run on GitHub-hosted Ubuntu. Other workflow checks remain enabled.

Push the corrected workflow on your branch or open a pull request to execute
the new revision. Re-running the old failed run uses its old workflow revision.
Verify both jobs are green, the k6 artifact contains the counts above, and the
upload step no longer reports Node 20, `punycode` or `url.parse()` deprecations.

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
user 65532:65532. The later hosted Phase 8 run succeeded; the inspected Phase 9
run passed quality and smoke but failed k6 setup as described above. Hosted
verification of the corrected workflow still requires a new push or PR run.
