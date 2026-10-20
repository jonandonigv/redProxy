// Package gateway assembles redProxy's runtime object graph: the backend
// pools, per-route middleware chains and proxies, the router, the health
// checker, and the metrics registry.
//
// A Gateway serves traffic via ServeHTTP and exposes the admin surface via
// AdminHandler. It owns its background work, started by Start and released by
// Stop, but not the listeners, http.Servers, or signals: those stay with the
// command that embeds it.
package gateway

import (
	"fmt"
	"log/slog"
	"net/http"

	"redproxy/internal/balancer"
	"redproxy/internal/config"
	"redproxy/internal/metrics"
	"redproxy/internal/middleware"
	"redproxy/internal/proxy"
	"redproxy/internal/router"
)

// Gateway is the assembled proxy for one configuration.
type Gateway struct {
	logger  *slog.Logger
	metrics *metrics.Metrics
	router  *router.Router
	pools   []*balancer.Pool
}

// New builds the gateway for cfg. A nil logger falls back to slog.Default.
func New(cfg *config.Config, logger *slog.Logger) (*Gateway, error) {
	if logger == nil {
		logger = slog.Default()
	}

	g := &Gateway{
		logger:  logger,
		metrics: metrics.New(),
	}

	routes := make([]router.Route, 0, len(cfg.Routes))
	for i := range cfg.Routes {
		rc := &cfg.Routes[i]

		pool, err := balancer.NewPool(rc.Strategy, rc.Backends)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", rc.Name, err)
		}
		g.pools = append(g.pools, pool)

		handler := middleware.Chain(g.chainFor(rc)...)(proxy.New(pool, rc.StripPrefix))
		routes = append(routes, router.Route{
			Name:       rc.Name,
			Host:       rc.Match.Host,
			PathPrefix: rc.Match.PathPrefix,
			Handler:    handler,
		})
	}

	rt, err := router.New(routes)
	if err != nil {
		return nil, fmt.Errorf("building router: %w", err)
	}
	g.router = rt

	return g, nil
}

// ServeHTTP dispatches proxy traffic to the matching route.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.router.ServeHTTP(w, r)
}

// Stop releases the gateway's background work. It is safe to call more than
// once.
func (g *Gateway) Stop() {}

// chainFor builds the per-route middleware chain. Order matters: metrics
// outermost so rejections are counted, then logging so they are recorded,
// then rate limiting and header manipulation around the proxy.
func (g *Gateway) chainFor(rc *config.RouteConfig) []middleware.Middleware {
	mws := []middleware.Middleware{
		g.metrics.Middleware(),
		middleware.Logging(g.logger),
	}
	if rl := rc.Middlewares.RateLimit; rl != nil {
		mws = append(mws, middleware.RateLimit(rl.RPS, rl.Burst))
	}
	if h := rc.Middlewares.Headers; h != nil {
		mws = append(mws, middleware.Headers(*h))
	}
	return mws
}
