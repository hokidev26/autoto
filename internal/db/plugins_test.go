package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginFreshSchemaAndV35Migration(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if version := readUserVersion(t, ctx, fresh.DB()); version != CurrentDBVersion {
		t.Fatalf("expected v%d, got v%d", CurrentDBVersion, version)
	}
	for _, table := range []string{"plugins", "plugin_tools"} {
		if !testTableExists(t, ctx, fresh.DB(), table) {
			t.Fatalf("fresh schema missing %s", table)
		}
	}
	for _, column := range []string{"id", "slug", "name", "version", "description", "manifest_version", "root_path", "command", "args_json", "env_json", "secret_refs_json", "enabled", "status", "revision", "manifest_hash", "last_checked_at", "last_error", "created_at", "updated_at"} {
		if !testColumnExists(t, ctx, fresh.DB(), "plugins", column) {
			t.Fatalf("fresh plugins missing %s", column)
		}
	}
	freshSnapshot := pluginSchemaSnapshot(t, ctx, fresh.DB())

	path := filepath.Join(t.TempDir(), "v35.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, strings.TrimSuffix(schemaSQL, pluginSchemaSQL)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 35`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version := readUserVersion(t, ctx, migrated.DB()); version != CurrentDBVersion {
		t.Fatalf("expected migrated v%d, got v%d", CurrentDBVersion, version)
	}
	if migratedSnapshot := pluginSchemaSnapshot(t, ctx, migrated.DB()); migratedSnapshot != freshSnapshot {
		t.Fatalf("fresh and migrated plugin schemas differ\nfresh:\n%s\nmigrated:\n%s", freshSnapshot, migratedSnapshot)
	}
}

func TestPluginCRUDStatusAndEnabledSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secretValue := "must-never-be-persisted"
	t.Setenv("PLUGIN_TEST_TOKEN", secretValue)
	created, err := store.CreatePlugin(ctx, testPlugin(t, "demo", false))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Revision != 1 || created.Status != "ready" || created.SecretRefs["API_TOKEN"] != "env:PLUGIN_TEST_TOKEN" {
		t.Fatalf("unexpected created plugin: %+v", created)
	}
	var storedEnv, storedRefs string
	if err := store.DB().QueryRowContext(ctx, `SELECT env_json, secret_refs_json FROM plugins WHERE id = ?`, created.ID).Scan(&storedEnv, &storedRefs); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedEnv, secretValue) || strings.Contains(storedRefs, secretValue) {
		t.Fatal("resolved secret value was persisted")
	}
	listed, err := store.ListPlugins(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("unexpected list: %+v err=%v", listed, err)
	}
	bySlug, err := store.GetPluginBySlug(ctx, "DEMO")
	if err != nil || bySlug.ID != created.ID {
		t.Fatalf("case-insensitive slug lookup failed: %+v err=%v", bySlug, err)
	}
	created.Version = "2.0.0"
	updated, err := store.UpdatePlugin(ctx, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "2.0.0" || updated.Revision != 2 {
		t.Fatalf("unexpected update: %+v", updated)
	}
	checkedAt := Now()
	enabled, err := store.UpdatePluginStatus(ctx, created.ID, "ready", true, checkedAt, "")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Revision != 3 || enabled.LastCheckedAt != checkedAt {
		t.Fatalf("unexpected status update: %+v", enabled)
	}
	if _, err := store.ReplacePluginTools(ctx, created.ID, []PluginTool{{RemoteName: "search", ExposedName: "demo.search", Description: "Search", InputSchemaJSON: []byte(`{"type":"object"}`)}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ListEnabledPluginsWithTools(ctx)
	if err != nil || len(snapshot) != 1 || len(snapshot[0].Tools) != 1 || snapshot[0].Tools[0].ExposedName != "demo.search" {
		t.Fatalf("unexpected enabled snapshot: %+v err=%v", snapshot, err)
	}
	if err := store.DeletePlugin(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPlugin(ctx, created.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected deleted plugin not found, got %v", err)
	}
	if err := store.DeletePlugin(ctx, created.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected repeated delete not found, got %v", err)
	}
}

func TestPluginToolSnapshotAtomicUniqueAndCascade(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CreatePlugin(ctx, testPlugin(t, "first", true))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreatePlugin(ctx, testPlugin(t, "second", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlugin(ctx, testPlugin(t, "FIRST", false)); !IsConflict(err) {
		t.Fatalf("expected case-insensitive slug conflict, got %v", err)
	}
	if _, err := store.ReplacePluginTools(ctx, first.ID, []PluginTool{{RemoteName: "old", ExposedName: "first.old", InputSchemaJSON: []byte(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplacePluginTools(ctx, second.ID, []PluginTool{{RemoteName: "reserved", ExposedName: "shared.tool", InputSchemaJSON: []byte(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplacePluginTools(ctx, first.ID, []PluginTool{
		{RemoteName: "new", ExposedName: "first.new", InputSchemaJSON: []byte(`{}`)},
		{RemoteName: "conflict", ExposedName: "SHARED.TOOL", InputSchemaJSON: []byte(`{}`)},
	})
	if !IsConflict(err) {
		t.Fatalf("expected exposed-name conflict, got %v", err)
	}
	tools, err := store.ListPluginTools(ctx, first.ID)
	if err != nil || len(tools) != 1 || tools[0].RemoteName != "old" {
		t.Fatalf("failed replacement did not roll back old snapshot: %+v err=%v", tools, err)
	}
	if err := store.DeletePlugin(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_tools WHERE plugin_id = ?`, first.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cascade delete left %d tools", count)
	}
}

func TestReplacePluginManifestAtomicallyUpdatesRowAndClearsTools(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "replace-manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CreatePlugin(ctx, testPlugin(t, "first", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlugin(ctx, testPlugin(t, "second", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplacePluginTools(ctx, first.ID, []PluginTool{{RemoteName: "old", ExposedName: "first.old", InputSchemaJSON: []byte(`{}`)}}); err != nil {
		t.Fatal(err)
	}

	conflicting := first
	conflicting.Slug = "second"
	if _, err := store.ReplacePluginManifest(ctx, conflicting); !IsConflict(err) {
		t.Fatalf("expected slug conflict, got %v", err)
	}
	unchanged, err := store.GetPlugin(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Slug != "first" || unchanged.Revision != first.Revision {
		t.Fatalf("failed replacement mutated plugin row: %+v", unchanged)
	}
	kept, err := store.ListPluginTools(ctx, first.ID)
	if err != nil || len(kept) != 1 {
		t.Fatalf("failed replacement dropped tool snapshot: %+v err=%v", kept, err)
	}

	replacement := first
	replacement.Slug = "renamed"
	replacement.Name = "Renamed"
	replacement.Version = "2.0.0"
	replacement.Description = "updated"
	replacement.Args = []string{"--fast"}
	replacement.Env = map[string]string{"MODE": "two"}
	replacement.SecretRefs = map[string]string{}
	replacement.Enabled = false
	replacement.Status = "disabled"
	replacement.ManifestHash = strings.Repeat("b", 64)
	replacement.LastCheckedAt = ""
	replacement.LastError = ""
	updated, err := store.ReplacePluginManifest(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Slug != "renamed" || updated.Name != "Renamed" || updated.Version != "2.0.0" || updated.Description != "updated" {
		t.Fatalf("replacement fields were not adopted: %+v", updated)
	}
	if updated.Enabled || updated.Status != "disabled" || updated.ManifestHash != strings.Repeat("b", 64) {
		t.Fatalf("replacement state was not adopted: %+v", updated)
	}
	if updated.Revision != first.Revision+1 {
		t.Fatalf("replacement did not bump revision: %+v", updated)
	}
	if updated.LastCheckedAt != "" || updated.LastError != "" {
		t.Fatalf("replacement kept stale check state: %+v", updated)
	}
	cleared, err := store.ListPluginTools(ctx, first.ID)
	if err != nil || len(cleared) != 0 {
		t.Fatalf("replacement did not clear tool snapshot: %+v err=%v", cleared, err)
	}

	ghost := testPlugin(t, "ghost", false)
	ghost.ID = "missing"
	if _, err := store.ReplacePluginManifest(ctx, ghost); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected unknown plugin not found, got %v", err)
	}
}

func TestPluginToolDescriptionAllowsTwoKiB(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "description-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plugin, err := store.CreatePlugin(ctx, testPlugin(t, "description-limit", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplacePluginTools(ctx, plugin.ID, []PluginTool{{RemoteName: "boundary", ExposedName: "description.boundary", Description: strings.Repeat("x", 2<<10), InputSchemaJSON: []byte(`{}`)}}); err != nil {
		t.Fatalf("expected 2 KiB description to be accepted: %v", err)
	}
	if _, err := store.ReplacePluginTools(ctx, plugin.ID, []PluginTool{{RemoteName: "too-long", ExposedName: "description.too-long", Description: strings.Repeat("x", (2<<10)+1), InputSchemaJSON: []byte(`{}`)}}); err == nil {
		t.Fatal("expected description above 2 KiB to be rejected")
	}
}

func testPlugin(t *testing.T, slug string, enabled bool) Plugin {
	t.Helper()
	root := t.TempDir()
	command := filepath.Join(root, "plugin")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Plugin{
		Slug: slug, Name: strings.ToUpper(slug), Version: "1.0.0", Description: "test plugin",
		ManifestVersion: "autoto.dev/v1alpha1", RootPath: root, Command: "plugin", Args: []string{"--stdio"},
		Env: map[string]string{"LOG_LEVEL": "debug"}, SecretRefs: map[string]string{"API_TOKEN": "env:PLUGIN_TEST_TOKEN"},
		Enabled: enabled, Status: "ready", ManifestHash: strings.Repeat("a", 64),
	}
}

func pluginSchemaSnapshot(t *testing.T, ctx context.Context, database *sql.DB) string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name IN ('plugins','plugin_tools') AND type IN ('table','index') AND sql IS NOT NULL ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var snapshot strings.Builder
	for rows.Next() {
		var objectType, name, definition string
		if err := rows.Scan(&objectType, &name, &definition); err != nil {
			t.Fatal(err)
		}
		snapshot.WriteString(objectType + ":" + name + "=" + definition + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}
