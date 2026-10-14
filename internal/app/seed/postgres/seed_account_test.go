package seed_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rs/xid"
	"golang.org/x/crypto/bcrypt"

	pgseed "github.com/pug-sh/pug/internal/app/seed/postgres"
	coreauth "github.com/pug-sh/pug/internal/core/auth"
	coreorgs "github.com/pug-sh/pug/internal/core/orgs"
	"github.com/pug-sh/pug/internal/core/projects"
	"github.com/pug-sh/pug/internal/gen/repo/dbread"
	"github.com/pug-sh/pug/internal/gen/repo/dbwrite"
	"github.com/pug-sh/pug/internal/testutil"
)

const adminEmail = "admin@pug.example"

// assertDemoOrgAdmin fails the test unless customerID is an admin of orgID and a
// member of no other org.
func assertDemoOrgAdmin(t *testing.T, ctx context.Context, read *dbread.Queries, customerID, orgID string) {
	t.Helper()
	orgs, err := read.GetOrgsWithRoleByCustomerID(ctx, customerID)
	if err != nil {
		t.Fatalf("GetOrgsWithRoleByCustomerID: %v", err)
	}
	if len(orgs) != 1 || orgs[0].ID != orgID || orgs[0].Role != coreorgs.RoleAdmin.String() {
		t.Fatalf("admin orgs = %+v, want admin of the demo org %s", orgs, orgID)
	}
}

// TestSeedAccountDemoAdmin covers the demo accounts' passwords. The demo user is
// seeded without one, and a hash an old seed left behind is cleared. An admin
// exists only once PUG_DEMO_ADMIN_PASSWORD is set: created then, in the existing
// demo org, put back if removed, and its password never overwritten.
func TestSeedAccountDemoAdmin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db := testutil.SetupPostgres(t)
	ctx := context.Background()
	read := dbread.New(db.PgW)
	write := dbwrite.New(db.PgW)

	t.Setenv("PUG_DEMO_ADMIN_PASSWORD", "")
	project, err := pgseed.SeedAccount(ctx, db.PgW)
	if err != nil {
		t.Fatalf("SeedAccount: %v", err)
	}
	user, err := read.GetCustomerByEmail(ctx, coreauth.DemoUserEmail)
	if err != nil {
		t.Fatalf("GetCustomerByEmail(demo user): %v", err)
	}
	if user.PasswordHash != "" {
		t.Error("the demo user was seeded with a password")
	}
	if _, err := read.GetCustomerByEmail(ctx, adminEmail); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("admin lookup with no password set: err = %v, want no rows", err)
	}

	// A database seeded while the demo user had a published password.
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-published-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}
	if _, err := write.UpdateCustomerPasswordHash(ctx, dbwrite.UpdateCustomerPasswordHashParams{
		ID: user.ID, PasswordHash: string(oldHash),
	}); err != nil {
		t.Fatalf("UpdateCustomerPasswordHash: %v", err)
	}
	if _, err := pgseed.SeedAccount(ctx, db.PgW); err != nil {
		t.Fatalf("SeedAccount over an old seed: %v", err)
	}
	if user, err = read.GetCustomerByEmail(ctx, coreauth.DemoUserEmail); err != nil {
		t.Fatalf("GetCustomerByEmail(demo user): %v", err)
	}
	if user.PasswordHash != "" {
		t.Error("re-seeding left the demo user's old password in place")
	}

	const first = "6chars" // exactly 6 characters, SetPassword's minimum: accepted
	t.Setenv("PUG_DEMO_ADMIN_PASSWORD", first)
	if _, err := pgseed.SeedAccount(ctx, db.PgW); err != nil {
		t.Fatalf("SeedAccount with an admin password: %v", err)
	}
	admin, err := read.GetCustomerByEmail(ctx, adminEmail)
	if err != nil {
		t.Fatalf("the admin was not created for the existing demo org: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(first)); err != nil {
		t.Errorf("admin password does not match PUG_DEMO_ADMIN_PASSWORD: %v", err)
	}
	assertDemoOrgAdmin(t, ctx, read, admin.ID, project.OrgID)

	// Removed from the org, then re-seeded with a different password: the admin is
	// put back, and its password is left alone. The new one is exactly 72 bytes,
	// bcrypt's limit, so it is accepted even though it is never applied.
	if _, err := write.DeleteOrgMember(ctx, dbwrite.DeleteOrgMemberParams{OrgID: project.OrgID, CustomerID: admin.ID}); err != nil {
		t.Fatalf("DeleteOrgMember: %v", err)
	}
	t.Setenv("PUG_DEMO_ADMIN_PASSWORD", strings.Repeat("b", 72))
	if _, err := pgseed.SeedAccount(ctx, db.PgW); err != nil {
		t.Fatalf("SeedAccount with a changed admin password: %v", err)
	}
	again, err := read.GetCustomerByEmail(ctx, adminEmail)
	if err != nil {
		t.Fatalf("GetCustomerByEmail(admin): %v", err)
	}
	if again.PasswordHash != admin.PasswordHash {
		t.Error("re-seeding overwrote the admin's password")
	}
	assertDemoOrgAdmin(t, ctx, read, admin.ID, project.OrgID)

	// Demoted rather than removed: the role is restored.
	if _, err := write.UpdateOrgMemberRole(ctx, dbwrite.UpdateOrgMemberRoleParams{
		Role: coreorgs.RoleViewer.String(), OrgID: project.OrgID, CustomerID: admin.ID,
	}); err != nil {
		t.Fatalf("UpdateOrgMemberRole: %v", err)
	}
	if _, err := pgseed.SeedAccount(ctx, db.PgW); err != nil {
		t.Fatalf("SeedAccount after a demotion: %v", err)
	}
	assertDemoOrgAdmin(t, ctx, read, admin.ID, project.OrgID)
}

