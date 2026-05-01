package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- 6a: subagent context inheritance ---

func TestInheritParentHistoryCopiesViewIntoSubagentSession(t *testing.T) {
	parent := session.NewSession("parent", "key")
	parent.Append(session.UserMessageEntry("first user msg"))
	parent.Append(session.AssistantMessageEntry("first reply"))
	parent.Append(session.UserMessageEntry("second user msg"))

	sub := NewSubagentSession("sub")
	inheritParentHistory(sub, parent)

	subView := sub.View()
	require.Len(t, subView, 3, "all 3 parent entries must land in subagent")

	// Order preserved.
	var got []string
	for _, e := range subView {
		var data session.MessageData
		require.NoError(t, decodeMessage(e, &data))
		got = append(got, data.Text)
	}
	assert.Equal(t, []string{"first user msg", "first reply", "second user msg"}, got)
}

func TestInheritParentHistoryWalksFromInheritedLeaf(t *testing.T) {
	// The first inherited entry must lose its ParentID — otherwise the
	// subagent's empty leafID lets Append leave a dangling pointer to a
	// parent entry that doesn't exist in the subagent's entryMap, and
	// View() walking back from that leaf either short-circuits early
	// or includes nothing.
	parent := session.NewSession("parent", "key")
	parent.Append(session.UserMessageEntry("u1"))
	parent.Append(session.AssistantMessageEntry("a1"))

	sub := NewSubagentSession("sub")
	inheritParentHistory(sub, parent)

	// View walks back from leaf via ParentID; we must reach BOTH entries.
	require.Len(t, sub.View(), 2, "View must traverse all inherited entries")
}

func TestInheritParentHistoryEmptyParentIsNoOp(t *testing.T) {
	parent := session.NewSession("parent", "key") // no entries
	sub := NewSubagentSession("sub")
	inheritParentHistory(sub, parent)
	assert.Empty(t, sub.View())
}

func TestSubagentFactoryInheritsContextWhenFlagSet(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "default", Name: "P", Model: "test/p", Workspace: t.TempDir(), MaxTurns: 5},
				{
					ID:             "researcher",
					Name:           "R",
					Model:          "test/r",
					Workspace:      t.TempDir(),
					MaxTurns:       5,
					Subagent:       true,
					Description:    "researches",
					InheritContext: true,
				},
			},
		},
	}
	parent := &Runtime{
		LLM:     &scriptedTextLLM{text: "p"},
		Session: session.NewSession("default", "test"),
		AgentID: "default",
		Model:   "p",
	}
	parent.Session.Append(session.UserMessageEntry("ORIGINAL_USER_MSG"))
	parent.Session.Append(session.AssistantMessageEntry("ORIGINAL_REPLY"))

	subLLM := &scriptedTextLLM{text: "ok"}
	factory := MakeSubagentFactory(cfg, RuntimeDeps{}, subagentBuilderForLLM(subLLM), parent)

	runner, err := factory(context.Background(), "researcher", 0)
	require.NoError(t, err)

	// Pull the subagent's runtime out via the adapter to inspect its
	// pre-populated session before the run advances the chain.
	rt := runner.(*subagentRunnerAdapter).rt
	view := rt.Session.View()
	require.Len(t, view, 2, "InheritContext must copy parent's 2 entries")
	var first session.MessageData
	require.NoError(t, decodeMessage(view[0], &first))
	assert.Equal(t, "ORIGINAL_USER_MSG", first.Text)
}

func TestSubagentFactoryDoesNotInheritWhenFlagUnset(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "default", Name: "P", Model: "test/p", Workspace: t.TempDir(), MaxTurns: 5},
				{
					ID:          "researcher",
					Name:        "R",
					Model:       "test/r",
					Workspace:   t.TempDir(),
					MaxTurns:    5,
					Subagent:    true,
					Description: "researches",
					// InheritContext: false (default)
				},
			},
		},
	}
	parent := &Runtime{
		LLM:     &scriptedTextLLM{text: "p"},
		Session: session.NewSession("default", "test"),
		AgentID: "default",
		Model:   "p",
	}
	parent.Session.Append(session.UserMessageEntry("ORIGINAL_USER_MSG"))

	factory := MakeSubagentFactory(cfg, RuntimeDeps{}, subagentBuilderForLLM(&scriptedTextLLM{text: "ok"}), parent)
	runner, err := factory(context.Background(), "researcher", 0)
	require.NoError(t, err)
	rt := runner.(*subagentRunnerAdapter).rt
	assert.Empty(t, rt.Session.View(), "subagent must start with empty session when InheritContext is false")
}

// --- 6b: provider fallback model ---

// retryableThenSuccessProvider returns IsRetryableModelError on the first
// call and a working stream on the second. Used to verify the runtime's
// fallback retry flips req.Model and re-invokes ChatStream.
type retryableThenSuccessProvider struct {
	llmtest.Base
	calls       int
	modelsSeen  []string
	finalText   string
}

