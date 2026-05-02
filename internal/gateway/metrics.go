package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics collects gateway operational metrics in Prometheus text format.
//
// The atomic counters drive the local /metrics endpoint (cheap, no
// dependency); the optional OTel instruments mirror each increment so
// the same data also flows to a configured OTLP collector.
type Metrics struct {
	requestsTotal   atomic.Int64
	wsConnections   atomic.Int64
	wsMessagesTotal atomic.Int64
	toolCallsTotal  atomic.Int64
	llmCallsTotal   atomic.Int64
	errorsTotal     atomic.Int64
	startTime       time.Time

	toolCounts map[string]*atomic.Int64
	mu         sync.RWMutex

	// OTel mirrors. nil when no OTel meter was passed at construction;
	// every Inc* method nil-guards before touching them.
	otelRequests   metric.Int64Counter
	otelWSConns    metric.Int64UpDownCounter
	otelWSMessages metric.Int64Counter
	otelToolCalls  metric.Int64Counter
	otelLLMCalls   metric.Int64Counter
	otelErrors     metric.Int64Counter
}

// NewMetrics creates a metrics collector with no OTel mirroring.
// Equivalent to NewMetricsWithMeter(nil).
func NewMetrics() *Metrics {
	return NewMetricsWithMeter(nil)
}

// NewMetricsWithMeter creates a metrics collector backed by both the
// existing atomic counters AND a parallel OTel instrument set. Pass
// nil for `meter` to skip the OTel half (the resulting *Metrics is
// identical to what NewMetrics returns). When `meter` is non-nil,
// each Inc*/Dec* method also bumps the corresponding OTel instrument
// so the metric flows to the configured OTLP collector.
func NewMetricsWithMeter(meter metric.Meter) *Metrics {
	m := &Metrics{
		startTime:  time.Now(),
		toolCounts: make(map[string]*atomic.Int64),
	}
	if meter == nil {
		return m
	}
	var err error
	m.otelRequests, err = meter.Int64Counter("felix.http.requests",
		metric.WithDescription("Total HTTP requests handled by the gateway."))
	if err != nil {
		slog.Warn("otel: failed to register felix.http.requests", "error", err)
	}
	m.otelWSConns, err = meter.Int64UpDownCounter("felix.ws.connections.active",
		metric.WithDescription("Active WebSocket connections."))
	if err != nil {
		slog.Warn("otel: failed to register felix.ws.connections.active", "error", err)
	}
	m.otelWSMessages, err = meter.Int64Counter("felix.ws.messages",
		metric.WithDescription("Total WebSocket messages received."))
	if err != nil {
		slog.Warn("otel: failed to register felix.ws.messages", "error", err)
	}
	m.otelToolCalls, err = meter.Int64Counter("felix.tool.calls",
		metric.WithDescription("Tool invocations, tagged by tool.name."))
	if err != nil {
		slog.Warn("otel: failed to register felix.tool.calls", "error", err)
	}
	m.otelLLMCalls, err = meter.Int64Counter("felix.llm.calls",
		metric.WithDescription("LLM API calls."))
	if err != nil {
		slog.Warn("otel: failed to register felix.llm.calls", "error", err)
	}
	m.otelErrors, err = meter.Int64Counter("felix.errors",
		metric.WithDescription("Error events."))
	if err != nil {
		slog.Warn("otel: failed to register felix.errors", "error", err)
	}
	// Observable gauge for uptime — read on every collection cycle.
	if _, err := meter.Float64ObservableGauge("felix.uptime.seconds",
		metric.WithDescription("Time since gateway started."),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			o.Observe(time.Since(m.startTime).Seconds())
			return nil
		}),
	); err != nil {
		slog.Warn("otel: failed to register felix.uptime.seconds", "error", err)
	}
	return m
}

// IncRequests increments the HTTP request counter.
func (m *Metrics) IncRequests() {
	m.requestsTotal.Add(1)
	if m.otelRequests != nil {
		m.otelRequests.Add(context.Background(), 1)
	}
}

// IncWSConnections increments the active WebSocket connection counter.
func (m *Metrics) IncWSConnections() {
	m.wsConnections.Add(1)
	if m.otelWSConns != nil {
		m.otelWSConns.Add(context.Background(), 1)
	}
}

