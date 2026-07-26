package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A fresh install and an upgraded install reach CurrentDBVersion by two
// different routes. An empty database takes the shortcut in Migrate: it runs
// migrateV1Baseline (the raw schemaSQL in schema.go) once and is stamped
// straight to CurrentDBVersion. An existing database instead replays every
// incremental migration.
//
// That means schemaSQL and the incremental migrations are dual-maintained. Add
// a column in a new migration and forget to add it to schemaSQL and nothing
// fails: upgraded users get the column, new users silently do not, and the bug
// only surfaces later as a query error on one population and not the other.
//
// These tests close that gap by building both schemas and comparing them.

func openMigratedTestDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".db")
	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)
	return database
}

// schemaObject is one CREATE statement from sqlite_master, normalized so that
// formatting differences between schemaSQL and a migration's ALTER do not
// register as drift.
type schemaObject struct {
	kind  string
	name  string
	table string
	sql   string
}

var schemaWhitespace = regexp.MustCompile(`\s+`)

func normalizeSchemaSQL(statement string) string {
	statement = schemaWhitespace.ReplaceAllString(statement, " ")
	statement = strings.ReplaceAll(statement, "( ", "(")
	statement = strings.ReplaceAll(statement, " )", ")")
	statement = strings.ReplaceAll(statement, ` "`, " ")
	statement = strings.ReplaceAll(statement, `" `, " ")
	statement = strings.ReplaceAll(statement, "IF NOT EXISTS ", "")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(statement), ";"))
}

