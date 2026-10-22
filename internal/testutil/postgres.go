package testutil

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/rs/xid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const testPostgresImage = "postgres@sha256:96d56f7f57c6aacd1fcb908bc83b345ec5f83231ee486dd66a1baadce274db88" // PostgreSQL 18.4-alpine

const (
	// pgTemplateDB holds the migrated schema every test database is cloned from.
	// Migrations run against it once per server, after which nothing may connect:
	// CREATE DATABASE ... TEMPLATE is refused while any session is attached to the
	// source. sealTemplate enforces that.
	pgTemplateDB = "pug_template"

	// pgTemplateLock is the advisory lock key that serializes the packages
	// sharing a server while one of them builds the template.
	pgTemplateLock = 0x7075675f746d706c

	// pgAdminDB is the database the CREATE/DROP DATABASE statements are issued
	// from. It is neither the template nor a test database, so those statements
	// never run from a connection to their own target.
	pgAdminDB = "postgres"

	// pgPoolMaxConns caps each pool's backends. pgxpool otherwise defaults
	// MaxConns to max(4, NumCPU), so on a many-core machine the packages sharing
	// the server could exhaust its max_connections.
	//
	// It is a ceiling, not a reservation: MinConns is 0, so a pool holds only what
	// it uses. 8 is the suite's widest fan-out — internal/core/auth's refresh and
	// magic-link races each run 8 goroutines through one pool — so all of them
	// reach the database together instead of queueing in Acquire for a connection,
	// which is the overlap those tests exist to create. Raise it alongside any
	// test that fans out wider.
	//
	// The ceiling that matters is one test's two pools plus the admin pool, 24
	// backends per package, since a binary's tests run sequentially
	// (TestSetupCallersDoNotUseParallel).
	pgPoolMaxConns = 8

	// pgMaxConnections is the shared server's max_connections: about 40 packages
	// at that ceiling, while go test runs up to GOMAXPROCS packages at once.
	pgMaxConnections = 1000
)

// TestPostgres holds the connection pools for one test's private database.
type TestPostgres struct {
	PgRO *pgxpool.Pool
	PgW  *pgxpool.Pool
}

