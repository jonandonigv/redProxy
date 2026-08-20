// Package balancer holds the upstream Backend type and the per-route load
// balancing strategies (round-robin, least-connections, random).
//
// Hot-path state on a Backend (health + in-flight request count) is stored in
// atomics so picking a backend requires no locking even under concurrent use.
package balancer

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync/atomic"

	"redproxy/internal/config"
)

// Backend is a single upstream server.
type Backend struct {
	URL string // upstream URL, e.g. "http://localhost:9001"

	healthy  atomic.Bool
	inflight atomic.Int64
}

// NewBackend creates a backend that starts healthy.
func NewBackend(url string) *Backend {
	b := &Backend{URL: url}
	b.healthy.Store(true)
	return b
}

// Healthy reports whether the backend is currently eligible for traffic.
func (b *Backend) Healthy() bool { return b.healthy.Load() }

// SetHealthy flips the backend's eligibility (used by the health checker and
// the proxy's passive error handling).
func (b *Backend) SetHealthy(v bool) { b.healthy.Store(v) }

// IncInflight increments and returns the in-flight request count.
func (b *Backend) IncInflight() int64 { return b.inflight.Add(1) }

// DecInflight decrements and returns the in-flight request count.
func (b *Backend) DecInflight() int64 { return b.inflight.Add(-1) }

// Inflight returns the current in-flight request count.
func (b *Backend) Inflight() int64 { return b.inflight.Load() }

// Balancer selects the next backend for a request. It returns nil when no
// healthy backend is available (callers map that to a 503).
type Balancer interface {
	Next() *Backend
}

// Pool owns a route's backends and the strategy used to pick among them.
// It satisfies Balancer via Next and also exposes Backends so the health
// checker can read/write each backend's state.
type Pool struct {
	backends []*Backend
	balancer Balancer
}

// NewPool builds a Pool for the given strategy from the route's backends.
func NewPool(strategy config.Strategy, cfgs []config.BackendConfig) (*Pool, error) {
	backends := make([]*Backend, 0, len(cfgs))
	for _, c := range cfgs {
		backends = append(backends, NewBackend(c.URL))
	}

	var b Balancer
	switch strategy {
	case config.StrategyRoundRobin:
		b = &roundRobin{backends: backends}
	case config.StrategyLeastConn:
		b = &leastConn{backends: backends}
	case config.StrategyRandom:
		b = &random{backends: backends}
	default:
		return nil, fmt.Errorf("unknown load-balancing strategy %q", strategy)
	}

	return &Pool{backends: backends, balancer: b}, nil
}

// Next picks the next eligible backend according to the strategy.
func (p *Pool) Next() *Backend { return p.balancer.Next() }

// Backends returns the pool's backends (shared with the health checker).
func (p *Pool) Backends() []*Backend { return p.backends }

// roundRobin rotates through backends, skipping unhealthy ones.
// The counter is unsigned so wraparound never produces a negative modulo.
type roundRobin struct {
	backends []*Backend
	idx      atomic.Uint64
}

func (r *roundRobin) Next() *Backend {
	n := uint64(len(r.backends))
	if n == 0 {
		return nil
	}
	base := r.idx.Add(1) - 1 // uint64: wraps safely at MaxUint64
	for i := uint64(0); i < n; i++ {
		b := r.backends[(base+i)%n]
		if b.Healthy() {
			return b
		}
	}
	return nil
}

// leastConn picks the healthy backend with the fewest in-flight requests.
type leastConn struct {
	backends []*Backend
}

func (l *leastConn) Next() *Backend {
	var best *Backend
	bestN := int64(math.MaxInt64)
	for _, b := range l.backends {
		if !b.Healthy() {
			continue
		}
		if n := b.Inflight(); n < bestN {
			best = b
			bestN = n
		}
	}
	return best
}

// random picks a uniformly random healthy backend.
type random struct {
	backends []*Backend
}

func (r *random) Next() *Backend {
	healthy := make([]*Backend, 0, len(r.backends))
	for _, b := range r.backends {
		if b.Healthy() {
			healthy = append(healthy, b)
		}
	}
	if len(healthy) == 0 {
		return nil
	}
	return healthy[rand.IntN(len(healthy))]
}
