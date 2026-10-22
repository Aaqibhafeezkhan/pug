package telemetry

import (
	"context"
	"log/slog"

	"github.com/pug-sh/pug/internal/correlation"
	"go.opentelemetry.io/otel/trace"
)

const (
	orgIDKey     = "org_id"
	projectIDKey = "project_id"
)

// correlationHandler stamps the per-request correlation id (and the trace id
// when a span is active) onto every log record, so all logs for a request
// correlate with the error_id returned to the client. It also stamps the
// request's tenant once known. No-ops when none is set.
type correlationHandler struct{ inner slog.Handler }

func newCorrelationHandler(inner slog.Handler) slog.Handler {
	return &correlationHandler{inner: inner}
}

func (h *correlationHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *correlationHandler) Handle(ctx context.Context, r slog.Record) error {
	// error_id mirrors the key returned in API error responses, so a user-reported
	// id cross-references directly to these logs. trace_id is stamped as a record
	// attr because in stdout mode (no OTLP endpoint configured) the inner handler is
	// a plain slog text handler with no native trace correlation, so this attr is the
	// only way the trace id reaches the log. Under the otelslog inner handler (OTLP
	// mode) it is redundant with the SDK-set native trace id — harmless. (The
	// shutdown fallback text handler in otel.go is not wrapped by this handler, so it
	// does not receive these attrs.)
	if id := correlation.IDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String("error_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	if t := correlation.TenantFromContext(ctx); t != (correlation.Tenant{}) {
		addTenant(&r, t)
	}
	return h.inner.Handle(ctx, r)
}

// addTenant leaves an org_id or project_id the call site set itself, so the key
// appears once and the explicit value wins.
func addTenant(r *slog.Record, t correlation.Tenant) {
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case orgIDKey:
			t.OrgID = ""
		case projectIDKey:
			t.ProjectID = ""
		}
		return true
	})
	if t.OrgID != "" {
		r.AddAttrs(slog.String(orgIDKey, t.OrgID))
	}
	if t.ProjectID != "" {
		r.AddAttrs(slog.String(projectIDKey, t.ProjectID))
	}
}

func (h *correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &correlationHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *correlationHandler) WithGroup(name string) slog.Handler {
	return &correlationHandler{inner: h.inner.WithGroup(name)}
}
