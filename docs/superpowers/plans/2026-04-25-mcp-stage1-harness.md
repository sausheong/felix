# MCP Stage 1: Smoke-Test Harness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `felix gt-harness`, a CLI subcommand that proves Felix can OAuth-authenticate to an AWS Bedrock AgentCore MCP gateway, list its tools, and invoke one — by reading credentials from a local dotenv-style file.

**Architecture:** Three small, single-purpose files in a new `internal/mcp/` package (`creds.go` parses the env file, `oauth.go` builds an OAuth2 client-credentials `*http.Client`, `client.go` wraps the official MCP Go SDK Streamable-HTTP transport) plus a thin cobra glue command in `cmd/felix/gtharness.go`. No agent runtime, no config integration, no hot reload — that's Stage 2.

**Tech Stack:** Go 1.25, cobra (existing), `golang.org/x/oauth2/clientcredentials` (new transitive→explicit), `github.com/modelcontextprotocol/go-sdk/mcp` (new), `testify` (existing).

**Spec:** [`docs/superpowers/specs/2026-04-25-mcp-integration-design.md`](../specs/2026-04-25-mcp-integration-design.md)

---

## File Structure

**Created:**
- `internal/mcp/creds.go` — KEY=VALUE file parser
- `internal/mcp/creds_test.go`
- `internal/mcp/oauth.go` — OAuth2 client-credentials HTTP client
- `internal/mcp/oauth_test.go`
- `internal/mcp/client.go` — MCP client wrapper (Streamable HTTP)
- `internal/mcp/client_test.go`
- `cmd/felix/gtharness.go` — cobra subcommand

**Modified:**
- `cmd/felix/main.go` — register `gtHarnessCmd()` in `rootCmd.AddCommand(...)`
- `.gitignore` — add `gt-harness.txt` and `*.env`
- `go.mod` / `go.sum` — new dependencies

---

## Task 1: Add dependencies and verify MCP SDK API shape

**Files:**
- Modify: `go.mod`, `go.sum`

The official MCP Go SDK has been actively stabilizing. Before writing code that calls into it, pin a version and confirm the exact symbol names. This task produces a tiny throwaway `cmd/felix/_mcpcheck/main.go` that compiles against the real SDK — proving the symbols exist before later tasks reference them.

- [ ] **Step 1: Pin dependencies**

```bash
go get github.com/modelcontextprotocol/go-sdk/mcp@latest
go get golang.org/x/oauth2@latest
go mod tidy
```

Expected: both modules added to `go.mod`. Note the resolved version of `github.com/modelcontextprotocol/go-sdk` — record it in your commit message so future readers know which API surface this plan was written against.

- [ ] **Step 2: Verify Streamable-HTTP client constructor exists**

Open the SDK godoc locally:
```bash
go doc github.com/modelcontextprotocol/go-sdk/mcp | head -80
go doc github.com/modelcontextprotocol/go-sdk/mcp Client
go doc github.com/modelcontextprotocol/go-sdk/mcp StreamableClientTransport
```

You are looking for three things:
1. A constructor that returns a `*Client` (commonly `NewClient`).
2. A Streamable-HTTP transport constructor (commonly `NewStreamableClientTransport(baseURL string, opts *StreamableClientTransportOptions) *StreamableClientTransport`) whose options struct accepts a custom `*http.Client`.
3. Methods on the connected session for `ListTools(ctx, *ListToolsParams) (*ListToolsResult, error)` and `CallTool(ctx, *CallToolParams) (*CallToolResult, error)`.

Write the **exact** symbol names you found into a scratch file `internal/mcp/SDK_NOTES.md` (untracked — add to .gitignore in Task 7). Later tasks reference these symbols. If any symbol differs from what is referenced in later tasks below, update the later tasks before writing the code.

- [ ] **Step 3: Sanity-compile against the SDK**

Create `cmd/felix/_mcpcheck/main.go`:

