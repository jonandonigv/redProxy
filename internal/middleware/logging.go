package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"redproxy/internal/router"
)

// Logging returns a middleware that writes one structured log line per
// request: method, path, status, duration, and the matched route name
// (when the request came through a router.Router).
func Logging(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}

			next.ServeHTTP(rec, r)

			logger.Info("request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.Status),
				slog.Duration("duration", time.Since(start)),
				slog.String("route", router.RouteName(r.Context())),
			)
		})
	}
}

// StatusRecorder captures the status code a handler wrote. Handlers that
// never call WriteHeader implicitly produce 200. It is exported so other
// packages (e.g. metrics) can reuse it.
type StatusRecorder struct {
	http.ResponseWriter
	Status    int
	wroteHead bool
}

func (r *StatusRecorder) WriteHeader(code int) {
	if !r.wroteHead {
		r.Status = code
		r.wroteHead = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *StatusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHead {
		r.Status = http.StatusOK
		r.wroteHead = true
	}
	return r.ResponseWriter.Write(b)
}
