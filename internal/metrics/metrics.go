// Package metrics wires redProxy's Prometheus instrumentation: a private
// registry, the request metrics middleware (requests_total,
// request_duration_seconds, in_flight), the backend_up gauge, and the
// /metrics exposition handler for the admin server.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"redproxy/internal/middleware"
	"redproxy/internal/router"
)

const namespace = "redproxy"

// Metrics owns redProxy's collectors. All state lives in Prometheus's
// thread-safe metric types; nothing here needs extra locking.
type Metrics struct {
	reg             *prometheus.Registry
	requestsTotal   *prometheus.CounterVec   // route, code
	requestDuration *prometheus.HistogramVec // route
	inFlight        prometheus.Gauge
	backendUp       *prometheus.GaugeVec // backend
}

// New creates the collectors on a fresh app-only registry (no Go/runtime
// collector noise).
func New() *Metrics {
	reg := prometheus.NewRegistry()

	requestsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "requests_total",
		Help:      "Requests handled per route and response status code.",
	}, []string{"route", "code"})

	requestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "request_duration_seconds",
		Help:      "Request handling duration per route.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route"})

	inFlight := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "in_flight_requests",
		Help:      "Requests currently being handled.",
	})

	backendUp := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "backend_up",
		Help:      "1 when the backend is considered healthy, 0 otherwise.",
	}, []string{"backend"})

	reg.MustRegister(requestsTotal, requestDuration, inFlight, backendUp)

	return &Metrics{
		reg:             reg,
		requestsTotal:   requestsTotal,
		requestDuration: requestDuration,
		inFlight:        inFlight,
		backendUp:       backendUp,
	}
}

// Middleware instruments request counting, durations, and in-flight gauge.
func (m *Metrics) Middleware() middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := router.RouteName(r.Context())

			m.inFlight.Inc()
			defer m.inFlight.Dec()

			start := time.Now()
			rec := &middleware.StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
			next.ServeHTTP(rec, r)

			m.requestsTotal.WithLabelValues(route, strconv.Itoa(rec.Status)).Inc()
			m.requestDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
		})
	}
}

// SetBackendUp reports a backend's health to the backend_up gauge. Wiring
// polls the balancer pools (or reacts to checker updates) with this.
func (m *Metrics) SetBackendUp(backend string, up bool) {
	v := 0.0
	if up {
		v = 1
	}
	m.backendUp.WithLabelValues(backend).Set(v)
}

// Handler returns the /metrics exposition handler over this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
