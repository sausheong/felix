//go:build windows

package local

import (
	"os/exec"
	"syscall"
)

// setProcAttr is a no-op on Windows. Process-group semantics differ enough
// (Job Objects) that the cleanest path is to leave Felix's existing
// per-process kill in place there. Felix doesn't ship Windows binaries
// today; the stub keeps the package buildable for cross-compile.
func setProcAttr(cmd *exec.Cmd) {}

// signalGroup just signals the leader on Windows. exec.Cmd.Process.Kill()
// is the only portable way to terminate a process from a sibling on
// Windows; the syscall.Signal argument is ignored unless it's syscall.SIGKILL.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if sig == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(sig)
}
