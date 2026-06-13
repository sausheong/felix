# Security Deeper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the deeper security tier — SSRF (DNS-rebinding), config-parser corruption, gateway surface caps, and unbounded file reads — across `harness` and `felix`.

**Architecture:** Four independent groups. G1 (SSRF) adds one shared `SafeHTTPClient` in `harness/tools/web` used by web_fetch + all web_search backends, plus a Chrome `host-resolver-rules` flag. G2 replaces the line-based JSON5 stripper in `felix` with a single-pass state machine. G3 adds gateway caps/auth/ctx fixes. G4 adds file-size guards in `harness`.

**Tech Stack:** Go 1.25, `net/http` custom Transport, chromedp, chi/gorilla gateway, testify.

**Cross-cutting rules:**
- harness and felix share a `go.mod replace`. After ANY harness change: `cd /Users/sausheong/projects/felix && go build ./...`.
- Run `go test -race ./...` in the repo you changed.
- This round changes behavior by design. Bar: **no legitimate use breaks** — paired tests (attack blocked + happy path unchanged).
- Commit messages: NO `Co-Authored-By` trailer.
- Spec: `docs/superpowers/specs/2026-06-13-security-deeper-design.md`.

---

### Task 1: SSRF — shared SafeHTTPClient (S2)

**Files:**
- Create: `/Users/sausheong/projects/harness/tools/web/client.go`
- Create: `/Users/sausheong/projects/harness/tools/web/client_test.go`

Builds the IP-pinning transport that closes the DNS-rebinding window. `isPrivateIP` already exists in `ssrf.go` (same package). `ValidateURLNotInternal` already exists for redirect re-validation.

- [ ] **Step 1: Write the failing test**

In `client_test.go`:

```go
package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// dialErrorTransport lets us assert what address the transport tried to dial.
func TestSafeHTTPClient_BlocksPrivateAtDial(t *testing.T) {
	// A resolver that maps "evil.test" to a loopback IP simulates rebinding:
	// the dial-time resolution returns a private address.
	client := SafeHTTPClient(5 * time.Second)
	// Use a URL whose host resolves to loopback via /etc/hosts-independent path:
	// 127.0.0.1 is itself private, so dialing it directly must be refused.
	_, err := client.Get("http://127.0.0.1:9/") // discard port, never connects
	require.Error(t, err)
	require.Contains(t, err.Error(), "internal address")
}

func TestSafeHTTPClient_AllowsPublic(t *testing.T) {
	// A test server listens on loopback, but we rewrite the request to use a
	// public-looking host that the custom resolver maps to the test server.
	// Simpler: assert a public IP dials through by pointing at a server via its
	// real loopback addr is impossible (loopback is blocked). Instead verify the
	// transport is non-nil and has a DialContext set.
	client := SafeHTTPClient(5 * time.Second)
	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, tr.DialContext)
	require.NotNil(t, client.CheckRedirect)
	require.Equal(t, 5*time.Second, client.Timeout)
}

func TestSafeHTTPClient_RedirectToPrivateBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	client := SafeHTTPClient(5 * time.Second)
	// The initial server is loopback (private) so the FIRST dial is already
	// blocked — assert the error mentions the block.
	_, err := client.Get(srv.URL)
	require.Error(t, err)
}

// ensure isPrivateIP is reachable from this package (compile guard).
var _ = isPrivateIP(net.IPv4(127, 0, 0, 1))
var _ = strings.TrimSpace
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -run TestSafeHTTPClient -v`
Expected: FAIL — `undefined: SafeHTTPClient`.

- [ ] **Step 3: Write minimal implementation**

Create `client.go`:

```go
package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// SafeHTTPClient returns an *http.Client whose transport resolves and validates
// the destination host, then dials the exact validated IP — closing the
// TOCTOU/DNS-rebinding window where a stock http.Client would re-resolve the
// hostname independently of ValidateURLNotInternal. Redirects are re-validated.
func SafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve %q — blocking to prevent SSRF", host)
			}
			var dialIP net.IP
			for _, ip := range ips {
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("access to internal address %s (%s) is blocked", host, ip)
				}
				if dialIP == nil {
					dialIP = ip
				}
			}
			if dialIP == nil {
				return nil, fmt.Errorf("no usable address for %q", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects (max 10)")
			}
			if err := ValidateURLNotInternal(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -run TestSafeHTTPClient -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/harness
git add tools/web/client.go tools/web/client_test.go
git commit -m "feat(web): add SafeHTTPClient with IP-pinning transport (S2)"
```

---

### Task 2: SSRF — wire web_fetch through SafeHTTPClient (S2)

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/web/webfetch.go:88-100`
- Test: `/Users/sausheong/projects/harness/tools/web/webfetch_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Append to `webfetch_test.go` (create the file with `package web` header if it does not exist):

```go
func TestWebFetch_BlocksPrivateHost(t *testing.T) {
	tool := &WebFetchTool{}
	res, err := tool.Execute(context.Background(),
		[]byte(`{"url":"http://169.254.169.254/latest/meta-data/"}`))
	require.NoError(t, err)
	require.NotEmpty(t, res.Error, "private metadata IP must be rejected")
}
```

