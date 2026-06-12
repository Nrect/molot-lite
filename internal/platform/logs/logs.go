// Package logs configures slog for the service: JSON in prod, text
// locally (LOG_FORMAT), with a handler wrapper that enriches every
// record with the request id carried in the context (the server
// middleware puts it there on each request).
package logs

import (
	"context"
	"log/slog"
	"os"
)

// NewLogger builds the process logger. format is "json" or "text"
// (anything else falls back to JSON — the production-safe choice).
func NewLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}

	var h slog.Handler
	switch format {
	case "text":
		h = slog.NewTextHandler(os.Stdout, opts)
	default:
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(NewContextHandler(h))
}

// NewContextHandler wraps next with the context-enriching handler used
// by NewLogger; exposed so tests and custom sinks share the exact
// production enrichment path.
func NewContextHandler(next slog.Handler) slog.Handler {
	return contextHandler{next: next}
}

type requestIDKey struct{}

// ContextWithRequestID attaches a request id to the context; every log
// record made with this context carries it. The server middleware calls
// this on each request before the handler runs.
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestIDFromContext returns the request id, if any.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

// contextHandler decorates a slog.Handler, adding context-scoped
// attributes (request_id).
type contextHandler struct {
	next slog.Handler
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id, ok := RequestIDFromContext(ctx); ok {
		rec.AddAttrs(slog.String("request_id", id))
	}
	return h.next.Handle(ctx, rec)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{next: h.next.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{next: h.next.WithGroup(name)}
}
