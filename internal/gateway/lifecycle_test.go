package gateway_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// observeNoProbes asserts that the Backend keeps answering no health probes
// for several check intervals. It is the observable proof that Stop released
// the health checker's background work.
func observeNoProbes(t *testing.T, backend *harnessBackend, settled int64) {
	t.Helper()

	deadline := time.Now().Add(5 * harnessHealthInterval)
	for time.Now().Before(deadline) {
		if got := backend.probed(); got != settled {
			t.Fatalf("health probes grew after Stop: %d -> %d", settled, got)
		}
		time.Sleep(harnessPollInterval)
	}
}

// waitForProbes blocks until the health checker has probed the Backend at
// least want times, so a later "no growth" assertion has something to compare
// against.
func waitForProbes(t *testing.T, backend *harnessBackend, want int64) {
	t.Helper()

	eventually(t, harnessSettleTimeout, func() error {
		if got := backend.probed(); got < want {
			return fmt.Errorf("health probes = %d, want at least %d", got, want)
		}
		return nil
	})
}

func TestGatewayDoubleStartIsNoOp(t *testing.T) {
	backend := newHarnessBackend(t, "web")
	gw := newGateway(t, configFor(harnessRoute("web", "/", backend)))

	gw.Start()
	gw.Start()

	if rec := request(t, gw, "", "/ping"); rec.Code != http.StatusOK {
		t.Fatalf("request after double Start status = %d, want 200", rec.Code)
	}
	waitForProbes(t, backend, 2)

	// Stop released the single set of background goroutines despite the
	// second Start.
	gw.Stop()
	observeNoProbes(t, backend, backend.probed())
}

func TestGatewayDoubleStopIsSafe(t *testing.T) {
	backend := newHarnessBackend(t, "web")
	gw := newGateway(t, configFor(harnessRoute("web", "/", backend)))

	gw.Start()
	waitForProbes(t, backend, 1)

	gw.Stop()
	gw.Stop()

	if rec := request(t, gw, "", "/ping"); rec.Code != http.StatusOK {
		t.Fatalf("request after double Stop status = %d, want 200", rec.Code)
	}
}

func TestGatewayStopBeforeStartIsSafe(t *testing.T) {
	backend := newHarnessBackend(t, "web")
	gw := newGateway(t, configFor(harnessRoute("web", "/", backend)))

	gw.Stop()
	gw.Start() // the lifecycle is over; Start must be a no-op

	if rec := request(t, gw, "", "/ping"); rec.Code != http.StatusOK {
		t.Fatalf("request after Stop-before-Start status = %d, want 200", rec.Code)
	}
	observeNoProbes(t, backend, 0)
}

func TestGatewayStopHaltsHealthProbing(t *testing.T) {
	backend := newHarnessBackend(t, "web")
	gw := newGateway(t, configFor(harnessRoute("web", "/", backend)))

	gw.Start()
	waitForProbes(t, backend, 2)

	gw.Stop()
	settled := backend.probed()

	// Several check intervals pass; a live checker would have probed again.
	observeNoProbes(t, backend, settled)
}

func TestGatewayRestartAfterStopIsNoOp(t *testing.T) {
	backend := newHarnessBackend(t, "web")
	gw := newGateway(t, configFor(harnessRoute("web", "/", backend)))

	gw.Start()
	waitForProbes(t, backend, 1)

	gw.Stop()
	settled := backend.probed()

	gw.Start() // restart is unsupported: safe no-op, never a panic

	if rec := request(t, gw, "", "/ping"); rec.Code != http.StatusOK {
		t.Fatalf("request after restart attempt status = %d, want 200", rec.Code)
	}
	observeNoProbes(t, backend, settled)

	gw.Stop()
}
