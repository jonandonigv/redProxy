package gateway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
