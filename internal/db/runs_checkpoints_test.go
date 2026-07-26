package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMigrationV17SeparatesQueuedRunAndToolLifecycleTimes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `
PRAGMA foreign_keys = OFF;
DROP TABLE runs;
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  trigger_message_id TEXT REFERENCES agent_messages(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'running',
  started_at TEXT NOT NULL,
  completed_at TEXT,
  error_message TEXT,
  base_head TEXT,
  end_head TEXT,
  checkpoint_repo_root TEXT,
  git_snapshot_at TEXT,
  checkpoint_state TEXT NOT NULL DEFAULT 'none',
  checkpoint_error TEXT,
  rolled_back_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_runs_agent_started ON runs(agent_id, started_at DESC);
CREATE INDEX idx_runs_status ON runs(status);
-- A v16 database predates both these columns and any index over them, so the
-- index has to go first; SQLite refuses to drop a column an index references.
DROP INDEX IF EXISTS idx_tool_calls_run_updated;
ALTER TABLE agent_tool_calls DROP COLUMN started_at;
ALTER TABLE agent_tool_calls DROP COLUMN completed_at;
ALTER TABLE agent_tool_calls DROP COLUMN updated_at;
PRAGMA foreign_keys = ON;
`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	now := Now()
	if _, err := raw.ExecContext(ctx, `INSERT INTO agents (id, type, title, model, permission_mode, status, created_at, updated_at) VALUES ('agent-v16', 'primary', 'v16', 'fake:test', 'acceptEdits', 'idle', ?, ?)`, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, started_at, created_at, updated_at) VALUES ('queued-v16', 'agent-v16', 'pending', ?, ?, ?), ('running-v16', 'agent-v16', 'running', ?, ?, ?)`, now, now, now, now, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO agent_tool_calls (id, agent_id, tool_use_id, tool_name, status, created_at) VALUES ('pending-tool', 'agent-v16', 'pending-tool', 'Bash', 'pending_approval', ?), ('completed-tool', 'agent-v16', 'completed-tool', 'Read', 'completed', ?)`, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 16`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected current migration version %d, got %d", CurrentDBVersion, version)
	}
	if !testColumnExists(t, ctx, store.DB(), "agent_tool_calls", "started_at") || !testColumnExists(t, ctx, store.DB(), "agent_tool_calls", "completed_at") || !testColumnExists(t, ctx, store.DB(), "agent_tool_calls", "updated_at") {
		t.Fatal("expected v17 tool lifecycle columns")
	}
	queued, err := store.GetRun(ctx, "agent-v16", "queued-v16")
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.GetRun(ctx, "agent-v16", "running-v16")
	if err != nil {
		t.Fatal(err)
	}
	if queued.StartedAt != "" || running.StartedAt == "" {
		t.Fatalf("expected queued run to lose synthetic start and running run to retain it: queued=%+v running=%+v", queued, running)
	}
	pendingTool, err := store.GetToolCallByUseID(ctx, "agent-v16", "pending-tool")
	if err != nil {
		t.Fatal(err)
	}
	completedTool, err := store.GetToolCallByUseID(ctx, "agent-v16", "completed-tool")
	if err != nil {
		t.Fatal(err)
	}
	if pendingTool.StartedAt != "" || pendingTool.CompletedAt != "" || pendingTool.UpdatedAt == "" || completedTool.StartedAt == "" || completedTool.CompletedAt == "" {
		t.Fatalf("unexpected migrated tool timestamps: pending=%+v completed=%+v", pendingTool, completedTool)
	}
}

func TestOpenMigratesVersionOneDatabaseToRunTracking(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected migrated version %d, got %d", CurrentDBVersion, version)
	}
	for _, column := range []struct {
		table  string
		column string
	}{
		{"agent_messages", "run_id"},
		{"agent_tool_calls", "run_id"},
		{"api_requests", "run_id"},
	} {
		if !testColumnExists(t, ctx, store.DB(), column.table, column.column) {
			t.Fatalf("expected column %s.%s to exist after migration", column.table, column.column)
		}
	}
	if !testTableExists(t, ctx, store.DB(), "runs") {
		t.Fatal("expected runs table after migration")
	}
}

func TestOpenMigratesVersionFourDatabaseToRunScopedGitCheckpoints(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v4.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE run_git_changes`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `ALTER TABLE runs DROP COLUMN checkpoint_repo_root`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `ALTER TABLE runs DROP COLUMN git_snapshot_at`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected migrated version %d, got %d", CurrentDBVersion, version)
	}
	if !testTableExists(t, ctx, store.DB(), "run_git_changes") {
		t.Fatal("expected run_git_changes table after migration")
	}
	for _, column := range []string{"checkpoint_repo_root", "git_snapshot_at"} {
		exists, err := columnExists(ctx, store.DB(), "runs", column)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected runs.%s after migration", column)
		}
	}
}

