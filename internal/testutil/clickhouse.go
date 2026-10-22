package testutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/chcol"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pressly/goose/v3"
	"github.com/rs/xid"
	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"

	chq "github.com/pug-sh/pug/internal/core/clickhouse"
	chdep "github.com/pug-sh/pug/internal/deps/clickhouse"
)

const testClickHouseImage = "clickhouse/clickhouse-server@sha256:a4202d2f7a0c0ac98aca2bda670f5b7278722463c0bdd4c2de5241e8e1e898ed" // 26.5.1.882-alpine

// chAdminDB issues the CREATE/DROP DATABASE statements for the per-test
// databases; tests never read or write it.
const chAdminDB = "default"

// chTestProfile turns off async inserts, on by default since ClickHouse 26: each
// insert would otherwise wait at least 50ms for a flush, and most tests insert one
// row at a time.
const chTestProfile = `<clickhouse><profiles><default><async_insert>0</async_insert></default></profiles></clickhouse>`

// TestClickHouse holds the connection to one test's private database.
type TestClickHouse struct {
	Conn *chdep.Conn
	URL  string
}

// sharedClickHouse is this package's handle on the run's ClickHouse container.
//
// template is migrated once per package. ClickHouse has no CREATE DATABASE ...
// TEMPLATE, so each test database replays its final schema, which takes
// milliseconds where rerunning every migration takes over a second.
type sharedClickHouse struct {
	admin    *sql.DB
	connFor  func(db string) string
	template string
	schema   []string
}

var chContainer = &lazyContainer[sharedClickHouse]{kind: "clickhouse", start: startClickHouse}

// SetupClickHouse starts (or reuses) the run's ClickHouse container, then
// creates a migrated database private to this test. Cleanup is registered via
// t.Cleanup; Main releases the container.
func SetupClickHouse(t *testing.T) *TestClickHouse {
	t.Helper()
	// Scoped to the test — see the note in SetupPostgres for why the shared
	// container and the cleanup below use context.Background instead.
	ctx := t.Context()

	sc := chContainer.get(t)

	name := "test_" + xid.New().String()
	if _, err := sc.admin.ExecContext(ctx, fmt.Sprintf("create database %s", quoteCH(name))); err != nil {
		t.Fatalf("testutil: create clickhouse database: %v", err)
	}

	// Registered before the schema replay and connection so a failure in either still
	// drops the database; otherwise it would linger in the shared container for
	// the rest of the run. The connection is closed only once set.
	var conn *chdep.Conn
	t.Cleanup(func() {
		if conn != nil {
			_ = conn.Close()
		}
		// Background, not t.Context: the test's context is already cancelled once
		// cleanups run, so the drop would never be sent.
		//
		// sync: Atomic databases keep a dropped table's data for 480s
		// (database_atomic_delay_before_drop_table_sec), and a run's worth would
		// pile up in the shared container.
		//
		// The timeout is a backstop against a wedged connection hanging the drop
		// until go test -timeout, not a fail-fast: it is generous so a sync drop
		// under a loaded daemon has room to finish rather than erroring out and
		// leaving the data it was meant to unlink (cleanupDropTimeout).
		dropCtx, cancel := context.WithTimeout(context.Background(), cleanupDropTimeout)
		defer cancel()
		if _, err := sc.admin.ExecContext(dropCtx,
			fmt.Sprintf("drop database if exists %s sync", quoteCH(name))); err != nil {
			t.Errorf("testutil: drop clickhouse database %s: %v", name, err)
		}
	})

	if err := sc.replaySchema(ctx, name); err != nil {
		t.Fatalf("testutil: replay clickhouse schema: %v", err)
	}

	dsn := sc.connFor(name)
	conn, err := chdep.NewReaderPool(ctx, &chdep.Config{URL: dsn})
	if err != nil {
		t.Fatalf("testutil: create clickhouse connection: %v", err)
	}

	return &TestClickHouse{Conn: conn, URL: dsn}
}

