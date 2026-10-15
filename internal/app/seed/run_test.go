package seed_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pug-sh/pug/internal/app/seed"
)

// TestRunChecksTheAdminPasswordFirst pins that `pug seed` refuses a bad
// PUG_DEMO_ADMIN_PASSWORD before its reset drops anything. Both stores point at a
// port nothing listens on, so a check that ran after the reset would fail on a
// connection error instead, and could never touch a real database.
func TestRunChecksTheAdminPasswordFirst(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://pug:pug@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	t.Setenv("CLICKHOUSE_URL", "clickhouse://127.0.0.1:1/none?dial_timeout=1s")
	t.Setenv("PUG_DEMO_ADMIN_PASSWORD", "too-short")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := seed.Run(ctx, seed.Options{})
	if err == nil || !strings.Contains(err.Error(), "PUG_DEMO_ADMIN_PASSWORD") {
		t.Fatalf("Run with a bad admin password: err = %v, want a PUG_DEMO_ADMIN_PASSWORD error", err)
	}
}
