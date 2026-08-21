package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	maxClients    = 65536            // cap on tracked client IPs
	clientIdleTTL = 10 * time.Minute // idle clients are evicted lazily
)

// RateLimit returns a middleware enforcing a per-client-IP token bucket
// (rps refill, burst size). Exceeding it answers 429. Client state is
// evicted lazily once the map grows past maxClients.
func RateLimit(rps float64, burst int) Middleware {
	var mu sync.Mutex
	clients := make(map[string]*clientEntry, 128)

	getLimiter := func(ip string, now time.Time) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()

		if e, ok := clients[ip]; ok {
			e.lastSeen = now
			return e.limiter
		}
		if len(clients) >= maxClients {
			for k, e := range clients { // lazy eviction: drop idle entries
				if now.Sub(e.lastSeen) > clientIdleTTL {
					delete(clients, k)
				}
			}
		}
		e := &clientEntry{limiter: rate.NewLimiter(rate.Limit(rps), burst), lastSeen: now}
		clients[ip] = e
		return e.limiter
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !getLimiter(clientIP(r), time.Now()).Allow() {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type clientEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// clientIP extracts the host part of RemoteAddr (the direct peer).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
