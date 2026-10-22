package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pug-sh/pug/internal/correlation"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type cancelCase struct {
	name     string
	ctx      context.Context
	canceled bool
}

// cancelCases are the contexts canceledRequest must tell apart. Only a canceled
// context with a correlation id is a canceled server request.
func cancelCases(t *testing.T) []cancelCase {
	t.Helper()
	request := func() context.Context { return correlation.WithID(context.Background(), "id-req") }
	canceled := func(ctx context.Context) context.Context {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	expired, cancel := context.WithDeadline(request(), time.Now())
	t.Cleanup(cancel)
	return []cancelCase{
		{name: "canceled request", ctx: canceled(request()), canceled: true},
		{name: "canceled without an id", ctx: canceled(context.Background())},
		{name: "detached from a canceled request", ctx: context.WithoutCancel(canceled(request()))},
		{name: "request past its deadline", ctx: expired},
		{name: "live request", ctx: request()},
	}
}

func TestRecordError_skipsCanceledRequest(t *testing.T) {
	for _, tc := range cancelCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

			ctx, span := tp.Tracer("test").Start(tc.ctx, "test-span")
			RecordError(ctx, errors.New("read failed"))
			span.End()

			s := exporter.GetSpans()[0]
			wantCode, wantEvents := codes.Error, 1
			if tc.canceled {
				wantCode, wantEvents = codes.Unset, 0
			}
			if s.Status.Code != wantCode || len(s.Events) != wantEvents {
				t.Errorf("status = %v with %d events, want %v with %d", s.Status.Code, len(s.Events), wantCode, wantEvents)
			}
		})
	}
}

func TestRecordError_NilError(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Errorf("failed to shutdown tracer provider: %v", err)
		}
	}()

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	RecordError(ctx, nil)
	span.End()

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	s := spans[0]
	if s.Status.Code != codes.Unset {
		t.Errorf("status = %v, want Unset for nil error", s.Status.Code)
	}
	if len(s.Events) != 0 {
		t.Errorf("expected no events for nil error, got %d", len(s.Events))
	}
}

func TestRecordError_RecordsError(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Errorf("failed to shutdown tracer provider: %v", err)
		}
	}()

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	RecordError(ctx, errors.New("something broke"))
	span.End()

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	s := spans[0]
	if s.Status.Code != codes.Error {
		t.Errorf("status = %v, want Error", s.Status.Code)
	}
	if s.Status.Description != "something broke" {
		t.Errorf("status description = %q, want %q", s.Status.Description, "something broke")
	}
	if len(s.Events) == 0 {
		t.Fatal("expected at least one event")
	}
	if s.Events[0].Name != "exception" {
		t.Errorf("event name = %q, want %q", s.Events[0].Name, "exception")
	}
}

func TestRecordError_WithAttributes(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Errorf("failed to shutdown tracer provider: %v", err)
		}
	}()

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	RecordError(ctx, errors.New("db error"), attribute.String("db.system", "postgresql"))
	span.End()

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	s := spans[0]
	if len(s.Events) == 0 {
		t.Fatal("expected at least one event")
	}

	found := false
	for _, a := range s.Events[0].Attributes {
		if string(a.Key) == "db.system" && a.Value.AsString() == "postgresql" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected db.system=postgresql attribute on error event")
	}
}
