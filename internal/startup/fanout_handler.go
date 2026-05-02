package startup

import (
	"context"
	"log/slog"
)

// fanoutHandler dispatches every slog record to multiple inner
// handlers. Used to plumb log records into both the local
// TextHandler (stderr / felix-app.log) AND the OTel slog bridge so
// the same record reaches the local log file and the configured
// OTLP/logs collector.
//
// Errors from individual children are aggregated by returning the
// first non-nil; we never short-circuit so one slow handler can't
// silence the others.
type fanoutHandler struct {
	children []slog.Handler
}

// newFanoutHandler returns a Handler that dispatches to all the
// supplied non-nil children. If only one is non-nil it is returned
// directly so we don't pay the dispatch overhead in the common case.
func newFanoutHandler(children ...slog.Handler) slog.Handler {
	out := make([]slog.Handler, 0, len(children))
	for _, c := range children {
		if c != nil {
			out = append(out, c)
		}
	}
	switch len(out) {
	case 0:
		return slog.NewTextHandler(noopWriter{}, nil)
	case 1:
		return out[0]
	}
	return &fanoutHandler{children: out}
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, c := range h.children {
		if c.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, c := range h.children {
		// Each child gets its own clone so attributes added by one don't
		// leak into another (slog.Record is by-value but contains attr
		// pointers; Clone is the correct copy).
		if err := c.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(h.children))
	for i, c := range h.children {
		out[i] = c.WithAttrs(attrs)
	}
	return &fanoutHandler{children: out}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(h.children))
	for i, c := range h.children {
		out[i] = c.WithGroup(name)
	}
	return &fanoutHandler{children: out}
}

// noopWriter is the io.Writer returned to slog.NewTextHandler when
// every child was nil — it discards everything so Handle never errors.
type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
