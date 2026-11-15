# Cluster load tests

Requires Docker Compose v2 with Docker running. The PowerShell runner works in
Windows PowerShell or PowerShell 7 (`pwsh` on Linux/macOS). No host k6 install is
needed. From the repository root:

```powershell
./loadtest/run.ps1
# Shorter/custom offered rates:
./loadtest/run.ps1 -Rates 1000,5000,10000 -Duration 20s -Run my-run
./loadtest/summarize.ps1 -Directory loadtest/results/my-run
# Use an existing service image without rebuilding:
./loadtest/run.ps1 -SkipBuild
```

The runner builds the service, starts an isolated `ratelimiter-bench` Compose
project on host ports 18090/19090, runs k6 1.0.0 inside the Docker network against
Nginx, and removes its containers and volume in `finally`. The regular development
stack is separate. Do not run multiple benchmark runners concurrently. Raw k6
summaries, logs, resolved Compose config, image IDs, and Docker environment details
are saved under `loadtest/results/<run>/`. A failing threshold makes the runner
exit unsuccessfully after preserving all cases and writing `summary.md`.

`rules.yaml` uses fail-closed decisions, limit 100/minute and zero burst for all
algorithms. The script rejects degraded decisions, HTTP/protocol errors, missing
quota/request-ID headers, and unexpected upstreams. An initial health probe
discovers exactly three upstream addresses, and each must handle measured checks.

The correctness case sends exactly 1,000 shared-key sliding-window checks using
8 concurrent VUs within a 20-second deadline. It requires **100 allowed, 900
blocked**, zero invalid decisions, and traffic across all three instances. A fresh
key prevents earlier runs consuming quota. Sliding windows avoid fixed-window
boundary resets and token-bucket refill; those algorithms have different valid
admission bounds and are also covered by the existing distributed integration
suite. The one-minute correctness window cannot expire within this test.

Performance cases offer 1k, 5k and 10k checks/s for 20 seconds per algorithm, with
64 preallocated VUs and a cap of 256. Requests cycle across 1,000 keys with a fresh
namespace per case. Traffic starts allowed and can become blocked when quotas are
exhausted. Token refill and epoch-aligned fixed-window boundaries affect the
allowed/blocked mix; this is a mixed-decision workload, not an allow-only test.
Cases run sequentially. Setup sends 30 warm-up checks at 10/s and waits five
seconds before measurement; those requests use a separate key and are excluded
from the latency percentiles. Measured keys start cold. These short single samples are exploratory;
repeat runs and use longer durations for capacity planning.

The p50/p95/p99 figures use only requests tagged `phase:load`, excluding setup
health probes. They measure k6 HTTP request duration over the Docker network
(Nginx, service and Redis), excluding client-side connection setup, VU scheduling
and script/JSON overhead. Completed/s is k6's iteration rate, including script
overhead, setup and drain time, not just allowed requests. It is a conservative
whole-run rate rather than the configured scenario arrival rate. `dropped_iterations` reports offered checks
the generator could not start. Valid/s counts authoritative allowed + blocked
decisions divided by k6's whole-run duration. Nonzero drops or invalid responses disqualify a
capacity claim. A p99 >=10ms fails the latency target but its measurements remain
useful. Quota 429s are expected and count as successful protocol decisions.

To run one case manually (create the results directory first):

```sh
docker compose -p ratelimiter-bench -f docker-compose.yml -f loadtest/compose.yaml up -d --build --wait
docker compose -p ratelimiter-bench -f docker-compose.yml -f loadtest/compose.yaml run --rm -e MODE=correctness k6 run /scripts/check.js
docker compose -p ratelimiter-bench -f docker-compose.yml -f loadtest/compose.yaml run --rm -e MODE=throughput -e RULE=token_bucket -e RATE=10000 -e DURATION=20s -e OUTPUT=/results/token-10000.json k6 run /scripts/check.js
docker compose -p ratelimiter-bench -f docker-compose.yml -f loadtest/compose.yaml down --volumes
```

On Linux, give the k6 container's user write access to the results directory
before running (for a disposable local results directory: `chmod 777 loadtest/results`).

For manual runs, set `RATELIMITER_HTTP_PORT=18090` and
`RATELIMITER_PROMETHEUS_PORT=19090` in your shell first if the default ports are
occupied. See [benchmark results](../docs/benchmarks.md) for measured outcomes.
