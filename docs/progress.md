# Implementation progress

Development follows the phase gates in PRD.md. Optional dashboard work is deferred.

- Phase 0: skeleton, core contract, Makefile, license, documentation, and local
  Git initialization complete. Build and vet pass with `-buildvcs=false`.
  The repository now has origin `luvkumar404/rate-limiter`; Phase 8 aligns the
  module path and public imports with it.
- Phase 1: token bucket, injected clock, cleanup worker, tests, runnable example,
  and benchmark implemented. All token bucket tests continue to pass.
  Race-detector validation passes in a Linux Go container.
- Phase 2: implemented. YAML validation, named and isolated rules, `/check`,
  `/healthz`, memory-mode `/readyz`, JSON request logs, and signal-driven graceful
  shutdown are available. Tests cover request validation, allowed/blocked
  responses, config failures, real HTTP serving, and in-flight request draining.
- Phase 3: implemented and verified against Redis 7.4.9. The embedded token
  bucket Lua script uses Redis TIME, atomic state updates, fractional refill,
  idle expiry, and EVALSHA with NOSCRIPT fallback. Connection options, Redis
  readiness, sample config, and a Compose dependency are included.
- Phase 4: implemented. Exact sliding logs and epoch-aligned fixed windows are
  available in memory and Redis. A validated rule contract and shared factory
  select all three algorithms from YAML. Shared tests run against all six
  algorithm/backend combinations, and both sample configs demonstrate each
  algorithm. [Algorithm tradeoffs](algorithms.md) document accuracy and storage.
- Phase 5: implemented. Redis limiters apply the configured fail-open or
  fail-closed policy through a shared circuit breaker with closed/open/half-open
  states. Default operation deadlines remain 50 ms; cooldown defaults to five
  seconds after five consecutive failures. Startup during Redis outages works,
  readiness reflects connectivity, and incoming checks recover automatically.
  [Failure handling](resilience.md) documents behavior and chaos-test commands.
- Phase 6: implemented. Per-instance Prometheus registries expose `/metrics`,
  decision counts, check latency histograms, actual Redis errors, circuit state,
  and runtime/process metrics. Check responses include quota headers and rounded
  Retry-After values. Request IDs propagate through HTTP responses, limiter
  context, access logs, fallback logs, and circuit-transition logs.
  [Observability](observability.md) includes setup, semantics, and PromQL.
- Phase 7: implemented. Public `pkg/limiter` constructors, contracts, Redis
  pools, and the rule factory support external Go consumers. `pkg/middleware`
  supports embedded and remote checks, header/custom/socket-IP keys, explicit
  trusted-proxy CIDRs, request correlation, deadlines, and failure policies.
  The [demo](../examples/demo-app/README.md) runs in both modes and the
  [middleware guide](middleware.md) documents setup and ownership.
- Phase 8: implemented and locally verified. A non-root static Go image and
  root Compose stack run three instances, Redis, Nginx, and Prometheus. The
  image is built once and shared. Readiness, direct per-instance scrapes,
  no-retry proxying, and a deployment smoke command are included. GitHub Actions
  defines build/vet/format, race/integration, golangci-lint, image-build, and
  cluster-smoke checks. README badges link to the repository. The hosted-CI
  "green on push" gate remains pending a push of these workflow changes.
- Phase 9: load scripts, shared-quota correctness, per-algorithm throughput and
  latency measurements, and raw artifacts are implemented. The 1,000-request
  three-instance correctness check passes (100 allowed, 900 blocked, no invalid
  decisions). The 1k/5k/10k offered-rate sweeps miss the performance targets;
  failures and dropped iterations are preserved rather than counted as capacity.
  See [measurements and limitations](benchmarks.md). The k6 correctness check is
  included in CI; hosted verification remains pending. The caller authorized
  starting this phase while the hosted Phase 8 gate was still pending.
  At 100 offered checks/s, all three algorithms have zero invalid decisions and
  drops; p99 is 8.64/10.35/11.38 ms for token/sliding/fixed respectively.
  Allowed/denied Go microbenchmarks run for all three algorithms with raw output.
- Phases 10–11: pending; dashboard remains optional.

Verification on 2026-11-15:

- Unit tests and vet pass with `-buildvcs=false`; golangci-lint 2.13.1 reports
  zero issues, including the integration/chaos/Prometheus build tags.
- Both final k6 correctness runs pass. Nine higher-rate and three baseline
  performance cases preserve all results, including failed latency, error and
  dropped-iteration thresholds. Benchmark containers and their volume are removed.
- All three algorithms' allowed/denied microbenchmarks pass for three samples
  each. Raw output and summary tables are linked from the benchmark report.
- Hosted CI has not been run or pushed from this session.

Verification on 2026-11-14:

- Phase 8: Windows build, module verification, formatting, and vet pass.
  Golangci-lint 2.13.1 reports zero issues, including opt-in test files. The
  complete unit/Redis integration suite passes with `-race` and coverage in
  Linux using Go 1.26. Required cleanup error handling and static-analysis
  findings were corrected without disabling those lint checks.
