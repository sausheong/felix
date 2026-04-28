package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/mcp"
)

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP server connections",
	}
	cmd.AddCommand(mcpLoginCmd())
	return cmd
}

func mcpLoginCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "login <server-id>",
		Short: "Run interactive OAuth login for an MCP server (auth-code + PKCE)",
		Long: `login performs the OAuth 2.0 Authorization Code + PKCE flow against
the configured MCP server's IdP. Felix opens your browser, listens on the
redirect URI's loopback port for the callback, exchanges the code for a
token, and writes the token to <data-dir>/mcp-tokens/<server-id>.json.

Use this to pre-seed a token before starting the gateway in a daemon
context, or to re-authenticate after a refresh token expires. The gateway
must be restarted to pick up a freshly written token (config watcher only
watches felix.json5).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMcpLogin(cmd.Context(), configPath, args[0], cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to config file (default ~/.felix/felix.json5)")
	return cmd
}

// runMcpLogin loads the config, finds the named MCP server, validates that
// it's an oauth2_authorization_code entry, runs the interactive PKCE
// flow, and reports the cache file path on success. Exposed (lower-case
// but called by the test in mcp_cmd_test.go) so we can drive it without
// going through cobra.
func runMcpLogin(ctx context.Context, configPath, serverID string, out io.Writer) error {
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config %s: %w", configPath, err)
	}

	// Reuse the same resolution path the gateway uses, so a successful
	// login here implies the gateway will accept the same config block.
	resolved, err := cfg.ResolveMCPServers()
	if err != nil {
		return fmt.Errorf("resolve mcp servers: %w", err)
	}

	var entry *mcp.ManagerServerConfig
	for i := range resolved {
		if resolved[i].ID == serverID {
			entry = &resolved[i]
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("mcp server %q not found in %s (or it's disabled, or its secret env var isn't set)", serverID, configPath)
	}
	if entry.Transport != "http" || entry.HTTP == nil {
		return fmt.Errorf("mcp server %q is not an HTTP server (transport=%q)", serverID, entry.Transport)
	}
	auth := entry.HTTP.Auth
	if auth.Kind != "oauth2_authorization_code" {
		return fmt.Errorf("mcp server %q uses auth.kind=%q; login is only meaningful for oauth2_authorization_code", serverID, auth.Kind)
	}

	tok, err := mcp.RunInteractiveLogin(ctx, mcp.AuthCodePKCEConfig{
		AuthURL:      auth.AuthURL,
		TokenURL:     auth.TokenURL,
		ClientID:     auth.ClientID,
		ClientSecret: auth.ClientSecret,
		Scope:        auth.Scope,
		RedirectURI:  auth.RedirectURI,
		StorePath:    auth.TokenStorePath,
	})
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	fmt.Fprintf(out, "Logged in to %s.\nToken cached at %s.\n", serverID, auth.TokenStorePath)
	if tok.RefreshToken == "" {
		// Worth surfacing — without a refresh token the user will have
		// to re-run `mcp login` whenever the access token expires.
		fmt.Fprintf(out, "WARNING: IdP did not return a refresh token. Add `offline_access` to scope (or remove auth.scope to use the default) for silent refresh.\n")
	}
	if !tok.Expiry.IsZero() {
		fmt.Fprintf(out, "Access token expires at %s.\n", tok.Expiry.Format("2006-01-02 15:04:05 MST"))
	}
	fmt.Fprintln(out, "Restart `felix start` to pick up the new token.")
	return nil
}

// Compile-time guard that we don't accidentally rely on os.Stdout in a
// way that would break tests passing a custom io.Writer.
var _ = os.Stdout