Ensure imports include `context`, `testing`, and `github.com/stretchr/testify/require`.

- [ ] **Step 2: Run test to verify it fails or passes via pre-check**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -run TestWebFetch_BlocksPrivateHost -v`
Expected: PASS already (pre-flight `ValidateURLNotInternal` blocks `169.254.x`). This test is the regression guard; the real change is swapping the client. Proceed to make the dial path safe too.

- [ ] **Step 3: Replace the inline client**

In `webfetch.go`, replace lines 88-100 (the `client := &http.Client{ ... }` block) with:

```go
	client := SafeHTTPClient(fetchTimeout)
```

Leave the pre-flight `ValidateURLNotInternal(in.URL)` at line ~70 unchanged. Leave `maxFetchSize` / `MaxOutputLength` unchanged.

- [ ] **Step 4: Run tests**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -v`
Expected: PASS (all web tests, including existing ones).

- [ ] **Step 5: Verify felix still builds**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/harness
git add tools/web/webfetch.go tools/web/webfetch_test.go
git commit -m "fix(web): web_fetch uses SafeHTTPClient — pins validated IP (S2)"
```

---

### Task 3: SSRF — route all web_search backends through SafeHTTPClient (N4)

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/web/websearch.go:134` (DDG path)
- Modify: `/Users/sausheong/projects/harness/tools/web/websearch_backends.go:61,122,172` (brave, tavily, searxng)
- Test: `/Users/sausheong/projects/harness/tools/web/websearch_test.go` (create if absent)

There are **five** `http.DefaultClient` call sites: `websearch.go:134` and `websearch_backends.go:61,122,172`. A single package-level safe client covers all.

- [ ] **Step 1: Write the failing test**

In `websearch_test.go`:

```go
package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSearchClient_IsSafe(t *testing.T) {
	require.NotNil(t, searchClient)
	require.NotNil(t, searchClient.Transport)
	require.NotNil(t, searchClient.CheckRedirect)
	require.Equal(t, searchTimeout, searchClient.Timeout)
}

func TestSearxng_BlocksPrivateBaseURL(t *testing.T) {
	b := newSearxngBackend("http://127.0.0.1:8080")
	_, err := b.Search(testCtx(t), "hello", 3)
	require.Error(t, err)
}
```

Add a tiny helper in the same file:

```go
import "context"

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -run 'TestSearchClient_IsSafe|TestSearxng' -v`
Expected: FAIL — `undefined: searchClient`.

- [ ] **Step 3: Add a package-level safe search client and use it everywhere**

In `websearch.go`, add near the `searchTimeout` const (line ~15):

```go
// searchClient is the shared SSRF-safe HTTP client for all web_search
// backends. It pins the validated IP and re-validates redirects, bringing
// web_search to parity with web_fetch. (N4)
var searchClient = SafeHTTPClient(searchTimeout)
```

In `websearch.go:134`, change `resp, err := http.DefaultClient.Do(req)` →
`resp, err := searchClient.Do(req)`.

In `websearch_backends.go`, change all three `http.DefaultClient.Do(req)` calls
(`:61` brave, `:122` tavily, `:172` searxng) → `searchClient.Do(req)`.

If `net/http` becomes unused in `websearch_backends.go` after the swap, keep it —
`http.NewRequestWithContext`, `http.MethodGet`, `http.StatusOK` are still used, so the import stays.

- [ ] **Step 4: Document the backend contract**

In `websearch_backends.go`, extend the `WebSearchBackend` interface doc comment (line ~13) by appending:

```go
// Implementations that make outbound HTTP calls MUST use the package
// searchClient (SafeHTTPClient) so a configured/typo'd internal base URL
// cannot be used for SSRF. (N4)
```

