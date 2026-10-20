package gateway

import (
	"runtime"
	"testing"
	"time"

	"redproxy/internal/health"
)

// TestStopWaitsForInFlightTeardown proves that a second, concurrent Stop does
// not return while the first Stop is still releasing background work. The
// pollerDone channel stands in for the real poller: the first Stop blocks in
// teardown until the test closes it.
func TestStopWaitsForInFlightTeardown(t *testing.T) {
	g := &Gateway{
		cancel:     func() {},
		pollerDone: make(chan struct{}),
		checker:    health.New(nil),
	}

	firstDone := make(chan struct{})
	go func() {
		g.Stop()
		close(firstDone)
	}()

	waitForStopClaim(t, g)

	secondDone := make(chan struct{})
	go func() {
		g.Stop()
		close(secondDone)
	}()

	// A concurrent Stop must still be blocked: the test owns teardown's
	// completion. The bounded wait only detects a premature return.
	select {
	case <-secondDone:
		t.Fatal("concurrent Stop returned before teardown finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(g.pollerDone)

	waitClosed(t, firstDone, "first Stop")
	waitClosed(t, secondDone, "second Stop")
}

// waitForStopClaim blocks until a Stop call has claimed teardown, so the
// second Stop is guaranteed to observe an in-flight teardown.
func waitForStopClaim(t *testing.T, g *Gateway) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		stopped := g.stopped
		g.mu.Unlock()
		if stopped {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("first Stop never claimed teardown")
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return", what)
	}
}
