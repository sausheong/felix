package gateway

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"sort"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/local"
	"github.com/sausheong/felix/internal/tools"
)

// redactedSentinel is the literal value rendered in place of a secret in
// API responses. Inlined here (rather than living in internal/config) so
// the settings.go port stays self-contained. The client recognises it as
// "this slot has a stored value; leaving it as the sentinel on PUT
// preserves it".
const redactedSentinel = "***redacted***"

// secretEnvKeyPattern matches env-var KEYs whose values must never be
// returned to the client. Case-insensitive against any of: secret, key,
// token, password, passwd, pwd, credential, bearer — matched at the END
// of the key name only. Non-anchored (no underscore requirement) so
// APITOKEN / OPENAIAPIKEY / GITHUBTOKEN are caught.
var secretEnvKeyPattern = regexp.MustCompile(`(?i)(secret|key|token|password|passwd|pwd|credential|bearer)$`)

// writeJSONError writes a {"error": msg} body with the given status. It
// marshals the message so any quotes, backslashes, or newlines in msg (common
// in json.Unmarshal / validation errors) are escaped — hand-built JSON via
// fmt.Fprintf produced invalid JSON for exactly those messages, breaking the
// client's JSON.parse and hiding the real reason. Caller must not have written
// a status yet. Assumes a fresh response (sets Content-Type and status here).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encode a map so the value is always valid JSON; ignore the encode error
	// (the only realistic failure is a broken connection, already unrecoverable).
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// redactSecretEnvMap returns a NEW map with secret-keyed values replaced
// by redactedSentinel. Input is not mutated.
func redactSecretEnvMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if secretEnvKeyPattern.MatchString(k) {
			out[k] = redactedSentinel
		} else {
			out[k] = v
		}
	}
	return out
}

// unredactEnvMap merges incoming over current with sentinel-preserving
// semantics: any incoming value equal to redactedSentinel is replaced by
// the corresponding current value (or dropped if current has none).
func unredactEnvMap(incoming, current map[string]string) map[string]string {
	if incoming == nil {
		return nil
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if v == redactedSentinel {
			if existing, ok := current[k]; ok {
				out[k] = existing
			}
			continue
		}
		out[k] = v
	}
	return out
}

// SettingsHandlers holds the HTTP handlers for the settings page and config API.
type SettingsHandlers struct {
	Page            http.HandlerFunc
	GetConfig       http.HandlerFunc
	SaveConfig      http.HandlerFunc
	ListTools       http.HandlerFunc
	BootstrapStatus http.HandlerFunc
}

// BootstrapSnapshotter is the subset of *local.Tracker the handler needs.
// Defined as an interface so callers may pass nil (no-op handler) and
// tests can inject fakes. Felix's production implementation is the
// bundled-Ollama Tracker in internal/local.
type BootstrapSnapshotter interface {
	Snapshot() local.BootstrapSnapshot
}

