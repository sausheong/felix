// Package compaction wraps github.com/sausheong/harness/compaction with the
// Felix-specific BuildManager / Provider wiring (config-driven Manager
// instantiation, per-agent caching). Felix consumers continue to import
// "internal/compaction" for both the Manager type and the BuildManager
// helpers; the underlying implementation lives in harness.
package compaction

import harness "github.com/sausheong/harness/compaction"

// Type aliases — drop-in for pre-extraction types.
type (
	Manager    = harness.Manager
	Reason     = harness.Reason
	Result     = harness.Result
	Summarizer = harness.Summarizer
)

// Reason constants.
const (
	ReasonPreventive = harness.ReasonPreventive
	ReasonReactive   = harness.ReasonReactive
	ReasonManual     = harness.ReasonManual
)

// Limits / helpers.
const MaxConsecutiveFailures = harness.MaxConsecutiveFailures

var IsContextOverflow = harness.IsContextOverflow
