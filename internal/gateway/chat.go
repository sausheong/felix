package gateway

import (
	_ "embed"
	"net/http"
)

// chatHTML is the single-page chat web interface, embedded as a static asset.
// It contains no server-side template substitution — the page derives its
// WebSocket endpoint from the browser's own origin — so it is served verbatim.
//
//go:embed assets/chat.html
var chatHTML []byte

// NewChatHandler returns an HTTP handler func that serves the chat web
// interface. The port parameter is retained for call-site stability; the page
// is fully static and uses same-origin WebSocket connections.
func NewChatHandler(port int) http.HandlerFunc {
	_ = port
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:")
		_, _ = w.Write(chatHTML)
	}
}
