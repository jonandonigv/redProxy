// Command redproxy runs the redProxy reverse proxy: it loads the YAML
// configuration, lets the gateway assemble pools, proxies, middleware chains,
// health checks and metrics, then serves traffic until SIGINT/SIGTERM.
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

	"redproxy/internal/config"
	"redproxy/internal/gateway"
)

const shutdownTimeout = 10 * time.Second

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

	gw, err := gateway.New(cfg, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gw.Start()
	// Unconditional cleanup: the idempotent Stop runs on every return path. The
	// select below funnels both signals and listener failures through the same
	// drain, which finishes before this deferred Stop releases background work.
	defer gw.Stop()

	proxySrv := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      gw,
		ReadTimeout:  cfg.Server.ReadTimeout.D(),
		WriteTimeout: cfg.Server.WriteTimeout.D(),
		IdleTimeout:  cfg.Server.IdleTimeout.D(),
	}
	adminSrv := &http.Server{Addr: cfg.Server.AdminListen, Handler: gw.AdminHandler()}

	errCh := make(chan error, 2)
	go func() { errCh <- proxySrv.ListenAndServe() }()
	go func() { errCh <- adminSrv.ListenAndServe() }()

	logger.Info("redProxy started",
		"listen", cfg.Server.Listen,
		"admin_listen", cfg.Server.AdminListen,
		"routes", len(cfg.Routes),
	)

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		serveErr = fmt.Errorf("server failed: %w", err)
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Drain both servers, including the survivor when only one listener
	// failed, before the deferred Gateway Stop releases background work.
	var wg sync.WaitGroup
	servers := []*http.Server{proxySrv, adminSrv}
	wg.Add(len(servers))
	for _, srv := range servers {
		go func(s *http.Server) {
			defer wg.Done()
			_ = s.Shutdown(shutdownCtx)
		}(srv)
	}
	wg.Wait()

	if serveErr != nil {
		return serveErr
	}

	logger.Info("redProxy stopped")
	return nil
}
