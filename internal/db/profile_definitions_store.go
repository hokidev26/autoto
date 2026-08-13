package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	DefinitionScopeGlobal    = "global"
	DefinitionScopeProject   = "project"
	DefinitionScopeWorkspace = "workspace"
)

type DefinitionScopeTarget struct {
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

func (target DefinitionScopeTarget) normalized() (DefinitionScopeTarget, string, error) {
	target.Scope = strings.TrimSpace(strings.ToLower(target.Scope))
	target.ProjectID = strings.TrimSpace(target.ProjectID)
	target.WorkspaceID = strings.TrimSpace(target.WorkspaceID)
	if target.ProjectID != "" && !validDefinitionScopeID(target.ProjectID) {
		return DefinitionScopeTarget{}, "", errors.New("projectId is invalid")
	}
	if target.WorkspaceID != "" && !validDefinitionScopeID(target.WorkspaceID) {
		return DefinitionScopeTarget{}, "", errors.New("workspaceId is invalid")
	}
	switch target.Scope {
	case DefinitionScopeGlobal:
		if target.ProjectID != "" || target.WorkspaceID != "" {
			return DefinitionScopeTarget{}, "", errors.New("global scope cannot include projectId or workspaceId")
		}
		return target, "global", nil
	case DefinitionScopeProject:
		if target.ProjectID == "" || target.WorkspaceID != "" {
			return DefinitionScopeTarget{}, "", errors.New("project scope requires projectId and forbids workspaceId")
		}
		return target, fmt.Sprintf("project:%d:%s", len(target.ProjectID), target.ProjectID), nil
	case DefinitionScopeWorkspace:
		if target.ProjectID == "" || target.WorkspaceID == "" {
			return DefinitionScopeTarget{}, "", errors.New("workspace scope requires projectId and workspaceId")
		}
		return target, fmt.Sprintf("workspace:%d:%s:%d:%s", len(target.ProjectID), target.ProjectID, len(target.WorkspaceID), target.WorkspaceID), nil
	default:
		return DefinitionScopeTarget{}, "", errors.New("scope must be global, project, or workspace")
	}
}

func validDefinitionScopeID(value string) bool {
	if len(value) < 1 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validDefinitionKey(value string) bool {
	if len(value) < 1 || len(value) > 64 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return false
	}
	return true
}

type storedDefinition struct {
	ID          string
	Key         string
	DisplayName string
	Summary     string
	Body        string
	Scope       string
	ProjectID   string
	WorkspaceID string
	Revision    int64
	DeletedAt   string
	CreatedAt   string
	UpdatedAt   string
}

type storedDefinitionRevision struct {
	storedDefinition
	Operation            string
	RestoredFromRevision int64
}

type definitionStoreSpec struct {
	headTable     string
	revisionTable string
	bodyColumn    string
	schemaSQL     string
}

func definitionStoreSchemaSQL(spec definitionStoreSpec) string {
	return spec.schemaSQL
}

func (s *storedDefStore) ensureDefinitionTables(ctx context.Context, spec definitionStoreSpec) error {
	_, err := s.db.ExecContext(ctx, definitionStoreSchemaSQL(spec))
	return err
}

func definitionColumns(bodyColumn string) string {
	return "id, definition_key, display_name, summary, " + bodyColumn + ", scope, COALESCE(project_id,''), COALESCE(workspace_id,''), revision, COALESCE(deleted_at,''), created_at, updated_at"
}

func scanStoredDefinition(scanner interface{ Scan(...any) error }) (storedDefinition, error) {
	var value storedDefinition
	err := scanner.Scan(&value.ID, &value.Key, &value.DisplayName, &value.Summary, &value.Body, &value.Scope, &value.ProjectID, &value.WorkspaceID, &value.Revision, &value.DeletedAt, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func insertStoredDefinitionRevision(ctx context.Context, tx *sql.Tx, spec definitionStoreSpec, value storedDefinition, operation string, restoredFrom int64) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		id, definition_id, revision, operation, restored_from_revision, definition_key, display_name, summary, %s,
		scope, project_id, workspace_id, deleted_at, head_created_at, head_updated_at, created_at
	) VALUES (?, ?, ?, ?, NULLIF(?,0), ?, ?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, ?, ?)`, spec.revisionTable, spec.bodyColumn),
		NewID(), value.ID, value.Revision, operation, restoredFrom, value.Key, value.DisplayName, value.Summary, value.Body,
		value.Scope, value.ProjectID, value.WorkspaceID, value.DeletedAt, value.CreatedAt, value.UpdatedAt, Now())
	return err
}

func (s *storedDefStore) createStoredDefinition(ctx context.Context, spec definitionStoreSpec, target DefinitionScopeTarget, key, displayName, summary, body string) (storedDefinition, error) {
	if err := s.ensureDefinitionTables(ctx, spec); err != nil {
		return storedDefinition{}, err
	}
	target, targetKey, err := target.normalized()
	if err != nil {
		return storedDefinition{}, err
	}
	now := Now()
	value := storedDefinition{ID: NewID(), Key: key, DisplayName: displayName, Summary: summary, Body: body, Scope: target.Scope, ProjectID: target.ProjectID, WorkspaceID: target.WorkspaceID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storedDefinition{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (id, definition_key, display_name, summary, %s, scope, project_id, workspace_id, target_key, revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), ?, 1, ?, ?)`, spec.headTable, spec.bodyColumn),
		value.ID, value.Key, value.DisplayName, value.Summary, value.Body, value.Scope, value.ProjectID, value.WorkspaceID, targetKey, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return storedDefinition{}, fmt.Errorf("%w: definition key already exists in scope", ErrConflict)
		}
		return storedDefinition{}, err
	}
	if err := insertStoredDefinitionRevision(ctx, tx, spec, value, "create", 0); err != nil {
		return storedDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return storedDefinition{}, err
	}
	return value, nil
}

