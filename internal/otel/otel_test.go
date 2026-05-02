package otel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		out  string
	}{
		{
			name: "full url with trailing slash → strip path, keep host as-is",
			in:   "http://aiap-nprd-otel-inet-nlb-6752644d1ca24fad.elb.ap-southeast-1.amazonaws.com/",
			out:  "http://aiap-nprd-otel-inet-nlb-6752644d1ca24fad.elb.ap-southeast-1.amazonaws.com",
		},
		{
			name: "https with explicit 4318 port",
			in:   "https://collector.example.com:4318/",
			out:  "https://collector.example.com:4318",
		},
		{
			name: "bare host:port → http scheme prepended",
			in:   "localhost:4318",
			out:  "http://localhost:4318",
		},
		{
			name: "user-supplied path is stripped (SDK appends /v1/...)",
			in:   "https://collector.example.com/some/path",
			out:  "https://collector.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeEndpoint(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.out, got)
		})
	}
}

func TestNormalizeEndpointErrors(t *testing.T) {
	for _, tc := range []string{"", "http://"} {
		_, err := normalizeEndpoint(tc)
		assert.Error(t, err, "input %q should fail", tc)
	}
}

func TestSetupDisabled(t *testing.T) {
	// When Enabled=false, Setup must return a no-op Provider with a
	// nil error AND no exporter goroutines. Calling Tracer/Meter
	// returns valid no-op instruments so callers never need nil checks.
	p, err := Setup(context.Background(), Config{Enabled: false})
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Nil(t, p.TracerProvider)
	assert.Nil(t, p.MeterProvider)
	assert.Nil(t, p.LoggerProvider)

	// Tracer + Meter are still safe to call on a disabled provider —
	// they return no-op instruments.
	tr := p.Tracer("test")
	require.NotNil(t, tr)
	_, span := tr.Start(context.Background(), "test")
	span.End()

	m := p.Meter("test")
	require.NotNil(t, m)

	require.NoError(t, p.Shutdown(context.Background()))
}

func TestDisabledProviderIsNilSafe(t *testing.T) {
	// The Disabled() sentinel must be safe to use without any guards.
	p := Disabled()
	assert.NotNil(t, p.Tracer("anything"))
	assert.NotNil(t, p.Meter("anything"))
	assert.NoError(t, p.Shutdown(context.Background()))

	// And the typed-nil case (which the Provider field could be in
	// some callers): Tracer/Meter on a nil receiver still works.
	var pn *Provider
	assert.NotNil(t, pn.Tracer("x"))
	assert.NotNil(t, pn.Meter("x"))
	assert.NoError(t, pn.Shutdown(context.Background()))
}
