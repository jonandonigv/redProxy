package gateway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"redproxy/internal/config"
)

const (
	// harnessHealthPath is the endpoint the gateway probes. Normal traffic
	// uses any other path, so a backend can be "down" for probes while still
	// serving requests.
	harnessHealthPath = "/healthz"

	harnessHealthInterval = 100 * time.Millisecond
	harnessHealthTimeout  = 50 * time.Millisecond
	harnessSettleTimeout  = 5 * time.Second
	harnessPollInterval   = 5 * time.Millisecond
)

// harnessBackend is an httptest Backend whose health endpoint can be flipped
// independently of its normal traffic handling.
type harnessBackend struct {
	name string
	srv  *httptest.Server

	healthy atomic.Bool
	traffic atomic.Int64
}

func newHarnessBackend(t *testing.T, name string) *harnessBackend {
	t.Helper()

	b := &harnessBackend{name: name}
	b.healthy.Store(true)
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == harnessHealthPath {
			if !b.healthy.Load() {
				http.Error(w, "unhealthy", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		b.traffic.Add(1)
		fmt.Fprint(w, b.name)
	}))
	t.Cleanup(b.srv.Close)

	return b
}

// setHealthy flips what the health endpoint reports.
func (b *harnessBackend) setHealthy(v bool) { b.healthy.Store(v) }

// served returns how many normal (non-probe) requests the backend has served.
func (b *harnessBackend) served() int64 { return b.traffic.Load() }

// eventually polls check until it succeeds or the timeout expires. Fixed
// waits are never assertions: tests observe state, not elapsed time.
func eventually(t *testing.T, timeout time.Duration, check func() error) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = check(); err == nil {
			return
		}
		time.Sleep(harnessPollInterval)
	}
	t.Fatalf("condition not met within %s: %v", timeout, err)
}

// harnessRoute builds a route whose active checks probe the harness Backends
// with a short interval. Thresholds stay at the health package defaults
// (2 failures down, 1 success up).
func harnessRoute(name, pathPrefix string, backends ...*harnessBackend) config.RouteConfig {
	rc := config.RouteConfig{
		Name:     name,
		Match:    config.MatchConfig{PathPrefix: pathPrefix},
		Strategy: config.StrategyRoundRobin,
		HealthCheck: config.HealthCheckConfig{
			Path:     harnessHealthPath,
			Interval: config.Duration(harnessHealthInterval),
			Timeout:  config.Duration(harnessHealthTimeout),
		},
	}
	for _, b := range backends {
		rc.Backends = append(rc.Backends, config.BackendConfig{URL: b.srv.URL})
	}
	return rc
}

func TestGatewayExcludesDownBackendAndReadmitsIt(t *testing.T) {
	survivor := newHarnessBackend(t, "survivor")
	failing := newHarnessBackend(t, "failing")

	gw := newGateway(t, configFor(harnessRoute("api", "/", survivor, failing)))
	gw.Start()

	// Both Backends serve traffic while healthy.
	eventually(t, harnessSettleTimeout, func() error {
		if rec := request(t, gw, "", "/ping"); rec.Code != http.StatusOK {
			return fmt.Errorf("status = %d, want 200", rec.Code)
		}
		if survivor.served() == 0 || failing.served() == 0 {
			return fmt.Errorf("not both receiving traffic yet: survivor=%d failing=%d",
				survivor.served(), failing.served())
		}
		return nil
	})

	// The failing Backend still answers normal requests; only its health
	// endpoint reports down.
	failing.setHealthy(false)
	resp, err := failing.srv.Client().Get(failing.srv.URL + "/ping")
	if err != nil {
		t.Fatalf("direct request to failing Backend: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("failing Backend normal traffic status = %d, want 200", resp.StatusCode)
	}

	// Active checks cut it out of rotation: a full round-robin cycle still
	// succeeds on the survivor.
	eventually(t, harnessSettleTimeout, func() error {
		for i := 0; i < 4; i++ {
			rec := request(t, gw, "", "/ping")
			if rec.Code != http.StatusOK || rec.Body.String() != "survivor" {
				return fmt.Errorf("request %d = %d %q, want 200 survivor", i, rec.Code, rec.Body.String())
			}
		}
		return nil
	})

	// Once excluded, it receives no further traffic at all.
	before := failing.served()
	for i := 0; i < 20; i++ {
		rec := request(t, gw, "", "/ping")
		if rec.Code != http.StatusOK || rec.Body.String() != "survivor" {
			t.Fatalf("request %d = %d %q, want 200 survivor", i, rec.Code, rec.Body.String())
		}
	}
	if got := failing.served(); got != before {
		t.Errorf("failed Backend served %d requests after exclusion, want %d", got-before, 0)
	}

	// Recovery readmits it: the failed Backend sees traffic again and
	// responses stay 200.
	failing.setHealthy(true)
	eventually(t, harnessSettleTimeout, func() error {
		rec := request(t, gw, "", "/ping")
		if rec.Code != http.StatusOK {
			return fmt.Errorf("status = %d, want 200", rec.Code)
		}
		if failing.served() == before {
			return fmt.Errorf("restored Backend not receiving traffic yet")
		}
		return nil
	})
	if got := failing.served(); got == before {
		t.Errorf("restored Backend never served traffic again")
	}
}

func TestGatewayReturns503WhenAllBackendsDown(t *testing.T) {
	one := newHarnessBackend(t, "one")
	two := newHarnessBackend(t, "two")

	gw := newGateway(t, configFor(harnessRoute("api", "/", one, two)))
	gw.Start()

	// Confirm the route works before the outage.
	eventually(t, harnessSettleTimeout, func() error {
		rec := request(t, gw, "", "/ping")
		if rec.Code != http.StatusOK {
			return fmt.Errorf("status = %d, want 200", rec.Code)
		}
		return nil
	})

	one.setHealthy(false)
	two.setHealthy(false)

	eventually(t, harnessSettleTimeout, func() error {
		rec := request(t, gw, "", "/ping")
		if rec.Code != http.StatusServiceUnavailable {
			return fmt.Errorf("status = %d, want 503", rec.Code)
		}
		return nil
	})

	// The outage persists while both health endpoints report down.
	for i := 0; i < 5; i++ {
		if rec := request(t, gw, "", "/ping"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d status = %d, want 503", i, rec.Code)
		}
	}
}
