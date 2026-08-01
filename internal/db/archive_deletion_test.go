package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func newArchiveDeletionStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func archiveProject(t *testing.T, store *Store, id string) {
	t.Helper()
	archived := true
	if _, err := store.UpdateProjectNavigationState(context.Background(), id, nil, &archived); err != nil {
		t.Fatal(err)
	}
}

func archiveAgent(t *testing.T, store *Store, id string) {
	t.Helper()
	archived := true
	if _, err := store.UpdateAgentNavigationState(context.Background(), id, nil, &archived); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, store *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := store.DB().QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestDeleteArchivedProjectRequiresArchiveFirst(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	project, _, _, err := store.CreateProject(ctx, "Live", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	err = store.DeleteArchivedProject(ctx, project.ID)
	if !IsNotArchived(err) {
		t.Fatalf("expected ErrNotArchived, got %v", err)
	}
	if _, err := store.GetProject(ctx, project.ID); err != nil {
		t.Fatalf("project must survive a refused delete: %v", err)
	}
}

func TestDeleteArchivedProjectRemovesWorklinesAndAgents(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	project, workline, agent, err := store.CreateProject(ctx, "Doomed", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	archiveProject(t, store, project.ID)

	if err := store.DeleteArchivedProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProject(ctx, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected project gone, got %v", err)
	}
	// agents.workline_id is ON DELETE SET NULL, so an orphan agent row here would
	// mean the delete silently leaked a conversation.
	if got := countRows(t, store, `SELECT COUNT(*) FROM agents WHERE id = ?`, agent.ID); got != 0 {
		t.Fatalf("expected agent removed, found %d", got)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM worklines WHERE id = ?`, workline.ID); got != 0 {
		t.Fatalf("expected workline removed, found %d", got)
	}
}

func TestDeleteArchivedProjectClearsMessageForeignKeys(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	project, _, agent, err := store.CreateProject(ctx, "Corrected", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "original"})
	if err != nil {
		t.Fatal(err)
	}
	correction, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE agent_messages SET correction_of_message_id = ? WHERE id = ?`, source.ID, correction.ID); err != nil {
		t.Fatal(err)
	}
	archiveProject(t, store, project.ID)

	if err := store.DeleteArchivedProject(ctx, project.ID); err != nil {
		t.Fatalf("archived project with correction messages must be deletable: %v", err)
	}
}

func TestDeleteArchivedProjectClearsToolResultMessageForeignKeys(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	project, _, agent, err := store.CreateProject(ctx, "Tool results", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, ToolExecutionGroupSchemaSQL()); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "tool call"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.AddMessage(ctx, Message{AgentID: agent.ID, RunID: run.ID, Role: "user", ContentText: "tool result"})
	if err != nil {
		t.Fatal(err)
	}
	groupID := NewID()
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_execution_groups (id, run_id, assistant_message_id, expected_count, status, created_at, updated_at) VALUES (?, ?, ?, 1, 'settled', ?, ?)`, groupID, run.ID, assistant.ID, Now(), Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_execution_group_items (group_id, tool_use_id, tool_name, ordinal, status, result_message_id, created_at, updated_at) VALUES (?, 'tool-1', 'Read', 0, 'completed', ?, ?, ?)`, groupID, result.ID, Now(), Now()); err != nil {
		t.Fatal(err)
	}
	archiveProject(t, store, project.ID)

	if err := store.DeleteArchivedProject(ctx, project.ID); err != nil {
		t.Fatalf("archived project with tool result messages must be deletable: %v", err)
	}
}

func TestDeleteArchivedProjectBlockedByActiveRun(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	project, _, agent, err := store.CreateProject(ctx, "Busy", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	archiveProject(t, store, project.ID)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, created_at, updated_at) VALUES (?, ?, 'running', ?, ?)`, NewID(), agent.ID, Now(), Now()); err != nil {
		t.Fatal(err)
	}

	err = store.DeleteArchivedProject(ctx, project.ID)
	if !HasActiveRun(err) {
		t.Fatalf("expected ErrHasActiveRun, got %v", err)
	}
	if _, err := store.GetProject(ctx, project.ID); err != nil {
		t.Fatalf("project must survive a refused delete: %v", err)
	}
}

func TestDeleteArchivedAgentRequiresArchiveFirst(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	_, _, agent, err := store.CreateProject(ctx, "Live", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	err = store.DeleteArchivedAgent(ctx, agent.ID)
	if !IsNotArchived(err) {
		t.Fatalf("expected ErrNotArchived, got %v", err)
	}
	if _, err := store.GetAgent(ctx, agent.ID); err != nil {
		t.Fatalf("agent must survive a refused delete: %v", err)
	}
}

func TestDeleteArchivedAgentKeepsProjectWithSiblings(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	project, _, agent, err := store.CreateProject(ctx, "Workspace", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	archiveAgent(t, store, agent.ID)

	if err := store.DeleteArchivedAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM agents WHERE id = ?`, agent.ID); got != 0 {
		t.Fatalf("expected agent removed, found %d", got)
	}
	// A workspace project is user-created: it must stay even when left empty.
	if _, err := store.GetProject(ctx, project.ID); err != nil {
		t.Fatalf("workspace project must survive: %v", err)
	}
}

func TestDeleteArchivedAgentCleansUpStandaloneShell(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	project, _, agent, err := store.CreateStandaloneConversation(ctx, "Chat", "openai-compatible:test")
	if err != nil {
		t.Fatal(err)
	}
	archiveAgent(t, store, agent.ID)

	if err := store.DeleteArchivedAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	// The auto-created conversation project has no purpose once empty.
	if _, err := store.GetProject(ctx, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected standalone shell project removed, got %v", err)
	}
}

func TestDeleteArchivedAgentBlockedByActiveRun(t *testing.T) {
	store := newArchiveDeletionStore(t)
	ctx := context.Background()
	_, _, agent, err := store.CreateProject(ctx, "Busy", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	archiveAgent(t, store, agent.ID)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, created_at, updated_at) VALUES (?, ?, 'continuation_pending', ?, ?)`, NewID(), agent.ID, Now(), Now()); err != nil {
		t.Fatal(err)
	}

	err = store.DeleteArchivedAgent(ctx, agent.ID)
	if !HasActiveRun(err) {
		t.Fatalf("expected ErrHasActiveRun, got %v", err)
	}
	if _, err := store.GetAgent(ctx, agent.ID); err != nil {
		t.Fatalf("agent must survive a refused delete: %v", err)
	}
}

func TestDeleteArchivedProjectMissingID(t *testing.T) {
	store := newArchiveDeletionStore(t)
	if err := store.DeleteArchivedProject(context.Background(), "does-not-exist"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}
