# Phase 9 measurements

Measured on 2026-11-15 on a Windows/amd64 Intel Core i5-8350U workstation,
using Docker Desktop/WSL2 with eight virtual CPUs and 7.677 GiB available to
Docker. k6 1.0.0 runs inside the same Docker network as Nginx, three Go service
instances, Redis 7 and Prometheus. Other development containers remain running;
there is no CPU or memory isolation. These are single local samples, not a
production capacity estimate. The server image uses Go 1.26 and the normal
50 ms Redis deadline, with fail-closed policy for the benchmark.

**The 10k checks/s and p99 <10 ms targets are not met.** All nine high-load
cases have invalid decisions and dropped iterations. The original diagnostics
showed Redis I/O timeouts and circuit-breaker activity. This identifies observed
failure behavior, not its root cause; CPU, logging, Docker scheduling, client
capacity and Redis should be profiled before tuning or making capacity claims.
Generated logs have been removed from the repository; rerunning the suite
creates ignored diagnostic logs alongside the new results.

Reproduce with [the load-test runner](../loadtest/README.md):

```powershell
./loadtest/run.ps1 -Rates 1000,5000,10000 -Duration 20s
./loadtest/run.ps1 -Rates 100 -Duration 20s
```

Each case uses 1,000 round-robin keys, limit 100/minute, zero burst, a fresh key
namespace, 30 setup warm-up requests and a five-second settling period. Offered
load lasts 20 seconds, with up to five seconds to drain. All HTTP requests pass
through Nginx. Latency excludes setup and includes all measured responses,
including errors; failed rows do not establish valid-decision latency or capacity.
Whole-run rates below use k6's reported elapsed duration, including setup/drain,
and count both allows and denials. Valid/s excludes protocol/backend failures.

## Distributed correctness

[Raw result](../loadtest/results/20261115-final/correctness.json): **100 allowed,
900 blocked, zero invalid decisions, zero dropped iterations** for 1,000 checks
against one shared sliding-window key. All three upstreams handled measured
traffic. Eight VUs finish within the 20-second deadline and therefore within
the one-minute quota window. A second independent run is preserved with the
baseline results. Fixed-window boundaries and token refill have different
admission bounds; the existing Redis integration suite covers those algorithms.

## Offered-load sweep

[Raw summaries](../loadtest/results/20261115-final/summary.md) also
include resolved configuration, image identities, environment and setup data.
Each row below has zero valid blocked responses: successful admissions stayed
below the per-key quota while timeouts/circuit rejections dominated other checks.
Completed attempts are **not** successful service throughput.

| Algorithm | Offered/s | Whole-run completed/s | Whole-run valid/s | p50 ms | p95 ms | p99 ms | Invalid | Dropped |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Token bucket | 1,000 | 686.0 | 247.9 | 11.91 | 146.18 | 270.24 | 63.87% | 1,990 |
| Token bucket | 5,000 | 1,203.1 | 447.8 | 54.21 | 246.53 | 396.74 | 62.78% | 68,334 |
| Token bucket | 10,000 | 1,311.6 | 423.0 | 42.13 | 197.41 | 320.42 | 67.75% | 161,193 |
| Sliding window | 1,000 | 664.0 | 317.5 | 17.63 | 184.09 | 307.01 | 52.18% | 2,526 |
| Sliding window | 5,000 | 1,541.1 | 309.2 | 37.68 | 177.74 | 311.80 | 79.94% | 59,048 |
| Sliding window | 10,000 | 1,827.9 | 223.7 | 29.18 | 139.12 | 270.45 | 87.76% | 151,439 |
| Fixed window | 1,000 | 685.1 | 194.6 | 12.84 | 125.23 | 251.55 | 71.60% | 2,009 |
| Fixed window | 5,000 | 1,967.6 | 499.7 | 30.52 | 138.98 | 232.65 | 74.60% | 48,139 |
| Fixed window | 10,000 | 1,361.8 | 315.2 | 44.27 | 183.85 | 299.47 | 76.85% | 163,527 |

