package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const CurrentDBVersion = 54

type migration struct {
	version int
	name    string
	up      func(context.Context, *sql.Tx) error
}

var migrations = []migration{
	{version: 1, name: "baseline schema", up: migrateV1Baseline},
	{version: 2, name: "run tracking", up: migrateV2RunTracking},
	{version: 3, name: "notification settings", up: migrateV3NotificationSettings},
	{version: 4, name: "workflow permissions", up: migrateV4WorkflowPermissions},
	{version: 5, name: "run scoped git checkpoints", up: migrateV5RunScopedGitCheckpoints},
	{version: 6, name: "durable run git checkpoints", up: migrateV6DurableRunGitCheckpoints},
	{version: 7, name: "rollback checkpoint recovery", up: migrateV7RollbackCheckpointRecovery},
	{version: 8, name: "server skills", up: migrateV8ServerSkills},
	{version: 9, name: "skill risk acknowledgement hardening", up: migrateV9SkillRiskAcknowledgementHardening},
	{version: 10, name: "skill acknowledgement content binding", up: migrateV10SkillAcknowledgementContentBinding},
	{version: 11, name: "skill scanner versions", up: migrateV11SkillScannerVersions},
	{version: 12, name: "skill audit events", up: migrateV12SkillAuditEvents},
	{version: 13, name: "agent and workline naming", up: migrateV13AgentWorklineNaming},
	{version: 14, name: "agent stream and permission generations", up: migrateV14AgentStreamGenerations},
	{version: 15, name: "scoped skill revisions", up: migrateV15ScopedSkillRevisions},
	{version: 16, name: "automation audit events", up: migrateV16AutomationAuditEvents},
	{version: 17, name: "integration connections", up: migrateV17IntegrationConnections},
	{version: 18, name: "memories", up: migrateV18Memories},
	{version: 19, name: "schedules and run sources", up: migrateV19Schedules},
	{version: 20, name: "notification deliveries", up: migrateV20NotificationDeliveries},
	{version: 21, name: "channel pairings events and cursors", up: migrateV21ChannelPersistence},
	{version: 22, name: "device action requests", up: migrateV22DeviceActionRequests},
	{version: 23, name: "execution generations and schedule modes", up: migrateV23ExecutionSchedule},
	{version: 24, name: "durable spec boards and goals", up: migrateV24SpecBoards},
	{version: 25, name: "model aggregates and runtime settings", up: migrateV25ModelClient},
	{version: 26, name: "execution generation recovery", up: migrateV26GenerationRecovery},
	{version: 27, name: "disabled remote execution scaffold", up: migrateV27RemoteExecutionScaffold},
	{version: 28, name: "provider account statistics", up: migrateV28ProviderAccountStats},
	{version: 29, name: "agent xhigh reasoning effort", up: migrateV29ReasoningEffortXHigh},
	{version: 30, name: "internal message provider state", up: migrateV30MessageProviderState},
	{version: 31, name: "run and tool call lifecycle timestamps", up: migrateV31RunToolCallLifecycle},
	{version: 32, name: "user handles and auth sessions", up: migrateV32UserHandlesAndAuthSessions},
	{version: 33, name: "per-user agent message drafts", up: migrateV33MessageDrafts},
	{version: 34, name: "immutable message corrections", up: migrateV34MessageCorrections},
	{version: 35, name: "project memberships", up: migrateV35ProjectMembers},
	{version: 36, name: "api request usage history indexes", up: migrateV36APIRequestUsageIndexes},
	{version: 37, name: "global plugins and tool snapshots", up: migrateV37Plugins},
	{version: 38, name: "durable plans and execution snapshots", up: migrateV38Plans},
	{version: 39, name: "durable background tasks and output", up: migrateV39BackgroundTasks},
	{version: 40, name: "run continuation metadata", up: migrateV40RunContinuations},
	{version: 41, name: "private api gateway", up: migrateV41PrivateAPIGateway},
	{version: 42, name: "provider api key secrets", up: migrateV42ProviderSecrets},
	{version: 43, name: "navigation archive and pin state", up: migrateV43NavigationState},
	{version: 44, name: "account preferences", up: migrateV44AccountPreferences},
	{version: 45, name: "OAuth app identities and sessions", up: migrateV45OAuthApp},
	{version: 46, name: "gateway account grants", up: migrateV46GatewayAccountGrants},
	{version: 47, name: "generated image disk metadata", up: migrateV47GeneratedImages},
	{version: 48, name: "account preferences setup version", up: migrateV48AccountPreferencesSetupVersion},
	{version: 49, name: "attachment model image metadata", up: migrateV49AttachmentModelImages},
	{version: 50, name: "tool execution settlement ledger", up: migrateV50ToolExecutionGroups},
	{version: 51, name: "profile configuration and lifecycle hooks", up: migrateV51ProfileConfiguration},
	{version: 52, name: "lexically sortable timestamps", up: migrateV52SortableTimestamps},
	{version: 53, name: "remote collaboration phase one", up: migrateV53RemoteCollaboration},
	{version: 54, name: "danger reflection preference", up: migrateV54DangerReflection},
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}

	version, err := getUserVersion(ctx, db)
	if err != nil {
		return err
	}
	if version > CurrentDBVersion {
		return fmt.Errorf("database version %d is newer than supported version %d", version, CurrentDBVersion)
	}

	if version == 0 {
		empty, err := databaseIsEmpty(ctx, db)
		if err != nil {
			return err
		}
		if empty {
			return runMigration(ctx, db, migration{version: CurrentDBVersion, name: "current schema", up: migrateV1Baseline})
		}
		if err := migrateLegacyZeroVersion(ctx, db); err != nil {
			return err
		}
		version = 1
	}

	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		if err := runMigration(ctx, db, m); err != nil {
			return err
		}
		version = m.version
	}
	return nil
}

func runMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d %s: %w", m.version, m.name, err)
	}
	defer tx.Rollback()

	if err := m.up(ctx, tx); err != nil {
		return fmt.Errorf("run migration %d %s: %w", m.version, m.name, err)
	}
	if err := setUserVersion(ctx, tx, m.version); err != nil {
		return fmt.Errorf("set database version %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d %s: %w", m.version, m.name, err)
	}
	return nil
}

func migrateV1Baseline(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, schemaSQL)
	return err
}

func migrateV2RunTracking(ctx context.Context, tx *sql.Tx) error {
	if exists, err := columnExists(ctx, tx, "runs", "agent_id"); err != nil {
		return err
	} else if exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  narrator_id TEXT NOT NULL REFERENCES narrators(id) ON DELETE CASCADE,
  trigger_message_id TEXT REFERENCES narrator_messages(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'running',
  started_at TEXT NOT NULL,
  completed_at TEXT,
  error_message TEXT,
  base_head TEXT,
  end_head TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_narrator_started ON runs(narrator_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);
`); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "narrator_messages", "run_id", "TEXT REFERENCES runs(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "narrator_tool_calls", "run_id", "TEXT REFERENCES runs(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "api_requests", "run_id", "TEXT REFERENCES runs(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_messages_run ON narrator_messages(run_id, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_calls_run ON narrator_tool_calls(run_id, created_at);
CREATE INDEX IF NOT EXISTS idx_api_requests_run ON api_requests(run_id, created_at);
`)
	return err
}

func migrateV3NotificationSettings(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS notification_settings (
  id TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL DEFAULT 0,
  webhook_url TEXT,
  notify_on_approval INTEGER NOT NULL DEFAULT 1,
  notify_on_done INTEGER NOT NULL DEFAULT 1,
  notify_on_error INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`)
	return err
}

func migrateV4WorkflowPermissions(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS workflow_preferences (
  id TEXT PRIMARY KEY,
  require_confirmation_for_exec INTEGER NOT NULL DEFAULT 1,
  require_confirmation_for_writes INTEGER NOT NULL DEFAULT 0,
  allow_read_only_by_default INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tool_permission_rules (
  id TEXT PRIMARY KEY,
  mode TEXT NOT NULL DEFAULT '*',
  tool_name TEXT NOT NULL DEFAULT '*',
  risk TEXT NOT NULL DEFAULT '*',
  decision TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  description TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tool_permission_rules_match ON tool_permission_rules(enabled, mode, tool_name, risk, priority);
`)
	return err
}

func migrateV5RunScopedGitCheckpoints(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumn(ctx, tx, "runs", "checkpoint_repo_root", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "runs", "git_snapshot_at", "TEXT"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS run_git_changes (
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  orig_path TEXT,
  index_status TEXT NOT NULL,
  worktree_status TEXT NOT NULL,
  untracked INTEGER NOT NULL DEFAULT 0,
  index_fingerprint TEXT,
  worktree_fingerprint TEXT NOT NULL,
  PRIMARY KEY (run_id, path)
);
CREATE INDEX IF NOT EXISTS idx_run_git_changes_run ON run_git_changes(run_id, path);
`)
	return err
}

func migrateV6DurableRunGitCheckpoints(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumn(ctx, tx, "runs", "checkpoint_state", "TEXT NOT NULL DEFAULT 'none'"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "runs", "checkpoint_error", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "runs", "rolled_back_at", "TEXT"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE runs
SET checkpoint_state = CASE
	WHEN COALESCE(git_snapshot_at, '') <> '' THEN 'ready'
	ELSE 'none'
END,
checkpoint_error = NULL,
rolled_back_at = NULL
WHERE COALESCE(checkpoint_state, '') IN ('', 'none')
`)
	return err
}