// DecWSConnections decrements the active WebSocket connection counter.
func (m *Metrics) DecWSConnections() {
	m.wsConnections.Add(-1)
	if m.otelWSConns != nil {
		m.otelWSConns.Add(context.Background(), -1)
	}
}

// IncWSMessages increments the WebSocket message counter.
func (m *Metrics) IncWSMessages() {
	m.wsMessagesTotal.Add(1)
	if m.otelWSMessages != nil {
		m.otelWSMessages.Add(context.Background(), 1)
	}
}

// IncToolCalls increments the tool call counter for a specific tool.
func (m *Metrics) IncToolCalls(toolName string) {
	m.toolCallsTotal.Add(1)

	m.mu.RLock()
	counter, ok := m.toolCounts[toolName]
	m.mu.RUnlock()

	if !ok {
		m.mu.Lock()
		counter, ok = m.toolCounts[toolName]
		if !ok {
			counter = &atomic.Int64{}
			m.toolCounts[toolName] = counter
		}
		m.mu.Unlock()
	}

	counter.Add(1)

	if m.otelToolCalls != nil {
		m.otelToolCalls.Add(context.Background(), 1,
			metric.WithAttributes(attribute.String("tool.name", toolName)))
	}
}

// IncLLMCalls increments the LLM call counter.
func (m *Metrics) IncLLMCalls() {
	m.llmCallsTotal.Add(1)
	if m.otelLLMCalls != nil {
		m.otelLLMCalls.Add(context.Background(), 1)
	}
}

// IncErrors increments the error counter.
func (m *Metrics) IncErrors() {
	m.errorsTotal.Add(1)
	if m.otelErrors != nil {
		m.otelErrors.Add(context.Background(), 1)
	}
}

// Handler returns an HTTP handler that serves Prometheus-compatible metrics.
func (m *Metrics) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		var b strings.Builder

		b.WriteString("# HELP felix_uptime_seconds Time since gateway started.\n")
		b.WriteString("# TYPE felix_uptime_seconds gauge\n")
		fmt.Fprintf(&b, "felix_uptime_seconds %.1f\n\n", time.Since(m.startTime).Seconds())

		b.WriteString("# HELP felix_http_requests_total Total HTTP requests.\n")
		b.WriteString("# TYPE felix_http_requests_total counter\n")
		fmt.Fprintf(&b, "felix_http_requests_total %d\n\n", m.requestsTotal.Load())

		b.WriteString("# HELP felix_ws_connections_active Active WebSocket connections.\n")
		b.WriteString("# TYPE felix_ws_connections_active gauge\n")
		fmt.Fprintf(&b, "felix_ws_connections_active %d\n\n", m.wsConnections.Load())

		b.WriteString("# HELP felix_ws_messages_total Total WebSocket messages received.\n")
		b.WriteString("# TYPE felix_ws_messages_total counter\n")
		fmt.Fprintf(&b, "felix_ws_messages_total %d\n\n", m.wsMessagesTotal.Load())

		b.WriteString("# HELP felix_tool_calls_total Total tool calls.\n")
		b.WriteString("# TYPE felix_tool_calls_total counter\n")
		fmt.Fprintf(&b, "felix_tool_calls_total %d\n\n", m.toolCallsTotal.Load())

		b.WriteString("# HELP felix_llm_calls_total Total LLM API calls.\n")
		b.WriteString("# TYPE felix_llm_calls_total counter\n")
		fmt.Fprintf(&b, "felix_llm_calls_total %d\n\n", m.llmCallsTotal.Load())

		b.WriteString("# HELP felix_errors_total Total errors.\n")
		b.WriteString("# TYPE felix_errors_total counter\n")
		fmt.Fprintf(&b, "felix_errors_total %d\n\n", m.errorsTotal.Load())

		// Per-tool breakdown
		m.mu.RLock()
		if len(m.toolCounts) > 0 {
			b.WriteString("# HELP felix_tool_calls_by_tool Tool calls by tool name.\n")
			b.WriteString("# TYPE felix_tool_calls_by_tool counter\n")

			// Sort tool names for deterministic output
			names := make([]string, 0, len(m.toolCounts))
			for name := range m.toolCounts {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				fmt.Fprintf(&b, "felix_tool_calls_by_tool{tool=%q} %d\n", name, m.toolCounts[name].Load())
			}
		}
		m.mu.RUnlock()

		w.Write([]byte(b.String()))
	}
}
