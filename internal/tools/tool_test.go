package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubTool is a minimal Tool implementation for ordering tests.
type stubTool struct {
	name string
}

func (s *stubTool) Name() string                              { return s.name }
func (s *stubTool) Description() string                       { return s.name + " description" }
func (s *stubTool) Parameters() json.RawMessage               { return json.RawMessage(`{}`) }
func (s *stubTool) IsConcurrencySafe(_ json.RawMessage) bool  { return false }
func (s *stubTool) Execute(ctx context.Context, in json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

// TestToolDefsAreDeterministic guards against map-iteration cache breakage.
// Tool definitions feed directly into the LLM prefix; nondeterministic order
// invalidates the prompt cache on every turn — both a 10× cost regression
// and a real correctness issue when summaries depend on stable history.
func TestToolDefsAreDeterministic(t *testing.T) {
	reg := NewRegistry()
	for _, name := range []string{"zebra", "alpha", "mango", "banana", "kiwi"} {
		reg.Register(&stubTool{name: name})
	}

	first := reg.ToolDefs()
	for i := 0; i < 50; i++ {
		got := reg.ToolDefs()
		assert.Equal(t, len(first), len(got), "length must be stable")
		for j := range first {
			assert.Equal(t, first[j].Name, got[j].Name,
				"position %d must be stable across calls (iteration %d)", j, i)
		}
	}

	for i := 1; i < len(first); i++ {
		assert.LessOrEqual(t, first[i-1].Name, first[i].Name,
			"ToolDefs must return tools sorted by name")
	}
}

func TestNamesAreDeterministic(t *testing.T) {
	reg := NewRegistry()
	for _, name := range []string{"zebra", "alpha", "mango"} {
		reg.Register(&stubTool{name: name})
	}

	got := reg.Names()
	assert.Equal(t, []string{"alpha", "mango", "zebra"}, got,
		"Names() must return tools sorted by name")
}
