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
		out[key] = parseEnvValue(line[eq+1:])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	return out, nil
}

// parseEnvValue applies the common .env conventions: surrounding single or
// double quotes are stripped (and their contents taken literally, including
// any '#'); otherwise a trailing inline comment introduced by whitespace+'#'
// is removed. It intentionally does NOT do shell escaping or variable
// interpolation.
func parseEnvValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	// Strip a trailing inline comment: a '#' preceded by whitespace.
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	} else if i := strings.Index(v, "\t#"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
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
