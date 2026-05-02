package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

// MaxConsecutiveAuthFailures is the per-server circuit-breaker threshold.
// After this many consecutive tool calls fail with auth-shaped errors
// that even an automatic Reconnect+retry couldn't fix, the adapter
// short-circuits subsequent calls without touching the network — the
// agent gets a strongly-worded "stop calling this server" error so it
// doesn't burn LLM tokens looping on a server that's persistently broken.
//
// The breaker resets on (a) a successful tool call (RecordSuccess) and
// (b) a user-initiated reconnect via Manager.ReconnectServer (the chat
// UI's "Re-authenticate" button), since explicit user action is a fresh
// signal that the operator believes the server should now be reachable.
const MaxConsecutiveAuthFailures = 3

// ServerEntry is a connected MCP server known to the Manager. The live
// *Client is held under mu so it can be swapped atomically by Reconnect
// without invalidating any adapter that's currently mid-call. Adapters
// hold *ServerEntry (not *Client) and read entry.Live() per call so the
// next call after a successful Reconnect picks up the new client.
type ServerEntry struct {
	ID         string
	ToolPrefix string
	// ParallelSafe mirrors ManagerServerConfig.ParallelSafe at
	// construction time. The MCP tool adapter no longer consults it for
	// IsConcurrencySafe — it reads the live config via ParallelSafeFn —
	// but it stays on the struct for API stability.
	ParallelSafe bool

	mu     sync.RWMutex
	client *Client
	cfg    ManagerServerConfig // retained for Reconnect to re-run connectOne

	failMu              sync.Mutex
	consecutiveFailures int // guarded by failMu; see MaxConsecutiveAuthFailures
}

// RecordSuccess clears the consecutive-failure counter. Called by the
// adapter after a successful tool call (initial OR after a successful
// auto Reconnect+retry) — the server is responsive, so any prior streak
// is now stale.
func (e *ServerEntry) RecordSuccess() {
	e.failMu.Lock()
	e.consecutiveFailures = 0
	e.failMu.Unlock()
}

// RecordFailure increments the consecutive-failure counter and returns
// the new value. Called by the adapter when an auth-shaped error
// persists even after an attempted Reconnect+retry.
func (e *ServerEntry) RecordFailure() int {
	e.failMu.Lock()
	e.consecutiveFailures++
	n := e.consecutiveFailures
	e.failMu.Unlock()
	return n
}

// FailureCount returns the current consecutive-failure count. Used by
// the adapter's pre-flight check to decide whether to short-circuit.
func (e *ServerEntry) FailureCount() int {
	e.failMu.Lock()
	defer e.failMu.Unlock()
	return e.consecutiveFailures
}

// resetFailures clears the breaker. Called by Manager.ReconnectServer
// (the user-initiated Re-authenticate path) so explicit user action
// always gives the server a fresh chance.
//
// Note: not called by Reconnect itself — that path is auto-triggered
// from Execute and shouldn't reset its own breaker (it would defeat
// the whole point of counting auto-recovery failures).
func (e *ServerEntry) resetFailures() {
	e.failMu.Lock()
	e.consecutiveFailures = 0
	e.failMu.Unlock()
}

// Live returns the current *Client. Adapters call this on every tool
// invocation so a Reconnect-driven swap is observed on the next call.
func (e *ServerEntry) Live() *Client {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.client
}

// Reconnect closes the existing client and opens a new one against the
// same config. Any in-flight tool call that already grabbed the old
// client via Live() finishes against the old session; subsequent calls
// observe the new one. Errors from connectOne propagate; on success the
// old client is closed in the background so the swap is observably
// instant to callers.
func (e *ServerEntry) Reconnect(ctx context.Context) error {
	newClient, err := connectOne(ctx, e.cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	old := e.client
	e.client = newClient
	e.mu.Unlock()
	if old != nil {
		go func() {
			if cerr := old.Close(); cerr != nil {
				slog.Debug("mcp: close old client after reconnect", "id", e.ID, "error", cerr)
			}
		}()
	}
	return nil
}

// Client is the legacy accessor. Kept for external callers that still
// expect a struct field; reads through Live() so they observe swaps.
// Deprecated: prefer Live().
func (e *ServerEntry) GetClient() *Client { return e.Live() }

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
			ID:           cfg.ID,
			ToolPrefix:   cfg.ToolPrefix,
			ParallelSafe: cfg.ParallelSafe,
			client:       client,
			cfg:          cfg,
		})
		slog.Info("mcp: connected to server", "id", cfg.ID, "transport", cfg.Transport)
	}
	return m, nil
}

// ReconnectServer finds the entry with the given ID and runs Reconnect
// on it. Returns an error if the server isn't known. Used by the HTTP
// re-auth endpoint after a successful interactive login refreshes the
// token store. Also resets the per-server consecutive-failure breaker
// — the user clicking Re-authenticate is an explicit signal that the
// server should now be reachable, so any prior streak is stale.
func (m *Manager) ReconnectServer(ctx context.Context, id string) error {
	if m == nil {
		return fmt.Errorf("mcp: manager not initialized")
	}
	for _, s := range m.servers {
		if s.ID == id {
			if err := s.Reconnect(ctx); err != nil {
				return err
			}
			s.resetFailures()
			return nil
		}
	}
	return fmt.Errorf("mcp: server %q not found", id)
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
		if err := s.Live().Close(); err != nil {
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
