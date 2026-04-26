//go:build !windows

package local

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the spawned ollama in its own process group so we can
// signal the entire group on shutdown. Without this, `ollama serve` survives
// signals fine but its `ollama runner` child processes (one per loaded model)
// outlive the parent — they inherit the parent's stdout/stderr pipes, which
// keeps cmd.Wait() blocked forever waiting for those FDs to close, which
// keeps Stop() blocked forever, which trips the menu-bar Quit watchdog.
func setProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalGroup sends sig to every process in the spawned process group
// (negative pid = group). Falls back to leader-only if Getpgid fails.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Signal(sig)
	}
	return syscall.Kill(-pgid, sig)
}
