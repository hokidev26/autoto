package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/secrets"
)

type Plugin struct {
	ID              string            `json:"id"`
	Slug            string            `json:"slug"`
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Description     string            `json:"description,omitempty"`
	ManifestVersion string            `json:"manifestVersion"`
	RootPath        string            `json:"rootPath"`
	Command         string            `json:"command"`
	Args            []string          `json:"args"`
	Env             map[string]string `json:"env"`
	SecretRefs      map[string]string `json:"-"`
	Enabled         bool              `json:"enabled"`
	Status          string            `json:"status"`
	Revision        int64             `json:"revision"`
	ManifestHash    string            `json:"manifestHash"`
	LastCheckedAt   string            `json:"lastCheckedAt,omitempty"`
	LastError       string            `json:"lastError,omitempty"`
	CreatedAt       string            `json:"createdAt"`
	UpdatedAt       string            `json:"updatedAt"`
}

type PluginTool struct {
	PluginID        string          `json:"pluginId"`
	RemoteName      string          `json:"remoteName"`
	ExposedName     string          `json:"exposedName"`
	Description     string          `json:"description,omitempty"`
	InputSchemaJSON json.RawMessage `json:"inputSchema"`
	DiscoveredAt    string          `json:"discoveredAt"`
}

type PluginWithTools struct {
	Plugin Plugin       `json:"plugin"`
	Tools  []PluginTool `json:"tools"`
}

const pluginColumns = `id, slug, name, version, description, manifest_version, root_path, command, args_json, env_json, secret_refs_json, enabled, status, revision, manifest_hash, COALESCE(last_checked_at,''), COALESCE(last_error,''), created_at, updated_at`

func (s *pluginStore) CreatePlugin(ctx context.Context, plugin Plugin) (Plugin, error) {
	canonical, argsJSON, envJSON, refsJSON, err := canonicalPlugin(plugin)
	if err != nil {
		return Plugin{}, err
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	if err := validatePluginText("id", canonical.ID, 128, true); err != nil {
		return Plugin{}, err
	}
	now := Now()
	canonical.Revision = 1
	canonical.CreatedAt, canonical.UpdatedAt = now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO plugins (id, slug, name, version, description, manifest_version, root_path, command, args_json, env_json, secret_refs_json, enabled, status, revision, manifest_hash, last_checked_at, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		canonical.ID, canonical.Slug, canonical.Name, canonical.Version, canonical.Description, canonical.ManifestVersion, canonical.RootPath, canonical.Command,
		argsJSON, envJSON, refsJSON, boolInt(canonical.Enabled), canonical.Status, canonical.ManifestHash, canonical.LastCheckedAt, canonical.LastError, now, now)
	if err != nil {
		return Plugin{}, pluginConstraintError(err)
	}
	return canonical, nil
}

func (s *pluginStore) ListPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+pluginColumns+` FROM plugins ORDER BY slug COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plugins := make([]Plugin, 0)
	for rows.Next() {
		plugin, err := scanPlugin(rows.Scan)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, plugin)
	}
	return plugins, rows.Err()
}

func (s *pluginStore) GetPlugin(ctx context.Context, id string) (Plugin, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Plugin{}, sql.ErrNoRows
	}
	return scanPlugin(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *pluginStore) GetPluginBySlug(ctx context.Context, slug string) (Plugin, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return Plugin{}, sql.ErrNoRows
	}
	return scanPlugin(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins WHERE slug = ? COLLATE NOCASE`, slug).Scan(dest...)
	})
}

