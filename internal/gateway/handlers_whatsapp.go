package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/sausheong/felix/internal/channel"
)

// WhatsAppHandlers exposes the HTTP endpoints for WhatsApp pairing.
type WhatsAppHandlers struct {
	Bridge PairingBridge
}

// NewWhatsAppHandlers wires the bridge into the HTTP layer.
func NewWhatsAppHandlers(b PairingBridge) *WhatsAppHandlers {
	return &WhatsAppHandlers{Bridge: b}
}

// Status returns the current WhatsApp connection status as JSON, along with
// the device JID and database path when known.
func (h *WhatsAppHandlers) Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.Bridge == nil {
		json.NewEncoder(w).Encode(map[string]string{"status": "not_configured"})
		return
	}
	resp := map[string]string{
		"status":  h.Bridge.WhatsAppStatus(),
		"jid":     h.Bridge.WhatsAppJID(),
		"db_path": h.Bridge.WhatsAppDBPath(),
	}
	json.NewEncoder(w).Encode(resp)
}

// Disconnect unpairs the WhatsApp device.
func (h *WhatsAppHandlers) Disconnect(w http.ResponseWriter, r *http.Request) {
	if h.Bridge == nil {
		http.Error(w, "pairing not available", http.StatusNotFound)
		return
	}
	if err := h.Bridge.DisconnectWhatsApp(r.Context()); err != nil {
		slog.Error("whatsapp disconnect failed", "error", err)
		http.Error(w, "disconnect failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Pair starts a pairing session and streams QR events as SSE. For each
// "code" event the QR string is rendered server-side as a base64 PNG and
// pushed to the browser, which can render it as <img src="data:image/png;base64,...">.
func (h *WhatsAppHandlers) Pair(w http.ResponseWriter, r *http.Request) {
	if h.Bridge == nil {
		http.Error(w, "pairing not available", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	events := make(chan channel.WhatsAppQREvent, 16)
	if err := h.Bridge.StartWhatsAppPairing(r.Context(), func(evt channel.WhatsAppQREvent) {
		select {
		case events <- evt:
		default:
		}
	}); err != nil {
		data, _ := json.Marshal(map[string]string{"message": err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case evt := <-events:
			switch evt.Type {
			case "code":
				png, err := qrcode.Encode(evt.Code, qrcode.Medium, 256)
				if err != nil {
					slog.Warn("qr encode failed", "error", err)
					continue
				}
				payload := map[string]string{
					"code":    evt.Code,
					"png_b64": base64.StdEncoding.EncodeToString(png),
				}
				data, _ := json.Marshal(payload)
				fmt.Fprintf(w, "event: qr\ndata: %s\n\n", data)
			case "login":
				fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
			case "timeout", "error":
				msg := evt.Err
				if msg == "" {
					msg = "Pairing failed"
				}
				data, _ := json.Marshal(map[string]string{"message": msg})
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			}
			flusher.Flush()
			if evt.Type == "login" || evt.Type == "timeout" || evt.Type == "error" {
				return
			}
		}
	}
}