func startClickHouse() (_ *sharedClickHouse, err error) {
	// Background, not the triggering test's t.Context: this container is shared by
	// the whole run and outlives whichever test started it.
	ctx := context.Background()

	shared := runScopedName("clickhouse")
	opts := []testcontainers.ContainerCustomizer{
		// Empty, or the image boots a throwaway server just to create the
		// database, which costs seconds per container.
		tcclickhouse.WithDatabase(""),
		tcclickhouse.WithUsername("default"),
		tcclickhouse.WithPassword("test"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(chTestProfile),
			ContainerFilePath: "/etc/clickhouse-server/users.d/pug-test.xml",
			FileMode:          0o644,
		}),
	}
	if shared != "" {
		opts = append(opts, testcontainers.WithReuseByName(shared))
	}
	ctr, err := tcclickhouse.Run(ctx, testClickHouseImage, opts...)
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

	connStr, err := ctr.ConnectionString(ctx)
	if err != nil {
		return nil, fmt.Errorf("connection string: %w", err)
	}

	base, err := url.Parse(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse connection string: %w", err)
	}
	connFor := func(db string) string {
		u := *base
		u.Path = "/" + db
		return u.String()
	}

	admin, err := sql.Open("clickhouse", connFor(chAdminDB))
	if err != nil {
		return nil, fmt.Errorf("admin connection: %w", err)
	}

	// Named per package so packages sharing the server never touch each other's.
	template := "pug_template_" + xid.New().String()
	schema, err := buildTemplate(ctx, admin, template, connFor(template))
	if err != nil {
		_ = admin.Close()
		return nil, err
	}

	teardowns.add(func() error {
		defer func() { _ = admin.Close() }()
		if shared != "" {
			dropCtx, cancel := context.WithTimeout(context.Background(), cleanupDropTimeout)
			defer cancel()
			if _, err := admin.ExecContext(dropCtx, "drop database if exists "+quoteCH(template)+" sync"); err != nil {
				return fmt.Errorf("drop clickhouse template: %w", err)
			}
			return nil
		}
		if err := release(ctr); err != nil {
			return fmt.Errorf("terminate clickhouse container: %w", err)
		}
		return nil
	})

	return &sharedClickHouse{admin: admin, connFor: connFor, template: template, schema: schema}, nil
}

// buildTemplate migrates the template database and returns its schema in replay
// order: tables, then the materialized views over them, then plain views.
func buildTemplate(ctx context.Context, admin *sql.DB, template, dsn string) ([]string, error) {
	if _, err := admin.ExecContext(ctx, "create database "+quoteCH(template)); err != nil {
		return nil, fmt.Errorf("create template: %w", err)
	}
	if err := migrateClickHouse(ctx, dsn); err != nil {
		return nil, fmt.Errorf("migrate template: %w", err)
	}

	// .inner tables belong to materialized views without TO, which recreate them.
	rows, err := admin.QueryContext(ctx, `
		select create_table_query
		from system.tables
		where database = ? and not startsWith(name, '.inner')
		order by multiIf(engine = 'MaterializedView', 1, engine = 'View', 2, 0), name`,
		template)
	if err != nil {
		return nil, fmt.Errorf("read template schema: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var schema []string
	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			return nil, fmt.Errorf("scan template schema: %w", err)
		}
		schema = append(schema, ddl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read template schema: %w", err)
	}
	return schema, nil
}

var errTemplateReference = errors.New("template reference survived the rewrite")

// replaySchema recreates the template's tables and views in db, along with the
// goose history, so db looks exactly as if it had been migrated.
func (sc *sharedClickHouse) replaySchema(ctx context.Context, db string) error {
	for _, ddl := range sc.schema {
		stmt := strings.ReplaceAll(ddl, sc.template+".", db+".")
		// A reference left unrewritten would have db read or write the template.
		if strings.Contains(stmt, sc.template) {
			return fmt.Errorf("%w: %s", errTemplateReference, stmt)
		}
		if _, err := sc.admin.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w: %s", err, stmt)
		}
	}
	if _, err := sc.admin.ExecContext(ctx, fmt.Sprintf("insert into %s.%s select * from %s.%s",
		quoteCH(db), goose.DefaultTablename, quoteCH(sc.template), goose.DefaultTablename)); err != nil {
		return fmt.Errorf("copy goose history: %w", err)
	}

	// A refreshable view refreshes as it is created, replacing its target, so a
	// test that wrote there first would lose its rows.
	var views []string
	if err := sc.admin.QueryRowContext(ctx,
		"select groupArray(view) from system.view_refreshes where database = ?", db).Scan(&views); err != nil {
		return fmt.Errorf("list refreshable views: %w", err)
	}
	for _, v := range views {
		if _, err := sc.admin.ExecContext(ctx, "system wait view "+quoteCH(db)+"."+quoteCH(v)); err != nil {
			return fmt.Errorf("wait for view %s: %w", v, err)
		}
	}
	return nil
}