```go
package main

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// Just reference the package so the import resolves.
	fmt.Println(mcp.LATEST_PROTOCOL_VERSION)
}
```

(If `LATEST_PROTOCOL_VERSION` is not exported, substitute any other exported identifier from `go doc github.com/modelcontextprotocol/go-sdk/mcp`.)

Run:
```bash
go build ./cmd/felix/_mcpcheck
```

Expected: builds clean, prints a version string when run.

- [ ] **Step 4: Delete the scratch program**

```bash
rm -rf cmd/felix/_mcpcheck
```

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "deps(mcp): add modelcontextprotocol/go-sdk and x/oauth2

Pinned for the MCP smoke-test harness (Stage 1)."
```

---

## Task 2: Credentials file parser (`internal/mcp/creds.go`)

**Files:**
- Create: `internal/mcp/creds.go`
- Test: `internal/mcp/creds_test.go`

Minimal dotenv parser. No shell expansion, no quote handling. KEY=VALUE per line, blank lines and `#` comments ignored. Value is everything after the first `=`, with leading/trailing whitespace trimmed.

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/creds_test.go`:

```go
package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEnvFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.txt")
	require.NoError(t, os.WriteFile(path, []byte(
		"# comment line\n"+
			"\n"+
			"MCP_SERVER_URL=https://example.com/mcp\n"+
			"  LTM_CLIENT_ID=abc123  \n"+
			"LTM_CLIENT_SECRET=shhh=with=equals\n"+
			"LTM_TOKEN_URL=https://auth.example.com/token\n"+
			"LTM_SCOPE=foo/bar\n",
	), 0600))

	got, err := LoadEnvFile(path)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"MCP_SERVER_URL":    "https://example.com/mcp",
		"LTM_CLIENT_ID":     "abc123",
		"LTM_CLIENT_SECRET": "shhh=with=equals",
		"LTM_TOKEN_URL":     "https://auth.example.com/token",
		"LTM_SCOPE":         "foo/bar",
	}, got)
}

func TestLoadEnvFile_MissingFile(t *testing.T) {
	_, err := LoadEnvFile("/nonexistent/path/nope.txt")
	require.Error(t, err)
}

func TestLoadEnvFile_MalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.txt")
	require.NoError(t, os.WriteFile(path, []byte("this line has no equals\n"), 0600))

	_, err := LoadEnvFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 1")
}

func TestLoadEnvFile_EmptyKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.txt")
	require.NoError(t, os.WriteFile(path, []byte("=value\n"), 0600))

	_, err := LoadEnvFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty key")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/mcp/...
```

Expected: FAIL — `internal/mcp/creds.go` doesn't exist; build fails with `undefined: LoadEnvFile`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/mcp/creds.go`:

```go
// Package mcp provides Model Context Protocol client integration for Felix.
//
// Stage 1 (smoke harness) lives in this package today: credentials loading,
// OAuth2 client-credentials transport, and a thin wrapper over the official
// MCP Go SDK. Stage 2 will add a manager that registers MCP tools into the
// agent runtime.
package mcp

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnvFile reads a minimal dotenv-style file and returns its KEY=VALUE
// pairs as a map. Lines that are blank or start with '#' are skipped. Value
// is everything after the first '=', with surrounding whitespace trimmed.
// No shell expansion, no quoting rules.
func LoadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()

	out := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("env file %s: line %d: missing '='", path, lineNo)
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			return nil, fmt.Errorf("env file %s: line %d: empty key", path, lineNo)
		}
		val := strings.TrimSpace(line[eq+1:])
		out[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	return out, nil
}

// RequireKeys returns an error listing any missing keys from the supplied env
// map. Used by the harness to fail fast with a clear message.
func RequireKeys(env map[string]string, keys ...string) error {
	var missing []string
	for _, k := range keys {
		if _, ok := env[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env keys: %s", strings.Join(missing, ", "))
	}
	return nil
}
```

- [ ] **Step 4: Add a test for `RequireKeys` (still TDD — write the test, then re-run)**

