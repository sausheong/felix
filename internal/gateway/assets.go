package gateway

import (
	_ "embed"
	"net/http"
)

//go:embed assets/felix-logo.png
var felixLogoPNG []byte

// FaviconHandler serves the embedded Felix logo as image/png with a long
// Cache-Control. Mount at /favicon.png and /favicon.ico — browsers tolerate
// PNG bytes served at the .ico path, and serving real bytes (rather than a
// 404) keeps the browser from showing the generic globe icon.
func FaviconHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		_, _ = w.Write(felixLogoPNG)
	})
}
