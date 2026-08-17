package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfigYAML = `
server:
  listen: ":8080"
  admin_listen: ":9090"
  read_timeout: 10s
routes:
  - name: api
    match:
      host: api.local
      path_prefix: /api
    strip_prefix: /api
    strategy: least_conn
    backends:
      - url: http://localhost:9001
      - url: http://localhost:9002
    health_check:
      path: /healthz
      interval: 5s
      timeout: 1s
    middlewares:
      rate_limit:
        rps: 100
        burst: 200
      headers:
        request_set:
          X-Forwarded-By: redproxy
        request_remove:
          - X-Internal
        response_set:
          X-Proxy: redproxy
        response_remove:
          - Server
  - name: web
    match:
      path_prefix: /
    backends:
      - url: http://localhost:8001
`

func TestParseValidConfig(t *testing.T) {
	cfg, err := Parse([]byte(validConfigYAML))
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}

	if cfg.Server.Listen != ":8080" {
		t.Errorf("Server.Listen = %q, want %q", cfg.Server.Listen, ":8080")
	}
	if cfg.Server.AdminListen != ":9090" {
		t.Errorf("Server.AdminListen = %q, want %q", cfg.Server.AdminListen, ":9090")
	}
	if cfg.Server.ReadTimeout.D() != 10*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want 10s", cfg.Server.ReadTimeout.D())
	}
	// Unset timeouts fall back to defaults.
	if cfg.Server.WriteTimeout.D() != 30*time.Second {
		t.Errorf("Server.WriteTimeout = %v, want default 30s", cfg.Server.WriteTimeout.D())
	}

	if len(cfg.Routes) != 2 {
		t.Fatalf("len(Routes) = %d, want 2", len(cfg.Routes))
	}

	api := cfg.Routes[0]
	if api.Name != "api" {
		t.Errorf("Routes[0].Name = %q, want %q", api.Name, "api")
	}
	if api.Match.Host != "api.local" {
		t.Errorf("Routes[0].Match.Host = %q, want %q", api.Match.Host, "api.local")
	}
	if api.Match.PathPrefix != "/api" {
		t.Errorf("Routes[0].Match.PathPrefix = %q, want %q", api.Match.PathPrefix, "/api")
	}
	if api.StripPrefix != "/api" {
		t.Errorf("Routes[0].StripPrefix = %q, want %q", api.StripPrefix, "/api")
	}
	if api.Strategy != StrategyLeastConn {
		t.Errorf("Routes[0].Strategy = %q, want %q", api.Strategy, StrategyLeastConn)
	}
	if len(api.Backends) != 2 || api.Backends[0].URL != "http://localhost:9001" {
		t.Errorf("Routes[0].Backends = %+v, want 2 backends starting with localhost:9001", api.Backends)
	}
	if api.HealthCheck.Path != "/healthz" {
		t.Errorf("Routes[0].HealthCheck.Path = %q, want %q", api.HealthCheck.Path, "/healthz")
	}
	if api.HealthCheck.Interval.D() != 5*time.Second {
		t.Errorf("Routes[0].HealthCheck.Interval = %v, want 5s", api.HealthCheck.Interval.D())
	}
	if api.HealthCheck.Timeout.D() != time.Second {
		t.Errorf("Routes[0].HealthCheck.Timeout = %v, want 1s", api.HealthCheck.Timeout.D())
	}

	rl := api.Middlewares.RateLimit
	if rl == nil {
		t.Fatal("Routes[0].Middlewares.RateLimit is nil, want configured")
	}
	if rl.RPS != 100 || rl.Burst != 200 {
		t.Errorf("RateLimit = {RPS: %v, Burst: %v}, want {100, 200}", rl.RPS, rl.Burst)
	}

	h := api.Middlewares.Headers
	if h == nil {
		t.Fatal("Routes[0].Middlewares.Headers is nil, want configured")
	}
	if h.RequestSet["X-Forwarded-By"] != "redproxy" {
		t.Errorf("Headers.RequestSet = %v, want X-Forwarded-By: redproxy", h.RequestSet)
	}
	if len(h.RequestRemove) != 1 || h.RequestRemove[0] != "X-Internal" {
		t.Errorf("Headers.RequestRemove = %v, want [X-Internal]", h.RequestRemove)
	}
	if h.ResponseSet["X-Proxy"] != "redproxy" {
		t.Errorf("Headers.ResponseSet = %v, want X-Proxy: redproxy", h.ResponseSet)
	}
	if len(h.ResponseRemove) != 1 || h.ResponseRemove[0] != "Server" {
		t.Errorf("Headers.ResponseRemove = %v, want [Server]", h.ResponseRemove)
	}

	// Second route relies on defaults and has no middleware block.
	web := cfg.Routes[1]
	if web.Strategy != StrategyRoundRobin {
		t.Errorf("Routes[1].Strategy = %q, want default %q", web.Strategy, StrategyRoundRobin)
	}
	if web.HealthCheck.Path != "/" {
		t.Errorf("Routes[1].HealthCheck.Path = %q, want default %q", web.HealthCheck.Path, "/")
	}
	if web.HealthCheck.Interval.D() != 10*time.Second {
		t.Errorf("Routes[1].HealthCheck.Interval = %v, want default 10s", web.HealthCheck.Interval.D())
	}
	if web.Middlewares.RateLimit != nil || web.Middlewares.Headers != nil {
		t.Errorf("Routes[1].Middlewares = %+v, want both nil", web.Middlewares)
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
routes:
  - name: minimal
    match:
      path_prefix: /
    backends:
      - url: http://localhost:9001
`))
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}

	if cfg.Server.Listen != ":8080" {
		t.Errorf("Server.Listen = %q, want default %q", cfg.Server.Listen, ":8080")
	}
	if cfg.Server.AdminListen != ":9090" {
		t.Errorf("Server.AdminListen = %q, want default %q", cfg.Server.AdminListen, ":9090")
	}
	if cfg.Server.ReadTimeout.D() != 5*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want default 5s", cfg.Server.ReadTimeout.D())
	}
	if cfg.Server.WriteTimeout.D() != 30*time.Second {
		t.Errorf("Server.WriteTimeout = %v, want default 30s", cfg.Server.WriteTimeout.D())
	}
	if cfg.Server.IdleTimeout.D() != 60*time.Second {
		t.Errorf("Server.IdleTimeout = %v, want default 60s", cfg.Server.IdleTimeout.D())
	}

	r := cfg.Routes[0]
	if r.Strategy != StrategyRoundRobin {
		t.Errorf("Strategy = %q, want default %q", r.Strategy, StrategyRoundRobin)
	}
	if r.HealthCheck.Path != "/" {
		t.Errorf("HealthCheck.Path = %q, want default %q", r.HealthCheck.Path, "/")
	}
	if r.HealthCheck.Interval.D() != 10*time.Second {
		t.Errorf("HealthCheck.Interval = %v, want default 10s", r.HealthCheck.Interval.D())
	}
	if r.HealthCheck.Timeout.D() != 2*time.Second {
		t.Errorf("HealthCheck.Timeout = %v, want default 2s", r.HealthCheck.Timeout.D())
	}
}

func TestParseValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "no routes",
			yaml:    `server: {listen: ":8080"}`,
			wantErr: "at least one route",
		},
		{
			name: "route name required",
			yaml: `
routes:
  - match: {path_prefix: /}
    backends: [{url: "http://a:9001"}]
`,
			wantErr: "name is required",
		},
		{
			name: "duplicate route names",
			yaml: `
routes:
  - name: dup
    match: {path_prefix: /a}
    backends: [{url: "http://a:9001"}]
  - name: dup
    match: {path_prefix: /b}
    backends: [{url: "http://b:9001"}]
`,
			wantErr: "duplicate route name",
		},
		{
			name: "path prefix required",
			yaml: `
routes:
  - name: x
    backends: [{url: "http://a:9001"}]
`,
			wantErr: "path_prefix is required",
		},
		{
			name: "path prefix must start with slash",
			yaml: `
routes:
  - name: x
    match: {path_prefix: api}
    backends: [{url: "http://a:9001"}]
`,
			wantErr: `must start with "/"`,
		},
		{
			name: "invalid strategy",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    strategy: banana
    backends: [{url: "http://a:9001"}]
`,
			wantErr: "invalid strategy",
		},
		{
			name: "no backends",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
`,
			wantErr: "at least one backend",
		},
		{
			name: "backend url not absolute",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "not-a-url"}]
`,
			wantErr: "invalid backend URL",
		},
		{
			name: "backend url missing host",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "http://"}]
