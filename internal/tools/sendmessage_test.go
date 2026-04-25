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

func TestSendMessageName(t *testing.T) {
	tool := &SendMessageTool{}
	assert.Equal(t, "send_message", tool.Name())
}

func TestSendMessageParametersValidJSON(t *testing.T) {
	tool := &SendMessageTool{}
	assert.True(t, json.Valid(tool.Parameters()))
}

func TestSendMessageUnknownChannel(t *testing.T) {
	tool := &SendMessageTool{}
	in, _ := json.Marshal(sendMessageInput{Channel: "smoke-signal", Text: "hi", ChatID: "1"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, `channel "smoke-signal" is not supported`)
}

func TestSendMessageDefaultsToTelegram(t *testing.T) {
	// No token configured → telegram channel should report not-configured.
	tool := &SendMessageTool{}
	in, _ := json.Marshal(sendMessageInput{Text: "hi", ChatID: "1"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "telegram channel is not configured")
}

func TestSendMessageTelegramMissingText(t *testing.T) {
	tool := &SendMessageTool{TelegramToken: "t", TelegramDefaultChatID: "1"}
	in, _ := json.Marshal(sendMessageInput{Text: "  "})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "text is required")
}

func TestSendMessageTelegramMissingChat(t *testing.T) {
	tool := &SendMessageTool{TelegramToken: "t"}
	in, _ := json.Marshal(sendMessageInput{Text: "hello"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "chat_id is required")
}

func TestSendMessageTelegramTooLong(t *testing.T) {
	tool := &SendMessageTool{TelegramToken: "t", TelegramDefaultChatID: "1"}
	in, _ := json.Marshal(sendMessageInput{Text: strings.Repeat("a", telegramMaxText+1)})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "4096-character limit")
}

func TestSendMessageTelegramInvalidParseMode(t *testing.T) {
	tool := &SendMessageTool{TelegramToken: "t", TelegramDefaultChatID: "1"}
	in, _ := json.Marshal(sendMessageInput{Text: "x", ParseMode: "rtf"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "invalid parse_mode")
}

func TestSendMessageTelegramSuccess(t *testing.T) {
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

	tool := &SendMessageTool{
		TelegramToken:         "SECRET",
		TelegramDefaultChatID: "9999",
		TelegramBaseURL:       srv.URL,
	}
	in, _ := json.Marshal(sendMessageInput{
		Text:           "hello",
		ParseMode:      "Markdown",
		DisablePreview: true,
	})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Empty(t, res.Error)
	assert.Contains(t, res.Output, "message_id=42")
	assert.Contains(t, res.Output, "9999")
	assert.Contains(t, res.Output, "telegram")

	assert.Equal(t, "9999", captured["chat_id"])
	assert.Equal(t, "hello", captured["text"])
	assert.Equal(t, "Markdown", captured["parse_mode"])
	assert.Equal(t, true, captured["disable_web_page_preview"])
	_, hasNotify := captured["disable_notification"]
	assert.False(t, hasNotify, "disable_notification should be omitted when false")
}

func TestSendMessageExplicitTelegramChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	tool := &SendMessageTool{TelegramToken: "T", TelegramDefaultChatID: "1", TelegramBaseURL: srv.URL}
	in, _ := json.Marshal(sendMessageInput{Channel: "telegram", Text: "x"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Empty(t, res.Error)
}

func TestSendMessageExplicitChatOverridesDefault(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	tool := &SendMessageTool{TelegramToken: "T", TelegramDefaultChatID: "default", TelegramBaseURL: srv.URL}
	in, _ := json.Marshal(sendMessageInput{Text: "x", ChatID: "@channel"})
	_, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "@channel", captured["chat_id"])
}

func TestSendMessageTelegramAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"chat not found"}`))
	}))
	defer srv.Close()

	tool := &SendMessageTool{TelegramToken: "T", TelegramDefaultChatID: "1", TelegramBaseURL: srv.URL}
	in, _ := json.Marshal(sendMessageInput{Text: "x"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "Telegram API error 400")
	assert.Contains(t, res.Error, "chat not found")
}

func TestSendMessageTelegramNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream offline"))
	}))
	defer srv.Close()

	tool := &SendMessageTool{TelegramToken: "T", TelegramDefaultChatID: "1", TelegramBaseURL: srv.URL}
	in, _ := json.Marshal(sendMessageInput{Text: "x"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "non-JSON")
	assert.Contains(t, res.Error, "502")
}

// RegisterSendMessage always registers the tool so it shows in the Settings →
// Agents picker even before any channel is configured. Calls to an
// unconfigured channel still surface a clear error at execute time.
func TestRegisterSendMessageAlwaysRegisters(t *testing.T) {
	reg := NewRegistry()
	RegisterSendMessage(reg, nil)
	tool, ok := reg.Get("send_message")
	require.True(t, ok)
	assert.Equal(t, "send_message", tool.Name())
}

func TestRegisterSendMessageWithConfigFn(t *testing.T) {
	reg := NewRegistry()
	RegisterSendMessage(reg, func() SendMessageRegistration {
		return SendMessageRegistration{
			TelegramEnabled:       true,
			TelegramBotToken:      "T",
			TelegramDefaultChatID: "1",
		}
	})
	tool, ok := reg.Get("send_message")
	require.True(t, ok)
	smt := tool.(*SendMessageTool)
	token, chat := smt.telegramConfig()
	assert.Equal(t, "T", token)
	assert.Equal(t, "1", chat)
}

func TestSendMessageConfigFnDisabledHidesToken(t *testing.T) {
	tool := &SendMessageTool{ConfigFn: func() SendMessageRegistration {
		return SendMessageRegistration{TelegramEnabled: false, TelegramBotToken: "T"}
	}}
	in, _ := json.Marshal(sendMessageInput{Text: "hi", ChatID: "1"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "telegram channel is not configured")
}
