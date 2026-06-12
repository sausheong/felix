package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRawDispositionForContentType(t *testing.T) {
	require.Equal(t, "attachment", rawDisposition("text/html; charset=utf-8"))
	require.Equal(t, "attachment", rawDisposition("image/svg+xml"))
	require.Equal(t, "attachment", rawDisposition("application/octet-stream"))
	require.Equal(t, "inline", rawDisposition("image/png"))
	require.Equal(t, "inline", rawDisposition("text/plain; charset=utf-8"))
	require.Equal(t, "inline", rawDisposition("application/pdf"))
	require.Equal(t, "inline", rawDisposition("application/json"))
}
