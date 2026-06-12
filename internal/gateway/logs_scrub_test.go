package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bearer", "auth failed: Authorization: Bearer sk-abc123XYZ tail", "auth failed: Authorization: Bearer [REDACTED] tail"},
		{"apikey", "connecting with api_key=supersecretvalue now", "connecting with api_key=[REDACTED] now"},
		{"token kv", "token: abcdef123456 done", "token: [REDACTED] done"},
		{"openai sk", "key sk-ABCDEFGHIJKLMNOPQR in use", "key [REDACTED] in use"},
		{"telegram bot", "url https://api.telegram.org/bot123456789:AAEhBOweik6ad9r_QXVvQ_abcdefghij/send", "url https://api.telegram.org/bot[REDACTED]/send"},
		{"url userinfo", "dialing https://user:p4ss@host/path", "dialing https://[REDACTED]@host/path"},
		{"clean line untouched", "agent loop completed in 1.2s with 3 tool calls", "agent loop completed in 1.2s with 3 tool calls"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, scrubSecrets(c.in))
		})
	}
}