func migrateV7RollbackCheckpointRecovery(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
UPDATE runs
SET checkpoint_state = 'invalid',
checkpoint_error = COALESCE(NULLIF(checkpoint_error, ''), 'process restarted while rollback was in progress'),
rolled_back_at = NULL
WHERE checkpoint_state = 'rolling_back'
`)
	return err
}

func migrateV8ServerSkills(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS skills (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  command TEXT NOT NULL COLLATE NOCASE,
  description TEXT NOT NULL,
  prompt TEXT NOT NULL,
  source TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  scan_verdict TEXT NOT NULL,
  scan_findings_json TEXT NOT NULL DEFAULT '[]',
  risk_acknowledged_at TEXT,
  risk_acknowledged_by TEXT,
  risk_acknowledged_hash TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (source IN ('manual', 'local_migration', 'skill_md')),
  CHECK (scan_verdict IN ('safe', 'review', 'blocked')),
  CHECK (enabled IN (0, 1)),
  CHECK (NOT (scan_verdict = 'blocked' AND enabled = 1)),
  CHECK (NOT (scan_verdict = 'review' AND enabled = 1 AND (TRIM(COALESCE(risk_acknowledged_at, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = '' OR TRIM(COALESCE(risk_acknowledged_by, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = '' OR COALESCE(risk_acknowledged_hash, '') <> content_hash)))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_command_nocase ON skills(command COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_skills_enabled_command ON skills(enabled, command COLLATE NOCASE);
`)
	return err
}

func migrateV9SkillRiskAcknowledgementHardening(ctx context.Context, tx *sql.Tx) error {
	// V8 allowed whitespace-only acknowledgement values. Those records never had
	// a meaningful confirmation, so fail closed rather than preserving enabled.
	if _, err := tx.ExecContext(ctx, `
UPDATE skills
SET enabled = 0,
    risk_acknowledged_at = NULL,
    risk_acknowledged_by = NULL
WHERE scan_verdict = 'review'
  AND enabled = 1
  AND (TRIM(COALESCE(risk_acknowledged_at, '')) = '' OR TRIM(COALESCE(risk_acknowledged_by, '')) = '')
`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
CREATE TRIGGER IF NOT EXISTS skills_review_acknowledgement_insert
BEFORE INSERT ON skills
WHEN NEW.scan_verdict = 'review' AND NEW.enabled = 1
 AND (TRIM(COALESCE(NEW.risk_acknowledged_at, '')) = '' OR TRIM(COALESCE(NEW.risk_acknowledged_by, '')) = '')
BEGIN
  SELECT RAISE(ABORT, 'review skills require non-blank risk acknowledgement before enabling');
END;
CREATE TRIGGER IF NOT EXISTS skills_review_acknowledgement_update
BEFORE UPDATE OF scan_verdict, enabled, risk_acknowledged_at, risk_acknowledged_by ON skills
WHEN NEW.scan_verdict = 'review' AND NEW.enabled = 1
 AND (TRIM(COALESCE(NEW.risk_acknowledged_at, '')) = '' OR TRIM(COALESCE(NEW.risk_acknowledged_by, '')) = '')
BEGIN
  SELECT RAISE(ABORT, 'review skills require non-blank risk acknowledgement before enabling');
END;
`)
	return err
}

func migrateV10SkillAcknowledgementContentBinding(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumn(ctx, tx, "skills", "risk_acknowledged_hash", "TEXT"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE skills
SET enabled = 0,
    risk_acknowledged_at = NULL,
    risk_acknowledged_by = NULL,
    risk_acknowledged_hash = NULL
WHERE scan_verdict = 'review'
  AND enabled = 1
  AND (
    TRIM(COALESCE(risk_acknowledged_at, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = ''
    OR TRIM(COALESCE(risk_acknowledged_by, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = ''
  );
UPDATE skills
SET risk_acknowledged_hash = content_hash
WHERE scan_verdict = 'review' AND enabled = 1;
UPDATE skills
SET risk_acknowledged_at = NULL,
    risk_acknowledged_by = NULL,
    risk_acknowledged_hash = NULL
WHERE enabled = 0 OR scan_verdict <> 'review';
DROP TRIGGER IF EXISTS skills_review_acknowledgement_insert;
DROP TRIGGER IF EXISTS skills_review_acknowledgement_update;
CREATE TRIGGER skills_review_acknowledgement_insert
BEFORE INSERT ON skills
WHEN NEW.scan_verdict = 'review' AND NEW.enabled = 1
 AND (
   TRIM(COALESCE(NEW.risk_acknowledged_at, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = ''
   OR TRIM(COALESCE(NEW.risk_acknowledged_by, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = ''
   OR COALESCE(NEW.risk_acknowledged_hash, '') <> NEW.content_hash
 )
BEGIN
  SELECT RAISE(ABORT, 'review skills require risk acknowledgement for the current content before enabling');
END;
CREATE TRIGGER skills_review_acknowledgement_update
BEFORE UPDATE ON skills
WHEN NEW.scan_verdict = 'review' AND NEW.enabled = 1
 AND (
   TRIM(COALESCE(NEW.risk_acknowledged_at, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = ''
   OR TRIM(COALESCE(NEW.risk_acknowledged_by, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = ''
   OR COALESCE(NEW.risk_acknowledged_hash, '') <> NEW.content_hash
 )
BEGIN
  SELECT RAISE(ABORT, 'review skills require risk acknowledgement for the current content before enabling');
END;
`)
	return err
}

func migrateV11SkillScannerVersions(ctx context.Context, tx *sql.Tx) error {
	// DEFAULT 0 deliberately marks all pre-versioned rows for one fail-closed
	// revalidation pass. ensureColumn keeps partially-created temporary v11
	// databases compatible.
	return ensureColumn(ctx, tx, "skills", "scanner_version", "INTEGER NOT NULL DEFAULT 0")
}

func migrateV12SkillAuditEvents(ctx context.Context, tx *sql.Tx) error {
	// Some development snapshots already advertised v11 before the migration was
	// finalized. Re-ensure its column here before adding the independent audit table.
	if err := ensureColumn(ctx, tx, "skills", "scanner_version", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS skill_audit_events (
  id TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  actor TEXT NOT NULL,
  skill_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  scan_verdict TEXT NOT NULL,
  finding_codes_json TEXT NOT NULL DEFAULT '[]',
  risk_acknowledged_at TEXT,
  created_at TEXT NOT NULL,
  CHECK (action IN ('create', 'update', 'enable', 'disable', 'delete'))
);
CREATE INDEX IF NOT EXISTS idx_skill_audit_events_skill_created ON skill_audit_events(skill_id, created_at DESC, id DESC);
`)
	return err
}

func migrateV13AgentWorklineNaming(ctx context.Context, tx *sql.Tx) error {
	tables := [][2]string{{"chapters", "worklines"}, {"narrators", "agents"}, {"narrator_messages", "agent_messages"}, {"narrator_message_attachments", "agent_message_attachments"}, {"narrator_tool_calls", "agent_tool_calls"}}
	for _, table := range tables {
		if err := renameTable(ctx, tx, table[0], table[1]); err != nil {
			return err
		}
	}
	columns := [][3]string{{"projects", "chapter_settings", "workline_settings"}, {"worklines", "parent_chapter_id", "parent_workline_id"}, {"worklines", "merged_into_chapter_id", "merged_into_workline_id"}, {"worklines", "review_source_chapter_id", "review_source_workline_id"}, {"agents", "chapter_id", "workline_id"}, {"agents", "parent_narrator_id", "parent_agent_id"}, {"runs", "narrator_id", "agent_id"}, {"agent_messages", "narrator_id", "agent_id"}, {"agent_message_attachments", "narrator_id", "agent_id"}, {"agent_tool_calls", "narrator_id", "agent_id"}, {"api_requests", "narrator_id", "agent_id"}, {"automation_audit_events", "narrator_id", "agent_id"}, {"memory_injections", "narrator_id", "agent_id"}}
	for _, column := range columns {
		if err := renameColumn(ctx, tx, column[0], column[1], column[2]); err != nil {
			return err
		}
	}
	for _, index := range []string{"idx_chapters_project", "idx_narrators_chapter", "idx_narrators_parent", "idx_runs_narrator_started", "idx_narrator_messages_narrator_time", "idx_messages_run", "idx_message_attachments_message", "idx_message_attachments_narrator", "idx_tool_calls_narrator", "idx_tool_calls_run", "idx_tool_calls_tool_use"} {
		if _, err := tx.ExecContext(ctx, "DROP INDEX IF EXISTS "+quoteIdentifier(index)); err != nil {
			return fmt.Errorf("drop legacy index %s: %w", index, err)
		}
	}
	_, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_worklines_project ON worklines(project_id);
CREATE INDEX IF NOT EXISTS idx_agents_workline ON agents(workline_id);
CREATE INDEX IF NOT EXISTS idx_agents_parent ON agents(parent_agent_id);
CREATE INDEX IF NOT EXISTS idx_runs_agent_started ON runs(agent_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_messages_agent_time ON agent_messages(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_messages_run ON agent_messages(run_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_message_attachments_message ON agent_message_attachments(message_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_message_attachments_agent ON agent_message_attachments(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_agent ON agent_tool_calls(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_run ON agent_tool_calls(run_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_tool_calls_tool_use ON agent_tool_calls(agent_id, tool_use_id);
`)
	return err
}

