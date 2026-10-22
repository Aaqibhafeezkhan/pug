package correlation

import (
	"context"
	"testing"
)

func TestWithID_roundTrip(t *testing.T) {
	ctx := WithID(context.Background(), "abc123")
	if got := IDFromContext(ctx); got != "abc123" {
		t.Errorf("IDFromContext = %q, want %q", got, "abc123")
	}
}

func TestIDFromContext_emptyWhenUnset(t *testing.T) {
	if got := IDFromContext(context.Background()); got != "" {
		t.Errorf("IDFromContext = %q, want empty", got)
	}
}

func TestSetTenant_reachesParentContext(t *testing.T) {
	ctx := WithTenant(context.Background())
	want := Tenant{OrgID: "org-1", ProjectID: "proj-1"}
	SetTenant(WithID(ctx, "abc123"), want)
	if got := TenantFromContext(ctx); got != want {
		t.Errorf("TenantFromContext = %+v, want %+v", got, want)
	}
}

func TestSetTenant_replaces(t *testing.T) {
	ctx := WithTenant(context.Background())
	SetTenant(ctx, Tenant{OrgID: "org-1", ProjectID: "proj-1"})
	SetTenant(ctx, Tenant{OrgID: "org-2"})
	if got := TenantFromContext(ctx); got != (Tenant{OrgID: "org-2"}) {
		t.Errorf("TenantFromContext = %+v, want org-2 alone", got)
	}
}

func TestSetTenant_noOpWithoutWithTenant(t *testing.T) {
	ctx := context.Background()
	SetTenant(ctx, Tenant{OrgID: "org-1"})
	if got := TenantFromContext(ctx); got != (Tenant{}) {
		t.Errorf("TenantFromContext = %+v, want zero", got)
	}
}
