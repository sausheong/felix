// Package llmtest re-exports the harness llmtest helpers so Felix tests
// importing "internal/llm/llmtest" continue to compile after the harness
// extraction. New code should prefer importing the harness package directly.
package llmtest

import "github.com/sausheong/harness/llm/llmtest"

type Base = llmtest.Base
