package local

import (
	"fmt"

	"github.com/sausheong/felix/internal/config"
)

// InjectLocalProvider loads the felix config at cfgPath, ensures a `local`
// provider entry exists pointing at boundPort, and saves the file in place.
//
// Used at startup once the supervisor has successfully bound a port, so the
// rest of the runtime sees the correct base_url.
func InjectLocalProvider(cfgPath string, boundPort int) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("inject local provider: load: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.ProviderConfig{}
	}
	cfg.Providers["local"] = config.ProviderConfig{
		Kind:    "local",
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", boundPort),
	}
	cfg.SetPath(cfgPath)
	return cfg.Save()
}

// DefaultModelsDir returns the default OLLAMA_MODELS path under the felix data dir.
func DefaultModelsDir() string {
	return fmt.Sprintf("%s/ollama/models", config.DefaultDataDir())
}
