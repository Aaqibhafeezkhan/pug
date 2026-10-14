package projects

import (
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	projectsv1 "github.com/pug-sh/pug/internal/gen/proto/dashboard/projects/v1"
)

// These pin the partial-update validation contract on UpdateMetaRequest. In edition
// 2023, a field with explicit presence skips its rules when unset, so an omitted
// display_name is valid ("leave unchanged"); a present empty string is rejected by
// min_len. If a future protovalidate enforced rules on unset present-able fields,
// the "omitted is valid" cases fail loudly and we'd switch to a message-level CEL.

func TestUpdateMetaRequest_OmittedDisplayNameIsValid(t *testing.T) {
	req := &projectsv1.UpdateMetaRequest{ReportingTimezone: proto.String("Asia/Kolkata")}
	if err := protovalidate.Validate(req); err != nil {
		t.Fatalf("omitted display_name should be valid (partial update): %v", err)
	}
}

func TestUpdateMetaRequest_EmptyDisplayNameRejected(t *testing.T) {
	req := &projectsv1.UpdateMetaRequest{DisplayName: proto.String("")}
	if err := protovalidate.Validate(req); err == nil {
		t.Fatal("present empty display_name should fail min_len")
	}
}

func TestUpdateMetaRequest_OmittedTimezoneIsValid(t *testing.T) {
	req := &projectsv1.UpdateMetaRequest{DisplayName: proto.String("ok")}
	if err := protovalidate.Validate(req); err != nil {
		t.Fatalf("omitted timezone should be valid: %v", err)
	}
}

func TestUpdateMetaRequest_FullyEmptyIsValid(t *testing.T) {
	if err := protovalidate.Validate(&projectsv1.UpdateMetaRequest{}); err != nil {
		t.Fatalf("empty request should be valid (no-op partial update): %v", err)
	}
}

// ----- API key requests -----

// kind is the one field a client cannot get wrong quietly: an unset or unknown
// value would otherwise reach the handler, which fails it closed as an internal
// error. protovalidate turns both into a clean InvalidArgument at the boundary.
func TestCreateApiKeyRequest_KindRequired(t *testing.T) {
	tests := []struct {
		name string
		req  *projectsv1.CreateApiKeyRequest
		ok   bool
	}{
		{"public is valid", &projectsv1.CreateApiKeyRequest{Kind: projectsv1.ApiKeyKind_API_KEY_KIND_PUBLIC.Enum()}, true},
		{"private is valid", &projectsv1.CreateApiKeyRequest{Kind: projectsv1.ApiKeyKind_API_KEY_KIND_PRIVATE.Enum()}, true},
		{"omitted kind rejected", &projectsv1.CreateApiKeyRequest{}, false},
		{"unspecified kind rejected", &projectsv1.CreateApiKeyRequest{Kind: projectsv1.ApiKeyKind_API_KEY_KIND_UNSPECIFIED.Enum()}, false},
		{"undefined kind rejected", &projectsv1.CreateApiKeyRequest{Kind: projectsv1.ApiKeyKind(99).Enum()}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := protovalidate.Validate(tt.req)
			if tt.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("expected a validation error, got nil")
			}
		})
	}
}

func TestCreateApiKeyRequest_DisplayNameBounds(t *testing.T) {
	req := &projectsv1.CreateApiKeyRequest{
		Kind:        projectsv1.ApiKeyKind_API_KEY_KIND_PRIVATE.Enum(),
		DisplayName: proto.String(strings.Repeat("a", 151)),
	}
	if err := protovalidate.Validate(req); err == nil {
		t.Fatal("display_name over 150 chars should fail max_len")
	}

	// A label is optional: an unnamed key is fine.
	req.DisplayName = nil
	if err := protovalidate.Validate(req); err != nil {
		t.Fatalf("omitted display_name should be valid: %v", err)
	}
}

func TestDeleteApiKeyRequest_IDRequired(t *testing.T) {
	if err := protovalidate.Validate(&projectsv1.DeleteApiKeyRequest{}); err == nil {
		t.Fatal("omitted id should fail required")
	}
	if err := protovalidate.Validate(&projectsv1.DeleteApiKeyRequest{Id: proto.String("key1")}); err != nil {
		t.Fatalf("present id should be valid: %v", err)
	}
}

func TestUpdateMetaRequest_DefaultTheme(t *testing.T) {
	tests := []struct {
		name  string
		value string
		ok    bool
	}{
		{"empty clears", "", true},
		{"built-in", "pug-colourblind", true},
		{"built-in pinned", "pug#dark", true},
		{"theme file", "https://themes.pug.sh/linen/theme.json", true},
		{"theme file pinned", "https://themes.pug.sh/linen/theme.json#light", true},
		{"raw GitHub with a query", "https://raw.githubusercontent.com/acme/brand/main/pug.json?v=2", true},
		{"a port", "https://cdn.example.com:8443/t.json", true},
		{"a host that only starts with localhost", "https://localhost.example.com/t.json", true},
		{"installed id", "installed-3f9a01bc", false},
		{"http", "http://themes.pug.sh/linen/theme.json", false},
		{"unknown pin", "https://themes.pug.sh/linen/theme.json#dim", false},
		{"pin on nothing", "#dark", false},
		{"uppercase id", "Pug", false},
		{"uppercase host", "https://Themes.pug.sh/linen/theme.json", false},
		{"userinfo", "https://user:pass@themes.pug.sh/t.json", false},
		{"localhost", "https://localhost/t.json", false},
		{"localhost with a port", "https://localhost:8080/t.json", false},
		{"a .localhost name", "https://app.localhost/t.json", false},
		{"localhost with a trailing dot", "https://localhost./t.json", false},
		{"localhost with no path", "https://localhost", false},
		{"localhost pinned with no path", "https://localhost#dark", false},
		{"IPv4", "https://10.0.0.1/t.json", false},
		{"IPv4 with a trailing dot", "https://10.0.0.1./t.json", false},
		{"IPv4 with a port", "https://10.0.0.1:8080/t.json", false},
		{"IPv4 with no path", "https://127.0.0.1", false},
		{"IPv4 pinned with no path", "https://127.0.0.1#dark", false},
		{"short IPv4", "https://127.1/t.json", false},
		{"decimal IPv4", "https://2130706433/t.json", false},
		{"hex IPv4", "https://0x7f000001/t.json", false},
		{"hex IPv4 with no digits", "https://0x/t.json", false},
		{"IPv6", "https://[::1]/t.json", false},
		{"percent-encoded host", "https://%6c%6fcalhost/t.json", false},
		{"a space in the path", "https://themes.pug.sh/a b.json", false},
		{"too long", "https://themes.pug.sh/" + strings.Repeat("a", 2048), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := protovalidate.Validate(&projectsv1.UpdateMetaRequest{DefaultTheme: proto.String(tt.value)})
			if tt.ok && err != nil {
				t.Fatalf("%q should be valid: %v", tt.value, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("%q should be refused", tt.value)
			}
		})
	}
}
