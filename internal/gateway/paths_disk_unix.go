//go:build darwin || linux

package gateway

import "syscall"

// diskUsageOK reports whether writing addBytes more bytes to the filesystem
// containing path would keep total usage under 80% capacity.
func diskUsageOK(path string, addBytes int64) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, err
	}
	total := int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	if total == 0 {
		return true, nil
	}
	used := total - free
	projected := used + addBytes
	return float64(projected)/float64(total) < 0.80, nil
}
