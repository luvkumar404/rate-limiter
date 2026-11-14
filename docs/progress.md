# Implementation progress

Development follows the phase gates in PRD.md. Optional dashboard work is deferred.

- Phase 0: skeleton, core contract, Makefile, license, documentation, and local
  Git initialization complete. Build and vet pass with `-buildvcs=false`.
  GitHub push is pending a repository destination; no remote is configured.
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
- Phases 5–11: pending.

Verification on 2026-11-14:

- Windows build, vet (including integration-tagged code), and integration tests
  pass. Coverage: API 98.6%, config 100%, limiter 91.2%, store 93.7% after Phase 4.
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
- Tests verify Redis TIME, refill, fractional state, expiry, clock regression,
  script-cache fallback, key/rule isolation, and bounded Redis I/O. Phase 4 adds
  exact sliding cutoffs, staggered events, fixed boundary resets, aligned
  timestamps, cancellation, and cleanup for all new state. Linux race testing
  caught a fixed-window comparison issue involving monotonic timestamps;
  fixed epoch calculations now consistently compare wall-clock timestamps.

Shutdown tests exercise context cancellation and request draining; the Windows
console Ctrl+C event has not been automated. See [Redis testing](redis.md) for
reproduction commands.

The next milestone is resilience (Phase 5). Scope is supplied by the caller
through the request key. Changing algorithms starts a new budget; restart
instances with consistent rule settings. Nonzero burst is rejected for window
algorithms. Failure policy values are validated; policy enforcement and the
circuit breaker come in Phase 5. Redis must be reachable at startup;
after startup, errors return 503. Three-instance cluster performance remains
unmeasured and is scheduled for Phase 9.

The module path is a placeholder until a GitHub repository is chosen. Publishing,
releases, portfolio changes, and performance claims require actual deployment
and measured results.
