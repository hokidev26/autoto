package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The schema parity tests prove that a fresh install and a replay of every
// migration over today's baseline agree with each other. What they cannot
// prove is that a database which *started life on an old schema and holds
// data* survives the trip: the v13 table renames, the v15 skill_revisions
// backfill and audit-table rebuild, the v23 run-generation backfill, the v25
// reasoning normalization, and the v52 timestamp rewrite all move or rewrite
// user rows, and none of them were exercised end-to-end against seeded data.
//
// The oldest schema this repository can still reproduce is the legacy
// pre-v13 naming schema that migrateLegacyZeroVersion builds from
// legacyNamingSchemaSQL. That is a deliberate approximation, and its limits
// matter for reading these tests:
//
//   - It is today's schemaSQL cut before the schedules fragment with tables
//     and columns renamed to their pre-v13 spellings, not a byte-for-byte
//     DDL snapshot of a historical release. Columns that later migrations
//     add (and their CHECK constraints) are therefore already present at the
//     starting point, so a value shape that today's CHECKs reject cannot be
//     seeded even though a truly ancient database may have held it.
//   - There is no way to reconstruct "the real schema at version N" for
//     arbitrary N. What can be reconstructed is "legacy schema, then the
//     first N migrations", which is exactly the on-disk state of an upgrade
//     that was interrupted after migration N committed — the state a crashed
//     or killed process leaves behind, and the state runMigrations must be
//     able to finish from.
//
// Within those limits the tests below cover: every intermediate stop point,
// data preservation across the full chain, idempotency at the current
// version, transactional rollback of a failed migration, the refusal to open
// a future database, and the version-0 (pre-versioning) bootstrap path.

// openUpgradeTestDB opens a plain (non-URI, no pragmas) handle like production
// legacy databases were created with. One connection keeps session pragmas
// stable; synchronous=OFF keeps the ~4000 migration commits the loop test
// performs from spending a fsync each, without changing transaction semantics.
func openUpgradeTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)
	if _, err := database.ExecContext(context.Background(), `PRAGMA synchronous = OFF`); err != nil {
		t.Fatal(err)
	}
	return database
}

