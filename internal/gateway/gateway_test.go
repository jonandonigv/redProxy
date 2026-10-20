package gateway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"redproxy/internal/config"
	"redproxy/internal/gateway"
)

// newBackend starts an httptest backend that identifies itself by name.
func newBackend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, name)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func route(name, host, pathPrefix, backendURL string) config.RouteConfig {
	return config.RouteConfig{
		Name:     name,
		Match:    config.MatchConfig{Host: host, PathPrefix: pathPrefix},
		Strategy: config.StrategyRoundRobin,
		Backends: []config.BackendConfig{{URL: backendURL}},
	}
}

func configFor(routes ...config.RouteConfig) *config.Config {
	return &config.Config{Routes: routes}
}

func newGateway(t *testing.T, cfg *config.Config) *gateway.Gateway {
	t.Helper()
	gw, err := gateway.New(cfg, nil)
	if err != nil {
		t.Fatalf("gateway.New() error: %v", err)
	}
	t.Cleanup(gw.Stop)
	return gw
}

func request(t *testing.T, gw *gateway.Gateway, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	return rec
}

// captureBackend records the request it served and adds response headers.
type captureBackend struct {
	srv *httptest.Server

	mu      sync.Mutex
	path    string
	headers http.Header
}

func newCaptureBackend(t *testing.T) *captureBackend {
	t.Helper()
	b := &captureBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.path = r.URL.Path
		b.headers = r.Header.Clone()
		b.mu.Unlock()

		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("X-Remove", "me")
		fmt.Fprint(w, "proxied")
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *captureBackend) snapshot() (string, http.Header) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.path, b.headers.Clone()
}

func TestGatewayStripsPrefixAndAppliesHeaders(t *testing.T) {
	backend := newCaptureBackend(t)

	api := route("api", "", "/api", backend.srv.URL)
	api.StripPrefix = "/api"
	api.Middlewares.Headers = &config.HeadersConfig{
		RequestSet:     map[string]string{"X-Forwarded-By": "redproxy"},
		RequestRemove:  []string{"X-Secret"},
		ResponseSet:    map[string]string{"X-Proxy": "redproxy"},
		ResponseRemove: []string{"X-Remove"},
	}
	gw := newGateway(t, configFor(api))

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("X-Secret", "do-not-forward")
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Proxy"); got != "redproxy" {
		t.Errorf("response header X-Proxy = %q, want redproxy", got)
	}
	if got := rec.Header().Get("X-Remove"); got != "" {
		t.Errorf("response header X-Remove = %q, want it removed", got)
	}
	if got := rec.Header().Get("X-Upstream"); got != "yes" {
		t.Errorf("response header X-Upstream = %q, want it passed through", got)
	}

	path, headers := backend.snapshot()
	if path != "/users" {
		t.Errorf("backend saw path %q, want /users (strip_prefix applied)", path)
	}
	if got := headers.Get("X-Forwarded-By"); got != "redproxy" {
		t.Errorf("backend saw X-Forwarded-By = %q, want redproxy", got)
	}
	if got := headers.Get("X-Secret"); got != "" {
		t.Errorf("backend saw X-Secret = %q, want it removed", got)
	}
}

func TestGatewayAdminHandlerServesHealthAndMetrics(t *testing.T) {
	backend := newBackend(t, "web")
	gw := newGateway(t, configFor(route("web", "", "/", backend.URL)))
	gw.Start()

	health := httptest.NewRecorder()
	gw.AdminHandler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", health.Code)
	}
	if body := health.Body.String(); body != "ok" {
		t.Errorf("/healthz body = %q, want ok", body)
	}

	// Generate traffic so the per-route request metrics exist.
	if rec := request(t, gw, "", "/x"); rec.Code != http.StatusOK {
		t.Fatalf("proxied request status = %d, want 200", rec.Code)
	}

	rec := httptest.NewRecorder()
	gw.AdminHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// backend_up is fed immediately on Start, not only after the 5s tick.
	if want := `redproxy_backend_up{backend="` + backend.URL + `"} 1`; !strings.Contains(body, want) {
		t.Errorf("/metrics missing %q immediately after Start", want)
	}
	if want := `redproxy_requests_total{code="200",route="web"} 1`; !strings.Contains(body, want) {
		t.Errorf("/metrics missing %q", want)
	}
}

func TestGatewayLifecycleIsOneShotAndIdempotent(t *testing.T) {
	backend := newBackend(t, "web")
	gw := newGateway(t, configFor(route("web", "", "/", backend.URL)))

	gw.Start()
	gw.Start()
	if rec := request(t, gw, "", "/x"); rec.Code != http.StatusOK {
		t.Fatalf("request after Start status = %d, want 200", rec.Code)
	}

	gw.Stop()
	gw.Stop()
	gw.Start() // restart is unsupported: must be a safe no-op, not a panic

	if rec := request(t, gw, "", "/x"); rec.Code != http.StatusOK {
		t.Fatalf("request after Stop status = %d, want 200", rec.Code)
	}
}

func TestGatewayRoutesByHostAndLongestPathPrefix(t *testing.T) {
	api := newBackend(t, "api")
	users := newBackend(t, "users")
	web := newBackend(t, "web")

	gw := newGateway(t, configFor(
		route("api", "api.example.com", "/api", api.URL),
		route("users", "api.example.com", "/api/users", users.URL),
		route("web", "", "/", web.URL),
	))

	tests := []struct {
		name string
		host string
		path string
		want string
	}{
		{name: "host-scoped route", host: "api.example.com", path: "/api/v1", want: "api"},
		{name: "longest prefix wins", host: "api.example.com", path: "/api/users/42", want: "users"},
		{name: "host mismatch falls through", host: "www.example.com", path: "/api/v1", want: "web"},
		{name: "root route", host: "www.example.com", path: "/", want: "web"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(t, gw, tt.host, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}