func (p *retryableThenSuccessProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.calls++
	p.modelsSeen = append(p.modelsSeen, req.Model)
	if p.calls == 1 {
		// errors.New so the runtime can call .Error() safely. The
		// substring classifier in IsRetryableModelError catches "529"
		// and "overloaded" — exercises the same retry path as a typed
		// anthropic.Error{StatusCode:529} would, without the SDK
		// internals's panic on partially-constructed Error structs.
		return nil, errors.New("anthropic 529 overloaded — try again")
	}
	ch := make(chan llm.ChatEvent, 4)
	ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.finalText}
	ch <- llm.ChatEvent{Type: llm.EventDone}
	close(ch)
	return ch, nil
}

func TestRuntimeFallbackModelRetriesOnRetryableError(t *testing.T) {
	prov := &retryableThenSuccessProvider{finalText: "fallback won"}
	rt := &Runtime{
		LLM:           prov,
		Tools:         tools.NewRegistry(),
		Session:       session.NewSession("a", "k"),
		AgentID:       "a",
		Model:         "claude-sonnet-4-5",
		FallbackModel: "claude-haiku-3-5",
		Provider:      "anthropic",
		MaxTurns:      2,
		Workspace:     t.TempDir(),
	}
	out, err := rt.RunSync(context.Background(), "do it", nil)
	require.NoError(t, err)
	assert.Equal(t, "fallback won", out)
	require.Equal(t, 2, prov.calls, "primary call + fallback retry = 2 calls")
	assert.Equal(t, "claude-sonnet-4-5", prov.modelsSeen[0])
	assert.Equal(t, "claude-haiku-3-5", prov.modelsSeen[1])
}

// nonRetryableProvider always returns a non-retryable error. The runtime
// must NOT engage the fallback (otherwise misconfigurations get masked).
type nonRetryableProvider struct {
	llmtest.Base
	calls int
}

func (p *nonRetryableProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.calls++
	return nil, errors.New("invalid api key")
}

func TestRuntimeFallbackModelDoesNotEngageOnNonRetryableError(t *testing.T) {
	prov := &nonRetryableProvider{}
	rt := &Runtime{
		LLM:           prov,
		Tools:         tools.NewRegistry(),
		Session:       session.NewSession("a", "k"),
		AgentID:       "a",
		Model:         "claude-sonnet-4-5",
		FallbackModel: "claude-haiku-3-5",
		Provider:      "anthropic",
		MaxTurns:      2,
		Workspace:     t.TempDir(),
	}
	_, err := rt.RunSync(context.Background(), "do it", nil)
	require.Error(t, err, "non-retryable error must surface to caller")
	assert.Equal(t, 1, prov.calls, "no retry on non-retryable error")
}

func TestRuntimeFallbackModelNoFallbackConfigured(t *testing.T) {
	prov := &retryableThenSuccessProvider{finalText: "should not reach"}
	rt := &Runtime{
		LLM:       prov,
		Tools:     tools.NewRegistry(),
		Session:   session.NewSession("a", "k"),
		AgentID:   "a",
		Model:     "claude-sonnet-4-5",
		// FallbackModel: "" — fallback disabled
		Provider:  "anthropic",
		MaxTurns:  2,
		Workspace: t.TempDir(),
	}
	_, err := rt.RunSync(context.Background(), "do it", nil)
	require.Error(t, err, "with no fallback configured the retryable error still fails")
	assert.Equal(t, 1, prov.calls, "no retry without configured fallback")
}

func TestBuildRuntimeForAgentStripsProviderPrefixFromFallbackModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:            "a",
			Name:          "A",
			Model:         "anthropic/claude-sonnet-4-5",
			FallbackModel: "anthropic/claude-haiku-3-5",
			Workspace:     t.TempDir(),
			MaxTurns:      5,
		},
	}
	rt, err := BuildRuntimeForAgent(
		RuntimeDeps{Config: cfg},
		RuntimeInputs{
			Provider: &scriptedTextLLM{text: "ok"},
			Tools:    tools.NewRegistry(),
			Session:  session.NewSession("a", "k"),
		},
		&cfg.Agents.List[0],
	)
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-3-5", rt.FallbackModel,
		"provider prefix must be stripped — Runtime.LLM is bound to a single provider")
}

func TestBuildRuntimeForAgentRejectsCrossProviderFallback(t *testing.T) {
	// Cross-provider fallback (anthropic primary, openai fallback) is
	// nonsense given Runtime.LLM holds one provider client. Discard
	// rather than silently swap to a model the wrong client can't
	// route, and log a warning so the misconfig is visible.
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:            "a",
			Name:          "A",
			Model:         "anthropic/claude-sonnet-4-5",
			FallbackModel: "openai/gpt-4o-mini",
			Workspace:     t.TempDir(),
			MaxTurns:      5,
		},
	}
	rt, err := BuildRuntimeForAgent(
		RuntimeDeps{Config: cfg},
		RuntimeInputs{
			Provider: &scriptedTextLLM{text: "ok"},
			Tools:    tools.NewRegistry(),
			Session:  session.NewSession("a", "k"),
		},
		&cfg.Agents.List[0],
	)
	require.NoError(t, err)
	assert.Equal(t, "", rt.FallbackModel, "cross-provider fallback must be discarded")
}

// decodeMessage unmarshals a session entry's MessageData payload.
func decodeMessage(e session.SessionEntry, dst *session.MessageData) error {
	return json.Unmarshal(e.Data, dst)
}
