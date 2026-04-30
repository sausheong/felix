package agent

import "testing"

func TestStreamingToolsEnabled_Default(t *testing.T) {
	t.Setenv("FELIX_STREAMING_TOOLS", "")
	if streamingToolsEnabled() {
		t.Fatal("expected false when env unset")
	}
}

func TestStreamingToolsEnabled_Override(t *testing.T) {
	t.Setenv("FELIX_STREAMING_TOOLS", "1")
	if !streamingToolsEnabled() {
		t.Fatal("expected true when env=1")
	}
}

func TestStreamingToolsEnabled_InvalidFallsBack(t *testing.T) {
	cases := []string{"0", "true", "True", "garbage", " 1 ", "01", "yes"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			t.Setenv("FELIX_STREAMING_TOOLS", v)
			if streamingToolsEnabled() {
				t.Fatalf("expected false for %q", v)
			}
		})
	}
}
