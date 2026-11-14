# Implementation progress

Development follows the phase gates in PRD.md. Optional dashboard work is deferred.

- Phase 0: skeleton, core contract, Makefile, license, documentation, and local
  Git initialization complete. Build and vet pass with `-buildvcs=false`.
  GitHub push is pending a repository destination; no remote is configured.
- Phase 1: token bucket, injected clock, cleanup worker, tests, runnable example,
  and benchmark implemented. Tests pass with 97.0% limiter statement coverage.
  Race-detector validation passes in a Linux Go container.
- Phase 2: implemented. YAML validation, named and isolated rules, `/check`,
  `/healthz`, memory-mode `/readyz`, JSON request logs, and signal-driven graceful
  shutdown are available. Tests cover request validation, allowed/blocked
  responses, config failures, real HTTP serving, and in-flight request draining.
- Phase 3: implemented and verified against Redis 7.4.9. The embedded token
  bucket Lua script uses Redis TIME, atomic state updates, fractional refill,
  idle expiry, and EVALSHA with NOSCRIPT fallback. Connection options, Redis
  readiness, sample config, and a Compose dependency are included.
- Phases 4–11: pending.

Verification on 2026-11-14:

- Windows build, vet (including integration-tagged code), and integration tests
  pass. Coverage: API 98.6%, config 100%, limiter 97.0%, Redis store 98.1%.
- The full suite passes with `-race -tags integration` inside
  `golang:1.26-bookworm`, including all earlier phases. Windows native race
  testing still requires a C compiler; Linux container verification resolves
  the previously pending race check without modifying the Windows toolchain.
- Two HTTP server instances with independent Redis connection pools enforce one
  shared budget: 1,000 checks, 120 allowed, 880 blocked, zero errors (limit 100,
  burst 20, 24-hour window). A separate 1,000-goroutine store test also admits
  exactly 120 across two clients.
- Tests verify Redis TIME, refill, fractional state, expiry, clock regression,
  script-cache fallback, key/rule isolation, and bounded Redis I/O.

Shutdown tests exercise context cancellation and request draining; the Windows
console Ctrl+C event has not been automated. See [Redis testing](redis.md) for
reproduction commands.

The next milestone is additional algorithms and the shared factory (Phase 4).
Scope is supplied by the caller through the request key. Only token bucket is
currently accepted. Failure policy values are validated; policy enforcement
and the circuit breaker come in Phase 5. Redis must be reachable at startup;
after startup, errors return 503. Three-instance cluster performance remains
unmeasured and is scheduled for Phase 9.

The module path is a placeholder until a GitHub repository is chosen. Publishing,
releases, portfolio changes, and performance claims require actual deployment
and measured results.