Append to `internal/mcp/creds_test.go`:

```go
func TestRequireKeys(t *testing.T) {
	env := map[string]string{"A": "1", "B": "2"}
	require.NoError(t, RequireKeys(env, "A", "B"))

	err := RequireKeys(env, "A", "C", "D")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "C")
	assert.Contains(t, err.Error(), "D")
	assert.NotContains(t, err.Error(), "A")
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/mcp/...
```

Expected: PASS, all four tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/creds.go internal/mcp/creds_test.go
git commit -m "feat(mcp): add minimal env-file credentials parser"
```

---

## Task 3: OAuth2 client-credentials HTTP client (`internal/mcp/oauth.go`)

**Files:**
- Create: `internal/mcp/oauth.go`
- Test: `internal/mcp/oauth_test.go`

Wraps `golang.org/x/oauth2/clientcredentials.Config`. Returns an `*http.Client` whose transport injects `Authorization: Bearer …` into every request and refreshes the token before expiry.

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/oauth_test.go`:

```go
package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClientCredentialsHTTPClient_InjectsBearer(t *testing.T) {
	// Token server: accepts client_credentials grant, returns a fixed token.
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		assert.Equal(t, "client_credentials", form.Get("grant_type"))
		assert.Equal(t, "test-scope", form.Get("scope"))
		// Cognito accepts client creds via Basic auth or form fields. The oauth2
		// library tries Basic first; we accept either to keep the test flexible.
		if user, pass, ok := r.BasicAuth(); ok {
			assert.Equal(t, "id-x", user)
			assert.Equal(t, "secret-y", pass)
		} else {
			assert.Equal(t, "id-x", form.Get("client_id"))
			assert.Equal(t, "secret-y", form.Get("client_secret"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer tokenServer.Close()

	// Resource server: asserts the bearer token arrived.
	var seenAuth string
	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer resourceServer.Close()

	client := NewClientCredentialsHTTPClient(ClientCredentialsConfig{
		TokenURL:     tokenServer.URL,
		ClientID:     "id-x",
		ClientSecret: "secret-y",
		Scope:        "test-scope",
	})

	resp, err := client.Get(resourceServer.URL + "/whatever")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(seenAuth, "Bearer "), "expected Bearer header, got %q", seenAuth)
	assert.Equal(t, "Bearer tok-abc", seenAuth)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/mcp/...
```

Expected: FAIL — `undefined: NewClientCredentialsHTTPClient`, `undefined: ClientCredentialsConfig`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/mcp/oauth.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/oauth.go internal/mcp/oauth_test.go
git commit -m "feat(mcp): OAuth2 client-credentials HTTP client wrapper"
```

---

## Task 4: MCP client wrapper (`internal/mcp/client.go`)

**Files:**
- Create: `internal/mcp/client.go`
- Test: `internal/mcp/client_test.go`

Wraps the official MCP SDK's Streamable-HTTP transport. Exposes only the surface Stage 1 needs: `Connect`, `ListTools`, `CallTool`, `Close`. The wrapper keeps Felix code free of direct SDK type references — Stage 2 will lift this same wrapper.

> **Verification gate before coding:** confirm the SDK symbol names you recorded in `internal/mcp/SDK_NOTES.md` (Task 1, Step 2) match what this task uses below. Three places to check: the streamable transport constructor, the client constructor, and the call/list method signatures. If any name differs, fix this task's code before continuing.

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/client_test.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMCPServer is a hand-rolled HTTP handler that speaks just enough of the
// MCP Streamable-HTTP protocol for ListTools to succeed. It does NOT
// implement the full protocol — the goal is to verify our wrapper sends a
// request, parses a response, and surfaces errors. Real protocol coverage
// comes from the manual end-to-end run in Task 6.
func fakeMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		method, _ := req["method"].(string)
		id := req["id"]

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "0"},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "echo",
							"description": "Echo input",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"text": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			})
		default:
			// Treat anything else (notifications/initialized, etc.) as ack.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

func TestClient_ListTools(t *testing.T) {
	srv := fakeMCPServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	tools, err := c.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)
	assert.Equal(t, "Echo input", tools[0].Description)
	assert.Contains(t, string(tools[0].InputSchema), "text")
}

func TestClient_ConnectFails_OnBadURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := Connect(ctx, "http://127.0.0.1:1/definitely-closed", http.DefaultClient)
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "connect") ||
			strings.Contains(err.Error(), "refused") ||
			strings.Contains(err.Error(), "initialize"),
		"unexpected error: %v", err,
	)
}
```