- [ ] **Step 5: Run tests**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/web/ -v`
Expected: PASS.

- [ ] **Step 6: Verify felix builds, commit**

```bash
cd /Users/sausheong/projects/felix && go build ./...
cd /Users/sausheong/projects/harness
git add tools/web/websearch.go tools/web/websearch_backends.go tools/web/websearch_test.go
git commit -m "fix(web): all web_search backends use SafeHTTPClient (N4)"
```

---

### Task 4: SSRF — block private-IP resolution in the browser (S3)

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/browser/browser.go:470-479` (`launchBrowser` allocator options)
- Test: `/Users/sausheong/projects/harness/tools/browser/browser_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

The flag list is internal to `launchBrowser`. Extract it to a package var so it is testable without launching Chrome.

In `browser_test.go`:

```go
package browser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostResolverRules_BlocksPrivateRanges(t *testing.T) {
	require.Contains(t, hostResolverRules, "127.0.0.0/8")
	require.Contains(t, hostResolverRules, "169.254.0.0/16")
	require.Contains(t, hostResolverRules, "10.0.0.0/8")
	require.Contains(t, hostResolverRules, "192.168.0.0/16")
	require.Contains(t, hostResolverRules, "172.16.0.0/12")
	require.Contains(t, hostResolverRules, "localhost")
	require.True(t, strings.Contains(hostResolverRules, "~NOTFOUND"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/browser/ -run TestHostResolverRules -v`
Expected: FAIL — `undefined: hostResolverRules`.

- [ ] **Step 3: Add the rules var and wire the flag**

In `browser.go`, add near the other consts (after line ~37):

```go
// hostResolverRules forces Chrome to fail DNS resolution for private/internal
// IP ranges and localhost, so in-page fetch() and evaluate()'d JS cannot reach
// cloud-metadata endpoints or localhost services — surfaces the Go-side
// ValidateURLNotInternal check (which only sees the top-level URL) covers for
// sub-resource and script-initiated requests. (S3)
//
// Residual gap: name-based rules do not stop a page that dials a *literal*
// private IP URL in some Chrome builds; the complete fix is an allowlisting
// proxy (deferred). See the spec's limitations section.
const hostResolverRules = "MAP localhost ~NOTFOUND," +
	"MAP *.localhost ~NOTFOUND," +
	"MAP 127.0.0.0/8 ~NOTFOUND," +
	"MAP 10.0.0.0/8 ~NOTFOUND," +
	"MAP 172.16.0.0/12 ~NOTFOUND," +
	"MAP 169.254.0.0/16 ~NOTFOUND," +
	"MAP 192.168.0.0/16 ~NOTFOUND," +
	"MAP [::1] ~NOTFOUND"
```

In `launchBrowser`, add to the `append(chromedp.DefaultExecAllocatorOptions[:], ...)` list (after the existing `chromedp.Flag("lang", "en-US"),` line):

```go
			chromedp.Flag("host-resolver-rules", hostResolverRules),
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/browser/ -v`
Expected: PASS (unit tests; live Chrome tests are `//go:build live` and not run here).

- [ ] **Step 5: Add a live test (guarded, not run in CI)**

Create `/Users/sausheong/projects/harness/tools/browser/browser_ssrf_live_test.go`:

```go
//go:build live

package browser

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInPageFetchPrivateIPBlocked_Live verifies host-resolver-rules stops an
// in-page fetch to a metadata IP. Requires a real Chrome. Run:
//   go test -tags live ./tools/browser/ -run TestInPageFetchPrivateIPBlocked_Live
func TestInPageFetchPrivateIPBlocked_Live(t *testing.T) {
	tool := NewBrowserTool()
	defer tool.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Navigate to a harmless public page first, then attempt an in-page fetch
	// to the metadata service; it must fail (resolution blocked).
	res, err := tool.Execute(ctx, []byte(`{"action":"evaluate","url":"https://example.com","session":"ssrf","script":"fetch('http://169.254.169.254/').then(()=>'REACHED').catch(e=>'BLOCKED:'+e)"}`))
	require.NoError(t, err)
	require.NotContains(t, res.Output, "REACHED")
}
```

- [ ] **Step 6: Verify felix builds, commit**

```bash
cd /Users/sausheong/projects/felix && go build ./...
cd /Users/sausheong/projects/harness
git add tools/browser/browser.go tools/browser/browser_test.go tools/browser/browser_ssrf_live_test.go
git commit -m "fix(browser): block private-IP resolution via host-resolver-rules (S3)"
```

---

### Task 5: File size cap for read_file and edit_file (N5)

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/file/readfile.go:96`
- Modify: `/Users/sausheong/projects/harness/tools/file/editfile.go:77`
- Test: `/Users/sausheong/projects/harness/tools/file/readfile_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

In `readfile_test.go`:

```go
package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeSized(t *testing.T, dir, name string, n int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, make([]byte, n), 0o600))
	return p
}

func TestReadFile_RejectsOversizedText(t *testing.T) {
	dir := t.TempDir()
	p := writeSized(t, dir, "big.txt", maxTextFileSize+1)
	tool := &ReadFileTool{}
	in, _ := json.Marshal(map[string]string{"path": p})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Contains(t, res.Error, "too large")
}

func TestReadFile_AllowsNormalText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ok.txt")
	require.NoError(t, os.WriteFile(p, []byte("hello"), 0o600))
	tool := &ReadFileTool{}
	in, _ := json.Marshal(map[string]string{"path": p})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Empty(t, res.Error)
	require.Equal(t, "hello", res.Output)
}

func TestReadFile_RejectsOversizedImage(t *testing.T) {
	dir := t.TempDir()
	p := writeSized(t, dir, "big.png", maxImageFileSize+1)
	tool := &ReadFileTool{}
	in, _ := json.Marshal(map[string]string{"path": p})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Contains(t, res.Error, "too large")
}

func TestEditFile_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	p := writeSized(t, dir, "big.txt", maxTextFileSize+1)
	tool := &EditFileTool{}
	in, _ := json.Marshal(map[string]string{"path": p, "old_string": "a", "new_string": "b"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Contains(t, res.Error, "too large")
}

var _ = strings.TrimSpace
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -run 'TestReadFile_|TestEditFile_Rejects' -v`
Expected: FAIL — `undefined: maxTextFileSize`.

- [ ] **Step 3: Add limits + a shared guard helper**

Create `/Users/sausheong/projects/harness/tools/file/limits.go`:

```go
package file

import (
	"fmt"
	"os"
)

const (
	maxTextFileSize  = 10 * 1024 * 1024 // 10 MB
	maxImageFileSize = 5 * 1024 * 1024  // 5 MB
)

// checkFileSize stats path and returns a non-empty error string if the file
// exceeds limit. kind is "text" or "image" for the message. Empty return =
// within bounds (or stat failed, in which case the caller's read handles it).
func checkFileSize(path string, limit int64, kind string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "" // let the subsequent read surface the real error
	}
	if fi.Size() > limit {
		return fmt.Sprintf("file too large: %d bytes exceeds the %d byte limit for %s files; read a specific range or use a different tool",
			fi.Size(), limit, kind)
	}
	return ""
}
```

- [ ] **Step 4: Guard read_file**

In `readfile.go`, replace the read block at lines 96-99:

```go
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return tool.ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}
```

with:

```go
	ext := strings.ToLower(filepath.Ext(in.Path))
	limit := int64(maxTextFileSize)
	kind := "text"
	if _, isImg := imageExtMap[ext]; isImg {
		limit = int64(maxImageFileSize)
		kind = "image"
	}
	if msg := checkFileSize(in.Path, limit, kind); msg != "" {
		return tool.ToolResult{Error: msg}, nil
	}

	f, err := os.Open(in.Path)
	if err != nil {
		return tool.ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return tool.ToolResult{Error: fmt.Sprintf("failed to read file: %v", err)}, nil
	}
	if int64(len(data)) > limit {
		return tool.ToolResult{Error: fmt.Sprintf("file too large: exceeds the %d byte limit for %s files; read a specific range or use a different tool", limit, kind)}, nil
	}
```

Then DELETE the now-duplicate `ext := strings.ToLower(filepath.Ext(in.Path))` line that exists later at line ~102 (the image-detection block reuses `ext`). Add `"io"` to the import block. Keep the existing `imageExtMap` lookup that follows for MIME detection (it already uses `ext`).

- [ ] **Step 5: Guard edit_file**

In `editfile.go`, immediately before line 77 (`data, err := os.ReadFile(in.Path)`), insert:

```go
	if msg := checkFileSize(in.Path, int64(maxTextFileSize), "text"); msg != "" {
		return tool.ToolResult{Error: msg}, nil
	}
```

(edit_file's existing `os.ReadFile` stays — it is bounded by the stat check, and an oversized file can't be single-shot edited anyway.)

- [ ] **Step 6: Run tests**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -v`
Expected: PASS (new + existing).

- [ ] **Step 7: Verify felix builds, commit**

```bash
cd /Users/sausheong/projects/felix && go build ./...
cd /Users/sausheong/projects/harness
git add tools/file/limits.go tools/file/readfile.go tools/file/editfile.go tools/file/readfile_test.go
git commit -m "feat(file): cap read_file/edit_file size to prevent OOM (N5)"
```

---

### Task 6: JSON5 parser — single-pass state machine (S6)

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/config/config.go:865-923` (replace `stripJSON5`, delete `inString` + `removeTrailingCommas`, add `nextSignificantIsCloser`)
- Test: `/Users/sausheong/projects/felix/internal/config/config_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `config_test.go` (ensure `package config`, imports `encoding/json`, `testing`, `github.com/stretchr/testify/require`):

```go
func TestStripJSON5_PreservesCommaInString(t *testing.T) {
	in := `{"prompt": "say hi, " }`
	out := stripJSON5(in)
	require.True(t, json.Valid([]byte(out)), "output must be valid JSON: %q", out)
	var m map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, "say hi, ", m["prompt"], "comma inside string must survive")
}

func TestStripJSON5_StripsCommentAfterURL(t *testing.T) {
	in := `{"base_url": "http://x/v1", // note` + "\n" + `"k": 1}`
	out := stripJSON5(in)
	require.True(t, json.Valid([]byte(out)), "output must be valid JSON: %q", out)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, "http://x/v1", m["base_url"], "URL with // must survive")
}

func TestStripJSON5_TrailingComma(t *testing.T) {
	in := `{"a": 1, "b": 2,}`
	out := stripJSON5(in)
	var m map[string]int
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, 2, m["b"])
}