// TestSeedAccountFindsTheSharedDemoOrg pins how the seeder finds the demo org on
// an existing database: the demo user's org that also has the demo viewer. An
// org created as the demo user while its password was public must never be
// taken for it.
func TestSeedAccountFindsTheSharedDemoOrg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db := testutil.SetupPostgres(t)
	ctx := context.Background()
	read := dbread.New(db.PgW)
	write := dbwrite.New(db.PgW)
	t.Setenv("PUG_DEMO_ADMIN_PASSWORD", "")

	project, err := pgseed.SeedAccount(ctx, db.PgW)
	if err != nil {
		t.Fatalf("SeedAccount: %v", err)
	}
	user, err := read.GetCustomerByEmail(ctx, coreauth.DemoUserEmail)
	if err != nil {
		t.Fatalf("GetCustomerByEmail(demo user): %v", err)
	}

	// An org created as the demo user, with a project of its own.
	planted, err := write.CreateOrg(ctx, dbwrite.CreateOrgParams{ID: xid.New().String(), DisplayName: "planted"})
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if _, err := write.CreateOrgMember(ctx, dbwrite.CreateOrgMemberParams{
		OrgID: planted.ID, CustomerID: user.ID, Role: coreorgs.RoleAdmin.String(),
	}); err != nil {
		t.Fatalf("CreateOrgMember: %v", err)
	}
	if _, err := projects.CreateProjectInTx(ctx, write, planted.ID, "planted", ""); err != nil {
		t.Fatalf("CreateProjectInTx: %v", err)
	}
	again, err := pgseed.SeedAccount(ctx, db.PgW)
	if err != nil {
		t.Fatalf("SeedAccount with a planted org: %v", err)
	}
	if again.ID != project.ID {
		t.Fatalf("resolved project %s, want the demo project %s", again.ID, project.ID)
	}

	// Removed from the demo org, the demo user's only org is the planted one, which
	// has no demo viewer: the seeder refuses rather than seeding into it.
	if _, err := write.DeleteOrgMember(ctx, dbwrite.DeleteOrgMemberParams{OrgID: project.OrgID, CustomerID: user.ID}); err != nil {
		t.Fatalf("DeleteOrgMember: %v", err)
	}
	if _, err := pgseed.SeedAccount(ctx, db.PgW); err == nil || !strings.Contains(err.Error(), "no org has both") {
		t.Fatalf("SeedAccount without the demo user in the demo org: err = %v, want a no-shared-org error", err)
	}
}