func (s *storedDefStore) getStoredDefinition(ctx context.Context, spec definitionStoreSpec, id string, includeDeleted bool) (storedDefinition, error) {
	if err := s.ensureDefinitionTables(ctx, spec); err != nil {
		return storedDefinition{}, err
	}
	query := `SELECT ` + definitionColumns(spec.bodyColumn) + ` FROM ` + spec.headTable + ` WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	return scanStoredDefinition(s.db.QueryRowContext(ctx, query, id))
}

func (s *storedDefStore) listStoredDefinitions(ctx context.Context, spec definitionStoreSpec, target DefinitionScopeTarget) ([]storedDefinition, error) {
	if err := s.ensureDefinitionTables(ctx, spec); err != nil {
		return nil, err
	}
	_, targetKey, err := target.normalized()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+definitionColumns(spec.bodyColumn)+` FROM `+spec.headTable+` WHERE target_key = ? AND deleted_at IS NULL ORDER BY definition_key COLLATE NOCASE, id`, targetKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []storedDefinition{}
	for rows.Next() {
		value, err := scanStoredDefinition(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *storedDefStore) updateStoredDefinitionCAS(ctx context.Context, spec definitionStoreSpec, id string, expectedRevision int64, key, displayName, summary, body string) (storedDefinition, error) {
	if expectedRevision < 1 {
		return storedDefinition{}, errors.New("expected revision must be positive")
	}
	if err := s.ensureDefinitionTables(ctx, spec); err != nil {
		return storedDefinition{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storedDefinition{}, err
	}
	defer tx.Rollback()
	current, err := scanStoredDefinition(tx.QueryRowContext(ctx, `SELECT `+definitionColumns(spec.bodyColumn)+` FROM `+spec.headTable+` WHERE id = ?`, id))
	if err != nil {
		return storedDefinition{}, err
	}
	if current.DeletedAt != "" {
		return storedDefinition{}, sql.ErrNoRows
	}
	if current.Revision != expectedRevision {
		return storedDefinition{}, fmt.Errorf("%w: definition revision changed", ErrConflict)
	}
	current.Key, current.DisplayName, current.Summary, current.Body = key, displayName, summary, body
	current.Revision++
	current.UpdatedAt = Now()
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET definition_key = ?, display_name = ?, summary = ?, %s = ?, revision = ?, updated_at = ? WHERE id = ? AND revision = ? AND deleted_at IS NULL`, spec.headTable, spec.bodyColumn),
		current.Key, current.DisplayName, current.Summary, current.Body, current.Revision, current.UpdatedAt, current.ID, expectedRevision)
	if err != nil {
		if isUniqueConstraint(err) {
			return storedDefinition{}, fmt.Errorf("%w: definition key already exists in scope", ErrConflict)
		}
		return storedDefinition{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return storedDefinition{}, fmt.Errorf("%w: definition revision changed", ErrConflict)
	}
	if err := insertStoredDefinitionRevision(ctx, tx, spec, current, "update", 0); err != nil {
		return storedDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return storedDefinition{}, err
	}
	return current, nil
}

