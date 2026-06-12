package main

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"fyne.io/systray"

	"github.com/sausheong/felix/internal/config"
)

//go:embed icon.png
var iconBytes []byte

var (
	version = "dev"
	commit  = "none"

	// logFile is opened once in initLogFile() and serves as the
	// destination for both the menubar app's slog output and the
	// spawned gateway subprocess's stdout/stderr. Without a file the
	// .app bundle has no visible diagnostics — Cocoa apps don't get
	// stderr.
	logFile *os.File
)

// firstRun is set in main() before the data dir is created, so we
// can decide whether to land the user on Chat or Settings → Models.
var firstRun bool

func main() {
	if _, err := os.Stat(config.DefaultDataDir()); os.IsNotExist(err) {
		firstRun = true
	}

	initLogFile()
	if logFile != nil {
		// Write menubar-app slog output to the same file the gateway
		// subprocess writes to. The gateway maintains its own
		// LogBuffer for the /logs UI tab; this stream is the outer
		// "what the wrapper itself did" view.
		slog.SetDefault(slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}
	slog.Info("felix-app starting", "version", version, "commit", commit, "pid", os.Getpid())

	loadShellEnv()
	systray.Run(onReady, onQuit)
}

// initLogFile opens ~/.felix/felix-app.log in append mode so the
// menubar app's own logs and the gateway subprocess's stdout/stderr
// land in the same file. We do not call slog.SetDefault here — main()
// does it after this returns, once we know the file is good.
func initLogFile() {
	dir := config.DefaultDataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "felix-app.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	logFile = f
}

// loadShellEnv runs an interactive login shell to dump its
// environment, then sets any missing variables in the current
// process. macOS .app bundles don't inherit shell env vars, so API
// keys exported in the user's shell profile would otherwise be
// invisible to the spawned gateway subprocess (which inherits our
// environment via cmd.Env = os.Environ()).
func loadShellEnv() {
	if runtime.GOOS != "darwin" {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	out, err := exec.Command(shell, "-ilc", "env").Output()
	if err != nil {
		slog.Debug("failed to load shell env", "error", err)
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			continue
		}
		// PATH always overrides so Homebrew and user paths reach the
		// child. Other variables only fill in what's missing.
		if k == "PATH" || os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// onReady is invoked by systray on the macOS main thread once the
// menubar item is alive. It sets the icon, spawns the gateway
// subprocess, opens a browser when /health is healthy, and wires up
// the menu items. All blocking work (subprocess spawn, readiness
// poll) runs on this thread because systray expects it to return
// quickly — we keep it under a few seconds in the happy path.
func onReady() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in onReady", "panic", r)
			showError(fmt.Sprintf("Felix crashed: %v", r))
			systray.Quit()
		}
	}()

	icon := trayIcon(iconBytes)
	if runtime.GOOS == "darwin" {
		systray.SetTemplateIcon(icon, icon)
	} else {
		systray.SetIcon(icon)
	}
	systray.SetTooltip("Felix")

	// Spawn (or attach to) the gateway. Generous 90s timeout: bundled
	// Ollama startup can take ~60s on first launch when it has to
	// pull a model.
	ctx := context.Background()
	gw, err := startOrAttachGateway(ctx, logFile, 90*time.Second)
	if err != nil {
		slog.Error("failed to start gateway", "error", err)
		showError(fmt.Sprintf("Felix failed to start the gateway:\n\n%v", err))
		systray.Quit()
		return
	}

	port := gw.port
	landingPath := "/chat"
	if firstRun {
		landingPath = "/settings#models"
	}
	openURL(fmt.Sprintf("http://localhost:%d%s", port, landingPath))

	mChat := systray.AddMenuItem("Open Chat", "Open chat in browser")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Shut down and exit")

	quitCh := make(chan os.Signal, 1)
	signal.Notify(quitCh, syscall.SIGTERM, syscall.SIGINT)

	// Respawn-on-exit budget. Most cases: the gateway exits cleanly
	// because the user clicked Restart in the chat UI (/admin/restart),
	// so we relaunch with brief backoff. Pathological cases: a binary
	// that crashes immediately would hot-loop, so we cap at 3 exits
	// within respawnWindow and fall back to the error dialog. The
	// window resets every time the gateway stays up longer than it.
	const (
		maxRespawns    = 3
		respawnWindow  = 60 * time.Second
		respawnBackoff = 1 * time.Second
	)
	var respawnTimes []time.Time

	go func() {
		for {
			select {
			case <-mChat.ClickedCh:
				openURL(fmt.Sprintf("http://localhost:%d/chat", port))
			case <-mQuit.ClickedCh:
				shutdownAndExit(gw, "menu Quit clicked")
				return
			case sig := <-quitCh:
				slog.Warn("received termination signal",
					"signal", sig.String(),
					"ppid", os.Getppid())
				shutdownAndExit(gw, fmt.Sprintf("signal %s", sig))
				return
			case err := <-gw.exitCh:
				// Trim respawn history to the recent window so a
				// long-stable process isn't penalised by old exits.
				now := time.Now()
				kept := respawnTimes[:0]
				for _, t := range respawnTimes {
					if now.Sub(t) < respawnWindow {
						kept = append(kept, t)
					}
				}
				respawnTimes = kept

				if len(respawnTimes) >= maxRespawns {
					slog.Error("gateway exited too many times; not respawning",
						"error", err,
						"recent_exits", len(respawnTimes),
						"window_seconds", int(respawnWindow.Seconds()))
					showError("Felix's gateway process has exited several times in quick succession. Use Quit and relaunch.")
					gw = &gateway{port: port, owned: false, exitCh: noExitCh()}
					continue
				}

				slog.Warn("gateway exited; respawning", "error", err, "recent_exits", len(respawnTimes))
				respawnTimes = append(respawnTimes, now)
				time.Sleep(respawnBackoff)
				newGw, startErr := startOrAttachGateway(ctx, logFile, 90*time.Second)
				if startErr != nil {
					slog.Error("respawn failed", "error", startErr)
					showError(fmt.Sprintf("Felix could not restart the gateway:\n\n%v", startErr))
					gw = &gateway{port: port, owned: false, exitCh: noExitCh()}
					continue
				}
				gw = newGw
				slog.Info("gateway respawned", "port", gw.port)
			}
		}
	}()
}

// shutdownAndExit sends SIGTERM to the gateway subprocess and waits
// for it to drain (bounded inside gw.stop's 15s grace + SIGKILL).
// 25 s outer deadline gives gw.stop room for its full SIGTERM →
// SIGKILL cycle plus a small margin for systray.Quit. A hung
// subprocess can't trap the user — if we hit 25 s the menubar
// force-exits regardless.
func shutdownAndExit(gw *gateway, reason string) {
	slog.Info("shutting down", "reason", reason)
	go func() {
		time.Sleep(25 * time.Second)
		slog.Error("cleanup exceeded 25s, force-exiting")
		os.Exit(1)
	}()
	gw.stop()
	systray.Quit()
}

func onQuit() {
	os.Exit(0)
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		// rundll32 avoids the cmd /c start title-parsing issue with URLs.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		slog.Warn("unsupported OS for opening URL", "os", runtime.GOOS)
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Error("failed to open URL", "url", url, "error", err)
	}
}
