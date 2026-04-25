package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	telegramDefaultBaseURL = "https://api.telegram.org"
	telegramTimeout        = 15 * time.Second
	telegramMaxText        = 4096 // Telegram hard limit per message
)

// TelegramTool sends outbound Telegram messages via the Bot API. Send-only —
// Felix does not receive or respond to Telegram messages with this tool. The
// agent calls it; there is no inbound webhook.
type TelegramTool struct {
	Token         string
	DefaultChatID string
	BaseURL       string // empty → telegramDefaultBaseURL; injected for tests
	HTTPClient    *http.Client
}

type telegramInput struct {
	Text             string `json:"text"`
	ChatID           string `json:"chat_id,omitempty"`
	ParseMode        string `json:"parse_mode,omitempty"`         // "Markdown", "MarkdownV2", "HTML"
	DisablePreview   bool   `json:"disable_link_preview,omitempty"`
	DisableNotify    bool   `json:"disable_notification,omitempty"`
}

func (t *TelegramTool) Name() string { return "telegram_send" }

func (t *TelegramTool) Description() string {
	return `Send a message to a Telegram chat via your configured bot. This is an outbound action — it actually delivers a message to a real person/channel and cannot be undone. Use sparingly and only when the user has asked for a message to be sent.

Provide "text" (max 4096 chars). "chat_id" is optional if a default is configured; otherwise pass a numeric chat ID, a "@channelname", or a group ID (negative number, e.g. -100123). Optional "parse_mode" enables Markdown/HTML formatting.`
}

func (t *TelegramTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text": {
				"type": "string",
				"description": "Message body (max 4096 characters)."
			},
			"chat_id": {
				"type": "string",
				"description": "Telegram chat ID, @channelname, or numeric group ID. Optional if a default chat is configured."
			},
			"parse_mode": {
				"type": "string",
				"enum": ["", "Markdown", "MarkdownV2", "HTML"],
				"description": "Optional formatting mode. Default is plain text."
			},
			"disable_link_preview": {
				"type": "boolean",
				"description": "Suppress link previews. Default false."
			},
			"disable_notification": {
				"type": "boolean",
				"description": "Send silently (no notification sound). Default false."
			}
		},
		"required": ["text"]
	}`)
}

func (t *TelegramTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	if strings.TrimSpace(t.Token) == "" {
		return ToolResult{Error: "telegram_send is not configured (missing bot_token)"}, nil
	}

	var in telegramInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if strings.TrimSpace(in.Text) == "" {
		return ToolResult{Error: "text is required"}, nil
	}
	if len(in.Text) > telegramMaxText {
		return ToolResult{Error: fmt.Sprintf("text exceeds Telegram's 4096-character limit (got %d)", len(in.Text))}, nil
	}
	chatID := strings.TrimSpace(in.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(t.DefaultChatID)
	}
	if chatID == "" {
		return ToolResult{Error: "chat_id is required (no default_chat_id configured)"}, nil
	}
	if in.ParseMode != "" {
		switch in.ParseMode {
		case "Markdown", "MarkdownV2", "HTML":
		default:
			return ToolResult{Error: fmt.Sprintf("invalid parse_mode %q (valid: Markdown, MarkdownV2, HTML)", in.ParseMode)}, nil
		}
	}

	payload := map[string]any{
		"chat_id": chatID,
		"text":    in.Text,
	}
	if in.ParseMode != "" {
		payload["parse_mode"] = in.ParseMode
	}
	if in.DisablePreview {
		payload["disable_web_page_preview"] = true
	}
	if in.DisableNotify {
		payload["disable_notification"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("encode payload: %v", err)}, nil
	}

	base := t.BaseURL
	if base == "" {
		base = telegramDefaultBaseURL
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(base, "/"), t.Token)

	reqCtx, cancel := context.WithTimeout(ctx, telegramTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("build request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	client := t.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: telegramTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("send to Telegram: %v", err)}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("read response: %v", err)}, nil
	}

	var apiResp struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return ToolResult{Error: fmt.Sprintf("Telegram returned non-JSON (HTTP %d): %s", resp.StatusCode, truncate(string(respBody), 200))}, nil
	}
	if !apiResp.OK {
		return ToolResult{Error: fmt.Sprintf("Telegram API error %d: %s", apiResp.ErrorCode, apiResp.Description)}, nil
	}

	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(apiResp.Result, &msg)

	return ToolResult{
		Output: fmt.Sprintf("Sent to chat %s (message_id=%d)", chatID, msg.MessageID),
		Metadata: map[string]any{
			"chat_id":    chatID,
			"message_id": msg.MessageID,
		},
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// RegisterTelegram registers the telegram_send tool when enabled and a token is
// set. Returns true if the tool was registered.
func RegisterTelegram(reg *Registry, cfg TelegramRegistration) bool {
	if !cfg.Enabled || strings.TrimSpace(cfg.BotToken) == "" {
		return false
	}
	reg.Register(&TelegramTool{
		Token:         cfg.BotToken,
		DefaultChatID: cfg.DefaultChatID,
	})
	return true
}

// TelegramRegistration is the subset of TelegramConfig the tools package needs.
// Decouples the tool registration from the config package.
type TelegramRegistration struct {
	Enabled       bool
	BotToken      string
	DefaultChatID string
}