func (s *storedDefStore) deleteStoredDefinitionCAS(ctx context.Context, spec definitionStoreSpec, id string, expectedRevision int64) (storedDefinition, error) {
	if expectedRevision < 1 {
		return storedDefinition{}, errors.New("expected revision must be positive")
	}
	if err := s.ensureDefinitionTables(ctx, spec); err != nil {
		return storedDefinition{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storedDefinition{}, err
	}
	defer tx.Rollback()
	current, err := scanStoredDefinition(tx.QueryRowContext(ctx, `SELECT `+definitionColumns(spec.bodyColumn)+` FROM `+spec.headTable+` WHERE id = ?`, id))
	if err != nil {
		return storedDefinition{}, err
	}
	if current.DeletedAt != "" {
		return storedDefinition{}, sql.ErrNoRows
	}
	if current.Revision != expectedRevision {
		return storedDefinition{}, fmt.Errorf("%w: definition revision changed", ErrConflict)
	}
	current.Revision++
	current.DeletedAt = Now()
	current.UpdatedAt = Now()
	result, err := tx.ExecContext(ctx, `UPDATE `+spec.headTable+` SET revision = ?, deleted_at = ?, updated_at = ? WHERE id = ? AND revision = ? AND deleted_at IS NULL`, current.Revision, current.DeletedAt, current.UpdatedAt, current.ID, expectedRevision)
	if err != nil {
		return storedDefinition{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return storedDefinition{}, fmt.Errorf("%w: definition revision changed", ErrConflict)
	}
	if err := insertStoredDefinitionRevision(ctx, tx, spec, current, "delete", 0); err != nil {
		return storedDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return storedDefinition{}, err
	}
	return current, nil
}

func (s *storedDefStore) listStoredDefinitionRevisions(ctx context.Context, spec definitionStoreSpec, id string) ([]storedDefinitionRevision, error) {
	if err := s.ensureDefinitionTables(ctx, spec); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT definition_id, definition_key, display_name, summary, `+spec.bodyColumn+`, scope, COALESCE(project_id,''), COALESCE(workspace_id,''), revision, COALESCE(deleted_at,''), head_created_at, head_updated_at, operation, COALESCE(restored_from_revision,0) FROM `+spec.revisionTable+` WHERE definition_id = ? ORDER BY revision DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []storedDefinitionRevision{}
	for rows.Next() {
		var value storedDefinitionRevision
		if err := rows.Scan(&value.ID, &value.Key, &value.DisplayName, &value.Summary, &value.Body, &value.Scope, &value.ProjectID, &value.WorkspaceID, &value.Revision, &value.DeletedAt, &value.CreatedAt, &value.UpdatedAt, &value.Operation, &value.RestoredFromRevision); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *storedDefStore) restoreStoredDefinitionCAS(ctx context.Context, spec definitionStoreSpec, id string, sourceRevision, expectedRevision int64) (storedDefinition, error) {
	if sourceRevision < 1 || expectedRevision < 1 {
		return storedDefinition{}, errors.New("source and expected revisions must be positive")
	}
	if err := s.ensureDefinitionTables(ctx, spec); err != nil {
		return storedDefinition{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storedDefinition{}, err
	}
	defer tx.Rollback()
	current, err := scanStoredDefinition(tx.QueryRowContext(ctx, `SELECT `+definitionColumns(spec.bodyColumn)+` FROM `+spec.headTable+` WHERE id = ?`, id))
	if err != nil {
		return storedDefinition{}, err
	}
	if current.Revision != expectedRevision {
		return storedDefinition{}, fmt.Errorf("%w: definition revision changed", ErrConflict)
	}
	var source storedDefinition
	err = tx.QueryRowContext(ctx, `SELECT definition_id, definition_key, display_name, summary, `+spec.bodyColumn+`, scope, COALESCE(project_id,''), COALESCE(workspace_id,''), revision, COALESCE(deleted_at,''), head_created_at, head_updated_at FROM `+spec.revisionTable+` WHERE definition_id = ? AND revision = ?`, id, sourceRevision).
		Scan(&source.ID, &source.Key, &source.DisplayName, &source.Summary, &source.Body, &source.Scope, &source.ProjectID, &source.WorkspaceID, &source.Revision, &source.DeletedAt, &source.CreatedAt, &source.UpdatedAt)
	if err != nil {
		return storedDefinition{}, err
	}
	if source.DeletedAt != "" {
		return storedDefinition{}, errors.New("cannot restore a deleted revision")
	}
	_, targetKey, err := (DefinitionScopeTarget{Scope: source.Scope, ProjectID: source.ProjectID, WorkspaceID: source.WorkspaceID}).normalized()
	if err != nil {
		return storedDefinition{}, err
	}
	restored := source
	restored.ID = current.ID
	restored.Revision = current.Revision + 1
	restored.DeletedAt = ""
	restored.CreatedAt = current.CreatedAt
	restored.UpdatedAt = Now()
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET definition_key = ?, display_name = ?, summary = ?, %s = ?, scope = ?, project_id = NULLIF(?,''), workspace_id = NULLIF(?,''), target_key = ?, revision = ?, deleted_at = NULL, updated_at = ? WHERE id = ? AND revision = ?`, spec.headTable, spec.bodyColumn),
		restored.Key, restored.DisplayName, restored.Summary, restored.Body, restored.Scope, restored.ProjectID, restored.WorkspaceID, targetKey, restored.Revision, restored.UpdatedAt, restored.ID, expectedRevision)
	if err != nil {
		if isUniqueConstraint(err) {
			return storedDefinition{}, fmt.Errorf("%w: definition key already exists in restored scope", ErrConflict)
		}
		return storedDefinition{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return storedDefinition{}, fmt.Errorf("%w: definition revision changed", ErrConflict)
	}
	if err := insertStoredDefinitionRevision(ctx, tx, spec, restored, "restore", sourceRevision); err != nil {
		return storedDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return storedDefinition{}, err
	}
	return restored, nil
}
