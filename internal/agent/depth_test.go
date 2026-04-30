package agent

import (
	"testing"

	"github.com/sausheong/felix/internal/config"
)

func TestMaxAgentDepth_Default(t *testing.T) {
	t.Setenv("FELIX_MAX_AGENT_DEPTH", "")
	if got := (&Runtime{}).maxAgentDepth(); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestMaxAgentDepth_EnvOverride(t *testing.T) {
	t.Setenv("FELIX_MAX_AGENT_DEPTH", "5")
	if got := (&Runtime{}).maxAgentDepth(); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestMaxAgentDepth_InvalidFallsBack(t *testing.T) {
	cases := []string{"garbage", "0", "-1", "1.5", " 3 "}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			t.Setenv("FELIX_MAX_AGENT_DEPTH", v)
			if got := (&Runtime{}).maxAgentDepth(); got != 3 {
				t.Fatalf("env=%q: got %d, want 3", v, got)
			}
		})
	}
}

func TestRuntime_MaxAgentDepth_ConfigWinsOverEnv(t *testing.T) {
	t.Setenv("FELIX_MAX_AGENT_DEPTH", "7")
	r := &Runtime{AgentLoop: config.AgentLoopConfig{MaxAgentDepth: 5}}
	if got := r.maxAgentDepth(); got != 5 {
		t.Errorf("got %d, want 5 (config wins)", got)
	}
}

func TestRuntime_MaxAgentDepth_EnvWhenConfigZero(t *testing.T) {
	t.Setenv("FELIX_MAX_AGENT_DEPTH", "7")
	r := &Runtime{AgentLoop: config.AgentLoopConfig{}}
	if got := r.maxAgentDepth(); got != 7 {
		t.Errorf("got %d, want 7 (env when config 0)", got)
	}
}

func TestRuntime_MaxAgentDepth_DefaultWhenBothUnset(t *testing.T) {
	t.Setenv("FELIX_MAX_AGENT_DEPTH", "")
	r := &Runtime{AgentLoop: config.AgentLoopConfig{}}
	if got := r.maxAgentDepth(); got != 3 {
		t.Errorf("got %d, want 3 (default)", got)
	}
}

func TestRuntime_MaxAgentDepth_ConfigZeroOrNegativeFallsBackToEnv(t *testing.T) {
	// 0 in config means "use fallback"; negative is treated the same.
	for _, v := range []int{0, -1, -10} {
		t.Setenv("FELIX_MAX_AGENT_DEPTH", "9")
		r := &Runtime{AgentLoop: config.AgentLoopConfig{MaxAgentDepth: v}}
		if got := r.maxAgentDepth(); got != 9 {
			t.Errorf("config=%d: got %d, want 9 (env fallback)", v, got)
		}
	}
}
