package mcp

// ManagerServerConfig is the resolved-secret shape Manager consumes. The
// caller (typically Config.ResolveMCPServers) is responsible for pulling
// the client secret out of its source (env var, file, etc.) before calling
// NewManager.
//
// Defined here so internal/config can return this type from
// ResolveMCPServers without depending on Manager itself (which lands in
// the next task).
type ManagerServerConfig struct {
	ID           string
	URL          string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	ToolPrefix   string
}