// quoteCH quotes a ClickHouse identifier. The names it is given are generated,
// not caller-supplied, so this is hygiene rather than a trust boundary.
func quoteCH(ident string) string {
	return "`" + ident + "`"
}

func migrateClickHouse(ctx context.Context, connStr string) error {
	db, err := sql.Open("clickhouse", connStr)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return errors.New("unable to determine source file path")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "schema", "clickhouse", "migrations")

	provider, err := goose.NewProvider(goose.DialectClickHouse, db, os.DirFS(dir))
	if err != nil {
		return err
	}

	_, err = provider.Up(ctx)
	return err
}

// InsertEvent inserts one row into the ClickHouse events table, routing
// promoted auto-properties to dedicated columns and storing the remainder in
// auto_properties.
func InsertEvent(
	ctx context.Context,
	t *testing.T,
	conn driver.Conn,
	eventID, projectID, distinctID, kind, sessionID string,
	auto, custom map[string]string,
	occurTime time.Time,
) {
	t.Helper()
	InsertEvents(ctx, t, conn, Event{
		EventID: eventID, ProjectID: projectID, DistinctID: distinctID, Kind: kind, SessionID: sessionID,
		Auto: auto, Custom: custom, OccurTime: occurTime,
	})
}

// Event is one row for InsertEvents.
type Event struct {
	EventID, ProjectID, DistinctID, Kind, SessionID string
	Auto, Custom                                    map[string]string
	OccurTime                                       time.Time
}

// InsertEvents inserts events in one batch. Each insert writes a part per table
// and materialized view, so seeding hundreds of rows one at a time takes seconds.
func InsertEvents(ctx context.Context, t *testing.T, conn driver.Conn, events ...Event) {
	t.Helper()

	batch, err := conn.PrepareBatch(ctx, chq.EventsInsertStmt)
	if err != nil {
		t.Fatalf("prepare event insert batch: %v", err)
	}

	for _, e := range events {
		// A zero time lands at 1970, outside every windowed read, so the test
		// would pass over nothing.
		if e.OccurTime.IsZero() {
			t.Fatalf("testutil: event %s has no OccurTime", e.EventID)
		}
		autoAny := make(map[string]any, len(e.Auto))
		for k, v := range e.Auto {
			autoAny[k] = v
		}
		customAny := make(map[string]any, len(e.Custom))
		for k, v := range e.Custom {
			customAny[k] = v
		}

		promoted, restAuto := chq.SplitPromotedAutoAnyProperties(autoAny)
		args := []any{e.EventID, e.ProjectID, e.DistinctID, e.Kind,
			stringMapToVariantMap(restAuto), stringMapToVariantMap(customAny)}
		args = append(args, promoted.AppendArgs()...)
		args = append(args, e.OccurTime, e.SessionID)
		if err := batch.Append(args...); err != nil {
			t.Fatalf("append event insert batch: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send event insert batch: %v", err)
	}
}

func stringMapToVariantMap(src map[string]any) map[string]chcol.Variant {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]chcol.Variant, len(src))
	for k, v := range src {
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = chcol.NewVariantWithType(s, "String")
	}
	return out
}