// SetOrgCreateTime backdates an org: the trial clock and the quota anchor both
// derive from create_time, so fixed period assertions would otherwise pass or
// fail by the calendar. Raw SQL because no write query updates it.
func SetOrgCreateTime(t *testing.T, pool *pgxpool.Pool, orgID string, at time.Time) {
	t.Helper()
	tag, err := pool.Exec(t.Context(),
		"update orgs set create_time = $1 where id = $2", at, orgID)
	if err != nil {
		t.Fatalf("backdate org %s: %v", orgID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("backdate org %s: matched %d rows, want 1", orgID, tag.RowsAffected())
	}
}

// WaitForAdvisoryLockWaiter blocks until a session is parked on an advisory lock,
// so a lock test never races the commit that releases it. Only the pool's own
// database counts: other packages share the server, and a package runs its tests
// serially, so a waiter there is the one under test.
// It also returns once done closes: a call that settled without waiting is what a
// lock test exists to catch, and its assertions name that better than a timeout.
func WaitForAdvisoryLockWaiter(t *testing.T, pool *pgxpool.Pool, done <-chan struct{}) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		var n int
		if err := pool.QueryRow(t.Context(), `
			select count(*) from pg_locks l join pg_stat_activity a using (pid)
			where l.locktype = 'advisory' and not l.granted and a.datname = current_database()`).Scan(&n); err != nil {
			t.Fatalf("testutil: read pg_locks: %v", err)
		}
		if n > 0 {
			return
		}
		select {
		case <-done:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("testutil: nothing blocked on an advisory lock within 30s")
}

// sharedPostgres is this package's handle on the run's Postgres container.
type sharedPostgres struct {
	admin *pgxpool.Pool
	base  *url.URL
}

// plainDSN builds a connection string for db carrying no pgxpool-only
// parameters, for a plain pgx connection such as migrate's sql.Open.
//
// The two DSN builders are separate functions rather than one with a flag
// because each is wrong in a different way at the other's call site, and only
// one of the two mistakes is visible. See poolDSN.
func plainDSN(base *url.URL, db string) string {
	u := *base
	u.Path = "/" + db
	return u.String()
}

// poolDSN builds a connection string for db carrying the pgPoolMaxConns cap, for
// pgxpool.New.
//
// Handing it to a plain pgx connection announces itself: pgx forwards a key it
// does not recognize to the server as a runtime parameter, so pool_max_conns
// fails the connection outright (SQLSTATE 42704) rather than being ignored. The
// reverse — a plainDSN reaching pgxpool.New — is the quiet one: the pool builds
// fine and falls back to its max(4, NumCPU) default, which is the cap's entire
// reason for existing, and nothing says so.
func poolDSN(base *url.URL, db string) string {
	u := *base
	u.Path = "/" + db
	q := u.Query()
	q.Set("pool_max_conns", strconv.Itoa(pgPoolMaxConns))
	u.RawQuery = q.Encode()
	return u.String()
}

var pgContainer = &lazyContainer[sharedPostgres]{kind: "postgres", start: startPostgres}

// SetupPostgres returns read and write pools onto a freshly migrated database
// private to this test. Cleanup is registered via t.Cleanup.
//
// The container is shared by every package of the go test run (runScopedName);
// each test's isolation comes from its own database cloned off a pre-migrated
// template, which costs milliseconds against the seconds a container start plus
// a migration run costs. Packages must wire up Main.
func SetupPostgres(t *testing.T) *TestPostgres {
	t.Helper()
	// Scoped to the test: t.Context is cancelled just before this test's cleanups
	// run, so a test that times out stops waiting on its own database too. The
	// shared container and the cleanup below deliberately do not use it — see
	// startPostgres and the t.Cleanup body.
	ctx := t.Context()

	sp := pgContainer.get(t)

	name := "test_" + xid.New().String()
	quoted := pgx.Identifier{name}.Sanitize()

	if _, err := sp.admin.Exec(ctx, fmt.Sprintf("create database %s template %s",
		quoted, pgx.Identifier{pgTemplateDB}.Sanitize())); err != nil {
		t.Fatalf("testutil: create test database: %v", err)
	}

	// Registered before the pools are built so a failure between here and the
	// return still drops the database; otherwise it would linger in the shared
	// container for the rest of the run. Each pool is closed only once set.
	var pgRO, pgW *pgxpool.Pool
	t.Cleanup(func() {
		if pgRO != nil {
			pgRO.Close()
		}
		if pgW != nil {
			pgW.Close()
		}
		// Background, not t.Context: the test's context is already cancelled by
		// the time cleanups run, so the drop would never be sent and the database
		// would linger in the shared container for the rest of the run. The
		// timeout backstops a wedged connection hanging the drop — and with it the
		// rest of the sequential package — until go test -timeout (cleanupDropTimeout).
		//
		// force: pgxpool releases connections asynchronously, and a backend that
		// outlives the pool holds the database open against a plain drop.
		dropCtx, cancel := context.WithTimeout(context.Background(), cleanupDropTimeout)
		defer cancel()
		if _, err := sp.admin.Exec(dropCtx,
			fmt.Sprintf("drop database if exists %s with (force)", quoted)); err != nil {
			t.Errorf("testutil: drop test database %s: %v", name, err)
		}
	})

	dsn := poolDSN(sp.base, name)

	var err error
	pgRO, err = pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("testutil: create read pool: %v", err)
	}

	pgW, err = pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("testutil: create write pool: %v", err)
	}

	return &TestPostgres{PgRO: pgRO, PgW: pgW}
}

func startPostgres() (_ *sharedPostgres, err error) {
	// Background, not the triggering test's t.Context: this container is shared by
	// the whole run and outlives whichever test happened to start it, so binding
	// it to that test's context would tear it down under the next one.
	ctx := context.Background()

	shared := runScopedName("postgres")
	opts := []testcontainers.ContainerCustomizer{
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.WithSQLDriver("pgx"),
		tcpostgres.BasicWaitStrategies(),
		testcontainers.WithCmdArgs("-c", fmt.Sprintf("max_connections=%d", pgMaxConnections)),
	}
	if shared != "" {
		opts = append(opts, testcontainers.WithReuseByName(shared))
	}
	ctr, err := tcpostgres.Run(ctx, testPostgresImage, opts...)
	// Run hands back a created-but-never-ready container alongside its error when
	// the readiness probe times out, so every failure path below has to release
	// it. release tolerates a nil container.
	defer func() {
		if err != nil {
			err = errors.Join(err, release(ctr))
		}
	}()
	if err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	// ConnectionString, not MustConnectionString: the Must variant panics, and a
	// panic here would carry err past the defer above still nil, so the container
	// it just decided it could not describe would never be terminated.
	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("connection string: %w", err)
	}

	base, err := url.Parse(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse connection string: %w", err)
	}
	if err = ensureTemplate(ctx, base); err != nil {
		return nil, err
	}

	admin, err := pgxpool.New(ctx, poolDSN(base, pgAdminDB))
	if err != nil {
		return nil, fmt.Errorf("admin pool: %w", err)
	}

	teardowns.add(func() error {
		admin.Close()
		if err := release(ctr); err != nil {
			return fmt.Errorf("terminate postgres container: %w", err)
		}
		return nil
	})

	return &sharedPostgres{admin: admin, base: base}, nil
}