func migrateV14AgentStreamGenerations(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		table      string
		column     string
		definition string
	}{
		{"agents", "entity_generation", "INTEGER NOT NULL DEFAULT 1"},
		{"agents", "permission_generation", "INTEGER NOT NULL DEFAULT 1"},
		{"workflow_preferences", "policy_generation", "INTEGER NOT NULL DEFAULT 1"},
		{"agent_tool_calls", "permission_generation", "INTEGER NOT NULL DEFAULT 1"},
		{"agent_tool_calls", "policy_generation", "INTEGER NOT NULL DEFAULT 1"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, column.table, column.column, column.definition); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
UPDATE agents SET entity_generation = 1 WHERE entity_generation IS NULL OR entity_generation < 1;
UPDATE agents SET permission_generation = 1 WHERE permission_generation IS NULL OR permission_generation < 1;
UPDATE workflow_preferences SET policy_generation = 1 WHERE policy_generation IS NULL OR policy_generation < 1;
UPDATE agent_tool_calls SET permission_generation = 1 WHERE permission_generation IS NULL OR permission_generation < 1;
UPDATE agent_tool_calls SET policy_generation = 1 WHERE policy_generation IS NULL OR policy_generation < 1;
`)
	return err
}

func migrateV15ScopedSkillRevisions(ctx context.Context, tx *sql.Tx) error {
	for _, rename := range [][3]string{{"skills", "chapter_id", "workline_id"}, {"skill_revisions", "chapter_id", "workline_id"}} {
		if err := renameColumn(ctx, tx, rename[0], rename[1], rename[2]); err != nil {
			return err
		}
	}
	columns := []struct {
		column     string
		definition string
	}{
		{"scope", "TEXT NOT NULL DEFAULT 'global'"},
		{"project_id", "TEXT REFERENCES projects(id) ON DELETE CASCADE"},
		{"workline_id", "TEXT REFERENCES worklines(id) ON DELETE CASCADE"},
		{"deleted_at", "TEXT"},
		{"revision_no", "INTEGER NOT NULL DEFAULT 1"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, "skills", column.column, column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE skills
SET scope = 'global', project_id = NULL, workline_id = NULL, revision_no = CASE WHEN revision_no < 1 THEN 1 ELSE revision_no END
WHERE scope IS NULL OR scope NOT IN ('global', 'project', 'workspace') OR revision_no IS NULL OR revision_no < 1;
DROP INDEX IF EXISTS idx_skills_command_nocase;
DROP INDEX IF EXISTS idx_skills_enabled_command;
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_global_command ON skills(command COLLATE NOCASE) WHERE deleted_at IS NULL AND scope = 'global';
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_project_command ON skills(project_id, command COLLATE NOCASE) WHERE deleted_at IS NULL AND scope = 'project';
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_workspace_command ON skills(workline_id, command COLLATE NOCASE) WHERE deleted_at IS NULL AND scope = 'workspace';
CREATE INDEX IF NOT EXISTS idx_skills_scope_command ON skills(scope, project_id, workline_id, command COLLATE NOCASE, id) WHERE deleted_at IS NULL;
DROP TRIGGER IF EXISTS skills_scope_shape_insert;
DROP TRIGGER IF EXISTS skills_scope_shape_update;
DROP TRIGGER IF EXISTS skills_workspace_scope_insert;
DROP TRIGGER IF EXISTS skills_workspace_scope_update;
DROP TRIGGER IF EXISTS skills_chapter_scope_insert;
DROP TRIGGER IF EXISTS skills_chapter_scope_update;
CREATE TRIGGER skills_scope_shape_insert
BEFORE INSERT ON skills
WHEN NOT (
  (NEW.scope = 'global' AND NEW.project_id IS NULL AND NEW.workline_id IS NULL)
  OR (NEW.scope = 'project' AND NEW.project_id IS NOT NULL AND NEW.workline_id IS NULL)
  OR (NEW.scope = 'workspace' AND NEW.project_id IS NOT NULL AND NEW.workline_id IS NOT NULL AND EXISTS (SELECT 1 FROM worklines WHERE id = NEW.workline_id AND project_id = NEW.project_id))
)
BEGIN
  SELECT RAISE(ABORT, 'invalid skill scope target');
END;
CREATE TRIGGER skills_scope_shape_update
BEFORE UPDATE OF scope, project_id, workline_id ON skills
WHEN NOT (
  (NEW.scope = 'global' AND NEW.project_id IS NULL AND NEW.workline_id IS NULL)
  OR (NEW.scope = 'project' AND NEW.project_id IS NOT NULL AND NEW.workline_id IS NULL)
  OR (NEW.scope = 'workspace' AND NEW.project_id IS NOT NULL AND NEW.workline_id IS NOT NULL AND EXISTS (SELECT 1 FROM worklines WHERE id = NEW.workline_id AND project_id = NEW.project_id))
)
BEGIN
  SELECT RAISE(ABORT, 'invalid skill scope target');
END;
CREATE TABLE IF NOT EXISTS skill_revisions (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  revision_no INTEGER NOT NULL,
  operation TEXT NOT NULL,
  actor TEXT NOT NULL,
  restored_from_revision_no INTEGER,
  name TEXT NOT NULL,
  command TEXT NOT NULL COLLATE NOCASE,
  description TEXT NOT NULL,
  prompt TEXT NOT NULL,
  source TEXT NOT NULL,
  scope TEXT NOT NULL,
  project_id TEXT,
  workline_id TEXT,
  deleted_at TEXT,
  content_hash TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  scan_verdict TEXT NOT NULL,
  scan_findings_json TEXT NOT NULL,
  scanner_version INTEGER NOT NULL,
  risk_acknowledged_at TEXT,
  risk_acknowledged_by TEXT,
  risk_acknowledged_hash TEXT,
  head_created_at TEXT NOT NULL,
  head_updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(skill_id, revision_no),
  CHECK (operation IN ('baseline', 'create', 'update', 'delete', 'restore', 'revalidate')),
  CHECK (scope IN ('global', 'project', 'workspace')),
  CHECK (enabled IN (0, 1)),
  CHECK (scan_verdict IN ('safe', 'review', 'blocked'))
);
CREATE INDEX IF NOT EXISTS idx_skill_revisions_skill_revision ON skill_revisions(skill_id, revision_no DESC);
CREATE INDEX IF NOT EXISTS idx_skill_revisions_snapshot ON skill_revisions(sequence, skill_id);
INSERT INTO skill_revisions (
  id, skill_id, revision_no, operation, actor, name, command, description, prompt, source, scope, project_id, workline_id, deleted_at,
  content_hash, enabled, scan_verdict, scan_findings_json, scanner_version, risk_acknowledged_at, risk_acknowledged_by, risk_acknowledged_hash,
  head_created_at, head_updated_at, created_at
)
SELECT lower(hex(randomblob(16))), s.id, s.revision_no, 'baseline', 'migration', s.name, s.command, s.description, s.prompt, s.source,
       s.scope, s.project_id, s.workline_id, s.deleted_at, s.content_hash, s.enabled, s.scan_verdict, s.scan_findings_json,
       COALESCE(s.scanner_version, 0), s.risk_acknowledged_at, s.risk_acknowledged_by, s.risk_acknowledged_hash, s.created_at, s.updated_at, s.updated_at
FROM skills s
WHERE NOT EXISTS (SELECT 1 FROM skill_revisions r WHERE r.skill_id = s.id);
`); err != nil {
		return err
	}

	auditExists, err := tableExists(ctx, tx, "skill_audit_events")
	if err != nil {
		return err
	}
	if auditExists {
		if _, err := tx.ExecContext(ctx, `
DROP INDEX IF EXISTS idx_skill_audit_events_skill_created;
CREATE TABLE skill_audit_events_v15 (
  id TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  actor TEXT NOT NULL,
  skill_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  scan_verdict TEXT NOT NULL,
  finding_codes_json TEXT NOT NULL DEFAULT '[]',
  risk_acknowledged_at TEXT,
  created_at TEXT NOT NULL,
  CHECK (action IN ('create', 'update', 'enable', 'disable', 'delete', 'restore'))
);
INSERT INTO skill_audit_events_v15 (id, action, actor, skill_id, content_hash, scan_verdict, finding_codes_json, risk_acknowledged_at, created_at)
SELECT id, action, actor, skill_id, content_hash, scan_verdict, finding_codes_json, risk_acknowledged_at, created_at FROM skill_audit_events;
DROP TABLE skill_audit_events;
ALTER TABLE skill_audit_events_v15 RENAME TO skill_audit_events;
CREATE INDEX idx_skill_audit_events_skill_created ON skill_audit_events(skill_id, created_at DESC, id DESC);
`); err != nil {
			return err
		}
	}
	return nil
}

func migrateV16AutomationAuditEvents(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, automationAuditSchemaSQL)
	return err
}

func migrateV17IntegrationConnections(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, integrationConnectionsSchemaSQL)
	return err
}

func migrateV18Memories(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, memorySchemaSQL)
	return err
}

func migrateV19Schedules(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		name       string
		definition string
	}{
		{"source", "TEXT NOT NULL DEFAULT 'manual'"},
		{"source_id", "TEXT NOT NULL DEFAULT ''"},
		{"permission_mode_cap", "TEXT NOT NULL DEFAULT '' CHECK (permission_mode_cap IN ('', 'readOnly', 'acceptEdits'))"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, "runs", column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, schedulesSchemaSQL); err != nil {
		return err
	}
	return ensureColumn(ctx, tx, "schedules", "timezone", "TEXT NOT NULL DEFAULT 'UTC'")
}

