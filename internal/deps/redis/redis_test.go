package redis

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pug-sh/pug/internal/testutil"
)

// TestTracingOmitsCommandArguments pins that the client's spans never carry a
// command's arguments. Callers pass secrets as arguments (the cookieless salt
// through SET NX, the cached project row with its FCM service-account key as a
// script ARGV), and redisotel's default db.statement would export them.
func TestTracingOmitsCommandArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(old)
		_ = tp.Shutdown(context.Background())
	})

	rd := testutil.SetupRedis(t)
	ctx := t.Context()

	client, err := NewFromConfig(ctx, &Config{URL: rd.URL})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	t.Cleanup(func() { client.Close(context.Background()) })
	rdb := client.Unwrap()

	const secret = "s3cr3t-private-key-material"

	if err := rdb.SetNX(ctx, "test:salt", secret, time.Minute).Err(); err != nil {
		t.Fatalf("SetNX: %v", err)
	}
	// The script cache is server-wide and survives SetupRedis's FLUSHDB. Flushing
	// it makes EVALSHA fail with NOSCRIPT on every run, so the error path (an
	// exception event and the span status) is traced before the script falls back
	// to EVAL.
	if err := rdb.ScriptFlush(ctx).Err(); err != nil {
		t.Fatalf("ScriptFlush: %v", err)
	}
	script := goredis.NewScript(`return redis.call('SET', KEYS[1], ARGV[1])`)
	if err := script.Run(ctx, rdb, []string{"test:row"}, secret).Err(); err != nil {
		t.Fatalf("script.Run: %v", err)
	}
	if _, err := rdb.Pipelined(ctx, func(p goredis.Pipeliner) error {
		p.Set(ctx, "test:pipe", secret, time.Minute)
		return nil
	}); err != nil {
		t.Fatalf("Pipelined: %v", err)
	}

	spans := exporter.GetSpans()
	traced := make(map[string]bool, len(spans))
	for _, span := range spans {
		// A span attribute limit drops attributes silently, and a dropped
		// db.statement would hide the leak.
		if span.DroppedAttributes > 0 {
			t.Errorf("span %q dropped %d attributes", span.Name, span.DroppedAttributes)
		}
		for _, attr := range span.Attributes {
			// Checked by key, not only by the substring check below: rediscmd
			// hex-encodes a whole argument if any byte is outside printable ASCII
			// (a newline, an accented letter), so a leaked value need not contain
			// the secret as written.
			if attr.Key == "db.statement" {
				t.Errorf("span %q carries db.statement %q", span.Name, attr.Value.String())
			}
			if attr.Key == "db.system" && attr.Value.AsString() == "redis" {
				traced[span.Name] = true
			}
		}
		// Every field, the name, events and status included: a failed command
		// writes its error text into an exception event and the status.
		if strings.Contains(fmt.Sprintf("%+v", span), secret) {
			t.Errorf("span %q leaks a command argument", span.Name)
		}
	}
	// Guard against passing vacuously: each command that carries the secret must
	// have been traced as a Redis span with its attributes kept. "eval" shows
	// NOSCRIPT was hit.
	for _, want := range []string{"set", "evalsha", "eval", "redis.pipeline set"} {
		if !traced[want] {
			t.Errorf("no %q span with db.system=redis recorded", want)
		}
	}
}
