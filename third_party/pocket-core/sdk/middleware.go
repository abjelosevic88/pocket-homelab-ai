package sdk

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey int

const introspectionKey ctxKey = 1

// FromContext returns the introspection stored by Middleware, if any.
func FromContext(ctx context.Context) *Introspection {
	v, _ := ctx.Value(introspectionKey).(*Introspection)
	return v
}

// Middleware validates Core-issued bearer tokens and stores the verdict in
// the request context; other requests pass through untouched so the app's
// own sessions and tokens keep working. Requests with an invalid Core token
// get 401.
func (c *Client) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
			tok = strings.TrimSpace(h[7:])
		} else {
			tok = strings.TrimSpace(r.Header.Get("X-API-Key"))
		}
		if tok == "" || !IsCoreToken(tok) || !c.Configured() {
			next.ServeHTTP(w, r)
			return
		}
		in, err := c.Introspect(r.Context(), tok)
		if err != nil || !in.Active {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"core token rejected"}`))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), introspectionKey, in)))
	})
}
