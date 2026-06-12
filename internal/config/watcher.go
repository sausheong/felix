package config

import (
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches the config file and calls a callback on changes.
type Watcher struct {
	path     string
	dir      string // parent directory actually registered with fsnotify
	name     string // base filename we filter events by
	callback func(*Config)
	watcher  *fsnotify.Watcher
	stop     chan struct{}
	once     sync.Once
}

// NewWatcher creates a new config file watcher.
//
// It watches the file's PARENT DIRECTORY (not the file inode) and filters
// events by basename. This is the standard fsnotify pattern for surviving
// atomic rename-replace saves: editors (vim, VS Code) and Felix's own
// Config.Save write a temp file and rename it over the target, which swaps the
// inode. A watch added directly on the file path is bound to the old inode and
// goes deaf after the first such save; a directory watch keeps firing because
// the directory inode is stable.
func NewWatcher(path string, callback func(*Config)) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := fw.Add(dir); err != nil {
		fw.Close()
		return nil, err
	}
	return &Watcher{
		path:     path,
		dir:      dir,
		name:     filepath.Base(path),
		callback: callback,
		watcher:  fw,
		stop:     make(chan struct{}),
	}, nil
}

// Start begins watching for file changes in a goroutine.
func (w *Watcher) Start() {
	go w.run()
}

func (w *Watcher) run() {
	// Debounce: editors often do rename+create or multiple writes
	var debounce *time.Timer
	// Ensure a pending debounce timer is stopped on exit so it can't fire a
	// reload after Stop() (and so the timer's resources are released).
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// We watch the parent directory, so filter to events for our
			// file. Create/Rename cover atomic rename-replace saves (the
			// temp file is renamed onto our name); Write covers in-place
			// edits.
			if filepath.Base(event.Name) != w.name {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(500*time.Millisecond, func() {
					w.reload()
				})
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("config watcher error", "error", err)
		case <-w.stop:
			return
		}
	}
}

func (w *Watcher) reload() {
	cfg, err := Load(w.path)
	if err != nil {
		slog.Error("failed to reload config", "error", err)
		return
	}
	slog.Info("config reloaded", "path", w.path)
	w.callback(cfg)
}

// Stop stops watching for changes.
func (w *Watcher) Stop() {
	w.once.Do(func() {
		close(w.stop)
		w.watcher.Close()
	})
}
