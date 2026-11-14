# Deployment files

The root `docker-compose.yml` runs three Go instances, Redis 7, Nginx, and
Prometheus. Run `docker compose up -d --build --wait` from the repository root.
The gateway defaults to localhost:8090 and Prometheus to localhost:9090.

`Dockerfile` builds a static, non-root Go image with a shell-free HTTP health
probe. `rules.cluster.yaml` points all instances at `redis:6379`. `nginx.conf`
balances traffic without retrying quota checks. `prometheus.cluster.yaml`
scrapes each instance directly, rather than sampling the load balancer.

See [deployment and CI](../docs/deployment.md) for full setup and the smoke check.

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
command and queries. The full stack uses `prometheus.cluster.yaml` instead.
See [Redis testing](../docs/redis.md) for host-based development.
