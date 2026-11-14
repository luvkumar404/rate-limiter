# Go middleware

`pkg/middleware` protects a `net/http` handler using a shared embedded limiter
or the remote `/check` service. `pkg/limiter` exposes all three built-in
algorithms, their configuration, a rule factory, and Redis pools. Application
code imports these public packages; it does not import `internal` packages.

```go
import (
    "github.com/luvkumar404/rate-limiter/pkg/limiter"
    "github.com/luvkumar404/rate-limiter/pkg/middleware"
)
```

The module path matches the repository: `github.com/luvkumar404/rate-limiter`.
Local consumer modules can use a Go module `replace` pointing at this checkout
while changes have not yet been pushed.
The runnable [demo](../examples/demo-app/README.md) works directly from the repo.

The core integration is construct, configure, wrap:

```go
bucket, _ := limiter.NewTokenBucket(limiter.TokenBucketConfig{Limit: 100, Burst: 20, Window: time.Minute})
defer bucket.Close()
protect, _ := middleware.New(bucket, middleware.Options{Key: middleware.ClientIP()})
handler := protect(appHandler)
```

See the [demo source](../examples/demo-app/main.go) for constructor error handling
and graceful shutdown. Other embedded constructors are `NewSlidingWindow` and
`NewFixedWindow`, both taking `WindowConfig{Limit, Window}`. They implement the
same `Limiter` interface and expose `Close` for their cleanup workers.

For embedded distributed enforcement, create a public Redis pool and select a
named rule with `limiter.New(name, RuleConfig, redisPool)`. For example:

```go
pool, err := limiter.NewRedis(limiter.RedisOptions{Addr: "localhost:16379"})
if err != nil { return err }
defer pool.Close()
embedded, err := limiter.New("login", limiter.RuleConfig{
    Algorithm: limiter.SlidingWindowAlgorithm, Limit: 5, Window: time.Minute,
}, pool)
if err != nil { return err }
protect, err := middleware.New(embedded, middleware.Options{})
if err != nil { return err }
```

The caller owns all limiter, Redis, client, and transport lifecycles. Middleware
does not close them. Share a constructed instance across requests. Redis rules
must use the same name, parameters, prefix, and database across applications.
Passing nil Redis to the factory selects memory; those results implement
`io.Closer`. Algorithm semantics match the [service algorithms](algorithms.md).

Remote mode uses the same wrapper:

```go
remote, err := middleware.NewRemote("http://localhost:8082", "login", middleware.RemoteOptions{})
if err != nil { return err }
protect, err := middleware.New(remote, middleware.Options{Key: middleware.ClientIP()})
if err != nil { return err }
mux.Handle("GET /hello", protect(appHandler))
```

`NewRemote` takes the service base URL, not its `/check` endpoint. Plain path
prefixes are supported. Credentials, queries, fragments, and escaped base paths
are rejected. Reuse one `RemoteLimiter` per remote rule. It forwards only the
extracted key, configured rule, and correlation ID; the application's request
body and other headers remain with the application.

The client accepts only consistent 200/allowed or 429/blocked JSON decisions,
requires all core fields, bounds responses to 8 KiB, validates durations against
overflow, and preserves valid burst remaining counts above the steady limit.
New unknown response fields are tolerated. Unexpected statuses, malformed or
oversized bodies, and transport failures become errors. It never explicitly
retries or follows redirects, since each check can consume quota. A caller-
supplied HTTP client is copied without modifying its redirect policy, while its
transport remains shared; that transport must not replay quota-consuming calls.

Both the remote client and middleware default to 100 ms timeouts; the shorter
deadline wins. An existing HTTP client's shorter timeout also applies.
`RemoteOptions.Timeout` and `Options.Timeout` configure them. The middleware
deadline applies only to `Allow`, not key extraction or the downstream handler.
Custom embedded implementations must honor context and be safe for concurrent
calls. Caller cancellation never becomes a fail-open application request.

`Options.Key` defaults to `ClientIP()`, which uses only the TCP socket peer and
ignores forwarding headers. IPv4-mapped IPv6 addresses are normalized to IPv4;
IPv6 addresses use canonical text without interface zones. Requests using the
same limiter and IP share a budget across all routes protected by that limiter.

`Header("X-API-Key")` requires one nonblank header value and trims surrounding
whitespace. It extracts a key without authenticating it. Run authentication
middleware first when keys depend on authenticated identities. A custom
`KeyFunc`, with signature `func(*http.Request) (string, error)`, can read that
identity from context and combine it with a route or method. Extractor errors,
blank keys, and keys longer than 512 bytes return 400 without calling the limiter.

Use explicit CIDRs when the app sits behind trusted proxies:

```go
key, err := middleware.TrustedProxyClientIP("10.0.0.0/8", "2001:db8:ffff::/48")
if err != nil { return err }
protect, err := middleware.New(remote, middleware.Options{Key: key})
```

Forwarding is honored only if the socket peer is trusted. The extractor walks
`X-Forwarded-For` from right to left, stops at the first untrusted address, and
ignores values to its left. This prevents a caller-supplied leftmost address
from replacing the actual untrusted hop. Multiple XFF header values preserve
their chain order. Missing XFF uses the socket peer. A malformed address in the
trusted portion returns 400; chains are limited to 32 hops and 4096 bytes.
`Forwarded` and `X-Real-IP` are not used. Configure the proxy to supply raw IP
literals in XFF and list the CIDRs of the proxies you actually trust.

Middleware defaults to `fail_closed`, returning 503 on limiter errors without
calling the protected handler. `Options.FailurePolicy = middleware.FailOpen`
instead calls the handler with a degraded allowed decision. This is a local
policy for call errors; normal allowed or denied decisions from the service are
honored, including an allowed decision marked degraded by the service's policy.
For strict fail-closed behavior throughout, configure both app and service
accordingly. Middleware does not add another circuit breaker; the central
service retains its own [Redis circuit breaker](resilience.md).

Allowed requests keep the application's response status and body. Blocked
requests return 429 JSON with the decision and retry delay. Quota headers match
known decisions, and Retry-After rounds up to seconds. A degraded response adds
`X-RateLimit-Degraded: true`. A locally generated fail-open result has unknown
limit/remaining quota represented by zeros in context, and omits quota headers.
A degraded decision returned by the service retains its configured limit and
remaining-zero headers. Error responses do not expose backend error text.

Every protected response has `X-Request-ID`, reusing a valid caller ID or
generating one. The ID is forwarded to remote checks and added to failure logs.
Applications can call `middleware.RequestID(r.Context())` for correlation and
`middleware.ResultFromContext(r.Context())` for the decision, including degraded
state. The downstream context remains the original request context, enriched
with these values; it is not the canceled check-timeout context.

Run `go test -buildvcs=false ./...` for ordinary tests. The Redis integration
suite uses the [existing setup](redis.md). Tests cover public embedded and
remote modes, shared budgets across application instances, proxy trust, keys,
protocol validation, timeouts, cancellation, no redirects/retries, correlation,
and preservation of application requests. The demo is tested over real HTTP in
both modes. A separate temporary Go module also compiles the public packages,
implements a custom limiter, and enforces all three built-in memory algorithms.
