package db

import (
	"context"
	"database/sql"
	"fmt"
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
	// Use the production writer DSN, not a bare path. A path-only open keeps
	// SQLite's default DELETE journal and synchronous=FULL, so applying
	// schemaSQL (one large multi-statement Exec) fsyncs the whole file on every
	// commit. On Windows that FlushFileBuffers call can stall the test suite
	// for minutes; WAL + NORMAL is the same durability Autoto already chose
	// for the live store.
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
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

// The per-migration tests in this package build only the tables the migration
// under test touches, then run the entire chain. Every later migration
// therefore meets a database missing most tables, so a migration that assumes
// a table exists breaks tests unrelated to itself — which is how
// migrateV57DangerReflectionLevel (guarded ALTER, unguarded UPDATE) started
// failing three reasoning/navigation/provider-stats tests at once.
//
// This locks the recent migrations against that. It deliberately does not cover
// the whole history: the pre-v28 migrations predate this convention and assume
// core tables like runs and agents, which is sound for real databases (those
// tables long precede them) but not reproducible against an empty file.
func TestRecentMigrationsToleratePartialSchema(t *testing.T) {
	ctx := context.Background()
	const firstGuardedVersion = 28
	for _, m := range migrations {
		if m.version < firstGuardedVersion {
			continue
		}
		t.Run(fmt.Sprintf("v%d_%s", m.version, strings.ReplaceAll(m.name, " ", "_")), func(t *testing.T) {
			raw := openRawDB(t, filepath.Join(t.TempDir(), "partial.db"))
			defer raw.Close()
			tx, err := raw.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := m.up(ctx, tx); err != nil {
				t.Fatalf("migration %d (%s) failed against an empty database: %v", m.version, m.name, err)
			}
		})
	}
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
