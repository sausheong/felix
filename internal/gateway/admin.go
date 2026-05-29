package gateway

import (
	"log/slog"
	"net/http"
	"os"
	"time"
)

// NewRestartHandler returns an HTTP handler that initiates a clean
// process exit. Intended for use behind any auth gate so authorized
// users can trigger a gateway restart from the chat UI.
//
// Felix supervision: when run under felix-app (the menubar wrapper),
// the supervisor respawns the gateway on clean exit with backoff (see
// cmd/felix-app). When run as plain `felix start` from a terminal,
// the process simply exits and the user must re-launch — there is no
// way around this without an external supervisor.
//
// Behavior: replies 202 Accepted immediately, flushes so the response
// reaches the client, then sleeps ~1 second (giving the WebSocket
// write buffer time to drain on the same connection) before calling
// os.Exit(0).
//
// exit is wired to os.Exit by default; tests override it.
func NewRestartHandler() http.HandlerFunc {
	return newRestartHandlerWith(os.Exit, 1*time.Second)
}

func newRestartHandlerWith(exit func(int), delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Warn("restart requested via /admin/restart", "remote", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"restarting"}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(delay)
			slog.Info("exiting for restart")
			exit(0)
		}()
	}
}
