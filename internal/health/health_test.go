package health

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"redproxy/internal/balancer"
	"redproxy/internal/config"
)

func newPool(t *testing.T, urls ...string) *balancer.Pool {
	t.Helper()
	cfgs := make([]config.BackendConfig, len(urls))
	for i, u := range urls {
		cfgs[i] = config.BackendConfig{URL: u}
	}
	p, err := balancer.NewPool(config.StrategyRoundRobin, cfgs)
	if err != nil {
		t.Fatalf("NewPool() error: %v", err)
	}
	return p
}

// waitFor polls cond until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestFoldResult(t *testing.T) {
	tests := []struct {
		name             string
		healthy          bool
		ok               bool
		fails            int
		successes        int
		failThreshold    int
		recoverThreshold int
		wantHealthy      bool
		wantFails        int
		wantSuccesses    int
	}{
		{
			name:    "success resets failure streak",
			healthy: true, ok: true, fails: 2, successes: 0,
			failThreshold: 2, recoverThreshold: 1,
			wantHealthy: true, wantFails: 0, wantSuccesses: 1,
		},
		{
			name:    "single failure below threshold keeps healthy",
			healthy: true, ok: false, fails: 0, successes: 3,
			failThreshold: 2, recoverThreshold: 1,
			wantHealthy: true, wantFails: 1, wantSuccesses: 0,
		},
		{
			name:    "consecutive failures reach threshold and mark down",
			healthy: true, ok: false, fails: 1, successes: 0,
			failThreshold: 2, recoverThreshold: 1,
			wantHealthy: false, wantFails: 2, wantSuccesses: 0,
		},
		{
			name:    "threshold of one marks down immediately",
			healthy: true, ok: false, fails: 0, successes: 0,
			failThreshold: 1, recoverThreshold: 1,
			wantHealthy: false, wantFails: 1, wantSuccesses: 0,
		},
		{
			name:    "failure while already down keeps counting",
			healthy: false, ok: false, fails: 5, successes: 0,
			failThreshold: 2, recoverThreshold: 1,
			wantHealthy: false, wantFails: 6, wantSuccesses: 0,
		},
		{
			name:    "single success below recovery threshold stays down",
			healthy: false, ok: true, fails: 0, successes: 0,
			failThreshold: 2, recoverThreshold: 2,
			wantHealthy: false, wantFails: 0, wantSuccesses: 1,
		},
		{
			name:    "consecutive successes reach threshold and mark up",
			healthy: false, ok: true, fails: 0, successes: 1,
			failThreshold: 2, recoverThreshold: 2,
			wantHealthy: true, wantFails: 0, wantSuccesses: 2,
		},
		{
			name:    "recovery threshold of one marks up immediately",
			healthy: false, ok: true, fails: 4, successes: 0,
			failThreshold: 2, recoverThreshold: 1,
			wantHealthy: true, wantFails: 0, wantSuccesses: 1,
		},
		{
			name:    "success while already healthy is a no-op state-wise",
			healthy: true, ok: true, fails: 0, successes: 7,
			failThreshold: 2, recoverThreshold: 1,
			wantHealthy: true, wantFails: 0, wantSuccesses: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHealthy, gotFails, gotSuccesses := foldResult(
				tt.healthy, tt.ok, tt.fails, tt.successes, tt.failThreshold, tt.recoverThreshold)
			if gotHealthy != tt.wantHealthy || gotFails != tt.wantFails || gotSuccesses != tt.wantSuccesses {
				t.Errorf("foldResult(%v, %v, %d, %d, %d, %d) = (%v, %d, %d), want (%v, %d, %d)",
					tt.healthy, tt.ok, tt.fails, tt.successes, tt.failThreshold, tt.recoverThreshold,
					gotHealthy, gotFails, gotSuccesses, tt.wantHealthy, tt.wantFails, tt.wantSuccesses)
			}
		})
	}
}

