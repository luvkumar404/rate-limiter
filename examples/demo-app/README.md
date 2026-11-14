# Middleware demo

Run from the repository root. The demo uses only the public limiter and
middleware packages. It serves a protected `GET /hello` and an unprotected
`GET /healthz`, and drains active requests on Ctrl+C.

Embedded mode needs no Redis or rate limiter service:

```powershell
go run -buildvcs=false ./examples/demo-app -addr :8083
```

The embedded token bucket starts with five tokens and refills five per ten
seconds. Six immediate requests from the same client IP produce five successful
responses and then a 429. After two seconds, one token has refilled.

```powershell
curl.exe -i http://localhost:8083/hello
```

For remote mode, start the updated limiter service in another terminal, then
run the demo against it:

```powershell
go run -buildvcs=false ./cmd/server -config rules.redis.yaml -addr :8082
```

```powershell
go run -buildvcs=false ./examples/demo-app -addr :8083 `
  -limiter-url http://localhost:8082 -rule login
```

Start the development Redis using the [Redis setup](../../docs/redis.md) for
normal Redis enforcement. The sample `login` rule permits five requests per
rolling minute. Remote applications using that rule and the same extracted key
share its budget. Use your actual service address if it runs on another port.

The default extractor uses the socket peer IP. To try separate header keys:

```powershell
go run -buildvcs=false ./examples/demo-app -addr :8083 -key-header X-API-Key
curl.exe -i -H 'X-API-Key: user-42' http://localhost:8083/hello
```

Headers are only keys; this demo does not authenticate API keys. Missing header
keys return 400. Allowed responses retain the application's JSON and expose
quota and request-ID headers. Denials return 429 with Retry-After.

`-failure-policy fail_closed` is the default for middleware errors. Select
`-failure-policy fail_open` to allow requests if the remote service cannot be
checked. Such responses include `X-RateLimit-Degraded: true`, and the demo JSON
reports `degraded: true`. An already-degraded allowed decision from the service
is also passed through. See [middleware behavior](../../docs/middleware.md).
