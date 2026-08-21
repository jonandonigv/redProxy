// Package middleware provides redProxy's per-route middleware chain:
// structured request logging, header manipulation, and per-client-IP rate
// limiting. Middlewares follow the classic func(http.Handler) http.Handler
// pattern and compose via Chain.
package middleware

import "net/http"

// Middleware wraps a handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// Chain composes middlewares so that Chain(a, b)(h) runs a outermost.
func Chain(mws ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			next = mws[i](next)
		}
		return next
	}
}