func TestCheckerFlipsUnhealthyAndRecovers(t *testing.T) {
	var code atomic.Int32
	code.Store(http.StatusServiceUnavailable)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()

	pool := newPool(t, srv.URL)
	c := New([]Check{{
		Pool: pool, Path: "/healthz",
		Interval: 5 * time.Millisecond, Timeout: 500 * time.Millisecond,
		FailThreshold: 1, RecoverThreshold: 1,
	}})
	c.Start()
	defer c.Stop()

	waitFor(t, 2*time.Second, func() bool { return !pool.Backends()[0].Healthy() },
		"backend returning 503 should be marked unhealthy")

	code.Store(http.StatusOK)
	waitFor(t, 2*time.Second, func() bool { return pool.Backends()[0].Healthy() },
		"backend returning 200 should recover")
}

func TestCheckerKeepsHealthyBackendUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := newPool(t, srv.URL)
	c := New([]Check{{
		Pool: pool, Path: "/healthz",
		Interval: 5 * time.Millisecond, Timeout: 500 * time.Millisecond,
	}})
	c.Start()
	defer c.Stop()

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !pool.Backends()[0].Healthy() {
			t.Fatal("healthy backend was marked down")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCheckerConnectionRefusedMarksDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // free the port: nothing is listening anymore

	pool := newPool(t, srv.URL)
	c := New([]Check{{
		Pool: pool, Path: "/healthz",
		Interval: 5 * time.Millisecond, Timeout: 500 * time.Millisecond,
		FailThreshold: 1, RecoverThreshold: 1,
	}})
	c.Start()
	defer c.Stop()

	waitFor(t, 2*time.Second, func() bool { return !pool.Backends()[0].Healthy() },
		"unreachable backend should be marked unhealthy")
}

func TestCheckerTimeoutCountsAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := newPool(t, srv.URL)
	c := New([]Check{{
		Pool: pool, Path: "/healthz",
		Interval: 5 * time.Millisecond, Timeout: 30 * time.Millisecond,
		FailThreshold: 1, RecoverThreshold: 1,
	}})
	c.Start()
	defer c.Stop()

	waitFor(t, 2*time.Second, func() bool { return !pool.Backends()[0].Healthy() },
		"backend exceeding probe timeout should be marked unhealthy")
}

func TestCheckerStopIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := newPool(t, srv.URL)
	c := New([]Check{{
		Pool: pool, Path: "/healthz",
		Interval: 5 * time.Millisecond, Timeout: 500 * time.Millisecond,
	}})
	c.Start()
	c.Stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Stop() // second Stop must not hang or panic
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Stop() did not return")
	}
}

// Stop must cancel in-flight probes instead of waiting out their timeout.
func TestCheckerStopCancelsInflightProbes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // client went away
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	pool := newPool(t, srv.URL)
	c := New([]Check{{
		Pool: pool, Path: "/healthz",
		Interval: time.Hour, Timeout: 5 * time.Second, // only the startup probe runs
		FailThreshold: 1, RecoverThreshold: 1,
	}})
	c.Start()

	time.Sleep(50 * time.Millisecond) // let the startup probe get in flight

	start := time.Now()
	c.Stop()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Stop() took %v, want < 1s: in-flight probes must be cancelled", elapsed)
	}
}

// A second Start must not launch (and orphan) another generation of probers.
func TestCheckerStartIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pool := newPool(t, srv.URL)
	c := New([]Check{{
		Pool: pool, Path: "/healthz",
		Interval: 5 * time.Millisecond, Timeout: 500 * time.Millisecond,
	}})
	c.Start()
	defer c.Stop()

	first := c.ctx
	c.Start()
	if c.ctx != first {
		t.Fatal("second Start() replaced the checker context, orphaning the first prober generation")
	}
}

// Counter state must be tracked per backend: one failing backend goes down
// while its healthy neighbor stays up.
func TestCheckerPerBackendStateIsIndependent(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	pool := newPool(t, bad.URL, good.URL)
	c := New([]Check{{
		Pool: pool, Path: "/",
		Interval: 5 * time.Millisecond, Timeout: 500 * time.Millisecond,
		FailThreshold: 2, RecoverThreshold: 1,
	}})
	c.Start()
	defer c.Stop()

	badB, goodB := pool.Backends()[0], pool.Backends()[1]

	// Shared counters would reset the failure streak on every success from
	// the good backend, so the bad one would never reach the threshold.
	waitFor(t, 2*time.Second, func() bool { return !badB.Healthy() },
		"failing backend should reach the failure threshold and go down")

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !goodB.Healthy() {
			t.Fatal("healthy backend was marked down: state leaked across the pool")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
