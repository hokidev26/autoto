package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/secrets"
)

func DefaultNotificationSettings() NotificationSettings {
	now := Now()
	return NotificationSettings{ID: "default", NotifyOnApproval: true, NotifyOnDone: true, NotifyOnError: true, CreatedAt: now, UpdatedAt: now}
}

func (s *Store) GetNotificationSettings(ctx context.Context) (NotificationSettings, error) {
	settings, err := scanNotificationSettings(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, enabled, COALESCE(webhook_url,''), notify_on_approval, notify_on_done, notify_on_error, created_at, updated_at FROM notification_settings WHERE id = 'default'`).Scan(dest...)
	})
	if err == nil {
		return settings, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return NotificationSettings{}, err
	}
	settings = DefaultNotificationSettings()
	_, err = s.UpdateNotificationSettings(ctx, settings)
	return settings, err
}

func (s *Store) UpdateNotificationSettings(ctx context.Context, settings NotificationSettings) (NotificationSettings, error) {
	if settings.ID == "" {
		settings.ID = "default"
	}
	now := Now()
	if settings.CreatedAt == "" {
		settings.CreatedAt = now
	}
	settings.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_settings (id, enabled, webhook_url, notify_on_approval, notify_on_done, notify_on_error, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled, webhook_url = excluded.webhook_url, notify_on_approval = excluded.notify_on_approval, notify_on_done = excluded.notify_on_done, notify_on_error = excluded.notify_on_error, updated_at = excluded.updated_at`, settings.ID, boolInt(settings.Enabled), strings.TrimSpace(settings.WebhookURL), boolInt(settings.NotifyOnApproval), boolInt(settings.NotifyOnDone), boolInt(settings.NotifyOnError), settings.CreatedAt, settings.UpdatedAt)
	if err != nil {
		return NotificationSettings{}, err
	}
	return s.GetNotificationSettings(ctx)
}

func DefaultWorkflowPreferences() WorkflowPreferences {
	now := Now()
	return WorkflowPreferences{ID: "default", RequireConfirmationForExec: true, RequireConfirmationForWrites: false, AllowReadOnlyByDefault: true, DangerReflectionEnabled: true, PolicyGeneration: 1, CreatedAt: now, UpdatedAt: now}
}

func (s *Store) GetWorkflowPreferences(ctx context.Context) (WorkflowPreferences, error) {
	prefs, err := scanWorkflowPreferences(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, require_confirmation_for_exec, require_confirmation_for_writes, allow_read_only_by_default, COALESCE(danger_reflection_enabled,1), COALESCE(policy_generation,1), created_at, updated_at FROM workflow_preferences WHERE id = 'default'`).Scan(dest...)
	})
	if err == nil {
		return prefs, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkflowPreferences{}, err
	}
	prefs = DefaultWorkflowPreferences()
	return s.UpdateWorkflowPreferences(ctx, prefs)
}

func (s *Store) UpdateWorkflowPreferences(ctx context.Context, prefs WorkflowPreferences) (WorkflowPreferences, error) {
	if prefs.ID == "" {
		prefs.ID = "default"
	}
	now := Now()
	if prefs.CreatedAt == "" {
		prefs.CreatedAt = now
	}
	prefs.UpdatedAt = now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowPreferences{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_preferences (id, require_confirmation_for_exec, require_confirmation_for_writes, allow_read_only_by_default, danger_reflection_enabled, policy_generation, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT(id) DO UPDATE SET require_confirmation_for_exec = excluded.require_confirmation_for_exec, require_confirmation_for_writes = excluded.require_confirmation_for_writes, allow_read_only_by_default = excluded.allow_read_only_by_default, danger_reflection_enabled = excluded.danger_reflection_enabled, policy_generation = workflow_preferences.policy_generation + 1, updated_at = excluded.updated_at`, prefs.ID, boolInt(prefs.RequireConfirmationForExec), boolInt(prefs.RequireConfirmationForWrites), boolInt(prefs.AllowReadOnlyByDefault), boolInt(prefs.DangerReflectionEnabled), prefs.CreatedAt, prefs.UpdatedAt)
	if err != nil {
		return WorkflowPreferences{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowPreferences{}, err
	}
	return s.GetWorkflowPreferences(ctx)
}

func ensureWorkflowPreferencesTx(ctx context.Context, tx *sql.Tx) error {
	now := Now()
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_preferences (id, require_confirmation_for_exec, require_confirmation_for_writes, allow_read_only_by_default, policy_generation, created_at, updated_at) VALUES ('default', 1, 0, 1, 1, ?, ?) ON CONFLICT(id) DO NOTHING`, now, now)
	return err
}

func bumpPolicyGenerationTx(ctx context.Context, tx *sql.Tx) error {
	if err := ensureWorkflowPreferencesTx(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE workflow_preferences SET policy_generation = policy_generation + 1, updated_at = ? WHERE id = 'default'`, Now())
	return err
}

