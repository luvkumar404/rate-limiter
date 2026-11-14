`compose.redis.yaml` runs the Redis 7 dependency for Phase 3 development and
integration tests. Run from the repository root:

```sh
docker compose -p ratelimiter-phase3 -f deploy/compose.redis.yaml up -d --wait
```

The port is bound to localhost at 16379. Override it with `REDIS_PORT` if needed
and update the address in your rules config. Persistence is disabled; stopping
the container discards limiter state. No preexisting Redis is modified.

```sh
docker compose -p ratelimiter-phase3 -f deploy/compose.redis.yaml down
```

`prometheus.yaml` is a standalone Docker Desktop scraper example targeting the
service on port 8082. See [observability](../docs/observability.md) for the launch
command and queries. The complete service cluster, load balancer, and integrated
Prometheus deployment will be added in Phase 8. See [Redis testing](../docs/redis.md).
