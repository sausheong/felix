package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTool_IsConcurrencySafe_Classifications locks in the per-tool
// classifications used by the Phase B parallel-dispatch partitioner.
//
// Read-only tools (read_file, web_fetch, web_search) declare themselves
// safe; everything else returns false. Changing a classification has
// concurrency-correctness implications — the partitioner will batch
// "safe" tools into a sync.WaitGroup + semaphore. Update with care.
//
// SendMessageTool and CronTool are constructed with nil dependencies —
// IsConcurrencySafe must not touch them, which is part of the
// "MUST NOT panic on weird input" interface contract.
func TestTool_IsConcurrencySafe_Classifications(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
		want bool
	}{
		{"read_file", &ReadFileTool{}, true},
		{"web_fetch", &WebFetchTool{}, true},
		{"web_search", &WebSearchTool{}, true},
		{"write_file", &WriteFileTool{}, false},
		{"edit_file", &EditFileTool{}, false},
		{"bash", &BashTool{}, false},
		{"browser", &BrowserTool{}, false},
		{"send_message", &SendMessageTool{}, false},
		{"cron", &CronTool{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.tool.IsConcurrencySafe(nil))
		})
	}
}