func migrateV20NotificationDeliveries(ctx context.Context, tx *sql.Tx) error {
	// Re-ensure timezone for development databases that reached v19 before the
	// schedule timezone contract was finalized.
	if err := ensureColumn(ctx, tx, "schedules", "timezone", "TEXT NOT NULL DEFAULT 'UTC'"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, notificationDeliveriesSchemaSQL)
	return err
}

func migrateV21ChannelPersistence(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumn(ctx, tx, "schedules", "timezone", "TEXT NOT NULL DEFAULT 'UTC'"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, channelPersistenceSchemaSQL)
	return err
}

func migrateV22DeviceActionRequests(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumn(ctx, tx, "schedules", "timezone", "TEXT NOT NULL DEFAULT 'UTC'"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, deviceActionRequestsSchemaSQL)
	return err
}

func migrateV23ExecutionSchedule(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		table      string
		name       string
		definition string
	}{
		{"agents", "execution_generation", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "execution_generation", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "dispatch_id", "TEXT"},
		{"runs", "duration_ms", "INTEGER"},
		{"runs", "trigger_type", "TEXT NOT NULL DEFAULT 'manual'"},
		{"notification_deliveries", "execution_generation", "INTEGER NOT NULL DEFAULT 0"},
		{"schedules", "environment_mode", "TEXT NOT NULL DEFAULT 'workline'"},
		{"schedules", "narrator_mode", "TEXT NOT NULL DEFAULT 'reuse'"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runs
SET execution_generation = (
  SELECT COUNT(*)
  FROM runs AS prior
  WHERE prior.agent_id = runs.agent_id
    AND (prior.started_at < runs.started_at OR (prior.started_at = runs.started_at AND prior.id <= runs.id))
)
WHERE COALESCE(execution_generation, 0) <= 0;
UPDATE runs
SET trigger_type = CASE COALESCE(source, 'manual')
  WHEN 'schedule' THEN 'scheduled'
  WHEN 'scheduled' THEN 'scheduled'
  WHEN 'goal' THEN 'goal'
  WHEN 'internal' THEN 'internal'
  ELSE 'manual'
END
WHERE trigger_type IS NULL OR trigger_type NOT IN ('manual', 'scheduled', 'goal', 'internal') OR (trigger_type = 'manual' AND source IN ('schedule', 'scheduled', 'goal', 'internal'));
UPDATE runs
SET duration_ms = MAX(0, CAST(ROUND((julianday(completed_at) - julianday(started_at)) * 86400000.0) AS INTEGER))
WHERE duration_ms IS NULL AND completed_at IS NOT NULL AND julianday(completed_at) IS NOT NULL AND julianday(started_at) IS NOT NULL;
UPDATE agents
SET execution_generation = MAX(
  COALESCE(execution_generation, 0),
  COALESCE((SELECT MAX(r.execution_generation) FROM runs r WHERE r.agent_id = agents.id), 0)
);
UPDATE notification_deliveries
SET execution_generation = COALESCE((SELECT r.execution_generation FROM runs r WHERE r.id = notification_deliveries.run_id), 0)
WHERE COALESCE(execution_generation, 0) <= 0;
UPDATE schedules SET environment_mode = 'workline' WHERE environment_mode IS NULL OR environment_mode NOT IN ('workline', 'standalone');
UPDATE schedules SET narrator_mode = 'reuse' WHERE narrator_mode IS NULL OR narrator_mode NOT IN ('reuse', 'new');
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_agent_execution_generation ON runs(agent_id, execution_generation) WHERE execution_generation > 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_dispatch_id ON runs(dispatch_id) WHERE dispatch_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_runs_agent_execution_after ON runs(agent_id, execution_generation, id);
DROP TRIGGER IF EXISTS runs_v23_validate_insert;
DROP TRIGGER IF EXISTS runs_v23_validate_update;
CREATE TRIGGER runs_v23_validate_insert BEFORE INSERT ON runs
WHEN NEW.execution_generation < 0 OR NEW.duration_ms < 0 OR NEW.trigger_type NOT IN ('manual', 'scheduled', 'goal', 'internal') OR (NEW.dispatch_id IS NOT NULL AND (length(CAST(NEW.dispatch_id AS BLOB)) < 1 OR length(CAST(NEW.dispatch_id AS BLOB)) > 256))
BEGIN SELECT RAISE(ABORT, 'invalid execution run metadata'); END;
CREATE TRIGGER runs_v23_validate_update BEFORE UPDATE OF execution_generation, dispatch_id, duration_ms, trigger_type ON runs
WHEN NEW.execution_generation < 0 OR NEW.duration_ms < 0 OR NEW.trigger_type NOT IN ('manual', 'scheduled', 'goal', 'internal') OR (NEW.dispatch_id IS NOT NULL AND (length(CAST(NEW.dispatch_id AS BLOB)) < 1 OR length(CAST(NEW.dispatch_id AS BLOB)) > 256))
BEGIN SELECT RAISE(ABORT, 'invalid execution run metadata'); END;
DROP TRIGGER IF EXISTS schedules_v23_validate_insert;
DROP TRIGGER IF EXISTS schedules_v23_validate_update;
CREATE TRIGGER schedules_v23_validate_insert BEFORE INSERT ON schedules
WHEN NEW.environment_mode NOT IN ('workline', 'standalone') OR NEW.narrator_mode NOT IN ('reuse', 'new')
BEGIN SELECT RAISE(ABORT, 'invalid schedule execution mode'); END;
CREATE TRIGGER schedules_v23_validate_update BEFORE UPDATE OF environment_mode, narrator_mode ON schedules
WHEN NEW.environment_mode NOT IN ('workline', 'standalone') OR NEW.narrator_mode NOT IN ('reuse', 'new')
BEGIN SELECT RAISE(ABORT, 'invalid schedule execution mode'); END;
DROP TRIGGER IF EXISTS notification_deliveries_v23_validate_insert;
DROP TRIGGER IF EXISTS notification_deliveries_v23_validate_update;
CREATE TRIGGER notification_deliveries_v23_validate_insert BEFORE INSERT ON notification_deliveries
WHEN NEW.execution_generation < 0
BEGIN SELECT RAISE(ABORT, 'invalid notification execution generation'); END;
CREATE TRIGGER notification_deliveries_v23_validate_update BEFORE UPDATE OF execution_generation ON notification_deliveries
WHEN NEW.execution_generation < 0
BEGIN SELECT RAISE(ABORT, 'invalid notification execution generation'); END;
`); err != nil {
		return err
	}
	return nil
}

func migrateV24SpecBoards(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, specSchemaSQL)
	return err
}

func migrateV25ModelClient(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumn(ctx, tx, "agents", "reasoning_effort", "TEXT"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, modelClientSchemaSQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
-- Early development builds stored reasoning as a boolean toggle in this TEXT
-- column. The toggle meant "use high reasoning" when enabled and "automatic"
-- when disabled. SQLite may retain that representation as INTEGER 0/1 or as
-- the text literals true/false, so normalize it before installing the enum
-- guard rather than silently clearing those existing user choices.
UPDATE agents
SET reasoning_effort = CASE
  WHEN reasoning_effort IS NULL THEN NULL
  WHEN lower(trim(CAST(reasoning_effort AS TEXT))) IN ('auto', 'low', 'medium', 'high')
    THEN lower(trim(CAST(reasoning_effort AS TEXT)))
  WHEN (typeof(reasoning_effort) = 'integer' AND reasoning_effort = 1)
    OR lower(trim(CAST(reasoning_effort AS TEXT))) IN ('true', '1')
    THEN 'high'
  WHEN (typeof(reasoning_effort) = 'integer' AND reasoning_effort = 0)
    OR lower(trim(CAST(reasoning_effort AS TEXT))) IN ('false', '0')
    THEN 'auto'
  -- Invalid legacy data must not be discarded into NULL (which inherits a
  -- potentially non-auto default); preserve a safe explicit fallback instead.
  ELSE 'auto'
END;
DROP TRIGGER IF EXISTS agents_reasoning_effort_insert;
DROP TRIGGER IF EXISTS agents_reasoning_effort_update;
CREATE TRIGGER agents_reasoning_effort_insert BEFORE INSERT ON agents
WHEN NEW.reasoning_effort IS NOT NULL AND NEW.reasoning_effort NOT IN ('auto', 'low', 'medium', 'high')
BEGIN SELECT RAISE(ABORT, 'invalid agent reasoning effort'); END;
CREATE TRIGGER agents_reasoning_effort_update BEFORE UPDATE OF reasoning_effort ON agents
WHEN NEW.reasoning_effort IS NOT NULL AND NEW.reasoning_effort NOT IN ('auto', 'low', 'medium', 'high')
BEGIN SELECT RAISE(ABORT, 'invalid agent reasoning effort'); END;
`); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_settings WHERE id = 'default'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		now := Now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_settings (id, installation_id, default_reasoning_effort, subscription_tier, account_email, revision, updated_at) VALUES ('default', ?, 'auto', 'free', NULL, 1, ?)`, uuid.NewString(), now); err != nil {
			return err
		}
	}
	return nil
}

func migrateV26GenerationRecovery(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumn(ctx, tx, "notification_deliveries", "execution_generation", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE notification_deliveries
SET execution_generation = COALESCE((SELECT r.execution_generation FROM runs r WHERE r.id = notification_deliveries.run_id), 0)
WHERE COALESCE(execution_generation, 0) <= 0;
CREATE INDEX IF NOT EXISTS idx_runs_agent_execution_after ON runs(agent_id, execution_generation, id);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_generation ON notification_deliveries(agent_id, execution_generation, created_at, id);
`)
	return err
}

