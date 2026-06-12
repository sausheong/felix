package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMinimalBaseEnvExcludesSecrets(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"TZ=UTC",
		"FELIX_SECRET=topsecret",
		"OPENAI_API_KEY=sk-xyz",
		"OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer z",
	}
	got := minimalBaseEnv(parent)
	asMap := map[string]bool{}
	for _, kv := range got {
		asMap[kv] = true
	}
	require.True(t, asMap["PATH=/usr/bin"])
	require.True(t, asMap["HOME=/home/u"])
	require.True(t, asMap["LANG=en_US.UTF-8"])
	require.True(t, asMap["LC_ALL=C"])
	require.True(t, asMap["TZ=UTC"])
	require.False(t, asMap["FELIX_SECRET=topsecret"])
	require.False(t, asMap["OPENAI_API_KEY=sk-xyz"])
	require.False(t, asMap["OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer z"])
}

func TestStdioEnvForUsesMinimalByDefault(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "SECRET=x"}
	overrides := map[string]string{"FOO": "bar"}

	minimal := stdioEnvFor(parent, overrides, false)
	mm := map[string]bool{}
	for _, kv := range minimal {
		mm[kv] = true
	}
	require.True(t, mm["PATH=/usr/bin"])
	require.True(t, mm["FOO=bar"])
	require.False(t, mm["SECRET=x"])

	full := stdioEnvFor(parent, overrides, true)
	fm := map[string]bool{}
	for _, kv := range full {
		fm[kv] = true
	}
	require.True(t, fm["SECRET=x"])
	require.True(t, fm["FOO=bar"])
}
