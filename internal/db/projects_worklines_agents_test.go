package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateProjectCreatesCoreRecords(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, workline, agent, err := store.CreateProject(context.Background(), "Demo", "desc", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if project.ID == "" || workline.ID == "" || agent.ID == "" {
		t.Fatal("expected ids")
	}
	got, err := store.GetAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorklineID != workline.ID {
		t.Fatalf("expected agent workline %s, got %s", workline.ID, got.WorklineID)
	}
	if !got.PlanReflection {
		t.Fatalf("new agents must default plan reflection on: %+v", got)
	}
	disabled, err := store.UpdateAgentPlanReflection(context.Background(), got.ID, false)
	if err != nil || disabled.PlanReflection {
		t.Fatalf("plan reflection did not turn off: %+v err=%v", disabled, err)
	}
	if disabled.PermissionGeneration != got.PermissionGeneration {
		t.Fatalf("plan reflection must not bump permission generation: before=%+v after=%+v", got, disabled)
	}
	if disabled.EntityGeneration <= got.EntityGeneration {
		t.Fatalf("plan reflection must bump entity generation: before=%+v after=%+v", got, disabled)
	}
}

func TestUpdateAgentSummaryModelRoundTripsAndClears(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "summary-model.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai-compatible:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if agent.SummaryModel != "" {
		t.Fatalf("new agents must inherit the host summary model: %+v", agent)
	}
	updated, err := store.UpdateAgentSummaryModel(ctx, agent.ID, "compact:mini")
	if err != nil {
		t.Fatal(err)
	}
	if updated.SummaryModel != "compact:mini" {
		t.Fatalf("summary model did not persist: %+v", updated)
	}
	if updated.EntityGeneration <= agent.EntityGeneration {
		t.Fatalf("summary model must bump entity generation: before=%+v after=%+v", agent, updated)
	}
	cleared, err := store.UpdateAgentSummaryModel(ctx, agent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.SummaryModel != "" {
		t.Fatalf("empty summary model must clear the override: %+v", cleared)
	}
	if _, err := store.UpdateAgentSummaryModel(ctx, agent.ID, strings.Repeat("x", 300)); err == nil {
		t.Fatal("expected oversized summary model to be rejected")
	}
}

func TestCreateAgentRejectsOrphanSubagentAndParentedPrimary(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, workline, parent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(ctx, Agent{WorklineID: workline.ID, Type: "subagent", Title: "orphan", Model: "fake:test", CWD: parent.CWD}); err == nil {
		t.Fatal("expected Type=subagent without a parent to be rejected")
	}
	if _, err := store.CreateAgent(ctx, Agent{WorklineID: workline.ID, Type: "primary", ParentAgentID: parent.ID, Title: "parented primary", Model: "fake:test", CWD: parent.CWD}); err == nil {
		t.Fatal("expected Type=primary with a parent to be rejected")
	}
	child, err := store.CreateAgent(ctx, Agent{WorklineID: workline.ID, ParentAgentID: parent.ID, Title: "inferred child", Model: "fake:test", CWD: parent.CWD})
	if err != nil {
		t.Fatal(err)
	}
	if child.Type != "subagent" || child.ParentAgentID != parent.ID {
		t.Fatalf("parent without type should persist as subagent, got %+v", child)
	}
}

func TestUpdateAgentContextSummaryRoundTrips(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAgentContextSummary(ctx, agent.ID, "summary text", "message-1", 42); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextSummary != "summary text" || got.PruneBoundaryMessageID != "message-1" || got.PrunedPercent != 42 {
		t.Fatalf("unexpected context summary round trip: %+v", got)
	}
}

func TestAgentContextPreferencesAndLogicalClear(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "first"})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "latest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAgentContextSummary(ctx, agent.ID, "summary", first.ID, 50); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.PruneEnabled {
		t.Fatal("summary updates must not force prune_enabled")
	}
	current, err = store.UpdateAgentPruneEnabled(ctx, agent.ID, true)
	if err != nil || !current.PruneEnabled {
		t.Fatalf("failed to enable pruning: %+v %v", current, err)
	}
	cleared, err := store.ClearAgentContext(ctx, agent.ID, current.EntityGeneration, latest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ContextSummary != "" || cleared.PruneBoundaryMessageID != latest.ID || cleared.PrunedPercent != 100 || !cleared.PruneEnabled {
		t.Fatalf("unexpected logical clear result: %+v", cleared)
	}
	if _, err := store.ClearAgentContext(ctx, agent.ID, current.EntityGeneration, latest.ID); !IsConflict(err) {
		t.Fatalf("expected stale generation conflict, got %v", err)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("logical clear changed durable messages: %d %v", len(messages), err)
	}
}

