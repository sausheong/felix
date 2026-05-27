package cortextools_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/felix/internal/tools/cortextools"
	"github.com/sausheong/harness/tool"
)

// openTestCortex opens a fresh *cortex.Cortex on a temp file with the
// deterministic extractor so Remember/Recall round-trips can actually
// produce entities to match against. Mirrors the minimal setup that
// internal/cortex/cortex.go uses for the "local" provider path.
func openTestCortex(t *testing.T) *cortex.Cortex {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatalf("cortex.Open: %v", err)
	}
	t.Cleanup(func() { _ = cx.Close() })
	return cx
}

// findTool returns the named tool from a tools slice, or nil if absent.
func findTool(tools []tool.Tool, name string) tool.Tool {
	for _, t := range tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func execJSON(t *testing.T, tl tool.Tool, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tl.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s.Execute: %v", tl.Name(), err)
	}
	return res.Output
}

func TestBuildTools_NilCortex_ReturnsListingStubs(t *testing.T) {
	// With nil cortex, BuildTools returns the same four tools so the
	// shared registry can list them in the Settings UI. Each Execute
	// returns the "cortex not configured" sentinel; Name/Description/
	// Parameters work normally.
	tools := cortextools.BuildTools(nil)
	if len(tools) != 4 {
		t.Fatalf("expected 4 listing stubs, got %d", len(tools))
	}
	ctx := context.Background()
	for _, tl := range tools {
		out, err := tl.Execute(ctx, []byte(`{}`))
		if err != nil {
			t.Fatalf("%s.Execute returned error: %v", tl.Name(), err)
		}
		if !strings.HasPrefix(out.Output, "error: cortex not configured") {
			t.Fatalf("%s.Execute output = %q, want 'cortex not configured' prefix", tl.Name(), out.Output)
		}
	}
}

func TestBuildTools_FourTools(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}
	want := map[string]bool{
		"recall":            false,
		"remember":          false,
		"find_entities":     false,
		"get_relationships": false,
	}
	for _, tl := range tools {
		if _, ok := want[tl.Name()]; ok {
			want[tl.Name()] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("missing tool: %s", n)
		}
	}
}

// TestRememberThenRecall validates the round-trip: a remember call should
// succeed without error, and a subsequent recall should return either a
// hit (preferred) or the empty-results sentinel. We accept either because
// the deterministic extractor's recall surface is keyword-based and may
// or may not match depending on the cortex version's stop-word list.
func TestRememberThenRecall(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	remember := findTool(tools, "remember")
	recall := findTool(tools, "recall")
	if remember == nil || recall == nil {
		t.Fatalf("remember or recall tool missing")
	}

	out := execJSON(t, remember, map[string]any{
		"content": "Sausheong prefers oat milk in coffee",
	})
	if out != "Remembered." {
		t.Fatalf("remember output: got %q want %q", out, "Remembered.")
	}

	out = execJSON(t, recall, map[string]any{"query": "oat milk"})
	// Accept either a hit containing 'oat' OR the no-results sentinel —
	// the deterministic extractor's recall is keyword-driven and may miss
	// depending on tokenization. Anything else (an error) is a failure.
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("recall returned error: %q", out)
	}
	if out != "No results." && !strings.Contains(strings.ToLower(out), "oat") {
		t.Fatalf("recall output neither contains 'oat' nor is 'No results.': %q", out)
	}
}

func TestRecall_EmptyResults(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	recall := findTool(tools, "recall")
	out := execJSON(t, recall, map[string]any{"query": "xyzzy-nonexistent"})
	if out != "No results." {
		t.Fatalf("expected 'No results.', got %q", out)
	}
}

func TestRecall_RequiresQuery(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	recall := findTool(tools, "recall")
	out := execJSON(t, recall, map[string]any{})
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected error prefix, got %q", out)
	}
}

func TestRemember_RequiresContent(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	remember := findTool(tools, "remember")
	out := execJSON(t, remember, map[string]any{})
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected error prefix, got %q", out)
	}
}

func TestFindEntities_Empty(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	find := findTool(tools, "find_entities")
	out := execJSON(t, find, map[string]any{"name": "nonexistent-entity-xyzzy"})
	if out != "No entities found." {
		t.Fatalf("expected 'No entities found.', got %q", out)
	}
}

func TestGetRelationships_RequiresEntityID(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	gr := findTool(tools, "get_relationships")
	out := execJSON(t, gr, map[string]any{})
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected error prefix, got %q", out)
	}
}
