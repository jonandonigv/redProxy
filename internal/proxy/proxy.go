// Package proxy turns a load-balanced backend pool into an http.Handler
// built on net/http/httputil.ReverseProxy.
//
// The backend is selected in ServeHTTP (not in the rewrite hook) so a
// fully-down pool can answer 503 directly; the pick travels to the rewrite
// and error hooks via the request context. Transport failures passively mark
// the backend unhealthy; the active health checker may restore it later.
package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"redproxy/internal/balancer"
)

const (
	dialTimeout         = 5 * time.Second
	dialKeepAlive       = 30 * time.Second
	tlsHandshakeTimeout = 5 * time.Second
	idleConnTimeout     = 90 * time.Second
	maxIdleConns        = 100
	maxIdleConnsPerHost = 32 // default is 2, far too low for a busy proxy
)

type ctxKey struct{}

// Proxy serves one route: it balances requests over a backend pool and
// forwards them with httputil.ReverseProxy.
type Proxy struct {
	pool *balancer.Pool
	rp   *httputil.ReverseProxy
}

// New creates a route handler for the pool. stripPrefix, when non-empty, is
// removed from the front of incoming paths before forwarding.
func New(pool *balancer.Pool, stripPrefix string) *Proxy {
	p := &Proxy{pool: pool}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			if b := backendFrom(pr.In.Context()); b != nil {
				if u, err := url.Parse(b.URL); err == nil {
					pr.SetURL(u)
				}
			}
			if stripPrefix != "" {
				pr.Out.URL.Path = applyStrip(pr.Out.URL.Path, stripPrefix)
			}
			pr.SetXForwarded()
		},
		Transport:     newTransport(),
		FlushInterval: -1, // flush immediately: streaming responses work
		ErrorHandler:  p.handleBackendError,
	}
	return p
}

// ServeHTTP picks a healthy backend and proxies the request to it. When no
// backend is available it answers 503 without touching ReverseProxy.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b := p.pool.Next()
	if b == nil {
		http.Error(w, "no healthy upstream", http.StatusServiceUnavailable)
		return
	}

	b.IncInflight()
	defer b.DecInflight()

	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, b)))
}

// handleBackendError marks the failing backend unhealthy (passive signal)
// and answers 502 to the client.
func (p *Proxy) handleBackendError(w http.ResponseWriter, r *http.Request, err error) {
	if b := backendFrom(r.Context()); b != nil {
		b.SetHealthy(false)
		slog.Warn("backend request failed",
			"backend", b.URL,
			"path", r.URL.Path,
			"error", err,
		)
	}
	http.Error(w, "bad gateway", http.StatusBadGateway)
}

func backendFrom(ctx context.Context) *balancer.Backend {
	b, _ := ctx.Value(ctxKey{}).(*balancer.Backend)
	return b
}

// applyStrip removes prefix from path; an empty remainder collapses to "/".
func applyStrip(path, prefix string) string {
	if rest := strings.TrimPrefix(path, prefix); rest != "" {
		return rest
	}
	return "/"
}

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: dialKeepAlive}).DialContext,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
	}
}