// NewSettingsHandlers returns handlers for the settings page and config REST API.
// toolReg may be nil; ListTools then returns an empty list.
// bootstrap may be nil; BootstrapStatus then reports an inactive snapshot.
func NewSettingsHandlers(cfg *config.Config, toolReg *tools.Registry, bootstrap BootstrapSnapshotter, onSave func(*config.Config)) *SettingsHandlers {
	return &SettingsHandlers{
		Page: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(settingsHTML)
		},

		GetConfig: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			// Deep-copy then redact: the on-disk / in-memory cfg keeps
			// real secrets, but the JSON we ship to the client masks
			// every secret-keyed env value. The clone uses
			// marshal+unmarshal to avoid hand-coding every nested
			// struct's pointer/map semantics.
			raw, err := json.Marshal(cfg)
			if err != nil {
				http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
				return
			}
			var clone config.Config
			if err := json.Unmarshal(raw, &clone); err != nil {
				http.Error(w, `{"error":"clone config"}`, http.StatusInternalServerError)
				return
			}
			redactConfigSecrets(&clone)

			// Re-marshal then unmarshal-to-map so we can (a) inject the wire-only
			// _hasKey field per provider for the Providers tab "key set" badge,
			// and (b) redact api_key with the same sentinel the MCP env path uses
			// so SaveConfig's restore step (see restoreSecretProviderKeys below)
			// preserves the stored key on round-trips that didn't touch it.
			cloneBytes, err := json.Marshal(&clone)
			if err != nil {
				http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
				return
			}
			var asMap map[string]any
			if err := json.Unmarshal(cloneBytes, &asMap); err != nil {
				http.Error(w, `{"error":"map config"}`, http.StatusInternalServerError)
				return
			}
			if provs, ok := asMap["providers"].(map[string]any); ok {
				for _, prov := range provs {
					pm, ok := prov.(map[string]any)
					if !ok {
						continue
					}
					apiKey, _ := pm["api_key"].(string)
					pm["_hasKey"] = apiKey != ""
					if apiKey != "" {
						pm["api_key"] = redactedSentinel
					}
				}
			}

			data, err := json.MarshalIndent(asMap, "", "  ")
			if err != nil {
				http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
				return
			}
			w.Write(data)
		},

		SaveConfig: func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
			if err != nil {
				http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
				return
			}

			var newCfg config.Config
			if err := json.Unmarshal(body, &newCfg); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}

			// Sentinel-preserves-existing for secret-keyed env values.
			// Walks incoming MCP env maps and substitutes any
			// ***redacted*** value with the matching value from the
			// current in-memory cfg. Clients GET → edit → PUT and
			// never need to know any secret they didn't type.
			restoreSecretEnvs(&newCfg, cfg)
			restoreSecretProviderKeys(&newCfg, cfg)
			restoreSecretScalars(&newCfg, cfg)

			if err := newCfg.Validate(); err != nil {
				writeJSONError(w, http.StatusBadRequest, "validation: "+err.Error())
				return
			}

			// Copy path from current config so Save writes to the right file.
			newCfg.SetPath(cfg.Path())

			// Strip runtime-only auto-added tool names (MCP + cortex) from
			// the on-disk write so user-edited allowlists do not accumulate
			// ghost entries when those subsystems are later disabled.
			// In-memory cfg is unaffected; UpdateFrom below re-syncs from
			// newCfg, and the next startup re-applies the auto-add.
			cfg.StripMCPAutoAdded(&newCfg)
			cfg.StripCortexAutoAdded(&newCfg)

			if err := newCfg.Save(); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "save: "+err.Error())
				return
			}

			// Update the in-memory config so the GET handler returns fresh values.
			cfg.UpdateFrom(&newCfg)

			slog.Info("config saved via settings page")

			// Trigger hot-reload callback if configured.
			if onSave != nil {
				onSave(&newCfg)
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		},

		ListTools: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			type toolDTO struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			out := struct {
				Tools []toolDTO `json:"tools"`
			}{Tools: []toolDTO{}}
			if toolReg != nil {
				names := toolReg.Names()
				sort.Strings(names)
				for _, n := range names {
					t, ok := toolReg.Get(n)
					if !ok {
						continue
					}
					out.Tools = append(out.Tools, toolDTO{Name: n, Description: t.Description()})
				}
			}
			data, err := json.Marshal(out)
			if err != nil {
				http.Error(w, `{"error":"marshal tools"}`, http.StatusInternalServerError)
				return
			}
			w.Write(data)
		},

		BootstrapStatus: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			var snap local.BootstrapSnapshot
			if bootstrap != nil {
				snap = bootstrap.Snapshot()
			}
			if snap.Models == nil {
				snap.Models = map[string]local.ModelStatus{}
			}
			data, err := json.Marshal(snap)
			if err != nil {
				http.Error(w, `{"error":"marshal bootstrap"}`, http.StatusInternalServerError)
				return
			}
			w.Write(data)
		},
	}
}

// redactConfigSecrets mutates cfg in place, replacing every secret-bearing
// value with redactedSentinel: MCP stdio env values, MCP HTTP auth literals
// (client_secret/token), the Telegram bot token, the gateway auth token, the
// web-search API key, and every OTel header value. The *_env name-reference
// forms are NOT secrets and are left intact. Provider api_key is handled
// separately in GetConfig.
func redactConfigSecrets(cfg *config.Config) {
	for i := range cfg.MCPServers {
		s := &cfg.MCPServers[i]
		if s.Stdio != nil && s.Stdio.Env != nil {
			s.Stdio.Env = redactSecretEnvMap(s.Stdio.Env)
		}
		if s.Auth.ClientSecret != "" {
			s.Auth.ClientSecret = redactedSentinel
		}
		if s.Auth.Token != "" {
			s.Auth.Token = redactedSentinel
		}
		if s.HTTP != nil {
			if s.HTTP.Auth.ClientSecret != "" {
				s.HTTP.Auth.ClientSecret = redactedSentinel
			}
			if s.HTTP.Auth.Token != "" {
				s.HTTP.Auth.Token = redactedSentinel
			}
		}
	}
	if cfg.Telegram.BotToken != "" {
		cfg.Telegram.BotToken = redactedSentinel
	}
	if cfg.Gateway.Auth.Token != "" {
		cfg.Gateway.Auth.Token = redactedSentinel
	}
	if cfg.WebSearch.APIKey != "" {
		cfg.WebSearch.APIKey = redactedSentinel
	}
	for k, v := range cfg.OTel.Headers {
		if v != "" {
			cfg.OTel.Headers[k] = redactedSentinel
		}
	}
}