// ensureTemplate migrates the template unless it already carries the digest of
// the migration files; the advisory lock holds the other packages until it is done.
func ensureTemplate(ctx context.Context, base *url.URL) error {
	mark, err := migrationsDigest()
	if err != nil {
		return fmt.Errorf("fingerprint migrations: %w", err)
	}

	conn, err := pgx.Connect(ctx, plainDSN(base, pgAdminDB))
	if err != nil {
		return fmt.Errorf("template lock connection: %w", err)
	}
	// Closing the session also releases the lock.
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", pgTemplateLock); err != nil {
		return fmt.Errorf("lock template: %w", err)
	}

	var ready bool
	if err := conn.QueryRow(ctx,
		"select exists (select 1 from pg_database where datname = $1 and shobj_description(oid, 'pg_database') = $2)",
		pgTemplateDB, mark).Scan(&ready); err != nil {
		return fmt.Errorf("read template state: %w", err)
	}
	if ready {
		return nil
	}

	// A template without the mark lost its builder partway, or was built from
	// other migrations by an earlier run that shared this server.
	quoted := pgx.Identifier{pgTemplateDB}.Sanitize()
	if _, err := conn.Exec(ctx, "drop database if exists "+quoted+" with (force)"); err != nil {
		return fmt.Errorf("drop stale template: %w", err)
	}
	if _, err := conn.Exec(ctx, "create database "+quoted); err != nil {
		return fmt.Errorf("create template: %w", err)
	}
	// The only connection the template ever takes; see sealTemplate.
	if err := migrate(ctx, plainDSN(base, pgTemplateDB)); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	if err := sealTemplate(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("comment on database %s is '%s'", quoted, mark)); err != nil {
		return fmt.Errorf("mark template ready: %w", err)
	}
	return nil
}

// migrationsDigest fingerprints the migration files' names and contents.
func migrationsDigest() (string, error) {
	dir, err := postgresMigrationsDir()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var all []byte
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", err
		}
		all = append(append(all, e.Name()...), b...)
	}
	return fmt.Sprintf("%x", sha256.Sum256(all)), nil
}

// sealTemplate locks the template down once it is migrated. CREATE DATABASE ...
// TEMPLATE fails outright while any session is attached to the source (SQLSTATE
// 55006), and migrate is the only thing that ever connects to it.
//
// database/sql closes its connections inline, so in practice the template is
// already idle by the time this runs. Barring new connections turns that into a
// guarantee, and turns a future stray connection to the template into a loud
// error at the point of the mistake rather than a clone that fails
// intermittently. Evicting whatever is still attached covers the remainder: the
// two-argument pg_terminate_backend waits for the backend to exit, where the
// one-argument form returns once the signal is merely sent and would leave the
// first clone racing a dying session.
func sealTemplate(ctx context.Context, admin *pgx.Conn) error {
	quoted := pgx.Identifier{pgTemplateDB}.Sanitize()

	if _, err := admin.Exec(ctx, fmt.Sprintf("alter database %s with allow_connections false", quoted)); err != nil {
		return fmt.Errorf("bar connections to template: %w", err)
	}
	if _, err := admin.Exec(ctx,
		"select pg_terminate_backend(pid, 5000) from pg_stat_activity where datname = $1 and pid <> pg_backend_pid()",
		pgTemplateDB); err != nil {
		return fmt.Errorf("evict template sessions: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, connStr string) error {
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	dir, err := postgresMigrationsDir()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS(dir))
	if err != nil {
		return err
	}

	_, err = provider.Up(ctx)
	return err
}

func postgresMigrationsDir() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("unable to determine source file path")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "schema", "postgres", "migrations"), nil
}

// PostgresMigrations steps one test database through the migrations SetupPostgres
// applied, for a test of a migration's data rewrite: the database arrives fully
// migrated, so step it down, seed rows in the older shape, then apply the next one.
func PostgresMigrations(t *testing.T, pg *TestPostgres) *goose.Provider {
	t.Helper()
	dir, err := postgresMigrationsDir()
	if err != nil {
		t.Fatalf("testutil: %v", err)
	}
	// Over the test's own pool: closing this handle leaves the pool open.
	db := stdlib.OpenDBFromPool(pg.PgW)
	t.Cleanup(func() { _ = db.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS(dir))
	if err != nil {
		t.Fatalf("testutil: goose provider: %v", err)
	}
	return provider
}