> Note: the fake server is a sketch — the SDK's exact handshake may require additional methods (e.g., `notifications/initialized`). If the test fails not because the wrapper is missing but because the SDK demands more from the fake server, extend `fakeMCPServer` rather than gutting the test. The goal is to keep the wrapper exercised by Go tests, even if the protocol fidelity is partial. If extending the fake proves intractable, mark this test with `t.Skip("requires real MCP server; covered by Task 6")` and rely on Task 6's manual verification.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/mcp/... -run TestClient
```

Expected: FAIL — `undefined: Connect`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/mcp/client.go`. The exact SDK symbol names (marked `<<SDK>>` below) must match what you recorded in `SDK_NOTES.md`. The shape below is what the official SDK exposed at the time this plan was written; verify before pasting.

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolInfo is the minimal projection of an MCP tool definition that the
// Stage 1 harness needs. Stage 2 may extend this.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// CallResult is the result of an MCP tools/call.
type CallResult struct {
	IsError bool
	Text    string          // concatenated text-content blocks
	Raw     json.RawMessage // full result for debugging
}

// Client is a thin wrapper over the official MCP SDK's Streamable-HTTP
// session. Keeps Felix code decoupled from SDK type churn.
type Client struct {
	session *mcpsdk.ClientSession
}

// Connect opens an MCP session against serverURL using the supplied HTTP
// client (which is expected to inject auth). The returned *Client must be
// Close()d when done.
func Connect(ctx context.Context, serverURL string, httpClient *http.Client) (*Client, error) {
	transport := mcpsdk.NewStreamableClientTransport(serverURL, &mcpsdk.StreamableClientTransportOptions{
		HTTPClient: httpClient,
	})

	sdkClient := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "felix",
		Version: "0.0.0-stage1-harness",
	}, nil)

	session, err := sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp connect: %w", err)
	}
	return &Client{session: session}, nil
}

// Close terminates the MCP session.
func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.Close()
}

// ListTools returns the tools exposed by the server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	res, err := c.session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	out := make([]ToolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, _ := json.Marshal(t.InputSchema)
		out = append(out, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out, nil
}

// CallTool invokes a tool by name with the supplied JSON arguments.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	res, err := c.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return CallResult{}, fmt.Errorf("mcp tools/call %s: %w", name, err)
	}

	var textBuf string
	for _, block := range res.Content {
		if tc, ok := block.(*mcpsdk.TextContent); ok {
			textBuf += tc.Text
		}
	}
	raw, _ := json.Marshal(res)
	return CallResult{
		IsError: res.IsError,
		Text:    textBuf,
		Raw:     raw,
	}, nil
}
```

If the SDK rejects your code (compile errors), do NOT invent symbols — re-run `go doc github.com/modelcontextprotocol/go-sdk/mcp` and update `SDK_NOTES.md` and the code together.

- [ ] **Step 4: Run tests to verify they pass (or skip with documented reason)**

```bash
go test ./internal/mcp/... -run TestClient
```

Expected: PASS. If the fake server proves insufficient and you used `t.Skip`, expected: SKIP with a visible skip message.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/client.go internal/mcp/client_test.go
git commit -m "feat(mcp): minimal Streamable-HTTP client wrapper

Wraps the official MCP Go SDK so Felix code stays insulated from
SDK type churn. Stage 1 surface only: Connect, ListTools, CallTool, Close."
```

