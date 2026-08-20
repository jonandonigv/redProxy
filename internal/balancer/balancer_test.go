package balancer

import (
	"math"
	"sync"
	"testing"

	"redproxy/internal/config"
)

func newBackends(urls ...string) []config.BackendConfig {
	cfgs := make([]config.BackendConfig, len(urls))
	for i, u := range urls {
		cfgs[i] = config.BackendConfig{URL: u}
	}
	return cfgs
}

func TestNewBackendStartsHealthy(t *testing.T) {
	b := NewBackend("http://a:9001")
	if !b.Healthy() {
		t.Fatal("NewBackend() should start healthy")
	}
}

func TestBackendHealthToggles(t *testing.T) {
	b := NewBackend("http://a:9001")
	b.SetHealthy(false)
	if b.Healthy() {
		t.Fatal("SetHealthy(false) did not take effect")
	}
	b.SetHealthy(true)
	if !b.Healthy() {
		t.Fatal("SetHealthy(true) did not take effect")
	}
}

func TestBackendInflightCounter(t *testing.T) {
	b := NewBackend("http://a:9001")
	if got := b.Inflight(); got != 0 {
		t.Fatalf("Inflight() = %d, want 0", got)
	}
	if got := b.IncInflight(); got != 1 {
		t.Fatalf("IncInflight() = %d, want 1", got)
	}
	b.IncInflight()
	if got := b.Inflight(); got != 2 {
		t.Fatalf("Inflight() = %d, want 2", got)
	}
	if got := b.DecInflight(); got != 1 {
		t.Fatalf("DecInflight() = %d, want 1", got)
	}
}

func TestNewPoolRejectsUnknownStrategy(t *testing.T) {
	_, err := NewPool(config.Strategy("bogus"), newBackends("http://a:9001"))
	if err == nil {
		t.Fatal("NewPool() with unknown strategy should error")
	}
}

func TestNewPoolEmptyBackends(t *testing.T) {
	p, err := NewPool(config.StrategyRoundRobin, nil)
	if err != nil {
		t.Fatalf("NewPool() unexpected error: %v", err)
	}
	if got := p.Next(); got != nil {
		t.Fatalf("Next() = %v, want nil for empty pool", got)
	}
	if len(p.Backends()) != 0 {
		t.Fatalf("Backends() len = %d, want 0", len(p.Backends()))
	}
}

func TestNewPoolExposesBackends(t *testing.T) {
	urls := []string{"http://a:9001", "http://b:9001"}
	p, err := NewPool(config.StrategyRoundRobin, newBackends(urls...))
	if err != nil {
		t.Fatalf("NewPool() unexpected error: %v", err)
	}
	bs := p.Backends()
	if len(bs) != 2 {
		t.Fatalf("Backends() len = %d, want 2", len(bs))
	}
	if bs[0].URL != urls[0] || bs[1].URL != urls[1] {
		t.Errorf("Backends() URLs = [%s, %s], want [%s, %s]", bs[0].URL, bs[1].URL, urls[0], urls[1])
	}
}

func TestRoundRobinCyclesThroughBackends(t *testing.T) {
	p, _ := NewPool(config.StrategyRoundRobin, newBackends("http://a:9001", "http://b:9001", "http://c:9001"))
	want := []string{"http://a:9001", "http://b:9001", "http://c:9001", "http://a:9001"}
	for i, w := range want {
		got := p.Next()
		if got == nil {
			t.Fatalf("Next() at step %d = nil, want %s", i, w)
		}
		if got.URL != w {
			t.Errorf("Next() at step %d = %s, want %s", i, got.URL, w)
		}
	}
}

func TestRoundRobinSkipsUnhealthy(t *testing.T) {
	p, _ := NewPool(config.StrategyRoundRobin, newBackends("http://a:9001", "http://b:9001", "http://c:9001"))
	p.Backends()[1].SetHealthy(false) // mark b down

	for i := 0; i < 6; i++ {
		got := p.Next()
		if got == nil {
			t.Fatalf("Next() at step %d = nil", i)
		}
		if got.URL == "http://b:9001" {
			t.Fatalf("Next() returned unhealthy backend %s at step %d", got.URL, i)
		}
	}
}

func TestRoundRobinAllUnhealthyReturnsNil(t *testing.T) {
	p, _ := NewPool(config.StrategyRoundRobin, newBackends("http://a:9001", "http://b:9001"))
	p.Backends()[0].SetHealthy(false)
	p.Backends()[1].SetHealthy(false)
	if got := p.Next(); got != nil {
		t.Fatalf("Next() = %s, want nil when all backends down", got.URL)
	}
}