func dumpSchemaObjects(t *testing.T, database *sql.DB) map[string]schemaObject {
	t.Helper()
	rows, err := database.QueryContext(context.Background(),
		`SELECT type, name, tbl_name, sql FROM sqlite_master
		 WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()
	objects := make(map[string]schemaObject)
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.table, &object.sql); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		object.sql = normalizeSchemaSQL(object.sql)
		objects[object.kind+":"+object.name] = object
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("schema dump is empty")
	}
	return objects
}

// tableColumns is compared alongside the raw SQL because an ALTER TABLE ADD
// COLUMN rewrites sqlite_master's stored text, so two schemas can hold the same
// columns while their CREATE statements differ cosmetically.
func tableColumns(t *testing.T, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	columns := make([]string, 0, 16)
	for rows.Next() {
		var (
			cid        int
			name, kind string
			notNull    int
			deflt      sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &kind, &notNull, &deflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	sort.Strings(columns)
	return columns
}

// freshInstallSchema is the database a brand new user gets.
func freshInstallSchema(t *testing.T) *sql.DB {
	t.Helper()
	database := openMigratedTestDB(t, "fresh")
	if err := runMigrations(context.Background(), database); err != nil {
		t.Fatalf("fresh install migrate: %v", err)
	}
	version, err := getUserVersion(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentDBVersion {
		t.Fatalf("fresh install stamped version %d, want %d", version, CurrentDBVersion)
	}
	return database
}

// upgradedSchema is the database an existing user ends up with: the same
// baseline, then every incremental migration replayed in order.
func upgradedSchema(t *testing.T) *sql.DB {
	t.Helper()
	database := openMigratedTestDB(t, "upgraded")
	ctx := context.Background()
	for _, m := range migrations {
		if err := runMigration(ctx, database, m); err != nil {
			t.Fatalf("replay migration %d %s: %v", m.version, m.name, err)
		}
	}
	version, err := getUserVersion(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentDBVersion {
		t.Fatalf("incremental path reached version %d, want %d; the migrations slice and CurrentDBVersion disagree", version, CurrentDBVersion)
	}
	return database
}

// indexSignature identifies an index by what it does rather than by its name.
//
// Comparing names produces false alarms this codebase is full of: tables were
// renamed over time so the same index is idx_tool_calls_run in schemaSQL and
// idx_agent_tool_calls_run after migrations, and a column-level UNIQUE in
// schemaSQL becomes an explicitly named unique index in a migration because
// SQLite cannot ALTER TABLE ADD CONSTRAINT. Both are the same guarantee.
// Auto-indexes are deliberately included so the UNIQUE-column form matches its
// migration-created equivalent.
func indexSignature(t *testing.T, database *sql.DB, table string) map[string]struct{} {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), fmt.Sprintf("PRAGMA index_list(%q)", table))
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	type indexMeta struct {
		name    string
		unique  int
		partial int
	}
	metas := make([]indexMeta, 0, 8)
	for rows.Next() {
		var (
			seq    int
			meta   indexMeta
			origin string
		)
		if err := rows.Scan(&seq, &meta.name, &meta.unique, &origin, &meta.partial); err != nil {
			rows.Close()
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		metas = append(metas, meta)
	}
	rows.Close()

	signatures := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		columnRows, err := database.QueryContext(context.Background(), fmt.Sprintf("PRAGMA index_info(%q)", meta.name))
		if err != nil {
			t.Fatalf("index_info(%s): %v", meta.name, err)
		}
		columns := make([]string, 0, 4)
		for columnRows.Next() {
			var (
				seqno, cid int
				column     sql.NullString
			)
			if err := columnRows.Scan(&seqno, &cid, &column); err != nil {
				columnRows.Close()
				t.Fatalf("scan index_info(%s): %v", meta.name, err)
			}
			if column.Valid {
				columns = append(columns, column.String)
			}
		}
		columnRows.Close()
		if len(columns) == 0 {
			continue
		}
		signatures[fmt.Sprintf("%s(%s) unique=%d partial=%d", table, strings.Join(columns, ","), meta.unique, meta.partial)] = struct{}{}
	}
	return signatures
}

// TestFreshInstallIndexesMatchIncrementalMigrations catches an index a
// migration creates that schemaSQL never gained, which leaves new installs
// silently slower than upgraded ones on the same query. It found three real
// cases when first written (idx_runs_continuation_pending,
// idx_runs_waiting_background_task, idx_notification_deliveries_generation).
//
// Only the migration-has-it-and-fresh-does-not direction is an error. The
// reverse is expected: replaying historical migrations over today's schemaSQL
// cannot reproduce indexes that were later renamed away.
func TestFreshInstallIndexesMatchIncrementalMigrations(t *testing.T) {
	freshDB := freshInstallSchema(t)
	upgradedDB := upgradedSchema(t)

	freshObjects := dumpSchemaObjects(t, freshDB)
	tables := make([]string, 0, len(freshObjects))
	for _, object := range freshObjects {
		if object.kind == "table" {
			tables = append(tables, object.name)
		}
	}
	sort.Strings(tables)

	for _, table := range tables {
		fresh := indexSignature(t, freshDB, table)
		upgraded := indexSignature(t, upgradedDB, table)
		missing := make([]string, 0)
		for signature := range upgraded {
			if _, ok := fresh[signature]; !ok {
				missing = append(missing, signature)
			}
		}
		sort.Strings(missing)
		for _, signature := range missing {
			t.Errorf("index %s is created by a migration but missing from a fresh install; add it to schemaSQL in schema.go", signature)
		}
	}
}

// TestFreshInstallTableColumnsMatchIncrementalMigrations catches the common
// case the object-level comparison can miss: ALTER TABLE ADD COLUMN leaves the
// table present in both schemas while only one of them has the new column.
func TestFreshInstallTableColumnsMatchIncrementalMigrations(t *testing.T) {
	freshDB := freshInstallSchema(t)
	upgradedDB := upgradedSchema(t)

	freshObjects := dumpSchemaObjects(t, freshDB)
	tables := make([]string, 0, len(freshObjects))
	for _, object := range freshObjects {
		if object.kind == "table" {
			tables = append(tables, object.name)
		}
	}
	sort.Strings(tables)

	for _, table := range tables {
		freshColumns := tableColumns(t, freshDB, table)
		upgradedColumns := tableColumns(t, upgradedDB, table)
		if strings.Join(freshColumns, ",") == strings.Join(upgradedColumns, ",") {
			continue
		}
		missingFromFresh := columnDifference(upgradedColumns, freshColumns)
		missingFromUpgraded := columnDifference(freshColumns, upgradedColumns)
		if len(missingFromFresh) > 0 {
			t.Errorf("table %q: columns %v exist after incremental migrations but not in a fresh install; add them to schemaSQL", table, missingFromFresh)
		}
		if len(missingFromUpgraded) > 0 {
			t.Errorf("table %q: columns %v exist in a fresh install but not after incremental migrations; an upgrading user will never get them", table, missingFromUpgraded)
		}
	}
}

func columnDifference(want, have []string) []string {
	present := make(map[string]struct{}, len(have))
	for _, column := range have {
		present[column] = struct{}{}
	}
	missing := make([]string, 0)
	for _, column := range want {
		if _, ok := present[column]; !ok {
			missing = append(missing, column)
		}
	}
	return missing
}

// TestMigrationsAreReentrant pins the property the parity tests rely on and
// that interrupted-upgrade recovery depends on: replaying every migration over
// an already-current database must succeed and must not destroy anything.
//
// It deliberately does not require the object set to be unchanged. Replaying
// historical migrations does add objects, and legitimately so: indexes under
// names that were later renamed, and triggers that are the migration-path
// equivalent of CHECK constraints schemaSQL declares inline, because SQLite
// cannot ALTER TABLE ADD CONSTRAINT. Losing an object is the real failure.
func TestMigrationsAreReentrant(t *testing.T) {
	database := freshInstallSchema(t)
	before := dumpSchemaObjects(t, database)

	ctx := context.Background()
	for _, m := range migrations {
		if err := runMigration(ctx, database, m); err != nil {
			t.Fatalf("migration %d %s is not re-entrant: %v", m.version, m.name, err)
		}
	}
	after := dumpSchemaObjects(t, database)

	// Only table loss is checked. Indexes and triggers legitimately disappear
	// here because several migrations drop and recreate them under new names
	// after a table rename (idx_tool_calls_run becomes idx_agent_tool_calls_run,
	// for instance); the index-signature test above is what proves nothing was
	// functionally lost in that churn. A missing table is never churn.
	for key, object := range before {
		if object.kind != "table" {
			continue
		}
		if _, ok := after[key]; !ok {
			t.Errorf("replaying migrations dropped table %s", object.name)
		}
	}
}

// TestMigrationVersionsAreSequential guards the list itself: a duplicated or
// skipped version silently changes which migrations an upgrading user runs.
func TestMigrationVersionsAreSequential(t *testing.T) {
	for index, m := range migrations {
		want := index + 1
		if m.version != want {
			t.Fatalf("migrations[%d] has version %d, want %d; versions must be contiguous and ordered", index, m.version, want)
		}
		if strings.TrimSpace(m.name) == "" {
			t.Errorf("migrations[%d] (version %d) has no name", index, m.version)
		}
		if m.up == nil {
			t.Errorf("migrations[%d] (version %d) has no up function", index, m.version)
		}
	}
	if last := migrations[len(migrations)-1].version; last != CurrentDBVersion {
		t.Fatalf("last migration is version %d but CurrentDBVersion is %d", last, CurrentDBVersion)
	}
}
