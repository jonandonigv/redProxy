package middleware

import (
	"net/http"

	"redproxy/internal/config"
)

// Headers returns a middleware that manipulates request and response
// headers. On both sides removals are applied before sets. Response changes
// happen just before the first write, so handlers may set headers freely.
func Headers(cfg config.HeadersConfig) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, k := range cfg.RequestRemove {
				r.Header.Del(k)
			}
			for k, v := range cfg.RequestSet {
				r.Header.Set(k, v)
			}

			if len(cfg.ResponseSet) == 0 && len(cfg.ResponseRemove) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			hw := &headerWriter{ResponseWriter: w, set: cfg.ResponseSet, del: cfg.ResponseRemove}
			next.ServeHTTP(hw, r)
		})
	}
}

// headerWriter applies response-header rules once, lazily, right before the
// handler's first WriteHeader or Write.
type headerWriter struct {
	http.ResponseWriter
	set       map[string]string
	del       []string
	applied   bool
	wroteHead bool
}

func (h *headerWriter) apply() {
	if h.applied {
		return
	}
	h.applied = true
	for _, k := range h.del {
		h.Header().Del(k)
	}
	for k, v := range h.set {
		h.Header().Set(k, v)
	}
}

func (h *headerWriter) WriteHeader(code int) {
	if !h.wroteHead {
		h.wroteHead = true
		h.apply()
	}
	h.ResponseWriter.WriteHeader(code)
}

func (h *headerWriter) Write(b []byte) (int, error) {
	if !h.wroteHead {
		h.wroteHead = true
		h.apply()
	}
	return h.ResponseWriter.Write(b)
}
