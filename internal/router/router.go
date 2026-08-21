// Package router matches incoming requests to routes by Host (optional) and
// longest path prefix, then dispatches to the route's handler. Requests that
// match no route get a 404.
//
// The matched route's name is stored in the request context so downstream
// middleware (logging, metrics) can label work per route.
package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Route is one routing entry: an optional host filter plus a required path
// prefix, served by Handler when matched.
type Route struct {
	Name       string
	Host       string // optional; empty matches any host
	PathPrefix string // required; must start with "/"
	Handler    http.Handler
}

type ctxKey struct{}

// Router dispatches requests to the best-matching route.
type Router struct {
	routes []Route
}

// New creates a Router, failing fast on invalid route definitions.
func New(routes []Route) (*Router, error) {
	for _, rt := range routes {
		if rt.PathPrefix == "" {
			return nil, fmt.Errorf("route %q: path_prefix is required", rt.Name)
		}
		if !strings.HasPrefix(rt.PathPrefix, "/") {
			return nil, fmt.Errorf("route %q: path_prefix must start with \"/\"", rt.Name)
		}
		if rt.Handler == nil {
			return nil, fmt.Errorf("route %q: handler is required", rt.Name)
		}
	}

	stored := make([]Route, len(routes))
	for i, rt := range routes {
		rt.Host = normalizeHost(rt.Host) // precompute: match compares on every request
		stored[i] = rt
	}
	return &Router{routes: stored}, nil
}

// Match returns the route serving host+path, or nil when nothing matches.
// Longest path prefix wins; among equal prefixes the first defined wins.
func (r *Router) Match(host, path string) *Route {
	h := normalizeHost(host)

	var best *Route
	for i := range r.routes {
		rt := &r.routes[i]
		if rt.Host != "" && rt.Host != h {
			continue
		}
		if !strings.HasPrefix(path, rt.PathPrefix) {
			continue
		}
		if best == nil || len(rt.PathPrefix) > len(best.PathPrefix) {
			best = rt
		}
	}
	return best
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rt := r.Match(req.Host, req.URL.Path)
	if rt == nil {
		http.NotFound(w, req)
		return
	}
	ctx := context.WithValue(req.Context(), ctxKey{}, rt.Name)
	rt.Handler.ServeHTTP(w, req.WithContext(ctx))
}

// RouteName returns the name of the route a request was dispatched to, as
// stored by Router.ServeHTTP. It returns "" outside of routed requests.
func RouteName(ctx context.Context) string {
	name, _ := ctx.Value(ctxKey{}).(string)
	return name
}

// normalizeHost lowercases and strips the port ("A.example.com:8080" and
// "a.example.com" are the same host).
func normalizeHost(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(h)
}
