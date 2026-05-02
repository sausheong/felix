package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/sausheong/felix/internal/startup"
)

//go:embed icon.png
var iconBytes []byte

var (
	version = "dev"
	commit  = "none"

	// logFile is opened once in initLogFile() and passed into startup.Options
	// so the gateway's LogBuffer wraps a TextHandler writing here. Without
	// this, startup.go's slog.SetDefault would silently override the file
	// handler with one writing to os.Stderr (which is /dev/null in a .app
	// bundle), leaving felix-app.log empty.
	logFile *os.File
)

// firstRun is set in main() before the data dir is created, so onReady()
// can decide whether to land the user on Chat or Settings → Models.
var firstRun bool

func main() {
	// Detect first run BEFORE initLogFile() creates the data dir.
	if _, err := os.Stat(config.DefaultDataDir()); os.IsNotExist(err) {
		firstRun = true
	}

	// Write logs to a file so crashes are diagnosable (macOS .app has no stderr,
	// Windows GUI apps have no console).
	initLogFile()

	// macOS .app bundles don't inherit shell env vars (e.g. API keys).
	// Source the user's shell profile to pick them up.
	loadShellEnv()
	systray.Run(onReady, onQuit)
}

// initLogFile opens the log file in the data directory and stashes the
// handle in package-global logFile. We DO NOT call slog.SetDefault here:
// startup.StartGateway also installs a slog default (its LogBuffer that
// powers the /logs UI tab), and whichever runs last wins. To keep both
// the file and the in-memory buffer working, the file is passed into
// startup.Options.LogWriter and startup wires the LogBuffer over it.
func initLogFile() {
	dir := config.DefaultDataDir()
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "felix-app.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	logFile = f
}

// loadShellEnv runs an interactive login shell to dump its environment,
// then sets any missing variables in the current process.
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
		// Always override PATH so Homebrew/user paths are available.
		// For other vars, only set if not already present.
		if k == "PATH" || os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func onReady() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in onReady", "error", r)
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

	// Start gateway in the background. Pass the log file (opened in
	// initLogFile) so the LogBuffer wraps a TextHandler writing to it.
	result, err := startup.StartGateway("", version, startup.Options{LogWriter: logFile})
	if err != nil {
		slog.Error("failed to start gateway", "error", err)
		showError(fmt.Sprintf("Felix failed to start:\n\n%v\n\nCheck config at:\n%s", err, config.DefaultConfigPath()))
		systray.Quit()
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in gateway server", "error", r)
			}
		}()
		if err := result.Server.Start(); err != nil && err != http.ErrServerClosed {
			slog.Error("gateway error", "error", err)
		}
	}()

	port := result.Config.Gateway.Port
	if port == 0 {
		port = 18789
	}

	// Auto-open the browser. First run lands on Settings → Models so the user
	// can pull a local model immediately; subsequent runs land on Chat.
	landingPath := "/chat"
	if firstRun {
		landingPath = "/settings#models"
	}
	openURL("http://localhost:" + itoa(port) + landingPath)

	// Menu items
	mChat := systray.AddMenuItem("Chat", "Open chat in browser")
	mJobs := systray.AddMenuItem("Jobs", "Open jobs in browser")
	mLogs := systray.AddMenuItem("Logs", "Open logs in browser")
	mSettings := systray.AddMenuItem("Settings", "Open settings in browser")
	mRestart := systray.AddMenuItem("Restart", "Restart the gateway")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Shut down and exit")

	// Posted to by SIGTERM/SIGINT handler so terminal-driven kills run
	// the same Cleanup → systray.Quit chain as the Quit menu item.
	// Without this, signals bypass cleanup and orphan the bundled Ollama
	// supervisor + its runner children.
	quitCh := make(chan os.Signal, 1)
	signal.Notify(quitCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		for {
			select {
			case <-mChat.ClickedCh:
				openURL("http://localhost:" + itoa(port) + "/chat")
			case <-mJobs.ClickedCh:
				openURL("http://localhost:" + itoa(port) + "/jobs")
			case <-mLogs.ClickedCh:
				openURL("http://localhost:" + itoa(port) + "/logs")
			case <-mSettings.ClickedCh:
				openURL("http://localhost:" + itoa(port) + "/settings")
			case <-mRestart.ClickedCh:
				slog.Info("restarting gateway")
				result.Cleanup()
				newResult, err := startup.StartGateway("", version, startup.Options{LogWriter: logFile})
				if err != nil {
					slog.Error("failed to restart gateway", "error", err)
					continue
				}
				go func() {
					defer func() {
						if r := recover(); r != nil {
							slog.Error("panic in gateway server", "error", r)
						}
					}()
					if err := newResult.Server.Start(); err != nil && err != http.ErrServerClosed {
						slog.Error("gateway error", "error", err)
					}
				}()
				result = newResult
				port = newResult.Config.Gateway.Port
				if port == 0 {
					port = 18789
				}
				slog.Info("gateway restarted", "port", port)
			case <-mQuit.ClickedCh:
				shutdownAndExit(result, "menu Quit clicked")
				return
			case sig := <-quitCh:
				// Log the signal as a top-level WARN before cleanup so it's
				// the first thing visible in felix-app.log if macOS reaps
				// us. ppid points back at launchd / ControlCenter / shell.
				slog.Warn("received termination signal",
					"signal", sig.String(),
					"ppid", os.Getppid())
				shutdownAndExit(result, fmt.Sprintf("signal %s", sig))
				return
			}
		}
	}()
}

// shutdownAndExit runs the gateway cleanup chain with a hard 5s deadline,
// then asks systray to exit. macOS Quit (and signal-driven kills) should
// always finish within seconds — a hung MCP server, cortex, or Ollama
// supervisor can't be allowed to trap the user.
func shutdownAndExit(result *startup.Result, reason string) {
	slog.Info("shutting down", "reason", reason)
	go func() {
		time.Sleep(5 * time.Second)
		slog.Error("cleanup exceeded 5s, force-exiting")
		os.Exit(1)
	}()
	result.Cleanup()
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
		// Use rundll32 to avoid cmd /c start title-parsing issues with URLs
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		slog.Warn("unsupported OS for opening URL", "os", runtime.GOOS)
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Error("failed to open URL", "url", url, "error", err)
	}
}

func openFile(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		slog.Warn("unsupported OS for opening file", "os", runtime.GOOS)
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Error("failed to open file", "path", path, "error", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
