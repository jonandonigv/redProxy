# redProxy

A portfolio-quality reverse proxy written in Go, built on the standard
library's `net/http/httputil.ReverseProxy` and configured through a single
YAML file.

## Features

- **Path/host-based routing** to multiple upstreams (longest prefix wins)
- **Load balancing**: round-robin, least-connections, random — per route
- **Active health checks** with failure/recovery thresholds; unhealthy
  backends stop receiving traffic and are restored automatically
- **Passive health marking** on connection failures (502 + backend down)
- **Middleware chain** per route: structured request logging, header
  manipulation, per-client-IP rate limiting
- **Prometheus metrics** on a separate admin port (`/metrics`, `/healthz`)
- **Graceful shutdown** on SIGINT/SIGTERM

Out of scope (future work): TLS termination, WebSocket proxying,
retries/circuit breaking, config hot reload.

## Quick start

```sh
make build

# three demo backends
./bin/demo-backend -listen :9001 &
./bin/demo-backend -listen :9002 &
./bin/demo-backend -listen :8001 &

# the proxy, using the example configuration
./bin/redproxy -config config.example.yaml
```

Then try it:

```sh
# api route: round-robin over :9001/:9002, /api stripped before forwarding
curl localhost:8080/api/users
curl localhost:8080/api/users   # repeat: responses alternate between backends

# web route: catch-all, least connections over :8001
curl localhost:8080/some/page

# rate limiting: the example caps /api at 2 rps burst 3 — hammer it
for i in $(seq 1 8); do curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/api/x; done
# 200 200 200 429 429 ...

# metrics
curl localhost:9090/metrics
curl localhost:9090/healthz
```

Kill one of the API backends and keep curling `/api`: within ~2s the health
checker excludes it and traffic flows to the survivor; restart it and it is
admitted back.

## Configuration

See [`config.example.yaml`](config.example.yaml) for a fully commented
example. Highlights:

| Field | Meaning |
|---|---|
| `server.listen` | proxy listen address |
| `server.admin_listen` | `/metrics` + `/healthz` address |
| `routes[].match.path_prefix` | required; longest matching prefix wins |
| `routes[].match.host` | optional host filter (case-insensitive, port ignored) |
| `routes[].strip_prefix` | removed from the path before forwarding |
| `routes[].strategy` | `round_robin`, `least_conn`, or `random` |
| `routes[].backends` | upstream URLs (`http://host:port`) |
| `routes[].health_check` | probe path, interval, timeout |
| `routes[].middlewares.rate_limit` | per-client-IP token bucket (`rps`, `burst`) |
| `routes[].middlewares.headers` | set/remove request and response headers |

Configuration is validated strictly at startup — unknown fields, bad URLs,
or inconsistent values fail fast with all problems reported at once.

## Metrics

Scraped from the admin port:

- `redproxy_requests_total{route, code}`
- `redproxy_request_duration_seconds{route}`
- `redproxy_in_flight_requests`
- `redproxy_backend_up{backend}`

## Development

```sh
make test   # go test ./... (unit + integration tests, race-safe)
make vet    # go vet ./...
```

Layout:

```
cmd/redproxy        entrypoint: wiring + graceful shutdown
cmd/demo-backend    tiny echo server for manual testing/demos
internal/config     YAML loading, strict validation
internal/balancer   Backend type + round-robin / least-conn / random strategies
internal/health     background health checker (atomic state)
internal/proxy      per-route ReverseProxy construction
internal/router     host + longest-prefix matching
internal/middleware logging, headers, rate limiting, chain helper
internal/metrics    Prometheus registry, middleware, exposition
```

Dependencies are deliberately minimal: `gopkg.in/yaml.v3`,
`github.com/prometheus/client_golang`, and `golang.org/x/time`; everything
else is the standard library.
