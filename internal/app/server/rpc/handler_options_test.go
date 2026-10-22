package rpc

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/authn"
	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"

	coreorgs "github.com/pug-sh/pug/internal/core/orgs"
	"github.com/pug-sh/pug/internal/correlation"
	orgsv1 "github.com/pug-sh/pug/internal/gen/proto/dashboard/orgs/v1"
	"github.com/pug-sh/pug/internal/gen/proto/dashboard/orgs/v1/orgsv1connect"
	profilesv1 "github.com/pug-sh/pug/internal/gen/proto/shared/profiles/v1"
	"github.com/pug-sh/pug/internal/gen/proto/shared/profiles/v1/profilesv1connect"
	"github.com/pug-sh/pug/internal/gen/repo/dbread"
)

type tenantCaptureHandler struct {
	mu      sync.Mutex
	tenants []correlation.Tenant
}

func (h *tenantCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *tenantCaptureHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "rpc ok" || r.Message == "rpc error" {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.tenants = append(h.tenants, correlation.TenantFromContext(ctx))
	}
	return nil
}
func (h *tenantCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *tenantCaptureHandler) WithGroup(string) slog.Handler      { return h }

func (h *tenantCaptureHandler) got() []correlation.Tenant {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tenants
}

// recordChain returns HandlerOptions wired to capture each access log's tenant and
// the server span.
func recordChain(t *testing.T, lookup memberRoleLookup) (connect.HandlerOption, *tenantCaptureHandler, *tracetest.InMemoryExporter) {
	t.Helper()
	logs := &tenantCaptureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })

	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp))
	if err != nil {
		t.Fatalf("otelconnect.NewInterceptor: %v", err)
	}
	return HandlerOptions(otel, mustAuthorizer(t), lookup), logs, spans
}

func serveAs(t *testing.T, principal *Principal, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(authn.SetInfo(r.Context(), principal)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func onlySpanAttrs(t *testing.T, spans *tracetest.InMemoryExporter) map[string]string {
	t.Helper()
	got := spans.GetSpans()
	if len(got) != 1 {
		t.Fatalf("recorded %d spans, want the one server span", len(got))
	}
	return spanAttrs(got[0])
}

// TestHandlerOptions_rejectedRequestLogsTenant pins the chain order: PrincipalInterceptor
// must run inside otel, or the server span loses the principal, and before validate,
// or a request that fails validation logs no tenant.
func TestHandlerOptions_rejectedRequestLogsTenant(t *testing.T) {
	opts, logs, spans := recordChain(t, fakeRoleLookup{})
	_, handler := profilesv1connect.NewProfilesServiceHandler(profilesv1connect.UnimplementedProfilesServiceHandler{}, opts)
	srv := serveAs(t, &Principal{
		AuthType: AuthTypePrivateKey,
		Project:  &dbread.Project{ID: "proj-1", OrgID: "org-1"},
	}, handler)

	_, err := profilesv1connect.NewProfilesServiceClient(srv.Client(), srv.URL).
		Get(context.Background(), connect.NewRequest(&profilesv1.GetRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument from validate", connect.CodeOf(err))
	}

	want := correlation.Tenant{OrgID: "org-1", ProjectID: "proj-1"}
	if got := logs.got(); len(got) != 1 || got[0] != want {
		t.Errorf("access log tenants = %+v, want one with %+v", got, want)
	}
	attrs := onlySpanAttrs(t, spans)
	for k, v := range map[string]string{"auth.type": "priv_key", "org.id": "org-1", "project.id": "proj-1"} {
		if attrs[k] != v {
			t.Errorf("span %s = %q, want %q", k, attrs[k], v)
		}
	}
}

// TestHandlerOptions_orgRPCNamesOnlyAuthorizedOrg: the chain gates an org RPC, and
// its logs and span name the message's org once authorized, never the x-project-id
// project the caller sent.
func TestHandlerOptions_orgRPCNamesOnlyAuthorizedOrg(t *testing.T) {
	tests := []struct {
		name     string
		lookup   fakeRoleLookup
		wantCode connect.Code
		want     correlation.Tenant
	}{
		{name: "refused", lookup: fakeRoleLookup{err: coreorgs.ErrMemberNotFound}, wantCode: connect.CodePermissionDenied},
		{name: "authorized", lookup: fakeRoleLookup{role: coreorgs.RoleViewer}, wantCode: connect.CodeUnimplemented, want: correlation.Tenant{OrgID: "org-9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, logs, spans := recordChain(t, tt.lookup)
			_, handler := orgsv1connect.NewOrgsServiceHandler(orgsv1connect.UnimplementedOrgsServiceHandler{}, opts)
			srv := serveAs(t, &Principal{
				AuthType: AuthTypeJWT,
				Customer: &dbread.Customer{ID: "cust-1"},
				Project:  &dbread.Project{ID: "proj-1", OrgID: "org-1"},
			}, handler)

			_, err := orgsv1connect.NewOrgsServiceClient(srv.Client(), srv.URL).
				ListMembers(context.Background(), connect.NewRequest(&orgsv1.ListMembersRequest{OrgId: proto.String("org-9")}))
			if connect.CodeOf(err) != tt.wantCode {
				t.Fatalf("code = %v, want %v", connect.CodeOf(err), tt.wantCode)
			}

			if got := logs.got(); len(got) != 1 || got[0] != tt.want {
				t.Errorf("access log tenants = %+v, want one with %+v", got, tt.want)
			}
			attrs := onlySpanAttrs(t, spans)
			if attrs["org.id"] != tt.want.OrgID {
				t.Errorf("span org.id = %q, want %q", attrs["org.id"], tt.want.OrgID)
			}
			if got, ok := attrs["project.id"]; ok {
				t.Errorf("span project.id = %q, want none", got)
			}
		})
	}
}