func TestListProjectsReturnsEmptySlice(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if projects == nil {
		t.Fatal("expected empty slice, got nil")
	}
	if len(projects) != 0 {
		t.Fatalf("expected no projects, got %d", len(projects))
	}
}

func TestListNavigationConversationsJoinsAndSortsSafely(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, err = store.DB().ExecContext(ctx, `
INSERT INTO projects (id, name, status, flow_mode, git_path, created_at, updated_at) VALUES
  ('project-a', 'Alpha', 'active', 'workspace', '/repos/alpha', '2026-01-01T00:00:00Z', '2026-01-05T00:00:00Z'),
  ('project-b', 'Empty', 'active', 'workspace', '/repos/empty', '2026-01-01T00:00:00Z', '2026-01-04T00:00:00Z'),
  ('project-c', 'Deleted', 'deleted', 'workspace', '/repos/deleted', '2026-01-01T00:00:00Z', '2026-01-03T00:00:00Z'),
  ('project-d', 'Delta', 'active', 'workspace', '/repos/delta', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z');
INSERT INTO worklines (id, project_id, title, status, role, branch, is_root, created_at, updated_at) VALUES
  ('workline-a', 'project-a', 'main', 'active', 'root', NULL, 1, '2026-01-01T00:00:00Z', '2026-01-05T01:00:00Z'),
  ('workline-b', 'project-a', 'feature', 'active', 'worktree', 'feature/nav', 0, '2026-01-01T00:00:00Z', '2026-01-05T02:00:00Z'),
  ('workline-c', 'project-c', 'deleted-main', 'active', 'root', NULL, 1, '2026-01-01T00:00:00Z', '2026-01-03T01:00:00Z'),
  ('workline-d', 'project-d', 'main', 'active', 'root', NULL, 1, '2026-01-01T00:00:00Z', '2026-01-02T01:00:00Z');
INSERT INTO agents (id, workline_id, type, title, model, system_prompt, permission_mode, status, cwd, message_count, last_message_at, context_summary, error_message, created_at, updated_at) VALUES
  ('agent-z', 'workline-a', 'primary', 'Newest', 'model-z', 'SYSTEM_SECRET', 'acceptEdits', 'running', '/repos/alpha', 9, '2026-02-03T00:00:00Z', 'CONTEXT_SECRET', 'ERROR_SECRET', '2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z'),
  ('agent-a', 'workline-a', 'subagent', 'Fallback', 'model-a', 'SYSTEM_SECRET', 'plan', 'idle', '/repos/alpha/pkg', 4, NULL, 'CONTEXT_SECRET', 'ERROR_SECRET', '2026-01-01T00:00:00Z', '2026-02-02T00:00:00Z'),
  ('agent-b', 'workline-b', 'primary', 'Feature', 'model-b', 'SYSTEM_SECRET', 'acceptEdits', 'idle', '/repos/alpha-feature', 2, '2026-02-02T00:00:00Z', 'CONTEXT_SECRET', 'ERROR_SECRET', '2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z'),
  ('agent-c', 'workline-c', 'primary', 'Deleted', 'model-c', 'SYSTEM_SECRET', 'acceptEdits', 'idle', '/repos/deleted', 1, '2026-02-04T00:00:00Z', 'CONTEXT_SECRET', 'ERROR_SECRET', '2026-01-01T00:00:00Z', '2026-02-04T00:00:00Z'),
  ('agent-d', 'workline-d', 'primary', 'Delta', 'model-d', 'SYSTEM_SECRET', 'default', 'idle', '/repos/delta', 3, '2026-02-02T00:00:00Z', 'CONTEXT_SECRET', 'ERROR_SECRET', '2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}

	conversations, err := store.ListNavigationConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"agent-z", "agent-a", "agent-b", "agent-d"}
	if len(conversations) != len(wantOrder) {
		t.Fatalf("expected %d active-project conversations, got %+v", len(wantOrder), conversations)
	}
	for index, agentID := range wantOrder {
		if conversations[index].AgentID != agentID {
			t.Fatalf("unexpected conversation order at %d: want %s, got %+v", index, agentID, conversations)
		}
	}
	fallback := conversations[1]
	if fallback.LastActivityAt != "2026-02-02T00:00:00Z" {
		t.Fatalf("expected updated_at fallback for missing last_message_at, got %+v", fallback)
	}
	feature := conversations[2]
	if feature.ProjectID != "project-a" || feature.ProjectName != "Alpha" || feature.ProjectPath != "/repos/alpha" || feature.ProjectUpdatedAt != "2026-01-05T00:00:00Z" || feature.WorklineID != "workline-b" || feature.WorklineTitle != "feature" || feature.WorklineRole != "worktree" || feature.WorklineBranch != "feature/nav" || feature.WorklineUpdatedAt != "2026-01-05T02:00:00Z" || feature.AgentTitle != "Feature" || feature.AgentType != "primary" || feature.AgentStatus != "idle" || feature.Model != "model-b" || feature.PermissionMode != "acceptEdits" || feature.CWD != "/repos/alpha-feature" || feature.MessageCount != 2 {
		t.Fatalf("unexpected flattened navigation DTO: %+v", feature)
	}
}

func TestNavigationStatePinsOrdersAndArchivesWithoutDeletingRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "navigation-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, workline, first, err := store.CreateProject(ctx, "Navigation", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateAgent(ctx, Agent{WorklineID: workline.ID, Type: "primary", Title: "Second", Model: "fake:test", PermissionMode: "acceptEdits", Status: "idle", CWD: project.GitPath})
	if err != nil {
		t.Fatal(err)
	}

	pinned := true
	updatedAgent, err := store.UpdateAgentNavigationState(ctx, first.ID, &pinned, nil)
	if err != nil || !updatedAgent.Pinned {
		t.Fatalf("pin agent: agent=%+v err=%v", updatedAgent, err)
	}
	conversations, err := store.ListNavigationConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 || conversations[0].AgentID != first.ID || !conversations[0].AgentPinned {
		t.Fatalf("expected pinned conversation first, got %+v", conversations)
	}

	archived := true
	updatedAgent, err = store.UpdateAgentNavigationState(ctx, first.ID, nil, &archived)
	if err != nil || updatedAgent.ArchivedAt == "" {
		t.Fatalf("archive agent: agent=%+v err=%v", updatedAgent, err)
	}
	conversations, err = store.ListNavigationConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 || conversations[0].AgentID != second.ID {
		t.Fatalf("archived conversation should be hidden by default, got %+v", conversations)
	}
	allConversations, err := store.ListNavigationConversationsWithOptions(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(allConversations) != 2 || allConversations[1].AgentID != first.ID || allConversations[1].AgentArchivedAt == "" {
		t.Fatalf("includeArchived should retain the archived conversation, got %+v", allConversations)
	}

	updatedProject, err := store.UpdateProjectNavigationState(ctx, project.ID, &pinned, &archived)
	if err != nil || !updatedProject.Pinned || updatedProject.ArchivedAt == "" {
		t.Fatalf("archive project: project=%+v err=%v", updatedProject, err)
	}
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("archived project should be hidden by default, got %+v", projects)
	}
	allProjects, err := store.ListProjectsWithOptions(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(allProjects) != 1 || allProjects[0].ID != project.ID || !allProjects[0].Pinned || allProjects[0].ArchivedAt == "" {
		t.Fatalf("includeArchived should retain the project state, got %+v", allProjects)
	}
	allConversations, err = store.ListNavigationConversationsWithOptions(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(allConversations) != 2 || allConversations[0].ProjectArchivedAt == "" || allConversations[1].ProjectArchivedAt == "" {
		t.Fatalf("project archive state should flow into conversations, got %+v", allConversations)
	}

	restored := false
	if _, err := store.UpdateProjectNavigationState(ctx, project.ID, nil, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgentNavigationState(ctx, first.ID, nil, &restored); err != nil {
		t.Fatal(err)
	}
	if records, err := store.ListNavigationConversations(ctx); err != nil || len(records) != 2 {
		t.Fatalf("restored records should return without data loss: records=%+v err=%v", records, err)
	}
	if _, err := store.UpdateProjectNavigationState(ctx, "missing", &pinned, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing project should return not found, got %v", err)
	}
	if _, err := store.UpdateAgentNavigationState(ctx, first.ID, nil, nil); err == nil {
		t.Fatal("empty navigation patch should fail")
	}
}

func TestNavigationStateMigrationAddsColumnsAndIndexes(t *testing.T) {
	ctx := context.Background()
	raw := openRawDB(t, filepath.Join(t.TempDir(), "navigation-state-v42.db"))
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `
CREATE TABLE projects (id TEXT PRIMARY KEY, updated_at TEXT NOT NULL);
CREATE TABLE agents (id TEXT PRIMARY KEY, updated_at TEXT NOT NULL);
INSERT INTO projects (id, updated_at) VALUES ('project', '2026-01-01T00:00:00Z');
INSERT INTO agents (id, updated_at) VALUES ('agent', '2026-01-01T00:00:00Z');
PRAGMA user_version = 42;
`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if version := readUserVersion(t, ctx, raw); version != CurrentDBVersion {
		t.Fatalf("expected migrated version %d, got %d", CurrentDBVersion, version)
	}
	for _, column := range []struct{ table, name string }{
		{"projects", "pinned"}, {"projects", "archived_at"}, {"agents", "pinned"}, {"agents", "archived_at"},
	} {
		exists, err := columnExists(ctx, raw, column.table, column.name)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("expected %s.%s after migration", column.table, column.name)
		}
	}
	for _, index := range []string{"idx_projects_navigation_state", "idx_agents_navigation_state"} {
		var count int
		if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected navigation index %s", index)
		}
	}
	var projectPinned, agentPinned int
	var projectArchived, agentArchived sql.NullString
	if err := raw.QueryRowContext(ctx, `SELECT pinned, archived_at FROM projects WHERE id = 'project'`).Scan(&projectPinned, &projectArchived); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT pinned, archived_at FROM agents WHERE id = 'agent'`).Scan(&agentPinned, &agentArchived); err != nil {
		t.Fatal(err)
	}
	if projectPinned != 0 || agentPinned != 0 || projectArchived.Valid || agentArchived.Valid {
		t.Fatalf("migration should preserve active unpinned defaults: project=(%d,%v) agent=(%d,%v)", projectPinned, projectArchived, agentPinned, agentArchived)
	}
}

