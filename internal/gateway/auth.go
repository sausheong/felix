package gateway

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuthMiddleware returns middleware that validates a Bearer token.
// If token is empty, the middleware is a no-op (no auth required).
func BearerAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next // no auth configured
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allow health check without auth
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			// Check Authorization header
			auth := r.Header.Get("Authorization")
			if auth == "" {
				// Also check query parameter for WebSocket clients that
				// can't set custom headers
				auth = "Bearer " + r.URL.Query().Get("token")
			}

			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			providedToken := strings.TrimPrefix(auth, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(providedToken), []byte(token)) != 1 {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// AllowedOrigins returns a WebSocket CheckOrigin function that validates the
// request origin. An empty origins slice means localhost-only. A request with
// no Origin header (CLI tools, curl) is allowed. Delegates to originAllowed so
// the WebSocket and HTTP RequireSameOrigin share one definition.
func AllowedOrigins(origins []string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // no origin header (e.g. CLI tools, curl)
		}
		return originAllowed(origin, origins)
	}
}
