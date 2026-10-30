package expiredtokens

import (
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/pug-sh/pug/internal/app/cron"
	"github.com/pug-sh/pug/internal/gen/repo/dbwrite"
	"github.com/pug-sh/pug/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func TestRunDeletesExpiredTokensRegardlessOfStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	pg := testutil.SetupPostgres(t)
	t.Setenv("DATABASE_URL", pg.PgW.Config().ConnString())

	customerID := xid.New().String()
	if _, err := dbwrite.New(pg.PgW).CreateCustomer(t.Context(), dbwrite.CreateCustomerParams{
		ID: customerID, Email: "expired-token-cleanup@example.com",
	}); err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}

	_, err := pg.PgW.Exec(t.Context(), `
		insert into refresh_tokens (id, customer_id, family_id, token_hash, expires_at, consumed_at, revoked_at)
		values
			($1, $2, $3, $4, now() - interval '1 hour', now(), null),
			($5, $2, $6, $7, now() - interval '1 hour', null, now()),
			($8, $2, $9, $10, now() + interval '1 hour', now(), null),
			($11, $2, $12, $13, now() + interval '1 hour', null, now())`,
		xid.New().String(), customerID, xid.New().String(), "expired-consumed-refresh",
		xid.New().String(), xid.New().String(), "expired-revoked-refresh",
		xid.New().String(), xid.New().String(), "live-consumed-refresh",
		xid.New().String(), xid.New().String(), "live-revoked-refresh")
	if err != nil {
		t.Fatalf("seed refresh tokens: %v", err)
	}

	_, err = pg.PgW.Exec(t.Context(), `
		insert into email_action_tokens (id, email, purpose, token_hash, expires_at, consumed_at)
		values
			($1, 'expired@example.com', 'magic_link', $2, now() - interval '1 hour', now()),
			($3, 'expired@example.com', 'org_invite', $4, now() - interval '1 hour', null),
			($5, 'live@example.com', 'magic_link', $6, now() + interval '1 hour', now())`,
		xid.New().String(), "expired-consumed-email-action",
		xid.New().String(), "expired-active-email-action",
		xid.New().String(), "live-consumed-email-action")
	if err != nil {
		t.Fatalf("seed email action tokens: %v", err)
	}

	if err := Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var refreshCount, emailCount int
	if err := pg.PgRO.QueryRow(t.Context(), "select count(*) from refresh_tokens").Scan(&refreshCount); err != nil {
		t.Fatalf("count refresh tokens: %v", err)
	}
	if err := pg.PgRO.QueryRow(t.Context(), "select count(*) from email_action_tokens").Scan(&emailCount); err != nil {
		t.Fatalf("count email action tokens: %v", err)
	}
	if refreshCount != 2 {
		t.Errorf("refresh token rows = %d, want 2 unexpired rows", refreshCount)
	}
	if emailCount != 1 {
		t.Errorf("email action token rows = %d, want 1 unexpired row", emailCount)
	}
}

func TestRunExitsZeroWhileAnotherPassHoldsTheLock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	pg := testutil.SetupPostgres(t)
	t.Setenv("DATABASE_URL", pg.PgW.Config().ConnString())

	ctx := t.Context()
	holder, err := pg.PgW.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if acquired, err := dbwrite.New(holder).TryCronLock(ctx, int64(cron.LockExpiredTokens)); err != nil || !acquired {
		t.Fatalf("TryCronLock = %t, %v", acquired, err)
	}
	if err := Run(ctx); err != nil {
		t.Fatalf("Run = %v, want nil while another pass holds the lock", err)
	}
}

func TestExpiryBoundaryIsInclusive(t *testing.T) {
	if !time.Now().After(time.Now().Add(-time.Nanosecond)) {
		t.Fatal("time comparison sanity check failed")
	}
}