`,
			wantErr: "invalid backend URL",
		},
		{
			name: "backend url unsupported scheme",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "ftp://a:9001"}]
`,
			wantErr: "invalid backend URL",
		},
		{
			name: "strip prefix must be a prefix of path prefix",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /api}
    strip_prefix: /v1
    backends: [{url: "http://a:9001"}]
`,
			wantErr: "is not a prefix of",
		},
		{
			name: "health check timeout must be less than interval",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "http://a:9001"}]
    health_check: {interval: 1s, timeout: 5s}
`,
			wantErr: "must be less than",
		},
		{
			name: "health check interval must be positive",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "http://a:9001"}]
    health_check: {interval: -5s, timeout: 1s}
`,
			wantErr: "interval must be positive",
		},
		{
			name: "rate limit rps must be positive",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "http://a:9001"}]
    middlewares: {rate_limit: {rps: 0, burst: 10}}
`,
			wantErr: "rate_limit.rps must be positive",
		},
		{
			name: "rate limit burst must be at least 1",
			yaml: `
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "http://a:9001"}]
    middlewares: {rate_limit: {rps: 10, burst: 0}}
`,
			wantErr: "rate_limit.burst must be at least 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("Parse() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseReportsMultipleErrors(t *testing.T) {
	_, err := Parse([]byte(`
routes:
  - name: ""
`))
	if err == nil {
		t.Fatal("Parse() succeeded, want validation errors")
	}
	msg := err.Error()
	for _, want := range []string{"name is required", "path_prefix is required", "at least one backend"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Parse() error missing %q, got:\n%s", want, msg)
		}
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte("routes: [unclosed"))
	if err == nil {
		t.Fatal("Parse() succeeded on malformed YAML, want error")
	}
}

func TestParseUnknownFieldRejected(t *testing.T) {
	_, err := Parse([]byte(`
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "http://a:9001"}]
    bogus_field: true
`))
	if err == nil {
		t.Fatal("Parse() succeeded with unknown field, want error")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("Parse() error = %q, want it to mention %q", err.Error(), "bogus_field")
	}
}

func TestParseInvalidDuration(t *testing.T) {
	_, err := Parse([]byte(`
routes:
  - name: x
    match: {path_prefix: /}
    backends: [{url: "http://a:9001"}]
    health_check: {interval: "not-a-duration", timeout: 1s}
`))
	if err == nil {
		t.Fatal("Parse() succeeded with invalid duration, want error")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("Load() succeeded for missing file, want error")
	}
}

func TestLoadValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(validConfigYAML), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if len(cfg.Routes) != 2 {
		t.Errorf("len(Routes) = %d, want 2", len(cfg.Routes))
	}
}