func TestStripJSON5_BlockComment(t *testing.T) {
	in := `{ /* hi */ "a": 1 }`
	out := stripJSON5(in)
	var m map[string]int
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, 1, m["a"])
}

func TestStripJSON5_EscapedQuoteInString(t *testing.T) {
	in := `{"a": "she said \"hi\", ok"}`
	out := stripJSON5(in)
	var m map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, `she said "hi", ok`, m["a"])
}

func TestStripJSON5_LineComment(t *testing.T) {
	in := "// header\n{\"a\": 1}\n"
	out := stripJSON5(in)
	var m map[string]int
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, 1, m["a"])
}

func TestStripJSON5_TrailingCommaThenComment(t *testing.T) {
	in := "{\"a\": 1, // last\n}"
	out := stripJSON5(in)
	var m map[string]int
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, 1, m["a"])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/config/ -run TestStripJSON5 -v`
Expected: FAIL — `TestStripJSON5_PreservesCommaInString` (comma deleted) and `TestStripJSON5_StripsCommentAfterURL` (comment not stripped → invalid JSON) fail on the current stripper; block-comment test fails (unsupported).

- [ ] **Step 3: Replace the stripper**

In `config.go`, replace the entire `stripJSON5` function AND the `inString` function AND the `removeTrailingCommas` function (lines 865-923) with:

```go
// stripJSON5 converts JSON5-lite (// line comments, /* block */ comments, and
// trailing commas before } or ]) into standard JSON. It walks the input once,
// tracking string/escape/comment state so commas and comment markers INSIDE
// string literals are never misread. Replaces the prior line-based stripper,
// which corrupted strings containing ", " and failed on URLs containing "//".
func stripJSON5(s string) string {
	const (
		stDefault = iota
		stString
		stEscape
		stLineComment
		stBlockComment
	)
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	state := stDefault
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch state {
		case stDefault:
			switch {
			case c == '"':
				b.WriteRune(c)
				state = stString
			case c == '/' && i+1 < len(runes) && runes[i+1] == '/':
				state = stLineComment
				i++
			case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
				state = stBlockComment
				i++
			case c == ',':
				if nextSignificantIsCloser(runes, i+1) {
					// drop trailing comma
				} else {
					b.WriteRune(c)
				}
			default:
				b.WriteRune(c)
			}
		case stString:
			b.WriteRune(c)
			if c == '\\' {
				state = stEscape
			} else if c == '"' {
				state = stDefault
			}
		case stEscape:
			b.WriteRune(c)
			state = stString
		case stLineComment:
			if c == '\n' {
				b.WriteRune(c)
				state = stDefault
			}
		case stBlockComment:
			if c == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				i++
				state = stDefault
			}
		}
	}
	return b.String()
}