// seedLegacyFixtureSQL populates the legacy-naming schema with representative
// rows: unicode, empty strings and NULLs in user-visible text, a foreign-key
// cycle between runs and messages, timestamps in the pre-v52 whole-second
// format alongside one that already carries nanoseconds, a boolean-era-adjacent
// reasoning effort, a skill that v15 must backfill into skill_revisions, and an
// audit event that v15 rebuilds into a new table.
//
// Every value has to satisfy the CHECK constraints of the modern-derived
// legacy DDL (see the file comment), which is why the historical boolean
// reasoning_effort spellings v25 normalizes cannot appear here.
const seedLegacyFixtureSQL = `
INSERT INTO users (id, username, created_at) VALUES
  ('user-1', 'ray', '2026-01-01T00:00:00Z'),
  ('user-2', '雷伊Ray', '2026-01-05T00:00:00Z');

INSERT INTO projects (id, name, description, created_at, updated_at) VALUES
  ('project-1', '任務控制台 🚀', NULL, '2026-01-01T01:00:00Z', '2026-01-01T01:00:00Z'),
  ('project-2', 'Edge', '', '2026-01-01T02:00:00Z', '2026-01-01T02:00:00Z');

INSERT INTO chapters (id, project_id, title, is_root, parent_chapter_id, created_at, updated_at) VALUES
  ('chapter-1', 'project-1', 'main', 1, NULL, '2026-01-01T03:00:00Z', '2026-01-01T03:00:00Z'),
  ('chapter-2', 'project-1', '支線/side', 0, 'chapter-1', '2026-01-01T04:00:00Z', '2026-01-01T04:00:00Z');

INSERT INTO narrators (id, chapter_id, type, title, model, permission_mode, status, reasoning_effort, parent_narrator_id, message_count, created_at, updated_at) VALUES
  ('narrator-1', 'chapter-1', 'primary', '主要代理 🤖', 'anthropic:claude', 'acceptEdits', 'idle', NULL, NULL, 3, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z'),
  ('narrator-2', 'chapter-2', 'primary', '', 'fake:test', 'readOnly', 'idle', 'high', NULL, 1, '2026-01-02T01:00:00Z', '2026-01-02T01:00:00Z'),
  ('narrator-3', NULL, 'subagent', 'Sub', 'fake:test', 'acceptEdits', 'idle', NULL, 'narrator-1', 0, '2026-01-02T02:00:00Z', '2026-01-02T02:00:00Z');

INSERT INTO narrator_messages (id, narrator_id, run_id, role, content_text, content_json, created_by, created_at) VALUES
  ('message-1', 'narrator-1', 'run-1', 'user', '你好，世界！👋 <script>alert("x")</script>', NULL, 'user-1', '2026-01-03T10:00:00Z'),
  ('message-2', 'narrator-1', 'run-1', 'assistant', '', '[{"type":"text","text":"done"}]', NULL, '2026-01-03T10:00:01.123456789Z'),
  ('message-3', 'narrator-1', NULL, 'assistant', NULL, NULL, NULL, '2026-01-03T10:00:02Z'),
  ('message-4', 'narrator-2', NULL, 'user', 'line one
"quoted" line two', NULL, NULL, '2026-01-03T10:00:03Z');

INSERT INTO runs (id, narrator_id, trigger_message_id, status, started_at, completed_at, git_snapshot_at, source, created_at, updated_at) VALUES
  ('run-1', 'narrator-1', 'message-1', 'completed', '2026-01-03T09:00:00Z', '2026-01-03T09:10:00Z', '2026-01-03T09:00:30Z', 'schedule', '2026-01-03T09:00:00Z', '2026-01-03T09:10:00Z'),
  ('run-2', 'narrator-1', NULL, 'running', '2026-01-03T11:00:00Z', NULL, NULL, 'manual', '2026-01-03T11:00:00Z', '2026-01-03T11:00:00Z');

INSERT INTO narrator_tool_calls (id, narrator_id, run_id, message_id, tool_use_id, tool_name, input_json, status, created_at, updated_at) VALUES
  ('tool-call-1', 'narrator-1', 'run-1', 'message-2', 'toolu_01', 'Shell', '{"command":"echo 測試"}', 'completed', '2026-01-03T09:05:00Z', ''),
  ('tool-call-2', 'narrator-1', 'run-2', NULL, 'toolu_02', 'Read', NULL, 'pending', '2026-01-03T11:05:00Z', '2026-01-03T11:05:00Z');

INSERT INTO narrator_message_attachments (id, message_id, narrator_id, filename, mime_type, kind, size_bytes, data_blob, created_at) VALUES
  ('attachment-1', 'message-1', 'narrator-1', '截圖 screenshot.png', 'image/png', 'image', 3, X'89504E', '2026-01-03T10:00:00.500000000Z');

INSERT INTO api_requests (id, narrator_id, run_id, message_id, kind, provider, model, input_tokens, output_tokens, cost_usd, created_at) VALUES
  ('api-request-1', 'narrator-1', 'run-1', 'message-2', 'model', 'anthropic', 'claude', 100, 50, 0.01, '2026-01-03T09:06:00Z');

INSERT INTO skills (id, name, command, description, prompt, source, scope, project_id, chapter_id, content_hash, enabled, scan_verdict, scan_findings_json, scanner_version, created_at, updated_at) VALUES
  ('skill-1', '部署技能', 'deploy', 'Deploys 🚀', '執行部署，然後回報結果。', 'manual', 'global', NULL, NULL, 'hash-1', 1, 'safe', '[]', 0, '2026-01-04T00:00:00Z', '2026-01-04T00:00:00Z');

INSERT INTO skill_audit_events (id, action, actor, skill_id, content_hash, scan_verdict, finding_codes_json, created_at) VALUES
  ('audit-1', 'create', 'tester', 'skill-1', 'hash-1', 'safe', '[]', '2026-01-04T00:00:01Z');

INSERT INTO workflow_preferences (id, require_confirmation_for_exec, danger_reflection_enabled, danger_reflection_level, created_at, updated_at) VALUES
  ('prefs-1', 1, 0, 'medium', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');

INSERT INTO notification_settings (id, enabled, webhook_url, created_at, updated_at) VALUES
  ('notify-1', 1, NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`

