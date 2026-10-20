# AGENTS.md — redProxy

## Project Overview

redProxy is a portfolio-quality reverse proxy written in Go, built on the stdlib
`net/http/httputil.ReverseProxy` and configured via a single YAML file.

Confirmed feature set:

- Path/host-based routing to multiple backends
- Load balancing: round-robin, least-connections, random
- Active health checks (unhealthy backends are excluded automatically)
- Middleware chain: structured request logging, header manipulation, per-client-IP rate limiting
- Prometheus metrics on a separate admin port

Explicitly out of scope (future work): TLS termination, WebSocket proxying,
retries/circuit breaking, config hot reload.

The authoritative design document is `Plan.md` (local-only, git-ignored). Consult it
before making architectural decisions; keep it and this file in sync with the code.

## Layout

```
cmd/
  redproxy/        # flags, config loading, listeners, signals, graceful shutdown
  demo-backend/    # tiny echo backend for manual testing/demos
internal/
  gateway/         # assembles routes, pools, middleware chains, health checks, metrics
  config/          # YAML structs, loading, validation
  router/          # host + longest-path-prefix matching
  balancer/        # Backend type + round_robin / least_conn / random strategies
  health/          # background health checker, atomic health state
  proxy/           # per-route httputil.ReverseProxy construction
  middleware/      # chain helper + logging, headers, ratelimit
  metrics/         # prometheus registry, metrics middleware, /metrics server
config.example.yaml
Makefile           # build / test / run / vet
```

- Module name: `redproxy`
- Tests live next to their packages as `*_test.go`; integration tests use `httptest.Server`.

## Dependencies

Keep dependencies minimal — this is a hard constraint:

- `gopkg.in/yaml.v3` (config)
- `github.com/prometheus/client_golang` (metrics)
- `golang.org/x/time` (rate limiting)

Everything else must come from the standard library. Do not add new dependencies
without explicit approval.

## Coding Conventions

- Standard Go style: `gofmt`-clean, `go vet`-clean.
- Hot-path state (backend health, in-flight counts) uses `sync/atomic`, not mutexes.
- Middleware follows the classic `func(http.Handler) http.Handler` pattern.
- Logging uses `log/slog` (JSON output); no `log.Printf` in proxy code.
- Configuration is validated strictly at startup — fail fast with clear errors.
- Graceful shutdown via `signal.NotifyContext` (SIGINT/SIGTERM) + `http.Server.Shutdown`.
- Keep changes minimal and focused; follow the architecture in `Plan.md`.
- Follow TDD, red->green aproach.

## Commands

```sh
make build    # build cmd/redproxy (and demo-backend)
make test     # go test ./...
make vet      # go vet ./...
make run      # run the proxy with config.example.yaml
```

(If the Makefile is not yet created, use the equivalent plain `go` commands:
`go build ./...`, `go test ./...`, `go vet ./...`, `go run ./cmd/redproxy -config config.example.yaml`.)

## Testing Expectations

- Unit tests for: balancer strategies (ordering, min-conn selection, unhealthy exclusion),
  router matching (host + longest prefix), config validation, rate limiter, header middleware.
- Gateway black-box integration tests in `internal/gateway` (external `gateway_test`
  package, `TestGateway*`): a real Gateway in front of `httptest.Server` backends — verify
  host + longest-prefix routing and `strip_prefix`, request/response header injection,
  rate-limited 429s in `requests_total` and the log record, backend health failure →
  exclusion → readmission, 503 when all backends are down, `backend_up` immediately
  after `Start`, and the one-shot lifecycle (`Start`/`Stop` idempotent, probing halted
  by `Stop`).
- Run `go vet ./...` and `go test ./...` (or `go test -race ./...` for the gateway suite)
  before considering any change complete.

## Git

- Never commit without explicit user approval.
- `Plan.md` is intentionally git-ignored (local working document).
