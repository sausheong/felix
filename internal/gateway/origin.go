package gateway

import (
	"net/http"
	"net/url"
	"strings"
)

// originAllowed reports whether origin is permitted given the configured
// allowlist. An empty allowlist means localhost-only: the origin must be an
// http(s) URL whose host is exactly 127.0.0.1 or localhost (any port). This
// matches the intent of the legacy http(s)://{127.0.0.1,localhost} prefix
// check but uses exact host comparison so look-alike hosts such as
// 127.0.0.1.evil.com are rejected. A non-empty allowlist requires an exact
// match (trailing slash tolerated). This is the single source of truth shared
// by the WebSocket origin check (AllowedOrigins) and the HTTP RequireSameOrigin
// middleware.
func originAllowed(origin string, allowed []string) bool {
	if len(allowed) == 0 {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return false
		}
		host := u.Hostname()
		return host == "127.0.0.1" || host == "localhost"
	}
	want := strings.TrimRight(origin, "/")
	for _, a := range allowed {
		if strings.TrimRight(a, "/") == want {
			return true
		}
	}
	return false
}

// RequireSameOrigin returns middleware that rejects cross-site requests.
// It is mounted only on routes that must be checked (all mutating routes
// plus the sensitive /logs* GETs), so it deliberately has NO safe-method
// short-circuit: every request routed through it is checked.
//
// Decision order: Sec-Fetch-Site (browser-set, unspoofable) is authoritative
// when present; otherwise fall back to Origin. A request with neither header
// (a local non-browser client such as curl) is trusted — a localhost bind is
// not defended against local processes by design (see the spec threat model).
func RequireSameOrigin(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Sec-Fetch-Site") {
			case "same-origin", "same-site", "none":
				next.ServeHTTP(w, r)
				return
			case "cross-site", "cross-origin":
				forbidCrossOrigin(w)
				return
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if originAllowed(origin, allowed) {
				next.ServeHTTP(w, r)
				return
			}
			forbidCrossOrigin(w)
		})
	}
}

func forbidCrossOrigin(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"cross-origin request blocked"}`))
}
