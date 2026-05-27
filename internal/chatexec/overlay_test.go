package chatexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/tools"
)

// stubBase is the minimal tools.Executor used as Base in overlay tests.
// It returns whatever defs/names are set on it and reports an error if
// Execute is invoked (overlay tests assert the overlay handles its own
// tool names without falling through to Base).
type stubBase struct {
	defs  []llm.ToolDef
	names []string
}

func (s stubBase) Execute(_ context.Context, name string, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Output: "base:" + name}, nil
}
func (s stubBase) ToolDefs() []llm.ToolDef         { return s.defs }
func (s stubBase) Names() []string                 { return s.names }
func (s stubBase) Get(_ string) (tools.Tool, bool) { return nil, false }

// countingMetrics counts IncToolCalls invocations per tool name so we
// can assert the overlay bumps the counter exactly once per Execute.
type countingMetrics struct {
	calls map[string]int
}

func (m *countingMetrics) IncToolCalls(name string) {
	if m.calls == nil {
		m.calls = map[string]int{}
	}
	m.calls[name]++
}

// TestOverlay_ExecuteFallsThroughToBase verifies any tool name not
// owned by the overlay is dispatched to Base.
func TestOverlay_ExecuteFallsThroughToBase(t *testing.T) {
	overlay := &ChatToolOverlay{Base: stubBase{}}
	res, err := overlay.Execute(context.Background(), "read_file", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "base:read_file" {
		t.Fatalf("Output = %q, want %q", res.Output, "base:read_file")
	}
}

// TestOverlay_ExecuteBumpsMetrics verifies the Metrics hook is called
// once per Execute regardless of whether the overlay handles the tool.
func TestOverlay_ExecuteBumpsMetrics(t *testing.T) {
	m := &countingMetrics{}
	overlay := &ChatToolOverlay{Base: stubBase{}, Metrics: m}
	if _, err := overlay.Execute(context.Background(), "read_file", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if m.calls["read_file"] != 1 {
		t.Fatalf("IncToolCalls(read_file) = %d, want 1", m.calls["read_file"])
	}
}

// TestOverlay_NamesIncludesBase verifies Names() returns Base's names
// alphabetically when no overlay tools are set.
func TestOverlay_NamesIncludesBase(t *testing.T) {
	overlay := &ChatToolOverlay{
		Base: stubBase{names: []string{"write_file", "read_file"}},
	}
	names := overlay.Names()
	if len(names) != 2 || names[0] != "read_file" || names[1] != "write_file" {
		t.Fatalf("Names() = %v, want [read_file write_file]", names)
	}
}

// TestOverlay_ToolDefsSortedAlpha verifies ToolDefs returns defs in
// alphabetical order — required for prompt-cache stability.
func TestOverlay_ToolDefsSortedAlpha(t *testing.T) {
	overlay := &ChatToolOverlay{
		Base: stubBase{defs: []llm.ToolDef{
			{Name: "write_file"},
			{Name: "read_file"},
			{Name: "bash"},
		}},
	}
	defs := overlay.ToolDefs()
	want := []string{"bash", "read_file", "write_file"}
	if len(defs) != len(want) {
		t.Fatalf("len(defs) = %d, want %d", len(defs), len(want))
	}
	for i, d := range defs {
		if d.Name != want[i] {
			t.Fatalf("defs[%d] = %q, want %q", i, d.Name, want[i])
		}
	}
}

// TestOverlay_GetFallsThroughToBase verifies Get returns (nil, false)
// when neither overlay nor Base knows the name.
func TestOverlay_GetFallsThroughToBase(t *testing.T) {
	overlay := &ChatToolOverlay{Base: stubBase{}}
	if _, ok := overlay.Get("missing"); ok {
		t.Fatal("Get(missing) returned ok=true, want false")
	}
}

// TestOverlay_CronDedupsInToolDefs verifies that when Base already
// exports a "cron" tool and the overlay also has Cron set, ToolDefs
// returns exactly one cron entry (the overlay's), preserving prompt
// cache stability. Cloudcat covered this incidentally via SendToAgent
// dedup tests, which were dropped in the felix port.
func TestOverlay_CronDedupsInToolDefs(t *testing.T) {
	cron := &tools.CronTool{}
	overlay := &ChatToolOverlay{
		Base: stubBase{defs: []llm.ToolDef{
			{Name: "cron", Description: "stale base cron"},
			{Name: "read_file"},
		}},
		Cron: cron,
	}
	defs := overlay.ToolDefs()
	want := []string{"cron", "read_file"}
	if len(defs) != len(want) {
		t.Fatalf("len(defs) = %d, want %d (%v)", len(defs), len(want), defs)
	}
	for i, d := range defs {
		if d.Name != want[i] {
			t.Fatalf("defs[%d] = %q, want %q", i, d.Name, want[i])
		}
	}
	// The cron entry must be the overlay's, not Base's stale def.
	if defs[0].Description == "stale base cron" {
		t.Fatal("ToolDefs returned Base's stale cron def, expected overlay's to win")
	}
	if defs[0].Description != cron.Description() {
		t.Fatalf("cron Description = %q, want overlay's %q", defs[0].Description, cron.Description())
	}
}

// TestOverlay_CronDedupsInNames verifies the same dedup in Names():
// when Base lists "cron" and the overlay also has Cron set, Names
// returns exactly one "cron" (alphabetically sorted with the rest).
func TestOverlay_CronDedupsInNames(t *testing.T) {
	overlay := &ChatToolOverlay{
		Base: stubBase{names: []string{"cron", "read_file"}},
		Cron: &tools.CronTool{},
	}
	names := overlay.Names()
	want := []string{"cron", "read_file"}
	if len(names) != len(want) {
		t.Fatalf("len(names) = %d, want %d (%v)", len(names), len(want), names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("names[%d] = %q, want %q", i, n, want[i])
		}
	}
}
