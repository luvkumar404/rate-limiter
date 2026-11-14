# Initial in-memory benchmark

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
Cluster measurements remain pending Phase 9.
