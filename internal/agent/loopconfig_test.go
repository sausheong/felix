package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Felix overrides the harness's conservative 4000-char tool-result cap:
// engineering agents read source files and run tests, and a 4K cap
// causes constant spill/truncation churn that burns the turn budget.
func TestEffectiveMaxToolResultLen_DefaultIs64K(t *testing.T) {
	assert.Equal(t, 65536, effectiveMaxToolResultLen(0),
		"unset config must default to 64K, not the harness 4000")
}

func TestEffectiveMaxToolResultLen_ExplicitConfigWins(t *testing.T) {
	assert.Equal(t, 16000, effectiveMaxToolResultLen(16000))
}