func migrateV28ProviderAccountStats(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExists(ctx, tx, "provider_account_stats")
	if err != nil {
		return err
	}
	if !exists {
		_, err = tx.ExecContext(ctx, providerAccountStatsSchemaSQL)
		return err
	}
	columns := []struct {
		name       string
		definition string
	}{
		{"success_count", "INTEGER NOT NULL DEFAULT 0"},
		{"failure_count", "INTEGER NOT NULL DEFAULT 0"},
		{"last_attempt_at", "TEXT"},
		{"last_use_at", "TEXT"},
		{"last_success_at", "TEXT"},
		{"last_failure_at", "TEXT"},
		{"last_http_status", "INTEGER"},
		{"last_status_code", "TEXT"},
		{"last_error_code", "TEXT"},
		{"quota_snapshot_json", "TEXT"},
		{"quota_fetched_at", "TEXT"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, "provider_account_stats", column.name, column.definition); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_provider_account_stats_last_use ON provider_account_stats(provider, last_use_at DESC, account_id)`)
	return err
}

func migrateV29ReasoningEffortXHigh(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExists(ctx, tx, "agents")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := extendAgentsReasoningEffortCheck(ctx, tx); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
DROP TRIGGER IF EXISTS agents_reasoning_effort_insert;
DROP TRIGGER IF EXISTS agents_reasoning_effort_update;
CREATE TRIGGER agents_reasoning_effort_insert BEFORE INSERT ON agents
WHEN NEW.reasoning_effort IS NOT NULL AND NEW.reasoning_effort NOT IN ('auto', 'low', 'medium', 'high', 'xhigh')
BEGIN SELECT RAISE(ABORT, 'invalid agent reasoning effort'); END;
CREATE TRIGGER agents_reasoning_effort_update BEFORE UPDATE OF reasoning_effort ON agents
WHEN NEW.reasoning_effort IS NOT NULL AND NEW.reasoning_effort NOT IN ('auto', 'low', 'medium', 'high', 'xhigh')
BEGIN SELECT RAISE(ABORT, 'invalid agent reasoning effort'); END;
`)
	return err
}

// Older fresh databases embed the allowed effort list in the agents table
// CHECK constraint, which SQLite cannot alter directly. Update only that exact
// constraint in sqlite_schema, then advance the schema cookie so SQLite reloads
// the validated DDL without rebuilding a table referenced by many child tables.
func extendAgentsReasoningEffortCheck(ctx context.Context, tx *sql.Tx) error {
	const oldCheck = "CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('auto', 'low', 'medium', 'high'))"
	const newCheck = "CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('auto', 'low', 'medium', 'high', 'xhigh'))"

	var definition sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'agents'`).Scan(&definition); err != nil {
		return err
	}
	if !definition.Valid || !strings.Contains(definition.String, oldCheck) {
		return nil
	}
	updated := strings.Replace(definition.String, oldCheck, newCheck, 1)
	var schemaVersion int
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA writable_schema = ON`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sqlite_schema SET sql = ? WHERE type = 'table' AND name = 'agents'`, updated); err != nil {
		_, _ = tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`)
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, schemaVersion+1)); err != nil {
		_, _ = tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`)
		return err
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`); err != nil {
		return err
	}
	return nil
}

func migrateV27RemoteExecutionScaffold(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		table      string
		name       string
		definition string
	}{
		{"agents", "execution_device_id", "TEXT NOT NULL DEFAULT 'local'"},
		{"runs", "execution_device_id", "TEXT NOT NULL DEFAULT 'local'"},
		{"agent_tool_calls", "execution_device_id", "TEXT NOT NULL DEFAULT 'local'"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, remoteExecutionSchemaSQL); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE agents SET execution_device_id = 'local' WHERE execution_device_id IS NULL OR TRIM(execution_device_id) = '';
UPDATE runs SET execution_device_id = 'local' WHERE execution_device_id IS NULL OR TRIM(execution_device_id) = '';
UPDATE agent_tool_calls SET execution_device_id = 'local' WHERE execution_device_id IS NULL OR TRIM(execution_device_id) = '';
CREATE INDEX IF NOT EXISTS idx_agents_execution_device ON agents(execution_device_id, id);
CREATE INDEX IF NOT EXISTS idx_runs_execution_device ON runs(execution_device_id, execution_generation, id);
CREATE INDEX IF NOT EXISTS idx_tool_calls_execution_device ON agent_tool_calls(execution_device_id, created_at, id);
DROP TRIGGER IF EXISTS agents_execution_device_insert;
DROP TRIGGER IF EXISTS agents_execution_device_update;
DROP TRIGGER IF EXISTS runs_execution_device_insert;
DROP TRIGGER IF EXISTS runs_execution_device_update;
DROP TRIGGER IF EXISTS tool_calls_execution_device_insert;
DROP TRIGGER IF EXISTS tool_calls_execution_device_update;
CREATE TRIGGER agents_execution_device_insert BEFORE INSERT ON agents
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid agent execution device'); END;
CREATE TRIGGER agents_execution_device_update BEFORE UPDATE OF execution_device_id ON agents
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid agent execution device'); END;
CREATE TRIGGER runs_execution_device_insert BEFORE INSERT ON runs
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid run execution device'); END;
CREATE TRIGGER runs_execution_device_update BEFORE UPDATE OF execution_device_id ON runs
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid run execution device'); END;
CREATE TRIGGER tool_calls_execution_device_insert BEFORE INSERT ON agent_tool_calls
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid tool call execution device'); END;
CREATE TRIGGER tool_calls_execution_device_update BEFORE UPDATE OF execution_device_id ON agent_tool_calls
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid tool call execution device'); END;
`)
	return err
}

func migrateV30MessageProviderState(ctx context.Context, tx *sql.Tx) error {
	return ensureColumn(ctx, tx, "agent_messages", "provider_state_json", "TEXT")
}

func migrateV31RunToolCallLifecycle(ctx context.Context, tx *sql.Tx) error {
	runsExist, err := tableExists(ctx, tx, "runs")
	if err != nil {
		return err
	}
	toolCallsExist, err := tableExists(ctx, tx, "agent_tool_calls")
	if err != nil {
		return err
	}
	if runsExist {
		if err := relaxRunsStartedAtConstraint(ctx, tx); err != nil {
			return err
		}
	}
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "runs", name: "started_at", definition: "TEXT"},
		{table: "runs", name: "completed_at", definition: "TEXT"},
		{table: "runs", name: "updated_at", definition: "TEXT"},
		{table: "agent_tool_calls", name: "started_at", definition: "TEXT"},
		{table: "agent_tool_calls", name: "completed_at", definition: "TEXT"},
		{table: "agent_tool_calls", name: "updated_at", definition: "TEXT"},
	} {
		if err := ensureColumn(ctx, tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if toolCallsExist {
		if _, err := tx.ExecContext(ctx, `
UPDATE agent_tool_calls
SET started_at = CASE
      WHEN status IN ('running', 'completed', 'error', 'succeeded', 'failed') THEN COALESCE(NULLIF(started_at, ''), created_at)
      ELSE NULL
    END,
    completed_at = CASE
      WHEN status IN ('completed', 'error', 'denied', 'succeeded', 'failed') THEN COALESCE(NULLIF(completed_at, ''), created_at)
      ELSE NULL
    END,
    updated_at = COALESCE(NULLIF(updated_at, ''), created_at);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_run_updated ON agent_tool_calls(run_id, updated_at DESC);
`); err != nil {
			return err
		}
	}
	if runsExist {
		_, err = tx.ExecContext(ctx, `UPDATE runs SET started_at = CASE WHEN status = 'pending' THEN NULL ELSE started_at END, updated_at = COALESCE(NULLIF(updated_at, ''), created_at)`)
	}
	return err
}

func relaxRunsStartedAtConstraint(ctx context.Context, tx *sql.Tx) error {
	var definition sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'runs'`).Scan(&definition); err != nil {
		return err
	}
	if !definition.Valid || !strings.Contains(definition.String, "started_at TEXT NOT NULL") {
		return nil
	}
	updated := strings.Replace(definition.String, "started_at TEXT NOT NULL", "started_at TEXT", 1)
	var schemaVersion int
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA writable_schema = ON`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sqlite_schema SET sql = ? WHERE type = 'table' AND name = 'runs'`, updated); err != nil {
		_, _ = tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`)
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, schemaVersion+1)); err != nil {
		_, _ = tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`)
		return err
	}
	_, err := tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`)
	return err
}

func migrateV32UserHandlesAndAuthSessions(ctx context.Context, tx *sql.Tx) error {
	usersExist, err := tableExists(ctx, tx, "users")
	if err != nil || !usersExist {
		return err
	}
	if err := ensureColumn(ctx, tx, "users", "handle", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "users", "handle_key", "TEXT"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, username, COALESCE(handle, '') FROM users`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, username, handle string
		if err := rows.Scan(&id, &username, &handle); err != nil {
			return err
		}
		if strings.TrimSpace(handle) == "" {
			handle = username
		}
		normalized, key, err := CanonicalHandle(handle)
		if err != nil {
			return fmt.Errorf("backfill handle for user %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET handle = ?, handle_key = ? WHERE id = ?`, normalized, key, id); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_handle_key ON users(handle_key);
CREATE TABLE IF NOT EXISTS auth_sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id);
`)
	return err
}

func migrateV33MessageDrafts(ctx context.Context, tx *sql.Tx) error {
	usersExist, err := tableExists(ctx, tx, "users")
	if err != nil || !usersExist {
		return err
	}
	agentsExist, err := tableExists(ctx, tx, "agents")
	if err != nil || !agentsExist {
		return err
	}
	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS message_drafts (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  content_text TEXT NOT NULL,
  version INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (user_id, agent_id)
);
CREATE INDEX IF NOT EXISTS idx_message_drafts_agent ON message_drafts(agent_id);
`)
	return err
}