func TestRunCheckpointTransitionsRejectIllegalStatesAndPreserveRolledBackAudit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, run.ID); err == nil {
		t.Fatal("expected capturing transition from none to fail")
	}
	if err := store.BeginRunGitCheckpoint(ctx, run.ID, "base", "/repo"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRunGitCheckpointChanges(ctx, run.ID, []RunGitChange{{Path: "failed-rollback.txt", IndexStatus: " ", WorktreeStatus: "M", WorktreeFingerprint: "fingerprint"}}); err != nil {
		t.Fatal(err)
	}
	ready, err := store.FinalizeRunGitCheckpoint(ctx, run.ID, "base")
	if err != nil || !ready {
		t.Fatalf("expected ready checkpoint, ready=%v err=%v", ready, err)
	}
	if err := store.MarkRunGitCheckpointRolledBack(ctx, run.ID); err == nil {
		t.Fatal("expected rolled_back transition without claim to fail")
	}
	if err := store.ClaimRunGitRollback(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FailRunGitRollback(ctx, run.ID, "test failure"); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimRunGitRollback(ctx, run.ID); err == nil {
		t.Fatal("expected invalid checkpoint rollback claim to fail")
	}
	failedRollbackChanges, err := store.ListRunGitChanges(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(failedRollbackChanges) != 1 || failedRollbackChanges[0].Path != "failed-rollback.txt" {
		t.Fatalf("rollback failure should preserve checkpoint audit changes, got %+v", failedRollbackChanges)
	}

	auditRun, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRunGitCheckpoint(ctx, auditRun.ID, "base", "/repo"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, auditRun.ID); err != nil {
		t.Fatal(err)
	}
	change := RunGitChange{Path: "owned.txt", IndexStatus: " ", WorktreeStatus: "M", WorktreeFingerprint: "fingerprint"}
	if err := store.ReplaceRunGitCheckpointChanges(ctx, auditRun.ID, []RunGitChange{change}); err != nil {
		t.Fatal(err)
	}
	ready, err = store.FinalizeRunGitCheckpoint(ctx, auditRun.ID, "base")
	if err != nil || !ready {
		t.Fatalf("expected ready checkpoint, ready=%v err=%v", ready, err)
	}
	if err := store.ClaimRunGitRollback(ctx, auditRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunGitCheckpointRolledBack(ctx, auditRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.InvalidateRunGitCheckpoint(ctx, auditRun.ID, "must not change rolled back checkpoint"); err == nil {
		t.Fatal("expected rolled_back checkpoint invalidation to fail")
	}
	stored, err := store.GetRunByID(ctx, auditRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := store.ListRunGitChanges(ctx, auditRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CheckpointState != RunCheckpointRolledBack || len(changes) != 1 || changes[0].Path != "owned.txt" {
		t.Fatalf("rolled_back checkpoint audit was mutated: run=%+v changes=%+v", stored, changes)
	}
	if err := store.InvalidateRunGitCheckpoint(ctx, "missing-run", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing invalidation to return sql.ErrNoRows, got %v", err)
	}
}

func TestOpenMigratesVersionFiveCheckpointLifecycleAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v5.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"checkpoint_state", "checkpoint_error", "rolled_back_at"} {
		if _, err := raw.ExecContext(ctx, `ALTER TABLE runs DROP COLUMN `+column); err != nil {
			t.Fatal(err)
		}
	}
	now := Now()
	if _, err := raw.ExecContext(ctx, `INSERT INTO agents (id, type, title, model, permission_mode, status, created_at, updated_at) VALUES ('agent-v5', 'primary', 'v5', 'fake:test', 'acceptEdits', 'idle', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, started_at, base_head, checkpoint_repo_root, git_snapshot_at, created_at, updated_at) VALUES ('ready-v5', 'agent-v5', 'completed', ?, 'abc', '/repo', ?, ?, ?)`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, started_at, created_at, updated_at) VALUES ('none-v5', 'agent-v5', 'completed', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := store.GetRunByID(ctx, "ready-v5")
	if err != nil {
		t.Fatal(err)
	}
	none, err := store.GetRunByID(ctx, "none-v5")
	if err != nil {
		t.Fatal(err)
	}
	if ready.CheckpointState != RunCheckpointReady || none.CheckpointState != RunCheckpointNone {
		t.Fatalf("unexpected v5 checkpoint migration: ready=%+v none=%+v", ready, none)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected idempotent v6 migration, got version %d", version)
	}
	ready, err = store.GetRunByID(ctx, "ready-v5")
	if err != nil || ready.CheckpointState != RunCheckpointReady {
		t.Fatalf("expected preserved ready checkpoint after reopen, run=%+v err=%v", ready, err)
	}
}

func TestOpenMigratesVersionSixRollingBackCheckpointToInvalid(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	now := Now()
	if _, err := raw.ExecContext(ctx, `INSERT INTO agents (id, type, title, model, permission_mode, status, created_at, updated_at) VALUES ('agent-v6', 'primary', 'v6', 'fake:test', 'acceptEdits', 'running', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, started_at, checkpoint_state, created_at, updated_at) VALUES ('rolling-v6', 'agent-v6', 'running', ?, 'rolling_back', ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 6`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := store.GetRunByID(ctx, "rolling-v6")
	if err != nil {
		t.Fatal(err)
	}
	if run.CheckpointState != RunCheckpointInvalid || !strings.Contains(run.CheckpointError, "process restarted") {
		t.Fatalf("expected v7 migration to invalidate rolling checkpoint, got %+v", run)
	}
}

func TestRunStatusTransitionsAreCASProtected(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Runs", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	pendingCannotComplete, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, pendingCannotComplete.ID, "completed", ""); !IsConflict(err) {
		t.Fatalf("pending run must not complete before starting, got %v", err)
	}
	if err := store.UpdateRunStatus(ctx, pendingCannotComplete.ID, "pending", ""); err == nil {
		t.Fatal("invalid non-terminal target must be rejected")
	}
	if err := store.CompleteRun(ctx, pendingCannotComplete.ID, "unknown", ""); err == nil {
		t.Fatal("invalid terminal target must be rejected")
	}

	pendingInterrupted, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, pendingInterrupted.ID, "interrupted", "cancelled before start"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, pendingInterrupted.ID, "error", "late error"); !IsConflict(err) {
		t.Fatalf("interrupted pending run must remain terminal, got %v", err)
	}

	pendingSuperseded, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, pendingSuperseded.ID, "superseded", ""); err != nil {
		t.Fatal(err)
	}

	pending, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunStatus(ctx, pending.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, pending.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, pending.ID, "interrupted", ""); !IsConflict(err) {
		t.Fatalf("terminal run must not be overwritten, got %v", err)
	}
	if err := store.UpdateRunStatus(ctx, pending.ID, "running", ""); !IsConflict(err) {
		t.Fatalf("duplicate start must conflict, got %v", err)
	}
	if err := store.UpdateRunStatus(ctx, "missing", "running", ""); !IsNotFound(err) {
		t.Fatalf("missing run must be identifiable, got %v", err)
	}

	running, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- store.CompleteRun(ctx, running.ID, "completed", "") }()
	go func() { defer wg.Done(); errs <- store.CompleteRun(ctx, running.ID, "interrupted", "manual") }()
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if IsConflict(err) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent terminal result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected exactly one terminal winner, successes=%d conflicts=%d", successes, conflicts)
	}
}
