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
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"redproxy/internal/balancer"
	"redproxy/internal/config"
	"redproxy/internal/health"
	"redproxy/internal/metrics"
	"redproxy/internal/middleware"
	"redproxy/internal/proxy"
	"redproxy/internal/router"
)

// backendPollInterval is how often backend health is copied into the
// backend_up gauge.
const backendPollInterval = 5 * time.Second

// Gateway is the assembled proxy for one configuration.
type Gateway struct {
	logger  *slog.Logger
	metrics *metrics.Metrics
	router  *router.Router
	pools   []*balancer.Pool
	checker *health.Checker
	admin   http.Handler

	mu         sync.Mutex
	started    bool
	stopped    bool
	cancel     context.CancelFunc
	pollerDone chan struct{}
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
	checks := make([]health.Check, 0, len(cfg.Routes))

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

		checks = append(checks, health.Check{
			Pool:     pool,
			Path:     rc.HealthCheck.Path,
			Interval: rc.HealthCheck.Interval.D(),
			Timeout:  rc.HealthCheck.Timeout.D(),
		})
	}

	rt, err := router.New(routes)
	if err != nil {
		return nil, fmt.Errorf("building router: %w", err)
	}
	g.router = rt
	g.checker = health.New(checks)

	admin := http.NewServeMux()
	admin.Handle("/metrics", g.metrics.Handler())
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	g.admin = admin

	return g, nil
}

// ServeHTTP dispatches proxy traffic to the matching route.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.router.ServeHTTP(w, r)
}

// AdminHandler serves the admin surface: Prometheus metrics and a static
// liveness answer.
func (g *Gateway) AdminHandler() http.Handler {
	return g.admin
}

// Start launches the health checker and the backend_up poller. It is a no-op
// after the first call and after Stop: a gateway runs one lifecycle.
func (g *Gateway) Start() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.started || g.stopped {
		return
	}
	g.started = true

	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.pollerDone = make(chan struct{})

	g.checker.Start()
	g.updateBackendUp() // immediate first reading; the poller refreshes it every 5s
	go func(done chan struct{}) {
		defer close(done)
		g.pollBackendHealth(ctx)
	}(g.pollerDone)
}

// Stop releases the gateway's background work. It is safe to call more than
// once.
func (g *Gateway) Stop() {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return
	}
	g.stopped = true
	cancel, done := g.cancel, g.pollerDone
	g.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	g.checker.Stop()
}

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

// pollBackendHealth refreshes the backend_up gauge from pool state until ctx
// is cancelled.
func (g *Gateway) pollBackendHealth(ctx context.Context) {
	ticker := time.NewTicker(backendPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.updateBackendUp()
		}
	}
}

// updateBackendUp copies every backend's health into the backend_up gauge.
func (g *Gateway) updateBackendUp() {
	for _, pool := range g.pools {
		for _, b := range pool.Backends() {
			g.metrics.SetBackendUp(b.URL, b.Healthy())
		}
	}
}
