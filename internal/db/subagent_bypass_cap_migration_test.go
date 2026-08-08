package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A subagent inherits bypassPermissions from a parent that already holds it, so
// storage has to stop rejecting that value. The upgrade path is what matters
// here: an existing database carries the narrow CHECK, and a fresh-schema test
// would not exercise it at all.
func TestV62SubagentBypassCapUpgradeFromV61(t *testing.T) {
	ctx := context.Background()
	raw := openRawDB(t, filepath.Join(t.TempDir(), "legacy-cap.db"))
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  status TEXT NOT NULL,
  permission_mode_cap TEXT NOT NULL DEFAULT '',
  trigger_type TEXT NOT NULL DEFAULT 'manual',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (permission_mode_cap IN ('', 'readOnly', 'acceptEdits'))
);
CREATE TABLE background_tasks (
  id TEXT PRIMARY KEY,
  owner_agent_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  permission_mode_cap TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (permission_mode_cap IN ('', 'readOnly', 'acceptEdits'))
);
INSERT INTO runs (id, agent_id, status, permission_mode_cap, trigger_type, created_at, updated_at)
  VALUES ('legacy-run', 'agent-1', 'completed', 'acceptEdits', 'scheduled', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
INSERT INTO background_tasks (id, owner_agent_id, kind, status, permission_mode_cap, created_at, updated_at)
  VALUES ('legacy-task', 'agent-1', 'agent', 'succeeded', 'readOnly', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
PRAGMA user_version = 61;
`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, raw); err != nil {
		t.Fatalf("v61 database failed to migrate: %v", err)
	}

	// Rewriting a table to widen its CHECK must not lose the rows it held.
	var runCap, taskCap string
	if err := raw.QueryRowContext(ctx, `SELECT permission_mode_cap FROM runs WHERE id = 'legacy-run'`).Scan(&runCap); err != nil || runCap != "acceptEdits" {
		t.Fatalf("legacy run cap was not preserved: cap=%q err=%v", runCap, err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT permission_mode_cap FROM background_tasks WHERE id = 'legacy-task'`).Scan(&taskCap); err != nil || taskCap != "readOnly" {
		t.Fatalf("legacy task cap was not preserved: cap=%q err=%v", taskCap, err)
	}

	if _, err := raw.ExecContext(ctx, `UPDATE runs SET permission_mode_cap = 'bypassPermissions' WHERE id = 'legacy-run'`); err != nil {
		t.Fatalf("bypassPermissions must be accepted on runs after migration: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE background_tasks SET permission_mode_cap = 'bypassPermissions' WHERE id = 'legacy-task'`); err != nil {
		t.Fatalf("bypassPermissions must be accepted on background_tasks after migration: %v", err)
	}

	// Widening must not mean removing. If the migration dropped the constraint
	// instead of extending it, the assertions above would still pass while any
	// value at all became storable.
	if _, err := raw.ExecContext(ctx, `UPDATE runs SET permission_mode_cap = 'root' WHERE id = 'legacy-run'`); err == nil {
		t.Fatal("the runs cap constraint was dropped rather than widened: an unknown mode was accepted")
	} else if !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("unexpected rejection reason for an unknown runs cap: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE background_tasks SET permission_mode_cap = 'root' WHERE id = 'legacy-task'`); err == nil {
		t.Fatal("the background_tasks cap constraint was dropped rather than widened: an unknown mode was accepted")
	} else if !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("unexpected rejection reason for an unknown task cap: %v", err)
	}
}
