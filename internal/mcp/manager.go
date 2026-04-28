package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// ServerEntry is a connected MCP server known to the Manager. Exposed so
// callers (the Tool registration code) can iterate without reaching into
// Manager internals.
type ServerEntry struct {
	ID         string
	Client     *Client
	ToolPrefix string
}

// Manager owns a Client per enabled MCP server. Servers that fail to
// connect at startup are logged and skipped — Manager construction still
// succeeds so the rest of the gateway can start.
type Manager struct {
	servers []*ServerEntry
}

// NewManager opens a session against each ManagerServerConfig in cfgs.
// Dispatches on cfg.Transport: "http" uses ConnectHTTP with an auth-aware
// *http.Client, "stdio" uses ConnectStdio with a spawned subprocess. An
// unknown transport (or a per-server connect failure) is logged and the
// entry skipped — Manager construction never fails.
func NewManager(ctx context.Context, cfgs []ManagerServerConfig) (*Manager, error) {
	m := &Manager{}
	for _, cfg := range cfgs {
		client, err := connectOne(ctx, cfg)
		if err != nil {
			slog.Warn("mcp: failed to connect to server, skipping",
				"id", cfg.ID, "transport", cfg.Transport, "error", err)
			continue
		}
		m.servers = append(m.servers, &ServerEntry{
			ID:         cfg.ID,
			Client:     client,
			ToolPrefix: cfg.ToolPrefix,
		})
		slog.Info("mcp: connected to server", "id", cfg.ID, "transport", cfg.Transport)
	}
	return m, nil
}

func connectOne(ctx context.Context, cfg ManagerServerConfig) (*Client, error) {
	switch cfg.Transport {
	case "http", "":
		if cfg.HTTP == nil {
			return nil, fmt.Errorf("http transport requires HTTP block")
		}
		httpClient, err := buildHTTPClient(ctx, cfg.HTTP.Auth)
		if err != nil {
			return nil, fmt.Errorf("build http client: %w", err)
		}
		return ConnectHTTP(ctx, cfg.HTTP.URL, httpClient)
	case "stdio":
		if cfg.Stdio == nil {
			return nil, fmt.Errorf("stdio transport requires Stdio block")
		}
		return ConnectStdio(ctx, cfg.ID, cfg.Stdio.Command, cfg.Stdio.Args, cfg.Stdio.Env)
	default:
		return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
	}
}

func buildHTTPClient(ctx context.Context, auth HTTPAuthConfig) (*http.Client, error) {
	switch auth.Kind {
	case "oauth2_client_credentials":
		return NewClientCredentialsHTTPClient(ClientCredentialsConfig{
			TokenURL:     auth.TokenURL,
			ClientID:     auth.ClientID,
			ClientSecret: auth.ClientSecret,
			Scope:        auth.Scope,
		}), nil
	case "oauth2_authorization_code":
		client, err := NewAuthCodePKCEHTTPClient(ctx, AuthCodePKCEConfig{
			AuthURL:      auth.AuthURL,
			TokenURL:     auth.TokenURL,
			ClientID:     auth.ClientID,
			ClientSecret: auth.ClientSecret,
			Scope:        auth.Scope,
			RedirectURI:  auth.RedirectURI,
			StorePath:    auth.TokenStorePath,
		})
		if errors.Is(err, ErrInteractiveLoginRequired) {
			// Daemon-friendly: the gateway shouldn't pop a browser at
			// startup if no token is cached. Tell the user how to get
			// one and skip this server.
			return nil, fmt.Errorf("no cached token at %s — run `felix mcp login <id>` first", auth.TokenStorePath)
		}
		return client, err
	case "bearer":
		return NewBearerHTTPClient(auth.BearerToken), nil
	case "none", "":
		return http.DefaultClient, nil
	default:
		return nil, fmt.Errorf("unsupported http auth kind %q", auth.Kind)
	}
}

// Servers returns the connected server entries.
func (m *Manager) Servers() []*ServerEntry {
	if m == nil {
		return nil
	}
	return m.servers
}

// Close terminates every server session. Errors are aggregated into a single
// returned error (joined with newlines) but Close always attempts every server.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var combined string
	for _, s := range m.servers {
		if err := s.Client.Close(); err != nil {
			if combined != "" {
				combined += "\n"
			}
			combined += fmt.Sprintf("close %s: %v", s.ID, err)
		}
	}
	if combined != "" {
		return fmt.Errorf("%s", combined)
	}
	return nil
}
