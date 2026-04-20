// Package local manages the bundled Ollama child process and models.
package local

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrBinaryNotFound is returned when the ollama binary cannot be located.
var ErrBinaryNotFound = errors.New("ollama binary not found")

// Discover returns the absolute path to the ollama binary.
//
// Search order:
//  1. $FELIX_OLLAMA_BIN (env override, for dev/testing)
//  2. <felixBinDir>/bin/ollama(.exe) — sibling to felix in unpacked zips
//  3. macOS app bundle: <felixBinDir>/../Resources/bin/ollama
//  4. PATH lookup (last resort, dev convenience)
//
// felixBinDir should be the directory containing the running felix binary.
func Discover(felixBinDir string) (string, error) {
	exe := "ollama"
	if runtime.GOOS == "windows" {
		exe = "ollama.exe"
	}

	if env := os.Getenv("FELIX_OLLAMA_BIN"); env != "" {
		if isExec(env) {
			return env, nil
		}
		return "", fmt.Errorf("%w: FELIX_OLLAMA_BIN=%s is not executable", ErrBinaryNotFound, env)
	}

	candidates := []string{
		filepath.Join(felixBinDir, "bin", exe),
	}
	if runtime.GOOS == "darwin" {
		// felix CLI inside Felix.app/Contents/MacOS — bundle is at ../Resources/bin
		candidates = append(candidates, filepath.Join(felixBinDir, "..", "Resources", "bin", exe))
	}

	for _, c := range candidates {
		if isExec(c) {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs, nil
			}
			return c, nil
		}
	}

	if path, err := exec.LookPath(exe); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("%w: searched env, %v, PATH", ErrBinaryNotFound, candidates)
}

func isExec(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // Windows has no exec bit; trust the .exe
	}
	return fi.Mode().Perm()&0o111 != 0
}
