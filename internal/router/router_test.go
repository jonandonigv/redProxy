package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func markerRoute(name, host, prefix, marker string) Route {
	return Route{
		Name:       name,
		Host:       host,
		PathPrefix: prefix,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(marker))
		}),
	}
}

func newRouter(t *testing.T, routes ...Route) *Router {
	t.Helper()
	r, err := New(routes)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	return r
}

func get(t *testing.T, r http.Handler, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestLongestPrefixWins(t *testing.T) {
	r := newRouter(t,
		markerRoute("api", "", "/api", "A"),
		markerRoute("apiv2", "", "/api/v2", "B"),
	)

	if got := get(t, r, "any.example.com", "/api/v2/users").Body.String(); got != "B" {
		t.Errorf("/api/v2/users routed to %q, want B (longest prefix)", got)
	}
	if got := get(t, r, "any.example.com", "/api/users").Body.String(); got != "A" {
		t.Errorf("/api/users routed to %q, want A", got)
	}
}

func TestHostRouting(t *testing.T) {
	r := newRouter(t,
		markerRoute("tenant-a", "a.example.com", "/", "A"),
		markerRoute("tenant-b", "b.example.com", "/", "B"),
	)

	if got := get(t, r, "a.example.com", "/anything").Body.String(); got != "A" {
		t.Errorf("a.example.com routed to %q, want A", got)
	}
	if got := get(t, r, "b.example.com", "/anything").Body.String(); got != "B" {
		t.Errorf("b.example.com routed to %q, want B", got)
	}

	rec := get(t, r, "unknown.example.com", "/anything")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown host status = %d, want 404", rec.Code)
	}
}

func TestHostIsCaseInsensitiveAndIgnoresPort(t *testing.T) {
	r := newRouter(t,
		markerRoute("tenant-a", "a.example.com", "/", "A"),
		markerRoute("web", "", "/", "W"),
	)

	if got := get(t, r, "A.EXAMPLE.COM:8080", "/x").Body.String(); got != "A" {
		t.Errorf("host %q routed to %q, want A (case-insensitive, port ignored)", "A.EXAMPLE.COM:8080", got)
	}
}

func TestCatchAllMatchesAnyHost(t *testing.T) {
	r := newRouter(t,
		markerRoute("api", "api.example.com", "/api", "A"),
		markerRoute("web", "", "/", "W"),
	)

	if got := get(t, r, "whatever.example.com", "/some/page").Body.String(); got != "W" {
		t.Errorf("unmatched host on non-api path routed to %q, want W (catch-all)", got)
	}
	if got := get(t, r, "api.example.com", "/api/x").Body.String(); got != "A" {
		t.Errorf("api.example.com/api/x routed to %q, want A", got)
	}
}

func TestRootPrefixMatchesEverything(t *testing.T) {
	r := newRouter(t, markerRoute("web", "", "/", "W"))

	for _, p := range []string{"/", "/a", "/a/b/c"} {
		if got := get(t, r, "h.example.com", p).Body.String(); got != "W" {
			t.Errorf("%s routed to %q, want W", p, got)
		}
	}
}

func TestNoMatchReturns404(t *testing.T) {
	r := newRouter(t, markerRoute("api", "", "/api", "A"))

	rec := get(t, r, "h.example.com", "/other")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "404") {
		t.Errorf("body = %q, want it to mention 404", rec.Body.String())
	}
}

func TestEmptyRouterReturns404(t *testing.T) {
	r := newRouter(t)

	rec := get(t, r, "h.example.com", "/")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestEqualLengthPrefixesFirstWins(t *testing.T) {
	r := newRouter(t,
		markerRoute("first", "", "/dup", "1st"),
		markerRoute("second", "", "/dup", "2nd"),
	)

	if got := get(t, r, "h.example.com", "/dup/x").Body.String(); got != "1st" {
		t.Errorf("equal prefixes resolved to %q, want 1st (first defined wins)", got)
	}
}

func TestQueryStringIgnored(t *testing.T) {
	r := newRouter(t, markerRoute("api", "", "/api", "A"))

	req := httptest.NewRequest(http.MethodGet, "/api/users?limit=10&page=2", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Body.String() != "A" {
		t.Errorf("query-string request routed to %q, want A", rec.Body.String())
	}
}

func TestRouteNameExposedViaContext(t *testing.T) {
	var gotName string
	r, err := New([]Route{{
		Name: "apiv2", PathPrefix: "/api/v2",
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
			gotName = RouteName(req.Context())
		}),
	}})
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}

	get(t, r, "h.example.com", "/api/v2/x")
	if gotName != "apiv2" {
		t.Errorf("RouteName(ctx) = %q, want %q", gotName, "apiv2")
	}
}

func TestNewRejectsBadRoutes(t *testing.T) {
	tests := []struct {
		name    string
		route   Route
		wantErr string
	}{
		{
			name:    "empty path prefix",
			route:   Route{Name: "x", PathPrefix: "", Handler: http.NotFoundHandler()},
			wantErr: "path_prefix is required",
		},
		{
			name:    "prefix without leading slash",
			route:   Route{Name: "x", PathPrefix: "api", Handler: http.NotFoundHandler()},
			wantErr: `must start with "/"`,
		},
		{
			name:    "nil handler",
			route:   Route{Name: "x", PathPrefix: "/api"},
			wantErr: "handler is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New([]Route{tt.route})
			if err == nil {
				t.Fatalf("New() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("New() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