The initial [diagnostic trial](../loadtest/results/20261115-local/correctness.json)
used 32 VUs without setup warm-up. It had 95 invalid decisions and is not a
correctness proof. The final correctness test uses eight VUs and warm-up, while
the performance tests retain the original deadline and expose overload failures.

## Lower-load baseline

At an offered 100 checks/s for 20 seconds, all three algorithms return only valid
decisions with no dropped iterations. This is an allow-only sample: no key reaches
100 checks. Token bucket meets the latency target at this rate; the window
algorithms narrowly miss it. No rate between 100 and 1,000/s was explored, so these
samples do not locate the saturation point.

[Baseline raw results](../loadtest/results/20261115-baseline/summary.md):

| Algorithm | Completed checks | Whole-run valid/s | p50 ms | p95 ms | p99 ms | Invalid | Dropped |
|---|---:|---:|---:|---:|---:|---:|---:|
| Token bucket | 2,001 | 76.2 | 1.98 | 5.98 | 8.64 | 0 | 0 |
| Sliding window | 2,001 | 76.2 | 1.90 | 5.69 | 10.35 | 0 | 0 |
| Fixed window | 2,000 | 76.4 | 2.20 | 6.05 | 11.38 | 0 | 0 |

The arrival executor can schedule one extra iteration at the endpoint. Whole-run
rates include setup; 100/s describes the 20-second offered-load interval. The
runner correctly exits unsuccessfully for sliding/fixed latency failures, even
though every request is authoritative. The repeated correctness case also passes
100 allowed / 900 blocked with zero invalid decisions.

## Algorithm microbenchmarks

Go 1.26.0 on Windows/amd64, eight logical CPUs, measured separately after the
load generator and benchmark containers stopped. Reproduce:

```sh
go test -buildvcs=false -run '^$' -bench . -benchmem -benchtime=200ms -count=3 ./internal/limiter
```

[Raw output](../loadtest/results/20261115-microbench.txt). Values are medians of
three samples; short runs show substantial variability.

| Algorithm/path | Median ns/op | B/op at median sample |
|---|---:|---:|
| Token bucket / allowed | 222.7 | 0 |
| Token bucket / denied | 250.9 | 0 |
| Sliding window / allowed | 559.8 | 120 |
| Sliding window / denied | 275.3 | 0 |
| Fixed window / allowed | 303.9 | 0 |
| Fixed window / denied | 353.5 | 0 |
| Original mixed token bucket benchmark | 201.8 | 0 |

The new benchmarks use one contended key, a stationary injected clock and
separate allowed/denied paths. Allowed capacity scales with the iteration count;
denied paths exhaust a one-request budget before timing. Sliding allowed checks
retain all events and include slice growth/search costs: its memory usage grows
with the benchmark iteration count, unlike the constant-sized denied state.
Go rounds allocs/op down to zero here even when occasional slice growth allocates;
the nonzero B/op captures that cost. These figures exclude HTTP and Redis, and
the stationary-clock cases exclude real clock-read costs. The retained original
mixed token-bucket benchmark uses the real clock. None predicts network latency.

## Initial in-memory benchmark

Measured on 2026-11-14 using Go 1.26.0, Windows/amd64, Intel Core i5-8350U.
One sample, eight logical CPUs; results depend on local machine load.

```sh
go test -buildvcs=false -run '^$' -bench . -benchmem ./internal/limiter
```

```text
BenchmarkTokenBucket-8  14916466  93.77 ns/op  0 B/op  0 allocs/op
```

This benchmark checks one shared in-memory key in parallel, including its mutex,
clock read, refill, and decision. It mixes allowed and blocked decisions. It
excludes HTTP and Redis and does not establish distributed throughput or p99.
See the cluster measurements above for HTTP/Redis behavior.
