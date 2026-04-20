//go:build darwin || linux

package local

import (
	"fmt"
	"syscall"
)

// EnsureFreeSpace returns an error if the filesystem containing path has
// less than wantBytes available.
func EnsureFreeSpace(path string, wantBytes int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("statfs %q: %w", path, err)
	}
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	if avail < wantBytes {
		return fmt.Errorf("insufficient disk: need %d bytes, have %d at %s", wantBytes, avail, path)
	}
	return nil
}