func TestOpenMigratesLegacyDatabaseMissingAgentColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw := openRawDB(t, path)
	_, err := raw.ExecContext(ctx, `
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  flow_mode TEXT NOT NULL DEFAULT 'workspace',
  git_path TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE chapters (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  role TEXT NOT NULL DEFAULT 'root',
  worktree_path TEXT,
  is_root INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE narrators (
  id TEXT PRIMARY KEY,
  chapter_id TEXT,
  type TEXT NOT NULL DEFAULT 'primary',
  title TEXT NOT NULL,
  model TEXT NOT NULL,
  permission_mode TEXT NOT NULL DEFAULT 'acceptEdits',
  status TEXT NOT NULL DEFAULT 'idle',
  plan_mode INTEGER NOT NULL DEFAULT 0,
  cwd TEXT,
  message_count INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO projects (id, name, description, status, flow_mode, git_path, created_at, updated_at)
VALUES ('project-1', 'Legacy', '', 'active', 'workspace', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
INSERT INTO chapters (id, project_id, title, status, role, worktree_path, is_root, created_at, updated_at)
VALUES ('chapter-1', 'project-1', 'main', 'active', 'root', '', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
INSERT INTO narrators (id, chapter_id, type, title, model, permission_mode, status, plan_mode, cwd, message_count, created_at, updated_at)
VALUES ('agent-1', 'chapter-1', 'primary', 'Legacy', 'openai:test', 'acceptEdits', 'idle', 0, '', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`)
	if err != nil {
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
		{"agents", "subagent_type"},
		{"agents", "context_summary"},
		{"agents", "prune_boundary_message_id"},
		{"agents", "pruned_percent"},
		{"agents", "prune_enabled"},
		{"agents", "parent_agent_id"},
		{"agents", "plan_reflection"},
		{"agents", "summary_model"},
		{"api_requests", "ttft_ms"},
		{"memory_injections", "agent_id"},
	} {
		if !testColumnExists(t, ctx, store.DB(), column.table, column.column) {
			t.Fatalf("expected column %s.%s to exist after migration", column.table, column.column)
		}
	}
	agent, err := store.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Title != "Legacy" || agent.PruneBoundaryMessageID != "" || agent.PrunedPercent != 0 || !agent.PlanReflection {
		t.Fatalf("unexpected migrated agent: %+v", agent)
	}
}

