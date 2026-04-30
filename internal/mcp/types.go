package mcp

// ManagerServerConfig is the resolved-secret, transport-discriminated shape
// Manager consumes. The caller (typically Config.ResolveMCPServers) picks
// the transport, resolves secrets from their config-or-env source, and
// populates the matching transport-specific block (HTTP or Stdio).
//
// Defined here so internal/config can return this type from
// ResolveMCPServers without depending on Manager itself.
type ManagerServerConfig struct {
	ID           string
	ToolPrefix   string
	Transport    string             // "http" | "stdio"
	HTTP         *HTTPServerConfig  // populated when Transport == "http"
	Stdio        *StdioServerConfig // populated when Transport == "stdio"
	// ParallelSafe is vestigial here as of the live-read refactor —
	// IsConcurrencySafe now reads the live config via the ParallelSafeFn
	// closure passed to RegisterTools. Still set by ResolveMCPServers and
	// mirrored into ServerEntry for API stability.
	ParallelSafe bool
}

// HTTPServerConfig describes an HTTP-transport MCP server, including which
// auth scheme to use against it.
type HTTPServerConfig struct {
	URL  string
	Auth HTTPAuthConfig
}

// HTTPAuthConfig discriminates on Kind. Only the fields relevant to the
// chosen Kind need be populated; Manager dispatches on Kind to build the
// right *http.Client.
type HTTPAuthConfig struct {
	Kind string // "oauth2_client_credentials" | "oauth2_authorization_code" | "bearer" | "none"

	// oauth2_client_credentials, oauth2_authorization_code
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string

	// oauth2_authorization_code
	AuthURL        string // IdP authorize endpoint
	RedirectURI    string // must be loopback per RFC 8252
	TokenStorePath string // absolute path to per-server token cache file

	// bearer
	BearerToken string
}

// StdioServerConfig describes a stdio-transport MCP server. The configured
// Env map is merged onto os.Environ() at spawn time so the child inherits
// PATH and other parent env vars unless explicitly overridden.
type StdioServerConfig struct {
	Command string
	Args    []string
	Env     map[string]string
}
