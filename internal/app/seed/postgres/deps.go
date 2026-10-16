package seed

import (
	"github.com/jackc/pgx/v5/pgxpool"
)

type deps struct {
	pg *pgxpool.Pool
	// adminPassword is PUG_DEMO_ADMIN_PASSWORD. Empty means ensureAdmin does
	// nothing; an existing admin stays.
	adminPassword string
}
