// Package otel wires Felix's existing instrumentation (slog, the
// gateway Metrics counters, and the agent.Trace phase markers) into
// an OpenTelemetry OTLP/HTTP exporter pipeline.
//
// The package is intentionally self-contained: callers ask for a
// Provider via Setup(ctx, cfg). When cfg.Enabled is false (or any
// signal toggle is false), the corresponding exporter is simply nil
// and downstream callers keep working — every consumer in Felix
// guards on nil.
//
// Endpoint handling is OTLP/HTTP-specific. The configured Endpoint is
// a full URL (e.g. http://collector.example.com/ or https://...:4318);
// we parse it once into host[:port] for the SDK options, and the SDK
// itself appends /v1/{traces,metrics,logs} when posting. The scheme
// drives Insecure (http) vs TLS (https).
package otel

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Config is the runtime view of the OTel configuration. It mirrors
// config.OTelConfig but lives here to keep the otel package free of
// any dependency on internal/config (avoiding a cycle when the agent
// or gateway packages need to import otel).
type Config struct {
	Enabled     bool
	Endpoint    string  // full URL, e.g. http://host:4318/ — scheme decides Insecure
	ServiceName string  // resource attribute service.name
	Version     string  // resource attribute service.version (build version)
	SampleRatio float64 // 0.0..1.0; 0 → never; >=1 → always
	Headers     map[string]string
	Traces      bool
	Metrics     bool
	Logs        bool
}

// Provider holds the SDK objects spun up by Setup. Any of TracerProvider,
// MeterProvider, or LoggerProvider may be nil when its signal is
// disabled. The Tracer / Meter accessors always return a valid (no-op
// if necessary) instrument so callers never have to nil-check.
type Provider struct {
	cfg            Config
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
	LoggerHandler  slog.Handler // bridge to forward slog records into LoggerProvider
}

// Disabled returns a Provider with everything off. Safe to use as a
// non-nil sentinel when the user has not enabled OTel.
func Disabled() *Provider {
	return &Provider{cfg: Config{}}
}

// Setup builds the SDK pipelines for whichever signals are enabled.
// Returns Disabled() (and a nil error) when cfg.Enabled is false so
// callers always get a usable Provider back. Setup never panics on
// exporter init failures — it logs a warning and disables that signal,
// because telemetry is auxiliary and must not take down the gateway.
func Setup(ctx context.Context, cfg Config) (*Provider, error) {
	if !cfg.Enabled {
		return Disabled(), nil
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "felix"
	}
	if cfg.SampleRatio <= 0 {
		cfg.SampleRatio = 1.0
	}

	endpoint, err := normalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return Disabled(), fmt.Errorf("parse otel endpoint: %w", err)
	}

	res, err := buildResource(cfg)
	if err != nil {
		return Disabled(), fmt.Errorf("build otel resource: %w", err)
	}

	prov := &Provider{cfg: cfg}

	if cfg.Traces {
		tp, err := buildTracerProvider(ctx, endpoint, cfg.Headers, res, cfg.SampleRatio)
		if err != nil {
			slog.Warn("otel: traces disabled (exporter init failed)", "error", err)
		} else {
			prov.TracerProvider = tp
			otelapi.SetTracerProvider(tp)
		}
	}

	if cfg.Metrics {
		mp, err := buildMeterProvider(ctx, endpoint, cfg.Headers, res)
		if err != nil {
			slog.Warn("otel: metrics disabled (exporter init failed)", "error", err)
		} else {
			prov.MeterProvider = mp
			otelapi.SetMeterProvider(mp)
		}
	}

	if cfg.Logs {
		lp, err := buildLoggerProvider(ctx, endpoint, cfg.Headers, res)
		if err != nil {
			slog.Warn("otel: logs disabled (exporter init failed)", "error", err)
		} else {
			prov.LoggerProvider = lp
			// otelslog.NewHandler builds a slog.Handler that forwards records
			// through the named LoggerProvider as OTLP log records.
			prov.LoggerHandler = otelslog.NewHandler("felix",
				otelslog.WithLoggerProvider(lp))
		}
	}

	slog.Info("otel: setup complete",
		"endpoint", cfg.Endpoint,
		"service_name", cfg.ServiceName,
		"traces", prov.TracerProvider != nil,
		"metrics", prov.MeterProvider != nil,
		"logs", prov.LoggerProvider != nil,
		"sample_ratio", cfg.SampleRatio,
	)
	return prov, nil
}

