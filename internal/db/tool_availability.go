package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ToolAvailabilityScopeGlobal    = "global"
	ToolAvailabilityScopeProject   = "project"
	ToolAvailabilityScopeWorkspace = "workspace"

	ToolAvailabilityEnabled  = "enabled"
	ToolAvailabilityDisabled = "disabled"
)

type ToolAvailabilityTarget struct {
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type ToolAvailabilityRule struct {
	ID          string `json:"id"`
	ToolName    string `json:"toolName"`
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	State       string `json:"state"`
	Revision    int64  `json:"revision"`
	DeletedAt   string `json:"deletedAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type ToolAvailabilityRevision struct {
	Sequence    int64  `json:"sequence"`
	RuleID      string `json:"ruleId"`
	Revision    int64  `json:"revision"`
	Operation   string `json:"operation"`
	Actor       string `json:"actor"`
	ToolName    string `json:"toolName"`
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	State       string `json:"state"`
	Deleted     bool   `json:"deleted"`
	CreatedAt   string `json:"createdAt"`
}

type ToolAvailabilityDecision struct {
	ToolName       string `json:"toolName"`
	Enabled        bool   `json:"enabled"`
	State          string `json:"state"`
	SourceScope    string `json:"sourceScope,omitempty"`
	SourceRuleID   string `json:"sourceRuleId,omitempty"`
	SourceRevision int64  `json:"sourceRevision,omitempty"`
	Default        bool   `json:"default"`
}

const toolAvailabilitySchemaSQL = `
CREATE TABLE IF NOT EXISTS tool_availability_rules (
  id TEXT PRIMARY KEY,
  tool_name TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('global','project','workspace')),
  project_id TEXT,
  workspace_id TEXT,
  target_key TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('enabled','disabled')),
  revision INTEGER NOT NULL CHECK (revision > 0),
  deleted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(tool_name, scope, target_key)
);
CREATE INDEX IF NOT EXISTS idx_tool_availability_rules_target ON tool_availability_rules(scope, target_key, deleted_at, tool_name);
CREATE TABLE IF NOT EXISTS tool_availability_revisions (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  rule_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  operation TEXT NOT NULL CHECK (operation IN ('create','update','restore','delete')),
  actor TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  scope TEXT NOT NULL,
  project_id TEXT,
  workspace_id TEXT,
  state TEXT NOT NULL,
  deleted INTEGER NOT NULL CHECK (deleted IN (0,1)),
  created_at TEXT NOT NULL,
  UNIQUE(rule_id, revision)
);
CREATE INDEX IF NOT EXISTS idx_tool_availability_revisions_rule ON tool_availability_revisions(rule_id, revision DESC);
`

func (s *toolAvailabilityStore) EnsureToolAvailability(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("tool availability store is not configured")
	}
	_, err := s.db.ExecContext(ctx, toolAvailabilitySchemaSQL)
	return err
}

func normalizeToolAvailabilityTarget(target ToolAvailabilityTarget) (ToolAvailabilityTarget, string, error) {
	target.Scope = strings.TrimSpace(target.Scope)
	if target.Scope == "" {
		target.Scope = ToolAvailabilityScopeGlobal
	}
	target.ProjectID = strings.TrimSpace(target.ProjectID)
	target.WorkspaceID = strings.TrimSpace(target.WorkspaceID)
	if err := validateToolAvailabilityIdentifier("projectId", target.ProjectID); err != nil {
		return ToolAvailabilityTarget{}, "", err
	}
	if err := validateToolAvailabilityIdentifier("workspaceId", target.WorkspaceID); err != nil {
		return ToolAvailabilityTarget{}, "", err
	}
	switch target.Scope {
	case ToolAvailabilityScopeGlobal:
		if target.ProjectID != "" || target.WorkspaceID != "" {
			return ToolAvailabilityTarget{}, "", errors.New("global tool rules cannot specify projectId or workspaceId")
		}
		return target, "", nil
	case ToolAvailabilityScopeProject:
		if target.ProjectID == "" || target.WorkspaceID != "" {
			return ToolAvailabilityTarget{}, "", errors.New("project tool rules require projectId and forbid workspaceId")
		}
		return target, target.ProjectID, nil
	case ToolAvailabilityScopeWorkspace:
		if target.ProjectID == "" || target.WorkspaceID == "" {
			return ToolAvailabilityTarget{}, "", errors.New("workspace tool rules require projectId and workspaceId")
		}
		return target, target.ProjectID + "\x1f" + target.WorkspaceID, nil
	default:
		return ToolAvailabilityTarget{}, "", errors.New("tool rule scope must be global, project, or workspace")
	}
}

func validateToolAvailabilityIdentifier(field, value string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || len(value) > 128 {
		return fmt.Errorf("%s must be valid UTF-8 and at most 128 bytes", field)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s cannot contain control characters", field)
		}
	}
	return nil
}

func normalizeToolAvailabilityName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || !utf8.ValidString(name) {
		return "", errors.New("toolName must be valid UTF-8 and between 1 and 128 bytes")
	}
	for i, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("/\\?#", r) {
			return "", errors.New("toolName contains unsupported characters")
		}
		if i == 0 && (r == '.' || r == ':') {
			return "", errors.New("toolName has an invalid prefix")
		}
	}
	return name, nil
}

func normalizeToolAvailabilityState(state string) (string, error) {
	state = strings.TrimSpace(strings.ToLower(state))
	if state != ToolAvailabilityEnabled && state != ToolAvailabilityDisabled {
		return "", errors.New("tool rule state must be enabled or disabled")
	}
	return state, nil
}

func normalizeToolAvailabilityActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "system"
	}
	if len(actor) > 128 {
		return actor[:128]
	}
	return actor
}

func scanToolAvailabilityRule(scan func(...any) error) (ToolAvailabilityRule, error) {
	var rule ToolAvailabilityRule
	err := scan(&rule.ID, &rule.ToolName, &rule.Scope, &rule.ProjectID, &rule.WorkspaceID, &rule.State, &rule.Revision, &rule.DeletedAt, &rule.CreatedAt, &rule.UpdatedAt)
	return rule, err
}

const toolAvailabilityRuleColumns = `id, tool_name, scope, COALESCE(project_id,''), COALESCE(workspace_id,''), state, revision, COALESCE(deleted_at,''), created_at, updated_at`

func insertToolAvailabilityRevision(ctx context.Context, tx *sql.Tx, rule ToolAvailabilityRule, operation, actor string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tool_availability_revisions (
		rule_id, revision, operation, actor, tool_name, scope, project_id, workspace_id, state, deleted, created_at
	) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		rule.ID, rule.Revision, operation, normalizeToolAvailabilityActor(actor), rule.ToolName, rule.Scope,
		rule.ProjectID, rule.WorkspaceID, rule.State, boolInt(rule.DeletedAt != ""), Now())
	return err
}

func (s *toolAvailabilityStore) SetToolAvailabilityRuleCAS(ctx context.Context, target ToolAvailabilityTarget, toolName, state string, expectedRevision int64, actor string) (ToolAvailabilityRule, error) {
	if err := s.EnsureToolAvailability(ctx); err != nil {
		return ToolAvailabilityRule{}, err
	}
	target, targetKey, err := normalizeToolAvailabilityTarget(target)
	if err != nil {
		return ToolAvailabilityRule{}, err
	}
	toolName, err = normalizeToolAvailabilityName(toolName)
	if err != nil {
		return ToolAvailabilityRule{}, err
	}
	state, err = normalizeToolAvailabilityState(state)
	if err != nil {
		return ToolAvailabilityRule{}, err
	}
	if expectedRevision < 0 {
		return ToolAvailabilityRule{}, errors.New("expectedRevision cannot be negative")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolAvailabilityRule{}, err
	}
	defer tx.Rollback()
	current, getErr := scanToolAvailabilityRule(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+toolAvailabilityRuleColumns+` FROM tool_availability_rules WHERE tool_name = ? AND scope = ? AND target_key = ?`, toolName, target.Scope, targetKey).Scan(dest...)
	})
	now := Now()
	operation := "create"
	var next ToolAvailabilityRule
	if errors.Is(getErr, sql.ErrNoRows) {
		if expectedRevision != 0 {
			return ToolAvailabilityRule{}, fmt.Errorf("%w: tool rule does not exist at expected revision", ErrConflict)
		}
		next = ToolAvailabilityRule{ID: NewID(), ToolName: toolName, Scope: target.Scope, ProjectID: target.ProjectID, WorkspaceID: target.WorkspaceID, State: state, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_availability_rules (id, tool_name, scope, project_id, workspace_id, target_key, state, revision, created_at, updated_at) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?)`,
			next.ID, next.ToolName, next.Scope, next.ProjectID, next.WorkspaceID, targetKey, next.State, next.Revision, next.CreatedAt, next.UpdatedAt); err != nil {
			if isUniqueConstraint(err) {
				return ToolAvailabilityRule{}, fmt.Errorf("%w: tool rule was created by another client", ErrConflict)
			}
			return ToolAvailabilityRule{}, err
		}
	} else if getErr != nil {
		return ToolAvailabilityRule{}, getErr
	} else {
		if expectedRevision != current.Revision {
			return ToolAvailabilityRule{}, fmt.Errorf("%w: tool rule revision changed", ErrConflict)
		}
		next = current
		next.State = state
		next.Revision++
		next.DeletedAt = ""
		next.UpdatedAt = now
		operation = "update"
		if current.DeletedAt != "" {
			operation = "restore"
		}
		result, err := tx.ExecContext(ctx, `UPDATE tool_availability_rules SET state = ?, revision = ?, deleted_at = NULL, updated_at = ? WHERE id = ? AND revision = ?`, next.State, next.Revision, next.UpdatedAt, next.ID, expectedRevision)
		if err != nil {
			return ToolAvailabilityRule{}, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return ToolAvailabilityRule{}, err
		} else if affected != 1 {
			return ToolAvailabilityRule{}, fmt.Errorf("%w: tool rule revision changed", ErrConflict)
		}
	}
	if err := insertToolAvailabilityRevision(ctx, tx, next, operation, actor); err != nil {
		return ToolAvailabilityRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolAvailabilityRule{}, err
	}
	return next, nil
}

