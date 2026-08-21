package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChainAppliesMiddlewaresInOrder(t *testing.T) {
	trace := strings.Builder{}
	appendMark := func(mark string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace.WriteString(mark + ">")
				next.ServeHTTP(w, r)
				trace.WriteString("<" + mark)
			})
		}
	}

	h := Chain(appendMark("a"), appendMark("b"))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trace.WriteString("H")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := trace.String(); got != "a>b>H<b<a" {
		t.Errorf("execution trace = %q, want %q (outermost first, unwinding in reverse)", got, "a>b>H<b<a")
	}
}

func TestChainWithNoMiddlewaresPassesThrough(t *testing.T) {
	called := false
	h := Chain()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("handler was never called through an empty chain")
	}
}
