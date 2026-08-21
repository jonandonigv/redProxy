package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func rateLimited(rps float64, burst int) http.Handler {
	return RateLimit(rps, burst)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func doGet(h http.Handler, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = ip + ":12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRateLimitAllowsBurstThenBlocks(t *testing.T) {
	// Negligible refill during the test: tokens come from the burst alone.
	h := rateLimited(0.0001, 2)

	if got := doGet(h, "10.0.0.1").Code; got != http.StatusOK {
		t.Fatalf("request 1: status = %d, want 200", got)
	}
	if got := doGet(h, "10.0.0.1").Code; got != http.StatusOK {
		t.Fatalf("request 2: status = %d, want 200 (within burst)", got)
	}

	rec := doGet(h, "10.0.0.1")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request 3: status = %d, want 429 once burst is exhausted", rec.Code)
	}
	if body := rec.Body.String(); body == "" {
		t.Error("429 response has empty body")
	}
}

func TestRateLimitIsPerClientIP(t *testing.T) {
	h := rateLimited(0.0001, 1)

	doGet(h, "10.0.0.1") // exhaust client A's bucket
	if got := doGet(h, "10.0.0.1").Code; got != http.StatusTooManyRequests {
		t.Fatalf("client A second request: status = %d, want 429", got)
	}
	if got := doGet(h, "10.0.0.2").Code; got != http.StatusOK {
		t.Fatalf("client B first request: status = %d, want 200 (independent bucket)", got)
	}
}

func TestRateLimitHandlesManyClients(t *testing.T) {
	h := rateLimited(1000, 1000) // effectively unlimited
	for i := 0; i < 500; i++ {
		ip := "192.168.0." + string(rune('a'+i%26)) + string(rune('0'+i%10))
		if got := doGet(h, ip).Code; got != http.StatusOK {
			t.Fatalf("client %s: status = %d, want 200", ip, got)
		}
	}
}
