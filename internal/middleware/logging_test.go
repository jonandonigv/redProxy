package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"redproxy/internal/router"
)

// loggedRouter builds Router → Logging(logger) → handler so log records
// carry the real route name from dispatch.
func loggedRouter(t *testing.T, logger *slog.Logger, inner http.HandlerFunc) *router.Router {
	t.Helper()
	r, err := router.New([]router.Route{{
		Name: "api", PathPrefix: "/api",
		Handler: Logging(logger)(inner),
	}})
	if err != nil {
		t.Fatalf("router.New() error: %v", err)
	}
	return r
}

func decodeLog(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	return rec
}

func TestLoggingRecordsRequestFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := loggedRouter(t, logger, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})

	req := httptest.NewRequest(http.MethodPost, "/api/things", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	rec := decodeLog(t, &buf)
	if rec["msg"] != "request" {
		t.Errorf("msg = %v, want request", rec["msg"])
	}
	if rec["method"] != http.MethodPost {
		t.Errorf("method = %v, want POST", rec["method"])
	}
	if rec["path"] != "/api/things" {
		t.Errorf("path = %v, want /api/things", rec["path"])
	}
	if s, ok := rec["status"].(float64); !ok || int(s) != http.StatusCreated {
		t.Errorf("status = %v, want %d", rec["status"], http.StatusCreated)
	}
	if rec["route"] != "api" {
		t.Errorf("route = %v, want api (from router dispatch)", rec["route"])
	}
	if d, ok := rec["duration"].(float64); !ok || d <= 0 {
		t.Errorf("duration = %v, want positive nanoseconds", rec["duration"])
	}
}

func TestLoggingDefaultsTo200WithoutWriteHeader(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("implicit 200"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	rec := decodeLog(t, &buf)
	if s, ok := rec["status"].(float64); !ok || int(s) != http.StatusOK {
		t.Errorf("status = %v, want implicit %d", rec["status"], http.StatusOK)
	}
}

func TestLoggingMeasuresDuration(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	h := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	rec := decodeLog(t, &buf)
	d, ok := rec["duration"].(float64)
	if !ok || d < 5_000_000 { // 5ms in ns; handler slept exactly 5ms
		t.Errorf("duration = %v, want >= 5ms of nanoseconds", rec["duration"])
	}
}
