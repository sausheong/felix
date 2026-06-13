package mcp

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadEnvFile_QuotingAndComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		`A="double"`,
		`B='single'`,
		`C=plain # trailing comment`,
		`D=a#b`,
		`E="has # hash inside"`,
		`F=  spaced  `,
	}, "\n")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	env, err := LoadEnvFile(p)
	require.NoError(t, err)
	require.Equal(t, "double", env["A"])
	require.Equal(t, "single", env["B"])
	require.Equal(t, "plain", env["C"])
	require.Equal(t, "a#b", env["D"])
	require.Equal(t, "has # hash inside", env["E"])
	require.Equal(t, "spaced", env["F"])
}

func TestRequireKeys(t *testing.T) {
	env := map[string]string{"A": "1", "B": "2"}
	require.NoError(t, RequireKeys(env, "A", "B"))

	err := RequireKeys(env, "A", "C", "D")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "C")
	assert.Contains(t, err.Error(), "D")
	assert.NotContains(t, err.Error(), "A")
}