func migrateV34MessageCorrections(ctx context.Context, tx *sql.Tx) error {
	return ensureColumn(ctx, tx, "agent_messages", "correction_of_message_id", "TEXT REFERENCES agent_messages(id) ON DELETE RESTRICT")
}

func migrateV35ProjectMembers(ctx context.Context, tx *sql.Tx) error {
	projectsExist, err := tableExists(ctx, tx, "projects")
	if err != nil || !projectsExist {
		return err
	}
	usersExist, err := tableExists(ctx, tx, "users")
	if err != nil || !usersExist {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS project_members (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(project_id, user_id),
  CHECK (role IN ('owner', 'member'))
);
CREATE INDEX IF NOT EXISTS idx_project_members_user ON project_members(user_id, project_id);
`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT OR IGNORE INTO project_members (project_id, user_id, role, created_at)
SELECT p.id, u.id, 'owner', p.created_at
FROM projects p
JOIN (
  SELECT id FROM users WHERE TRIM(id) <> '' ORDER BY created_at ASC, id ASC LIMIT 1
) u
WHERE NOT EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id)`)
	return err
}

func migrateV36APIRequestUsageIndexes(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExists(ctx, tx, "api_requests")
	if err != nil || !exists {
		return err
	}
	_, err = tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_api_requests_created ON api_requests(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_requests_provider_model_created ON api_requests(provider, model, created_at DESC, id DESC);
`)
	return err
}

func migrateV37Plugins(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, pluginSchemaSQL)
	return err
}

func migrateV38Plans(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, planSchemaSQL); err != nil {
		return err
	}
	if err := ensureColumn(ctx, tx, "plans", "source_run_id", "TEXT REFERENCES runs(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_source_run ON plans(source_run_id) WHERE source_run_id IS NOT NULL`); err != nil {
		return err
	}
	runsExist, err := tableExists(ctx, tx, "runs")
	if err != nil || !runsExist {
		return err
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{"execution_mode", "TEXT NOT NULL DEFAULT 'execute'"},
		{"plan_id", "TEXT REFERENCES plans(id) ON DELETE SET NULL"},
		{"policy_generation_snapshot", "INTEGER NOT NULL DEFAULT 0"},
		{"agent_generation_snapshot", "INTEGER NOT NULL DEFAULT 0"},
		{"tool_catalog_digest", "TEXT NOT NULL DEFAULT ''"},
		{"workspace_fingerprint", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureColumn(ctx, tx, "runs", column.name, column.definition); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
UPDATE runs
SET execution_mode = 'execute'
WHERE execution_mode IS NULL OR execution_mode NOT IN ('plan', 'execute');
UPDATE runs
SET policy_generation_snapshot = 0
WHERE policy_generation_snapshot IS NULL OR policy_generation_snapshot < 0;
UPDATE runs
SET agent_generation_snapshot = 0
WHERE agent_generation_snapshot IS NULL OR agent_generation_snapshot < 0;
UPDATE runs
SET tool_catalog_digest = ''
WHERE tool_catalog_digest IS NULL;
UPDATE runs
SET workspace_fingerprint = ''
WHERE workspace_fingerprint IS NULL;
CREATE INDEX IF NOT EXISTS idx_runs_plan ON runs(plan_id, execution_generation DESC, id DESC);
`)
	return err
}

func migrateV39BackgroundTasks(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, backgroundTaskSchemaSQL)
	return err
}

func migrateV40RunContinuations(ctx context.Context, tx *sql.Tx) error {
	runsExist, err := tableExists(ctx, tx, "runs")
	if err != nil || !runsExist {
		return err
	}
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{"runs", "auto_continuation_mode", "TEXT NOT NULL DEFAULT 'off'"},
		{"runs", "continuation_count", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "continuation_segment_turns", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "turn_count", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "max_total_turns", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "max_continuations", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "max_total_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "consumed_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "consumed_output_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"runs", "deadline_at", "TEXT"},
		{"runs", "resume_after_message_id", "TEXT REFERENCES agent_messages(id) ON DELETE SET NULL"},
		{"runs", "last_stop_reason", "TEXT"},
		{"runs", "continuation_reason", "TEXT"},
		{"runs", "waiting_background_task_id", "TEXT REFERENCES background_tasks(id) ON DELETE SET NULL"},
		{"agent_messages", "completion_state", "TEXT"},
		{"agent_messages", "stop_reason", "TEXT"},
		{"api_requests", "stop_reason", "TEXT"},
		{"api_requests", "turn_index", "INTEGER NOT NULL DEFAULT 0"},
		{"api_requests", "continuation_index", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(ctx, tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
UPDATE runs SET auto_continuation_mode = 'off' WHERE auto_continuation_mode IS NULL OR auto_continuation_mode NOT IN ('off', 'safe');
UPDATE runs SET continuation_count = MAX(0, COALESCE(continuation_count, 0)), continuation_segment_turns = MAX(0, COALESCE(continuation_segment_turns, 0)), turn_count = MAX(0, COALESCE(turn_count, 0)), max_total_turns = MAX(0, COALESCE(max_total_turns, 0)), max_continuations = MAX(0, COALESCE(max_continuations, 0)), max_total_tokens = MAX(0, COALESCE(max_total_tokens, 0)), consumed_input_tokens = MAX(0, COALESCE(consumed_input_tokens, 0)), consumed_output_tokens = MAX(0, COALESCE(consumed_output_tokens, 0));
UPDATE api_requests SET turn_index = MAX(0, COALESCE(turn_index, 0)), continuation_index = MAX(0, COALESCE(continuation_index, 0));
CREATE INDEX IF NOT EXISTS idx_runs_continuation_pending ON runs(status, updated_at ASC, id ASC) WHERE status = 'continuation_pending';
CREATE INDEX IF NOT EXISTS idx_runs_waiting_background_task ON runs(waiting_background_task_id, status, id) WHERE waiting_background_task_id IS NOT NULL;
CREATE TRIGGER IF NOT EXISTS trg_runs_auto_continuation_mode_insert
BEFORE INSERT ON runs FOR EACH ROW
WHEN NEW.auto_continuation_mode NOT IN ('off', 'safe')
BEGIN SELECT RAISE(ABORT, 'invalid auto_continuation_mode'); END;
CREATE TRIGGER IF NOT EXISTS trg_runs_auto_continuation_mode_update
BEFORE UPDATE OF auto_continuation_mode ON runs FOR EACH ROW
WHEN NEW.auto_continuation_mode NOT IN ('off', 'safe')
BEGIN SELECT RAISE(ABORT, 'invalid auto_continuation_mode'); END;
`)
	return err
}

func migrateV42ProviderSecrets(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, providerSecretsSchemaSQL)
	return err
}

func migrateV43NavigationState(ctx context.Context, tx *sql.Tx) error {
	for _, column := range []struct {
		table      string
		column     string
		definition string
	}{
		{"projects", "pinned", "INTEGER NOT NULL DEFAULT 0"},
		{"projects", "archived_at", "TEXT"},
		{"agents", "pinned", "INTEGER NOT NULL DEFAULT 0"},
		{"agents", "archived_at", "TEXT"},
	} {
		if err := ensureColumn(ctx, tx, column.table, column.column, column.definition); err != nil {
			return err
		}
	}
	projectsExist, err := tableExists(ctx, tx, "projects")
	if err != nil {
		return err
	}
	if projectsExist {
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET pinned = 0 WHERE pinned IS NULL OR pinned NOT IN (0, 1)`); err != nil {
			return err
		}
		updatedAtExists, err := columnExists(ctx, tx, "projects", "updated_at")
		if err != nil {
			return err
		}
		if updatedAtExists {
			if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_projects_navigation_state ON projects(pinned DESC, archived_at, updated_at DESC, id ASC)`); err != nil {
				return err
			}
		}
	}
	agentsExist, err := tableExists(ctx, tx, "agents")
	if err != nil {
		return err
	}
	if agentsExist {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET pinned = 0 WHERE pinned IS NULL OR pinned NOT IN (0, 1)`); err != nil {
			return err
		}
		updatedAtExists, err := columnExists(ctx, tx, "agents", "updated_at")
		if err != nil {
			return err
		}
		if updatedAtExists {
			if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_agents_navigation_state ON agents(pinned DESC, archived_at, updated_at DESC, id ASC)`); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateV44AccountPreferences(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, accountPreferencesSchemaSQL)
	return err
}

func migrateV45OAuthApp(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, oauthAppSchemaSQL)
	return err
}

func migrateV46GatewayAccountGrants(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, gatewayAccountGrantsSchemaSQL)
	return err
}

func migrateV47GeneratedImages(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"agents", "agent_messages"} {
		exists, err := tableExists(ctx, tx, table)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
	}
	_, err := tx.ExecContext(ctx, generatedImagesSchemaSQL)
	return err
}

func migrateV48AccountPreferencesSetupVersion(ctx context.Context, tx *sql.Tx) error {
	return ensureColumn(ctx, tx, "account_preferences", "setup_version", "INTEGER NOT NULL DEFAULT 0 CHECK (setup_version BETWEEN 0 AND 1000)")
}