---

## Task 5: Cobra subcommand (`cmd/felix/gtharness.go`)

**Files:**
- Create: `cmd/felix/gtharness.go`
- Modify: `cmd/felix/main.go` (one-line registration)

The harness command is glue — no business logic. Loads creds, builds the OAuth client, opens an MCP session, prints `tools/list`, optionally invokes one tool, exits.

- [ ] **Step 1: Create the subcommand file**

Create `cmd/felix/gtharness.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sausheong/felix/internal/mcp"
)

func gtHarnessCmd() *cobra.Command {
	var (
		envFile string
		callTool string
		callArgs string
	)
	cmd := &cobra.Command{
		Use:   "gt-harness",
		Short: "Smoke-test connection to a remote MCP gateway (experimental)",
		Long: "Reads OAuth client-credentials from a dotenv-style file, opens an " +
			"MCP Streamable-HTTP session, and lists (or invokes) tools.\n\n" +
			"Required env keys: MCP_SERVER_URL, LTM_CLIENT_ID, LTM_CLIENT_SECRET, " +
			"LTM_TOKEN_URL, LTM_SCOPE.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGtHarness(cmd.Context(), os.Stdout, envFile, callTool, callArgs)
		},
	}
	cmd.Flags().StringVar(&envFile, "env-file", "gt-harness.txt", "path to KEY=VALUE credentials file")
	cmd.Flags().StringVar(&callTool, "call", "", "optional: name of a tool to invoke after listing")
	cmd.Flags().StringVar(&callArgs, "args", "{}", "JSON object of arguments for --call")
	return cmd
}

func runGtHarness(ctx context.Context, out io.Writer, envFile, callTool, callArgs string) error {
	env, err := mcp.LoadEnvFile(envFile)
	if err != nil {
		return err
	}
	if err := mcp.RequireKeys(env,
		"MCP_SERVER_URL", "LTM_CLIENT_ID", "LTM_CLIENT_SECRET",
		"LTM_TOKEN_URL", "LTM_SCOPE",
	); err != nil {
		return err
	}

	httpClient := mcp.NewClientCredentialsHTTPClient(mcp.ClientCredentialsConfig{
		TokenURL:     env["LTM_TOKEN_URL"],
		ClientID:     env["LTM_CLIENT_ID"],
		ClientSecret: env["LTM_CLIENT_SECRET"],
		Scope:        env["LTM_SCOPE"],
	})

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, err := mcp.Connect(connectCtx, env["MCP_SERVER_URL"], httpClient)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}

	fmt.Fprintf(out, "Connected to %s\n", env["MCP_SERVER_URL"])
	fmt.Fprintf(out, "Tools (%d):\n", len(tools))
	for _, t := range tools {
		fmt.Fprintf(out, "  - %s\n      %s\n", t.Name, t.Description)
		if len(t.InputSchema) > 0 {
			fmt.Fprintf(out, "      schema: %s\n", string(t.InputSchema))
		}
	}

	if callTool == "" {
		return nil
	}

	var argsMap map[string]any
	if err := json.Unmarshal([]byte(callArgs), &argsMap); err != nil {
		return fmt.Errorf("--args must be a JSON object: %w", err)
	}

	fmt.Fprintf(out, "\nCalling %s(%s) ...\n", callTool, callArgs)
	res, err := client.CallTool(ctx, callTool, argsMap)
	if err != nil {
		return err
	}
	if res.IsError {
		fmt.Fprintf(out, "Tool returned error.\n")
	}
	if res.Text != "" {
		fmt.Fprintf(out, "Text:\n%s\n", res.Text)
	}
	fmt.Fprintf(out, "Raw:\n%s\n", string(res.Raw))
	return nil
}
```

- [ ] **Step 2: Register the subcommand in `main.go`**

Open `cmd/felix/main.go`. Find the `rootCmd.AddCommand(...)` block (around line 51–61). Add `gtHarnessCmd(),` to the list:

