package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeLLMText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ascii passthrough", "ls -la /tmp", "ls -la /tmp"},
		{"nbsp between words", "open /Users/me/SGQR\u00a0Specs.pdf", "open /Users/me/SGQR Specs.pdf"},
		{"narrow nbsp", "echo a\u202fb", "echo a b"},
		{"ideographic space", "echo a\u3000b", "echo a b"},
		{"en space", "echo a\u2002b", "echo a b"},
		{"zero-width joiner stripped", "echo foo\u200dbar", "echo foobar"},
		{"bom stripped", "\ufeffls", "ls"},
		{"line separator to newline", "ls\u2028pwd", "ls\npwd"},
		{"preserves real tab and newline", "ls\t-l\npwd", "ls\t-l\npwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeLLMText(tt.in); got != tt.want {
				t.Errorf("sanitizeLLMText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveExistingPath(t *testing.T) {
	dir := t.TempDir()

	// File on disk has a real NBSP in its name.
	nbspPath := filepath.Join(dir, "SGQR\u00a0Specifications.pdf")
	if err := os.WriteFile(nbspPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// File on disk has plain ASCII space.
	asciiPath := filepath.Join(dir, "plain space.txt")
	if err := os.WriteFile(asciiPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	asciiVariantOfNBSP := filepath.Join(dir, "SGQR Specifications.pdf")
	missing := filepath.Join(dir, "does-not-exist.txt")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"existing nbsp path returned unchanged", nbspPath, nbspPath},
		{"existing ascii path returned unchanged", asciiPath, asciiPath},
		{"nbsp emitted by LLM for ascii-space file resolves", filepath.Join(dir, "plain\u00a0space.txt"), asciiPath},
		{"ascii-space LLM input recovers real nbsp file via dir scan", asciiVariantOfNBSP, nbspPath},
		{"missing path returned unchanged", missing, missing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveExistingPath(tt.in); got != tt.want {
				t.Errorf("resolveExistingPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