func migrateV49AttachmentModelImages(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "model_data_blob", definition: "BLOB"},
		{name: "model_mime_type", definition: "TEXT"},
		{name: "image_width", definition: "INTEGER NOT NULL DEFAULT 0 CHECK (image_width BETWEEN 0 AND 8192)"},
		{name: "image_height", definition: "INTEGER NOT NULL DEFAULT 0 CHECK (image_height BETWEEN 0 AND 8192)"},
		{name: "sha256", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "processing_status", definition: "TEXT NOT NULL DEFAULT '' CHECK (processing_status IN ('', 'ready', 'rejected'))"},
		{name: "processing_code", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "processing_error", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, "agent_message_attachments", column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

// migrateV52SortableTimestamps pads legacy timestamps to the fixed-width
// fractional-second form written by Now(). Values were previously formatted with
// time.RFC3339Nano, which strips trailing zeros, so a whole-second value like
// "2026-07-25T06:00:00Z" sorted lexically *after* every sub-second value in the
// same second. Every ORDER BY created_at query and keyset cursor in this schema
// compares these values as text, so the unpadded rows reordered history.
func migrateV53RemoteCollaboration(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, remoteCollaborationSchemaSQL)
	return err
}

// migrateV54DangerReflection makes the model-in-the-loop safety gate switchable.
// It defaults to on: an existing user who never opens the setting keeps the
// stricter behavior rather than silently losing a control they already had.
func migrateV54DangerReflection(ctx context.Context, tx *sql.Tx) error {
	return ensureColumn(ctx, tx, "workflow_preferences", "danger_reflection_enabled", "INTEGER NOT NULL DEFAULT 1")
}

func migrateV52SortableTimestamps(ctx context.Context, tx *sql.Tx) error {
	columns, err := timestampTextColumns(ctx, tx)
	if err != nil {
		return err
	}
	for _, column := range columns {
		// Only rewrite the values that sort incorrectly: an RFC3339 UTC instant
		// with no fractional part. Rows that already carry a fractional second
		// keep their exact stored text.
		statement := fmt.Sprintf(
			`UPDATE %s SET %s = substr(%s, 1, 19) || '.000000000Z' WHERE %s IS NOT NULL AND length(%s) = 20 AND substr(%s, 20, 1) = 'Z'`,
			quoteIdentifier(column.table), quoteIdentifier(column.column), quoteIdentifier(column.column),
			quoteIdentifier(column.column), quoteIdentifier(column.column), quoteIdentifier(column.column),
		)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("normalize %s.%s timestamps: %w", column.table, column.column, err)
		}
	}
	return nil
}

type timestampColumn struct {
	table  string
	column string
}

// timestampTextColumns discovers the stored timestamp columns from the live
// schema so the migration stays correct as tables are added, instead of
// hardcoding a list that silently misses new ordering keys.
func timestampTextColumns(ctx context.Context, tx *sql.Tx) ([]timestampColumn, error) {
	tableRows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	var tables []string
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			tableRows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := tableRows.Err(); err != nil {
		tableRows.Close()
		return nil, err
	}
	if err := tableRows.Close(); err != nil {
		return nil, err
	}

	var columns []timestampColumn
	for _, table := range tables {
		columnRows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, quoteIdentifier(table)))
		if err != nil {
			return nil, fmt.Errorf("inspect table %s: %w", table, err)
		}
		for columnRows.Next() {
			var (
				cid          int
				name         string
				declaredType sql.NullString
				notNull      int
				defaultValue sql.NullString
				primaryKey   int
			)
			if err := columnRows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
				columnRows.Close()
				return nil, err
			}
			if isTimestampColumnName(name) && strings.EqualFold(strings.TrimSpace(declaredType.String), "TEXT") {
				columns = append(columns, timestampColumn{table: table, column: name})
			}
		}
		if err := columnRows.Err(); err != nil {
			columnRows.Close()
			return nil, err
		}
		if err := columnRows.Close(); err != nil {
			return nil, err
		}
	}
	return columns, nil
}

func isTimestampColumnName(name string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(trimmed, "_at")
}

func migrateV50ToolExecutionGroups(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, toolExecutionGroupSchemaSQL)
	return err
}

func migrateV51ProfileConfiguration(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, profileConfigurationSchemaSQL)
	return err
}

