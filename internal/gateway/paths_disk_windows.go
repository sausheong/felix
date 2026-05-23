//go:build windows

package gateway

// diskUsageOK on Windows always permits the write. A proper implementation
// would use syscall.GetDiskFreeSpaceEx — left for a follow-up.
func diskUsageOK(_ string, _ int64) (bool, error) {
	return true, nil
}