func (s *toolAvailabilityStore) DeleteToolAvailabilityRuleCAS(ctx context.Context, ruleID string, expectedRevision int64, actor string) (ToolAvailabilityRule, error) {
	if err := s.EnsureToolAvailability(ctx); err != nil {
		return ToolAvailabilityRule{}, err
	}
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" || expectedRevision < 1 {
		return ToolAvailabilityRule{}, errors.New("rule id and positive expectedRevision are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolAvailabilityRule{}, err
	}
	defer tx.Rollback()
	current, err := scanToolAvailabilityRule(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+toolAvailabilityRuleColumns+` FROM tool_availability_rules WHERE id = ?`, ruleID).Scan(dest...)
	})
	if err != nil {
		return ToolAvailabilityRule{}, err
	}
	if current.Revision != expectedRevision {
		return ToolAvailabilityRule{}, fmt.Errorf("%w: tool rule revision changed", ErrConflict)
	}
	if current.DeletedAt != "" {
		return ToolAvailabilityRule{}, sql.ErrNoRows
	}
	current.Revision++
	current.DeletedAt = Now()
	current.UpdatedAt = current.DeletedAt
	result, err := tx.ExecContext(ctx, `UPDATE tool_availability_rules SET revision = ?, deleted_at = ?, updated_at = ? WHERE id = ? AND revision = ? AND deleted_at IS NULL`, current.Revision, current.DeletedAt, current.UpdatedAt, current.ID, expectedRevision)
	if err != nil {
		return ToolAvailabilityRule{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ToolAvailabilityRule{}, err
	} else if affected != 1 {
		return ToolAvailabilityRule{}, fmt.Errorf("%w: tool rule revision changed", ErrConflict)
	}
	if err := insertToolAvailabilityRevision(ctx, tx, current, "delete", actor); err != nil {
		return ToolAvailabilityRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolAvailabilityRule{}, err
	}
	return current, nil
}