const (
	maxStoredToolPermissionDescriptionBytes = 2000
	maxStoredToolPermissionPriority         = 10000
)

func normalizeStoredToolPermissionRule(rule ToolPermissionRule) (ToolPermissionRule, error) {
	rule.Mode = strings.TrimSpace(rule.Mode)
	rule.ToolName = strings.TrimSpace(rule.ToolName)
	rule.Risk = strings.TrimSpace(rule.Risk)
	rule.Decision = strings.TrimSpace(rule.Decision)
	rule.Description = strings.TrimSpace(rule.Description)
	if !validStoredToolPermissionMode(rule.Mode) {
		return ToolPermissionRule{}, errors.New("invalid tool permission mode")
	}
	if !validStoredToolPermissionToolName(rule.ToolName) {
		return ToolPermissionRule{}, errors.New("invalid tool permission tool name")
	}
	if !validStoredToolPermissionRisk(rule.Risk) {
		return ToolPermissionRule{}, errors.New("invalid tool permission risk")
	}
	if rule.Decision != "allow" && rule.Decision != "ask" && rule.Decision != "deny" {
		return ToolPermissionRule{}, errors.New("invalid tool permission decision")
	}
	if rule.Decision == "allow" && (rule.Risk == "danger" || rule.Risk == "*") {
		return ToolPermissionRule{}, errors.New("allow rules cannot target danger or wildcard risk")
	}
	if rule.Priority < -maxStoredToolPermissionPriority || rule.Priority > maxStoredToolPermissionPriority {
		return ToolPermissionRule{}, errors.New("tool permission priority is out of range")
	}
	if len(rule.Description) > maxStoredToolPermissionDescriptionBytes {
		return ToolPermissionRule{}, errors.New("tool permission description is too long")
	}
	return rule, nil
}

func validStoredToolPermissionMode(mode string) bool {
	switch mode {
	case "*", "readOnly", "bypassPermissions", "acceptEdits", "default", "dontAsk":
		return true
	default:
		return false
	}
}

func validStoredToolPermissionToolName(name string) bool {
	if name == "*" {
		return true
	}
	if len(name) == 0 || len(name) > 192 {
		return false
	}
	for i, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') || (i > 0 && strings.ContainsRune("_.:-", r)) {
			continue
		}
		return false
	}
	return true
}

func validStoredToolPermissionRisk(risk string) bool {
	switch risk {
	case "*", "read", "write", "exec", "danger":
		return true
	default:
		return false
	}
}

