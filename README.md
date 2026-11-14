# ratelimiter

A Go rate limiting service under development. See [PRD.md](PRD.md) for the
requirements and [implementation progress](docs/progress.md) for current scope.

Requires Go 1.22 or later. The implementation has no external dependencies.

```sh
go build ./...
go vet ./...
go test ./...
go run ./cmd/server
go run ./examples/token-bucket
```

Make targets: `build`, `test` (race detector), `lint` (go vet), `run`, and `bench`.
The race detector requires CGO and a compatible C compiler on Windows.

The token bucket implements lazy refill, configurable burst, per-key isolation,
and background idle eviction. Its capacity is `limit + burst`, and its refill
rate is `limit / window`. The demo allows three immediate calls, blocks the next
two, and allows another after a 500 ms refill. Call `Close` on each bucket to
stop its cleanup worker. This implementation only shares state within one process.

On this Windows workspace, elevated builds hit Git's ownership check. Add
`-buildvcs=false` to Go commands if you encounter that error, for example
`go test -buildvcs=false ./...`. This disables optional VCS metadata stamping.

The module path `github.com/ratelimiter/ratelimiter` is a placeholder. The HTTP
API, Redis backend, and deployment are not implemented yet. No distributed
correctness or throughput claims have been verified.

See [benchmark notes](docs/benchmarks.md) for the measured local microbenchmark.
