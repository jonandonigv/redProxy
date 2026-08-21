package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"redproxy/internal/balancer"
	"redproxy/internal/config"
)

// echoBackend is a backend that records the last request it saw and answers
// with "path|query|forwardedFor".
type echoBackend struct {
	srv *httptest.Server

	mu           sync.Mutex
	lastPath     string
	lastQuery    string
	lastForward  string
	requestCount int
}

func newEchoBackend(t *testing.T) *echoBackend {
	t.Helper()
	b := &echoBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.lastPath = r.URL.Path
		b.lastQuery = r.URL.RawQuery
		b.lastForward = r.Header.Get("X-Forwarded-For")
		b.requestCount++
		b.mu.Unlock()
		fmt.Fprintf(w, "%s|%s|%s", r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Forwarded-For"))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *echoBackend) snapshot() (path, query, forward string, count int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastPath, b.lastQuery, b.lastForward, b.requestCount
}

func newPool(t *testing.T, strategy config.Strategy, urls ...string) *balancer.Pool {
	t.Helper()
	cfgs := make([]config.BackendConfig, len(urls))
	for i, u := range urls {
		cfgs[i] = config.BackendConfig{URL: u}
	}
	p, err := balancer.NewPool(strategy, cfgs)
	if err != nil {
		t.Fatalf("NewPool() error: %v", err)
	}
	return p
}

func do(t *testing.T, p http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func TestProxyForwardsRequest(t *testing.T) {
	backend := newEchoBackend(t)
	pool := newPool(t, config.StrategyRoundRobin, backend.srv.URL)
	p := New(pool, "")

	rec := do(t, p, "/hello/world?a=1&b=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	path, query, forward, _ := backend.snapshot()
	if path != "/hello/world" {
		t.Errorf("backend saw path %q, want /hello/world", path)
	}
	if query != "a=1&b=2" {
		t.Errorf("backend saw query %q, want a=1&b=2", query)
	}
	if forward == "" {
		t.Error("backend saw empty X-Forwarded-For, want it set by the proxy")
	}

	want := "/hello/world|a=1&b=2|" + forward
	if got := rec.Body.String(); got != want {
		t.Errorf("response body = %q, want %q", got, want)
	}
	if got := pool.Backends()[0].Inflight(); got != 0 {
		t.Errorf("in-flight after request = %d, want 0 (must be decremented)", got)
	}
}

func TestProxyStripsPrefix(t *testing.T) {
	backend := newEchoBackend(t)
	pool := newPool(t, config.StrategyRoundRobin, backend.srv.URL)
	p := New(pool, "/api")

	do(t, p, "/api/users")
	if path, _, _, _ := backend.snapshot(); path != "/users" {
		t.Errorf("backend saw path %q, want /users", path)
	}

	do(t, p, "/api")
	if path, _, _, _ := backend.snapshot(); path != "/" {
		t.Errorf("backend saw path %q, want / (exact prefix match collapses to root)", path)
	}
}

func TestProxyDistributesAcrossBackends(t *testing.T) {
	a := newEchoBackend(t)
	b := newEchoBackend(t)
	pool := newPool(t, config.StrategyRoundRobin, a.srv.URL, b.srv.URL)
	p := New(pool, "")

	for i := 0; i < 4; i++ {
		if rec := do(t, p, "/x"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}

	if _, _, _, n := a.snapshot(); n == 0 {
		t.Error("backend a never received traffic")
	}
	if _, _, _, n := b.snapshot(); n == 0 {
		t.Error("backend b never received traffic")
	}
}

func TestProxyReturns503WhenAllBackendsDown(t *testing.T) {
	backend := newEchoBackend(t)
	pool := newPool(t, config.StrategyRoundRobin, backend.srv.URL)
	pool.Backends()[0].SetHealthy(false)

	p := New(pool, "")
	rec := do(t, p, "/x")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no backends are healthy", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no healthy") {
		t.Errorf("body = %q, want it to mention no healthy upstream", rec.Body.String())
	}
}

func TestProxyMarksBackendUnhealthyOnConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listening anymore

	pool := newPool(t, config.StrategyRoundRobin, url)
	p := New(pool, "")

	rec := do(t, p, "/x")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 on unreachable backend", rec.Code)
	}
	if pool.Backends()[0].Healthy() {
		t.Error("backend still marked healthy after connection failure; passive marking missing")
	}
}
