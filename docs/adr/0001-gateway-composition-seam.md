# Gateway owns composition, not the process

redProxy's runtime object graph — pools, routes, middleware chains, proxies,
health checking and metrics — was assembled inline in `cmd/redproxy/main.go`,
so no test could exercise the full stack. We introduced `internal/gateway`
(`New`/`ServeHTTP`/`AdminHandler`/`Start`/`Stop`): the gateway owns the
composition and the background work, but not listeners, `http.Server`s or
signals, which stay in `main`. That is the largest seam a test can drive
without binding ports or handling signals, and it keeps every runtime
invariant that spans packages behind one interface.

## Considered options

- **Whole application module** (gateway owns listeners, servers, signals,
  shutdown): rejected because tests would have to bind ports and exercise
  signal handling to verify routing.
- **Handler-only module** (composition returned, checker and metrics poller
  started by `main`): rejected because the health and metrics wiring — the
  parts with the subtle invariants — would remain untested.

## Consequences

- Full-stack tests build a gateway and drive it through `http.Handler`.
- Lifecycle is one-shot: `Start` and `Stop` are idempotent, restart is not
  supported.
- Health thresholds come from `health` defaults instead of `main`; values and
  behavior are unchanged.
- The gateway creates its own metrics registry and serves it through
  `AdminHandler`; tests scrape the same surface as operations.
- This change is behavior-preserving. `backend_up` keeps its 5s polling lag
  and `/healthz` stays a static liveness answer; both are candidates for the
  health-state work, not this decision.
