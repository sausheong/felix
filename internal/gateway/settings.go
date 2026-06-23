package gateway

import (
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
			fmt.Fprint(w, settingsHTML)
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

const settingsHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Settings · Felix</title>
<link rel="icon" type="image/png" href="/favicon.png">
<style>
/* === Custom Properties === */
/* Palette mirrors chat.go: cream + forest-green OKLCH, light by default. */
:root {
	--color-primary: oklch(0.55 0.13 162);
	--color-primary-hover: oklch(0.48 0.13 162);
	--color-bg: oklch(0.985 0.005 95);
	--color-surface: oklch(0.99 0.005 95);
	--color-bg-soft: oklch(0.97 0.005 95);
	--color-text: oklch(0.22 0.01 95);
	--color-text-muted: oklch(0.5 0.01 95);
	--color-border: oklch(0.9 0.008 95);
	--color-error: oklch(0.55 0.18 27);
	--color-success: oklch(0.55 0.13 162);
	--radius: 8px;
	--shadow: 0 1px 3px oklch(0 0 0 / 0.06);
	--shadow-md: 0 4px 12px oklch(0 0 0 / 0.08);
}
html.dark {
	--color-primary: oklch(0.78 0.13 162);
	--color-primary-hover: oklch(0.7 0.13 162);
	--color-bg: oklch(0.18 0.01 162);
	--color-surface: oklch(0.21 0.01 162);
	--color-bg-soft: oklch(0.25 0.01 162);
	--color-text: oklch(0.92 0.005 95);
	--color-text-muted: oklch(0.68 0.01 95);
	--color-border: oklch(0.32 0.015 162);
	--color-error: oklch(0.72 0.18 27);
	--color-success: oklch(0.78 0.13 162);
	--shadow: 0 1px 3px oklch(0 0 0 / 0.3);
	--shadow-md: 0 4px 12px oklch(0 0 0 / 0.35);
}

/* === Reset === */
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
	background: var(--color-bg);
	color: var(--color-text);
	min-height: 100vh;
	line-height: 1.6;
}

