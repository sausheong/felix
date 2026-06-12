package config

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatcherDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")

	// Write initial valid config
	initialConfig := `{
		"gateway": {"host": "127.0.0.1", "port": 18789},
		"agents": {"list": [{"id": "default", "name": "Test", "model": "openai/gpt-4o"}]}
	}`
	err := os.WriteFile(cfgPath, []byte(initialConfig), 0o644)
	require.NoError(t, err)

	var callbackFired atomic.Int32

	w, err := NewWatcher(cfgPath, func(cfg *Config) {
		callbackFired.Add(1)
	})
	require.NoError(t, err)

	w.Start()
	defer w.Stop()

	// Give the watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Modify the file
	updatedConfig := `{
		"gateway": {"host": "127.0.0.1", "port": 19000},
		"agents": {"list": [{"id": "default", "name": "Updated", "model": "openai/gpt-4o"}]}
	}`
	err = os.WriteFile(cfgPath, []byte(updatedConfig), 0o644)
	require.NoError(t, err)

	// Wait for debounce (500ms) + some buffer
	assert.Eventually(t, func() bool {
		return callbackFired.Load() > 0
	}, 3*time.Second, 100*time.Millisecond, "callback should fire after file change")
}

// TestWatcherSurvivesAtomicRename reproduces the R3 regression: an atomic
// rename-replace save (temp file renamed over the target) swaps the inode. A
// watcher bound to the file inode goes deaf after the first such save. Because
// the watcher now watches the parent directory and filters by basename, it must
// keep firing across repeated atomic replaces — including the SECOND one, which
// is what the old inode-watch missed.
func TestWatcherSurvivesAtomicRename(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")

	initialConfig := `{
		"gateway": {"host": "127.0.0.1", "port": 18789},
		"agents": {"list": [{"id": "default", "name": "Test", "model": "openai/gpt-4o"}]}
	}`
	require.NoError(t, os.WriteFile(cfgPath, []byte(initialConfig), 0o600))

	var callbackFired atomic.Int32
	w, err := NewWatcher(cfgPath, func(cfg *Config) {
		callbackFired.Add(1)
	})
	require.NoError(t, err)
	w.Start()
	defer w.Stop()

	time.Sleep(100 * time.Millisecond)

	// atomicReplace writes to a temp file in the same dir and renames it over
	// cfgPath — exactly what Config.Save (via WriteFileAtomic) and editors do.
	atomicReplace := func(port int) {
		body := `{
			"gateway": {"host": "127.0.0.1", "port": ` + itoa(port) + `},
			"agents": {"list": [{"id": "default", "name": "Test", "model": "openai/gpt-4o"}]}
		}`
		tmp := filepath.Join(dir, ".felix.json5.tmp")
		require.NoError(t, os.WriteFile(tmp, []byte(body), 0o600))
		require.NoError(t, os.Rename(tmp, cfgPath))
	}

	// First atomic replace.
	atomicReplace(19000)
	require.Eventually(t, func() bool {
		return callbackFired.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "callback should fire after first atomic replace")

	after1 := callbackFired.Load()

	// Second atomic replace — the inode has changed again. This is the case
	// the old file-inode watch missed entirely.
	atomicReplace(19001)
	require.Eventually(t, func() bool {
		return callbackFired.Load() > after1
	}, 3*time.Second, 50*time.Millisecond, "callback should fire again after SECOND atomic replace")
}

// itoa is a tiny local int->string to avoid importing strconv just for the test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestWatcherStopDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")

	initialConfig := `{
		"gateway": {"host": "127.0.0.1", "port": 18789},
		"agents": {"list": [{"id": "default", "name": "Test", "model": "openai/gpt-4o"}]}
	}`
	err := os.WriteFile(cfgPath, []byte(initialConfig), 0o644)
	require.NoError(t, err)

	w, err := NewWatcher(cfgPath, func(cfg *Config) {})
	require.NoError(t, err)

	w.Start()

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop returned promptly
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() hung for more than 3 seconds")
	}
}