func (s *Store) ListToolPermissionRules(ctx context.Context) ([]ToolPermissionRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, mode, tool_name, risk, decision, priority, enabled, COALESCE(description,''), created_at, updated_at FROM tool_permission_rules ORDER BY priority DESC, (CASE WHEN mode <> '*' THEN 1 ELSE 0 END + CASE WHEN tool_name <> '*' THEN 1 ELSE 0 END + CASE WHEN risk <> '*' THEN 1 ELSE 0 END) DESC, CASE decision WHEN 'deny' THEN 2 WHEN 'ask' THEN 1 ELSE 0 END DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]ToolPermissionRule, 0)
	for rows.Next() {
		rule, err := scanToolPermissionRule(rows.Scan)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) CreateToolPermissionRule(ctx context.Context, rule ToolPermissionRule) (ToolPermissionRule, error) {
	if rule.ID == "" {
		rule.ID = NewID()
	}
	now := Now()
	if rule.CreatedAt == "" {
		rule.CreatedAt = now
	}
	if rule.UpdatedAt == "" {
		rule.UpdatedAt = rule.CreatedAt
	}
	rule, err := normalizeStoredToolPermissionRule(rule)
	if err != nil {
		return ToolPermissionRule{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolPermissionRule{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO tool_permission_rules (id, mode, tool_name, risk, decision, priority, enabled, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, rule.ID, rule.Mode, rule.ToolName, rule.Risk, rule.Decision, rule.Priority, boolInt(rule.Enabled), rule.Description, rule.CreatedAt, rule.UpdatedAt)
	if err != nil {
		return ToolPermissionRule{}, err
	}
	if err := bumpPolicyGenerationTx(ctx, tx); err != nil {
		return ToolPermissionRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolPermissionRule{}, err
	}
	return s.GetToolPermissionRule(ctx, rule.ID)
}

func (s *Store) GetToolPermissionRule(ctx context.Context, id string) (ToolPermissionRule, error) {
	return scanToolPermissionRule(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, mode, tool_name, risk, decision, priority, enabled, COALESCE(description,''), created_at, updated_at FROM tool_permission_rules WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) UpdateToolPermissionRule(ctx context.Context, rule ToolPermissionRule) (ToolPermissionRule, error) {
	if strings.TrimSpace(rule.ID) == "" {
		return ToolPermissionRule{}, errors.New("tool permission rule id is required")
	}
	existing, err := s.GetToolPermissionRule(ctx, rule.ID)
	if err != nil {
		return ToolPermissionRule{}, err
	}
	rule.CreatedAt = existing.CreatedAt
	rule.UpdatedAt = Now()
	rule, err = normalizeStoredToolPermissionRule(rule)
	if err != nil {
		return ToolPermissionRule{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolPermissionRule{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE tool_permission_rules SET mode = ?, tool_name = ?, risk = ?, decision = ?, priority = ?, enabled = ?, description = NULLIF(?, ''), updated_at = ? WHERE id = ?`, rule.Mode, rule.ToolName, rule.Risk, rule.Decision, rule.Priority, boolInt(rule.Enabled), rule.Description, rule.UpdatedAt, rule.ID)
	if err != nil {
		return ToolPermissionRule{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ToolPermissionRule{}, err
	} else if affected != 1 {
		return ToolPermissionRule{}, sql.ErrNoRows
	}
	if err := bumpPolicyGenerationTx(ctx, tx); err != nil {
		return ToolPermissionRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolPermissionRule{}, err
	}
	return s.GetToolPermissionRule(ctx, rule.ID)
}

func (s *Store) DeleteToolPermissionRule(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM tool_permission_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}
	if err := bumpPolicyGenerationTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListMCPServers(ctx context.Context) ([]MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, transport, command, COALESCE(args_json,''), COALESCE(cwd,''), COALESCE(env_json,''), enabled, created_at, updated_at FROM mcp_servers ORDER BY enabled DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers := make([]MCPServer, 0)
	for rows.Next() {
		server, err := scanMCPServer(rows.Scan)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

func (s *Store) GetMCPServer(ctx context.Context, id string) (MCPServer, error) {
	return scanMCPServer(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, name, transport, command, COALESCE(args_json,''), COALESCE(cwd,''), COALESCE(env_json,''), enabled, created_at, updated_at FROM mcp_servers WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) CreateMCPServer(ctx context.Context, server MCPServer) (MCPServer, error) {
	if server.ID == "" {
		server.ID = NewID()
	}
	if server.Transport == "" {
		server.Transport = "stdio"
	}
	if server.Env == nil {
		server.Env = map[string]string{}
	}
	now := Now()
	server.CreatedAt = now
	server.UpdatedAt = now
	argsJSON, _ := json.Marshal(server.Args)
	envJSON, _ := json.Marshal(server.Env)
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_servers (id, name, transport, command, args_json, cwd, env_json, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`, server.ID, server.Name, server.Transport, server.Command, string(argsJSON), server.CWD, string(envJSON), boolInt(server.Enabled), server.CreatedAt, server.UpdatedAt)
	if err != nil {
		return MCPServer{}, err
	}
	return server, nil
}

func (s *Store) UpdateMCPServer(ctx context.Context, server MCPServer) (MCPServer, error) {
	if server.Env == nil {
		server.Env = map[string]string{}
	}
	now := Now()
	argsJSON, _ := json.Marshal(server.Args)
	envJSON, _ := json.Marshal(server.Env)
	result, err := s.db.ExecContext(ctx, `UPDATE mcp_servers SET name = ?, transport = ?, command = ?, args_json = NULLIF(?, ''), cwd = NULLIF(?, ''), env_json = NULLIF(?, ''), enabled = ?, updated_at = ? WHERE id = ?`, server.Name, server.Transport, server.Command, string(argsJSON), server.CWD, string(envJSON), boolInt(server.Enabled), now, server.ID)
	if err != nil {
		return MCPServer{}, err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return MCPServer{}, sql.ErrNoRows
	}
	return s.GetMCPServer(ctx, server.ID)
}

func (s *Store) DeleteMCPServer(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListIntegrationConnections(ctx context.Context) ([]IntegrationConnection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, name, enabled, endpoint, settings_json, secret_refs_json, created_at, updated_at FROM integration_connections ORDER BY kind ASC, name ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := make([]IntegrationConnection, 0)
	for rows.Next() {
		connection, err := scanIntegrationConnection(rows.Scan)
		if err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func (s *Store) GetIntegrationConnection(ctx context.Context, id string) (IntegrationConnection, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return IntegrationConnection{}, sql.ErrNoRows
	}
	return scanIntegrationConnection(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, kind, name, enabled, endpoint, settings_json, secret_refs_json, created_at, updated_at FROM integration_connections WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) CreateIntegrationConnection(ctx context.Context, connection IntegrationConnection) (IntegrationConnection, error) {
	canonical, settings, refs, err := canonicalIntegrationConnection(connection)
	if err != nil {
		return IntegrationConnection{}, err
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	if err := validateIntegrationText("id", canonical.ID, 128, true, false); err != nil {
		return IntegrationConnection{}, err
	}
	now := Now()
	canonical.CreatedAt, canonical.UpdatedAt = now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO integration_connections (id, kind, name, enabled, endpoint, settings_json, secret_refs_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, canonical.ID, canonical.Kind, canonical.Name, boolInt(canonical.Enabled), canonical.Endpoint, settings, refs, canonical.CreatedAt, canonical.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return IntegrationConnection{}, fmt.Errorf("%w: integration connection kind and name already exist", ErrConflict)
		}
		return IntegrationConnection{}, err
	}
	return canonical, nil
}

func (s *Store) UpdateIntegrationConnection(ctx context.Context, connection IntegrationConnection) (IntegrationConnection, error) {
	canonical, settings, refs, err := canonicalIntegrationConnection(connection)
	if err != nil {
		return IntegrationConnection{}, err
	}
	if err := validateIntegrationText("id", canonical.ID, 128, true, false); err != nil {
		return IntegrationConnection{}, err
	}
	canonical.UpdatedAt = Now()
	result, err := s.db.ExecContext(ctx, `UPDATE integration_connections SET kind = ?, name = ?, enabled = ?, endpoint = ?, settings_json = ?, secret_refs_json = ?, updated_at = ? WHERE id = ?`, canonical.Kind, canonical.Name, boolInt(canonical.Enabled), canonical.Endpoint, settings, refs, canonical.UpdatedAt, canonical.ID)
	if err != nil {
		if isUniqueConstraint(err) {
			return IntegrationConnection{}, fmt.Errorf("%w: integration connection kind and name already exist", ErrConflict)
		}
		return IntegrationConnection{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return IntegrationConnection{}, err
	}
	if affected == 0 {
		return IntegrationConnection{}, sql.ErrNoRows
	}
	return s.GetIntegrationConnection(ctx, canonical.ID)
}

func (s *Store) DeleteIntegrationConnection(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return sql.ErrNoRows
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM integration_connections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func canonicalIntegrationConnection(connection IntegrationConnection) (IntegrationConnection, string, string, error) {
	connection.ID = strings.TrimSpace(connection.ID)
	connection.Kind = strings.TrimSpace(connection.Kind)
	connection.Name = strings.TrimSpace(connection.Name)
	connection.Endpoint = strings.TrimSpace(connection.Endpoint)
	if err := validateIntegrationText("kind", connection.Kind, 64, true, true); err != nil {
		return IntegrationConnection{}, "", "", err
	}
	if err := validateIntegrationText("name", connection.Name, 120, true, false); err != nil {
		return IntegrationConnection{}, "", "", err
	}
	if err := validateIntegrationText("endpoint", connection.Endpoint, 2048, false, false); err != nil {
		return IntegrationConnection{}, "", "", err
	}
	settings, err := normalizeIntegrationSettings(connection.SettingsJSON)
	if err != nil {
		return IntegrationConnection{}, "", "", err
	}
	secretRefs, encodedRefs, err := normalizeIntegrationSecretRefs(connection.SecretRefs)
	if err != nil {
		return IntegrationConnection{}, "", "", err
	}
	connection.SettingsJSON = settings
	connection.SecretRefs = secretRefs
	connection.SecretConfigured = integrationSecretConfigured(secretRefs)
	return connection, string(settings), string(encodedRefs), nil
}

func validateIntegrationText(name, value string, maxBytes int, required, token bool) error {
	if required && value == "" {
		return fmt.Errorf("integration connection %s is required", name)
	}
	if value == "" {
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid integration connection %s", name)
	}
	if token {
		for i, char := range value {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (i > 0 && char >= '0' && char <= '9') || (i > 0 && strings.ContainsRune("_.:-", char)) {
				continue
			}
			return fmt.Errorf("invalid integration connection %s", name)
		}
	}
	return nil
}

func normalizeIntegrationSettings(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = `{}`
	}
	if len(trimmed) > IntegrationSettingsMaxBytes {
		return nil, fmt.Errorf("integration settings exceed %d bytes", IntegrationSettingsMaxBytes)
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, errors.New("integration settings must be a valid JSON object")
	}
	var settings map[string]any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&settings); err != nil || settings == nil {
		return nil, errors.New("integration settings must be a valid JSON object")
	}
	if key, found := integrationSensitiveKey(settings); found {
		return nil, fmt.Errorf("integration settings contain forbidden sensitive key %q", key)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode integration settings: %w", err)
	}
	if len(encoded) > IntegrationSettingsMaxBytes {
		return nil, fmt.Errorf("integration settings exceed %d bytes", IntegrationSettingsMaxBytes)
	}
	return encoded, nil
}

func integrationSensitiveKey(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbiddenIntegrationSettingsKey(key) {
				return key, true
			}
			if nested, found := integrationSensitiveKey(child); found {
				return nested, true
			}
		}
	case []any:
		for _, child := range typed {
			if nested, found := integrationSensitiveKey(child); found {
				return nested, true
			}
		}
	}
	return "", false
}

func forbiddenIntegrationSettingsKey(key string) bool {
	var normalized strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(key)) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			normalized.WriteRune(char)
		}
	}
	value := normalized.String()
	for _, marker := range []string{"password", "passwd", "secret", "token", "apikey", "credential", "privatekey", "accesskey", "authorization", "cookie"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func normalizeIntegrationSecretRefs(input map[string]string) (map[string]string, json.RawMessage, error) {
	if input == nil {
		input = map[string]string{}
	}
	refs := make(map[string]string, len(input))
	for rawName, value := range input {
		name := strings.TrimSpace(rawName)
		if !validIntegrationSecretName(name) {
			return nil, nil, errors.New("invalid integration secret logical name")
		}
		if _, exists := refs[name]; exists {
			return nil, nil, errors.New("duplicate integration secret logical name")
		}
		ref, err := secrets.ParseRef(value)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid integration secret reference for %q: %w", name, err)
		}
		refs[name] = ref.String()
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return nil, nil, fmt.Errorf("encode integration secret references: %w", err)
	}
	if len(encoded) > IntegrationSecretRefsMaxBytes {
		return nil, nil, fmt.Errorf("integration secret references exceed %d bytes", IntegrationSecretRefsMaxBytes)
	}
	return refs, encoded, nil
}

func validIntegrationSecretName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (i > 0 && char >= '0' && char <= '9') || (i > 0 && strings.ContainsRune("_.-", char)) {
			continue
		}
		return false
	}
	return true
}

func integrationSecretConfigured(refs map[string]string) map[string]bool {
	configured := make(map[string]bool, len(refs))
	for name := range refs {
		configured[name] = true
	}
	return configured
}

type integrationConnectionScanner func(dest ...any) error

func scanIntegrationConnection(scan integrationConnectionScanner) (IntegrationConnection, error) {
	var connection IntegrationConnection
	var enabled int
	var settingsJSON, refsJSON string
	if err := scan(&connection.ID, &connection.Kind, &connection.Name, &enabled, &connection.Endpoint, &settingsJSON, &refsJSON, &connection.CreatedAt, &connection.UpdatedAt); err != nil {
		return IntegrationConnection{}, err
	}
	settings, err := normalizeIntegrationSettings(json.RawMessage(settingsJSON))
	if err != nil {
		return IntegrationConnection{}, fmt.Errorf("stored integration settings for %s are invalid: %w", connection.ID, err)
	}
	var refs map[string]string
	if err := json.Unmarshal([]byte(refsJSON), &refs); err != nil || refs == nil {
		return IntegrationConnection{}, fmt.Errorf("stored integration secret references for %s are invalid", connection.ID)
	}
	refs, _, err = normalizeIntegrationSecretRefs(refs)
	if err != nil {
		return IntegrationConnection{}, fmt.Errorf("stored integration secret references for %s are invalid: %w", connection.ID, err)
	}
	connection.Enabled = enabled != 0
	connection.SettingsJSON = settings
	connection.SecretRefs = refs
	connection.SecretConfigured = integrationSecretConfigured(refs)
	return connection, nil
}

func (s *Store) AddAutomationAuditEvent(ctx context.Context, event AutomationAuditEvent) (AutomationAuditEvent, error) {
	canonical, err := canonicalAutomationAuditEvent(event)
	if err != nil {
		return AutomationAuditEvent{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_audit_events (id, category, action, actor, agent_id, run_id, subject_type, subject_id, outcome, risk, details_json, created_at) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)`, canonical.ID, canonical.Category, canonical.Action, canonical.Actor, canonical.AgentID, canonical.RunID, canonical.SubjectType, canonical.SubjectID, canonical.Outcome, canonical.Risk, string(canonical.DetailsJSON), canonical.CreatedAt)
	if err != nil {
		return AutomationAuditEvent{}, fmt.Errorf("insert automation audit event: %w", err)
	}
	return canonical, nil
}

// RecordAutomationAuditEvent is an explicit audit-oriented alias for callers
// that do not otherwise use Store's Add naming convention.
func (s *Store) RecordAutomationAuditEvent(ctx context.Context, event AutomationAuditEvent) (AutomationAuditEvent, error) {
	return s.AddAutomationAuditEvent(ctx, event)
}

func (s *Store) CreateAutomationAuditEvent(ctx context.Context, event AutomationAuditEvent) (AutomationAuditEvent, error) {
	return s.AddAutomationAuditEvent(ctx, event)
}

// ListAutomationAuditEvents returns newest events first. A zero limit uses 50;
// callers may request at most AutomationAuditMaxListLimit rows and paginate with
// a non-negative offset.
func (s *Store) ListAutomationAuditEvents(ctx context.Context, limit, offset int) ([]AutomationAuditEvent, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 0 || limit > AutomationAuditMaxListLimit {
		return nil, fmt.Errorf("automation audit limit must be between 1 and %d", AutomationAuditMaxListLimit)
	}
	if offset < 0 {
		return nil, errors.New("automation audit offset must not be negative")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, category, action, actor, COALESCE(agent_id,''), COALESCE(run_id,''), COALESCE(subject_type,''), COALESCE(subject_id,''), outcome, risk, details_json, created_at FROM automation_audit_events ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AutomationAuditEvent, 0)
	for rows.Next() {
		var event AutomationAuditEvent
		var details string
		if err := rows.Scan(&event.ID, &event.Category, &event.Action, &event.Actor, &event.AgentID, &event.RunID, &event.SubjectType, &event.SubjectID, &event.Outcome, &event.Risk, &details, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.DetailsJSON, err = normalizeAutomationAuditDetails(json.RawMessage(details))
		if err != nil {
			return nil, fmt.Errorf("stored automation audit details for %s are invalid: %w", event.ID, err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func canonicalAutomationAuditEvent(event AutomationAuditEvent) (AutomationAuditEvent, error) {
	event.ID = strings.TrimSpace(event.ID)
	event.Category = strings.TrimSpace(event.Category)
	event.Action = strings.TrimSpace(event.Action)
	event.Actor = strings.TrimSpace(event.Actor)
	event.AgentID = strings.TrimSpace(event.AgentID)
	event.RunID = strings.TrimSpace(event.RunID)
	event.SubjectType = strings.TrimSpace(event.SubjectType)
	event.SubjectID = strings.TrimSpace(event.SubjectID)
	event.Outcome = strings.TrimSpace(event.Outcome)
	event.Risk = strings.TrimSpace(event.Risk)
	event.CreatedAt = strings.TrimSpace(event.CreatedAt)

	if event.ID == "" {
		event.ID = NewID()
	}
	if event.CreatedAt == "" {
		event.CreatedAt = Now()
	}
	for _, field := range []struct {
		name     string
		value    string
		maxBytes int
		required bool
		token    bool
	}{
		{"id", event.ID, 128, true, false},
		{"category", event.Category, 64, true, true},
		{"action", event.Action, 96, true, true},
		{"actor", event.Actor, 200, true, false},
		{"agent_id", event.AgentID, 128, false, false},
		{"run_id", event.RunID, 128, false, false},
		{"subject_type", event.SubjectType, 64, false, true},
		{"subject_id", event.SubjectID, 256, false, false},
	} {
		if err := validateAutomationAuditText(field.name, field.value, field.maxBytes, field.required, field.token); err != nil {
			return AutomationAuditEvent{}, err
		}
	}
	if (event.SubjectType == "") != (event.SubjectID == "") {
		return AutomationAuditEvent{}, errors.New("automation audit subject_type and subject_id must be provided together")
	}
	if !validAutomationAuditOutcome(event.Outcome) {
		return AutomationAuditEvent{}, errors.New("invalid automation audit outcome")
	}
	if !validAutomationAuditRisk(event.Risk) {
		return AutomationAuditEvent{}, errors.New("invalid automation audit risk")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.CreatedAt); err != nil {
		return AutomationAuditEvent{}, errors.New("invalid automation audit created_at")
	}
	var err error
	event.DetailsJSON, err = normalizeAutomationAuditDetails(event.DetailsJSON)
	if err != nil {
		return AutomationAuditEvent{}, err
	}
	return event, nil
}

func validateAutomationAuditText(name, value string, maxBytes int, required, token bool) error {
	if required && value == "" {
		return fmt.Errorf("automation audit %s is required", name)
	}
	if value == "" {
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid automation audit %s", name)
	}
	if token && !validAutomationAuditToken(value) {
		return fmt.Errorf("invalid automation audit %s", name)
	}
	return nil
}

func validAutomationAuditToken(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		switch char {
		case '.', '_', ':', '-', '/':
			continue
		default:
			return false
		}
	}
	return value != ""
}

func validAutomationAuditOutcome(value string) bool {
	switch value {
	case "success", "failure", "denied", "error", "unknown":
		return true
	default:
		return false
	}
}

func validAutomationAuditRisk(value string) bool {
	switch value {
	case "none", "low", "medium", "high", "critical":
		return true
	default:
		return false
	}
}

func normalizeAutomationAuditDetails(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 {
		trimmed = `{}`
	}
	if len(trimmed) > AutomationAuditDetailsMaxBytes {
		return nil, fmt.Errorf("automation audit details exceed %d bytes", AutomationAuditDetailsMaxBytes)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(trimmed), &details); err != nil {
		return nil, errors.New("automation audit details must be a valid JSON object")
	}
	if details == nil {
		return nil, errors.New("automation audit details must be a valid JSON object")
	}
	if key, found := automationAuditSensitiveKey(details); found {
		return nil, fmt.Errorf("automation audit details contain forbidden sensitive key %q", key)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return nil, fmt.Errorf("encode automation audit details: %w", err)
	}
	if len(encoded) > AutomationAuditDetailsMaxBytes {
		return nil, fmt.Errorf("automation audit details exceed %d bytes", AutomationAuditDetailsMaxBytes)
	}
	return json.RawMessage(encoded), nil
}

func automationAuditSensitiveKey(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbiddenAutomationAuditKey(key) {
				return key, true
			}
			if nested, found := automationAuditSensitiveKey(child); found {
				return nested, true
			}
		}
	case []any:
		for _, child := range typed {
			if nested, found := automationAuditSensitiveKey(child); found {
				return nested, true
			}
		}
	}
	return "", false
}

func forbiddenAutomationAuditKey(key string) bool {
	var normalized strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(key)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			normalized.WriteRune(char)
		}
	}
	value := normalized.String()
	if value == "input" || strings.Contains(value, "rawinput") || strings.Contains(value, "inputjson") || strings.Contains(value, "toolinput") || value == "toolargs" || value == "toolarguments" {
		return true
	}
	for _, marker := range []string{"password", "passwd", "secret", "apikey", "privatekey", "accesskey", "credential", "authorization", "cookie"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	if value == "token" || strings.HasSuffix(value, "token") {
		return true
	}
	return false
}

func (s *Store) SeedBackends(ctx context.Context, backends []Backend) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_backends`).Scan(&count); err != nil {
		return err
	}
	if count > 0 || len(backends) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hasActive := false
	for _, backend := range backends {
		if backend.Name == "" || backend.BaseURL == "" {
			continue
		}
		if backend.ID == "" {
			backend.ID = NewID()
		}
		if backend.Kind == "" {
			backend.Kind = "local"
		}
		now := Now()
		backend.CreatedAt = now
		backend.UpdatedAt = now
		active := backend.Active || !hasActive
		if active {
			hasActive = true
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_backends (id, name, kind, base_url, api_key, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, backend.ID, backend.Name, backend.Kind, backend.BaseURL, nullEmpty(backend.APIKey), boolInt(active), backend.CreatedAt, backend.UpdatedAt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) ListBackends(ctx context.Context) ([]Backend, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, base_url, COALESCE(api_key,''), active, created_at, updated_at FROM agent_backends ORDER BY active DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []Backend
	for rows.Next() {
		backend, err := scanBackend(rows.Scan)
		if err != nil {
			return nil, err
		}
		backends = append(backends, backend)
	}
	return backends, rows.Err()
}

func (s *Store) GetBackend(ctx context.Context, id string) (Backend, error) {
	return scanBackend(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, name, kind, base_url, COALESCE(api_key,''), active, created_at, updated_at FROM agent_backends WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) CreateBackend(ctx context.Context, backend Backend) (Backend, error) {
	if backend.ID == "" {
		backend.ID = NewID()
	}
	if backend.Kind == "" {
		backend.Kind = "local"
	}
	now := Now()
	backend.CreatedAt = now
	backend.UpdatedAt = now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Backend{}, err
	}
	defer tx.Rollback()

	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_backends WHERE active = 1`).Scan(&activeCount); err != nil {
		return Backend{}, err
	}
	backend.Active = backend.Active || activeCount == 0
	if backend.Active {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_backends SET active = 0, updated_at = ? WHERE active = 1`, now); err != nil {
			return Backend{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_backends (id, name, kind, base_url, api_key, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, backend.ID, backend.Name, backend.Kind, backend.BaseURL, nullEmpty(backend.APIKey), boolInt(backend.Active), backend.CreatedAt, backend.UpdatedAt); err != nil {
		return Backend{}, err
	}
	if err := tx.Commit(); err != nil {
		return Backend{}, err
	}
	return backend, nil
}

func (s *Store) UpdateBackend(ctx context.Context, backend Backend) (Backend, error) {
	now := Now()
	if backend.Active {
		if _, err := s.db.ExecContext(ctx, `UPDATE agent_backends SET active = 0, updated_at = ? WHERE id != ? AND active = 1`, now, backend.ID); err != nil {
			return Backend{}, err
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_backends SET name = ?, kind = ?, base_url = ?, api_key = NULLIF(?, ''), active = ?, updated_at = ? WHERE id = ?`, backend.Name, backend.Kind, backend.BaseURL, backend.APIKey, boolInt(backend.Active), now, backend.ID)
	if err != nil {
		return Backend{}, err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return Backend{}, sql.ErrNoRows
	}
	return s.GetBackend(ctx, backend.ID)
}

func (s *Store) ActivateBackend(ctx context.Context, id string) (Backend, error) {
	now := Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Backend{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_backends SET active = 0, updated_at = ? WHERE active = 1`, now); err != nil {
		return Backend{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_backends SET active = 1, updated_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return Backend{}, err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return Backend{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return Backend{}, err
	}
	return s.GetBackend(ctx, id)
}

func (s *Store) DeleteBackend(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var wasActive int
	if err := tx.QueryRowContext(ctx, `SELECT active FROM agent_backends WHERE id = ?`, id).Scan(&wasActive); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM agent_backends WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	if wasActive != 0 {
		_, err = tx.ExecContext(ctx, `UPDATE agent_backends SET active = 1, updated_at = ? WHERE id = (SELECT id FROM agent_backends ORDER BY created_at ASC LIMIT 1)`, Now())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

type backendScanner func(dest ...any) error

type mcpServerScanner func(dest ...any) error

type notificationSettingsScanner func(dest ...any) error

type workflowPreferencesScanner func(dest ...any) error

type toolPermissionRuleScanner func(dest ...any) error

func scanNotificationSettings(scan notificationSettingsScanner) (NotificationSettings, error) {
	var settings NotificationSettings
	var enabled, notifyOnApproval, notifyOnDone, notifyOnError int
	if err := scan(&settings.ID, &enabled, &settings.WebhookURL, &notifyOnApproval, &notifyOnDone, &notifyOnError, &settings.CreatedAt, &settings.UpdatedAt); err != nil {
		return NotificationSettings{}, err
	}
	settings.Enabled = enabled != 0
	settings.NotifyOnApproval = notifyOnApproval != 0
	settings.NotifyOnDone = notifyOnDone != 0
	settings.NotifyOnError = notifyOnError != 0
	return settings, nil
}

func scanWorkflowPreferences(scan workflowPreferencesScanner) (WorkflowPreferences, error) {
	var prefs WorkflowPreferences
	var requireExec, requireWrites, allowReadOnly, dangerReflection int
	if err := scan(&prefs.ID, &requireExec, &requireWrites, &allowReadOnly, &dangerReflection, &prefs.PolicyGeneration, &prefs.CreatedAt, &prefs.UpdatedAt); err != nil {
		return WorkflowPreferences{}, err
	}
	prefs.RequireConfirmationForExec = requireExec != 0
	prefs.RequireConfirmationForWrites = requireWrites != 0
	prefs.AllowReadOnlyByDefault = allowReadOnly != 0
	prefs.DangerReflectionEnabled = dangerReflection != 0
	return prefs, nil
}

func scanToolPermissionRule(scan toolPermissionRuleScanner) (ToolPermissionRule, error) {
	var rule ToolPermissionRule
	var enabled int
	if err := scan(&rule.ID, &rule.Mode, &rule.ToolName, &rule.Risk, &rule.Decision, &rule.Priority, &enabled, &rule.Description, &rule.CreatedAt, &rule.UpdatedAt); err != nil {
		return ToolPermissionRule{}, err
	}
	rule.Enabled = enabled != 0
	return rule, nil
}

func scanMCPServer(scan mcpServerScanner) (MCPServer, error) {
	var server MCPServer
	var argsJSON, envJSON string
	var enabled int
	if err := scan(&server.ID, &server.Name, &server.Transport, &server.Command, &argsJSON, &server.CWD, &envJSON, &enabled, &server.CreatedAt, &server.UpdatedAt); err != nil {
		return MCPServer{}, err
	}
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &server.Args); err != nil {
			return MCPServer{}, err
		}
	}
	if strings.TrimSpace(envJSON) != "" {
		if err := json.Unmarshal([]byte(envJSON), &server.Env); err != nil {
			return MCPServer{}, err
		}
	}
	if server.Env == nil {
		server.Env = map[string]string{}
	}
	server.Enabled = enabled != 0
	return server, nil
}

func scanBackend(scan backendScanner) (Backend, error) {
	var backend Backend
	var active int
	if err := scan(&backend.ID, &backend.Name, &backend.Kind, &backend.BaseURL, &backend.APIKey, &active, &backend.CreatedAt, &backend.UpdatedAt); err != nil {
		return Backend{}, err
	}
	backend.Active = active != 0
	return backend, nil
}
