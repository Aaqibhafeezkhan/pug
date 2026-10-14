package projects

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	coreprojects "github.com/pug-sh/pug/internal/core/projects"
	"github.com/pug-sh/pug/internal/deps/postgres"
	projectsv1 "github.com/pug-sh/pug/internal/gen/proto/dashboard/projects/v1"
	"github.com/pug-sh/pug/internal/gen/repo/dbread"
	"github.com/pug-sh/pug/internal/gen/repo/dbwrite"
)

// roToRPCMsg is compared as a whole message, so the test fails if the mapper
// starts setting another field. The input row holds an FCM service-account
// JSON with a private key, and no output field may carry it.
func TestROToRPCMsg(t *testing.T) {
	in := dbread.Project{
		ID:                "proj_001",
		OrgID:             "org_abc",
		DisplayName:       "My Project",
		ReportingTimezone: "Asia/Kolkata",
		DefaultTheme:      "pug#dark",
		FcmServiceJson:    postgres.NewText(`{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----"}`),
	}
	want := &projectsv1.Project{
		DisplayName:       proto.String("My Project"),
		Id:                proto.String("proj_001"),
		OrgId:             proto.String("org_abc"),
		ReportingTimezone: proto.String("Asia/Kolkata"),
		DefaultTheme:      proto.String("pug#dark"),
	}

	if diff := cmp.Diff(want, roToRPCMsg(in), protocmp.Transform()); diff != "" {
		t.Errorf("roToRPCMsg mismatch (-want +got):\n%s", diff)
	}
}

// wToRPCMsg leans on dbread.Project and dbwrite.Project being the same row
// generated twice. Pin that it maps identically.
func TestWToRPCMsgMatchesRead(t *testing.T) {
	w := dbwrite.Project{
		ID:                "proj_w01",
		OrgID:             "org_w_abc",
		DisplayName:       "Write Project",
		ReportingTimezone: "UTC",
		DefaultTheme:      "https://themes.pug.sh/linen/theme.json",
		FcmServiceJson:    postgres.NewText(`{"project_id":"123"}`),
	}

	if diff := cmp.Diff(roToRPCMsg(dbread.Project(w)), wToRPCMsg(w), protocmp.Transform()); diff != "" {
		t.Errorf("wToRPCMsg differs from roToRPCMsg (-read +write):\n%s", diff)
	}
}

func TestApiKeyToRPCMsg(t *testing.T) {
	createTime := time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name    string
		input   dbread.ApiKey
		wantKey string
		wantKnd projectsv1.ApiKeyKind
	}{
		{
			name: "public key returns its value",
			input: dbread.ApiKey{
				ID:          "key_pub",
				ProjectID:   "proj_1",
				Kind:        string(coreprojects.KindPublic),
				Token:       "pub_abc123",
				Masked:      "pub_...c123",
				DisplayName: "web",
				CreateTime:  pgtype.Timestamptz{Time: createTime, Valid: true},
			},
			wantKey: "pub_abc123",
			wantKnd: projectsv1.ApiKeyKind_API_KEY_KIND_PUBLIC,
		},
		{
			// The token of a private key is its digest. Returning it would leak the
			// stored secret, and it is useless to the client either way.
			name: "private key never returns its token",
			input: dbread.ApiKey{
				ID:          "key_prv",
				ProjectID:   "proj_1",
				Kind:        string(coreprojects.KindPrivate),
				Token:       "0f5d4e3c2b1a09876543210fedcba98765432100fedcba9876543210abcdef01",
				Masked:      "prv_...ef01",
				DisplayName: "CI",
				CreateTime:  pgtype.Timestamptz{Time: createTime, Valid: true},
			},
			wantKey: "",
			wantKnd: projectsv1.ApiKeyKind_API_KEY_KIND_PRIVATE,
		},
		{
			// Fails closed: an unreadable kind must not pass for a public key, which
			// would echo the token back.
			name: "unknown kind is unspecified and returns no value",
			input: dbread.ApiKey{
				ID:         "key_bad",
				ProjectID:  "proj_1",
				Kind:       "wat",
				Token:      "some_token",
				Masked:     "some...oken",
				CreateTime: pgtype.Timestamptz{Time: createTime, Valid: true},
			},
			wantKey: "",
			wantKnd: projectsv1.ApiKeyKind_API_KEY_KIND_UNSPECIFIED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := apiKeyToRPCMsg(tt.input)

			if msg.GetId() != tt.input.ID {
				t.Errorf("Id = %q, want %q", msg.GetId(), tt.input.ID)
			}
			if msg.GetDisplayName() != tt.input.DisplayName {
				t.Errorf("DisplayName = %q, want %q", msg.GetDisplayName(), tt.input.DisplayName)
			}
			if msg.GetMasked() != tt.input.Masked {
				t.Errorf("Masked = %q, want %q", msg.GetMasked(), tt.input.Masked)
			}
			if msg.GetKind() != tt.wantKnd {
				t.Errorf("Kind = %v, want %v", msg.GetKind(), tt.wantKnd)
			}
			if msg.GetKey() != tt.wantKey {
				t.Errorf("Key = %q, want %q", msg.GetKey(), tt.wantKey)
			}
			if got := msg.GetCreateTime().AsTime(); !got.Equal(createTime) {
				t.Errorf("CreateTime = %v, want %v", got, createTime)
			}
		})
	}
}

// createdApiKeyToRPCMsg leans on dbread.ApiKey and dbwrite.ApiKey being the same
// row generated twice. Pin that it maps identically.
func TestCreatedApiKeyToRPCMsgMatchesRead(t *testing.T) {
	w := dbwrite.ApiKey{
		ID:          "key_new",
		ProjectID:   "proj_1",
		Kind:        string(coreprojects.KindPublic),
		Token:       "pub_fresh0001",
		Masked:      "pub_...0001",
		DisplayName: "fresh",
		CreateTime:  pgtype.Timestamptz{Time: time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC), Valid: true},
	}

	if diff := cmp.Diff(apiKeyToRPCMsg(dbread.ApiKey(w)), createdApiKeyToRPCMsg(w), protocmp.Transform()); diff != "" {
		t.Errorf("createdApiKeyToRPCMsg differs from apiKeyToRPCMsg (-read +write):\n%s", diff)
	}
}

func TestKindFromRPCEnum(t *testing.T) {
	tests := []struct {
		name   string
		input  projectsv1.ApiKeyKind
		want   coreprojects.Kind
		wantOK bool
	}{
		{"public", projectsv1.ApiKeyKind_API_KEY_KIND_PUBLIC, coreprojects.KindPublic, true},
		{"private", projectsv1.ApiKeyKind_API_KEY_KIND_PRIVATE, coreprojects.KindPrivate, true},
		{"unspecified fails closed", projectsv1.ApiKeyKind_API_KEY_KIND_UNSPECIFIED, "", false},
		{"unknown fails closed", projectsv1.ApiKeyKind(99), "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := kindFromRPCEnum(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}
