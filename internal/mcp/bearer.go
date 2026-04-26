package mcp

import "net/http"

// NewBearerHTTPClient returns an *http.Client whose RoundTripper attaches an
// Authorization: Bearer <token> header to every outgoing request, unless the
// caller has already set Authorization. Useful for HTTP MCP servers that
// authenticate via a static token (Anthropic-hosted, Vercel-hosted, custom
// internal endpoints).
//
// An empty token disables injection — the returned client behaves exactly
// like http.DefaultClient. The resolver is expected to skip empty-token
// servers before reaching this point; this defensive no-op exists so a
// programming error doesn't crash the gateway.
func NewBearerHTTPClient(token string) *http.Client {
	return &http.Client{
		Transport: &bearerRoundTripper{token: token, base: http.DefaultTransport},
	}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.token != "" && req.Header.Get("Authorization") == "" {
		// Clone before mutating — the http package contract requires
		// RoundTrippers not to modify the inbound request.
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(req)
}
