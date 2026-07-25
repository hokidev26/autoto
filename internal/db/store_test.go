package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenInitializesUserVersionForNewDatabase(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	version := readUserVersion(t, ctx, store.DB())
	if version != CurrentDBVersion {
		t.Fatalf("expected database version %d, got %d", CurrentDBVersion, version)
	}
}

func TestOpenSupportsRelativeDatabasePath(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(".tmp-store-tests", NewID())
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	store, err := Open(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected database version %d, got %d", CurrentDBVersion, version)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	version := readUserVersion(t, ctx, store.DB())
	if version != CurrentDBVersion {
		t.Fatalf("expected database version %d, got %d", CurrentDBVersion, version)
	}
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "Demo" {
		t.Fatalf("expected preserved project after idempotent open, got %+v", projects)
	}
}

func TestOpenRejectsFutureDatabaseVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	store, err := Open(ctx, path)
	if err == nil {
		store.Close()
		t.Fatal("expected future database version to be rejected")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("expected clear future version error, got %v", err)
	}
}

func TestForeignKeysEnabledAfterOpen(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Force the first connection back to the pool boundary. The DSN pragma
	// must apply again when database/sql opens a replacement connection.
	store.DB().SetMaxIdleConns(0)
	var enabled int
	if err := store.DB().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("expected foreign keys to be enabled, got %d", enabled)
	}
	var busyTimeout int
	if err := store.DB().QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("expected busy timeout 5000ms, got %d", busyTimeout)
	}
}

func readUserVersion(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func openRawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func testTableExists(t *testing.T, ctx context.Context, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}

func testColumnExists(t *testing.T, ctx context.Context, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}
