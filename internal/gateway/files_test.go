package gateway

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/config"
)

func newTestFilesHandlers(t *testing.T, agentID, workspace string) *FilesHandlers {
	t.Helper()
	cfg := newTestConfig(t, agentID, workspace)
	return NewFilesHandlers(func() *config.Config { return cfg })
}

func TestList_EmptyWorkspace(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	h := newTestFilesHandlers(t, "default", ws)

	req := httptest.NewRequest("GET", "/files/list?agent=default&dir=", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	var resp struct {
		Entries []struct{ Name string } `json:"entries"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(resp.Entries))
	}
}

func TestList_DirsBeforeFiles(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	os.MkdirAll(filepath.Join(ws, "zdir"), 0o755)
	os.WriteFile(filepath.Join(ws, "afile.txt"), []byte("hi"), 0o644)
	h := newTestFilesHandlers(t, "default", ws)

	req := httptest.NewRequest("GET", "/files/list?agent=default&dir=", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var resp struct {
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	if len(resp.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(resp.Entries))
	}
	if resp.Entries[0].Type != "dir" {
		t.Errorf("first entry should be dir, got %q", resp.Entries[0].Type)
	}
	if resp.Entries[1].Type != "file" {
		t.Errorf("second entry should be file, got %q", resp.Entries[1].Type)
	}
}

func TestList_DotfilesHidden(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	os.WriteFile(filepath.Join(ws, ".hidden"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(ws, "visible.txt"), []byte("hi"), 0o644)
	h := newTestFilesHandlers(t, "default", ws)

	req := httptest.NewRequest("GET", "/files/list?agent=default&dir=", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var resp struct {
		Entries []struct{ Name string } `json:"entries"`
	}
	json.NewDecoder(rr.Body).Decode(&resp)
	for _, e := range resp.Entries {
		if strings.HasPrefix(e.Name, ".") {
			t.Errorf("dotfile %q should be hidden", e.Name)
		}
	}
	if len(resp.Entries) != 1 {
		t.Errorf("want 1 visible entry, got %d", len(resp.Entries))
	}
}

func TestUpload_ThenList(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	h := newTestFilesHandlers(t, "default", ws)
	h.diskCheck = func(_ string, _ int64) (bool, error) { return true, nil }

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "hello.txt")
	fw.Write([]byte("hello world"))
	w.Close()

	req := httptest.NewRequest("POST", "/files/upload?agent=default&dir=", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("upload got %d: %s", rr.Code, rr.Body.String())
	}

	if _, err := os.Stat(filepath.Join(ws, "hello.txt")); err != nil {
		t.Errorf("uploaded file not found: %v", err)
	}
}

func TestDelete_File(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	path := filepath.Join(ws, "todelete.txt")
	os.WriteFile(path, []byte("bye"), 0o644)
	h := newTestFilesHandlers(t, "default", ws)

	req := httptest.NewRequest("DELETE", "/files?agent=default&path=todelete.txt", nil)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be gone after delete")
	}
}

func TestMkDir(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	h := newTestFilesHandlers(t, "default", ws)

	body := `{"agent":"default","path":"newdir"}`
	req := httptest.NewRequest("POST", "/files/mkdir", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.MkDir(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("mkdir got %d: %s", rr.Code, rr.Body.String())
	}
	info, err := os.Stat(filepath.Join(ws, "newdir"))
	if err != nil || !info.IsDir() {
		t.Error("directory should exist after mkdir")
	}
}

func TestRename(t *testing.T) {
	ws := t.TempDir()
	ws, _ = filepath.EvalSymlinks(ws)
	os.WriteFile(filepath.Join(ws, "old.txt"), []byte("data"), 0o644)
	h := newTestFilesHandlers(t, "default", ws)

	body := `{"agent":"default","path":"old.txt","newName":"new.txt"}`
	req := httptest.NewRequest("POST", "/files/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Rename(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("rename got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(ws, "new.txt")); err != nil {
		t.Error("renamed file should exist")
	}
}
