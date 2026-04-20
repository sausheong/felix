package local

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModelPaths_DefaultSearchOrder(t *testing.T) {
	result, err := ResolveModelPaths("", "")
	if err == nil {
		t.Fatalf("expected error when no model found, got: %+v", result)
	}
}

func TestResolveModelPaths_ExplicitDir(t *testing.T) {
	tmp := t.TempDir()
	modelDir := filepath.Join(tmp, "gemma-4-e4b")
	os.MkdirAll(modelDir, 0o755)
	writeTestFile(t, filepath.Join(modelDir, "model.gguf"), "model-data")
	writeTestFile(t, filepath.Join(modelDir, "mmproj.gguf"), "mmproj-data")

	result, err := ResolveModelPaths(modelDir, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelPath != filepath.Join(modelDir, "model.gguf") {
		t.Errorf("expected model path %s, got %s", filepath.Join(modelDir, "model.gguf"), result.ModelPath)
	}
	if result.MMProjPath != filepath.Join(modelDir, "mmproj.gguf") {
		t.Errorf("expected mmproj path %s, got %s", filepath.Join(modelDir, "mmproj.gguf"), result.MMProjPath)
	}
}

func TestVerifySHA256_MatchingFiles(t *testing.T) {
	tmp := t.TempDir()
	modelData := "test-model-binary"
	mmprojData := "test-mmproj-binary"
	modelPath := filepath.Join(tmp, "model.gguf")
	mmprojPath := filepath.Join(tmp, "mmproj.gguf")

	modelHash := fmt.Sprintf("%x", sha256.Sum256([]byte(modelData)))
	mmprojHash := fmt.Sprintf("%x", sha256.Sum256([]byte(mmprojData)))

	os.WriteFile(modelPath, []byte(modelData), 0o644)
	os.WriteFile(mmprojPath, []byte(mmprojData), 0o644)
	sumsContent := fmt.Sprintf("%s  model.gguf\n%s  mmproj.gguf\n", modelHash, mmprojHash)
	os.WriteFile(filepath.Join(tmp, "SHA256SUMS"), []byte(sumsContent), 0o644)

	result := ModelPaths{ModelPath: modelPath, MMProjPath: mmprojPath}
	err := result.VerifySHA256(tmp)
	if err != nil {
		t.Fatalf("expected SHA256 verify to pass, got: %v", err)
	}
}

func TestVerifySHA256_Mismatch(t *testing.T) {
	tmp := t.TempDir()
	modelPath := filepath.Join(tmp, "model.gguf")
	os.WriteFile(modelPath, []byte("bad-data"), 0o644)
	shaContent := "model.gguf  aaaa1111bbbb2222cccc3333dddd4444\n"
	os.WriteFile(filepath.Join(tmp, "SHA256SUMS"), []byte(shaContent), 0o644)

	result := ModelPaths{ModelPath: modelPath}
	err := result.VerifySHA256(tmp)
	if err == nil {
		t.Fatal("expected SHA256 mismatch error, got nil")
	}
}

func TestParseSHA256SUMS(t *testing.T) {
	content := "abc123  model.gguf\ndef456  mmproj.gguf\n# comment\n\n"
	hashes := parseSHA256SUMS(content)
	if len(hashes) != 2 {
		t.Fatalf("expected 2 hashes, got %d", len(hashes))
	}
	if hashes["model.gguf"] != "abc123" {
		t.Errorf("expected model.gguf hash abc123, got %s", hashes["model.gguf"])
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	os.WriteFile(path, []byte(content), 0o644)
}
