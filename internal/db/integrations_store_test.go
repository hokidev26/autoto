package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendRegistryActivatesSingleBackend(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.CreateBackend(ctx, Backend{Name: "Local", Kind: "local", BaseURL: "http://127.0.0.1:8000"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Active {
		t.Fatal("expected first backend to become active")
	}
	second, err := store.CreateBackend(ctx, Backend{Name: "Cloud", Kind: "cloud", BaseURL: "https://example.test", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Active {
		t.Fatal("expected requested backend to become active")
	}

	backends, err := store.ListBackends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activeCount := 0
	for _, backend := range backends {
		if backend.Active {
			activeCount++
			if backend.ID != second.ID {
				t.Fatalf("expected second backend active, got %s", backend.ID)
			}
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected exactly one active backend, got %d", activeCount)
	}
}

func TestMCPServerRegistryRoundTripsConfig(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreateMCPServer(ctx, MCPServer{Name: "Fake", Transport: "stdio", Command: "node", Args: []string{"server.js"}, CWD: "/tmp", Env: map[string]string{"TOKEN": "secret"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetMCPServer(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "node" || got.Args[0] != "server.js" || got.Env["TOKEN"] != "secret" || !got.Enabled {
		t.Fatalf("unexpected MCP server round trip: %+v", got)
	}

	got.Enabled = false
	got.Args = []string{"other.js"}
	updated, err := store.UpdateMCPServer(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.Args[0] != "other.js" {
		t.Fatalf("unexpected MCP server update: %+v", updated)
	}

	servers, err := store.ListMCPServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].ID != created.ID {
		t.Fatalf("expected one MCP server, got %+v", servers)
	}
	if err := store.DeleteMCPServer(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMCPServer(ctx, created.ID); !IsNotFound(err) {
		t.Fatalf("expected deleted MCP server to be missing, got %v", err)
	}
}

func TestNotificationSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	settings, err := store.GetNotificationSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ID != "default" || settings.Enabled || !settings.NotifyOnApproval || !settings.NotifyOnDone || !settings.NotifyOnError {
		t.Fatalf("unexpected default notification settings: %+v", settings)
	}
	updated, err := store.UpdateNotificationSettings(ctx, NotificationSettings{Enabled: true, WebhookURL: " https://example.test/hook ", NotifyOnApproval: true, NotifyOnDone: false, NotifyOnError: true})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Enabled || updated.WebhookURL != "https://example.test/hook" || updated.NotifyOnDone {
		t.Fatalf("unexpected updated notification settings: %+v", updated)
	}
}

func TestOpenMigratesVersionTwoDatabaseToNotificationSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE notification_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
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
	if !testTableExists(t, ctx, store.DB(), "notification_settings") {
		t.Fatal("expected notification_settings table after migration")
	}
}

func TestWorkflowPreferencesRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	prefs, err := store.GetWorkflowPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.ID != "default" || !prefs.RequireConfirmationForExec || prefs.RequireConfirmationForWrites || !prefs.AllowReadOnlyByDefault || !prefs.DangerReflectionEnabled {
		t.Fatalf("unexpected default workflow preferences: %+v", prefs)
	}
	updated, err := store.UpdateWorkflowPreferences(ctx, WorkflowPreferences{RequireConfirmationForExec: false, RequireConfirmationForWrites: true, AllowReadOnlyByDefault: false})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequireConfirmationForExec || !updated.RequireConfirmationForWrites || updated.AllowReadOnlyByDefault {
		t.Fatalf("unexpected updated workflow preferences: %+v", updated)
	}
}

func TestToolPermissionRuleCRUD(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	low, err := store.CreateToolPermissionRule(ctx, ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "ask", Priority: 1, Enabled: true, Description: "ask bash"})
	if err != nil {
		t.Fatal(err)
	}
	high, err := store.CreateToolPermissionRule(ctx, ToolPermissionRule{Mode: "*", ToolName: "Bash", Risk: "exec", Decision: "deny", Priority: 10, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := store.ListToolPermissionRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[0].ID != high.ID || rules[1].ID != low.ID {
		t.Fatalf("expected priority ordering high then low, got %+v", rules)
	}
	low.Decision = "allow"
	low.Enabled = false
	updated, err := store.UpdateToolPermissionRule(ctx, low)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Decision != "allow" || updated.Enabled || updated.CreatedAt != low.CreatedAt {
		t.Fatalf("unexpected updated rule: %+v", updated)
	}
	if err := store.DeleteToolPermissionRule(ctx, high.ID); err != nil {
		t.Fatal(err)
	}
	rules, err = store.ListToolPermissionRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].ID != low.ID {
		t.Fatalf("expected only low rule after delete, got %+v", rules)
	}
}

func TestToolPermissionRuleOrderingUsesSpecificityAndSafeTieBreak(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	wildcardDeny, err := store.CreateToolPermissionRule(ctx, ToolPermissionRule{Mode: "*", ToolName: "*", Risk: "exec", Decision: "deny", Priority: 30, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	exactAllow, err := store.CreateToolPermissionRule(ctx, ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "allow", Priority: 30, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	exactAsk, err := store.CreateToolPermissionRule(ctx, ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "ask", Priority: 30, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	exactDeny, err := store.CreateToolPermissionRule(ctx, ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "deny", Priority: 30, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	rules, err := store.ListToolPermissionRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{exactDeny.ID, exactAsk.ID, exactAllow.ID, wildcardDeny.ID}
	if len(rules) != len(want) {
		t.Fatalf("expected %d ordered rules, got %+v", len(want), rules)
	}
	for i, id := range want {
		if rules[i].ID != id {
			t.Fatalf("unexpected rule order at %d: want %s, got %+v", i, id, rules)
		}
	}
}

func TestStoreRejectsUnsafeToolPermissionRules(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	valid, err := store.CreateToolPermissionRule(ctx, ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "allow", Priority: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	invalid := []ToolPermissionRule{
		{Mode: "root", ToolName: "Bash", Risk: "exec", Decision: "ask"},
		{Mode: "acceptEdits", ToolName: "Bash", Risk: "unknown", Decision: "ask"},
		{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "approve"},
		{Mode: "acceptEdits", ToolName: "Bash", Risk: "danger", Decision: "allow"},
		{Mode: "acceptEdits", ToolName: "Bash", Risk: "*", Decision: "allow"},
		{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "ask", Priority: maxStoredToolPermissionPriority + 1},
	}
	for _, rule := range invalid {
		if _, err := store.CreateToolPermissionRule(ctx, rule); err == nil {
			t.Fatalf("expected direct store create to reject %+v", rule)
		}
	}
	valid.Risk = "danger"
	valid.Decision = "allow"
	if _, err := store.UpdateToolPermissionRule(ctx, valid); err == nil {
		t.Fatal("expected direct store update to reject danger allow")
	}
	persisted, err := store.GetToolPermissionRule(ctx, valid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Risk != "exec" || persisted.Decision != "allow" {
		t.Fatalf("invalid update should not persist, got %+v", persisted)
	}
}

func TestOpenMigratesVersionThreeDatabaseToWorkflowPermissions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v3.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE workflow_preferences`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE tool_permission_rules`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
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
	for _, table := range []string{"workflow_preferences", "tool_permission_rules"} {
		if !testTableExists(t, ctx, store.DB(), table) {
			t.Fatalf("expected %s table after migration", table)
		}
	}
}

func TestIntegrationConnectionCRUDValidationAndConflicts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreateIntegrationConnection(ctx, IntegrationConnection{
		Kind: " github ", Name: " primary ", Enabled: true, Endpoint: " https://api.example.test ",
		SettingsJSON: json.RawMessage(`{"region":"us","retry":{"count":2},"labels":["one"]}`),
		SecretRefs:   map[string]string{"apiKey": "env:GITHUB_API_KEY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Kind != "github" || created.Name != "primary" || created.Endpoint != "https://api.example.test" || !created.SecretConfigured["apiKey"] {
		t.Fatalf("unexpected created integration connection: %+v", created)
	}
	if string(created.SettingsJSON) != `{"labels":["one"],"region":"us","retry":{"count":2}}` {
		t.Fatalf("expected canonical settings JSON, got %s", created.SettingsJSON)
	}

	got, err := store.GetIntegrationConnection(ctx, " "+created.ID+" ")
	if err != nil {
		t.Fatal(err)
	}
	if got.SecretRefs["apiKey"] != "env:GITHUB_API_KEY" || !got.SecretConfigured["apiKey"] {
		t.Fatalf("unexpected stored references: %+v", got)
	}
	listed, err := store.ListIntegrationConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("unexpected integration list: %+v", listed)
	}

	if _, err := store.CreateIntegrationConnection(ctx, IntegrationConnection{Kind: "github", Name: "primary"}); !IsConflict(err) {
		t.Fatalf("expected kind/name uniqueness conflict, got %v", err)
	}
	created.Name = "secondary"
	created.Enabled = false
	created.SecretRefs = map[string]string{"token": "env:GITHUB_TOKEN"}
	updated, err := store.UpdateIntegrationConnection(ctx, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "secondary" || updated.Enabled || updated.SecretRefs["token"] != "env:GITHUB_TOKEN" || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("unexpected updated integration: %+v", updated)
	}
	if _, err := store.UpdateIntegrationConnection(ctx, IntegrationConnection{ID: "missing", Kind: "github", Name: "missing"}); !IsNotFound(err) {
		t.Fatalf("expected missing update to be not found, got %v", err)
	}
	if err := store.DeleteIntegrationConnection(ctx, updated.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIntegrationConnection(ctx, updated.ID); !IsNotFound(err) {
		t.Fatalf("expected missing delete to be not found, got %v", err)
	}
}

func TestIntegrationConnectionRejectsSensitiveSettingsAndInvalidRefs(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	invalidSettings := []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`null`),
		json.RawMessage(`{"broken":`),
		json.RawMessage(`{"password":"raw"}`),
		json.RawMessage(`{"nested":{"apiKey":"raw"}}`),
		json.RawMessage(`{"items":[{"access_token":"raw"}]}`),
		json.RawMessage(`{"credentialFile":"raw"}`),
		json.RawMessage(`{"note":"` + strings.Repeat("x", IntegrationSettingsMaxBytes) + `"}`),
	}
	for index, settings := range invalidSettings {
		_, err := store.CreateIntegrationConnection(ctx, IntegrationConnection{Kind: "test", Name: fmt.Sprintf("settings-%d", index), SettingsJSON: settings})
		if err == nil {
			t.Fatalf("expected invalid settings to fail: %s", settings)
		}
	}

	invalidRefs := []string{
		"raw-secret-value",
		"file:/tmp/secret",
		"env:",
		"env:9INVALID",
		"env:HAS-DASH",
		"env:HAS SPACE",
		"env:HAS\nNEWLINE",
		" env:LEADING_SPACE",
	}
	for index, ref := range invalidRefs {
		_, err := store.CreateIntegrationConnection(ctx, IntegrationConnection{Kind: "test", Name: fmt.Sprintf("ref-%d", index), SecretRefs: map[string]string{"token": ref}})
		if err == nil {
			t.Fatalf("expected invalid secret reference %q to fail", ref)
		}
	}
	if _, err := store.CreateIntegrationConnection(ctx, IntegrationConnection{Kind: "test", Name: "bad-logical", SecretRefs: map[string]string{"bad key": "env:VALID"}}); err == nil {
		t.Fatal("expected invalid logical secret name to fail")
	}
}

func TestIntegrationConnectionFreshSchemaAndV16MigrationMatch(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if version := readUserVersion(t, ctx, fresh.DB()); version != CurrentDBVersion {
		t.Fatalf("expected fresh database version %d, got %d", CurrentDBVersion, version)
	}
	freshSchema := integrationConnectionSchemaSnapshot(t, ctx, fresh.DB())
	for _, name := range []string{"integration_connections", "idx_integration_connections_enabled", "idx_integration_connections_kind"} {
		if !strings.Contains(freshSchema, name) {
			t.Fatalf("fresh integration schema missing %s: %s", name, freshSchema)
		}
	}

	path := filepath.Join(t.TempDir(), "v16.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE integration_connections; PRAGMA user_version = 16`); err != nil {
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
		t.Fatalf("expected migrated database version %d, got %d", CurrentDBVersion, version)
	}
	migratedSchema := integrationConnectionSchemaSnapshot(t, ctx, migrated.DB())
	if migratedSchema != freshSchema {
		t.Fatalf("fresh and migrated integration schemas differ\nfresh:\n%s\nmigrated:\n%s", freshSchema, migratedSchema)
	}
}

func integrationConnectionSchemaSnapshot(t *testing.T, ctx context.Context, database *sql.DB) string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'integration_connections' AND type IN ('table', 'index') AND sql IS NOT NULL ORDER BY type, name`)
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
		snapshot.WriteString(objectType)
		snapshot.WriteByte(':')
		snapshot.WriteString(name)
		snapshot.WriteByte('=')
		snapshot.WriteString(definition)
		snapshot.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}

func TestAutomationAuditFreshSchemaAndV15MigrationMatch(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if version := readUserVersion(t, ctx, fresh.DB()); version != CurrentDBVersion {
		t.Fatalf("expected fresh database version %d, got %d", CurrentDBVersion, version)
	}
	freshSchema := automationAuditSchemaSnapshot(t, ctx, fresh.DB())
	for _, name := range []string{"automation_audit_events", "idx_automation_audit_created", "idx_automation_audit_category_action", "idx_automation_audit_agent", "idx_automation_audit_run"} {
		if !strings.Contains(freshSchema, name) {
			t.Fatalf("fresh automation audit schema missing %s: %s", name, freshSchema)
		}
	}

	path := filepath.Join(t.TempDir(), "v15.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE automation_audit_events; PRAGMA user_version = 15`); err != nil {
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
		t.Fatalf("expected migrated database version %d, got %d", CurrentDBVersion, version)
	}
	migratedSchema := automationAuditSchemaSnapshot(t, ctx, migrated.DB())
	if migratedSchema != freshSchema {
		t.Fatalf("fresh and migrated automation audit schemas differ\nfresh:\n%s\nmigrated:\n%s", freshSchema, migratedSchema)
	}
}

func TestAutomationAuditWriteValidationAndPagination(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Audit", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	base := AutomationAuditEvent{
		Category: " automation ", Action: " run.started ", Actor: " system ", AgentID: agent.ID, RunID: run.ID,
		SubjectType: "run", SubjectID: run.ID, Outcome: "success", Risk: "low", DetailsJSON: json.RawMessage(`{"source":"scheduler","counts":{"attempt":1}}`),
	}
	createdTimes := []string{"2026-01-01T00:00:01Z", "2026-01-01T00:00:02Z", "2026-01-01T00:00:03Z"}
	created := make([]AutomationAuditEvent, 0, len(createdTimes))
	for index, createdAt := range createdTimes {
		event := base
		event.ID = "audit-" + string(rune('a'+index))
		event.CreatedAt = createdAt
		stored, err := store.AddAutomationAuditEvent(ctx, event)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, stored)
	}
	if created[0].Category != "automation" || created[0].Action != "run.started" || created[0].Actor != "system" {
		t.Fatalf("expected trimmed audit fields, got %+v", created[0])
	}
	firstPage, err := store.ListAutomationAuditEvents(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || firstPage[0].ID != "audit-c" || firstPage[1].ID != "audit-b" {
		t.Fatalf("unexpected first audit page: %+v", firstPage)
	}
	secondPage, err := store.ListAutomationAuditEvents(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 || secondPage[0].ID != "audit-a" {
		t.Fatalf("unexpected second audit page: %+v", secondPage)
	}
	if _, err := store.ListAutomationAuditEvents(ctx, AutomationAuditMaxListLimit+1, 0); err == nil {
		t.Fatal("expected excessive audit list limit to fail")
	}
	if _, err := store.ListAutomationAuditEvents(ctx, 10, -1); err == nil {
		t.Fatal("expected negative audit offset to fail")
	}

	invalidDetails := []json.RawMessage{
		json.RawMessage(`{"broken":`),
		json.RawMessage(`[]`),
		json.RawMessage(`null`),
		json.RawMessage(`{"password":"hidden"}`),
		json.RawMessage(`{"metadata":{"password_hash":"hidden"}}`),
		json.RawMessage(`{"nested":[{"apiKey":"hidden"}]}`),
		json.RawMessage(`{"metadata":{"authToken":"hidden"}}`),
		json.RawMessage(`{"tool":{"rawToolInput":{"command":"rm"}}}`),
		json.RawMessage(`{"note":"` + strings.Repeat("x", AutomationAuditDetailsMaxBytes) + `"}`),
	}
	for _, details := range invalidDetails {
		event := base
		event.ID = ""
		event.CreatedAt = ""
		event.DetailsJSON = details
		if _, err := store.AddAutomationAuditEvent(ctx, event); err == nil {
			t.Fatalf("expected invalid automation audit details to fail: %s", details)
		}
	}
	invalidEnum := base
	invalidEnum.Outcome = "ok"
	if _, err := store.AddAutomationAuditEvent(ctx, invalidEnum); err == nil {
		t.Fatal("expected invalid audit outcome to fail")
	}
	invalidEnum = base
	invalidEnum.Risk = "dangerous"
	if _, err := store.AddAutomationAuditEvent(ctx, invalidEnum); err == nil {
		t.Fatal("expected invalid audit risk to fail")
	}
}

func TestAutomationAuditForeignKeysSetNull(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Audit FK", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	makeEvent := func(id, runID string) AutomationAuditEvent {
		return AutomationAuditEvent{ID: id, Category: "automation", Action: "lifecycle", Actor: "test", AgentID: agent.ID, RunID: runID, Outcome: "success", Risk: "none", DetailsJSON: json.RawMessage(`{}`)}
	}
	if _, err := store.AddAutomationAuditEvent(ctx, makeEvent("audit-run-fk", run.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAutomationAuditEvent(ctx, makeEvent("audit-agent-fk", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	var agentID, runID string
	if err := store.DB().QueryRowContext(ctx, `SELECT COALESCE(agent_id,''), COALESCE(run_id,'') FROM automation_audit_events WHERE id = 'audit-run-fk'`).Scan(&agentID, &runID); err != nil {
		t.Fatal(err)
	}
	if agentID != agent.ID || runID != "" {
		t.Fatalf("deleting run should only clear run_id, got agent=%q run=%q", agentID, runID)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COALESCE(agent_id,'') FROM automation_audit_events WHERE id = 'audit-agent-fk'`).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if agentID != "" {
		t.Fatalf("deleting agent should clear agent_id, got %q", agentID)
	}
}

func automationAuditSchemaSnapshot(t *testing.T, ctx context.Context, database *sql.DB) string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'automation_audit_events' AND type IN ('table', 'index') AND sql IS NOT NULL ORDER BY type, name`)
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
		snapshot.WriteString(objectType)
		snapshot.WriteByte(':')
		snapshot.WriteString(name)
		snapshot.WriteByte('=')
		snapshot.WriteString(definition)
		snapshot.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}