/* === Header / Nav === */
#header {
	background: var(--color-surface);
	border-bottom: 1px solid var(--color-border);
	padding: 0.75rem 1.5rem;
	box-shadow: var(--shadow);
	display: flex;
	align-items: center;
	gap: 0.75rem;
	position: sticky;
	top: 0;
	z-index: 10;
	/* sticky already creates a positioning context for the absolute menu */
}
#header { padding: 0.95rem 1.5rem; }
#header h1 { font-size: 1.05rem; font-weight: 600; color: var(--color-text); }
.spacer { margin-left: auto; }
#hamburger-btn {
	background: none;
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	padding: 0.25rem 0.5rem;
	cursor: pointer;
	font-size: 1.1rem;
	line-height: 1;
	color: var(--color-text);
	transition: border-color 0.2s, color 0.2s;
}
#hamburger-btn:hover, #hamburger-btn[aria-expanded="true"] { border-color: var(--color-primary); color: var(--color-primary); }
#hamburger-menu {
	position: absolute;
	top: 3rem;
	left: 1.5rem;
	z-index: 100;
	min-width: 14rem;
	background: var(--color-surface);
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	box-shadow: var(--shadow);
	padding: 0.35rem;
	display: flex;
	flex-direction: column;
	gap: 0.1rem;
}
#hamburger-menu[hidden] { display: none; }
.menu-item {
	display: block;
	width: 100%;
	text-align: left;
	background: none;
	border: none;
	color: var(--color-text);
	font: inherit;
	font-size: 0.85rem;
	padding: 0.5rem 0.65rem;
	border-radius: 5px;
	cursor: pointer;
	text-decoration: none;
	transition: background 0.15s, color 0.15s;
}
.menu-item:hover, .menu-item:focus { background: var(--color-bg-soft); color: var(--color-primary); outline: none; }
.menu-item.menu-danger { color: var(--color-error); }
.menu-item.menu-danger:hover { background: var(--color-error); color: #fff; }
.menu-icon {
	display: inline-block;
	width: 1.5em;
	text-align: center;
	margin-right: 0.5rem;
	opacity: 0.85;
}
.menu-sep {
	margin: 0.25rem 0;
	border: none;
	border-top: 1px solid var(--color-border);
}
#status-msg { font-size: 0.85rem; }
#status-msg.success { color: var(--color-success); }
#status-msg.error { color: var(--color-error); }
.dirty-indicator {
	color: var(--color-warning, oklch(0.6 0.16 65));
	font-size: 0.8rem;
	margin-right: 0.6rem;
	font-weight: 500;
}

/* === Buttons === */
.btn-primary {
	display: inline-flex;
	align-items: center;
	padding: 0.45rem 1rem;
	background: var(--color-primary);
	color: #fff;
	border: none;
	border-radius: var(--radius);
	font-size: 0.875rem;
	font-weight: 500;
	cursor: pointer;
	transition: background 0.15s;
}
.btn-primary:hover { background: var(--color-primary-hover); }
.btn-primary:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-danger {
	display: inline-flex;
	align-items: center;
	padding: 0.45rem 0.85rem;
	background: var(--color-surface);
	color: var(--color-error);
	border: 1px solid var(--color-error);
	border-radius: var(--radius);
	font-size: 0.875rem;
	font-weight: 500;
	cursor: pointer;
	transition: background 0.15s, color 0.15s;
}
.btn-danger:hover { background: var(--color-error); color: #fff; }
.btn-danger:disabled { opacity: 0.5; cursor: wait; }
.btn-icon {
	background: var(--color-surface);
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	padding: 0.3rem 0.55rem;
	cursor: pointer;
	font-size: 1rem;
	line-height: 1;
	color: var(--color-text);
	transition: border-color 0.15s;
}
.btn-icon:hover { border-color: var(--color-primary); }

/* === Main Layout === */
main { padding: 1.25rem 1.5rem 2.5rem; }
.container { width: 100%; }

/* === Settings shell — page-level, not a card === */
.settings-wide {
	background: transparent;
	border: 0;
	border-radius: 0;
	padding: 0;
	box-shadow: none;
}

/* === Side-nav layout (replaces top finger-tabs) === */
.settings-shell {
	display: flex;
	gap: 1.5rem;
	align-items: flex-start;
}
.finger-tabs {
	display: flex;
	flex-direction: column;
	gap: 0.15rem;
	width: 200px;
	flex-shrink: 0;
	padding: 0.5rem 0;
}
.finger-tab {
	display: flex;
	align-items: center;
	gap: 0.65rem;
	padding: 0.55rem 0.85rem;
	font-size: 0.9rem;
	font-weight: 500;
	color: var(--color-text);
	cursor: pointer;
	border: none;
	background: none;
	border-radius: 8px;
	text-align: left;
	white-space: nowrap;
	transition: background 0.12s, color 0.12s;
	font-family: inherit;
}
.finger-tab:hover { background: var(--color-bg-soft); }
.finger-tab.active {
	background: color-mix(in oklch, var(--color-primary) 14%, transparent);
	color: var(--color-primary);
	font-weight: 600;
}
.finger-tab .ft-icon {
	width: 18px; height: 18px;
	stroke: currentColor; fill: none;
	stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round;
	flex-shrink: 0;
	color: var(--color-text-muted);
}
.finger-tab.active .ft-icon, .finger-tab:hover .ft-icon { color: var(--color-primary); }
.finger-panels { flex: 1; min-width: 0; }
.finger-panel {
	display: none;
	background: var(--color-surface);
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	padding: 1.75rem 2rem;
}
.finger-panel.active { display: block; }
@media (max-width: 720px) {
	.settings-shell { flex-direction: column; gap: 0.75rem; }
	.finger-tabs {
		flex-direction: row;
		width: 100%;
		overflow-x: auto;
		padding: 0;
		scrollbar-width: none;
	}
	.finger-tabs::-webkit-scrollbar { display: none; }
	.finger-tab { flex-shrink: 0; }
}

/* === Form Groups (label above input) === */
.form-group { margin-bottom: 1rem; }
.form-group > label {
	display: block;
	font-size: 0.875rem;
	font-weight: 500;
	margin-bottom: 0.3rem;
	color: var(--color-text);
}
.form-group input[type="text"],
.form-group input[type="password"],
.form-group input[type="number"],
.form-group input[type="url"],
.form-group input[type="email"],
.form-group select,
.form-group textarea {
	width: 100%;
	padding: 0.5rem 0.75rem;
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	font-size: 0.9rem;
	line-height: 1.4;
	background: var(--color-bg);
	color: var(--color-text);
	font-family: inherit;
	box-sizing: border-box;
	transition: border-color 0.15s, box-shadow 0.15s;
}
/* Strip native chrome from <select> so its rendered height matches
   <input>; provide our own chevron via background-image. Without this,
   macOS Safari renders the select shorter than equivalently-padded
   inputs in the same row. */
.form-group select {
	appearance: none;
	-webkit-appearance: none;
	-moz-appearance: none;
	background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'><polyline points='6 9 12 15 18 9'/></svg>");
	background-repeat: no-repeat;
	background-position: right 0.65rem center;
	background-size: 12px 12px;
	padding-right: 2rem;
}
.form-group input:focus,
.form-group select:focus,
.form-group textarea:focus {
	outline: none;
	border-color: var(--color-primary);
	box-shadow: 0 0 0 3px rgba(22,219,170,0.15);
}
.form-group textarea {
	min-height: 80px;
	resize: vertical;
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 0.85rem;
}

/* === 2-column Row === */
.form-row {
	display: grid;
	grid-template-columns: 1fr 1fr;
	gap: 1rem;
}

/* === Toggle Group === */
.toggle-group {
	display: flex;
	align-items: center;
	gap: 0.65rem;
	margin-bottom: 1rem;
}
.toggle-label {
	font-size: 0.875rem;
	font-weight: 500;
	color: var(--color-text);
}
.toggle {
	position: relative;
	width: 40px;
	height: 22px;
	flex-shrink: 0;
}
.toggle input { opacity: 0; width: 0; height: 0; position: absolute; }
.toggle .slider {
	position: absolute;
	cursor: pointer;
	top: 0; left: 0; right: 0; bottom: 0;
	background: var(--color-border);
	border-radius: 22px;
	transition: 0.25s;
}
.toggle .slider:before {
	content: "";
	position: absolute;
	height: 16px;
	width: 16px;
	left: 3px;
	bottom: 3px;
	background: #fff;
	border-radius: 50%;
	transition: 0.25s;
}
.toggle input:checked + .slider { background: var(--color-primary); }
.toggle input:checked + .slider:before { transform: translateX(18px); }

/* === Panel Sections (sub-headings within a panel) === */
.panel-section { margin-bottom: 0.25rem; }
.panel-section + .panel-section {
	margin-top: 1.5rem;
	padding-top: 1.25rem;
	border-top: 1px solid var(--color-border);
}
.panel-section h3 {
	font-size: 1rem;
	font-weight: 600;
	color: var(--color-text);
	margin-bottom: 1rem;
}

/* === Dynamic Cards (Providers / Agents) === */
.dynamic-list { display: flex; flex-direction: column; gap: 0.75rem; margin-bottom: 0.75rem; }
.dynamic-item {
	background: var(--color-bg);
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	padding: 1rem 1rem 0.25rem;
	position: relative;
}
/* === Agent card collapse + Default badge (Task 1 of agents-collapse-default plan) === */
.collapse-card-header {
	display: flex;
	align-items: center;
	gap: 0.6rem;
	padding: 0.5rem 0.75rem;
	cursor: pointer;
	border-radius: 8px;
	margin: -0.5rem -0.75rem 0.5rem;
	user-select: none;
}
.collapse-card-header:hover { background: var(--color-bg-soft); }
.collapse-card-chevron {
	width: 14px;
	height: 14px;
	flex-shrink: 0;
	color: var(--color-text-muted);
	transition: transform 0.12s ease;
}
.dynamic-item.collapsed .collapse-card-chevron { transform: rotate(-90deg); }
.collapse-card-id {
	font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
	font-size: 0.85rem;
	color: var(--color-text-muted);
}
.collapse-card-name { font-weight: 600; }
.collapse-card-meta {
	color: var(--color-text-muted);
	font-size: 0.85rem;
	margin-left: auto;
}
.agent-card-default-badge {
	padding: 0.1rem 0.45rem;
	border-radius: 999px;
	background: color-mix(in oklch, var(--color-primary) 14%, transparent);
	color: var(--color-primary);
	font-size: 0.7rem;
	font-weight: 600;
	text-transform: uppercase;
	letter-spacing: 0.04em;
	flex-shrink: 0;
}
.dynamic-item.collapsed .collapse-card-body { display: none; }
.collapse-card-dot {
	width: 8px;
	height: 8px;
	border-radius: 50%;
	flex-shrink: 0;
	background: var(--color-text-muted);
}
.collapse-card-dot[data-on="true"] {
	background: #10b981;
}
.collapse-card-key-badge {
	padding: 0.1rem 0.45rem;
	border-radius: 999px;
	background: var(--color-surface-muted, rgba(0,0,0,0.06));
	color: var(--color-text-muted);
	font-size: 0.7rem;
	font-weight: 600;
	text-transform: uppercase;
	letter-spacing: 0.04em;
}
.collapse-card-key-badge[data-set="true"] {
	background: color-mix(in oklch, var(--color-primary) 14%, transparent);
	color: var(--color-primary);
}
.field-error {
	color: var(--color-error);
	font-size: 0.75rem;
	margin-top: 0.2rem;
	min-height: 1.1em;
}
.field-with-error input,
.field-with-error select,
.field-with-error textarea {
	border-color: var(--color-error);
}
input.field-with-error,
select.field-with-error,
textarea.field-with-error {
	border-color: var(--color-error);
}
.dynamic-item-title {
	font-weight: 600;
	font-size: 0.9rem;
	color: var(--color-text);
	margin-bottom: 0.75rem;
}
.inline-filter {
	width: 100%;
	padding: 0.4rem 0.6rem;
	margin: 0 0 0.6rem;
	font-size: 0.85rem;
	background: var(--color-bg);
	border: 1px solid var(--color-border);
	border-radius: 6px;
	color: var(--color-text);
	box-sizing: border-box;
}
.inline-filter:focus { border-color: var(--color-primary); outline: none; }
.dynamic-item-new {
	animation: dynamic-item-flash 1.2s ease-out;
}
@keyframes dynamic-item-flash {
	0%   { background: color-mix(in oklch, var(--color-primary) 35%, transparent); }
	100% { background: transparent; }
}
.remove-btn {
	position: absolute;
	top: 0.75rem;
	right: 0.75rem;
	background: none;
	border: none;
	color: var(--color-error);
	cursor: pointer;
	font-size: 1.1rem;
	line-height: 1;
	padding: 0.1rem 0.25rem;
	opacity: 0.6;
	border-radius: 4px;
}
.remove-btn:hover { opacity: 1; background: rgba(220,38,38,0.08); }
.add-btn {
	display: block;
	width: 100%;
	background: none;
	border: 1px dashed var(--color-border);
	border-radius: var(--radius);
	padding: 0.5rem;
	color: var(--color-text-muted);
	cursor: pointer;
	font-size: 0.875rem;
	transition: border-color 0.15s, color 0.15s;
}
.add-btn:hover { border-color: var(--color-primary); color: var(--color-primary); }

/* === WhatsApp QR Modal === */
#wa-qr-modal { display: none; position: fixed; inset: 0; z-index: 1000; }
.wa-qr-overlay {
	position: absolute; inset: 0;
	background: rgba(0,0,0,0.55);
	display: flex; align-items: center; justify-content: center;
}
.wa-qr-card {
	background: var(--color-surface);
	border-radius: var(--radius);
	padding: 2rem;
	max-width: 360px;
	text-align: center;
	box-shadow: 0 12px 40px rgba(0,0,0,0.3);
}
#wa-qr-modal[style*="flex"] { display: flex !important; }

/* === Loading / Error === */
.loading-state {
	text-align: center;
	padding: 3rem;
	color: var(--color-text-muted);
}
.error-state {
	padding: 1rem;
	background: #450a0a;
	color: var(--color-error);
	border-radius: var(--radius);
}
.error-state { background: color-mix(in oklch, var(--color-error) 14%, transparent); }

/* === Responsive === */
@media (max-width: 600px) {
	.form-row { grid-template-columns: 1fr; }
	.finger-tab { padding: 0.5rem 0.75rem; font-size: 0.8rem; }
}
</style>
</head>
<body>
<div id="header">
	<h1>Settings</h1>
	<span class="spacer"></span>
	<span id="status-msg"></span>
	<span id="save-dirty-indicator" class="dirty-indicator" hidden>Unsaved changes</span>
	<button class="btn-primary" id="save-btn" disabled>Save</button>
</div>
<!-- Hamburger kept hidden so existing JS handlers don't crash; the
     chat sidebar shell provides cross-page nav when this page is
     embedded in it. -->
<button id="hamburger-btn" hidden></button>
<div id="hamburger-menu" hidden>
	<button class="menu-item" data-action="theme" id="menu-theme" hidden></button>
</div>
<main>
<div class="container">
	<div id="loading" class="loading-state">Loading configuration&#8230;</div>
	<div id="settings-root" style="display:none">
		<div class="settings-wide settings-shell">
			<nav class="finger-tabs" id="tabs">
				<button class="finger-tab active" data-tab="agents">
					<svg class="ft-icon" viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 20a8 8 0 0 1 16 0"/></svg>
					Agents
				</button>
				<button class="finger-tab" data-tab="providers">
					<svg class="ft-icon" viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6c0 1.66 4.03 3 9 3s9-1.34 9-3V5M3 11v6c0 1.66 4.03 3 9 3s9-1.34 9-3v-6"/></svg>
					Providers
				</button>
				<button class="finger-tab" data-tab="models">
					<svg class="ft-icon" viewBox="0 0 24 24"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>
					Models
				</button>
				<button class="finger-tab" data-tab="intelligence">
					<svg class="ft-icon" viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14c0 1.66 4.03 3 9 3s9-1.34 9-3V5"/></svg>
					Memory
				</button>
				<button class="finger-tab" data-tab="security">
					<svg class="ft-icon" viewBox="0 0 24 24"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
					Security
				</button>
				<button class="finger-tab" data-tab="messaging">
					<svg class="ft-icon" viewBox="0 0 24 24"><path d="M22 2L11 13M22 2l-7 20-4-9-9-4z"/></svg>
					Messaging
				</button>
				<button class="finger-tab" data-tab="mcp">
					<svg class="ft-icon" viewBox="0 0 24 24"><rect x="2" y="3" width="20" height="6" rx="1"/><rect x="2" y="15" width="20" height="6" rx="1"/><path d="M6 6h.01M6 18h.01"/></svg>
					MCP
				</button>
				<button class="finger-tab" data-tab="gateway">
					<svg class="ft-icon" viewBox="0 0 24 24"><path d="M3 13h3l2-7 4 14 3-10 2 6h4"/></svg>
					OpenTelemetry
				</button>
				<button class="finger-tab" data-tab="skills">
					<svg class="ft-icon" viewBox="0 0 24 24"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2zM22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>
					Skills
				</button>
			</nav>
			<div class="finger-panels">
				<div class="finger-panel active" id="panel-agents"></div>
				<div class="finger-panel" id="panel-providers"></div>
				<div class="finger-panel" id="panel-models"></div>
				<div class="finger-panel" id="panel-intelligence"></div>
				<div class="finger-panel" id="panel-security"></div>
				<div class="finger-panel" id="panel-messaging"></div>
				<div class="finger-panel" id="panel-mcp"></div>
				<div class="finger-panel" id="panel-gateway"></div>
				<div class="finger-panel" id="panel-skills"></div>
			</div>
		</div>
	</div>
</div>
</main>

<script>
(function() {
	var saveBtn = document.getElementById('save-btn');
	var statusMsg = document.getElementById('status-msg');
	var hamburgerBtn = document.getElementById('hamburger-btn');
	var hamburgerMenu = document.getElementById('hamburger-menu');
	var menuTheme = document.getElementById('menu-theme');
	var loading = document.getElementById('loading');
	var settingsRoot = document.getElementById('settings-root');
	var cfg = null;
	var availableTools = []; // [{name, description}], populated from /settings/api/tools

	// Dirty-state tracking: any input/change inside settingsRoot flips the
	// dirty flag and reveals the "Unsaved changes" indicator next to Save.
	// beforeunload guards against accidental nav-away. Cleared on successful
	// save. wireDirtyTracking is called once after render() in the initial
	// load .then(), so the listeners fire only after the form is hydrated
	// (the render() pass itself programmatically populates inputs, which
	// would otherwise look like dirty edits).
	var dirtyIndicator = document.getElementById('save-dirty-indicator');
	var isDirty = false;
	function setDirty(v) {
		if (isDirty === v) return;
		isDirty = v;
		if (dirtyIndicator) dirtyIndicator.hidden = !v;
	}
	function wireDirtyTracking() {
		settingsRoot.addEventListener('input', function() { setDirty(true); });
		settingsRoot.addEventListener('change', function() { setDirty(true); });
	}
	window.addEventListener('beforeunload', function(e) {
		if (!isDirty) return;
		e.preventDefault();
		e.returnValue = '';
	});

	// === Hamburger menu: open/close + action dispatch ===
	function openMenu() {
		hamburgerMenu.hidden = false;
		hamburgerBtn.setAttribute('aria-expanded', 'true');
	}
	function closeMenu() {
		hamburgerMenu.hidden = true;
		hamburgerBtn.setAttribute('aria-expanded', 'false');
	}
	function toggleMenu() { hamburgerMenu.hidden ? openMenu() : closeMenu(); }
	hamburgerBtn.addEventListener('click', function(e) { e.stopPropagation(); toggleMenu(); });
	document.addEventListener('click', function(e) {
		if (!hamburgerMenu.hidden && !hamburgerMenu.contains(e.target) && e.target !== hamburgerBtn) {
			closeMenu();
		}
	});
	document.addEventListener('keydown', function(e) {
		if (e.key === 'Escape' && !hamburgerMenu.hidden) closeMenu();
	});
	hamburgerMenu.addEventListener('click', function(e) {
		var item = e.target.closest('[data-action]');
		if (!item) return; // anchor links (Chat/Jobs/Logs) just navigate
		closeMenu();
		switch (item.dataset.action) {
		case 'theme': toggleTheme(); break;
		}
	});

	// === Theme — menu label shows what clicking will switch TO ===
	function setTheme(mode) {
		if (mode === 'dark') {
			document.documentElement.classList.add('dark');
		} else {
			document.documentElement.classList.remove('dark');
		}
		if (menuTheme) menuTheme.textContent = (mode === 'dark') ? 'Light theme' : 'Dark theme';
		localStorage.setItem('felix-theme', mode);
	}
	setTheme(localStorage.getItem('felix-theme') || 'light');
	function toggleTheme() {
		setTheme(document.documentElement.classList.contains('dark') ? 'light' : 'dark');
	}

	// === Tab switching ===
	var tabBtns = document.querySelectorAll('.finger-tab');
	function activateTab(name) {
		var found = false;
		tabBtns.forEach(function(b) {
			if (b.dataset.tab === name) { b.classList.add('active'); found = true; }
			else { b.classList.remove('active'); }
		});
		document.querySelectorAll('.finger-panel').forEach(function(p) { p.classList.remove('active'); });
		var panel = document.getElementById('panel-' + name);
		if (panel) panel.classList.add('active');
		return found;
	}
	tabBtns.forEach(function(btn) {
		btn.addEventListener('click', function() {
			activateTab(btn.dataset.tab);
		});
	});
	// Honor URL hash on load (e.g. /settings#models) so the menu bar app
	// can deep-link to a specific tab on first run.
	if (location.hash) {
		activateTab(location.hash.slice(1));
	}

	// === Status message ===
	function showStatus(msg, isError) {
		statusMsg.textContent = msg;
		statusMsg.className = isError ? 'error' : 'success';
		if (!isError) setTimeout(function() { statusMsg.textContent = ''; statusMsg.className = ''; }, 3000);
	}

	// === Load config + tools list in parallel ===
	Promise.all([
		fetch(location.pathname + '/api/config').then(function(r) { return r.json(); }),
		fetch(location.pathname + '/api/tools').then(function(r) {
			return r.ok ? r.json() : {tools: []};
		}).catch(function() { return {tools: []}; })
	]).then(function(results) {
		cfg = results[0];
		availableTools = (results[1] && results[1].tools) || [];
		loading.style.display = 'none';
		settingsRoot.style.display = 'block';
		render();
		wireDirtyTracking();
		saveBtn.disabled = false;
	}).catch(function(err) {
		loading.className = 'error-state';
		loading.textContent = 'Failed to load config: ' + err.message;
	});

	// === Save ===
	saveBtn.addEventListener('click', function() {
		saveBtn.disabled = true;
		fetch(location.pathname + '/api/config', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify(cfg)
		})
		.then(function(r) { return r.json().then(function(d) { return {ok: r.ok, data: d}; }); })
		.then(function(res) {
			saveBtn.disabled = false;
			if (res.data.ok) {
				showStatus('Saved', false);
				setDirty(false);
			} else {
				showStatus('Error: ' + (res.data.error || 'unknown'), true);
			}
		})
		.catch(function(err) {
			saveBtn.disabled = false;
			showStatus('Error: ' + err.message, true);
		});
	});

	// === Render all panels ===
	function render() {
		renderAgents();
		renderProviders();
		renderModels();
		renderIntelligence();
		renderSecurity();
		renderMessaging();
		renderMCP();
		renderGateway();
		renderSkills();
		renderMemory();
	}

	// fmtBytes is shared by Skills (size column) and Memory (entry bytes).
	function fmtBytes(n) {
		if (!n || n < 0) return '';
		if (n < 1024) return n + ' B';
		var u = ['KB','MB','GB','TB'];
		var i = -1;
		do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
		return n.toFixed(1) + ' ' + u[i];
	}

	// === Helper: toggle-group ===
	function makeToggle(parent, label, checked, onChange) {
		var g = document.createElement('div');
		g.className = 'toggle-group';
		var t = document.createElement('label');
		t.className = 'toggle';
		var inp = document.createElement('input');
		inp.type = 'checkbox';
		inp.checked = !!checked;
		inp.addEventListener('change', function() { onChange(inp.checked); });
		var sl = document.createElement('span');
		sl.className = 'slider';
		t.appendChild(inp);
		t.appendChild(sl);
		var lbl = document.createElement('span');
		lbl.className = 'toggle-label';
		lbl.textContent = label;
		g.appendChild(t);
		g.appendChild(lbl);
		parent.appendChild(g);
		return g;
	}

	// === Helper: form-group (label above input) ===
	function makeField(parent, label, type, value, onChange) {
		if (type === 'toggle') {
			return makeToggle(parent, label, value, onChange);
		}
		var g = document.createElement('div');
		g.className = 'form-group';
		var l = document.createElement('label');
		l.textContent = label;
		g.appendChild(l);

		if (type === 'select') {
			var sel = document.createElement('select');
			var opts = (value && value.options) ? value.options : [];
			var cur = (value && value.value != null) ? value.value : '';
			for (var i = 0; i < opts.length; i++) {
				var opt = document.createElement('option');
				var ov, ol;
				if (opts[i] && typeof opts[i] === 'object') {
					ov = opts[i].value; ol = opts[i].label || opts[i].value;
				} else {
					ov = opts[i]; ol = opts[i];
				}
				opt.value = ov;
				opt.textContent = ol;
				if (ov === cur) opt.selected = true;
				sel.appendChild(opt);
			}
			sel.addEventListener('change', function() { onChange(sel.value); });
			g.appendChild(sel);
		} else if (type === 'textarea') {
			var ta = document.createElement('textarea');
			ta.value = value || '';
			ta.addEventListener('input', function() { onChange(ta.value); });
			g.appendChild(ta);
		} else {
			var inp = document.createElement('input');
			inp.type = type || 'text';
			inp.value = value != null ? value : '';
			if (type === 'password') inp.placeholder = '(leave blank to keep)';
			inp.addEventListener('input', function() {
				onChange(type === 'number' ? (parseInt(inp.value, 10) || 0) : inp.value);
			});
			g.appendChild(inp);
		}

		parent.appendChild(g);
		return g;
	}

	// validateField attaches a blur-time validator to a field built by
	// makeField. validator(value) returns "" for valid or an error
	// message string. The message renders into a .field-error sibling
	// inside the field's wrapper; the wrapper gets .field-with-error
	// toggled to colour the input border. Editing the field clears any
	// stale error immediately; the validator re-runs on the next blur.
	function validateField(fieldEl, validator) {
		var input = fieldEl.querySelector('input,select,textarea');
		if (!input) return;
		var errEl = document.createElement('div');
		errEl.className = 'field-error';
		fieldEl.appendChild(errEl);
		function check() {
			var msg = validator(input.value) || '';
			errEl.textContent = msg;
			if (msg) {
				fieldEl.classList.add('field-with-error');
			} else {
				fieldEl.classList.remove('field-with-error');
			}
		}
		input.addEventListener('blur', check);
		input.addEventListener('input', function() {
			if (fieldEl.classList.contains('field-with-error')) {
				fieldEl.classList.remove('field-with-error');
				errEl.textContent = '';
			}
		});
	}

	// === Helper: read-only display field (no input — shows a value with id) ===
	function makeReadOnlyField(parent, label, valueElemId, placeholder) {
		var g = document.createElement('div');
		g.className = 'form-group';
		var l = document.createElement('label');
		l.textContent = label;
		g.appendChild(l);
		var v = document.createElement('div');
		v.id = valueElemId;
		v.style.cssText = 'padding:0.5rem 0.75rem; border:1px solid var(--color-border); border-radius:var(--radius); background:var(--color-bg); font-size:0.9rem; font-family:inherit; color:var(--color-text-muted); user-select:text; min-height:1.2em; word-break:break-all;';
		v.textContent = placeholder || '';
		g.appendChild(v);
		parent.appendChild(g);
		return g;
	}

	// === Helper: tools checkbox grid for an agent ===
	function makeToolsCheckboxes(parent, idx, agent) {
		var g = document.createElement('div');
		g.className = 'form-group';
		var l = document.createElement('label');
		l.textContent = 'Allowed Tools';
		g.appendChild(l);

		var toolsFilter = document.createElement('input');
		toolsFilter.type = 'search';
		toolsFilter.className = 'inline-filter';
		toolsFilter.placeholder = 'Filter tools...';
		toolsFilter.style.cssText = 'margin-bottom:0.4rem;';
		g.appendChild(toolsFilter);

		var allow = ((agent.tools || {}).allow || []).slice();
		// Empty allow = allow all (matches Policy semantics). Render that as all-checked.
		var allowAll = allow.length === 0;

		if (availableTools.length === 0) {
			toolsFilter.style.display = 'none';
			var empty = document.createElement('div');
			empty.style.cssText = 'color:var(--color-text-muted); font-size:0.85rem; padding:0.5rem 0;';
			empty.textContent = 'No tools registered (or tools list endpoint unavailable).';
			g.appendChild(empty);
			parent.appendChild(g);
			return g;
		}

		var grid = document.createElement('div');
		grid.style.cssText = 'display:grid; grid-template-columns:repeat(auto-fill,minmax(180px,1fr)); gap:0.4rem 0.75rem; padding:0.4rem 0;';

		availableTools.forEach(function(t) {
			var lbl = document.createElement('label');
			lbl.style.cssText = 'display:flex; align-items:center; gap:0.4rem; font-size:0.85rem; cursor:pointer;';
			lbl.title = t.description || '';
			var cb = document.createElement('input');
			cb.type = 'checkbox';
			cb.checked = allowAll || allow.indexOf(t.name) >= 0;
			cb.addEventListener('change', function() {
				if (!cfg.agents.list[idx].tools) cfg.agents.list[idx].tools = {};
				var cur = (cfg.agents.list[idx].tools.allow || []).slice();
				// If it was empty (allow-all), seed with the full list before mutating.
				if (cur.length === 0) {
					cur = availableTools.map(function(x) { return x.name; });
				}
				var pos = cur.indexOf(t.name);
				if (cb.checked && pos < 0) cur.push(t.name);
				if (!cb.checked && pos >= 0) cur.splice(pos, 1);
				cfg.agents.list[idx].tools.allow = cur;
			});
			lbl.appendChild(cb);
			var span = document.createElement('span');
			span.textContent = t.name;
			lbl.appendChild(span);
			grid.appendChild(lbl);
		});

		g.appendChild(grid);

		toolsFilter.addEventListener('input', function() {
			var q = toolsFilter.value.toLowerCase();
			var labels = grid.querySelectorAll('label');
			for (var i = 0; i < labels.length; i++) {
				var name = (labels[i].querySelector('span') || {}).textContent || '';
				labels[i].style.display = (!q || name.toLowerCase().indexOf(q) !== -1) ? '' : 'none';
			}
		});

		parent.appendChild(g);
		return g;
	}

	// === Helper: 2-column row ===
	function makeRow(parent) {
		var row = document.createElement('div');
		row.className = 'form-row';
		parent.appendChild(row);
		return row;
	}

	// === Helper: panel section with optional heading ===
	function makeSection(panel, title) {
		var sec = document.createElement('div');
		sec.className = 'panel-section';
		if (title) {
			var h = document.createElement('h3');
			h.textContent = title;
			sec.appendChild(h);
		}
		panel.appendChild(sec);
		return sec;
	}

	// === Helper: scroll into view + briefly flash the most-recently-added .dynamic-item ===
	function focusAndFlashNewRow(listEl) {
		requestAnimationFrame(function() {
			var rows = listEl.querySelectorAll('.dynamic-item');
			var lastRow = rows[rows.length - 1];
			if (!lastRow) return;
			lastRow.scrollIntoView({behavior: 'smooth', block: 'center'});
			var input = lastRow.querySelector('input,select,textarea');
			if (input) input.focus();
			lastRow.classList.add('dynamic-item-new');
			setTimeout(function() { lastRow.classList.remove('dynamic-item-new'); }, 1200);
		});
	}

	// === Gateway Panel ===
	// === Messaging Panel — outbound channels (Telegram today, more later) ===
	function renderMessaging() {
		var p = document.getElementById('panel-messaging');
		p.innerHTML = '';

		var tgSec = makeSection(p, 'Telegram (send-only)');

		var help = document.createElement('p');
		help.style.cssText = 'color:var(--color-text-muted); font-size:0.85rem; margin:0 0 0.75rem 0;';
		help.innerHTML = 'Lets agents send messages to Telegram via the <code>send_message</code> tool (channel: telegram). ' +
			'Send-only — Felix does not receive Telegram messages. ' +
			'After saving, configuration is hot-reloaded — then add <code>send_message</code> to the allow list of any agent that should use it (Agents tab).';
		tgSec.appendChild(help);

		var setupHdr = document.createElement('div');
		setupHdr.style.cssText = 'font-weight:600; font-size:0.85rem; margin:0.5rem 0 0.25rem 0;';
		setupHdr.textContent = 'Setup';
		tgSec.appendChild(setupHdr);

		var setup = document.createElement('ol');
		setup.style.cssText = 'color:var(--color-text-muted); font-size:0.8rem; margin:0 0 0.75rem 1.25rem; padding:0; line-height:1.5;';
		setup.innerHTML =
			'<li>Create a bot with <a href="https://t.me/BotFather" target="_blank" rel="noopener">@BotFather</a> (<code>/newbot</code>) and copy the token into Bot Token below.</li>' +
			'<li>Get a recipient chat ID — three options:' +
				'<ul style="margin:0.25rem 0 0.25rem 1.25rem; padding:0;">' +
				'<li>Easiest: open Telegram, message <a href="https://t.me/userinfobot" target="_blank" rel="noopener">@userinfobot</a> — it replies with your numeric chat ID.</li>' +
				'<li>Or: have the recipient message your bot at least once, then open <code>https://api.telegram.org/bot&lt;TOKEN&gt;/getUpdates</code> in a browser and copy <code>result[].message.chat.id</code>.</li>' +
				'<li>Or: forward a message from the recipient to <a href="https://t.me/getidsbot" target="_blank" rel="noopener">@getidsbot</a>.</li>' +
				'</ul></li>' +
			'<li>Paste the chat ID into Default Chat ID below — the agent uses it whenever it omits an explicit recipient.</li>';
		tgSec.appendChild(setup);

		var caveat = document.createElement('p');
		caveat.style.cssText = 'color:var(--color-text-muted); font-size:0.8rem; margin:0 0 0.75rem 0; padding:0.5rem 0.75rem; background:var(--color-surface-muted, rgba(0,0,0,0.04)); border-radius:var(--radius);';
		caveat.innerHTML =
			'<strong>Important:</strong> a Telegram bot cannot send the first message to a personal user — the user must <code>/start</code> the bot (or send any message) at least once first. Otherwise Telegram returns "Forbidden: bot can\'t initiate conversation with a user." ' +
			'Also: <code>@username</code> as a chat ID works only for <strong>public channels and supergroups</strong> the bot is in — not for personal users. For people, always use the numeric ID.';
		tgSec.appendChild(caveat);

		var tg = cfg.telegram || {};
		makeField(tgSec, 'Enabled', 'toggle', !!tg.enabled, function(v) {
			if (!cfg.telegram) cfg.telegram = {};
			cfg.telegram.enabled = v;
		});
		makeField(tgSec, 'Bot Token', 'password', '', function(v) {
			if (!v) return;
			if (!cfg.telegram) cfg.telegram = {};
			cfg.telegram.bot_token = v;
		});
		makeField(tgSec, 'Default Chat ID', 'text', tg.default_chat_id || '', function(v) {
			if (!cfg.telegram) cfg.telegram = {};
			cfg.telegram.default_chat_id = v;
		});

		var note = document.createElement('p');
		note.style.cssText = 'color:var(--color-text-muted); font-size:0.8rem; margin:0.5rem 0 0 0;';
		note.innerHTML = 'Default Chat ID is used when the agent omits <code>chat_id</code>. Personal users: positive numeric ID (e.g. <code>123456789</code>). Groups/supergroups: negative ID (e.g. <code>-1001234567890</code>). Public channels/supergroups only: <code>@channelname</code>.';
		tgSec.appendChild(note);
	}

	function renderMCP() {
		var p = document.getElementById('panel-mcp');
		p.innerHTML = '';
		if (!renderMCP._expanded) renderMCP._expanded = {};
		var expanded = renderMCP._expanded;
		var sec = makeSection(p, 'MCP Servers');

		var help = document.createElement('p');
		help.style.cssText = 'color:var(--color-text-muted); font-size:0.85rem; margin:0 0 0.5rem 0;';
		help.innerHTML = 'Model Context Protocol servers Felix connects to at startup. ' +
			'Each server\'s tools become available to agents alongside core tools (with the optional <code>tool_prefix</code> applied). ' +
			'Two transports: <strong>HTTP</strong> (Streamable HTTP, e.g. AWS Bedrock AgentCore) and <strong>stdio</strong> ' +
			'(spawn a local subprocess, e.g. <code>npx @modelcontextprotocol/server-github</code>).';
		sec.appendChild(help);

		var caveat = document.createElement('p');
		caveat.style.cssText = 'color:var(--color-text-muted); font-size:0.8rem; margin:0 0 0.75rem 0; padding:0.5rem 0.75rem; background:var(--color-surface-muted, rgba(0,0,0,0.04)); border-radius:var(--radius);';
		caveat.innerHTML =
			'<strong>Note:</strong> secrets (HTTP client secret, bearer token) are stored in <code>~/.felix/felix.json5</code> ' +
			'alongside other secrets (<code>telegram.bot_token</code>, <code>providers.*.api_key</code>). MCP config changes ' +
			'require a process restart — hot reload of MCP servers is not yet supported.';
		sec.appendChild(caveat);

		var servers = cfg.mcp_servers || [];
		var list = document.createElement('div');
		list.className = 'dynamic-list';
		sec.appendChild(list);

		// Migrate any legacy flat HTTP entries into the nested http block on
		// first render. Invisible to the user; subsequent saves emit only the
		// nested form. This is the user-initiated migration path — opening
		// the Settings UI is itself the user action.
		for (var mi = 0; mi < servers.length; mi++) {
			var ms = servers[mi];
			if (!ms.transport && (ms.url || (ms.auth && ms.auth.kind))) {
				ms.transport = 'http';
				ms.http = {url: ms.url || '', auth: ms.auth || {}};
				delete ms.url;
				delete ms.auth;
			}
		}

		for (var i = 0; i < servers.length; i++) {
			(function(idx) {
				var s = servers[idx];
				if (!s.transport) s.transport = 'http';
				if (s.transport === 'http') {
					if (!s.http) s.http = {url: '', auth: {kind: 'oauth2_client_credentials'}};
					if (!s.http.auth) s.http.auth = {kind: 'oauth2_client_credentials'};
				} else if (s.transport === 'stdio') {
					if (!s.stdio) s.stdio = {command: '', args: [], env: {}};
				}

				var item = document.createElement('div');
				item.className = 'dynamic-item mcp-card';

				var isOnly = servers.length === 1;
				var isJustAdded = !!s.__justAdded;
				var startsExpanded = isOnly || isJustAdded || !!expanded[idx];

				// === Header ===
				var header = document.createElement('div');
				header.className = 'collapse-card-header';

				var chevron = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
				chevron.setAttribute('viewBox', '0 0 24 24');
				chevron.setAttribute('class', 'collapse-card-chevron');
				chevron.innerHTML = '<path d="M6 9l6 6 6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>';
				header.appendChild(chevron);

				var hId = document.createElement('span');
				hId.className = 'collapse-card-id';
				hId.textContent = s.id || '(new)';
				header.appendChild(hId);

				var hMeta = document.createElement('span');
				hMeta.className = 'collapse-card-meta';
				hMeta.textContent = s.transport || 'http';
				header.appendChild(hMeta);

				var dot = document.createElement('span');
				dot.className = 'collapse-card-dot';
				dot.dataset.on = s.enabled ? 'true' : 'false';
				dot.title = s.enabled ? 'Enabled' : 'Disabled';
				header.appendChild(dot);

				var rm = document.createElement('button');
				rm.className = 'remove-btn';
				rm.innerHTML = '&times;';
				rm.onclick = function(e) {
					e.stopPropagation();
					cfg.mcp_servers.splice(idx, 1);
					renderMCP._expanded = {};
					render();
				};
				header.appendChild(rm);

				item.appendChild(header);

				if (isOnly) {
					chevron.style.visibility = 'hidden';
					header.style.cursor = 'default';
				} else {
					header.addEventListener('click', function(e) {
						if (e.target.closest('button, input, select, textarea')) return;
						expanded[idx] = !item.classList.contains('collapsed') ? false : true;
						item.classList.toggle('collapsed');
					});
				}

				// === Body ===
				var body = document.createElement('div');
				body.className = 'collapse-card-body';
				item.appendChild(body);

				var row1 = makeRow(body);
				makeField(row1, 'ID', 'text', s.id || '', function(v) {
					cfg.mcp_servers[idx].id = v;
					hId.textContent = v || '(new)';
				});
				makeField(row1, 'Tool Prefix', 'text', s.tool_prefix || '', function(v) { cfg.mcp_servers[idx].tool_prefix = v; });

				makeField(body, 'Transport', 'select', {
					value: s.transport,
					options: [
						{value: 'http', label: 'HTTP (Streamable)'},
						{value: 'stdio', label: 'stdio (subprocess)'}
					]
				}, function(v) {
					cfg.mcp_servers[idx].transport = v;
					if (v === 'stdio' && !cfg.mcp_servers[idx].stdio) {
						cfg.mcp_servers[idx].stdio = {command: '', args: [], env: {}};
					}
					if (v === 'http' && !cfg.mcp_servers[idx].http) {
						cfg.mcp_servers[idx].http = {url: '', auth: {kind: 'oauth2_client_credentials'}};
					}
					render();
				});

				makeField(body, 'Enabled', 'toggle', !!s.enabled, function(v) {
					cfg.mcp_servers[idx].enabled = v;
					dot.dataset.on = v ? 'true' : 'false';
					dot.title = v ? 'Enabled' : 'Disabled';
				});
				makeField(body, 'Parallel-safe', 'toggle', !!s.parallelSafe, function(v) { cfg.mcp_servers[idx].parallelSafe = v; });

				if (s.transport === 'http') {
					renderHTTPBlock(body, idx, s);
				} else if (s.transport === 'stdio') {
					renderStdioBlock(body, idx, s);
				}

				// === Apply collapsed state ===
				var hasErr = body.querySelector('.field-with-error');
				if (!isOnly && !startsExpanded && !hasErr) {
					item.classList.add('collapsed');
				}
				if (s.__justAdded) delete cfg.mcp_servers[idx].__justAdded;

				list.appendChild(item);
			})(i);
		}

		var addBtn = document.createElement('button');
		addBtn.className = 'add-btn';
		addBtn.textContent = '+ Add MCP Server';
		addBtn.onclick = function() {
			if (!cfg.mcp_servers) cfg.mcp_servers = [];
			cfg.mcp_servers.push({
				id: '',
				transport: 'http',
				http: {
					url: '',
					auth: {
						kind: 'oauth2_client_credentials',
						token_url: '',
						client_id: '',
						client_secret: '',
						scope: ''
					}
				},
				enabled: true,
				parallelSafe: false,
				tool_prefix: '',
				__justAdded: true
			});
			render();
			focusAndFlashNewRow(list);
		};
		sec.appendChild(addBtn);
	}

	function renderHTTPBlock(item, idx, s) {
		makeField(item, 'URL', 'text', s.http.url || '', function(v) { cfg.mcp_servers[idx].http.url = v; });

		var authHdr = document.createElement('div');
		authHdr.style.cssText = 'font-weight:600; font-size:0.85rem; margin:0.75rem 0 0.25rem 0;';
		authHdr.textContent = 'Authentication';
		item.appendChild(authHdr);

		makeField(item, 'Auth Kind', 'select', {
			value: s.http.auth.kind || 'oauth2_client_credentials',
			options: [
				{value: 'oauth2_client_credentials', label: 'OAuth2 Client Credentials (M2M)'},
				{value: 'oauth2_authorization_code', label: 'OAuth2 Authorization Code + PKCE (interactive login)'},
				{value: 'bearer', label: 'Bearer Token'},
				{value: 'none', label: 'None'}
			]
		}, function(v) {
			cfg.mcp_servers[idx].http.auth = {kind: v};
			render();
		});

		var kind = s.http.auth.kind || 'oauth2_client_credentials';
		if (kind === 'oauth2_client_credentials') {
			makeField(item, 'Token URL', 'text', s.http.auth.token_url || '', function(v) {
				cfg.mcp_servers[idx].http.auth.token_url = v;
			});
			var row = makeRow(item);
			makeField(row, 'Client ID', 'text', s.http.auth.client_id || '', function(v) {
				cfg.mcp_servers[idx].http.auth.client_id = v;
			});
			makeField(row, 'Scope', 'text', s.http.auth.scope || '', function(v) {
				cfg.mcp_servers[idx].http.auth.scope = v;
			});
			makeField(item, 'Client Secret', 'password', s.http.auth.client_secret || '', function(v) {
				if (!v) return;
				cfg.mcp_servers[idx].http.auth.client_secret = v;
			});
			var hint = document.createElement('p');
			hint.style.cssText = 'color:var(--color-text-muted); font-size:0.75rem; margin:0.25rem 0 0 0;';
			hint.innerHTML = 'Stored in <code>felix.json5</code>. To source from an env var instead, leave blank and set <code>auth.client_secret_env</code> in the JSON5 file.';
			item.appendChild(hint);
		} else if (kind === 'oauth2_authorization_code') {
			makeField(item, 'Authorize URL', 'text', s.http.auth.auth_url || '', function(v) {
				cfg.mcp_servers[idx].http.auth.auth_url = v;
			});
			makeField(item, 'Token URL', 'text', s.http.auth.token_url || '', function(v) {
				cfg.mcp_servers[idx].http.auth.token_url = v;
			});
			var rowAC = makeRow(item);
			makeField(rowAC, 'Client ID', 'text', s.http.auth.client_id || '', function(v) {
				cfg.mcp_servers[idx].http.auth.client_id = v;
			});
			makeField(rowAC, 'Scope', 'text', s.http.auth.scope || '', function(v) {
				cfg.mcp_servers[idx].http.auth.scope = v;
			});
			makeField(item, 'Redirect URI', 'text', s.http.auth.redirect_uri || '', function(v) {
				cfg.mcp_servers[idx].http.auth.redirect_uri = v;
			});
			makeField(item, 'Client Secret', 'password', s.http.auth.client_secret || '', function(v) {
				if (!v) return;
				cfg.mcp_servers[idx].http.auth.client_secret = v;
			});
			var hintAC = document.createElement('p');
			hintAC.style.cssText = 'color:var(--color-text-muted); font-size:0.75rem; margin:0.25rem 0 0 0;';
			hintAC.innerHTML =
				'Interactive OAuth login. Redirect URI must be a loopback URL like ' +
				'<code>http://localhost:12341/callback</code> registered with the IdP. ' +
				'Scope defaults to <code>openid offline_access</code> when blank (so refresh tokens work). ' +
				'Some IdPs (Cognito) require a client secret even for PKCE clients; pure public clients can leave it blank. ' +
				'After saving, run <code>felix mcp login ' + (s.id || '&lt;id&gt;') + '</code> in a terminal to complete the browser dance — ' +
				'the gateway caches the token under <code>~/.felix/mcp-tokens/</code> and refreshes it silently after that. ' +
				'A restart is required to pick up a freshly minted token.';
			item.appendChild(hintAC);
		} else if (kind === 'bearer') {
			makeField(item, 'Bearer Token', 'password', s.http.auth.token || '', function(v) {
				if (!v) return;
				cfg.mcp_servers[idx].http.auth.token = v;
			});
			var hintB = document.createElement('p');
			hintB.style.cssText = 'color:var(--color-text-muted); font-size:0.75rem; margin:0.25rem 0 0 0;';
			hintB.innerHTML = 'Sent as <code>Authorization: Bearer &lt;token&gt;</code>. Stored in <code>felix.json5</code>; to source from an env var instead, leave blank and set <code>auth.token_env</code> in the JSON5 file.';
			item.appendChild(hintB);
		} else if (kind === 'none') {
			var hintN = document.createElement('p');
			hintN.style.cssText = 'color:var(--color-text-muted); font-size:0.75rem; margin:0.25rem 0 0 0;';
			hintN.textContent = 'No Authorization header sent. Useful only for unauthenticated local HTTP MCP servers.';
			item.appendChild(hintN);
		}
	}

	function renderStdioBlock(item, idx, s) {
		makeField(item, 'Command', 'text', s.stdio.command || '', function(v) {
			cfg.mcp_servers[idx].stdio.command = v;
		});

		var argsTxt = (s.stdio.args || []).join('\n');
		makeField(item, 'Arguments (one per line)', 'textarea', argsTxt, function(v) {
			var lines = (v || '').split('\n').map(function(x) { return x.trim(); }).filter(function(x) { return x.length > 0; });
			cfg.mcp_servers[idx].stdio.args = lines;
		});

		var envTxt = '';
		if (s.stdio.env) {
			var keys = Object.keys(s.stdio.env);
			for (var k = 0; k < keys.length; k++) {
				envTxt += keys[k] + '=' + s.stdio.env[keys[k]] + '\n';
			}
			envTxt = envTxt.replace(/\n$/, '');
		}
		makeField(item, 'Environment (KEY=VALUE per line; secrets shown as ***redacted*** — leave that to keep, replace to overwrite)', 'textarea', envTxt, function(v) {
			var env = {};
			var lines = (v || '').split('\n');
			for (var li = 0; li < lines.length; li++) {
				var line = lines[li].trim();
				if (!line || line.charAt(0) === '#') continue;
				var eq = line.indexOf('=');
				if (eq < 0) continue;
				env[line.slice(0, eq).trim()] = line.slice(eq + 1);
			}
			cfg.mcp_servers[idx].stdio.env = env;
		});

		var hintS = document.createElement('p');
		hintS.style.cssText = 'color:var(--color-text-muted); font-size:0.75rem; margin:0.25rem 0 0 0;';
		hintS.innerHTML = 'The command is launched on Felix startup. Env vars are merged onto Felix\'s own environment (PATH inherited). Common examples: <code>npx -y @modelcontextprotocol/server-filesystem /tmp</code>, <code>uvx mcp-server-git</code>.';
		item.appendChild(hintS);
	}

	function renderGateway() {
		var p = document.getElementById('panel-gateway');
		p.innerHTML = '';

		// Host/Port/Auth Token/Reload Mode are operator-controlled and not
		// safe for tenants to change at runtime, so they're omitted here.

		// === OpenTelemetry ===
		// Felix can additionally export traces, metrics, and logs to an
		// OTLP/HTTP collector. Disabled by default. Standard OTEL_*
		// environment variables (OTEL_EXPORTER_OTLP_ENDPOINT,
		// OTEL_SERVICE_NAME, OTEL_EXPORTER_OTLP_HEADERS, ...) override
		// the values set here on the next process start. Settings here
		// require a restart to take effect — OTel SDK providers can't
		// safely swap mid-flight.
		if (!cfg.otel) cfg.otel = {};
		if (!cfg.otel.signals) cfg.otel.signals = {traces: true, metrics: true, logs: true};
		var otelSec = makeSection(p, 'OpenTelemetry');
		var otelNote = document.createElement('div');
		otelNote.className = 'note';
		otelNote.style.marginBottom = '8px';
		otelNote.style.opacity = '0.75';
		otelNote.textContent = 'Restart required to apply changes.';
		otelSec.appendChild(otelNote);

		makeField(otelSec, 'Enabled', 'toggle', !!cfg.otel.enabled, function(v) {
			cfg.otel.enabled = v;
		});
		makeField(otelSec, 'Endpoint (full URL, e.g. http://collector:4318/)', 'text',
			cfg.otel.endpoint || '', function(v) { cfg.otel.endpoint = v.trim(); });

		var otelRow = makeRow(otelSec);
		makeField(otelRow, 'Service Name', 'text', cfg.otel.serviceName || 'felix', function(v) {
			cfg.otel.serviceName = v;
		});
		makeField(otelRow, 'Sample Ratio (0..1)', 'number',
			cfg.otel.sampleRatio == null ? 1.0 : cfg.otel.sampleRatio,
			function(v) { cfg.otel.sampleRatio = parseFloat(v) || 0; });

		var sigRow = makeRow(otelSec);
		makeField(sigRow, 'Traces', 'toggle', cfg.otel.signals.traces !== false, function(v) {
			cfg.otel.signals.traces = v;
		});
		makeField(sigRow, 'Metrics', 'toggle', cfg.otel.signals.metrics !== false, function(v) {
			cfg.otel.signals.metrics = v;
		});
		makeField(sigRow, 'Logs', 'toggle', cfg.otel.signals.logs !== false, function(v) {
			cfg.otel.signals.logs = v;
		});

		// Headers — comma-separated key=value pairs. Round-trip in/out of
		// the cfg.otel.headers map. Most users won't touch this.
		var hdrLines = [];
		if (cfg.otel.headers) {
			for (var hk in cfg.otel.headers) {
				if (Object.prototype.hasOwnProperty.call(cfg.otel.headers, hk)) {
					hdrLines.push(hk + '=' + cfg.otel.headers[hk]);
				}
			}
		}
		makeField(otelSec, 'Headers (key=value, comma-separated; e.g. X-Scope-OrgID=tenant1)', 'text',
			hdrLines.join(', '),
			function(v) {
				var out = {};
				var parts = (v || '').split(',');
				for (var pi = 0; pi < parts.length; pi++) {
					var s = parts[pi].trim();
					if (!s) continue;
					var eq = s.indexOf('=');
					if (eq < 0) continue;
					out[s.substring(0, eq).trim()] = s.substring(eq + 1).trim();
				}
				cfg.otel.headers = out;
			});
	}

	// === Providers Panel ===
	function renderProviders() {
		var p = document.getElementById('panel-providers');
		p.innerHTML = '';
		if (!renderProviders._expanded) renderProviders._expanded = {};
		var expanded = renderProviders._expanded;
		var sec = makeSection(p, null);
		var providers = cfg.providers || {};
		var names = Object.keys(providers);
		var list = document.createElement('div');
		list.className = 'dynamic-list';
		sec.appendChild(list);

		for (var i = 0; i < names.length; i++) {
			(function(name) {
				var prov = providers[name];
				var item = document.createElement('div');
				item.className = 'dynamic-item provider-card';

				var isOnly = names.length === 1;
				var isJustAdded = !!(prov && prov._isNew);
				var startsExpanded = isOnly || isJustAdded || !!expanded[name];

				// === Header ===
				var header = document.createElement('div');
				header.className = 'collapse-card-header';

				var chevron = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
				chevron.setAttribute('viewBox', '0 0 24 24');
				chevron.setAttribute('class', 'collapse-card-chevron');
				chevron.innerHTML = '<path d="M6 9l6 6 6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>';
				header.appendChild(chevron);

				// Title: editable input for _isNew, plain span otherwise.
				var title;
				if (prov && prov._isNew) {
					title = document.createElement('input');
					title.className = 'collapse-card-id';
					title.type = 'text';
					title.value = name;
					title.placeholder = 'Provider name (e.g. anthropic, openai)';
					title.addEventListener('blur', function() {
						var newName = title.value.trim();
						if (!newName || newName === name) return;
						if (cfg.providers[newName]) {
							title.classList.add('field-with-error');
							return;
						}
						cfg.providers[newName] = cfg.providers[name];
						delete cfg.providers[newName]._isNew;
						delete cfg.providers[name];
						// Carry expand state across rename so the renamed card stays open.
						expanded[newName] = true;
						render();
					});
				} else {
					title = document.createElement('span');
					title.className = 'collapse-card-id';
					title.textContent = name;
				}
				header.appendChild(title);

				var hKind = document.createElement('span');
				hKind.className = 'collapse-card-meta';
				hKind.textContent = prov.kind || '(no kind)';
				header.appendChild(hKind);

				var keyBadge = document.createElement('span');
				keyBadge.className = 'collapse-card-key-badge';
				var hasKey = !!(prov.api_key || prov._hasKey);
				keyBadge.dataset.set = hasKey ? 'true' : 'false';
				keyBadge.textContent = hasKey ? 'key set' : 'no key';
				header.appendChild(keyBadge);

				var rm = document.createElement('button');
				rm.className = 'remove-btn';
				rm.innerHTML = '&times;';
				rm.onclick = function(e) {
					e.stopPropagation();
					delete cfg.providers[name];
					renderProviders._expanded = {};
					render();
				};
				header.appendChild(rm);

				item.appendChild(header);

				if (isOnly) {
					chevron.style.visibility = 'hidden';
					header.style.cursor = 'default';
				} else {
					header.addEventListener('click', function(e) {
						if (e.target.closest('button, input, select, textarea')) return;
						expanded[name] = !item.classList.contains('collapsed') ? false : true;
						item.classList.toggle('collapsed');
					});
				}

				// === Body ===
				var body = document.createElement('div');
				body.className = 'collapse-card-body';
				item.appendChild(body);

				var row = makeRow(body);
				row.style.gridTemplateColumns = 'minmax(180px, 0.3fr) 1fr';
				makeField(row, 'Kind', 'select', {
					value: prov.kind || '',
					options: [
						{value: '', label: '(choose)'},
						{value: 'anthropic', label: 'anthropic'},
						{value: 'openai', label: 'openai'},
						{value: 'openai-compatible', label: 'openai-compatible'},
						{value: 'gemini', label: 'gemini'},
						{value: 'qwen', label: 'qwen'},
						{value: 'local', label: 'local'},
					],
				}, function(v) {
					cfg.providers[name].kind = v;
					hKind.textContent = v || '(no kind)';
				});
				var urlField = makeField(row, 'Base URL', 'text', prov.base_url || '', function(v) { cfg.providers[name].base_url = v; });
				var urlInput = urlField.querySelector('input');
				urlInput.setAttribute('type', 'url');
				urlInput.setAttribute('inputmode', 'url');
				validateField(urlField, function(v) {
					v = (v || '').trim();
					if (!v) return '';
					var u;
					try { u = new URL(v); } catch (e) { return 'Not a valid URL.'; }
					if (u.protocol !== 'https:' && u.protocol !== 'http:') {
						return 'URL must use https:// (or http:// for local dev).';
					}
					return '';
				});
				makeField(body, 'API Key', 'password', '', function(v) {
					if (v) {
						cfg.providers[name].api_key = v;
						keyBadge.dataset.set = 'true';
						keyBadge.textContent = 'key set';
					}
				});

				// === Apply collapsed state ===
				var hasErr = body.querySelector('.field-with-error');
				if (!isOnly && !startsExpanded && !hasErr) {
					item.classList.add('collapsed');
				}

				list.appendChild(item);
			})(names[i]);
		}

		var addBtn = document.createElement('button');
		addBtn.className = 'add-btn';
		addBtn.textContent = '+ Add Provider';
		addBtn.onclick = function() {
			if (!cfg.providers) cfg.providers = {};
			var i = 1;
			while (cfg.providers['new-provider-' + i]) i++;
			var placeholder = 'new-provider-' + i;
			cfg.providers[placeholder] = {kind: '', api_key: '', base_url: '', _isNew: true};
			render();
			focusAndFlashNewRow(list);
		};
		sec.appendChild(addBtn);
	}

	// === Models tab — talks directly to bundled Ollama via providers.local.base_url ===
	var CURATED_MODELS = [
		{name: 'gemma4:latest',     label: 'Gemma 4 (multimodal)',     size: '~9.6 GB', note: 'recommended — vision + general agent'},
		{name: 'qwen3.5:9b',        label: 'Qwen 3.5 9B',              size: '~5.0 GB', note: 'lighter, text-only'},
		{name: 'nomic-embed-text',  label: 'Nomic Embed Text',         size: '~274 MB', note: 'embeddings — recommended for memory'},
		{name: 'mxbai-embed-large', label: 'MixedBread Embed Large',   size: '~670 MB', note: 'embeddings — higher quality'}
	];
	var pullState = {}; // name -> {pct, status, err, source}
	var pollTimer = null;
	var bootstrapTimer = null;

	function ollamaBase() {
		var base = (cfg.providers && cfg.providers.local && cfg.providers.local.base_url) || 'http://127.0.0.1:18790';
		return base.replace(/\/v1\/?$/, '').replace(/\/$/, '');
	}

	function fmtBytes(n) {
		if (!n || n < 0) return '';
		if (n < 1024) return n + ' B';
		var u = ['KB','MB','GB','TB'];
		var i = -1;
		do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
		return n.toFixed(1) + ' ' + u[i];
	}

	function renderModels() {
		var panel = document.getElementById('panel-models');
		panel.innerHTML = '';

		var section = document.createElement('div');
		section.className = 'panel-section';
		var h = document.createElement('h3');
		h.textContent = 'Local models';
		section.appendChild(h);

		var p = document.createElement('p');
		p.style.cssText = 'color:var(--color-text-muted); font-size:0.85rem; margin:0.25rem 0 1rem 0;';
		p.textContent = 'Endpoint: ' + ollamaBase();
		section.appendChild(p);

		// Installed list
		var installedHdr = document.createElement('div');
		installedHdr.style.cssText = 'font-weight:600; font-size:0.9rem; margin-bottom:0.5rem;';
		installedHdr.textContent = 'Installed';
		section.appendChild(installedHdr);

		var installedBox = document.createElement('div');
		installedBox.id = 'models-installed';
		installedBox.style.cssText = 'border:1px solid var(--color-border); border-radius:var(--radius); padding:0.5rem; margin-bottom:1.5rem; min-height:2.5rem;';
		installedBox.textContent = 'Loading…';
		section.appendChild(installedBox);

		// Curated download list
		var availHdr = document.createElement('div');
		availHdr.style.cssText = 'font-weight:600; font-size:0.9rem; margin-bottom:0.5rem;';
		availHdr.textContent = 'Available to download';
		section.appendChild(availHdr);

		var grid = document.createElement('div');
		grid.style.cssText = 'display:grid; grid-template-columns:1fr; gap:0.75rem;';
		CURATED_MODELS.forEach(function(m) {
			var card = document.createElement('div');
			card.style.cssText = 'border:1px solid var(--color-border); border-radius:var(--radius); padding:0.75rem;';

			var top = document.createElement('div');
			top.style.cssText = 'display:flex; justify-content:space-between; align-items:center; gap:0.5rem;';
			var info = document.createElement('div');
			var nameLine = document.createElement('div');
			nameLine.style.cssText = 'font-weight:600;';
			nameLine.textContent = m.label + ' (' + m.name + ')';
			var sub = document.createElement('div');
			sub.style.cssText = 'color:var(--color-text-muted); font-size:0.8rem;';
			sub.textContent = m.size + ' • ' + m.note;
			info.appendChild(nameLine);
			info.appendChild(sub);

			var btn = document.createElement('button');
			btn.className = 'btn';
			btn.dataset.model = m.name;
			btn.textContent = 'Download';
			btn.addEventListener('click', function() { startPull(m.name); });

			top.appendChild(info);
			top.appendChild(btn);
			card.appendChild(top);

			var prog = document.createElement('div');
			prog.id = 'pull-progress-' + m.name;
			prog.style.cssText = 'margin-top:0.5rem; display:none;';
			prog.innerHTML = '<div style="font-size:0.8rem; color:var(--color-text-muted); margin-bottom:0.25rem;" class="progress-text">Starting…</div>' +
				'<div style="height:6px; background:var(--color-border); border-radius:3px; overflow:hidden;"><div class="progress-bar" style="height:100%; width:0%; background:var(--color-accent, #3b82f6); transition:width 0.3s;"></div></div>';
			card.appendChild(prog);

			grid.appendChild(card);
		});
		section.appendChild(grid);

		panel.appendChild(section);

		refreshInstalled();
		refreshBootstrap();
		// Apply any in-flight pull state in case the user switched tabs and back.
		Object.keys(pullState).forEach(function(name) { applyPullState(name); });
	}

	// === First-run bootstrap polling — surface auto-pulls so users see progress ===
	function refreshBootstrap() {
		fetch('/settings/api/bootstrap', {cache: 'no-store'})
			.then(function(r) { return r.ok ? r.json() : null; })
			.then(function(snap) {
				if (!snap || !snap.models) return;
				var stillActive = false;
				Object.keys(snap.models).forEach(function(name) {
					var m = snap.models[name];
					var st = pullState[name] || {};
					// Don't overwrite a user-initiated pull already in flight.
					if (st.source === 'user') return;
					st.source = 'bootstrap';
					st.status = m.status;
					st.completed = m.completed;
					st.total = m.total;
					st.pct = m.pct;
					st.err = m.error;
					if (m.status === 'done') st.done = true;
					pullState[name] = st;
					if (m.status === 'queued' || m.status === 'downloading') {
						stillActive = true;
					}
					applyPullState(name);
					// Once a bootstrap pull completes, fade the progress UI after a
					// short delay so the model just appears in the Installed list.
					if (m.status === 'done' && !st._cleared) {
						st._cleared = true;
						setTimeout(function() {
							delete pullState[name];
							applyPullState(name);
						}, 3000);
					}
				});
				// On any change to "done", refresh the installed list.
				refreshInstalled();
				if (bootstrapTimer) { clearTimeout(bootstrapTimer); bootstrapTimer = null; }
				if (stillActive || snap.active) {
					bootstrapTimer = setTimeout(refreshBootstrap, 1500);
				}
			})
			.catch(function() { /* endpoint absent or transient — ignore */ });
	}

	function refreshInstalled() {
		var box = document.getElementById('models-installed');
		if (!box) return;
		fetch(ollamaBase() + '/api/tags')
			.then(function(r) { return r.json(); })
			.then(function(data) {
				var models = (data && data.models) || [];
				if (models.length === 0) {
					box.textContent = 'No models installed yet.';
					return;
				}
				box.innerHTML = '';
				models.forEach(function(m) {
					var row = document.createElement('div');
					row.style.cssText = 'display:flex; justify-content:space-between; align-items:center; gap:0.5rem; padding:0.4rem 0.25rem; border-bottom:1px solid var(--color-border);';
					var nm = document.createElement('div');
					nm.style.cssText = 'flex:1; min-width:0; word-break:break-all;';
					nm.textContent = m.name;
					var sz = document.createElement('div');
					sz.style.cssText = 'color:var(--color-text-muted); font-size:0.85rem;';
					sz.textContent = fmtBytes(m.size);
					var rm = document.createElement('button');
					rm.className = 'btn';
					rm.textContent = 'Remove';
					rm.style.cssText = 'padding:0.25rem 0.6rem; font-size:0.8rem;';
					rm.addEventListener('click', function() { removeInstalledModel(m.name); });
					row.appendChild(nm);
					row.appendChild(sz);
					row.appendChild(rm);
					box.appendChild(row);
				});
				box.lastChild.style.borderBottom = 'none';
			})
			.catch(function(err) {
				box.textContent = 'Error: ' + err.message + ' — is the bundled Ollama running?';
			});
	}

	function removeInstalledModel(name) {
		if (!confirm('Remove model "' + name + '"? This deletes it from the bundled Ollama store.')) return;
		fetch(ollamaBase() + '/api/delete', {
			method: 'DELETE',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({name: name})
		}).then(function(r) {
			if (!r.ok) {
				return r.text().then(function(t) {
					alert('Remove failed: ' + (t || ('HTTP ' + r.status)));
				});
			}
			refreshInstalled();
		}).catch(function(err) {
			alert('Remove failed: ' + err.message);
		});
	}

	function applyPullState(name) {
		var st = pullState[name];
		var prog = document.getElementById('pull-progress-' + name);
		var btn = document.querySelector('button[data-model="' + name + '"]');
		if (!prog || !btn) return;
		if (!st) {
			prog.style.display = 'none';
			btn.disabled = false;
			btn.textContent = 'Download';
			return;
		}
		prog.style.display = 'block';
		btn.disabled = true;
		btn.textContent = 'Downloading…';
		var bar = prog.querySelector('.progress-bar');
		var txt = prog.querySelector('.progress-text');
		if (bar) bar.style.width = (st.pct || 0) + '%';
		if (txt) {
			var label = st.status || 'pulling';
			if (st.completed && st.total) {
				label += ' — ' + fmtBytes(st.completed) + ' / ' + fmtBytes(st.total) + ' (' + (st.pct || 0).toFixed(1) + '%)';
			} else if (st.pct != null) {
				label += ' — ' + st.pct.toFixed(1) + '%';
			}
			if (st.err) label = 'Error: ' + st.err;
			txt.textContent = label;
		}
	}

	function startPull(name) {
		if (pullState[name] && !pullState[name].err && !pullState[name].done) return;
		pullState[name] = {pct: 0, status: 'starting', source: 'user'};
		applyPullState(name);

		fetch(ollamaBase() + '/api/pull', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({name: name, stream: true})
		}).then(function(resp) {
			if (!resp.ok || !resp.body) {
				pullState[name].err = 'HTTP ' + resp.status;
				applyPullState(name);
				return;
			}
			var reader = resp.body.getReader();
			var decoder = new TextDecoder();
			var buf = '';
			function read() {
				return reader.read().then(function(chunk) {
					if (chunk.done) {
						pullState[name].done = true;
						pullState[name].pct = 100;
						pullState[name].status = 'done';
						applyPullState(name);
						refreshInstalled();
						setTimeout(function() { delete pullState[name]; applyPullState(name); }, 3000);
						return;
					}
					buf += decoder.decode(chunk.value, {stream: true});
					var lines = buf.split('\n');
					buf = lines.pop();
					lines.forEach(function(line) {
						if (!line.trim()) return;
						try {
							var ev = JSON.parse(line);
							var st = pullState[name];
							st.status = ev.status || st.status;
							if (typeof ev.total === 'number') st.total = ev.total;
							if (typeof ev.completed === 'number') st.completed = ev.completed;
							if (st.total > 0) st.pct = (st.completed || 0) * 100 / st.total;
							if (ev.error) st.err = ev.error;
							applyPullState(name);
						} catch (e) { /* ignore unparsable line */ }
					});
					return read();
				});
			}
			return read();
		}).catch(function(err) {
			pullState[name].err = err.message;
			applyPullState(name);
		});
	}


	// === Agents Panel ===
	function renderAgents() {
		var p = document.getElementById('panel-agents');
		p.innerHTML = '';
		var sec = makeSection(p, null);
		var agents = (cfg.agents || {}).list || [];
		var list = document.createElement('div');
		list.className = 'dynamic-list';
		sec.appendChild(list);

		// Per-card expanded state. Map key = agent index. Closure-local —
		// rebuilt every render(). Force-rules below override.
		if (!renderAgents._expanded) renderAgents._expanded = {};
		var expanded = renderAgents._expanded;

		for (var i = 0; i < agents.length; i++) {
			(function(idx) {
				var a = agents[idx];
				var item = document.createElement('div');
				item.className = 'dynamic-item agent-card';

				var isOnly = agents.length === 1;
				var isJustAdded = !!a.__justAdded;
				var startsExpanded = isOnly || isJustAdded || !!a.default || !!expanded[idx];

				// === Header (always visible; click toggles when not isOnly) ===
				var header = document.createElement('div');
				header.className = 'collapse-card-header';

				var chevron = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
				chevron.setAttribute('viewBox', '0 0 24 24');
				chevron.setAttribute('class', 'collapse-card-chevron');
				chevron.innerHTML = '<path d="M6 9l6 6 6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>';
				header.appendChild(chevron);

				var hId = document.createElement('span');
				hId.className = 'collapse-card-id';
				hId.textContent = a.id || '(new)';
				header.appendChild(hId);

				var hName = document.createElement('span');
				hName.className = 'collapse-card-name';
				hName.textContent = a.name || '';
				header.appendChild(hName);

				if (a.default) {
					var badge = document.createElement('span');
					badge.className = 'agent-card-default-badge';
					badge.textContent = 'Default';
					header.appendChild(badge);
				}

				var hModel = document.createElement('span');
				hModel.className = 'collapse-card-meta';
				hModel.textContent = a.model || '';
				header.appendChild(hModel);

				item.appendChild(header);

				if (isOnly) {
					chevron.style.visibility = 'hidden';
					header.style.cursor = 'default';
				} else {
					header.addEventListener('click', function(e) {
						if (e.target.closest('button, input, select, textarea')) return;
						expanded[idx] = !item.classList.contains('collapsed') ? false : true;
						item.classList.toggle('collapsed');
					});
				}

				var rm = document.createElement('button');
				rm.className = 'remove-btn';
				rm.innerHTML = '&times;';
				rm.onclick = function(e) {
					e.stopPropagation();
					cfg.agents.list.splice(idx, 1);
					renderAgents._expanded = {};
					render();
				};
				header.appendChild(rm);

				// === Body (collapsible) ===
				var body = document.createElement('div');
				body.className = 'collapse-card-body';
				item.appendChild(body);

				var row1 = makeRow(body);
				var idField = makeField(row1, 'ID', 'text', a.id || '', function(v) {
					cfg.agents.list[idx].id = v;
					hId.textContent = v || '(new)';
				});
				(function(idx) {
					var inp = idField.querySelector('input');
					inp.setAttribute('pattern', '[a-zA-Z0-9._-]+');
					inp.setAttribute('required', '');
					validateField(idField, function(v) {
						v = (v || '').trim();
						if (!v) return 'ID is required.';
						if (!/^[a-zA-Z0-9._-]+$/.test(v)) return 'ID may contain letters, digits, dot, dash, underscore.';
						for (var j = 0; j < (cfg.agents.list || []).length; j++) {
							if (j === idx) continue;
							if ((cfg.agents.list[j].id || '').trim() === v) return 'Duplicate ID — must be unique.';
						}
						return '';
					});
				})(idx);

				var nameField = makeField(row1, 'Name', 'text', a.name || '', function(v) {
					cfg.agents.list[idx].name = v;
					hName.textContent = v || '';
				});
				nameField.querySelector('input').setAttribute('maxlength', '200');
				validateField(nameField, function(v) {
					if ((v || '').length > 200) return 'Name must be 200 characters or fewer.';
					return '';
				});

				var row2 = makeRow(body);
				makeField(row2, 'Model', 'text', a.model || '', function(v) {
					cfg.agents.list[idx].model = v;
					hModel.textContent = v || '';
				});
				makeField(row2, 'Max Turns', 'number', a.maxTurns || 0, function(v) { cfg.agents.list[idx].maxTurns = v; });

				var row2b = makeRow(body);
				makeField(row2b, 'Context Window (0 = auto-detect)', 'number', a.contextWindow || 0, function(v) {
					cfg.agents.list[idx].contextWindow = v;
				});

				var row2bb = makeRow(body);
				makeField(row2bb, 'Reasoning', 'select', {
					value: a.reasoning || 'off',
					options: [
						{value: 'off',    label: 'off'},
						{value: 'low',    label: 'low'},
						{value: 'medium', label: 'medium'},
						{value: 'high',   label: 'high'}
					]
				}, function(v) {
					// Store empty string for "off" so omitempty drops it from the saved JSON.
					cfg.agents.list[idx].reasoning = (v === 'off') ? '' : v;
				});
				var reasoningHelp = document.createElement('div');
				reasoningHelp.className = 'form-group';
				var rhLabel = document.createElement('label');
				rhLabel.textContent = ' ';
				reasoningHelp.appendChild(rhLabel);
				var rhNote = document.createElement('div');
				rhNote.style.fontSize = '0.85em';
				rhNote.style.color = '#666';
				rhNote.style.lineHeight = '1.4';
				rhNote.textContent = 'Maps to Anthropic thinking budget, OpenAI reasoning_effort, Gemini ThinkingConfig, Qwen enable_thinking. Ignored by models that do not support extended reasoning.';
				reasoningHelp.appendChild(rhNote);
				row2bb.appendChild(reasoningHelp);

				// Subagent group: opt-in flag + the description that the
				// supervisor task tool shows to its LLM + inheritContext.
				// Setting Subagent without a description is technically
				// allowed but the supervisor will show "(no description)"
				// in the tool spec, which makes routing unreliable.
				var row2c = makeRow(body);
				makeField(row2c, 'Subagent (callable via task tool)', 'toggle', !!a.subagent, function(v) {
					cfg.agents.list[idx].subagent = v;
				});
				makeField(row2c, 'Inherit Context (subagent sees parent history)', 'toggle', !!a.inheritContext, function(v) {
					cfg.agents.list[idx].inheritContext = v;
				});

				// Default toggle: radio-style. Setting on clears others, re-renders.
				makeField(body, 'Default agent (shown first in chat dropdown)', 'toggle', !!a.default, function(v) {
					if (v) {
						for (var j = 0; j < cfg.agents.list.length; j++) {
							cfg.agents.list[j].default = (j === idx);
						}
					} else {
						cfg.agents.list[idx].default = false;
					}
					render();
				});

				makeField(body, 'Subagent Description (shown to supervisor; required when Subagent is on)', 'textarea',
					a.description || '',
					function(v) { cfg.agents.list[idx].description = v; });

				makeReadOnlyField(body, 'Sandbox', 'agent-sandbox-' + idx, 'not implemented yet');

				makeField(body, 'System Prompt', 'textarea', a.system_prompt || '', function(v) {
					cfg.agents.list[idx].system_prompt = v;
				});

				makeToolsCheckboxes(body, idx, a);

				// === Apply initial collapsed state ===
				// Force-expand if any field already has an error on first paint.
				// validateField creates an empty .field-error div eagerly on every
				// validated field, so we check for the .field-with-error class on
				// the wrapper instead — that's only set when a validator fails.
				var hasErr = body.querySelector('.field-with-error');
				if (!isOnly && !startsExpanded && !hasErr) {
					item.classList.add('collapsed');
				}

				// Consume the __justAdded sentinel after first render.
				if (a.__justAdded) delete cfg.agents.list[idx].__justAdded;

				list.appendChild(item);
			})(i);
		}

		var addBtn = document.createElement('button');
		addBtn.className = 'add-btn';
		addBtn.textContent = '+ Add Agent';
		addBtn.onclick = function() {
			if (!cfg.agents) cfg.agents = {list: []};
			if (!cfg.agents.list) cfg.agents.list = [];
			cfg.agents.list.push({id: '', name: '', model: '', tools: {allow: []}, __justAdded: true});
			render();
			focusAndFlashNewRow(list);
		};
		sec.appendChild(addBtn);
	}

	// === Intelligence Panel (Memory + Cortex + Compaction + Agent Loop) ===
	function renderIntelligence() {
		var p = document.getElementById('panel-intelligence');
		p.innerHTML = '';

		// Memory — defaults to enabled when the field is missing.
		var m = cfg.memory || {};
		var memEnabled = m.enabled !== false;
		if (!cfg.memory) cfg.memory = {};
		cfg.memory.enabled = memEnabled;
		// Default the embedding model to nomic-embed-text when not set.
		if (!cfg.memory.embeddingModel) cfg.memory.embeddingModel = 'nomic-embed-text';

		var memSec = makeSection(p, 'Memory');
		makeField(memSec, 'Enabled', 'toggle', memEnabled, function(v) {
			cfg.memory.enabled = v;
		});
		var memRow = makeRow(memSec);
		makeField(memRow, 'Embedding Provider', 'select', {
			value: m.embeddingProvider || '',
			options: Object.keys(cfg.providers || {})
		}, function(v) {
			cfg.memory.embeddingProvider = v;
		});
		var embFld = makeField(memRow, 'Embedding Model', 'text', cfg.memory.embeddingModel, function(v) {
			cfg.memory.embeddingModel = v;
		});
		var embInp = embFld.querySelector('input');
		if (embInp) embInp.placeholder = 'nomic-embed-text';

		// Cortex — defaults to enabled when the field is missing.
		var cx = cfg.cortex || {};
		var cxEnabled = cx.enabled !== false;
		if (!cfg.cortex) cfg.cortex = {};
		cfg.cortex.enabled = cxEnabled;

		var cxSec = makeSection(p, 'Cortex');
		makeField(cxSec, 'Enabled', 'toggle', cxEnabled, function(v) {
			cfg.cortex.enabled = v;
		});
		// DB Path, Provider, and LLM Model are intentionally not editable here.
		// Cortex stores its DB at ~/.felix/brain.db and mirrors the chatting
		// agent's provider+model so its LLM extraction stays in lock-step with
		// the conversation. Power users can override any of these via
		// cortex.dbPath / cortex.provider / cortex.llmModel in felix.json5.

		// Compaction — by default the summarizer mirrors the chat agent's
		// model. A user can override that here (e.g. point compaction at a
		// faster Haiku while chatting on Sonnet). Empty value = auto-mirror.
		if (!cfg.agents) cfg.agents = {};
		if (!cfg.agents.defaults) cfg.agents.defaults = {};
		if (!cfg.agents.defaults.compaction) cfg.agents.defaults.compaction = {};
		var cmp = cfg.agents.defaults.compaction;

		var cmpSec = makeSection(p, 'Compaction');
		var cmpFld = makeField(cmpSec, 'Summarizer Model (provider/model — leave blank to mirror chat agent)', 'text',
			cmp.model || '',
			function(v) { cfg.agents.defaults.compaction.model = (v || '').trim(); });
		var cmpInp = cmpFld.querySelector('input');
		if (cmpInp) cmpInp.placeholder = 'auto: matches chatting agent (e.g. anthropic/claude-haiku-4-5)';

		// Agent Loop — three knobs controlling tool dispatch behavior. Lives
		// in the Intelligence panel because it's a runtime tuning control
		// alongside Memory/Cortex; saving any of these takes effect on the
		// next agent run via fsnotify hot-reload (no restart).
		if (!cfg.agentLoop) cfg.agentLoop = {};
		var al = cfg.agentLoop;

		var alSec = makeSection(p, 'Agent Loop');
		makeField(alSec, 'Streaming Tools (mid-stream tool kickoff)', 'toggle',
			!!al.streamingTools,
			function(v) { cfg.agentLoop.streamingTools = v; });

		var alRow = makeRow(alSec);
		// makeField('number', ...) already parses input to int via parseInt.
		makeField(alRow, 'Max Tool Concurrency (0 = default 10)', 'number',
			al.maxToolConcurrency || 0,
			function(v) { cfg.agentLoop.maxToolConcurrency = v; });
		makeField(alRow, 'Max Agent Depth (0 = default 3)', 'number',
			al.maxAgentDepth || 0,
			function(v) { cfg.agentLoop.maxAgentDepth = v; });
		makeField(alRow, 'Max Tool Result Length (chars, 0 = default 65536)', 'number',
			al.maxToolResultLen || 0,
			function(v) { cfg.agentLoop.maxToolResultLen = v; });

		// Memory Entries — folded in below Agent Loop. Was a top-level
		// tab; the CRUD UI lives in renderMemoryEntries(), targeting a
		// container appended here so it shares the Memory panel.
		var meSec = makeSection(p, 'Memory Entries');
		var meContainer = document.createElement('div');
		meContainer.id = 'memory-entries-container';
		meSec.appendChild(meContainer);
	}

	// === Security Panel ===
	function renderSecurity() {
		var p = document.getElementById('panel-security');
		p.innerHTML = '';
		var sec = makeSection(p, null);
		var security = cfg.security || {};
		var exec = security.execApprovals || {};

		makeField(sec, 'Exec Approvals Level', 'select', {
			value: exec.level || 'full',
			options: ['full', 'allowlist', 'deny']
		}, function(v) {
			if (!cfg.security) cfg.security = {};
			if (!cfg.security.execApprovals) cfg.security.execApprovals = {};
			cfg.security.execApprovals.level = v;
		});
		makeField(sec, 'Exec Allowlist (comma-separated commands)', 'text',
			(exec.allowlist || []).join(', '),
			function(v) {
				if (!cfg.security) cfg.security = {};
				if (!cfg.security.execApprovals) cfg.security.execApprovals = {};
				cfg.security.execApprovals.allowlist = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
			}
		);
	}

	// === Skills tab ===
	var skillsViewing = null; // currently-open filename in side panel, or null

	function renderSkills() {
		var panel = document.getElementById('panel-skills');
		panel.innerHTML =
			'<div style="margin-bottom:1rem; display:flex; gap:0.75rem; align-items:center;">' +
				'<label class="btn btn-primary" style="cursor:pointer;">' +
					'Upload .md' +
					'<input type="file" id="skill-upload-input" accept=".md" style="display:none">' +
				'</label>' +
				'<span style="color: var(--color-text-muted); font-size: 0.85rem;">' +
					'Files go to ~/.felix/skills/ and load on next chat turn.' +
				'</span>' +
			'</div>' +
			'<input id="skills-filter" class="inline-filter" type="search" placeholder="Filter skills...">' +
			'<div id="skills-list">Loading&#8230;</div>' +
			'<div id="skill-view-panel" style="margin-top:1.5rem; display:none;">' +
				'<h3 id="skill-view-name" style="margin-bottom:0.5rem;"></h3>' +
				'<pre id="skill-view-body" style="background: var(--color-bg); padding: 1rem; border-radius: var(--radius); border: 1px solid var(--color-border); overflow:auto; max-height:60vh; white-space: pre-wrap; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.85rem;"></pre>' +
			'</div>';

		document.getElementById('skill-upload-input').addEventListener('change', onSkillUpload);
		refreshSkillList();
	}

	function refreshSkillList() {
		fetch('/settings/api/skills')
			.then(function(r) { return r.json(); })
			.then(function(data) {
				var listDiv = document.getElementById('skills-list');
				if (!data.skills || data.skills.length === 0) {
					listDiv.innerHTML = '<p style="color: var(--color-text-muted);">No skills uploaded yet.</p>';
					return;
				}
				var html = '<table style="width:100%; border-collapse: collapse;">';
				html += '<thead><tr>' +
					'<th style="text-align:left; padding:0.5rem; border-bottom:1px solid var(--color-border);">Name</th>' +
					'<th style="text-align:left; padding:0.5rem; border-bottom:1px solid var(--color-border);">Description</th>' +
					'<th style="text-align:right; padding:0.5rem; border-bottom:1px solid var(--color-border);">Size</th>' +
					'<th style="text-align:right; padding:0.5rem; border-bottom:1px solid var(--color-border);">Actions</th>' +
					'</tr></thead><tbody>';
				data.skills.forEach(function(s) {
					var rowStyle = '';
					var note = '';
					if (s.parse_error) {
						rowStyle = 'color: var(--color-error);';
						note = ' <span title="' + escapeAttr(s.parse_error) + '">&#9888; parse error</span>';
					} else if (s.unavailable) {
						rowStyle = 'color: var(--color-text-muted);';
						note = ' <span title="missing: ' + escapeAttr((s.missing_bins || []).join(', ')) + '">&#9888; unavailable</span>';
					}
					html += '<tr data-skill="' + escapeAttr(s.filename) + '" style="' + rowStyle + '">' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border);"><code>' + escapeHtml(s.filename) + '</code>' + note + '</td>' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border);">' + escapeHtml(s.description || '') + '</td>' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border); text-align:right; font-variant-numeric:tabular-nums;">' + fmtBytes(s.size_bytes) + '</td>' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border); text-align:right;">' +
							'<button class="btn-link" data-skill-view="' + escapeAttr(s.filename) + '">View</button> ' +
							'<button class="btn-link" data-skill-delete="' + escapeAttr(s.filename) + '" style="color:var(--color-error);">Delete</button>' +
						'</td>' +
					'</tr>';
				});
				html += '</tbody></table>';
				listDiv.innerHTML = html;
				listDiv.querySelectorAll('[data-skill-view]').forEach(function(b) {
					b.addEventListener('click', function() { viewSkill(b.dataset.skillView); });
				});
				listDiv.querySelectorAll('[data-skill-delete]').forEach(function(b) {
					b.addEventListener('click', function() { deleteSkill(b.dataset.skillDelete); });
				});
				var skillsFilter = document.getElementById('skills-filter');
				if (skillsFilter) {
					skillsFilter.oninput = function() {
						var q = skillsFilter.value.toLowerCase();
						var rows = listDiv.querySelectorAll('tr[data-skill]');
						for (var i = 0; i < rows.length; i++) {
							var name = rows[i].dataset.skill || rows[i].textContent || '';
							rows[i].style.display = (!q || name.toLowerCase().indexOf(q) !== -1) ? '' : 'none';
						}
					};
				}
			})
			.catch(function(err) { showStatus('Skills load failed: ' + err.message, true); });
	}

	function onSkillUpload(ev) {
		var f = ev.target.files[0];
		if (!f) return;
		if (!/\.md$/i.test(f.name)) {
			showStatus('Skill files must end in .md', true);
			ev.target.value = '';
			return;
		}
		var fd = new FormData();
		fd.append('file', f);
		fetch('/settings/api/skills', { method: 'POST', body: fd })
			.then(function(r) { return r.json().then(function(j) { return { ok: r.ok, status: r.status, body: j }; }); })
			.then(function(res) {
				ev.target.value = '';
				if (!res.ok) {
					showStatus('Upload failed: ' + (res.body.error || res.status), true);
					return;
				}
				var msg = 'Uploaded ' + (res.body.filename || f.name);
				if (res.body.warning) msg += ' (' + res.body.warning + ')';
				showStatus(msg, false);
				refreshSkillList();
			})
			.catch(function(err) {
				ev.target.value = '';
				showStatus('Upload failed: ' + err.message, true);
			});
	}

	function viewSkill(filename) {
		fetch('/settings/api/skills/' + encodeURIComponent(filename))
			.then(function(r) {
				if (!r.ok) throw new Error('HTTP ' + r.status);
				return r.text();
			})
			.then(function(text) {
				skillsViewing = filename;
				document.getElementById('skill-view-name').textContent = filename;
				document.getElementById('skill-view-body').textContent = text;
				document.getElementById('skill-view-panel').style.display = '';
			})
			.catch(function(err) { showStatus('View failed: ' + err.message, true); });
	}

	function deleteSkill(filename) {
		if (!confirm('Delete ' + filename + '? This cannot be undone.')) return;
		fetch('/settings/api/skills/' + encodeURIComponent(filename), { method: 'DELETE' })
			.then(function(r) { return r.json().then(function(j) { return { ok: r.ok, status: r.status, body: j }; }); })
			.then(function(res) {
				if (!res.ok) {
					showStatus('Delete failed: ' + (res.body.error || res.status), true);
					return;
				}
				var msg = 'Deleted ' + filename;
				if (res.body.warning) msg += ' (' + res.body.warning + ')';
				showStatus(msg, false);
				if (skillsViewing === filename) {
					skillsViewing = null;
					document.getElementById('skill-view-panel').style.display = 'none';
				}
				refreshSkillList();
			})
			.catch(function(err) { showStatus('Delete failed: ' + err.message, true); });
	}

	function escapeHtml(s) {
		return String(s).replace(/[&<>"']/g, function(c) {
			return ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' })[c];
		});
	}
	function escapeAttr(s) { return escapeHtml(s); }

	// === Memory tab ===
	var memoryEditing = null; // currently-editing id, or null when in list mode

	function renderMemory() {
		var panel = document.getElementById('memory-entries-container');
		if (!panel) return; // Memory entries live inside the Memory (was Intelligence) panel.
		panel.innerHTML =
			'<div style="margin-bottom:1rem; display:flex; gap:0.75rem; align-items:center;">' +
				'<button class="btn btn-primary" id="memory-new-btn">New entry</button>' +
				'<span style="color: var(--color-text-muted); font-size: 0.85rem;">' +
					'Stored as Markdown in ~/.felix/memory/entries/. Searchable via the agent\'s load_memory tool.' +
				'</span>' +
			'</div>' +
			'<div id="memory-list">Loading&#8230;</div>' +
			'<div id="memory-edit-panel" style="display:none; margin-top:1rem;">' +
				'<div style="display:flex; gap:0.5rem; align-items:center; margin-bottom:0.5rem;">' +
					'<input type="text" id="memory-edit-id" placeholder="entry-id (letters, digits, dot, dash, underscore)" style="flex:1; padding:0.5rem 0.75rem; border:1px solid var(--color-border); border-radius:var(--radius); background:var(--color-surface); color:var(--color-text); font-family:inherit;">' +
					'<button class="btn btn-primary" id="memory-save-btn">Save</button>' +
					'<button class="btn-icon" id="memory-cancel-btn">Cancel</button>' +
				'</div>' +
				'<textarea id="memory-edit-content" placeholder="# Title\n\nMarkdown content..." style="width:100%; min-height:300px; padding:0.75rem; border:1px solid var(--color-border); border-radius:var(--radius); background:var(--color-surface); color:var(--color-text); font-family:ui-monospace, SFMono-Regular, Menlo, monospace; font-size:0.85rem; resize:vertical;"></textarea>' +
			'</div>';

		document.getElementById('memory-new-btn').addEventListener('click', function() {
			openMemoryEditor('', '');
		});
		document.getElementById('memory-cancel-btn').addEventListener('click', closeMemoryEditor);
		document.getElementById('memory-save-btn').addEventListener('click', saveMemoryEntry);

		refreshMemoryList();
	}

	function refreshMemoryList() {
		fetch('/settings/api/memory')
			.then(function(r) { return r.ok ? r.json() : Promise.reject(r.status); })
			.then(function(data) {
				var listDiv = document.getElementById('memory-list');
				if (!data.entries || data.entries.length === 0) {
					listDiv.innerHTML = '<p style="color: var(--color-text-muted);">No memory entries yet. Click "New entry" to create one.</p>';
					return;
				}
				var html = '<table style="width:100%; border-collapse: collapse;">' +
					'<thead><tr>' +
					'<th style="text-align:left; padding:0.5rem; border-bottom:1px solid var(--color-border);">ID</th>' +
					'<th style="text-align:left; padding:0.5rem; border-bottom:1px solid var(--color-border);">Title</th>' +
					'<th style="text-align:right; padding:0.5rem; border-bottom:1px solid var(--color-border);">Size</th>' +
					'<th style="text-align:right; padding:0.5rem; border-bottom:1px solid var(--color-border);">Actions</th>' +
					'</tr></thead><tbody>';
				data.entries.forEach(function(e) {
					html += '<tr>' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border);"><code>' + escapeHtml(e.id) + '</code></td>' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border);">' + escapeHtml(e.title || '') + '</td>' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border); text-align:right; font-variant-numeric:tabular-nums;">' + fmtBytes(e.bytes) + '</td>' +
						'<td style="padding:0.5rem; border-bottom:1px solid var(--color-border); text-align:right;">' +
							'<button class="btn-link" data-mem-edit="' + escapeAttr(e.id) + '">Edit</button> ' +
							'<button class="btn-link" data-mem-delete="' + escapeAttr(e.id) + '" style="color:var(--color-error);">Delete</button>' +
						'</td>' +
					'</tr>';
				});
				html += '</tbody></table>';
				listDiv.innerHTML = html;
				listDiv.querySelectorAll('[data-mem-edit]').forEach(function(b) {
					b.addEventListener('click', function() { editMemoryEntry(b.dataset.memEdit); });
				});
				listDiv.querySelectorAll('[data-mem-delete]').forEach(function(b) {
					b.addEventListener('click', function() { deleteMemoryEntry(b.dataset.memDelete); });
				});
			})
			.catch(function(err) {
				var msg = (err && err.toString) ? err.toString() : err;
				document.getElementById('memory-list').innerHTML =
					'<p style="color: var(--color-text-muted);">Memory is unavailable (' + escapeHtml(msg) + '). Enable it under the Memory section above.</p>';
			});
	}

	function openMemoryEditor(id, content) {
		memoryEditing = id;
		document.getElementById('memory-edit-id').value = id;
		document.getElementById('memory-edit-id').readOnly = !!id; // editing existing → id is fixed
		document.getElementById('memory-edit-content').value = content;
		document.getElementById('memory-edit-panel').style.display = 'block';
	}

	function closeMemoryEditor() {
		memoryEditing = null;
		document.getElementById('memory-edit-panel').style.display = 'none';
	}

	function editMemoryEntry(id) {
		fetch('/settings/api/memory/' + encodeURIComponent(id))
			.then(function(r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.text(); })
			.then(function(text) { openMemoryEditor(id, text); })
			.catch(function(err) { showStatus('Load failed: ' + err.message, true); });
	}

	function saveMemoryEntry() {
		var id = document.getElementById('memory-edit-id').value.trim();
		var content = document.getElementById('memory-edit-content').value;
		if (!id) { showStatus('id is required', true); return; }
		if (!content.trim()) { showStatus('content is empty', true); return; }
		fetch('/settings/api/memory', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({id: id, content: content})
		})
			.then(function(r) { return r.json().then(function(j) { return {ok: r.ok, body: j}; }); })
			.then(function(res) {
				if (!res.ok) { showStatus('Save failed: ' + (res.body.error || ''), true); return; }
				showStatus('Saved ' + id, false);
				closeMemoryEditor();
				refreshMemoryList();
			})
			.catch(function(err) { showStatus('Save failed: ' + err.message, true); });
	}

	function deleteMemoryEntry(id) {
		if (!confirm('Delete memory entry "' + id + '"? This cannot be undone.')) return;
		fetch('/settings/api/memory/' + encodeURIComponent(id), {method: 'DELETE'})
			.then(function(r) { return r.json().then(function(j) { return {ok: r.ok, body: j}; }); })
			.then(function(res) {
				if (!res.ok) { showStatus('Delete failed: ' + (res.body.error || ''), true); return; }
				showStatus('Deleted ' + id, false);
				if (memoryEditing === id) closeMemoryEditor();
				refreshMemoryList();
			})
			.catch(function(err) { showStatus('Delete failed: ' + err.message, true); });
	}
})();
</script>
</body>
</html>`
