package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"redproxy/internal/config"
)

func TestHeadersManipulatesRequest(t *testing.T) {
	var gotOld, gotNew string
	cfg := config.HeadersConfig{
		RequestSet:    map[string]string{"X-Injected": "yes"},
		RequestRemove: []string{"X-Stripped"},
	}
	h := Headers(cfg)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotOld = r.Header.Get("X-Stripped")
		gotNew = r.Header.Get("X-Injected")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Stripped", "should-disappear")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotOld != "" {
		t.Errorf("X-Stripped reached the handler as %q, want it removed", gotOld)
	}
	if gotNew != "yes" {
		t.Errorf("X-Injected = %q, want yes", gotNew)
	}
}

func TestHeadersManipulatesResponse(t *testing.T) {
	cfg := config.HeadersConfig{
		ResponseSet:    map[string]string{"X-Proxy": "redproxy"},
		ResponseRemove: []string{"Server"},
	}
	h := Headers(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "secret-backend")
		_, _ = w.Write([]byte("body"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Server"); got != "" {
		t.Errorf("Server header = %q, want it removed before responding", got)
	}
	if got := rec.Header().Get("X-Proxy"); got != "redproxy" {
		t.Errorf("X-Proxy header = %q, want redproxy", got)
	}
}

func TestHeadersAppliedBeforeBodyWrite(t *testing.T) {
	cfg := config.HeadersConfig{ResponseSet: map[string]string{"X-Early": "1"}}
	h := Headers(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Write body without calling WriteHeader: headers must still be applied.
		_, _ = w.Write([]byte("x"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Early"); got != "1" {
		t.Errorf("X-Early = %q, want 1 (headers set despite implicit WriteHeader)", got)
	}
}
