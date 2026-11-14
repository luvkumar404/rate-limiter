# Redis backend

Start the isolated development Redis 7 dependency on localhost port 16379:

```sh
docker compose -p ratelimiter-phase3 -f deploy/compose.redis.yaml up -d --wait
go run ./cmd/server -config rules.redis.yaml -addr :8080
```

In another terminal, run a second instance with `-addr :8081`. Both instances
must use the same rule parameters, database, and key prefix. State is shared by
algorithm, rule name, and caller-supplied key. Separate deployments should use different
prefixes. Changing a prefix starts a new budget; rule changes require consistent
configuration across instances. There is no dynamic configuration rollout yet.

`rules.yaml` still defaults to process-local memory mode. `rules.redis.yaml`
selects Redis explicitly. Supported Redis config fields:

| Field | Default | Meaning |
|---|---|---|
| `addr` | `localhost:6379` | Redis TCP address; sample config uses port 16379 |
| `username`, `password` | empty | Redis credentials; environment `REDIS_PASSWORD` overrides password |
| `db` | `0` | Database index |
| `prefix` | `ratelimiter` | Deployment namespace |
| `pool_size` | `32` | Maximum pool size per service instance |
| `dial_timeout` | `1s` | Connection establishment timeout |
| `operation_timeout` | `50ms` | Overall deadline for each check or readiness ping |
| `read_timeout`, `write_timeout`, `pool_timeout` | operation timeout | I/O and connection pool wait limits |

All configured timeouts must be positive (zero selects defaults). Existing
caller deadlines can shorten the operation timeout. An unreachable Redis fails
startup. After startup, backend errors return 503 and readiness returns 503;
process health remains independent. Fail-open/fail-closed policy enforcement and
the circuit breaker remain Phase 5 work. Clients must not automatically retry
an ambiguous failed check: a lost response may follow a committed decision.

The embedded script lives in
[token_bucket.lua](../internal/store/scripts/token_bucket.lua). It reads Redis
`TIME`, stores fractional `tokens` and microsecond `last_refill` in one hash,
computes the decision, and refreshes a millisecond TTL on every check, including
denied checks. Capacity is `limit + burst`; refill is `limit / window`. TTL covers
the time to refill full capacity, plus a millisecond to account for rounding.
Redis windows must be multiples of one microsecond.

Phase 4 adds [sliding_window.lua](../internal/store/scripts/sliding_window.lua)
and [fixed_window.lua](../internal/store/scripts/fixed_window.lua). Sliding
windows use an exact sorted-set log and a clock key; both expire after a window
of inactivity. Fixed windows use a hash counter aligned to the Unix epoch and
expire at the interval boundary. All three scripts use Redis TIME and the same
EVALSHA/NOSCRIPT behavior. See [algorithm tradeoffs](algorithms.md).

Redis guarantees atomic script execution, so other requests cannot interleave
the read and update. See [Redis Lua scripting](https://redis.io/docs/latest/develop/programmability/eval-intro/).
The client uses `EVALSHA`, falling back to `EVAL` only on `NOSCRIPT`; see
[Redis NOSCRIPT behavior](https://redis.io/docs/latest/develop/reference/errors/common/noscript/).
Network retries are disabled to avoid consuming twice after a lost reply.
Keys encode the rule name and hash the caller key to prevent delimiter
collisions and avoid storing raw caller identifiers in Redis key names.

Run ordinary tests without Redis:

```sh
go test -buildvcs=false ./...
```

Run the Redis integration suite from PowerShell:

```powershell
$env:REDIS_TEST_ADDR = 'localhost:16379'
go test -buildvcs=false -tags integration -v ./...
```

On Linux/macOS use
`REDIS_TEST_ADDR=localhost:16379 go test -buildvcs=false -tags integration -v ./...`.
The integration suite defaults to `localhost:6379` when that variable is absent,
and fails if Redis is unavailable. Use a dedicated development instance. Tests
use unique prefixes, clean up their own keys, and never run `FLUSHDB`,
`FLUSHALL`, or `SCRIPT FLUSH`.

The tests cover concurrent decisions across independent clients, Redis time,
fractional refill, clock regression, capacity caps, key/rule isolation, TTL
refresh and actual expiration, corrupt state, and script-cache misses. The HTTP
integration test starts two independently configured servers with separate Redis
connection pools in the test process for each algorithm. Each run sends 1,000
checks with limit 100 and a 24-hour window. Token buckets with burst 20 allow
120 and block 880; sliding and fixed windows with burst 0 allow 100 and block
900. No requests fail. The long window prevents a complete token refill during
the test.
This verifies correctness; it is not a throughput or latency benchmark.

Windows does not have a C compiler configured here. Run race-enabled tests in a
Linux Go container on the same Redis network instead (PowerShell):

```powershell
docker run --rm --network ratelimiter-phase3_default `
  --mount "type=bind,source=$($PWD.Path),target=/src,readonly" --workdir /src `
  --env REDIS_TEST_ADDR=redis:6379 golang:1.26-bookworm `
  go test -buildvcs=false -race -tags integration ./...
```

The source mount is read-only; compiler caches and temporary files stay inside
the disposable container. The development Redis container has persistence
disabled and should not hold data you need to keep.

Verified on 2026-11-14 against Redis 7.4.9: Windows build, vet, and integration
tests pass; the complete race-enabled integration suite also passes in the
Linux Go container. Store statement coverage from the Windows integration run
is 93.7% after adding all three algorithms. Temporary verification containers
are removed after testing. Start the development Redis with
the Compose command above before running the service or integration suite.