func migrateV41PrivateAPIGateway(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, gatewaySchemaSQL); err != nil {
		return err
	}
	apiRequestsExist, err := tableExists(ctx, tx, "api_requests")
	if err != nil || !apiRequestsExist {
		return err
	}
	if err := ensureColumn(ctx, tx, "api_requests", "gateway_key_id", "TEXT REFERENCES gateway_keys(id) ON DELETE SET NULL"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_api_requests_gateway_key_created ON api_requests(gateway_key_id, created_at DESC, id DESC)`)
	return err
}

func migrateLegacyZeroVersion(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy database migration: %w", err)
	}
	defer tx.Rollback()

	legacySchema := legacyNamingSchemaSQL()
	if err := execSchemaStatements(ctx, tx, legacySchema, func(stmt string) bool {
		upper := strings.ToUpper(strings.TrimSpace(stmt))
		return strings.HasPrefix(upper, "PRAGMA ") || strings.HasPrefix(upper, "CREATE TABLE ")
	}); err != nil {
		return fmt.Errorf("create missing legacy tables: %w", err)
	}
	if err := ensureLegacyColumns(ctx, tx); err != nil {
		return fmt.Errorf("ensure legacy columns: %w", err)
	}
	if err := execSchemaStatements(ctx, tx, legacySchema, func(stmt string) bool {
		upper := strings.ToUpper(strings.TrimSpace(stmt))
		return strings.HasPrefix(upper, "CREATE INDEX ") || strings.HasPrefix(upper, "CREATE UNIQUE INDEX ")
	}); err != nil {
		return fmt.Errorf("create missing legacy indexes: %w", err)
	}
	if err := setUserVersion(ctx, tx, 1); err != nil {
		return fmt.Errorf("set legacy database version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy database migration: %w", err)
	}
	return nil
}

func legacyNamingSchemaSQL() string {
	// P2-P3 tables were introduced after the agent/workline naming migration and
	// must be created by their own migrations with modern column names.
	legacySchema := schemaSQL
	// Append-only migrations keep extending schemaSQL, so an exact composite
	// suffix is brittle. Cut at the first post-v18 schema fragment instead; the
	// later migrations create those tables after the v13 naming transition.
	if index := strings.Index(legacySchema, schedulesSchemaSQL); index >= 0 {
		legacySchema = legacySchema[:index]
	}
	return strings.NewReplacer(
		"agent_message_attachments", "narrator_message_attachments",
		"agent_messages", "narrator_messages",
		"agent_tool_calls", "narrator_tool_calls",
		"idx_runs_agent_started", "idx_runs_narrator_started",
		"idx_message_attachments_agent", "idx_message_attachments_narrator",
		"idx_tool_calls_agent", "idx_tool_calls_narrator",
		"idx_agents_", "idx_narrators_",
		"parent_agent_id", "parent_narrator_id",
		"agent_id", "narrator_id",
		"REFERENCES agents(", "REFERENCES narrators(",
		"CREATE TABLE IF NOT EXISTS agents (", "CREATE TABLE IF NOT EXISTS narrators (",
		" ON agents(", " ON narrators(",
		"workline", "chapter",
	).Replace(legacySchema)
}

func execSchemaStatements(ctx context.Context, tx *sql.Tx, schema string, include func(string) bool) error {
	for _, raw := range strings.Split(schema, ";") {
		stmt := strings.TrimSpace(raw)
		if stmt == "" || !include(stmt) {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", firstLine(stmt), err)
		}
	}
	return nil
}

func firstLine(stmt string) string {
	line, _, _ := strings.Cut(stmt, "\n")
	return strings.TrimSpace(line)
}

func databaseIsEmpty(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'index', 'trigger', 'view') AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect database objects: %w", err)
	}
	return count == 0, nil
}

func getUserVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read database user_version: %w", err)
	}
	return version, nil
}

func setUserVersion(ctx context.Context, tx *sql.Tx, version int) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version))
	return err
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func tableExists(ctx context.Context, q rowQuerier, table string) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func renameTable(ctx context.Context, tx *sql.Tx, oldName, newName string) error {
	oldExists, err := tableExists(ctx, tx, oldName)
	if err != nil {
		return fmt.Errorf("inspect legacy table %s: %w", oldName, err)
	}
	newExists, err := tableExists(ctx, tx, newName)
	if err != nil {
		return fmt.Errorf("inspect renamed table %s: %w", newName, err)
	}
	if !oldExists {
		return nil
	}
	if newExists {
		return fmt.Errorf("cannot rename table %s: %s already exists", oldName, newName)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", quoteIdentifier(oldName), quoteIdentifier(newName))); err != nil {
		return fmt.Errorf("rename table %s to %s: %w", oldName, newName, err)
	}
	return nil
}

func renameColumn(ctx context.Context, tx *sql.Tx, table, oldName, newName string) error {
	tablePresent, err := tableExists(ctx, tx, table)
	if err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	if !tablePresent {
		return nil
	}
	oldExists, err := columnExists(ctx, tx, table, oldName)
	if err != nil {
		return fmt.Errorf("inspect column %s.%s: %w", table, oldName, err)
	}
	if !oldExists {
		return nil
	}
	newExists, err := columnExists(ctx, tx, table, newName)
	if err != nil {
		return fmt.Errorf("inspect column %s.%s: %w", table, newName, err)
	}
	if newExists {
		return fmt.Errorf("cannot rename column %s.%s: %s already exists", table, oldName, newName)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", quoteIdentifier(table), quoteIdentifier(oldName), quoteIdentifier(newName))); err != nil {
		return fmt.Errorf("rename column %s.%s to %s: %w", table, oldName, newName, err)
	}
	return nil
}

func columnExists(ctx context.Context, q rowsQuerier, table, column string) (bool, error) {
	rows, err := q.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func ensureColumn(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	exists, err := tableExists(ctx, tx, table)
	if err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	if !exists {
		return nil
	}
	exists, err = columnExists(ctx, tx, table, column)
	if err != nil {
		return fmt.Errorf("inspect column %s.%s: %w", table, column, err)
	}
	if exists {
		return nil
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", quoteIdentifier(table), quoteIdentifier(column), definition)
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func ensureLegacyColumns(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		table      string
		column     string
		definition string
	}{
		{"users", "password_hash", "TEXT"},
		{"users", "role", "TEXT NOT NULL DEFAULT 'user'"},
		{"users", "avatar_color", "TEXT"},
		{"users", "avatar_image_id", "TEXT"},
		{"users", "git_username", "TEXT"},
		{"users", "git_email", "TEXT"},
		{"projects", "description", "TEXT"},
		{"projects", "status", "TEXT NOT NULL DEFAULT 'active'"},
		{"projects", "flow_mode", "TEXT NOT NULL DEFAULT 'workspace'"},
		{"projects", "git_path", "TEXT"},
		{"projects", "remote_url", "TEXT"},
		{"projects", "default_branch", "TEXT"},
		{"projects", "startup_script", "TEXT"},
		{"projects", "copy_files", "TEXT"},
		{"projects", "chapter_settings", "TEXT"},
		{"projects", "proxy_domain", "TEXT"},
		{"projects", "pinned", "INTEGER NOT NULL DEFAULT 0"},
		{"projects", "archived_at", "TEXT"},
		{"chapters", "description", "TEXT"},
		{"chapters", "status", "TEXT NOT NULL DEFAULT 'active'"},
		{"chapters", "role", "TEXT NOT NULL DEFAULT 'root'"},
		{"chapters", "branch", "TEXT"},
		{"chapters", "worktree_path", "TEXT"},
		{"chapters", "base_branch", "TEXT"},
		{"chapters", "parent_chapter_id", "TEXT"},
		{"chapters", "fork_point", "TEXT"},
		{"chapters", "merged_into_chapter_id", "TEXT"},
		{"chapters", "merge_commit_sha", "TEXT"},
		{"chapters", "merge_strategy", "TEXT"},
		{"chapters", "pre_merge_target_sha", "TEXT"},
		{"chapters", "container_config", "TEXT"},
		{"chapters", "exploration_group_id", "TEXT"},
		{"chapters", "is_root", "INTEGER NOT NULL DEFAULT 0"},
		{"chapters", "head_commit_sha", "TEXT"},
		{"chapters", "start_commit_sha", "TEXT"},
		{"chapters", "commit_count", "INTEGER NOT NULL DEFAULT 0"},
		{"chapters", "color", "TEXT"},
		{"chapters", "group_label", "TEXT"},
		{"chapters", "pinned", "INTEGER NOT NULL DEFAULT 0"},
		{"chapters", "position_x", "REAL"},
		{"chapters", "position_y", "REAL"},
		{"chapters", "panel_expanded", "INTEGER"},
		{"chapters", "panel_width", "REAL"},
		{"chapters", "panel_height", "REAL"},
		{"chapters", "review_source_chapter_id", "TEXT"},
		{"chapters", "review_status", "TEXT"},
		{"chapters", "last_accessed_at", "TEXT"},
		{"narrators", "api_conversation_id", "TEXT"},
		{"narrators", "fork_message_id", "TEXT"},
		{"narrators", "subagent_type", "TEXT"},
		{"narrators", "inherit_mode", "TEXT"},
		{"narrators", "parent_narrator_id", "TEXT"},
		{"narrators", "context_summary", "TEXT"},
		{"narrators", "system_prompt", "TEXT"},
		{"narrators", "previous_permission_mode", "TEXT"},
		{"narrators", "reasoning_effort", "TEXT"},
		{"narrators", "fast_mode", "INTEGER NOT NULL DEFAULT 0"},
		{"narrators", "relaxed_plan", "INTEGER NOT NULL DEFAULT 0"},
		{"narrators", "message_count", "INTEGER NOT NULL DEFAULT 0"},
		{"narrators", "total_cost_usd", "REAL NOT NULL DEFAULT 0"},
		{"narrators", "last_message_at", "TEXT"},
		{"narrators", "plan_mode", "INTEGER NOT NULL DEFAULT 0"},
		{"narrators", "cwd", "TEXT"},
		{"narrators", "error_message", "TEXT"},
		{"narrators", "todos_json", "TEXT"},
		{"narrators", "todos_tool_use_id", "TEXT"},
		{"narrators", "prune_boundary_message_id", "TEXT"},
		{"narrators", "pruned_percent", "INTEGER"},
		{"narrators", "prune_enabled", "INTEGER NOT NULL DEFAULT 0"},
		{"narrators", "is_background", "INTEGER NOT NULL DEFAULT 0"},
		{"narrators", "background_status", "TEXT"},
		{"narrators", "background_result", "TEXT"},
		{"narrators", "background_completed_at", "TEXT"},
		{"narrators", "pinned", "INTEGER NOT NULL DEFAULT 0"},
		{"narrators", "archived_at", "TEXT"},
		{"narrator_messages", "run_id", "TEXT"},
		{"narrator_messages", "sdk_message_uuid", "TEXT"},
		{"narrator_messages", "parent_tool_use_id", "TEXT"},
		{"narrator_messages", "content_json", "TEXT"},
		{"narrator_messages", "content_text", "TEXT"},
		{"narrator_messages", "tokens_in", "INTEGER"},
		{"narrator_messages", "cost_usd", "REAL"},
		{"narrator_messages", "turn_usage_json", "TEXT"},
		{"narrator_messages", "context_percent", "REAL"},
		{"narrator_messages", "meter_usage", "REAL"},
		{"narrator_messages", "meter_unit", "TEXT"},
		{"narrator_messages", "commit_sha", "TEXT"},
		{"narrator_messages", "command_text", "TEXT"},
		{"narrator_messages", "created_by", "TEXT"},
		{"narrator_tool_calls", "run_id", "TEXT"},
		{"narrator_tool_calls", "message_id", "TEXT"},
		{"narrator_tool_calls", "input_json", "TEXT"},
		{"narrator_tool_calls", "output_json", "TEXT"},
		{"narrator_tool_calls", "duration_ms", "INTEGER"},
		{"narrator_tool_calls", "error_message", "TEXT"},
		{"narrator_tool_calls", "permission_decided_by", "TEXT"},
		{"narrator_tool_calls", "permission_decided_at", "TEXT"},
		{"narrator_tool_calls", "permission_deny_message", "TEXT"},
		{"narrator_tool_calls", "permission_decision_reason", "TEXT"},
		{"narrator_tool_calls", "permission_suggestions", "TEXT"},
		{"narrator_tool_calls", "is_background", "INTEGER NOT NULL DEFAULT 0"},
		{"narrator_tool_calls", "input_tokens", "INTEGER"},
		{"narrator_tool_calls", "output_tokens", "INTEGER"},
		{"narrator_tool_calls", "total_cost", "REAL"},
		{"narrator_tool_calls", "provider", "TEXT"},
		{"narrator_tool_calls", "model", "TEXT"},
		{"narrator_tool_calls", "result_message_id", "TEXT"},
		{"api_requests", "run_id", "TEXT"},
		{"api_requests", "kind", "TEXT NOT NULL DEFAULT 'model'"},
		{"api_requests", "provider", "TEXT"},
		{"api_requests", "credential_id", "TEXT"},
		{"api_requests", "model", "TEXT"},
		{"api_requests", "input_tokens", "INTEGER"},
		{"api_requests", "output_tokens", "INTEGER"},
		{"api_requests", "cached_input_tokens", "INTEGER"},
		{"api_requests", "reasoning_tokens", "INTEGER"},
		{"api_requests", "ttft_ms", "INTEGER"},
		{"api_requests", "duration_ms", "INTEGER"},
		{"api_requests", "cost_usd", "REAL"},
		{"api_requests", "context_percent", "REAL"},
		{"api_requests", "meter_usage", "REAL"},
		{"api_requests", "meter_unit", "TEXT"},
		{"api_requests", "error_message", "TEXT"},
		{"api_requests", "raw_dump_json", "TEXT"},
		{"agent_backends", "kind", "TEXT NOT NULL DEFAULT 'local'"},
		{"agent_backends", "api_key", "TEXT"},
		{"agent_backends", "active", "INTEGER NOT NULL DEFAULT 0"},
		{"mcp_servers", "transport", "TEXT NOT NULL DEFAULT 'stdio'"},
		{"mcp_servers", "args_json", "TEXT"},
		{"mcp_servers", "cwd", "TEXT"},
		{"mcp_servers", "env_json", "TEXT"},
		{"mcp_servers", "enabled", "INTEGER NOT NULL DEFAULT 1"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, tx, column.table, column.column, column.definition); err != nil {
			return err
		}
	}
	return nil
}
