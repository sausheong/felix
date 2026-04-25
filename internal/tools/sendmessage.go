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

	channelTelegram = "telegram"
)

// SendMessageTool sends outbound messages to messaging channels. Today only
// Telegram is supported, but the channel field leaves room for WhatsApp /
// Discord / Slack / etc. without changing the tool's identity from the LLM's
// point of view.
//
// The tool is always registered so it appears in the Agents settings tab even
// before any channel is configured; calls into an unconfigured channel return a
// clear error pointing to the Messaging settings.
//
// Config is read live via ConfigFn (set in production) so changes saved through
// the Settings UI take effect on the next call without a process restart. Tests
// set the static fields directly and leave ConfigFn nil.
//
// Send-only — Felix does not receive replies through this tool. There is no
// inbound webhook; the agent calls it, the recipient sees a message.
type SendMessageTool struct {
	// Live config provider. When non-nil, takes precedence over the static
	// fields below and is read on every Execute call.
	ConfigFn func() SendMessageRegistration

	// Static fallback used when ConfigFn is nil (tests, ad-hoc construction).
	TelegramToken         string
	TelegramDefaultChatID string

	TelegramBaseURL string // empty → telegramDefaultBaseURL; injected for tests
	HTTPClient      *http.Client
}

// telegramConfig returns the live token and default chat ID, preferring
// ConfigFn when set so hot-reloaded config is honored without re-registration.
func (t *SendMessageTool) telegramConfig() (token, defaultChatID string) {
	if t.ConfigFn != nil {
		c := t.ConfigFn()
		if !c.TelegramEnabled {
			return "", ""
		}
		return c.TelegramBotToken, c.TelegramDefaultChatID
	}
	return t.TelegramToken, t.TelegramDefaultChatID
}

type sendMessageInput struct {
	Channel        string `json:"channel,omitempty"` // defaults to "telegram"
	Text           string `json:"text"`
	ChatID         string `json:"chat_id,omitempty"`
	ParseMode      string `json:"parse_mode,omitempty"` // "Markdown", "MarkdownV2", "HTML"
	DisablePreview bool   `json:"disable_link_preview,omitempty"`
	DisableNotify  bool   `json:"disable_notification,omitempty"`
}

func (t *SendMessageTool) Name() string { return "send_message" }

func (t *SendMessageTool) Description() string {
	return `Send a message to a messaging channel. This is an outbound action — it actually delivers a message to a real person/channel and cannot be undone. Use sparingly and only when the user has asked for a message to be sent.

Currently supported channels:
- "telegram" (default): Telegram Bot API. "chat_id" can be a numeric user ID, "@channelname", or a negative group ID. Optional "parse_mode" enables Markdown/HTML formatting.

Required: "text" (max 4096 characters for Telegram). "chat_id" is optional if a default is configured for the channel; otherwise pass an explicit recipient.`
}

func (t *SendMessageTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"channel": {
				"type": "string",
				"enum": ["telegram"],
				"description": "Messaging channel to send through. Default: telegram."
			},
			"text": {
				"type": "string",
				"description": "Message body (max 4096 characters for Telegram)."
			},
			"chat_id": {
				"type": "string",
				"description": "Recipient ID. For Telegram: numeric user/group ID, @channelname, or negative group ID. Optional if a default chat is configured."
			},
			"parse_mode": {
				"type": "string",
				"enum": ["", "Markdown", "MarkdownV2", "HTML"],
				"description": "Optional formatting mode (Telegram). Default is plain text."
			},
			"disable_link_preview": {
				"type": "boolean",
				"description": "Suppress link previews (Telegram). Default false."
			},
			"disable_notification": {
				"type": "boolean",
				"description": "Send silently, no notification sound (Telegram). Default false."
			}
		},
		"required": ["text"]
	}`)
}

func (t *SendMessageTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in sendMessageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	channel := strings.TrimSpace(in.Channel)
	if channel == "" {
		channel = channelTelegram
	}
	switch channel {
	case channelTelegram:
		return t.sendTelegram(ctx, in)
	default:
		return ToolResult{Error: fmt.Sprintf("channel %q is not supported (available: telegram)", channel)}, nil
	}
}

func (t *SendMessageTool) sendTelegram(ctx context.Context, in sendMessageInput) (ToolResult, error) {
	token, defaultChatID := t.telegramConfig()
	if strings.TrimSpace(token) == "" {
		return ToolResult{Error: "telegram channel is not configured — enable it and add a bot_token in Settings → Messaging"}, nil
	}
	if strings.TrimSpace(in.Text) == "" {
		return ToolResult{Error: "text is required"}, nil
	}
	if len(in.Text) > telegramMaxText {
		return ToolResult{Error: fmt.Sprintf("text exceeds Telegram's 4096-character limit (got %d)", len(in.Text))}, nil
	}
	chatID := strings.TrimSpace(in.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(defaultChatID)
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

	base := t.TelegramBaseURL
	if base == "" {
		base = telegramDefaultBaseURL
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(base, "/"), token)

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
		Output: fmt.Sprintf("Sent telegram → %s (message_id=%d)", chatID, msg.MessageID),
		Metadata: map[string]any{
			"channel":    channelTelegram,
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

// SendMessageRegistration is the subset of config needed to drive the
// send_message tool. Decouples the tool from the config package.
type SendMessageRegistration struct {
	TelegramEnabled       bool
	TelegramBotToken      string
	TelegramDefaultChatID string
}

// RegisterSendMessage always registers the send_message tool so it appears in
// the Settings → Agents tool picker even before any channel is configured.
// configFn is invoked on every Execute call so changes saved through the
// Settings UI take effect immediately without a process restart. Pass nil for
// configFn in code paths where no live config exists (the tool will then
// always report "not configured").
func RegisterSendMessage(reg *Registry, configFn func() SendMessageRegistration) {
	reg.Register(&SendMessageTool{ConfigFn: configFn})
}