// restoreSecretScalars mirrors restoreSecretEnvs/restoreSecretProviderKeys for
// the non-map scalar secrets. Any incoming field whose value is exactly
// redactedSentinel is swapped back to the stored value from current, so a
// GET -> edit -> PUT round-trip never drops a secret the user did not retype.
// A non-sentinel value is a genuine user edit and is left as-is.
func restoreSecretScalars(incoming, current *config.Config) {
	if incoming.Telegram.BotToken == redactedSentinel {
		incoming.Telegram.BotToken = current.Telegram.BotToken
	}
	if incoming.Gateway.Auth.Token == redactedSentinel {
		incoming.Gateway.Auth.Token = current.Gateway.Auth.Token
	}
	if incoming.WebSearch.APIKey == redactedSentinel {
		incoming.WebSearch.APIKey = current.WebSearch.APIKey
	}
	for k, v := range incoming.OTel.Headers {
		if v == redactedSentinel {
			incoming.OTel.Headers[k] = current.OTel.Headers[k]
		}
	}
	curByID := make(map[string]*config.MCPServerConfig, len(current.MCPServers))
	for i := range current.MCPServers {
		curByID[current.MCPServers[i].ID] = &current.MCPServers[i]
	}
	for i := range incoming.MCPServers {
		s := &incoming.MCPServers[i]
		cur := curByID[s.ID]
		if cur == nil {
			continue
		}
		if s.Auth.ClientSecret == redactedSentinel {
			s.Auth.ClientSecret = cur.Auth.ClientSecret
		}
		if s.Auth.Token == redactedSentinel {
			s.Auth.Token = cur.Auth.Token
		}
		if s.HTTP != nil && cur.HTTP != nil {
			if s.HTTP.Auth.ClientSecret == redactedSentinel {
				s.HTTP.Auth.ClientSecret = cur.HTTP.Auth.ClientSecret
			}
			if s.HTTP.Auth.Token == redactedSentinel {
				s.HTTP.Auth.Token = cur.HTTP.Auth.Token
			}
		}
	}
}

// restoreSecretEnvs walks incoming's MCP env maps and replaces every
// RedactedSentinel value with the matching value from current. Keys
// present in incoming but missing from current pass through unchanged
// (they were genuinely new entries); keys present in current but
// missing from incoming are dropped (user deleted them from the form).
func restoreSecretEnvs(incoming, current *config.Config) {
	curByID := make(map[string]*config.MCPServerConfig, len(current.MCPServers))
	for i := range current.MCPServers {
		curByID[current.MCPServers[i].ID] = &current.MCPServers[i]
	}
	for i := range incoming.MCPServers {
		s := &incoming.MCPServers[i]
		if s.Stdio == nil || s.Stdio.Env == nil {
			continue
		}
		cur := curByID[s.ID]
		var curEnv map[string]string
		if cur != nil && cur.Stdio != nil {
			curEnv = cur.Stdio.Env
		}
		s.Stdio.Env = unredactEnvMap(s.Stdio.Env, curEnv)
	}
}

// restoreSecretProviderKeys mirrors restoreSecretEnvs for the per-provider
// api_key field. The GetConfig handler replaces every non-empty api_key
// with redactedSentinel on the wire; this walks incoming and substitutes
// any sentinel value back with current's stored key. Providers present in
// incoming but missing from current pass through unchanged (genuinely new
// providers the user just added). A truly empty incoming api_key clears
// the stored value, preserving "delete the key" as a UI gesture.
func restoreSecretProviderKeys(incoming, current *config.Config) {
	if incoming.Providers == nil || current.Providers == nil {
		return
	}
	for name, prov := range incoming.Providers {
		if prov.APIKey != redactedSentinel {
			continue
		}
		cur, ok := current.Providers[name]
		if !ok {
			continue
		}
		prov.APIKey = cur.APIKey
		incoming.Providers[name] = prov
	}
}

// settingsHTML is the settings single-page UI, embedded as a static asset.
// It contains no server-side template substitution, so it is served verbatim.
//
//go:embed assets/settings.html
var settingsHTML []byte
