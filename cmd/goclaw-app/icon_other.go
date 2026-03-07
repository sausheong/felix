//go:build !windows

package main

// trayIcon returns the icon data as-is on non-Windows platforms.
func trayIcon(pngData []byte) []byte {
	return pngData
}
