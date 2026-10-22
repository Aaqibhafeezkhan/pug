package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/sdk/log"
)

func newLogExporter(ctx context.Context) (log.Exporter, error) {
	var opts []otlploggrpc.Option
	if insecureExporter() {
		opts = append(opts, otlploggrpc.WithInsecure())
	}
	return otlploggrpc.New(ctx, opts...)
}

func newLoggerProvider(ctx context.Context) (*log.LoggerProvider, error) {
	logExporter, err := newLogExporter(ctx)
	if err != nil {
		return nil, err
	}

	loggerProvider := log.NewLoggerProvider(log.WithProcessor(log.NewBatchProcessor(logExporter)))
	return loggerProvider, nil
}

func installOTLPLogHandler(serviceName string, lp *log.LoggerProvider) {
	slog.SetDefault(slog.New(newCorrelationHandler(
		otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(lp), otelslog.WithSource(true)),
	)))
}