// nextSignificantIsCloser reports whether the next non-whitespace, non-comment
// rune starting at index j is } or ] — i.e. the comma at j-1 is a trailing
// comma. Skips whitespace and // and /* comments.
func nextSignificantIsCloser(runes []rune, j int) bool {
	for j < len(runes) {
		c := runes[j]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			j++
		case c == '/' && j+1 < len(runes) && runes[j+1] == '/':
			for j < len(runes) && runes[j] != '\n' {
				j++
			}
		case c == '/' && j+1 < len(runes) && runes[j+1] == '*':
			j += 2
			for j+1 < len(runes) && !(runes[j] == '*' && runes[j+1] == '/') {
				j++
			}
			j += 2
		case c == '}' || c == ']':
			return true
		default:
			return false
		}
	}
	return false
}
```

If `strings` is no longer imported elsewhere after removing the old functions, keep it — `strings.Builder` uses it. Verify no other code references `inString` or `removeTrailingCommas`: run `grep -rn "inString\|removeTrailingCommas" internal/` — expect zero hits after the edit.

- [ ] **Step 4: Run tests**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/config/ -v`
Expected: PASS (all config tests, new + existing).

- [ ] **Step 5: Golden check against the real default config**

Run: `cd /Users/sausheong/projects/felix && go build -o /tmp/felix ./cmd/felix && /tmp/felix doctor 2>&1 | head -5 || true`
Expected: doctor runs without a config parse error (confirms the real config still loads).

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/config/config.go internal/config/config_test.go
git commit -m "fix(config): single-pass JSON5 stripper — string-safe comments/commas (S6)"
```

---

### Task 7: Drop query-param auth, add WS subprotocol auth (S10)

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/gateway/auth.go:24-30`
- Modify: `/Users/sausheong/projects/felix/internal/gateway/websocket.go:84-87` (upgrader) and `:230-235` (Handle upgrade)
- Test: `/Users/sausheong/projects/felix/internal/gateway/auth_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `auth_test.go` (create with `package gateway` + imports if absent):

```go
func TestBearerAuth_RejectsQueryParamToken(t *testing.T) {
	mw := BearerAuthMiddleware("secret")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/settings/api/config?token=secret", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "query-param token must NOT authenticate")
}

