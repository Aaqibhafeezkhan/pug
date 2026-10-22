package telemetry

import (
	"context"
	"errors"

	"github.com/pug-sh/pug/internal/correlation"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// canceledRequest reports a canceled context under a server request. Usually the
// client left, but an errgroup or the code's own cancel match too. Only a request
// carries a correlation id, so a job stopped by SIGTERM never matches.
func canceledRequest(ctx context.Context) bool {
	return correlation.IDFromContext(ctx) != "" && errors.Is(ctx.Err(), context.Canceled)
}

// RecordError records err on the current span from ctx, sets the span status to
// Error, and captures a stack trace on the error event. Any additional attributes
// are also attached to the error event. It is a no-op when err is nil; when ctx
// carries no active span, the OTel SDK's no-op span silently discards the calls.
// Callers that need errors to always be recorded should also use slog.ErrorContext.
//
// A canceled server request records nothing: it is not a server error, and the
// span's Error status could not be lowered once the RPC answers CANCELED.
func RecordError(ctx context.Context, err error, attributes ...attribute.KeyValue) {
	if canceledRequest(ctx) {
		return
	}
	RecordErrorOnSpan(trace.SpanFromContext(ctx), err, attributes...)
}

// RecordErrorOnSpan is RecordError against a span held directly. Use it where a
// span outlives the call that started it, so the originating context does not
// have to be retained purely to find the span again.
func RecordErrorOnSpan(span trace.Span, err error, attributes ...attribute.KeyValue) {
	if err == nil {
		return
	}
	span.RecordError(err, trace.WithStackTrace(true), trace.WithAttributes(attributes...))
	span.SetStatus(codes.Error, err.Error())
}
