// Package tokens is a thin re-export shim over github.com/sausheong/harness/tokens.
// All Felix consumers continue to import this path; the real implementation
// lives in the harness module. Type aliases keep call-site shapes
// (tokens.Calibrator, tokens.Estimate, etc.) identical to pre-extraction.
package tokens

import (
	harness "github.com/sausheong/harness/tokens"
)

// Type aliases — drop-in replacements for the pre-extraction concrete types.
type (
	Calibrator      = harness.Calibrator
	CalibratorStore = harness.CalibratorStore
)

// Function re-exports.
var (
	NewCalibrator         = harness.NewCalibrator
	NewCalibratorStore    = harness.NewCalibratorStore
	Estimate              = harness.Estimate
	ContextWindow         = harness.ContextWindow
	ContextWindowFor      = harness.ContextWindowFor
	RegisterOllamaContext = harness.RegisterOllamaContext
	ResetOllamaContexts   = harness.ResetOllamaContexts
)