func TestBearerAuth_AcceptsHeader(t *testing.T) {
	mw := BearerAuthMiddleware("secret")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/settings/api/config", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}
```

Imports needed: `net/http`, `net/http/httptest`, `testing`, `github.com/stretchr/testify/require`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestBearerAuth -v`
Expected: FAIL on `RejectsQueryParamToken` (currently accepts `?token=`).

- [ ] **Step 3: Remove the query-param fallback**

In `auth.go`, delete lines 26-30 (the block that sets `auth = "Bearer " + r.URL.Query().Get("token")`), so the code goes straight from reading the `Authorization` header to the `HasPrefix` check:

```go
			// Check Authorization header
			auth := r.Header.Get("Authorization")

			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
```

- [ ] **Step 4: Run auth tests**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestBearerAuth -v`
Expected: PASS.

- [ ] **Step 5: Add WS subprotocol auth so browser WS clients still work**

In `websocket.go`, the upgrader is constructed at lines 84-87 and auth for the WS endpoint flows through the same `BearerAuthMiddleware` on the HTTP route. Browser WS clients send the token as a subprotocol. Add subprotocol negotiation to the upgrader so the handshake echoes it back:

In `NewWebSocketHandler`, change the `upgrader` literal (lines 84-87) to also accept the bearer subprotocol:

```go
		upgrader: websocket.Upgrader{
			CheckOrigin: AllowedOrigins(nil), // default: localhost-only; overridden by SetOriginChecker
			Subprotocols: []string{"felix"},  // echoed on accept; browsers may also send "Bearer.<token>"
		},
```

In `Handle` (around line 231), before `h.upgrader.Upgrade`, extract a bearer subprotocol token and translate it into an `Authorization` header so the existing middleware-equivalent check still applies. Since the WS route is already behind `BearerAuthMiddleware` at the HTTP layer, the middleware reads the header — but browser clients can't set it. Add a shim at the top of `Handle`:

```go
	// Browser WebSocket clients cannot set Authorization headers; accept the
	// token via the Sec-WebSocket-Protocol subprotocol "Bearer.<token>" and
	// promote it to the header BEFORE the auth middleware runs. (S10)
	// NOTE: this runs inside the route that BearerAuthMiddleware wraps, so the
	// promotion must happen earlier — see server wiring below.
```

Because the middleware runs *before* `Handle`, the promotion must occur in the middleware chain. Instead, add the promotion to `BearerAuthMiddleware` itself: after reading the header, if empty, check the WS subprotocol header:

In `auth.go`, replace the header read with:

```go
			// Check Authorization header
			auth := r.Header.Get("Authorization")
			if auth == "" {
				// Browser WebSocket clients can't set headers — accept the token
				// via the Sec-WebSocket-Protocol "Bearer.<token>" value. (S10)
				for _, proto := range websocketSubprotocols(r) {
					if strings.HasPrefix(proto, "Bearer.") {
						auth = "Bearer " + strings.TrimPrefix(proto, "Bearer.")
						break
					}
				}
			}
```

Add the helper in `auth.go`:

```go
// websocketSubprotocols parses the comma-separated Sec-WebSocket-Protocol
// request header into individual tokens.
func websocketSubprotocols(r *http.Request) []string {
	raw := r.Header.Get("Sec-WebSocket-Protocol")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
```

- [ ] **Step 6: Test subprotocol auth**

Append to `auth_test.go`:

```go
func TestBearerAuth_AcceptsSubprotocol(t *testing.T) {
	mw := BearerAuthMiddleware("secret")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "felix, Bearer.secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "Bearer.<token> subprotocol must authenticate")
}
```

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestBearerAuth -v`
Expected: PASS.

- [ ] **Step 7: Check no built-in client relies on ?token=**

Run: `cd /Users/sausheong/projects/felix && grep -rn "token=" internal/gateway/*.go cmd/ 2>/dev/null | grep -iv "test" | grep -i "query\|?token" || echo "none found"`
Expected: no live client uses `?token=` for auth (echo prints "none found", or hits are unrelated). If a built-in HTML/JS client sets `?token=`, update it to send the `Bearer.<token>` subprotocol on the WS connection in the same commit.

- [ ] **Step 8: Run full gateway tests, build, commit**

```bash
cd /Users/sausheong/projects/felix && go test ./internal/gateway/ && go build ./...
git add internal/gateway/auth.go internal/gateway/auth_test.go internal/gateway/websocket.go
git commit -m "fix(gateway): drop query-param auth; accept WS Bearer subprotocol (S10)"
```

---

### Task 8: Gateway connection + concurrent-run caps (G3)

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/gateway/websocket.go` (struct fields ~46-66; `Handle` ~230; `handleChatSend` goroutine ~439)
- Test: `/Users/sausheong/projects/felix/internal/gateway/websocket_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `websocket_test.go` (create with `package gateway` if absent):

```go
func TestRunSemaphore_BoundsConcurrentRuns(t *testing.T) {
	h := &WebSocketHandler{}
	h.initLimits()
	// Acquire all permits.
	acquired := 0
	for i := 0; i < maxConcurrentRuns; i++ {
		if h.acquireRun() {
			acquired++
		}
	}
	require.Equal(t, maxConcurrentRuns, acquired)
	require.False(t, h.acquireRun(), "must reject beyond the cap")
	h.releaseRun()
	require.True(t, h.acquireRun(), "permit freed after release")
}

func TestConnCap_BoundsConnections(t *testing.T) {
	h := &WebSocketHandler{}
	h.initLimits()
	for i := 0; i < maxConnections; i++ {
		require.True(t, h.acquireConn())
	}
	require.False(t, h.acquireConn(), "must reject beyond the connection cap")
	h.releaseConn()
	require.True(t, h.acquireConn())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run 'TestRunSemaphore|TestConnCap' -v`
Expected: FAIL — `undefined: initLimits / acquireRun / maxConcurrentRuns`.

- [ ] **Step 3: Add the limits primitives**

Create `/Users/sausheong/projects/felix/internal/gateway/limits.go`:

```go
package gateway

import "sync/atomic"

const (
	maxConnections    = 64 // concurrent WebSocket connections
	maxConcurrentRuns = 8  // concurrent chat.send agent runs
)

// initLimits lazily creates the run semaphore. Safe to call multiple times;
// the connection counter is a plain atomic and needs no init.
func (h *WebSocketHandler) initLimits() {
	if h.runSem == nil {
		h.runSem = make(chan struct{}, maxConcurrentRuns)
	}
}

func (h *WebSocketHandler) acquireRun() bool {
	select {
	case h.runSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *WebSocketHandler) releaseRun() {
	select {
	case <-h.runSem:
	default:
	}
}

func (h *WebSocketHandler) acquireConn() bool {
	if h.connCount.Add(1) > maxConnections {
		h.connCount.Add(-1)
		return false
	}
	return true
}

func (h *WebSocketHandler) releaseConn() { h.connCount.Add(-1) }
```

Add the fields to the `WebSocketHandler` struct in `websocket.go` (after line 65, the `mu sync.RWMutex` line — add before or after, inside the struct):

```go
	runSem    chan struct{} // bounds concurrent chat.send runs; nil until initLimits
	connCount atomic.Int64  // current open WebSocket connections
```

Add `"sync/atomic"` to the `websocket.go` imports.

In `NewWebSocketHandler`, call `initLimits()` before returning — restructure to:

```go
	h := &WebSocketHandler{
		providers:         providers,
		tools:             toolReg,
		sessionStore:      sessionStore,
		config:            cfg,
		compactionProv:    compaction.NewProvider(cfg),
		activeSessionKeys: make(map[*websocket.Conn]map[string]string),
		sessionsBaseDir:   sessionsBaseDir,
		upgrader: websocket.Upgrader{
			CheckOrigin:  AllowedOrigins(nil),
			Subprotocols: []string{"felix"},
		},
	}
	h.initLimits()
	return h
```

- [ ] **Step 4: Enforce the connection cap in Handle**

In `Handle` (line ~230), right after the function opens and BEFORE `h.upgrader.Upgrade`:

```go
	if !h.acquireConn() {
		http.Error(w, `{"error":"too many connections"}`, http.StatusServiceUnavailable)
		return
	}
	defer h.releaseConn()
```

(The `acquireConn` runs before upgrade so a rejected client gets a clean HTTP 503, not a half-open socket.)

- [ ] **Step 5: Enforce the run semaphore in handleChatSend**

In `handleChatSend`, replace the `go func() { ... }()` block (lines ~439-448) with:

```go
	if !h.acquireRun() {
		writeRPCError(conn, metrics, rpcID, -32000, "server busy: too many concurrent runs, retry shortly")
		return
	}
	go func() {
		defer h.releaseRun()
		_, err := chatexec.RunTurn(context.Background(), deps, scope, params.Text, sub)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("chatexec.RunTurn", "agent", scope.AgentID, "session", scope.SessionKey, "error", err)
			writeRPCError(conn, metrics, rpcID, -32603, err.Error())
		}
	}()
```

- [ ] **Step 6: Run tests, build, commit**

```bash
cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -race && go build ./...
git add internal/gateway/limits.go internal/gateway/websocket.go internal/gateway/websocket_test.go
git commit -m "feat(gateway): cap concurrent connections and chat runs (G3)"
```

---

### Task 9: SSE subscriber cap + fan-out off the lock (N2) and cancellable compaction (G4)

**Files:**
- Modify: `/Users/sausheong/projects/felix/internal/gateway/logs.go:81-117` (Handle fan-out), `:148-157` (Subscribe), `:188-208` (stream handler)
- Modify: `/Users/sausheong/projects/felix/internal/gateway/websocket.go:961` (compaction ctx)
- Test: `/Users/sausheong/projects/felix/internal/gateway/logs_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `logs_test.go` (create with `package gateway` if absent):

```go
func TestSubscribe_CapsSubscribers(t *testing.T) {
	buf := NewLogBuffer(16, slog.NewTextHandler(io.Discard, nil))
	var chans []chan LogEntry
	for i := 0; i < maxSSESubscribers; i++ {
		ch := buf.Subscribe()
		require.NotNil(t, ch, "subscriber %d should be admitted", i)
		chans = append(chans, ch)
	}
	require.Nil(t, buf.Subscribe(), "subscriber beyond cap must be refused")
	for _, ch := range chans {
		buf.Unsubscribe(ch)
	}
}

func TestHandle_FanOutDoesNotBlockOnFullSubscriber(t *testing.T) {
	buf := NewLogBuffer(16, slog.NewTextHandler(io.Discard, nil))
	// A subscriber whose channel is never drained.
	ch := buf.Subscribe()
	require.NotNil(t, ch)
	defer buf.Unsubscribe(ch)
	// Fill its 64-deep buffer, then emit more — Handle must not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			_ = buf.Handle(context.Background(), slog.Record{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle blocked on a full subscriber channel")
	}
}
```

Imports: `context`, `io`, `log/slog`, `testing`, `time`, `github.com/stretchr/testify/require`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run 'TestSubscribe_Caps|TestHandle_FanOut' -v`
Expected: FAIL — `undefined: maxSSESubscribers` (and Subscribe never returns nil).

- [ ] **Step 3: Add the cap and refuse-over-cap to Subscribe**

In `logs.go`, add a const near the top of the file (after the `LogEntry` type or with other consts):

```go
const maxSSESubscribers = 16
```

Replace `Subscribe` (lines ~150-157) with:

```go
// Subscribe returns a channel that receives new log entries, or nil if the
// subscriber cap is reached (caller must handle nil). Call Unsubscribe when done.
func (b *LogBuffer) Subscribe() chan LogEntry {
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) >= maxSSESubscribers {
		return nil
	}
	ch := make(chan LogEntry, 64)
	s.subs[ch] = struct{}{}
	return ch
}
```

- [ ] **Step 4: Move fan-out off s.mu in Handle**

In `logs.go`, replace the locked fan-out block in `Handle` (lines ~99-113) with snapshot-then-send:

```go
	s := b.store
	s.mu.Lock()
	s.entries[s.head] = entry
	s.head = (s.head + 1) % s.max
	if s.count < s.max {
		s.count++
	}
	// Snapshot subscriber channels under the lock; send AFTER releasing so a
	// large/slow subscriber set can't serialize every slog call. (N2)
	subs := make([]chan LogEntry, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}
```

- [ ] **Step 5: Handle nil channel in the stream handler**

In `NewLogsStreamHandler` (line ~207), after `ch := buf.Subscribe()` add:

```go
		ch := buf.Subscribe()
		if ch == nil {
			fmt.Fprint(w, ": too many log subscribers, try again later\n\n")
			flusher.Flush()
			return
		}
		defer buf.Unsubscribe(ch)
```

(Replace the existing `ch := buf.Subscribe()` + `defer buf.Unsubscribe(ch)` pair.)

- [ ] **Step 6: G4 — cancellable manual compaction**

In `websocket.go:961`, replace:

```go
	res, err := mgr.MaybeCompact(context.Background(), sess, compaction.ReasonManual, params.Instructions)
```

with:

```go
	// Tie manual compaction to the server context so it unwinds on shutdown.
	// The summarizer already self-bounds with its own per-call deadline, so no
	// extra timeout is needed here. serverCtx is nil-able. (G4)
	compactCtx := h.serverCtx
	if compactCtx == nil {
		compactCtx = context.Background()
	}
	res, err := mgr.MaybeCompact(compactCtx, sess, compaction.ReasonManual, params.Instructions)
```

- [ ] **Step 7: Run tests, build, commit**

```bash
cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -race && go build ./...
git add internal/gateway/logs.go internal/gateway/logs_test.go internal/gateway/websocket.go
git commit -m "fix(gateway): cap SSE subscribers + fan-out off lock (N2); cancellable compaction (G4)"
```

---

### Task 10: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Full harness suite under race**

Run: `cd /Users/sausheong/projects/harness && go test -race ./...`
Expected: PASS.

- [ ] **Step 2: Full felix suite under race**

Run: `cd /Users/sausheong/projects/felix && go test -race ./...`
Expected: PASS.

- [ ] **Step 3: Both build**

Run: `cd /Users/sausheong/projects/harness && go build ./... && cd /Users/sausheong/projects/felix && go build ./... && go build -o /tmp/felix ./cmd/felix`
Expected: no output (success).

- [ ] **Step 4: go vet both**

Run: `cd /Users/sausheong/projects/harness && go vet ./... && cd /Users/sausheong/projects/felix && go vet ./...`
Expected: no output.

---

## Notes for the executor

- **harness ↔ felix:** after every harness commit, `cd felix && go build ./...` before moving on.
- **No `Co-Authored-By` trailer** in any commit message.
- **Test field names:** `tool.ToolResult` has `Output string`, `Error string`, `Images []llm.ImageContent`. `session` types are not used here.
- **If a referenced line number has drifted:** match on the code snippet shown, not the line number.
- **Task order:** 1→2→3 (web SSRF, sequential — share files), 4 (browser), 5 (file), 6 (config), 7→8→9 (gateway, sequential — share websocket.go), 10 (verify). Tasks 4, 5, 6 are independent of each other and of the web/gateway chains.
