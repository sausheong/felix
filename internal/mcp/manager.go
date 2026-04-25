package mcp

import (
	"context"
	"fmt"
	"log/slog"
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
// Returns an error only on construction failures the caller can do nothing
// about (currently: none — every per-server failure is non-fatal).
func NewManager(ctx context.Context, cfgs []ManagerServerConfig) (*Manager, error) {
	m := &Manager{}
	for _, cfg := range cfgs {
		httpClient := NewClientCredentialsHTTPClient(ClientCredentialsConfig{
			TokenURL:     cfg.TokenURL,
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Scope:        cfg.Scope,
		})
		client, err := Connect(ctx, cfg.URL, httpClient)
		if err != nil {
			slog.Warn("mcp: failed to connect to server, skipping",
				"id", cfg.ID, "url", cfg.URL, "error", err)
			continue
		}
		m.servers = append(m.servers, &ServerEntry{
			ID:         cfg.ID,
			Client:     client,
			ToolPrefix: cfg.ToolPrefix,
		})
		slog.Info("mcp: connected to server", "id", cfg.ID, "url", cfg.URL)
	}
	return m, nil
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
