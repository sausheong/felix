package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramName(t *testing.T) {
	tool := &TelegramTool{}
	assert.Equal(t, "telegram_send", tool.Name())
}

func TestTelegramParametersValidJSON(t *testing.T) {
	tool := &TelegramTool{}
	assert.True(t, json.Valid(tool.Parameters()))
}

func TestTelegramMissingToken(t *testing.T) {
	tool := &TelegramTool{}
	in, _ := json.Marshal(telegramInput{Text: "hi", ChatID: "123"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "not configured")
}

func TestTelegramMissingText(t *testing.T) {
	tool := &TelegramTool{Token: "t", DefaultChatID: "123"}
	in, _ := json.Marshal(telegramInput{Text: "  "})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "text is required")
}

func TestTelegramMissingChat(t *testing.T) {
	tool := &TelegramTool{Token: "t"}
	in, _ := json.Marshal(telegramInput{Text: "hello"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "chat_id is required")
}

func TestTelegramTooLong(t *testing.T) {
	tool := &TelegramTool{Token: "t", DefaultChatID: "1"}
	in, _ := json.Marshal(telegramInput{Text: strings.Repeat("a", telegramMaxText+1)})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "4096-character limit")
}

func TestTelegramInvalidParseMode(t *testing.T) {
	tool := &TelegramTool{Token: "t", DefaultChatID: "1"}
	in, _ := json.Marshal(telegramInput{Text: "x", ParseMode: "rtf"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "invalid parse_mode")
}

func TestTelegramSendSuccess(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botSECRET/sendMessage", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer srv.Close()

	tool := &TelegramTool{Token: "SECRET", DefaultChatID: "9999", BaseURL: srv.URL}
	in, _ := json.Marshal(telegramInput{
		Text:           "hello",
		ParseMode:      "Markdown",
		DisablePreview: true,
	})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Empty(t, res.Error)
	assert.Contains(t, res.Output, "message_id=42")
	assert.Contains(t, res.Output, "9999")

	assert.Equal(t, "9999", captured["chat_id"])
	assert.Equal(t, "hello", captured["text"])
	assert.Equal(t, "Markdown", captured["parse_mode"])
	assert.Equal(t, true, captured["disable_web_page_preview"])
	_, hasNotify := captured["disable_notification"]
	assert.False(t, hasNotify, "disable_notification should be omitted when false")
}

func TestTelegramExplicitChatOverridesDefault(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	tool := &TelegramTool{Token: "T", DefaultChatID: "default", BaseURL: srv.URL}
	in, _ := json.Marshal(telegramInput{Text: "x", ChatID: "@channel"})
	_, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "@channel", captured["chat_id"])
}

func TestTelegramAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	}))
	defer srv.Close()

	tool := &TelegramTool{Token: "T", DefaultChatID: "1", BaseURL: srv.URL}
	in, _ := json.Marshal(telegramInput{Text: "x"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "Telegram API error 400")
	assert.Contains(t, res.Error, "chat not found")
}

func TestTelegramNonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream offline"))
	}))
	defer srv.Close()

	tool := &TelegramTool{Token: "T", DefaultChatID: "1", BaseURL: srv.URL}
	in, _ := json.Marshal(telegramInput{Text: "x"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "non-JSON")
	assert.Contains(t, res.Error, "502")
}

func TestRegisterTelegramSkippedWhenDisabled(t *testing.T) {
	reg := NewRegistry()
	require.False(t, RegisterTelegram(reg, TelegramRegistration{Enabled: false, BotToken: "T"}))
	require.False(t, RegisterTelegram(reg, TelegramRegistration{Enabled: true, BotToken: ""}))
	_, ok := reg.Get("telegram_send")
	assert.False(t, ok)
}

func TestRegisterTelegramRegisters(t *testing.T) {
	reg := NewRegistry()
	require.True(t, RegisterTelegram(reg, TelegramRegistration{Enabled: true, BotToken: "T", DefaultChatID: "1"}))
	tool, ok := reg.Get("telegram_send")
	require.True(t, ok)
	assert.Equal(t, "telegram_send", tool.Name())
}
