package telemetry

import (
	"context"

	"github.com/pug-sh/pug/internal/correlation"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SetTenant records the org and project a request acts for on its span and on
// every log line it writes from here on.
func SetTenant(ctx context.Context, orgID, projectID string) {
	correlation.SetTenant(ctx, correlation.Tenant{OrgID: orgID, ProjectID: projectID})
	var attrs []attribute.KeyValue
	if orgID != "" {
		attrs = append(attrs, attribute.String("org.id", orgID))
	}
	if projectID != "" {
		attrs = append(attrs, attribute.String("project.id", projectID))
	}
	trace.SpanFromContext(ctx).SetAttributes(attrs...)
}