// TestSeedAccountDemoAdminOnFirstSeed covers a new deployment that sets the
// variable from the start: the admin exists after the very first seed.
func TestSeedAccountDemoAdminOnFirstSeed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db := testutil.SetupPostgres(t)
	ctx := context.Background()
	read := dbread.New(db.PgW)

	t.Setenv("PUG_DEMO_ADMIN_PASSWORD", "first-boot-password")
	project, err := pgseed.SeedAccount(ctx, db.PgW)
	if err != nil {
		t.Fatalf("SeedAccount: %v", err)
	}
	admin, err := read.GetCustomerByEmail(ctx, adminEmail)
	if err != nil {
		t.Fatalf("the first seed created no admin: %v", err)
	}
	assertDemoOrgAdmin(t, ctx, read, admin.ID, project.OrgID)
}

// TestSeedAccountRejectsBadAdminPassword pins SetPassword's bounds on
// PUG_DEMO_ADMIN_PASSWORD; the accepted edges are covered in
// TestSeedAccountDemoAdmin. The check runs before the database is touched.
func TestSeedAccountRejectsBadAdminPassword(t *testing.T) {
	for name, password := range map[string]string{
		"under 6 characters":      "five5",
		"a trailing newline":      "secret-password\n",
		"a leading space":         " secret-password",
		"3 characters in 6 bytes": "\u00e9\u00e9\u00e9",
		"over 72 bytes":           strings.Repeat("a", 73),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PUG_DEMO_ADMIN_PASSWORD", password)
			_, err := pgseed.SeedAccount(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "PUG_DEMO_ADMIN_PASSWORD") {
				t.Fatalf("err = %v, want a PUG_DEMO_ADMIN_PASSWORD error", err)
			}
		})
	}
}

// TestSeedAccountApiKeys covers what the demo deployment and `pug seed` depend
// on: the seeded project comes with a working public key, and with the private
// key the seeder creates explicitly for local SDK/MCP calls. Re-running resolves
// the same project rather than seeding a second one.
func TestSeedAccountApiKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db := testutil.SetupPostgres(t)
	ctx := context.Background()

	project, err := pgseed.SeedAccount(ctx, db.PgW)
	if err != nil {
		t.Fatalf("SeedAccount: %v", err)
	}

	svc := projects.NewService(db.PgRO, db.PgW, nil)
	keys, err := svc.ListApiKeys(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListApiKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2 (the starter public key and the seeded private one)", len(keys))
	}

	byKind := map[string]dbread.ApiKey{}
	for _, k := range keys {
		byKind[k.Kind] = k
	}

	pub, ok := byKind[string(projects.KindPublic)]
	if !ok {
		t.Fatal("the seeded project has no public key")
	}
	// A public key is stored whole, so the token IS the key the SDK snippet pastes.
	if !strings.HasPrefix(pub.Token, "pub_") {
		t.Errorf("public key token = %q, want a pub_ key", pub.Token)
	}

	// The private key is only reachable as a digest here; that it exists at all is
	// what `pug worker demo` and local SDK calls need.
	if _, ok := byKind[string(projects.KindPrivate)]; !ok {
		t.Error("the seeded project has no private key, so local dev has nothing to call the SDK/MCP endpoints with")
	}

	t.Run("re-seeding resolves the same project", func(t *testing.T) {
		again, err := pgseed.SeedAccount(ctx, db.PgW)
		if err != nil {
			t.Fatalf("SeedAccount (second run): %v", err)
		}
		if again.ID != project.ID {
			t.Errorf("second run returned project %q, want the first run's %q", again.ID, project.ID)
		}

		keys, err := svc.ListApiKeys(ctx, project.ID)
		if err != nil {
			t.Fatalf("ListApiKeys: %v", err)
		}
		if len(keys) != 2 {
			t.Errorf("got %d keys after re-seeding, want the original 2", len(keys))
		}
	})
}
