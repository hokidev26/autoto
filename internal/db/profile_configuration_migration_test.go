package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var profileConfigurationTables = []string{
	"tool_availability_rules",
	"tool_availability_revisions",
	"agent_role_definitions",
	"agent_role_definition_revisions",
	"prompt_definitions",
	"prompt_definition_revisions",
	"lifecycle_hooks",
	"lifecycle_hook_run_bindings",
	"lifecycle_hook_events",
	"lifecycle_hook_executions",
	"lifecycle_hook_attempts",
}

var profileConfigurationIndexes = []string{
	"idx_tool_availability_rules_target",
	"idx_tool_availability_revisions_rule",
	"idx_agent_role_definitions_scope",
	"idx_agent_role_definition_revisions_history",
	"idx_prompt_definitions_scope",
	"idx_prompt_definition_revisions_history",
	"lifecycle_hooks_dispatch_idx",
	"lifecycle_hook_events_run_idx",
	"lifecycle_hook_executions_hook_idx",
	"lifecycle_hook_executions_event_idx",
	"lifecycle_hook_attempts_execution_idx",
}

func TestV51ProfileConfigurationMigrationFromV50(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v50.db")
	raw := openRawDB(t, path)
	withoutV53 := strings.TrimSuffix(schemaSQL, remoteCollaborationSchemaSQL)
	v50Schema := strings.TrimSuffix(withoutV53, profileConfigurationSchemaSQL)
	if withoutV53 == schemaSQL || v50Schema == withoutV53 {
		t.Fatal("v51/v53 schema suffixes were not present")
	}
	if _, err := raw.ExecContext(ctx, v50Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 50`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assertProfileConfigurationSchema(t, ctx, store.DB())
	// Opening runs every pending migration, so assert the database is fully
	// migrated rather than pinning the version to 51 and breaking whenever a
	// later migration is appended.
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("user_version=%d, want %d", version, CurrentDBVersion)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer store.Close()
	assertProfileConfigurationSchema(t, ctx, store.DB())

	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh-comparison.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	migratedObjects := profileConfigurationSchemaObjects(t, ctx, store.DB())
	freshObjects := profileConfigurationSchemaObjects(t, ctx, fresh.DB())
	if !reflect.DeepEqual(migratedObjects, freshObjects) {
		t.Fatalf("v50 migration schema differs from fresh schema\nmigrated: %#v\nfresh: %#v", migratedObjects, freshObjects)
	}
}

func TestFreshDatabaseIncludesV51ProfileConfigurationSchema(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertProfileConfigurationSchema(t, ctx, store.DB())
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("user_version=%d, want %d", version, CurrentDBVersion)
	}
}

func profileConfigurationSchemaObjects(t *testing.T, ctx context.Context, database *sql.DB) map[string]string {
	t.Helper()
	objects := make(map[string]string, len(profileConfigurationTables)+len(profileConfigurationIndexes))
	names := append(append([]string{}, profileConfigurationTables...), profileConfigurationIndexes...)
	for _, name := range names {
		var objectType, sqlText string
		if err := database.QueryRowContext(ctx, `SELECT type, COALESCE(sql, '') FROM sqlite_master WHERE name = ?`, name).Scan(&objectType, &sqlText); err != nil {
			t.Fatalf("read schema object %s: %v", name, err)
		}
		objects[objectType+":"+name] = strings.Join(strings.Fields(sqlText), " ")
	}
	return objects
}

func assertProfileConfigurationSchema(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, table := range profileConfigurationTables {
		if !testTableExists(t, ctx, database, table) {
			t.Errorf("missing table %s", table)
		}
	}
	for _, index := range profileConfigurationIndexes {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("missing index %s", index)
		}
	}

	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_list(lifecycle_hook_events)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	foundBindingFK := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "lifecycle_hook_run_bindings" && from == "binding_id" && to == "id" && strings.EqualFold(onDelete, "CASCADE") {
			foundBindingFK = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !foundBindingFK {
		t.Fatal("lifecycle_hook_events binding foreign key is missing")
	}
}