func (s *toolAvailabilityStore) GetToolAvailabilityRule(ctx context.Context, ruleID string) (ToolAvailabilityRule, error) {
	if err := s.EnsureToolAvailability(ctx); err != nil {
		return ToolAvailabilityRule{}, err
	}
	return scanToolAvailabilityRule(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+toolAvailabilityRuleColumns+` FROM tool_availability_rules WHERE id = ?`, strings.TrimSpace(ruleID)).Scan(dest...)
	})
}

func (s *toolAvailabilityStore) ListToolAvailabilityRules(ctx context.Context, target ToolAvailabilityTarget, includeDeleted bool) ([]ToolAvailabilityRule, error) {
	if err := s.EnsureToolAvailability(ctx); err != nil {
		return nil, err
	}
	target, targetKey, err := normalizeToolAvailabilityTarget(target)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + toolAvailabilityRuleColumns + ` FROM tool_availability_rules WHERE scope = ? AND target_key = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY tool_name COLLATE NOCASE, id`
	rows, err := s.db.QueryContext(ctx, query, target.Scope, targetKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ToolAvailabilityRule, 0)
	for rows.Next() {
		item, err := scanToolAvailabilityRule(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *toolAvailabilityStore) ListToolAvailabilityRevisions(ctx context.Context, ruleID string) ([]ToolAvailabilityRevision, error) {
	if err := s.EnsureToolAvailability(ctx); err != nil {
		return nil, err
	}
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return nil, errors.New("rule id is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, rule_id, revision, operation, actor, tool_name, scope, COALESCE(project_id,''), COALESCE(workspace_id,''), state, deleted, created_at FROM tool_availability_revisions WHERE rule_id = ? ORDER BY revision DESC`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ToolAvailabilityRevision, 0)
	for rows.Next() {
		var item ToolAvailabilityRevision
		var deleted int
		if err := rows.Scan(&item.Sequence, &item.RuleID, &item.Revision, &item.Operation, &item.Actor, &item.ToolName, &item.Scope, &item.ProjectID, &item.WorkspaceID, &item.State, &deleted, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Deleted = deleted != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if _, err := s.GetToolAvailabilityRule(ctx, ruleID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *toolAvailabilityStore) ResolveToolAvailability(ctx context.Context, target ToolAvailabilityTarget, toolName string) (ToolAvailabilityDecision, error) {
	normalized, err := normalizeToolAvailabilityName(toolName)
	if err != nil {
		return ToolAvailabilityDecision{}, err
	}
	decisions, err := s.ResolveToolAvailabilities(ctx, target, []string{normalized})
	if err != nil {
		return ToolAvailabilityDecision{}, err
	}
	return decisions[normalized], nil
}

func (s *toolAvailabilityStore) ResolveToolAvailabilities(ctx context.Context, target ToolAvailabilityTarget, toolNames []string) (map[string]ToolAvailabilityDecision, error) {
	if err := s.EnsureToolAvailability(ctx); err != nil {
		return nil, err
	}
	target, _, err := normalizeToolAvailabilityTarget(target)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(toolNames))
	seen := make(map[string]struct{}, len(toolNames))
	for _, name := range toolNames {
		normalized, err := normalizeToolAvailabilityName(name)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		names = append(names, normalized)
	}
	candidates := []ToolAvailabilityTarget{{Scope: ToolAvailabilityScopeGlobal}}
	if target.Scope == ToolAvailabilityScopeProject || target.Scope == ToolAvailabilityScopeWorkspace {
		candidates = append(candidates, ToolAvailabilityTarget{Scope: ToolAvailabilityScopeProject, ProjectID: target.ProjectID})
	}
	if target.Scope == ToolAvailabilityScopeWorkspace {
		candidates = append(candidates, target)
	}
	decisions := make(map[string]ToolAvailabilityDecision, len(names))
	for _, toolName := range names {
		decision := ToolAvailabilityDecision{ToolName: toolName, Enabled: true, State: ToolAvailabilityEnabled, Default: true}
		for _, candidate := range candidates {
			normalized, key, _ := normalizeToolAvailabilityTarget(candidate)
			rule, err := scanToolAvailabilityRule(func(dest ...any) error {
				return s.db.QueryRowContext(ctx, `SELECT `+toolAvailabilityRuleColumns+` FROM tool_availability_rules WHERE tool_name = ? AND scope = ? AND target_key = ? AND deleted_at IS NULL`, toolName, normalized.Scope, key).Scan(dest...)
			})
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			decision.Enabled = rule.State == ToolAvailabilityEnabled
			decision.State = rule.State
			decision.SourceScope = rule.Scope
			decision.SourceRuleID = rule.ID
			decision.SourceRevision = rule.Revision
			decision.Default = false
		}
		decisions[toolName] = decision
	}
	return decisions, nil
}
