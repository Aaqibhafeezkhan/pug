package testutil

import (
	"errors"
	"slices"
	"testing"
)

// TestReplayedClickHouseMatchesTemplate guards the schema replay: a test database
// must hold the same tables, columns and goose history as the migrated template.
func TestReplayedClickHouseMatchesTemplate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ch := SetupClickHouse(t)
	sc := chContainer.get(t)

	var db string
	if err := ch.Conn.QueryRow(t.Context(), "select currentDatabase()").Scan(&db); err != nil {
		t.Fatalf("current database: %v", err)
	}
	read := func(query, db string) []string {
		var out []string
		if err := sc.admin.QueryRowContext(t.Context(), query, db).Scan(&out); err != nil {
			t.Fatalf("read schema of %s: %v", db, err)
		}
		return out
	}
	for _, query := range []string{
		`select arraySort(groupArray(concat(name, ' ', ifNull(toString(total_rows), ''), ' ',
			replaceAll(create_table_query, concat(database, '.'), ''))))
		from system.tables where database = ? and not startsWith(name, '.inner')`,
		`select arraySort(groupArray(concat(table, ' ', toString(position), ' ', name, ' ', type, ' ',
			default_kind, ' ', default_expression, ' ', compression_codec)))
		from system.columns where database = ? and not startsWith(table, '.inner')`,
	} {
		if got, want := read(query, db), read(query, sc.template); !slices.Equal(got, want) {
			t.Errorf("replayed database differs from the template:\n got %q\nwant %q", got, want)
		}
	}
}

func TestReplaySchemaRefusesTemplateReference(t *testing.T) {
	sc := &sharedClickHouse{
		template: "pug_template_x",
		schema:   []string{"CREATE TABLE pug_template_x.m (k String) ENGINE = Merge('pug_template_x', '^e')"},
	}
	if err := sc.replaySchema(t.Context(), "test_y"); !errors.Is(err, errTemplateReference) {
		t.Fatalf("replaySchema = %v, want errTemplateReference", err)
	}
}
