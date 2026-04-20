package local

import (
	"fmt"
	"os"

	"github.com/sausheong/felix/internal/config"
)

// InjectLocalProvider adds the "local" provider to the config if model files
// are resolvable and local.enabled is true.
func InjectLocalProvider(cfg *config.Config) error {
	if !cfg.Local.Enabled {
		return nil
	}

	dataDir := config.DefaultDataDir()
	paths, err := ResolveModelPaths(cfg.Local.ModelDir, dataDir)
	if err != nil {
		return err
	}

	if err := paths.VerifySHA256(paths.SearchRoot); err != nil {
		return err
	}

	cfg.Providers["local"] = config.ProviderConfig{
		Kind:    "local",
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.Local.Port),
		APIKey:  "",
	}

	return nil
}

// DefaultModelForLocal returns the appropriate default model string if local
// model files are available and no cloud provider has an API key configured.
func DefaultModelForLocal(cfg *config.Config) string {
	if !cfg.Local.Enabled {
		return ""
	}

	dataDir := config.DefaultDataDir()
	_, err := ResolveModelPaths(cfg.Local.ModelDir, dataDir)
	if err != nil {
		return ""
	}

	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			return ""
		}
	}

	for _, envVar := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"} {
		if os.Getenv(envVar) != "" {
			return ""
		}
	}

	return "local/gemma-4-e4b-it"
}
