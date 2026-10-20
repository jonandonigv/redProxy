package gateway_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"redproxy/internal/config"
	"redproxy/internal/gateway"
)

// rateLimitedRoute is a route that admits one burst and then throttles.
func rateLimitedRoute(name, backendURL string) config.RouteConfig {
	rc := route(name, "", "/", backendURL)
	rc.Middlewares.RateLimit = &config.RateLimitConfig{RPS: 0.01, Burst: 1}
	return rc
}

// burstThenThrottle consumes the route's burst with a 200 and then gets a 429,
// returning the rejected response so callers can inspect it.
func burstThenThrottle(t *testing.T, gw *gateway.Gateway) *httptest.ResponseRecorder {
	t.Helper()

	if rec := request(t, gw, "", "/first"); rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}
	rec := request(t, gw, "", "/second")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec.Code)
	}
	return rec
}

func TestGatewayRateLimitReturns429AfterBurst(t *testing.T) {
	backend := newBackend(t, "web")
	gw := newGateway(t, configFor(rateLimitedRoute("throttled", backend.URL)))

	rec := burstThenThrottle(t, gw)
	if got := rec.Body.String(); !strings.Contains(got, "rate limit exceeded") {
		t.Errorf("429 body = %q, want it to mention the rate limit", got)
	}
}

// metricSample is one parsed line of the Prometheus text exposition.
type metricSample struct {
	name   string
	labels map[string]string
	value  float64
}

// scrapeMetrics reads /metrics through the Gateway's admin handler exactly as
// a Prometheus server would, and parses the exposition text.
func scrapeMetrics(t *testing.T, gw *gateway.Gateway) []metricSample {
	t.Helper()

	rec := httptest.NewRecorder()
	gw.AdminHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}

	var samples []metricSample
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		id, raw, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("malformed metric line %q: missing value", line)
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			t.Fatalf("malformed metric line %q: %v", line, err)
		}

		s := metricSample{labels: map[string]string{}, value: value}
		if brace := strings.IndexByte(id, '{'); brace >= 0 {
			s.name = id[:brace]
			inner := strings.TrimSuffix(id[brace+1:], "}")
			for _, pair := range strings.Split(inner, ",") {
				k, v, ok := strings.Cut(pair, "=")
				if !ok {
					t.Fatalf("malformed metric label %q in %q", pair, line)
				}
				s.labels[k] = strings.Trim(v, `"`)
			}
		} else {
			s.name = id
		}
		samples = append(samples, s)
	}
	return samples
}

// metricValue returns the value of the sample with exactly these labels.
func metricValue(t *testing.T, samples []metricSample, name string, labels map[string]string) float64 {
	t.Helper()

	for _, s := range samples {
		if s.name != name || len(s.labels) != len(labels) {
			continue
		}
		match := true
		for k, v := range labels {
			if s.labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return s.value
		}
	}
	t.Fatalf("metric %s%v not found", name, labels)
	return 0
}

func TestGatewayRateLimitedRequestAppearsInMetrics(t *testing.T) {
	backend := newBackend(t, "web")
	gw := newGateway(t, configFor(rateLimitedRoute("throttled", backend.URL)))

	burstThenThrottle(t, gw)

	samples := scrapeMetrics(t, gw)
	if got := metricValue(t, samples, "redproxy_requests_total", map[string]string{
		"code": "429", "route": "throttled",
	}); got != 1 {
		t.Errorf("redproxy_requests_total{code=429,route=throttled} = %v, want 1", got)
	}
	if got := metricValue(t, samples, "redproxy_requests_total", map[string]string{
		"code": "200", "route": "throttled",
	}); got != 1 {
		t.Errorf("redproxy_requests_total{code=200,route=throttled} = %v, want 1", got)
	}
}

// logBuffer is a concurrency-safe io.Writer for captured JSON log output.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records parses the captured lines as JSON log records, failing on anything
// that is not valid JSON.
func (b *logBuffer) records(t *testing.T) []map[string]any {
	t.Helper()

	b.mu.Lock()
	raw := b.buf.String()
	b.mu.Unlock()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not valid JSON: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

func TestGatewayRateLimitedRequestIsLoggedWithRouteAndStatus(t *testing.T) {
	backend := newBackend(t, "web")
	logs := &logBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))

	gw := newGatewayWithLogger(t, configFor(rateLimitedRoute("throttled", backend.URL)), logger)
	gw.Start()

	burstThenThrottle(t, gw)

	var rejected []map[string]any
	for _, rec := range logs.records(t) {
		if rec["msg"] == "request" && rec["status"] == float64(http.StatusTooManyRequests) {
			rejected = append(rejected, rec)
		}
	}
	if len(rejected) != 1 {
		t.Fatalf("log records with status 429 = %d, want 1", len(rejected))
	}
	if got := rejected[0]["route"]; got != "throttled" {
		t.Errorf("429 log route = %v, want throttled", got)
	}
	if got := rejected[0]["status"]; got != float64(http.StatusTooManyRequests) {
		t.Errorf("429 log status = %v, want 429", got)
	}
}
