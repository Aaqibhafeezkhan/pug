package telemetry

import (
	"context"
	"testing"

	"github.com/pug-sh/pug/internal/correlation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestSetTenant(t *testing.T) {
	tests := []struct {
		name      string
		orgID     string
		projectID string
		wantAttrs map[string]string
	}{
		{
			name:      "org and project",
			orgID:     "org-1",
			projectID: "proj-1",
			wantAttrs: map[string]string{"org.id": "org-1", "project.id": "proj-1"},
		},
		{
			name:      "org alone",
			orgID:     "org-1",
			wantAttrs: map[string]string{"org.id": "org-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

			ctx, span := tp.Tracer("test").Start(correlation.WithTenant(context.Background()), "rpc")
			SetTenant(ctx, tt.orgID, tt.projectID)
			span.End()

			want := correlation.Tenant{OrgID: tt.orgID, ProjectID: tt.projectID}
			if got := correlation.TenantFromContext(ctx); got != want {
				t.Errorf("tenant = %+v, want %+v", got, want)
			}
			got := map[string]string{}
			for _, a := range exporter.GetSpans()[0].Attributes {
				got[string(a.Key)] = a.Value.AsString()
			}
			if len(got) != len(tt.wantAttrs) {
				t.Errorf("span attrs = %v, want %v", got, tt.wantAttrs)
			}
			for k, v := range tt.wantAttrs {
				if got[k] != v {
					t.Errorf("span attr %s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