// buildSeededLegacyV1DB reproduces a populated database as it stood at
// version 1 of the migration chain: the pre-v13 legacy naming schema plus
// representative data.
//
// The schema is executed on a foreign_keys=ON connection (schemaSQL turns the
// pragma on itself) but seeding runs with it off, matching how the data
// accumulated historically: runs.plan_id, for instance, references a plans
// table that migration 38 has not created yet, which would make every INSERT
// into runs unpreparable if enforcement were left on.
func buildSeededLegacyV1DB(t *testing.T, ctx context.Context, path string) *sql.DB {
	t.Helper()
	database := openUpgradeTestDB(t, path)
	if _, err := database.ExecContext(ctx, legacyNamingSchemaSQL()); err != nil {
		t.Fatalf("build legacy schema: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, seedLegacyFixtureSQL); err != nil {
		t.Fatalf("seed legacy fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	return database
}

func assertNoForeignKeyViolations(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	violations := make([]string, 0)
	for rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			t.Fatalf("scan foreign_key_check: %v", err)
		}
		violations = append(violations, fmt.Sprintf("%s rowid=%v -> %s (fk %d)", table, rowid.Int64, parent, fkid))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Errorf("migrated database has foreign key violations:\n%s", strings.Join(violations, "\n"))
	}
}

func queryString(t *testing.T, ctx context.Context, database *sql.DB, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := database.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value.String
}

func queryInt(t *testing.T, ctx context.Context, database *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := database.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value
}

// assertSeededDataSurvived is the cheap check the per-version loop runs: row
// counts of every seeded table, plus one value per class of rewrite (unicode
// text, backfilled run generations) so that a migration that silently drops or
// mangles rows fails every starting version it affects.
func assertSeededDataSurvived(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for table, want := range map[string]int{
		"users":                     2,
		"projects":                  2,
		"worklines":                 2,
		"agents":                    3,
		"agent_messages":            4,
		"runs":                      2,
		"agent_tool_calls":          2,
		"agent_message_attachments": 1,
		"api_requests":              1,
		"skills":                    1,
		"skill_revisions":           1,
		"skill_audit_events":        1,
	} {
		if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM `+table); got != want {
			t.Errorf("table %s: %d rows survived, want %d", table, got, want)
		}
	}
	if got := queryString(t, ctx, database, `SELECT content_text FROM agent_messages WHERE id = 'message-1'`); got != `你好，世界！👋 <script>alert("x")</script>` {
		t.Errorf("message-1 content mangled: %q", got)
	}
	if got := queryInt(t, ctx, database, `SELECT execution_generation FROM runs WHERE id = 'run-1'`); got != 1 {
		t.Errorf("run-1 execution_generation = %d, want backfilled 1", got)
	}
	if got := queryInt(t, ctx, database, `SELECT execution_generation FROM runs WHERE id = 'run-2'`); got != 2 {
		t.Errorf("run-2 execution_generation = %d, want backfilled 2", got)
	}
}

// TestUpgradeFromLegacyV1PreservesData walks a populated legacy database
// through all 64 incremental migrations and pins down what each data-moving
// migration was required to do to the seeded rows.
func TestUpgradeFromLegacyV1PreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v1.db")
	database := buildSeededLegacyV1DB(t, ctx, path)

	if err := runMigrations(ctx, database); err != nil {
		t.Fatalf("upgrade from legacy v1: %v", err)
	}
	version, err := getUserVersion(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentDBVersion {
		t.Fatalf("upgrade stopped at version %d, want %d", version, CurrentDBVersion)
	}
	assertNoForeignKeyViolations(t, ctx, database)
	assertSeededDataSurvived(t, ctx, database)

	// v13: legacy names must be gone, and rows must have followed the rename.
	for _, legacy := range []string{"chapters", "narrators", "narrator_messages", "narrator_tool_calls", "narrator_message_attachments"} {
		if testTableExists(t, ctx, database, legacy) {
			t.Errorf("legacy table %s still exists after upgrade", legacy)
		}
	}
	if got := queryString(t, ctx, database, `SELECT title FROM agents WHERE id = 'narrator-1'`); got != "主要代理 🤖" {
		t.Errorf("agent title = %q, want unicode preserved", got)
	}
	if got := queryString(t, ctx, database, `SELECT title FROM agents WHERE id = 'narrator-2'`); got != "" {
		t.Errorf("empty agent title became %q", got)
	}
	if got := queryString(t, ctx, database, `SELECT parent_agent_id FROM agents WHERE id = 'narrator-3'`); got != "narrator-1" {
		t.Errorf("subagent parent = %q, want narrator-1", got)
	}
	if got := queryString(t, ctx, database, `SELECT workline_id FROM agents WHERE id = 'narrator-2'`); got != "chapter-2" {
		t.Errorf("agent workline = %q, want chapter-2", got)
	}

	// v23: generations, trigger types and durations are derived from the rows.
	if got := queryString(t, ctx, database, `SELECT trigger_type FROM runs WHERE id = 'run-1'`); got != "scheduled" {
		t.Errorf("run-1 trigger_type = %q, want scheduled (mapped from source)", got)
	}
	if got := queryString(t, ctx, database, `SELECT trigger_type FROM runs WHERE id = 'run-2'`); got != "manual" {
		t.Errorf("run-2 trigger_type = %q, want manual", got)
	}
	if got := queryInt(t, ctx, database, `SELECT duration_ms FROM runs WHERE id = 'run-1'`); got != 600000 {
		t.Errorf("run-1 duration_ms = %d, want 600000 derived from start/completion", got)
	}
	if got := queryInt(t, ctx, database, `SELECT execution_generation FROM agents WHERE id = 'narrator-1'`); got != 2 {
		t.Errorf("agent execution_generation = %d, want max run generation 2", got)
	}

	// v6: a run with a git snapshot becomes checkpoint-ready.
	if got := queryString(t, ctx, database, `SELECT checkpoint_state FROM runs WHERE id = 'run-1'`); got != "ready" {
		t.Errorf("run-1 checkpoint_state = %q, want ready", got)
	}

	// v25: an in-range reasoning effort passes through the normalization.
	if got := queryString(t, ctx, database, `SELECT reasoning_effort FROM agents WHERE id = 'narrator-2'`); got != "high" {
		t.Errorf("reasoning effort = %q, want high", got)
	}
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM runtime_settings WHERE id = 'default'`); got != 1 {
		t.Error("v25 did not install the default runtime settings row")
	}

	// v31: lifecycle timestamps are backfilled from created_at, and only for
	// statuses that logically had them.
	if got := queryString(t, ctx, database, `SELECT started_at FROM agent_tool_calls WHERE id = 'tool-call-1'`); got != "2026-01-03T09:05:00.000000000Z" {
		t.Errorf("completed tool call started_at = %q, want backfilled+padded created_at", got)
	}
	if got := queryString(t, ctx, database, `SELECT updated_at FROM agent_tool_calls WHERE id = 'tool-call-1'`); got != "2026-01-03T09:05:00.000000000Z" {
		t.Errorf("blank tool call updated_at = %q, want backfilled created_at", got)
	}
	var pendingStarted sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT started_at FROM agent_tool_calls WHERE id = 'tool-call-2'`).Scan(&pendingStarted); err != nil {
		t.Fatal(err)
	}
	if pendingStarted.Valid {
		t.Errorf("pending tool call gained started_at %q", pendingStarted.String)
	}

	// v32: handles are backfilled from usernames, unicode included.
	if got := queryString(t, ctx, database, `SELECT handle FROM users WHERE id = 'user-1'`); got != "ray" {
		t.Errorf("user-1 handle = %q, want ray", got)
	}
	if got := queryString(t, ctx, database, `SELECT handle FROM users WHERE id = 'user-2'`); got != "雷伊Ray" {
		t.Errorf("user-2 handle = %q, want unicode username", got)
	}

	// v35: the earliest user becomes owner of every unowned project.
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM project_members WHERE user_id = 'user-1' AND role = 'owner'`); got != 2 {
		t.Errorf("project_members owner rows = %d, want 2", got)
	}

	// v15: the enabled skill got a baseline revision and the audit event
	// survived the table rebuild.
	if got := queryString(t, ctx, database, `SELECT operation FROM skill_revisions WHERE skill_id = 'skill-1'`); got != "baseline" {
		t.Errorf("skill revision operation = %q, want baseline", got)
	}
	if got := queryString(t, ctx, database, `SELECT prompt FROM skill_revisions WHERE skill_id = 'skill-1'`); got != "執行部署，然後回報結果。" {
		t.Errorf("skill revision prompt = %q, want seeded prompt", got)
	}
	if got := queryString(t, ctx, database, `SELECT actor FROM skill_audit_events WHERE id = 'audit-1'`); got != "tester" {
		t.Errorf("audit event actor = %q, want preserved through rebuild", got)
	}

	// v52: whole-second timestamps are padded, fractional ones kept verbatim.
	if got := queryString(t, ctx, database, `SELECT created_at FROM agent_messages WHERE id = 'message-1'`); got != "2026-01-03T10:00:00.000000000Z" {
		t.Errorf("message-1 created_at = %q, want padded", got)
	}
	if got := queryString(t, ctx, database, `SELECT created_at FROM agent_messages WHERE id = 'message-2'`); got != "2026-01-03T10:00:01.123456789Z" {
		t.Errorf("message-2 created_at = %q, want untouched", got)
	}
	if got := queryString(t, ctx, database, `SELECT created_at FROM agent_message_attachments WHERE id = 'attachment-1'`); got != "2026-01-03T10:00:00.500000000Z" {
		t.Errorf("attachment created_at = %q, want untouched", got)
	}

	// Message boundary values: empty string and NULL must stay distinct.
	if got := queryString(t, ctx, database, `SELECT content_text FROM agent_messages WHERE id = 'message-2'`); got != "" {
		t.Errorf("empty message content became %q", got)
	}
	var nullContent sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT content_text FROM agent_messages WHERE id = 'message-3'`).Scan(&nullContent); err != nil {
		t.Fatal(err)
	}
	if nullContent.Valid {
		t.Errorf("NULL message content became %q", nullContent.String)
	}

	// v57: the boolean danger reflection toggle maps to a level.
	if got := queryString(t, ctx, database, `SELECT danger_reflection_level FROM workflow_preferences WHERE id = 'prefs-1'`); got != "off" {
		t.Errorf("danger_reflection_level = %q, want off (from enabled=0)", got)
	}

	// Tables the legacy bootstrap creates ahead of their own migrations must
	// end with modern column names, or the store's queries fail only for
	// upgraded-from-legacy users.
	for _, table := range []string{"message_drafts", "agent_message_queue", "agent_queued_message_attachments", "memories"} {
		hasModern, err := columnExistsInDB(ctx, database, table, "agent_id")
		if err != nil {
			t.Fatal(err)
		}
		hasLegacy, err := columnExistsInDB(ctx, database, table, "narrator_id")
		if err != nil {
			t.Fatal(err)
		}
		if !hasModern || hasLegacy {
			t.Errorf("table %s: agent_id present=%v narrator_id present=%v; want the modern column only", table, hasModern, hasLegacy)
		}
	}

	// The production entry point must accept the result and stay at the
	// current version (Open also runs runtime-settings and skill
	// revalidation, which must tolerate the migrated rows).
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after legacy upgrade: %v", err)
	}
	defer store.Close()
	reopenedVersion, err := getUserVersion(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if reopenedVersion != CurrentDBVersion {
		t.Fatalf("version after reopen = %d, want %d", reopenedVersion, CurrentDBVersion)
	}
	if got := queryInt(t, ctx, store.DB(), `SELECT COUNT(*) FROM agents`); got != 3 {
		t.Errorf("agents after reopen = %d, want 3", got)
	}
}

func columnExistsInDB(ctx context.Context, database *sql.DB, table, column string) (bool, error) {
	return columnExists(ctx, database, table, column)
}

// TestUpgradedLegacySchemaMatchesFreshInstall reuses the parity machinery from
// schema_parity_test.go against the third schema route: legacy naming schema,
// then every migration. Tables and column sets must match a fresh install
// exactly in both directions; index signatures must match as sets (names
// legitimately differ, e.g. idx_chapters_parent_chapter survives the v13
// rename with its old name but indexes the same modern column).
func TestUpgradedLegacySchemaMatchesFreshInstall(t *testing.T) {
	ctx := context.Background()
	upgraded := buildSeededLegacyV1DB(t, ctx, filepath.Join(t.TempDir(), "legacy-parity.db"))
	if err := runMigrations(ctx, upgraded); err != nil {
		t.Fatalf("upgrade from legacy v1: %v", err)
	}
	fresh := freshInstallSchema(t)

	freshTables := make([]string, 0)
	for _, object := range dumpSchemaObjects(t, fresh) {
		if object.kind == "table" {
			freshTables = append(freshTables, object.name)
		}
	}
	sort.Strings(freshTables)
	upgradedTables := make([]string, 0)
	for _, object := range dumpSchemaObjects(t, upgraded) {
		if object.kind == "table" {
			upgradedTables = append(upgradedTables, object.name)
		}
	}
	sort.Strings(upgradedTables)
	if !reflect.DeepEqual(freshTables, upgradedTables) {
		t.Fatalf("table sets differ\nfresh:    %v\nupgraded: %v", freshTables, upgradedTables)
	}

	for _, table := range freshTables {
		freshColumns := tableColumns(t, fresh, table)
		upgradedColumns := tableColumns(t, upgraded, table)
		if missing := columnDifference(freshColumns, upgradedColumns); len(missing) > 0 {
			t.Errorf("table %s: columns %v exist in a fresh install but not after a legacy upgrade", table, missing)
		}
		if extra := columnDifference(upgradedColumns, freshColumns); len(extra) > 0 {
			t.Errorf("table %s: columns %v exist after a legacy upgrade but not in a fresh install", table, extra)
		}

		freshIndexes := indexSignature(t, fresh, table)
		upgradedIndexes := indexSignature(t, upgraded, table)
		for signature := range freshIndexes {
			if _, ok := upgradedIndexes[signature]; !ok {
				t.Errorf("index %s exists in a fresh install but not after a legacy upgrade", signature)
			}
		}
		for signature := range upgradedIndexes {
			if _, ok := freshIndexes[signature]; !ok {
				t.Errorf("index %s exists after a legacy upgrade but not in a fresh install", signature)
			}
		}
	}
}

// TestUpgradeFromEveryIntermediateVersion proves that stopping after any
// migration and finishing later works. Because every migration commits its own
// transaction and stamps user_version, "a database at version N" is exactly
// what a process killed between migrations N and N+1 leaves behind; every N
// must therefore be a valid resume point, with data intact at the end.
func TestUpgradeFromEveryIntermediateVersion(t *testing.T) {
	ctx := context.Background()
	for n := 1; n <= CurrentDBVersion; n++ {
		t.Run(fmt.Sprintf("from_v%02d", n), func(t *testing.T) {
			database := buildSeededLegacyV1DB(t, ctx, filepath.Join(t.TempDir(), "step.db"))
			for _, m := range migrations {
				if m.version < 2 || m.version > n {
					continue
				}
				if err := runMigration(ctx, database, m); err != nil {
					t.Fatalf("replay migration %d %s: %v", m.version, m.name, err)
				}
			}
			if err := runMigrations(ctx, database); err != nil {
				t.Fatalf("resume upgrade from version %d: %v", n, err)
			}
			version, err := getUserVersion(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			if version != CurrentDBVersion {
				t.Fatalf("resumed upgrade reached version %d, want %d", version, CurrentDBVersion)
			}
			assertNoForeignKeyViolations(t, ctx, database)
			assertSeededDataSurvived(t, ctx, database)
		})
	}
}

// TestMigrateIsNoOpAtCurrentVersion pins idempotency at the top of the chain:
// running migrations against an already-current database must succeed and must
// not touch a single schema object. This is what every process restart does.
func TestMigrateIsNoOpAtCurrentVersion(t *testing.T) {
	ctx := context.Background()
	database := openMigratedTestDB(t, "noop")
	if err := runMigrations(ctx, database); err != nil {
		t.Fatal(err)
	}
	before := dumpSchemaObjects(t, database)

	if err := runMigrations(ctx, database); err != nil {
		t.Fatalf("second migrate on a current database: %v", err)
	}
	version, err := getUserVersion(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentDBVersion {
		t.Fatalf("version after re-migrate = %d, want %d", version, CurrentDBVersion)
	}
	after := dumpSchemaObjects(t, database)
	if !reflect.DeepEqual(before, after) {
		t.Error("re-running migrations on a current database changed schema objects")
	}
}

// TestMigrateRejectsFutureDatabaseVersion: opening a database written by a
// newer build must fail loudly instead of running stale migrations over a
// schema they do not understand.
func TestMigrateRejectsFutureDatabaseVersion(t *testing.T) {
	ctx := context.Background()
	database := openUpgradeTestDB(t, filepath.Join(t.TempDir(), "future.db"))
	if _, err := database.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, CurrentDBVersion+1)); err != nil {
		t.Fatal(err)
	}
	err := runMigrations(ctx, database)
	if err == nil {
		t.Fatal("migrating a future-version database succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("unexpected error for a future-version database: %v", err)
	}
}

// TestFailedMigrationRollsBackCompletely verifies the transactional contract
// runMigration promises: when a migration fails partway, nothing it did may
// remain — no half schema, no version bump — and once the obstacle is removed
// the same database must finish the upgrade.
//
// The failure is injected without touching production code: v15 rebuilds the
// audit table through a scratch table it creates with a bare CREATE TABLE, so
// planting a table by that name makes v15 fail after it has already renamed
// skill columns, created skill_revisions and backfilled baselines — a genuine
// mid-transaction crash point.
func TestFailedMigrationRollsBackCompletely(t *testing.T) {
	ctx := context.Background()
	database := buildSeededLegacyV1DB(t, ctx, filepath.Join(t.TempDir(), "poison.db"))
	for _, m := range migrations {
		if m.version < 2 || m.version > 14 {
			continue
		}
		if err := runMigration(ctx, database, m); err != nil {
			t.Fatalf("replay migration %d %s: %v", m.version, m.name, err)
		}
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE skill_audit_events_v15 (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	err := runMigrations(ctx, database)
	if err == nil {
		t.Fatal("expected migration 15 to fail against the planted scratch table")
	}
	if !strings.Contains(err.Error(), "migration 15") {
		t.Fatalf("failure was not attributed to migration 15: %v", err)
	}

	version, err := getUserVersion(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Fatalf("version after failed migration = %d, want 14 (the last committed one)", version)
	}
	// Markers of work v15 performs earlier in the same transaction, each of
	// which must have been undone. The skill_revisions *table* is no marker —
	// the legacy bootstrap schema already carries it — but its backfilled
	// baseline row, the migration-only scope-shape trigger, and the v8-era
	// index that v15 drops all are.
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM skill_revisions`); got != 0 {
		t.Errorf("failed v15 left %d backfilled skill revisions behind; its transaction did not roll back", got)
	}
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'skills_scope_shape_insert'`); got != 0 {
		t.Error("failed v15 left the skills_scope_shape_insert trigger behind; its transaction did not roll back")
	}
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_skills_command_nocase'`); got != 1 {
		t.Error("failed v15 dropped idx_skills_command_nocase permanently; its transaction did not roll back")
	}
	if hasChapterID, err := columnExistsInDB(ctx, database, "skills", "chapter_id"); err != nil {
		t.Fatal(err)
	} else if !hasChapterID {
		t.Error("failed v15 left skills.chapter_id renamed; its transaction did not roll back")
	}
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM skills WHERE id = 'skill-1' AND enabled = 1`); got != 1 {
		t.Error("failed migration corrupted seeded skill data")
	}

	// Remove the obstacle: the interrupted upgrade must resume and finish.
	if _, err := database.ExecContext(ctx, `DROP TABLE skill_audit_events_v15`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, database); err != nil {
		t.Fatalf("resume after removing the obstacle: %v", err)
	}
	version, err = getUserVersion(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentDBVersion {
		t.Fatalf("resumed upgrade reached version %d, want %d", version, CurrentDBVersion)
	}
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'skills_scope_shape_insert'`); got != 1 {
		t.Error("resumed v15 did not create the skills_scope_shape_insert trigger")
	}
	assertNoForeignKeyViolations(t, ctx, database)
	assertSeededDataSurvived(t, ctx, database)
}