func TestLeastConnPicksFewestInflight(t *testing.T) {
	p, _ := NewPool(config.StrategyLeastConn, newBackends("http://a:9001", "http://b:9001", "http://c:9001"))
	bs := p.Backends()

	// a is busiest, so least-conn should prefer b then c (ties keep order).
	bs[0].IncInflight()
	bs[0].IncInflight()
	if got := p.Next(); got == nil || got.URL != "http://b:9001" {
		t.Fatalf("Next() = %v, want %s (fewest inflight)", got, "http://b:9001")
	}

	bs[1].IncInflight()
	bs[1].IncInflight()
	bs[1].IncInflight() // b now busier than c
	if got := p.Next(); got == nil || got.URL != "http://c:9001" {
		t.Fatalf("Next() = %v, want %s (fewest inflight)", got, "http://c:9001")
	}
}

func TestLeastConnSkipsUnhealthy(t *testing.T) {
	p, _ := NewPool(config.StrategyLeastConn, newBackends("http://a:9001", "http://b:9001"))
	p.Backends()[0].SetHealthy(false)
	got := p.Next()
	if got == nil || got.URL != "http://b:9001" {
		t.Fatalf("Next() = %v, want %s", got, "http://b:9001")
	}
}

func TestLeastConnAllUnhealthyReturnsNil(t *testing.T) {
	p, _ := NewPool(config.StrategyLeastConn, newBackends("http://a:9001"))
	p.Backends()[0].SetHealthy(false)
	if got := p.Next(); got != nil {
		t.Fatalf("Next() = %s, want nil", got.URL)
	}
}

func TestRandomReturnsOnlyHealthy(t *testing.T) {
	p, _ := NewPool(config.StrategyRandom, newBackends("http://a:9001", "http://b:9001", "http://c:9001"))
	p.Backends()[1].SetHealthy(false) // b down

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		got := p.Next()
		if got == nil {
			t.Fatalf("Next() = nil during healthy run at iter %d", i)
		}
		if got.URL == "http://b:9001" {
			t.Fatalf("Next() returned unhealthy backend at iter %d", i)
		}
		seen[got.URL] = true
	}
	if len(seen) != 2 {
		t.Fatalf("random picked %d distinct backends, want 2 (a and c)", len(seen))
	}
}

func TestRandomAllUnhealthyReturnsNil(t *testing.T) {
	p, _ := NewPool(config.StrategyRandom, newBackends("http://a:9001", "http://b:9001"))
	p.Backends()[0].SetHealthy(false)
	p.Backends()[1].SetHealthy(false)
	if got := p.Next(); got != nil {
		t.Fatalf("Next() = %s, want nil when all down", got.URL)
	}
}

// Regression: the round-robin counter must survive wraparound without a
// negative modulo (panic) on any platform.
func TestRoundRobinCounterOverflow(t *testing.T) {
	r := &roundRobin{backends: []*Backend{NewBackend("http://a:9001"), NewBackend("http://b:9001")}}
	r.idx.Store(math.MaxUint64 - 1) // next Add(1) wraps to MaxUint64, then 0

	for i := 0; i < 4; i++ {
		if got := r.Next(); got == nil {
			t.Fatalf("Next() = nil after counter wrap at step %d", i)
		}
	}
}

// Run with -race: exercises the atomics/concurrency contract of the package.
func TestBalancerConcurrentUse(t *testing.T) {
	strategies := []config.Strategy{config.StrategyRoundRobin, config.StrategyLeastConn, config.StrategyRandom}
	for _, s := range strategies {
		t.Run(string(s), func(t *testing.T) {
			p, err := NewPool(s, newBackends("http://a:9001", "http://b:9001", "http://c:9001"))
			if err != nil {
				t.Fatalf("NewPool() error: %v", err)
			}

			// One goroutine continuously flips a backend's health.
			stop := make(chan struct{})
			var flipper sync.WaitGroup
			flipper.Add(1)
			go func() {
				defer flipper.Done()
				for {
					select {
					case <-stop:
						return
					default:
						p.Backends()[0].SetHealthy(false)
						p.Backends()[0].SetHealthy(true)
					}
				}
			}()

			var workers sync.WaitGroup
			for i := 0; i < 8; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					for j := 0; j < 500; j++ {
						b := p.Next()
						if b == nil {
							continue
						}
						b.IncInflight()
						b.DecInflight()
					}
				}()
			}
			workers.Wait()
			close(stop)
			flipper.Wait()
		})
	}
}