// Tracer returns a named tracer for callers to use. Returns a no-op
// tracer when traces are disabled, so callers never need a nil guard.
func (p *Provider) Tracer(name string) oteltrace.Tracer {
	if p == nil || p.TracerProvider == nil {
		return tracenoop.NewTracerProvider().Tracer(name)
	}
	return p.TracerProvider.Tracer(name)
}

// Meter returns a named meter. Returns a no-op meter when metrics are
// disabled.
func (p *Provider) Meter(name string) otelmetric.Meter {
	if p == nil || p.MeterProvider == nil {
		return metricnoop.NewMeterProvider().Meter(name)
	}
	return p.MeterProvider.Meter(name)
}

// Shutdown drains exporters within the supplied context's deadline.
// Errors are logged but never returned upward — telemetry must not
// block process exit. A 5s deadline is reasonable for healthy networks.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.TracerProvider != nil {
		if err := p.TracerProvider.Shutdown(ctx); err != nil {
			slog.Warn("otel: tracer shutdown error", "error", err)
		}
	}
	if p.MeterProvider != nil {
		if err := p.MeterProvider.Shutdown(ctx); err != nil {
			slog.Warn("otel: meter shutdown error", "error", err)
		}
	}
	if p.LoggerProvider != nil {
		if err := p.LoggerProvider.Shutdown(ctx); err != nil {
			slog.Warn("otel: logger shutdown error", "error", err)
		}
	}
	return nil
}

// normalizeEndpoint returns a canonical OTLP/HTTP base URL: scheme +
// host[:port] with a single trailing slash and any user-supplied path
// stripped. The SDK's WithEndpointURL appends /v1/{traces,metrics,logs}
// per signal, so we MUST NOT carry a trailing path here or the SDK
// would produce "/users-path/v1/traces" instead of "/v1/traces".
//
// Port handling: respect whatever the user wrote. If the URL has no
// explicit port, the scheme's default applies (80 for http, 443 for
// https). We do NOT force the OTLP/HTTP default port (4318) because
// load-balancers (NLBs, ingresses) commonly front collectors on 80/443
// and the user's explicit URL already encodes their intent.
//
// Returns the normalized URL ready for WithEndpointURL.
func normalizeEndpoint(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("endpoint is empty")
	}
	// If the user pasted "host:4318" with no scheme, prepend http://
	// so url.Parse doesn't treat the colon as part of the path.
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("endpoint has no host: %q", raw)
	}
	// Strip any path/fragment/query the user happened to include.
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func buildResource(cfg Config) (*resource.Resource, error) {
	hostname, _ := os.Hostname()
	kvs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
		semconv.HostName(hostname),
		semconv.ProcessPID(os.Getpid()),
	}
	if cfg.Version != "" {
		kvs = append(kvs, semconv.ServiceVersion(cfg.Version))
	}
	return resource.Merge(resource.Default(), resource.NewSchemaless(kvs...))
}

func buildTracerProvider(ctx context.Context, endpointURL string, headers map[string]string, res *resource.Resource, ratio float64) (*sdktrace.TracerProvider, error) {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(endpointURL + "/v1/traces"),
		otlptracehttp.WithTimeout(10 * time.Second),
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(ratio)),
	)
	return tp, nil
}

func buildMeterProvider(ctx context.Context, endpointURL string, headers map[string]string, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpointURL(endpointURL + "/v1/metrics"),
		otlpmetrichttp.WithTimeout(10 * time.Second),
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(headers))
	}
	exp, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp,
			sdkmetric.WithInterval(30*time.Second))),
		sdkmetric.WithResource(res),
	)
	return mp, nil
}

func buildLoggerProvider(ctx context.Context, endpointURL string, headers map[string]string, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	opts := []otlploghttp.Option{
		otlploghttp.WithEndpointURL(endpointURL + "/v1/logs"),
		otlploghttp.WithTimeout(10 * time.Second),
	}
	if len(headers) > 0 {
		opts = append(opts, otlploghttp.WithHeaders(headers))
	}
	exp, err := otlploghttp.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(res),
	)
	return lp, nil
}
