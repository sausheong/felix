package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSupervisor_StartAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "engine.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/bash\nsleep 30\n"), 0o755)

	sup := NewSupervisor(SupervisorOptions{
		EnginePath:  scriptPath,
		ModelPath:   "/dev/null",
		MMProjPath:  "/dev/null",
		Port:        0,
		ContextSize: 65536,
		GPULayers:   999,
	})
	ctx := context.Background()

	err := sup.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if !sup.IsRunning() {
		t.Fatal("supervisor should be running after Start")
	}

	sup.Stop()

	if sup.IsRunning() {
		t.Fatal("supervisor should be stopped after Stop")
	}
}

func TestSupervisor_CrashRestartBackoff(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "crasher.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 1\n"), 0o755)

	sup := NewSupervisor(SupervisorOptions{
		EnginePath:  scriptPath,
		ModelPath:   "/dev/null",
		MMProjPath:  "/dev/null",
		Port:        0,
		ContextSize: 65536,
		GPULayers:   999,
	})

	ctx := context.Background()
	sup.Start(ctx)
	time.Sleep(3 * time.Second)
	sup.Stop()
}

func TestSupervisor_Port(t *testing.T) {
	sup := NewSupervisor(SupervisorOptions{
		EnginePath:  "echo",
		ModelPath:   "/dev/null",
		MMProjPath:  "/dev/null",
		Port:        18790,
		ContextSize: 65536,
		GPULayers:   999,
	})
	if sup.Port() != 18790 {
		t.Errorf("expected default port 18790, got %d", sup.Port())
	}
}