func TestCreateProjectConversationReusesProjectAndCreatesDistinctRootHierarchy(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "project-conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, firstWorkline, firstAgent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:first", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, secondWorkline, secondAgent, err := store.CreateProjectConversation(ctx, project.ID, "", "fake:second", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if secondProject.ID != project.ID || secondWorkline.ID == firstWorkline.ID || secondAgent.ID == firstAgent.ID {
		t.Fatalf("conversation did not reuse project with distinct hierarchy: project=%+v workline=%+v agent=%+v", secondProject, secondWorkline, secondAgent)
	}
	if secondWorkline.ProjectID != project.ID || !secondWorkline.IsRoot || secondWorkline.Role != "root" || secondAgent.WorklineID != secondWorkline.ID || secondAgent.Type != "primary" {
		t.Fatalf("unexpected second root hierarchy: workline=%+v agent=%+v", secondWorkline, secondAgent)
	}
	if secondWorkline.Title == firstWorkline.Title || secondAgent.Title == firstAgent.Title {
		t.Fatalf("default titles did not distinguish conversations: first=%q second=%q", firstWorkline.Title, secondWorkline.Title)
	}
	conversations, err := store.ListNavigationConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 {
		t.Fatalf("expected two navigable conversations, got %d: %+v", len(conversations), conversations)
	}
}

func TestCreateWorklineConversationAddsPrimaryOnExistingWorkline(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workline-conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, workline, first, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:first", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, sameWorkline, second, err := store.CreateWorklineConversation(ctx, workline.ID, "Follow-up", "fake:second", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if sameWorkline.ID != workline.ID || second.WorklineID != workline.ID || second.Type != "primary" || second.ParentAgentID != "" {
		t.Fatalf("expected another primary on the same workline: workline=%+v agent=%+v", sameWorkline, second)
	}
	if second.ID == first.ID || second.Title != "Follow-up" || second.CWD != project.GitPath {
		t.Fatalf("unexpected second conversation: %+v", second)
	}
	_, _, duplicateTitle, err := store.CreateWorklineConversation(ctx, workline.ID, "Follow-up", "fake:third", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if duplicateTitle.Title != "Follow-up (2)" {
		t.Fatalf("expected uniquified title, got %q", duplicateTitle.Title)
	}
	conversations, err := store.ListNavigationConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 3 {
		t.Fatalf("expected three navigable conversations, got %d: %+v", len(conversations), conversations)
	}
}
