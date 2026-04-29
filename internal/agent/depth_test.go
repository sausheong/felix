package agent

import (
	"testing"
)

func TestMaxAgentDepth_Default(t *testing.T) {
	t.Setenv("FELIX_MAX_AGENT_DEPTH", "")
	if got := maxAgentDepth(); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
}

func TestMaxAgentDepth_EnvOverride(t *testing.T) {
	t.Setenv("FELIX_MAX_AGENT_DEPTH", "5")
	if got := maxAgentDepth(); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

func TestMaxAgentDepth_InvalidFallsBack(t *testing.T) {
	cases := []string{"garbage", "0", "-1", "1.5", " 3 "}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			t.Setenv("FELIX_MAX_AGENT_DEPTH", v)
			if got := maxAgentDepth(); got != 3 {
				t.Fatalf("env=%q: got %d, want 3", v, got)
			}
		})
	}
}
