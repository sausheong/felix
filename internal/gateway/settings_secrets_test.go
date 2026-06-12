package gateway

import (
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRedactAndRestoreScalars(t *testing.T) {
	cur := &config.Config{}
	cur.Telegram.BotToken = "bot-secret"
	cur.Gateway.Auth.Token = "gw-secret"
	cur.WebSearch.APIKey = "ws-secret"
	cur.OTel.Headers = map[string]string{"authorization": "Bearer otel-secret"}

	clone := &config.Config{}
	clone.Telegram.BotToken = "bot-secret"
	clone.Gateway.Auth.Token = "gw-secret"
	clone.WebSearch.APIKey = "ws-secret"
	clone.OTel.Headers = map[string]string{"authorization": "Bearer otel-secret"}
	redactConfigSecrets(clone)
	require.Equal(t, redactedSentinel, clone.Telegram.BotToken)
	require.Equal(t, redactedSentinel, clone.Gateway.Auth.Token)
	require.Equal(t, redactedSentinel, clone.WebSearch.APIKey)
	require.Equal(t, redactedSentinel, clone.OTel.Headers["authorization"])

	incoming := &config.Config{}
	incoming.Telegram.BotToken = redactedSentinel
	incoming.Gateway.Auth.Token = "gw-rotated"
	incoming.WebSearch.APIKey = redactedSentinel
	incoming.OTel.Headers = map[string]string{"authorization": redactedSentinel}
	restoreSecretScalars(incoming, cur)
	require.Equal(t, "bot-secret", incoming.Telegram.BotToken)
	require.Equal(t, "gw-rotated", incoming.Gateway.Auth.Token)
	require.Equal(t, "ws-secret", incoming.WebSearch.APIKey)
	require.Equal(t, "Bearer otel-secret", incoming.OTel.Headers["authorization"])
}

func TestRedactMCPHTTPAuthSecrets(t *testing.T) {
	cur := &config.Config{
		MCPServers: []config.MCPServerConfig{{
			ID:   "srv1",
			Auth: config.MCPAuthConfig{ClientSecret: "cs", Token: "tk"},
		}},
	}
	clone := &config.Config{
		MCPServers: []config.MCPServerConfig{{
			ID:   "srv1",
			Auth: config.MCPAuthConfig{ClientSecret: "cs", Token: "tk"},
		}},
	}
	redactConfigSecrets(clone)
	require.Equal(t, redactedSentinel, clone.MCPServers[0].Auth.ClientSecret)
	require.Equal(t, redactedSentinel, clone.MCPServers[0].Auth.Token)

	incoming := &config.Config{
		MCPServers: []config.MCPServerConfig{{
			ID:   "srv1",
			Auth: config.MCPAuthConfig{ClientSecret: redactedSentinel, Token: redactedSentinel},
		}},
	}
	restoreSecretScalars(incoming, cur)
	require.Equal(t, "cs", incoming.MCPServers[0].Auth.ClientSecret)
	require.Equal(t, "tk", incoming.MCPServers[0].Auth.Token)
}