func (s *pluginStore) UpdatePlugin(ctx context.Context, plugin Plugin) (Plugin, error) {
	current, err := s.GetPlugin(ctx, plugin.ID)
	if err != nil {
		return Plugin{}, err
	}
	canonical, argsJSON, envJSON, refsJSON, err := canonicalPlugin(plugin)
	if err != nil {
		return Plugin{}, err
	}
	canonical.ID = current.ID
	canonical.CreatedAt = current.CreatedAt
	canonical.Revision = current.Revision + 1
	canonical.UpdatedAt = Now()
	result, err := s.db.ExecContext(ctx, `UPDATE plugins SET slug = ?, name = ?, version = ?, description = ?, manifest_version = ?, root_path = ?, command = ?, args_json = ?, env_json = ?, secret_refs_json = ?, enabled = ?, status = ?, revision = ?, manifest_hash = ?, last_checked_at = NULLIF(?, ''), last_error = NULLIF(?, ''), updated_at = ? WHERE id = ?`,
		canonical.Slug, canonical.Name, canonical.Version, canonical.Description, canonical.ManifestVersion, canonical.RootPath, canonical.Command,
		argsJSON, envJSON, refsJSON, boolInt(canonical.Enabled), canonical.Status, canonical.Revision, canonical.ManifestHash,
		canonical.LastCheckedAt, canonical.LastError, canonical.UpdatedAt, canonical.ID)
	if err != nil {
		return Plugin{}, pluginConstraintError(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Plugin{}, err
	} else if affected == 0 {
		return Plugin{}, sql.ErrNoRows
	}
	return canonical, nil
}

// ReplacePluginManifest atomically adopts freshly loaded manifest fields and
// deletes the plugin's tool snapshot. The row update (with a revision bump
// applied in SQL and RowsAffected checked) and the snapshot delete commit or
// roll back together, so a slug conflict never leaves a partial update.
func (s *pluginStore) ReplacePluginManifest(ctx context.Context, plugin Plugin) (Plugin, error) {
	canonical, argsJSON, envJSON, refsJSON, err := canonicalPlugin(plugin)
	if err != nil {
		return Plugin{}, err
	}
	if canonical.ID == "" {
		return Plugin{}, sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Plugin{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE plugins SET slug = ?, name = ?, version = ?, description = ?, manifest_version = ?, root_path = ?, command = ?, args_json = ?, env_json = ?, secret_refs_json = ?, enabled = ?, status = ?, revision = revision + 1, manifest_hash = ?, last_checked_at = NULLIF(?, ''), last_error = NULLIF(?, ''), updated_at = ? WHERE id = ?`,
		canonical.Slug, canonical.Name, canonical.Version, canonical.Description, canonical.ManifestVersion, canonical.RootPath, canonical.Command,
		argsJSON, envJSON, refsJSON, boolInt(canonical.Enabled), canonical.Status, canonical.ManifestHash,
		canonical.LastCheckedAt, canonical.LastError, Now(), canonical.ID)
	if err != nil {
		return Plugin{}, pluginConstraintError(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Plugin{}, err
	} else if affected != 1 {
		return Plugin{}, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_tools WHERE plugin_id = ?`, canonical.ID); err != nil {
		return Plugin{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plugin{}, err
	}
	return s.GetPlugin(ctx, canonical.ID)
}

func (s *pluginStore) UpdatePluginStatus(ctx context.Context, id, status string, enabled bool, lastCheckedAt, lastError string) (Plugin, error) {
	id = strings.TrimSpace(id)
	status = strings.TrimSpace(status)
	lastCheckedAt = strings.TrimSpace(lastCheckedAt)
	lastError = strings.TrimSpace(lastError)
	if id == "" {
		return Plugin{}, sql.ErrNoRows
	}
	if err := validatePluginStatus(status, lastCheckedAt, lastError); err != nil {
		return Plugin{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE plugins SET enabled = ?, status = ?, last_checked_at = NULLIF(?, ''), last_error = NULLIF(?, ''), revision = revision + 1, updated_at = ? WHERE id = ?`, boolInt(enabled), status, lastCheckedAt, lastError, Now(), id)
	if err != nil {
		return Plugin{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Plugin{}, err
	} else if affected == 0 {
		return Plugin{}, sql.ErrNoRows
	}
	return s.GetPlugin(ctx, id)
}

func (s *pluginStore) DeletePlugin(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return sql.ErrNoRows
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *pluginStore) ReplacePluginTools(ctx context.Context, pluginID string, tools []PluginTool) ([]PluginTool, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil, sql.ErrNoRows
	}
	canonical := make([]PluginTool, len(tools))
	for index, tool := range tools {
		tool.PluginID = pluginID
		var err error
		canonical[index], err = canonicalPluginTool(tool)
		if err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM plugins WHERE id = ?`, pluginID).Scan(&exists); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_tools WHERE plugin_id = ?`, pluginID); err != nil {
		return nil, err
	}
	for _, tool := range canonical {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_tools (plugin_id, remote_name, exposed_name, description, input_schema_json, discovered_at) VALUES (?, ?, ?, ?, ?, ?)`, tool.PluginID, tool.RemoteName, tool.ExposedName, tool.Description, string(tool.InputSchemaJSON), tool.DiscoveredAt); err != nil {
			return nil, pluginConstraintError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return canonical, nil
}

// EnablePluginWithTools atomically installs a discovered tool snapshot and marks
// the plugin healthy/enabled. A tool conflict or state update failure rolls back
// both the deleted old snapshot and every inserted replacement row.
func (s *pluginStore) EnablePluginWithTools(ctx context.Context, pluginID string, tools []PluginTool, checkedAt string) (PluginWithTools, error) {
	pluginID = strings.TrimSpace(pluginID)
	checkedAt = strings.TrimSpace(checkedAt)
	if pluginID == "" {
		return PluginWithTools{}, sql.ErrNoRows
	}
	if checkedAt == "" {
		checkedAt = Now()
	}
	if _, err := time.Parse(time.RFC3339Nano, checkedAt); err != nil {
		return PluginWithTools{}, errors.New("invalid plugin last_checked_at")
	}
	canonical := make([]PluginTool, len(tools))
	for index, tool := range tools {
		tool.PluginID = pluginID
		var err error
		canonical[index], err = canonicalPluginTool(tool)
		if err != nil {
			return PluginWithTools{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PluginWithTools{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM plugins WHERE id = ?`, pluginID).Scan(&exists); err != nil {
		return PluginWithTools{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_tools WHERE plugin_id = ?`, pluginID); err != nil {
		return PluginWithTools{}, err
	}
	for _, tool := range canonical {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_tools (plugin_id, remote_name, exposed_name, description, input_schema_json, discovered_at) VALUES (?, ?, ?, ?, ?, ?)`, tool.PluginID, tool.RemoteName, tool.ExposedName, tool.Description, string(tool.InputSchemaJSON), tool.DiscoveredAt); err != nil {
			return PluginWithTools{}, pluginConstraintError(err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugins SET enabled = 1, status = 'healthy', last_checked_at = ?, last_error = NULL, revision = revision + 1, updated_at = ? WHERE id = ?`, checkedAt, Now(), pluginID)
	if err != nil {
		return PluginWithTools{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return PluginWithTools{}, err
	} else if affected != 1 {
		return PluginWithTools{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return PluginWithTools{}, err
	}
	plugin, err := s.GetPlugin(ctx, pluginID)
	if err != nil {
		return PluginWithTools{}, err
	}
	storedTools, err := s.ListPluginTools(ctx, pluginID)
	if err != nil {
		return PluginWithTools{}, err
	}
	return PluginWithTools{Plugin: plugin, Tools: storedTools}, nil
}

func (s *pluginStore) ListPluginTools(ctx context.Context, pluginID string) ([]PluginTool, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil, sql.ErrNoRows
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM plugins WHERE id = ?`, pluginID).Scan(&exists); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT plugin_id, remote_name, exposed_name, description, input_schema_json, discovered_at FROM plugin_tools WHERE plugin_id = ? ORDER BY exposed_name COLLATE NOCASE, remote_name`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tools := make([]PluginTool, 0)
	for rows.Next() {
		tool, err := scanPluginTool(rows.Scan)
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, rows.Err()
}

func (s *pluginStore) ListEnabledPluginsWithTools(ctx context.Context) ([]PluginWithTools, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+pluginColumns+` FROM plugins WHERE enabled = 1 ORDER BY slug COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	plugins := make([]PluginWithTools, 0)
	for rows.Next() {
		plugin, err := scanPlugin(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		plugins = append(plugins, PluginWithTools{Plugin: plugin, Tools: []PluginTool{}})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range plugins {
		toolRows, err := tx.QueryContext(ctx, `SELECT plugin_id, remote_name, exposed_name, description, input_schema_json, discovered_at FROM plugin_tools WHERE plugin_id = ? ORDER BY exposed_name COLLATE NOCASE, remote_name`, plugins[index].Plugin.ID)
		if err != nil {
			return nil, err
		}
		for toolRows.Next() {
			tool, err := scanPluginTool(toolRows.Scan)
			if err != nil {
				toolRows.Close()
				return nil, err
			}
			plugins[index].Tools = append(plugins[index].Tools, tool)
		}
		if err := toolRows.Close(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return plugins, nil
}

func canonicalPlugin(plugin Plugin) (Plugin, string, string, string, error) {
	plugin.ID = strings.TrimSpace(plugin.ID)
	plugin.Slug = strings.ToLower(strings.TrimSpace(plugin.Slug))
	plugin.Name = strings.TrimSpace(plugin.Name)
	plugin.Version = strings.TrimSpace(plugin.Version)
	plugin.Description = strings.TrimSpace(plugin.Description)
	plugin.ManifestVersion = strings.TrimSpace(plugin.ManifestVersion)
	plugin.RootPath = strings.TrimSpace(plugin.RootPath)
	plugin.Command = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(plugin.Command))))
	plugin.Status = strings.TrimSpace(plugin.Status)
	plugin.ManifestHash = strings.TrimSpace(plugin.ManifestHash)
	plugin.LastCheckedAt = strings.TrimSpace(plugin.LastCheckedAt)
	plugin.LastError = strings.TrimSpace(plugin.LastError)
	if plugin.Status == "" {
		plugin.Status = "unknown"
	}
	for _, field := range []struct {
		name, value string
		max         int
		required    bool
	}{
		{"slug", plugin.Slug, 63, true}, {"name", plugin.Name, 120, true}, {"version", plugin.Version, 64, true}, {"description", plugin.Description, 1000, false},
		{"manifest_version", plugin.ManifestVersion, 64, true}, {"root_path", plugin.RootPath, 4096, true}, {"command", plugin.Command, 1024, true},
	} {
		if err := validatePluginText(field.name, field.value, field.max, field.required); err != nil {
			return Plugin{}, "", "", "", err
		}
	}
	if !validPluginSlug(plugin.Slug) {
		return Plugin{}, "", "", "", errors.New("invalid plugin slug")
	}
	if !filepath.IsAbs(plugin.RootPath) {
		return Plugin{}, "", "", "", errors.New("plugin root_path must be absolute")
	}
	if plugin.Command == "." || filepath.IsAbs(plugin.Command) || plugin.Command == ".." || strings.HasPrefix(plugin.Command, "../") {
		return Plugin{}, "", "", "", errors.New("plugin command must be relative to root_path")
	}
	if len(plugin.ManifestHash) != 64 || !isLowerHex(plugin.ManifestHash) {
		return Plugin{}, "", "", "", errors.New("invalid plugin manifest_hash")
	}
	if err := validatePluginStatus(plugin.Status, plugin.LastCheckedAt, plugin.LastError); err != nil {
		return Plugin{}, "", "", "", err
	}
	if plugin.Args == nil {
		plugin.Args = []string{}
	}
	if len(plugin.Args) > 64 {
		return Plugin{}, "", "", "", errors.New("plugin args exceed maximum count")
	}
	for _, arg := range plugin.Args {
		if len(arg) > 4096 || !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return Plugin{}, "", "", "", errors.New("invalid plugin arg")
		}
	}
	argsJSON, err := json.Marshal(plugin.Args)
	if err != nil {
		return Plugin{}, "", "", "", err
	}
	if plugin.Env == nil {
		plugin.Env = map[string]string{}
	}
	if plugin.SecretRefs == nil {
		plugin.SecretRefs = map[string]string{}
	}
	env := make(map[string]string, len(plugin.Env))
	for key, value := range plugin.Env {
		if !validPluginEnvName(key) || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || sensitivePluginKey(key) {
			return Plugin{}, "", "", "", fmt.Errorf("invalid or sensitive plugin env key %q", key)
		}
		env[key] = value
	}
	refs := make(map[string]string, len(plugin.SecretRefs))
	for key, value := range plugin.SecretRefs {
		if !validPluginEnvName(key) {
			return Plugin{}, "", "", "", fmt.Errorf("invalid plugin secret key %q", key)
		}
		if _, duplicate := env[key]; duplicate {
			return Plugin{}, "", "", "", fmt.Errorf("plugin key %q appears in env and secret refs", key)
		}
		ref, err := secrets.ParseRef(value)
		if err != nil {
			return Plugin{}, "", "", "", fmt.Errorf("invalid plugin secret reference for %q: %w", key, err)
		}
		refs[key] = ref.String()
	}
	plugin.Env, plugin.SecretRefs = env, refs
	envJSON, err := json.Marshal(env)
	if err != nil {
		return Plugin{}, "", "", "", err
	}
	refsJSON, err := json.Marshal(refs)
	if err != nil {
		return Plugin{}, "", "", "", err
	}
	return plugin, string(argsJSON), string(envJSON), string(refsJSON), nil
}

func canonicalPluginTool(tool PluginTool) (PluginTool, error) {
	tool.PluginID = strings.TrimSpace(tool.PluginID)
	tool.RemoteName = strings.TrimSpace(tool.RemoteName)
	tool.ExposedName = strings.TrimSpace(tool.ExposedName)
	tool.Description = strings.TrimSpace(tool.Description)
	tool.DiscoveredAt = strings.TrimSpace(tool.DiscoveredAt)
	if tool.DiscoveredAt == "" {
		tool.DiscoveredAt = Now()
	}
	for _, field := range []struct {
		name, value string
		max         int
		required    bool
	}{{"plugin_id", tool.PluginID, 128, true}, {"remote_name", tool.RemoteName, 128, true}, {"exposed_name", tool.ExposedName, 192, true}, {"description", tool.Description, 2 << 10, false}} {
		if err := validatePluginText(field.name, field.value, field.max, field.required); err != nil {
			return PluginTool{}, err
		}
	}
	if !validPluginToolName(tool.RemoteName) || !validPluginToolName(tool.ExposedName) {
		return PluginTool{}, errors.New("invalid plugin tool name")
	}
	if _, err := time.Parse(time.RFC3339Nano, tool.DiscoveredAt); err != nil {
		return PluginTool{}, errors.New("invalid plugin tool discovered_at")
	}
	trimmed := strings.TrimSpace(string(tool.InputSchemaJSON))
	if trimmed == "" {
		trimmed = `{}`
	}
	if len(trimmed) > 64<<10 || !json.Valid([]byte(trimmed)) {
		return PluginTool{}, errors.New("plugin tool input schema must be a valid JSON object")
	}
	var schema map[string]any
	if json.Unmarshal([]byte(trimmed), &schema) != nil || schema == nil {
		return PluginTool{}, errors.New("plugin tool input schema must be a valid JSON object")
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return PluginTool{}, err
	}
	tool.InputSchemaJSON = encoded
	return tool, nil
}

type pluginScanner func(...any) error

func scanPlugin(scan pluginScanner) (Plugin, error) {
	var plugin Plugin
	var argsJSON, envJSON, refsJSON string
	var enabled int
	if err := scan(&plugin.ID, &plugin.Slug, &plugin.Name, &plugin.Version, &plugin.Description, &plugin.ManifestVersion, &plugin.RootPath, &plugin.Command, &argsJSON, &envJSON, &refsJSON, &enabled, &plugin.Status, &plugin.Revision, &plugin.ManifestHash, &plugin.LastCheckedAt, &plugin.LastError, &plugin.CreatedAt, &plugin.UpdatedAt); err != nil {
		return Plugin{}, err
	}
	if json.Unmarshal([]byte(argsJSON), &plugin.Args) != nil || plugin.Args == nil {
		return Plugin{}, fmt.Errorf("stored plugin args for %s are invalid", plugin.ID)
	}
	if json.Unmarshal([]byte(envJSON), &plugin.Env) != nil || plugin.Env == nil {
		return Plugin{}, fmt.Errorf("stored plugin env for %s is invalid", plugin.ID)
	}
	if json.Unmarshal([]byte(refsJSON), &plugin.SecretRefs) != nil || plugin.SecretRefs == nil {
		return Plugin{}, fmt.Errorf("stored plugin secret refs for %s are invalid", plugin.ID)
	}
	plugin.Enabled = enabled != 0
	return plugin, nil
}

type pluginToolScanner func(...any) error

func scanPluginTool(scan pluginToolScanner) (PluginTool, error) {
	var tool PluginTool
	var schema string
	if err := scan(&tool.PluginID, &tool.RemoteName, &tool.ExposedName, &tool.Description, &schema, &tool.DiscoveredAt); err != nil {
		return PluginTool{}, err
	}
	if !json.Valid([]byte(schema)) {
		return PluginTool{}, errors.New("stored plugin tool input schema is invalid")
	}
	tool.InputSchemaJSON = json.RawMessage(schema)
	return tool, nil
}

func validatePluginStatus(status, checkedAt, lastError string) error {
	if err := validatePluginText("status", status, 64, true); err != nil {
		return err
	}
	if checkedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, checkedAt); err != nil {
			return errors.New("invalid plugin last_checked_at")
		}
	}
	return validatePluginText("last_error", lastError, 4096, false)
}

func validatePluginText(name, value string, max int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("plugin %s is required", name)
	}
	if len(value) > max || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid plugin %s", name)
	}
	return nil
}

func validPluginSlug(value string) bool {
	if value == "" || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
			return false
		}
	}
	return true
}

func validPluginEnvName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func validPluginToolName(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:/-", char) {
			continue
		}
		return false
	}
	return true
}

func sensitivePluginKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(key))
	for _, marker := range []string{"password", "passwd", "secret", "token", "apikey", "credential", "privatekey", "accesskey", "authorization", "cookie", "bearer", "jwt"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func pluginConstraintError(err error) error {
	if isUniqueConstraint(err) {
		return fmt.Errorf("%w: plugin slug, remote name, or exposed tool name already exists", ErrConflict)
	}
	return err
}