// TestLegacyZeroVersionUpgradePreservesAncientData exercises the oldest
// supported input: a populated pre-versioning database (user_version 0). Such
// a database has only the original core tables, in shapes far narrower than
// legacyNamingSchemaSQL — migrateLegacyZeroVersion has to widen them with
// ensureLegacyColumns before the legacy indexes can even be created, and the
// full chain has to run afterwards.
func TestLegacyZeroVersionUpgradePreservesAncientData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ancient-v0.db")
	database := openUpgradeTestDB(t, path)
	if _, err := database.ExecContext(ctx, `
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE chapters (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  title TEXT NOT NULL,
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
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE narrator_messages (
  id TEXT PRIMARY KEY,
  narrator_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content_text TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE narrator_tool_calls (
  id TEXT PRIMARY KEY,
  narrator_id TEXT NOT NULL,
  tool_use_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE api_requests (
  id TEXT PRIMARY KEY,
  narrator_id TEXT,
  created_at TEXT NOT NULL
);
INSERT INTO users (id, username, created_at) VALUES ('user-legacy', 'oldtimer', '2025-01-01T00:00:00Z');
INSERT INTO projects (id, name, created_at, updated_at) VALUES ('project-legacy', '舊專案', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z');
INSERT INTO chapters (id, project_id, title, created_at, updated_at) VALUES ('chapter-legacy', 'project-legacy', 'main', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z');
INSERT INTO narrators (id, chapter_id, title, model, created_at, updated_at) VALUES ('narrator-legacy', 'chapter-legacy', '老代理 📜', 'fake:test', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z');
INSERT INTO narrator_messages (id, narrator_id, role, content_text, created_at) VALUES
  ('message-legacy-1', 'narrator-legacy', 'user', '古老訊息 🏺', '2025-01-01T00:00:01Z'),
  ('message-legacy-2', 'narrator-legacy', 'assistant', NULL, '2025-01-01T00:00:02Z');
INSERT INTO narrator_tool_calls (id, narrator_id, tool_use_id, tool_name, status, created_at, updated_at) VALUES
  ('tool-legacy', 'narrator-legacy', 'toolu_legacy', 'Shell', 'completed', '2025-01-01T00:00:03Z', '2025-01-01T00:00:03Z');
INSERT INTO api_requests (id, narrator_id, created_at) VALUES ('request-legacy', 'narrator-legacy', '2025-01-01T00:00:04Z');
`); err != nil {
		t.Fatal(err)
	}

	if err := runMigrations(ctx, database); err != nil {
		t.Fatalf("upgrade from a version-0 legacy database: %v", err)
	}
	version, err := getUserVersion(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if version != CurrentDBVersion {
		t.Fatalf("version-0 upgrade reached %d, want %d", version, CurrentDBVersion)
	}
	assertNoForeignKeyViolations(t, ctx, database)

	if got := queryString(t, ctx, database, `SELECT title FROM agents WHERE id = 'narrator-legacy'`); got != "老代理 📜" {
		t.Errorf("ancient agent title = %q, want preserved", got)
	}
	if got := queryString(t, ctx, database, `SELECT content_text FROM agent_messages WHERE id = 'message-legacy-1'`); got != "古老訊息 🏺" {
		t.Errorf("ancient message content = %q, want preserved", got)
	}
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM agent_messages`); got != 2 {
		t.Errorf("ancient messages = %d rows, want 2", got)
	}
	if got := queryString(t, ctx, database, `SELECT role FROM users WHERE id = 'user-legacy'`); got != "user" {
		t.Errorf("ancient user role = %q, want backfilled default", got)
	}
	if got := queryString(t, ctx, database, `SELECT handle FROM users WHERE id = 'user-legacy'`); got != "oldtimer" {
		t.Errorf("ancient user handle = %q, want backfilled from username", got)
	}
	if got := queryString(t, ctx, database, `SELECT tool_name FROM agent_tool_calls WHERE id = 'tool-legacy'`); got != "Shell" {
		t.Errorf("ancient tool call = %q, want preserved", got)
	}
	if got := queryInt(t, ctx, database, `SELECT COUNT(*) FROM api_requests`); got != 1 {
		t.Errorf("ancient api_requests = %d rows, want 1", got)
	}

	// The bootstrap also creates modern tables ahead of their migrations; they
	// must come out with the column names the store queries.
	for _, table := range []string{"message_drafts", "agent_message_queue", "agent_queued_message_attachments", "memories"} {
		hasModern, err := columnExistsInDB(ctx, database, table, "agent_id")
		if err != nil {
			t.Fatal(err)
		}
		hasLegacy, err := columnExistsInDB(ctx, database, table, "narrator_id")
		if err != nil {
			t.Fatal(err)
		}
		if !hasModern || hasLegacy {
			t.Errorf("table %s: agent_id present=%v narrator_id present=%v; want the modern column only", table, hasModern, hasLegacy)
		}
	}

	// The production entry point must also accept this database.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after version-0 upgrade: %v", err)
	}
	defer store.Close()
}
