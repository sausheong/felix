package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Installer wraps the Ollama HTTP API for model management.
type Installer struct {
	baseURL string // e.g. http://127.0.0.1:18790
	client  *http.Client
}

// NewInstaller creates an Installer pointing at an Ollama instance.
func NewInstaller(baseURL string) *Installer {
	return &Installer{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 0}, // streaming-friendly; per-request contexts handle cancellation
	}
}

// Model describes a locally-available model.
type Model struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size"`
}

// List returns the locally-pulled models via /api/tags.
func (i *Installer) List(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list: ollama returned %s", resp.Status)
	}
	var body struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("list: decode: %w", err)
	}
	return body.Models, nil
}

// Delete removes a model via /api/delete.
func (i *Installer) Delete(ctx context.Context, name string) error {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, i.baseURL+"/api/delete", bytesReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete %q: ollama returned %s", name, resp.Status)
	}
	return nil
}

// ProgressEvent is one line of pull progress emitted by Ollama.
type ProgressEvent struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Pull streams model bytes via /api/pull, calling onEvent for each NDJSON line.
// onEvent may be nil. The function returns when the stream ends or an error occurs.
func (i *Installer) Pull(ctx context.Context, name string, onEvent func(ProgressEvent)) error {
	body, err := json.Marshal(map[string]any{"name": name, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.baseURL+"/api/pull", bytesReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("pull: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull %q: ollama returned %s", name, resp.Status)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var ev ProgressEvent
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("pull %q: decode: %w", name, err)
		}
		if ev.Error != "" {
			return fmt.Errorf("pull %q: %s", name, ev.Error)
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
}

// Show returns the size in bytes of a remote (or local) model via /api/show.
func (i *Installer) Show(ctx context.Context, name string) (int64, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.baseURL+"/api/show", bytesReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("show: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("show %q: ollama returned %s", name, resp.Status)
	}
	var out struct {
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("show %q: decode: %w", name, err)
	}
	return out.Size, nil
}

// shortDeadline returns a context cancelled after d for one-shot calls.
func shortDeadline(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// bytesReader is a small wrapper to avoid importing bytes alongside io for one call site.
func bytesReader(b []byte) *bytesReadCloser { return &bytesReadCloser{b: b} }

type bytesReadCloser struct {
	b   []byte
	pos int
}

func (r *bytesReadCloser) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

func (r *bytesReadCloser) Close() error { return nil }
