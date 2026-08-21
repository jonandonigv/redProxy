package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"redproxy/internal/router"
)

// routedMetrics builds Router → Metrics.Middleware() → handler so requests
// carry a real route label.
func routedMetrics(t *testing.T, m *Metrics, inner http.HandlerFunc) *router.Router {
	t.Helper()
	r, err := router.New([]router.Route{{
		Name:       "api",
		PathPrefix: "/api",
		Handler:    m.Middleware()(inner),
	}})
	if err != nil {
		t.Fatalf("router.New() error: %v", err)
	}
	return r
}

func histCount(t *testing.T, m *Metrics, route string) uint64 {
	t.Helper()
	obs, err := m.requestDuration.GetMetricWithLabelValues(route)
	if err != nil {
		t.Fatalf("histogram with route %q: %v", route, err)
	}
	h, ok := obs.(prometheus.Histogram)
	if !ok {
		t.Fatalf("collector for route %q is %T, want prometheus.Histogram", route, obs)
	}
	var d dto.Metric
	if err := h.Write(&d); err != nil {
		t.Fatalf("histogram Write(): %v", err)
	}
	return d.GetHistogram().GetSampleCount()
}

func TestMiddlewareCountsRequestsByRouteAndStatus(t *testing.T) {
	m := New()
	r := routedMetrics(t, m, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/x" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	for i := 0; i < 2; i++ {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/y", nil))

	if got := testutil.ToFloat64(m.requestsTotal.WithLabelValues("api", "201")); got != 2 {
		t.Errorf("requests_total{route=api,code=201} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.requestsTotal.WithLabelValues("api", "500")); got != 1 {
		t.Errorf("requests_total{route=api,code=500} = %v, want 1", got)
	}
}

func TestMiddlewareObservesDuration(t *testing.T) {
	m := New()
	r := routedMetrics(t, m, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/slow", nil))

	if got := histCount(t, m, "api"); got != 1 {
		t.Errorf("histogram sample count = %d, want 1 after one request", got)
	}
}

func TestInFlightTracksConcurrentRequests(t *testing.T) {
	m := New()

	release := make(chan struct{})
	blocked := m.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))

	var served sync.WaitGroup // Done only after Middleware's defer has run
	for i := 0; i < 3; i++ {
		served.Add(1)
		go func() {
			defer served.Done()
			blocked.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && testutil.ToFloat64(m.inFlight) < 3 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := testutil.ToFloat64(m.inFlight); got != 3 {
		t.Errorf("in_flight mid-request = %v, want 3", got)
	}

	close(release)
	served.Wait()
	if got := testutil.ToFloat64(m.inFlight); got != 0 {
		t.Errorf("in_flight after completion = %v, want 0", got)
	}
}

func TestSetBackendUpGauge(t *testing.T) {
	m := New()

	m.SetBackendUp("http://a:9001", true)
	m.SetBackendUp("http://b:9001", false)

	if got := testutil.ToFloat64(m.backendUp.WithLabelValues("http://a:9001")); got != 1 {
		t.Errorf("backend_up{a} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.backendUp.WithLabelValues("http://b:9001")); got != 0 {
		t.Errorf("backend_up{b} = %v, want 0", got)
	}
}

func TestHandlerServesPrometheusExposition(t *testing.T) {
	m := New()
	// Generate traffic first: CounterVec/HistogramVec families only appear
	// once they have at least one child series.
	r := routedMetrics(t, m, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/x", nil))
	m.SetBackendUp("http://a:9001", true)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("content type = %q, want Prometheus text exposition format", ct)
	}
	body := rec.Body.String()
	for _, family := range []string{
		"redproxy_requests_total",
		"redproxy_request_duration_seconds",
		"redproxy_in_flight_requests",
		"redproxy_backend_up",
	} {
		if !strings.Contains(body, family) {
			t.Errorf("exposition missing metric family %q", family)
		}
	}
	if strings.Contains(body, "go_goroutines") {
		t.Error("exposition contains Go runtime metrics; registry should be app-metrics only")
	}
}
