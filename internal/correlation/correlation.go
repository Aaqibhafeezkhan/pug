// Package correlation carries per-request correlation state through context: the
// correlation id and the tenant the request acts for. It is a leaf package so both
// the RPC layer (which sets them) and the telemetry layer (which stamps them onto
// logs) can depend on it without a cycle.
package correlation

import (
	"context"
	"sync/atomic"
)

type ctxKey struct{}

type tenantKey struct{}

// Tenant is the org and project a request acts for.
type Tenant struct {
	OrgID     string
	ProjectID string
}

// WithID returns a context carrying the given correlation id.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// IDFromContext returns the correlation id, or "" if none is set.
func IDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// WithTenant returns a context with an empty tenant for SetTenant to fill. It holds
// a pointer so a tenant found deep in a request reaches logs written further out.
func WithTenant(ctx context.Context) context.Context {
	return context.WithValue(ctx, tenantKey{}, new(atomic.Pointer[Tenant]))
}

// WithoutTenant hides ctx's tenant behind an empty one, for a log line that speaks
// for many requests.
func WithoutTenant(ctx context.Context) context.Context { return WithTenant(ctx) }

// SetTenant replaces the tenant on ctx. It does nothing without WithTenant.
func SetTenant(ctx context.Context, t Tenant) {
	if p, ok := ctx.Value(tenantKey{}).(*atomic.Pointer[Tenant]); ok {
		p.Store(&t)
	}
}

// TenantFromContext returns the tenant set on ctx, or the zero Tenant.
func TenantFromContext(ctx context.Context) Tenant {
	if p, ok := ctx.Value(tenantKey{}).(*atomic.Pointer[Tenant]); ok {
		if t := p.Load(); t != nil {
			return *t
		}
	}
	return Tenant{}
}
