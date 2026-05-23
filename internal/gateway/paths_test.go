package gateway

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sausheong/felix/internal/config"
)

func newTestConfig(t *testing.T, agentID, workspace string) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: agentID, Workspace: workspace},
			},
		},
	}
}

func TestResolveAgentPath_HappyPaths(t *testing.T) {
	ws := t.TempDir()
	ws, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "src", "utils"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := newTestConfig(t, "default", ws)

	cases := []struct {
		name, rel, want string
	}{
		{"root", "", ws},
		{"dot", ".", ws},
		{"subdir", "src", filepath.Join(ws, "src")},
		{"nested subdir", "src/utils", filepath.Join(ws, "src", "utils")},
		{"file", "src/main.go", filepath.Join(ws, "src", "main.go")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAgentPath(cfg, "default", tc.rel)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveAgentPath_Rejections(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	cfg := newTestConfig(t, "default", ws)

	cases := []string{
		"../escape",
		".dotfile",
		"sub/.hidden",
	}
	for _, rel := range cases {
		t.Run(rel, func(t *testing.T) {
			_, err := resolveAgentPath(cfg, "default", rel)
			if err == nil {
				t.Errorf("expected error for %q, got nil", rel)
			}
		})
	}
}

func TestResolveAgentPath_UnknownAgent(t *testing.T) {
	ws := t.TempDir()
	cfg := newTestConfig(t, "default", ws)
	_, err := resolveAgentPath(cfg, "nonexistent", "foo")
	if err == nil {
		t.Error("expected error for unknown agent, got nil")
	}
}

func TestAgentWorkspace(t *testing.T) {
	ws := t.TempDir()
	cfg := newTestConfig(t, "myagent", ws)
	if got := agentWorkspace(cfg, "myagent"); got != ws {
		t.Errorf("got %q, want %q", got, ws)
	}
	if got := agentWorkspace(cfg, "unknown"); got != "" {
		t.Errorf("expected empty string for unknown agent, got %q", got)
	}
}

func TestIsInside(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	sub := filepath.Join(ws, "sub")
	outside := t.TempDir()
	outside, _ = filepath.EvalSymlinks(outside)

	if !isInside(ws, ws) {
		t.Error("workspace should be inside itself")
	}
	if !isInside(sub, ws) {
		t.Error("subdir should be inside workspace")
	}
	if isInside(outside, ws) {
		t.Error("sibling dir should not be inside workspace")
	}
}
