package local

import (
	"context"
	"encoding/json"
	"fmt"
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

// shortDeadline returns a context cancelled after d for one-shot calls.
func shortDeadline(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