```go
	rootCmd.AddCommand(
		startCmd(),
		chatCmd(),
		clearCmd(),
		statusCmd(),
		sessionsCmd(),
		versionCmd(),
		onboardCmd(),
		doctorCmd(),
		modelCmd(),
		gtHarnessCmd(),
	)
```

- [ ] **Step 3: Build to verify compilation**

```bash
go build -o felix ./cmd/felix
```

Expected: clean build, no errors.

- [ ] **Step 4: Smoke-check the command lists itself**

```bash
./felix gt-harness --help
```

Expected: usage text shows `--env-file`, `--call`, `--args` flags.

- [ ] **Step 5: Commit**

```bash
git add cmd/felix/gtharness.go cmd/felix/main.go
git commit -m "feat(cli): add gt-harness MCP smoke-test subcommand"
```

---

## Task 6: End-to-end manual verification

**Files:**
- None (verification only)

This is the only test that exercises the real AgentCore gateway. It is intentionally a manual step — automating it would require committing live credentials.

- [ ] **Step 1: Confirm `gt-harness.txt` is present and not staged**

```bash
ls gt-harness.txt
git status --porcelain | grep gt-harness.txt
```

Expected: file exists; git does not list it as modified or untracked. (If it shows as untracked, Task 7 will fix that.)

- [ ] **Step 2: List tools against the live gateway**

```bash
./felix gt-harness
```

Expected: `Connected to https://...amazonaws.com/mcp` followed by a non-zero list of tools, each with a name and (likely) a description and JSON schema. If you see `connect: ... 401` or `403`, the OAuth token request likely failed — re-check the creds file and the scope.

- [ ] **Step 3: Invoke one tool**

Pick the simplest-looking tool from Step 2's output (likely something with no required args, or a "search" with a string arg). Invoke it:

```bash
./felix gt-harness --call <tool-name> --args '{}'
# or, for a tool that takes a query:
./felix gt-harness --call <tool-name> --args '{"query": "hello"}'
```

Expected: a `Text:` and/or `Raw:` block with the tool's response. `Tool returned error.` is acceptable as long as the round-trip completed — that proves the plumbing works even when the tool itself rejects the input.

- [ ] **Step 4: Capture the verification evidence**

Paste the (sanitized) output of Step 2 and Step 3 into the commit message of Task 7. This becomes the proof that Stage 1 is real.

---

## Task 7: Gitignore and verification commit

**Files:**
- Modify: `.gitignore`
- Possibly modify: nothing else (this is the closing commit)

- [ ] **Step 1: Add ignores**

Append to `.gitignore`:

```
# Local secrets / experiments
gt-harness.txt
*.env
internal/mcp/SDK_NOTES.md
```

- [ ] **Step 2: Verify `gt-harness.txt` is now ignored**

```bash
git check-ignore -v gt-harness.txt
```

Expected: output references `.gitignore`. If the file was already tracked before being added to `.gitignore`, it must be removed from the index:

```bash
git rm --cached gt-harness.txt 2>/dev/null || true
```

- [ ] **Step 3: Run the full test suite once more**

```bash
go test ./... -count=1
```

Expected: all packages green, including new `internal/mcp` tests.

- [ ] **Step 4: Commit**

```bash
git add .gitignore
git commit -m "chore: ignore gt-harness.txt and SDK notes

Stage 1 of MCP integration verified end-to-end against the
AgentCore gateway:

<paste sanitized output of Task 6 Steps 2 and 3 here>"
```

---

## Done criteria

Stage 1 is complete when:
- `go test ./...` is green.
- `./felix gt-harness` lists tools against the live AgentCore gateway.
- `./felix gt-harness --call <tool>` returns a result (error result is fine).
- `gt-harness.txt` is gitignored.
- The packages `internal/mcp/{creds,oauth,client}.go` exist and are independently usable (Stage 2 will lift them as-is).

Stage 2 (config-driven subsystem) is a separate plan, written after Stage 1's learnings settle.
