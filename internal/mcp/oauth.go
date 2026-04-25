package mcp

import (
	"context"
	"net/http"

	"golang.org/x/oauth2/clientcredentials"
)

// ClientCredentialsConfig is the minimal config needed to perform an OAuth2
// client-credentials grant against a token endpoint (e.g. AWS Cognito).
type ClientCredentialsConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string // single scope; multi-scope can be space-separated
}

// NewClientCredentialsHTTPClient returns an *http.Client whose RoundTripper
// injects an OAuth2 bearer token into every outgoing request and refreshes
// the token automatically before it expires.
//
// The returned client is safe for concurrent use. It uses the background
// context for token fetches, which means token refreshes are not cancelled
// when an individual request is cancelled — that is the desired behavior
// for a long-lived session.
func NewClientCredentialsHTTPClient(cfg ClientCredentialsConfig) *http.Client {
	cc := &clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL,
	}
	if cfg.Scope != "" {
		cc.Scopes = []string{cfg.Scope}
	}
	return cc.Client(context.Background())
}
