package local

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ModelPaths holds resolved paths to model files.
type ModelPaths struct {
	ModelPath  string // path to model.gguf
	MMProjPath string // path to mmproj.gguf
	EnginePath string // path to llamafile binary
	SearchRoot string // directory containing model.gguf
}

// ResolveModelPaths searches for bundled model files in order:
// 1. explicitDir (env override)
// 2. ~/.felix/models/gemma-4-e4b/
// 3. <binary-dir>/models/gemma-4-e4b/
// 4. /usr/local/share/felix/models/gemma-4-e4b/ (macOS)
// Returns the first directory that contains both model.gguf and mmproj.gguf.
func ResolveModelPaths(explicitDir, dataDir string) (*ModelPaths, error) {
	candidates := []string{}

	if explicitDir != "" {
		candidates = append(candidates, explicitDir)
	}
	if dataDir != "" {
		candidates = append(candidates, filepath.Join(dataDir, "models", "gemma-4-e4b"))
	}

	execPath, err := os.Executable()
	if err == nil {
		binDir := filepath.Dir(execPath)
		if strings.Contains(binDir, "Contents/MacOS") {
			appRoot := strings.Split(binDir, "Contents/MacOS")[0]
			candidates = append(candidates, filepath.Join(appRoot, "Contents", "Resources", "models", "gemma-4-e4b"))
		}
		candidates = append(candidates, filepath.Join(binDir, "models", "gemma-4-e4b"))
	}

	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/usr/local/share/felix/models/gemma-4-e4b")
	}

	for _, dir := range candidates {
		model := filepath.Join(dir, "model.gguf")
		mmproj := filepath.Join(dir, "mmproj.gguf")
		if _, err := os.Stat(model); err == nil {
			if _, err := os.Stat(mmproj); err == nil {
				engine := resolveEnginePath()
				return &ModelPaths{
					ModelPath:  model,
					MMProjPath: mmproj,
					EnginePath: engine,
					SearchRoot: dir,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("local model files not found (searched %d locations)", len(candidates))
}

// VerifySHA256 checks that model.gguf and mmproj.gguf match SHA256SUMS.
func (m *ModelPaths) VerifySHA256(modelDir string) error {
	sumsPath := filepath.Join(modelDir, "SHA256SUMS")
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		return fmt.Errorf("read SHA256SUMS: %w", err)
	}

	hashes := parseSHA256SUMS(string(data))
	if len(hashes) == 0 {
		return fmt.Errorf("no hashes found in SHA256SUMS")
	}

	for filename, expectedHash := range hashes {
		filePath := filepath.Join(modelDir, filename)
		actual, err := sha256File(filePath)
		if err != nil {
			return fmt.Errorf("hash %s: %w", filename, err)
		}
		if actual != expectedHash {
			return fmt.Errorf("SHA256 mismatch for %s: expected %s, got %s",
				filename, expectedHash, actual)
		}
	}

	return nil
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func parseSHA256SUMS(content string) map[string]string {
	hashes := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			hashes[parts[1]] = parts[0]
		}
	}
	return hashes
}

func resolveEnginePath() string {
	execPath, err := os.Executable()
	if err != nil {
		return "llamafile"
	}
	binDir := filepath.Dir(execPath)

	if strings.Contains(binDir, "Contents/MacOS") {
		appRoot := strings.Split(binDir, "Contents/MacOS")[0]
		candidate := filepath.Join(appRoot, "Contents", "Resources", "models", "llamafile")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	candidate := filepath.Join(binDir, "models", "llamafile")
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	return "llamafile"
}
