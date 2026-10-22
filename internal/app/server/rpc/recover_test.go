package rpc

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pug-sh/pug/internal/correlation"
)

// ctxCaptureHandler keeps the context of the last record it handled.
type ctxCaptureHandler struct{ ctx context.Context }

func (h *ctxCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *ctxCaptureHandler) Handle(ctx context.Context, _ slog.Record) error {
	h.ctx = ctx
	return nil
}
func (h *ctxCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *ctxCaptureHandler) WithGroup(string) slog.Handler      { return h }

// A panic on a canceled request is still recorded, and logged under a context the
// log handler does not downgrade, one that keeps the request's id.
func TestRecoverHandlerPanic_canceledRequestStaysAnError(t *testing.T) {
	logs := &ctxCaptureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })

	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("test").Start(correlation.WithID(context.Background(), "id-panic"), "server")
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_ = RecoverHandlerPanic(ctx, connect.Spec{Procedure: "/test.v1.Svc/Method"}, nil, "boom")
	span.End()

	if logs.ctx == nil {
		t.Fatal("the panic was not logged")
	}
	if logs.ctx.Err() != nil || correlation.IDFromContext(logs.ctx) != "id-panic" {
		t.Errorf("panic logged with err=%v id=%q, want a live context with id-panic",
			logs.ctx.Err(), correlation.IDFromContext(logs.ctx))
	}
	s := spans.GetSpans()[0]
	if s.Status.Code != codes.Error || len(s.Events) != 1 || s.Events[0].Name != "exception" {
		t.Errorf("span status = %v with events %v, want Error with one exception event", s.Status.Code, s.Events)
	}
}
