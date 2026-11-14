# Algorithm selection

Both memory and Redis backends implement the same `Limiter` interface. The
server calls `store.NewLimiter` with the validated rule and optional Redis pool.
Changing `algorithm` in YAML and restarting selects the implementation without
code changes. Each algorithm has its own Redis key namespace. Changing algorithms
starts a new budget, and old state expires through its existing TTL. Restart all
instances with consistent config when changing rules.

| Algorithm | Enforcement | State per active key | Best fit |
|---|---|---|---|
| `token_bucket` | Initial capacity `limit + burst`, refills `limit / window` continuously | O(1) | APIs that allow short bursts and need a steady long-term rate |
| `sliding_window` | At most `limit` admitted events in the preceding `window` | O(limit) | Strict rolling quotas such as login attempts |
| `fixed_window` | At most `limit` admissions in each Unix-epoch-aligned interval | O(1) | Simple baseline or inexpensive aligned counters |

The sliding implementation is an exact log, not a weighted counter
approximation. It retains only admitted events in `(now - window, now]`.
An event exactly one window old no longer counts; blocked calls do not extend
the event log. `retry_after_ms` is based on expiry of the oldest admitted event.
New competing requests may consume that freed capacity before a retry arrives.

In Redis, sliding windows use a sorted set plus a saved clock key. Each event
has a random 128-bit member ID; `ZADD NX` rejects a collision rather than
silently merging two admissions. One Lua script reads Redis TIME, prunes expired
scores, counts, decides, records an allowed event, and refreshes both TTLs.
The memory version uses a timestamp slice with lazy pruning and a mutex.
The Redis work is O(log n + k) for n retained events and k removals, and the
memory implementation can copy O(n) timestamps when pruning. Redis documents
these operations in [ZREMRANGEBYSCORE](https://redis.io/docs/latest/commands/zremrangebyscore/)
and [ZADD](https://redis.io/docs/latest/commands/zadd/).

Fixed windows are aligned to the Unix epoch, not the first request. A 10-second
rule resets at timestamps 0, 10, 20, and so on. With a limit of two, two requests
at 9.999 seconds and two more at 10.000 seconds can all pass. Each fixed interval
respects its own limit, but this algorithm permits up to twice the limit around
a boundary. Use sliding windows when that behavior is undesirable. Retry time
is the duration until the next fixed boundary.

Token buckets permit capacity-sized immediate bursts and accumulate refill
continuously. They do not promise an exact rolling-window quota. They use a
constant amount of state; the Lua script and memory check perform constant
algorithmic work per decision. Retry time is how long until one token refills.

`burst` defaults to zero and must be zero for both window algorithms. Limits
and durations must be positive; Redis durations must be whole microseconds.
Redis scripts use Redis TIME, while memory implementations accept an injected
clock for deterministic tests. All algorithms guard against time moving
backwards. Denied checks consume no additional quota.

Idle memory keys are removed by a cleanup worker. Redis sliding keys expire
after a window of inactivity; fixed keys expire at the next boundary; token
keys expire after enough idle time to refill capacity. Redis TTLs include a
millisecond rounding margin. A fixed-window hash may remain briefly beyond its
boundary because of that margin; the script still resets its counter at the
exact logical boundary.

The sample configs exercise all three algorithms: `api-default` is a token
bucket, `login` is a sliding window, and `fixed-demo` is a fixed window.
Existing running binaries must be restarted to pick up these changes.

The shared suite runs quota, remaining count, retry, key isolation,
cancellation, and 1,000-way concurrent access checks against all six backend
and algorithm combinations. Deterministic tests cover staggered sliding events,
cutoff equality, fixed boundary renewal, and clock regression. Integration tests
also cover real expiry and script cache misses. Separate HTTP tests load each
algorithm from YAML and verify shared enforcement across two independent server
instances and Redis pools. See [Redis test commands](redis.md).
