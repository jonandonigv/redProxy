// Command redproxy runs the redProxy reverse proxy: it loads the YAML
// configuration, wires balancer pools, proxies, middleware chains, health
// checks and metrics into a router, then serves traffic until SIGINT/SIGTERM.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"redproxy/internal/balancer"
	"redproxy/internal/config"
	"redproxy/internal/health"
	"redproxy/internal/metrics"
	"redproxy/internal/middleware"
	"redproxy/internal/proxy"
	"redproxy/internal/router"
)

const (
	shutdownTimeout     = 10 * time.Second
	backendPollInterval = 5 * time.Second
)

// routeEntry keeps a route's runtime pieces together for health wiring.
type routeEntry struct {
	name string
	pool *balancer.Pool
}

func main() {
	if err := run(); err != nil {
		slog.Error("redproxy exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to YAML configuration file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	appMetrics := metrics.New()

	var (
		entries []routeEntry
		checks  []health.Check
	)
	routerRoutes := make([]router.Route, 0, len(cfg.Routes))

	for i := range cfg.Routes {
		rc := &cfg.Routes[i]

		pool, err := balancer.NewPool(rc.Strategy, rc.Backends)
		if err != nil {
			return fmt.Errorf("route %q: %w", rc.Name, err)
		}
		entries = append(entries, routeEntry{name: rc.Name, pool: pool})

		routeHandler := middleware.Chain(chainFor(logger, appMetrics, rc)...)(proxy.New(pool, rc.StripPrefix))
		routerRoutes = append(routerRoutes, router.Route{
			Name:       rc.Name,
			Host:       rc.Match.Host,
			PathPrefix: rc.Match.PathPrefix,
			Handler:    routeHandler,
		})

		checks = append(checks, health.Check{
			Pool:             pool,
			Path:             rc.HealthCheck.Path,
			Interval:         rc.HealthCheck.Interval.D(),
			Timeout:          rc.HealthCheck.Timeout.D(),
			FailThreshold:    2,
			RecoverThreshold: 1,
		})
	}

	rt, err := router.New(routerRoutes)
	if err != nil {
		return fmt.Errorf("building router: %w", err)
	}

	checker := health.New(checks)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	checker.Start()
	go pollBackendHealth(ctx, entries, appMetrics)

	proxySrv := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      rt,
		ReadTimeout:  cfg.Server.ReadTimeout.D(),
		WriteTimeout: cfg.Server.WriteTimeout.D(),
		IdleTimeout:  cfg.Server.IdleTimeout.D(),
	}

	adminMux := http.NewServeMux()
	adminMux.Handle("/metrics", appMetrics.Handler())
	adminMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	adminSrv := &http.Server{Addr: cfg.Server.AdminListen, Handler: adminMux}

	errCh := make(chan error, 2)
	go func() { errCh <- proxySrv.ListenAndServe() }()
	go func() { errCh <- adminSrv.ListenAndServe() }()

	logger.Info("redProxy started",
		"listen", cfg.Server.Listen,
		"admin_listen", cfg.Server.AdminListen,
		"routes", len(cfg.Routes),
	)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return fmt.Errorf("server failed: %w", err)
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	for _, srv := range []*http.Server{proxySrv, adminSrv} {
		go func(s *http.Server) {
			defer wg.Done()
			_ = s.Shutdown(shutdownCtx)
		}(srv)
	}
	wg.Wait()

	checker.Stop()
	logger.Info("redProxy stopped")
	return nil
}

// chainFor builds the per-route middleware chain. Order matters:
// metrics outermost so rejections are counted, then logging so they are
// recorded, then rate limiting and header manipulation around the proxy.
func chainFor(logger *slog.Logger, m *metrics.Metrics, rc *config.RouteConfig) []middleware.Middleware {
	mws := []middleware.Middleware{
		m.Middleware(),
		middleware.Logging(logger),
	}
	if rl := rc.Middlewares.RateLimit; rl != nil {
		mws = append(mws, middleware.RateLimit(rl.RPS, rl.Burst))
	}
	if h := rc.Middlewares.Headers; h != nil {
		mws = append(mws, middleware.Headers(*h))
	}
	return mws
}

// pollBackendHealth feeds the backend_up gauge from pool state until ctx is
// cancelled.
func pollBackendHealth(ctx context.Context, entries []routeEntry, m *metrics.Metrics) {
	ticker := time.NewTicker(backendPollInterval)
	defer ticker.Stop()

	update := func() {
		for _, e := range entries {
			for _, b := range e.pool.Backends() {
				m.SetBackendUp(b.URL, b.Healthy())
			}
		}
	}

	update()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}