- The complete six-container stack becomes healthy. Plain Compose startup
  automatically builds the single shared image; the final rebuilt deployment
  passes the smoke command: three upstream instances, five allowed/seven blocked
  checks for one sliding-window key, and three healthy Prometheus targets.
  Docker reports the final image size as 5,859,565 bytes with user 65532:65532.
  Runtime Go filesystems are read-only. No cluster throughput claim is made.
- CI configuration and local equivalents are verified; no hosted Actions run
  for the new workflow has been observed. See [deployment](deployment.md).

- Windows build, vet (including integration-tagged code), and integration tests
  pass. Coverage: API 97.8%, config 100%, limiter 91.2%, store 93.7%, resilience
  98.8%, metrics 100%, request IDs 92.3%, middleware 98.1% after Phase 7. Vet also covers the
  opt-in chaos and real-Prometheus test code.
- The full suite passes with `-race -tags integration` inside
  `golang:1.26-bookworm`, including all earlier phases. Windows native race
  testing still requires a C compiler; Linux container verification resolves
  the previously pending race check without modifying the Windows toolchain.
- Two HTTP server instances with independent Redis connection pools enforce one
  shared budget for each algorithm: 1,000 checks per algorithm, limit 100,
  24-hour window. Token buckets with burst 20 allow 120 and block 880; both
  window algorithms with burst 0 allow 100 and block 900. All runs have zero
  backend errors. The shared suite also verifies 1,000 concurrent checks per
  algorithm/backend pair without over-admission.
- Phase 6 verifies both servers' exposed metrics against observed HTTP results:
  500 counter increments and histogram observations per instance, with aggregate
  allowed/blocked counts matching the 1,000 requests for each algorithm.
- A real Prometheus 3.2.1 scraper ingests a temporary live memory-mode service:
  up=1, allowed=2, blocked=1, histogram count=3, Redis errors=0, circuit=closed.
  The test queries the Prometheus API and removes its own scraper afterward.
- Metric tests verify all outcomes, isolated registries, actual Redis failures
  versus circuit short-circuits, cancellation exclusions, and live
  closed/open/half-open gauges. Header and log tests cover Retry-After rounding,
  quota/body consistency, ID validation, and correlation through failure logs.
- Phase 7 middleware tests verify embedded and remote application enforcement,
  validated remote responses, preserved application bodies and contexts,
  deadlines, cancellation, request-ID forwarding, explicit no-redirect behavior,
  failure policies, and trusted-proxy extraction resistant to spoofed prefixes.
- Two application instances sharing Redis admit exactly 25/200 checks with a
  token bucket (limit 20, burst 5), or 20/200 with each window algorithm. All
  remaining checks are blocked with zero errors, in both embedded and remote
  modes. The full Linux race suite includes these tests and both live demo modes.
- A separate temporary Go module imports only public packages, implements a
  custom limiter, and verifies first-allow/second-deny behavior for all three
  built-in memory algorithms. No consumer imports of internal packages are
  needed. The demo independently verifies live HTTP responses and shutdown in
  embedded and remote modes.
- Tests verify Redis TIME, refill, fractional state, expiry, clock regression,
  script-cache fallback, key/rule isolation, and bounded Redis I/O. Phase 4 adds
  exact sliding cutoffs, staggered events, fixed boundary resets, aligned
  timestamps, cancellation, and cleanup for all new state. Linux race testing
  caught a fixed-window comparison issue involving monotonic timestamps;
  fixed epoch calculations now consistently compare wall-clock timestamps.
- Phase 5 deterministic tests verify one probe under 1,000 concurrent callers,
  cooldown/reopening, successful and denied decisions, shared rule state,
  caller cancellation, and stale completions during and after recovery.
- The separate Windows chaos test passes for both failure policies and all
  algorithms against a dedicated Redis 7 container: startup with Redis stopped,
  automatic recovery, Redis killed after healthy traffic, policy-correct outage
  responses, and a second recovery with normal 429 enforcement. Health stays
  200 and readiness reports 503 during both outages. No application restart is
  needed. The Linux race suite excludes this Docker-controlling test and covers
  all unit and ordinary Redis integration tests.

Shutdown tests exercise context cancellation and request draining; the Windows
console Ctrl+C event has not been automated. See [Redis testing](redis.md) for
reproduction commands.

The next planned milestone is optional dashboard work (Phase 10), followed by
polish and shipping (Phase 11). Hosted CI and the performance targets remain
outstanding. Scope is supplied by the caller
through the request key. Changing algorithms starts a new budget; restart
instances with consistent rule settings. Nonzero burst is rejected for window
algorithms. During Redis errors or circuit cooldown, fail-open returns an allowed
decision marked degraded with unknown remaining quota represented by zero;
fail-closed returns 503. Ordinary quota denials remain 429. Redis availability
does not prevent HTTP startup. Readiness pings do not alter circuit state.
Three-instance measurements are recorded in Phase 9; 10k checks/s with p99 under
10 ms has not been demonstrated on this workstation.

The canonical module path is `github.com/luvkumar404/rate-limiter`. Hosted CI
activation is pending the next push. Releases and portfolio changes remain future
work. Performance statements must retain the measured error/drop rates and local
environment limitations.
