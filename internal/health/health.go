// Package health implements active health checking: each configured route
// gets a background prober that periodically requests every backend's health
// endpoint and updates the backend's atomic health flag.
//
// Consecutive-failure/success thresholds avoid flapping on transient errors.
// Per-backend counters live inside the check goroutine, so no locking beyond
// Backend's own atomics is needed.
package health

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"redproxy/internal/balancer"
)

const (
	defaultPath             = "/"
	defaultInterval         = 10 * time.Second
	defaultTimeout          = 2 * time.Second
	defaultFailThreshold    = 2
	defaultRecoverThreshold = 1
	maxDrainBytes           = 4 << 10 // cap drained body so connections can be reused
)

// Check describes active probing for one route's backend pool. Zero-valued
// fields fall back to package defaults when passed to New.
type Check struct {
	Pool             *balancer.Pool
	Path             string        // health endpoint path, e.g. "/healthz"
	Interval         time.Duration // time between probe rounds
	Timeout          time.Duration // per-probe request timeout
	FailThreshold    int           // consecutive failures before marking down
	RecoverThreshold int           // consecutive successes before marking up
}

// Checker runs one goroutine per Check until stopped.
type Checker struct {
	client *http.Client
	checks []Check

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a Checker for the given checks, applying defaults to any
// zero-valued fields.
func New(checks []Check) *Checker {
	for i := range checks {
		c := &checks[i]
		if c.Path == "" {
			c.Path = defaultPath
		}
		if c.Interval <= 0 {
			c.Interval = defaultInterval
		}
		if c.Timeout <= 0 {
			c.Timeout = defaultTimeout
		}
		if c.FailThreshold <= 0 {
			c.FailThreshold = defaultFailThreshold
		}
		if c.RecoverThreshold <= 0 {
			c.RecoverThreshold = defaultRecoverThreshold
		}
	}
	return &Checker{
		client: &http.Client{},
		checks: checks,
	}
}

// Start launches a prober goroutine per check. It is a no-op if the Checker
// was already started: a Checker runs one generation of probers for its
// lifetime, released by Stop.
func (c *Checker) Start() {
	if c.cancel != nil {
		return
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	for _, chk := range c.checks {
		c.wg.Add(1)
		go c.run(c.ctx, chk)
	}
}

// Stop cancels all probes and waits for the goroutines to exit. It is safe
// to call more than once.
func (c *Checker) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

func (c *Checker) run(ctx context.Context, chk Check) {
	defer c.wg.Done()

	ticker := time.NewTicker(chk.Interval)
	defer ticker.Stop()

	// Probe immediately so a dead backend is caught at startup instead of
	// after a full interval.
	state := newState(chk.Pool)
	c.probeAll(ctx, state, chk)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.probeAll(ctx, state, chk)
		}
	}
}

// backendState tracks consecutive probe results for one backend. It is owned
// by the single check goroutine, so no synchronization is required.
type backendState struct {
	fails     int
	successes int
}

type poolState map[*balancer.Backend]*backendState

func newState(pool *balancer.Pool) poolState {
	st := make(poolState, len(pool.Backends()))
	for _, b := range pool.Backends() {
		st[b] = &backendState{}
	}
	return st
}

func (c *Checker) probeAll(ctx context.Context, state poolState, chk Check) {
	for _, b := range chk.Pool.Backends() {
		ok := c.probeOne(ctx, b.URL, chk.Path, chk.Timeout)
		st := state[b]

		healthy, fails, successes := foldResult(
			b.Healthy(), ok, st.fails, st.successes, chk.FailThreshold, chk.RecoverThreshold)
		st.fails, st.successes = fails, successes

		if healthy != b.Healthy() {
			b.SetHealthy(healthy)
		}
	}
}

// foldResult folds one probe result into the consecutive counters and
// returns the backend's desired health plus the updated counters.
func foldResult(healthy, ok bool, fails, successes, failThreshold, recoverThreshold int) (bool, int, int) {
	if ok {
		fails = 0
		successes++
		if !healthy && successes >= recoverThreshold {
			return true, fails, successes
		}
		return healthy, fails, successes
	}

	successes = 0
	fails++
	if healthy && fails >= failThreshold {
		return false, fails, successes
	}
	return healthy, fails, successes
}

// probeOne GETs the health endpoint and reports whether the backend answered
// with a 2xx within the timeout. The request is cancelled when ctx is done
// (e.g. on Stop), so in-flight probes do not delay shutdown.
func (c *Checker) probeOne(ctx context.Context, backendURL, path string, timeout time.Duration) bool {
	u := strings.TrimSuffix(backendURL, "/") + path

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Drain a bounded amount of the body so the connection can be reused.
	io.CopyN(io.Discard, resp.Body, maxDrainBytes)

	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
