package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/skills"
)

const (
	SkillScopeGlobal    = "global"
	SkillScopeProject   = "project"
	SkillScopeWorkspace = "workspace"
)

type SkillRestoreReviewRequiredError struct {
	ScanVerdict    string          `json:"scanVerdict"`
	ScanFindings   json.RawMessage `json:"scanFindings"`
	ContentHash    string          `json:"contentHash"`
	ScannerVersion int             `json:"scannerVersion"`
}

func (e *SkillRestoreReviewRequiredError) Error() string {
	return "conflict: restored review skill requires acknowledgeRisk and matching acknowledgedContentHash"
}

func (e *SkillRestoreReviewRequiredError) Unwrap() error {
	return ErrConflict
}

type skillCursor struct {
	Version          int    `json:"v"`
	Kind             string `json:"kind"`
	SnapshotSequence int64  `json:"snapshotSequence"`
	Scope            string `json:"scope,omitempty"`
	ProjectID        string `json:"projectId,omitempty"`
	WorklineID       string `json:"worklineId,omitempty"`
	AgentID          string `json:"agentId,omitempty"`
	SkillID          string `json:"skillId,omitempty"`
	AfterCommand     string `json:"afterCommand,omitempty"`
	AfterSkillID     string `json:"afterSkillId,omitempty"`
	AfterRevisionNo  int64  `json:"afterRevisionNo,omitempty"`
}

func normalizeSkillPageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func encodeSkillCursor(cursor skillCursor) (string, error) {
	cursor.Version = 1
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeSkillCursor(value string) (skillCursor, error) {
	if strings.TrimSpace(value) == "" {
		return skillCursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return skillCursor{}, errors.New("invalid skill cursor")
	}
	var cursor skillCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Version != 1 {
		return skillCursor{}, errors.New("invalid skill cursor")
	}
	return cursor, nil
}

func normalizeSkillScopeTarget(target SkillScopeTarget) (SkillScopeTarget, error) {
	target.Scope = strings.TrimSpace(target.Scope)
	if target.Scope == "" {
		target.Scope = SkillScopeGlobal
	}
	target.ProjectID = strings.TrimSpace(target.ProjectID)
	target.WorklineID = strings.TrimSpace(target.WorklineID)
	if !validSkillScopeTarget(target.Scope, target.ProjectID, target.WorklineID) {
		return SkillScopeTarget{}, errors.New("invalid skill scope target")
	}
	return target, nil
}

type skillScopeQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateSkillScopeTargetContext(ctx context.Context, queryer skillScopeQueryer, target SkillScopeTarget) error {
	switch target.Scope {
	case SkillScopeGlobal:
		return nil
	case SkillScopeProject:
		var exists int
		if err := queryer.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, target.ProjectID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: project not found", ErrConflict)
			}
			return err
		}
		return nil
	case SkillScopeWorkspace:
		var projectID string
		if err := queryer.QueryRowContext(ctx, `SELECT project_id FROM worklines WHERE id = ?`, target.WorklineID).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: workspace workline not found", ErrConflict)
			}
			return err
		}
		if projectID != target.ProjectID {
			return fmt.Errorf("%w: workspace workline belongs to another project", ErrConflict)
		}
		return nil
	default:
		return errors.New("invalid skill scope")
	}
}

func validateSkillScopeTx(ctx context.Context, tx *sql.Tx, skill Skill) error {
	target, err := normalizeSkillScopeTarget(SkillScopeTarget{Scope: skill.Scope, ProjectID: skill.ProjectID, WorklineID: skill.WorklineID})
	if err != nil {
		return err
	}
	return validateSkillScopeTargetContext(ctx, tx, target)
}

func insertSkillRevision(ctx context.Context, tx *sql.Tx, skill Skill, operation, actor string, restoredFromRevisionNo int64) (int64, error) {
	if skill.RevisionNo < 1 {
		return 0, errors.New("skill revision number must be positive")
	}
	createdAt := Now()
	result, err := tx.ExecContext(ctx, `INSERT INTO skill_revisions (
		id, skill_id, revision_no, operation, actor, restored_from_revision_no,
		name, command, description, prompt, source, scope, project_id, workline_id, deleted_at,
		content_hash, enabled, scan_verdict, scan_findings_json, scanner_version,
		risk_acknowledged_at, risk_acknowledged_by, risk_acknowledged_hash,
		head_created_at, head_updated_at, created_at
	) VALUES (?, ?, ?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		NewID(), skill.ID, skill.RevisionNo, operation, normalizeSkillAuditActor(actor), restoredFromRevisionNo,
		skill.Name, skill.Command, skill.Description, skill.Prompt, skill.Source, skill.Scope, skill.ProjectID, skill.WorklineID, skill.DeletedAt,
		skill.ContentHash, boolInt(skill.Enabled), skill.ScanVerdict, string(skill.ScanFindings), skill.ScannerVersion,
		skill.RiskAcknowledgedAt, skill.RiskAcknowledgedBy, skill.RiskAcknowledgedHash,
		skill.CreatedAt, skill.UpdatedAt, createdAt,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func scanSkillRevision(scan skillScanner) (SkillRevision, error) {
	var revision SkillRevision
	var enabled int
	var findings string
	if err := scan(
		&revision.Sequence, &revision.ID, &revision.SkillID, &revision.RevisionNo, &revision.Operation, &revision.Actor, &revision.RestoredFromRevisionNo,
		&revision.Name, &revision.Command, &revision.Description, &revision.Prompt, &revision.Source, &revision.Scope, &revision.ProjectID, &revision.WorklineID, &revision.DeletedAt,
		&revision.ContentHash, &enabled, &revision.ScanVerdict, &findings, &revision.ScannerVersion,
		&revision.RiskAcknowledgedAt, &revision.RiskAcknowledgedBy, &revision.RiskAcknowledgedHash,
		&revision.HeadCreatedAt, &revision.HeadUpdatedAt, &revision.CreatedAt,
	); err != nil {
		return SkillRevision{}, err
	}
	if !json.Valid([]byte(findings)) {
		return SkillRevision{}, errors.New("stored skill revision findings are invalid JSON")
	}
	revision.Enabled = enabled != 0
	revision.ScanFindings = json.RawMessage(findings)
	return revision, nil
}

const skillRevisionColumns = `sequence, id, skill_id, revision_no, operation, actor, COALESCE(restored_from_revision_no,0), name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), content_hash, enabled, scan_verdict, scan_findings_json, scanner_version, COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), head_created_at, head_updated_at, created_at`

func (s *skillStore) GetSkillRevision(ctx context.Context, skillID string, revisionNo int64) (SkillRevision, error) {
	return scanSkillRevision(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+skillRevisionColumns+` FROM skill_revisions WHERE skill_id = ? AND revision_no = ?`, skillID, revisionNo).Scan(dest...)
	})
}

func (s *skillStore) getSkillIncludingDeleted(ctx context.Context, id string) (Skill, error) {
	return scanSkill(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version,0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *skillStore) GetSkillIncludingDeleted(ctx context.Context, id string) (Skill, error) {
	return s.getSkillIncludingDeleted(ctx, id)
}

func (s *skillStore) latestSkillRevisionSequence(ctx context.Context) (int64, error) {
	var sequence int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM skill_revisions`).Scan(&sequence); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (s *skillStore) latestSkillRevisionSequenceForSkill(ctx context.Context, skillID string) (int64, error) {
	var sequence int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM skill_revisions WHERE skill_id = ?`, skillID).Scan(&sequence); err != nil {
		return 0, err
	}
	return sequence, nil
}

func validateSkillCursorSnapshot(cursor skillCursor, latestSequence int64) error {
	if cursor.SnapshotSequence > latestSequence {
		return errors.New("skill cursor snapshot is in the future")
	}
	return nil
}

type skillAgentContext struct {
	ProjectID  string
	WorklineID string
}

func (s *skillStore) skillAgentContext(ctx context.Context, agentID string) (skillAgentContext, error) {
	var current skillAgentContext
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(w.project_id,''), COALESCE(a.workline_id,'') FROM agents a LEFT JOIN worklines w ON w.id = a.workline_id WHERE a.id = ?`, agentID).Scan(&current.ProjectID, &current.WorklineID)
	return current, err
}

func (s *skillStore) ValidateEffectiveSkillContext(ctx context.Context, agentID string, target SkillScopeTarget) error {
	target, err := normalizeSkillScopeTarget(target)
	if err != nil {
		return err
	}
	current, err := s.skillAgentContext(ctx, agentID)
	if err != nil {
		return err
	}
	switch target.Scope {
	case SkillScopeGlobal:
		return nil
	case SkillScopeProject:
		if target.ProjectID != current.ProjectID {
			return fmt.Errorf("%w: effective skill project context does not match agent", ErrConflict)
		}
	case SkillScopeWorkspace:
		if target.ProjectID != current.ProjectID || target.WorklineID != current.WorklineID {
			return fmt.Errorf("%w: effective skill workspace context does not match agent", ErrConflict)
		}
	}
	return nil
}

func validateScopeCursor(cursor skillCursor, kind string, target SkillScopeTarget) error {
	if cursor.Kind != kind || cursor.Scope != target.Scope || cursor.ProjectID != target.ProjectID || cursor.WorklineID != target.WorklineID || cursor.SnapshotSequence < 0 {
		return errors.New("skill cursor does not match the requested scope")
	}
	return nil
}

func (s *skillStore) ListSkillsPage(ctx context.Context, target SkillScopeTarget, limit int, cursorValue string) (SkillPage, error) {
	target, err := normalizeSkillScopeTarget(target)
	if err != nil {
		return SkillPage{}, err
	}
	if err := validateSkillScopeTargetContext(ctx, s.db, target); err != nil {
		return SkillPage{}, err
	}
	limit = normalizeSkillPageLimit(limit)
	cursor, err := decodeSkillCursor(cursorValue)
	if err != nil {
		return SkillPage{}, err
	}
	latestSequence, err := s.latestSkillRevisionSequence(ctx)
	if err != nil {
		return SkillPage{}, err
	}
	if cursor.Kind != "" {
		if err := validateScopeCursor(cursor, "scope-skills", target); err != nil {
			return SkillPage{}, err
		}
		if err := validateSkillCursorSnapshot(cursor, latestSequence); err != nil {
			return SkillPage{}, err
		}
	} else {
		cursor = skillCursor{Version: 1, Kind: "scope-skills", Scope: target.Scope, ProjectID: target.ProjectID, WorklineID: target.WorklineID, SnapshotSequence: latestSequence}
	}

	rows, err := s.db.QueryContext(ctx, `WITH latest AS (
		SELECT r.* FROM skill_revisions r
		JOIN (SELECT skill_id, MAX(sequence) AS max_sequence FROM skill_revisions WHERE sequence <= ? GROUP BY skill_id) x
		  ON x.skill_id = r.skill_id AND x.max_sequence = r.sequence
	)
	SELECT skill_id, name, command, description, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), revision_no,
	       content_hash, enabled, scan_verdict, scan_findings_json, scanner_version,
	       COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''),
	       head_created_at, head_updated_at
	FROM latest
	WHERE deleted_at IS NULL AND scope = ? AND COALESCE(project_id,'') = ? AND COALESCE(workline_id,'') = ?
	  AND (? = '' OR (command COLLATE NOCASE > ? COLLATE NOCASE) OR (command = ? COLLATE NOCASE AND skill_id > ?))
	ORDER BY command COLLATE NOCASE ASC, skill_id ASC
	LIMIT ?`, cursor.SnapshotSequence, target.Scope, target.ProjectID, target.WorklineID,
		cursor.AfterCommand, cursor.AfterCommand, cursor.AfterCommand, cursor.AfterSkillID, limit+1)
	if err != nil {
		return SkillPage{}, err
	}
	defer rows.Close()
	items := make([]SkillSummary, 0, limit+1)
	for rows.Next() {
		var item SkillSummary
		var enabled int
		var findings string
		if err := rows.Scan(&item.ID, &item.Name, &item.Command, &item.Description, &item.Source, &item.Scope, &item.ProjectID, &item.WorklineID, &item.RevisionNo,
			&item.ContentHash, &enabled, &item.ScanVerdict, &findings, &item.ScannerVersion,
			&item.RiskAcknowledgedAt, &item.RiskAcknowledgedBy, &item.RiskAcknowledgedHash, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return SkillPage{}, err
		}
		var parsed []json.RawMessage
		if json.Unmarshal([]byte(findings), &parsed) != nil {
			return SkillPage{}, errors.New("stored skill revision findings are invalid JSON")
		}
		item.Enabled = enabled != 0
		item.FindingCount = len(parsed)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SkillPage{}, err
	}
	page := SkillPage{Items: items, SnapshotSequence: cursor.SnapshotSequence}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		cursor.AfterCommand, cursor.AfterSkillID = last.Command, last.ID
		page.NextCursor, err = encodeSkillCursor(cursor)
		if err != nil {
			return SkillPage{}, err
		}
	}
	return page, nil
}

func (s *skillStore) ListSkillRevisionsPage(ctx context.Context, skillID string, limit int, cursorValue string) (SkillRevisionPage, error) {
	limit = normalizeSkillPageLimit(limit)
	cursor, err := decodeSkillCursor(cursorValue)
	if err != nil {
		return SkillRevisionPage{}, err
	}
	latestSequence, err := s.latestSkillRevisionSequenceForSkill(ctx, skillID)
	if err != nil {
		return SkillRevisionPage{}, err
	}
	if cursor.Kind != "" {
		if cursor.Kind != "skill-revisions" || cursor.SkillID != skillID || cursor.SnapshotSequence < 0 {
			return SkillRevisionPage{}, errors.New("skill revision cursor does not match the requested skill")
		}
		if err := validateSkillCursorSnapshot(cursor, latestSequence); err != nil {
			return SkillRevisionPage{}, err
		}
	} else {
		cursor = skillCursor{Version: 1, Kind: "skill-revisions", SkillID: skillID, SnapshotSequence: latestSequence}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, skill_id, revision_no, operation, actor, COALESCE(restored_from_revision_no,0), content_hash, enabled, scan_verdict, CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END, created_at
		FROM skill_revisions
		WHERE skill_id = ? AND sequence <= ? AND (? = 0 OR revision_no < ?)
		ORDER BY revision_no DESC LIMIT ?`, skillID, cursor.SnapshotSequence, cursor.AfterRevisionNo, cursor.AfterRevisionNo, limit+1)
	if err != nil {
		return SkillRevisionPage{}, err
	}
	defer rows.Close()
	items := make([]SkillRevisionSummary, 0, limit+1)
	for rows.Next() {
		var item SkillRevisionSummary
		var enabled, deleted int
		if err := rows.Scan(&item.Sequence, &item.SkillID, &item.RevisionNo, &item.Operation, &item.Actor, &item.RestoredFromRevisionNo, &item.ContentHash, &enabled, &item.ScanVerdict, &deleted, &item.CreatedAt); err != nil {
			return SkillRevisionPage{}, err
		}
		item.Enabled = enabled != 0
		item.Deleted = deleted != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SkillRevisionPage{}, err
	}
	if len(items) == 0 {
		if _, err := s.getSkillIncludingDeleted(ctx, skillID); err != nil {
			return SkillRevisionPage{}, err
		}
	}
	page := SkillRevisionPage{Items: items, SnapshotSequence: cursor.SnapshotSequence}
	if len(items) > limit {
		page.Items = items[:limit]
		cursor.AfterRevisionNo = page.Items[len(page.Items)-1].RevisionNo
		page.NextCursor, err = encodeSkillCursor(cursor)
		if err != nil {
			return SkillRevisionPage{}, err
		}
	}
	return page, nil
}

func (s *skillStore) ResolveSkillByAgentAndCommand(ctx context.Context, agentID, command string) (Skill, error) {
	return scanSkill(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT s.id, s.name, s.command, s.description, s.prompt, s.source, s.scope, COALESCE(s.project_id,''), COALESCE(s.workline_id,''), COALESCE(s.deleted_at,''), COALESCE(s.revision_no,1), s.content_hash, s.enabled, s.scan_verdict, s.scan_findings_json, COALESCE(s.scanner_version,0), COALESCE(s.risk_acknowledged_at,''), COALESCE(s.risk_acknowledged_by,''), COALESCE(s.risk_acknowledged_hash,''), s.created_at, s.updated_at
			FROM agents a
			LEFT JOIN worklines w ON w.id = a.workline_id
			JOIN skills s ON s.deleted_at IS NULL AND s.command = ? COLLATE NOCASE
			WHERE a.id = ? AND (
			  (s.scope = 'workspace' AND s.workline_id = a.workline_id)
			  OR (s.scope = 'project' AND s.project_id = w.project_id)
			  OR s.scope = 'global'
			)
			ORDER BY CASE s.scope WHEN 'workspace' THEN 3 WHEN 'project' THEN 2 ELSE 1 END DESC
			LIMIT 1`, command, agentID).Scan(dest...)
	})
}

func (s *skillStore) ListEffectiveSkillsPage(ctx context.Context, agentID string, limit int, cursorValue string) (SkillPage, error) {
	limit = normalizeSkillPageLimit(limit)
	cursor, err := decodeSkillCursor(cursorValue)
	if err != nil {
		return SkillPage{}, err
	}
	currentContext, err := s.skillAgentContext(ctx, agentID)
	if err != nil {
		return SkillPage{}, err
	}
	latestSequence, err := s.latestSkillRevisionSequence(ctx)
	if err != nil {
		return SkillPage{}, err
	}
	if cursor.Kind != "" {
		if cursor.Kind != "effective-skills" || cursor.AgentID != agentID || cursor.ProjectID != currentContext.ProjectID || cursor.WorklineID != currentContext.WorklineID || cursor.SnapshotSequence < 0 {
			return SkillPage{}, errors.New("effective skill cursor does not match the requested agent")
		}
		if err := validateSkillCursorSnapshot(cursor, latestSequence); err != nil {
			return SkillPage{}, err
		}
	} else {
		cursor = skillCursor{Version: 1, Kind: "effective-skills", AgentID: agentID, ProjectID: currentContext.ProjectID, WorklineID: currentContext.WorklineID, SnapshotSequence: latestSequence}
	}
	rows, err := s.db.QueryContext(ctx, `WITH latest AS (
		SELECT r.* FROM skill_revisions r
		JOIN (SELECT skill_id, MAX(sequence) AS max_sequence FROM skill_revisions WHERE sequence <= ? GROUP BY skill_id) x
		  ON x.skill_id = r.skill_id AND x.max_sequence = r.sequence
	), agent_context AS (
		SELECT a.workline_id, w.project_id FROM agents a LEFT JOIN worklines w ON w.id = a.workline_id WHERE a.id = ?
	), ranked AS (
		SELECT latest.*, ROW_NUMBER() OVER (PARTITION BY LOWER(latest.command) ORDER BY CASE latest.scope WHEN 'workspace' THEN 3 WHEN 'project' THEN 2 ELSE 1 END DESC, latest.skill_id ASC) AS owner_rank
		FROM latest, agent_context
		WHERE latest.deleted_at IS NULL AND (
		  (latest.scope = 'workspace' AND latest.workline_id = agent_context.workline_id)
		  OR (latest.scope = 'project' AND latest.project_id = agent_context.project_id)
		  OR latest.scope = 'global'
		)
	)
	SELECT skill_id, name, command, description, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), revision_no,
	       content_hash, enabled, scan_verdict, scan_findings_json, scanner_version,
	       COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), head_created_at, head_updated_at
	FROM ranked
	WHERE owner_rank = 1 AND (? = '' OR (command COLLATE NOCASE > ? COLLATE NOCASE) OR (command = ? COLLATE NOCASE AND skill_id > ?))
	ORDER BY command COLLATE NOCASE ASC, skill_id ASC LIMIT ?`, cursor.SnapshotSequence, agentID,
		cursor.AfterCommand, cursor.AfterCommand, cursor.AfterCommand, cursor.AfterSkillID, limit+1)
	if err != nil {
		return SkillPage{}, err
	}
	defer rows.Close()
	items := make([]SkillSummary, 0, limit+1)
	for rows.Next() {
		var item SkillSummary
		var enabled int
		var findings string
		if err := rows.Scan(&item.ID, &item.Name, &item.Command, &item.Description, &item.Source, &item.Scope, &item.ProjectID, &item.WorklineID, &item.RevisionNo,
			&item.ContentHash, &enabled, &item.ScanVerdict, &findings, &item.ScannerVersion,
			&item.RiskAcknowledgedAt, &item.RiskAcknowledgedBy, &item.RiskAcknowledgedHash, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return SkillPage{}, err
		}
		var parsed []json.RawMessage
		if json.Unmarshal([]byte(findings), &parsed) != nil {
			return SkillPage{}, errors.New("stored skill revision findings are invalid JSON")
		}
		item.Enabled = enabled != 0
		item.FindingCount = len(parsed)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SkillPage{}, err
	}
	page := SkillPage{Items: items, SnapshotSequence: cursor.SnapshotSequence}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		cursor.AfterCommand, cursor.AfterSkillID = last.Command, last.ID
		page.NextCursor, err = encodeSkillCursor(cursor)
		if err != nil {
			return SkillPage{}, err
		}
	}
	return page, nil
}

func (s *skillStore) DeleteSkillCAS(ctx context.Context, id, expectedUpdatedAt, actor string) (Skill, error) {
	expectedUpdatedAt = strings.TrimSpace(expectedUpdatedAt)
	if expectedUpdatedAt == "" {
		return Skill{}, errors.New("expected skill updated_at is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Skill{}, err
	}
	defer tx.Rollback()
	current, err := scanSkill(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version,0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE id = ?`, id).Scan(dest...)
	})
	if err != nil {
		return Skill{}, err
	}
	if current.DeletedAt != "" {
		return Skill{}, sql.ErrNoRows
	}
	deleted := current
	deleted.Enabled = false
	deleted.RiskAcknowledgedAt, deleted.RiskAcknowledgedBy, deleted.RiskAcknowledgedHash = "", "", ""
	deleted.DeletedAt = Now()
	deleted.UpdatedAt = nextSkillUpdatedAt(current.UpdatedAt)
	deleted.RevisionNo = current.RevisionNo + 1
	result, err := tx.ExecContext(ctx, `UPDATE skills SET enabled = 0, risk_acknowledged_at = NULL, risk_acknowledged_by = NULL, risk_acknowledged_hash = NULL, deleted_at = ?, revision_no = ?, updated_at = ? WHERE id = ? AND updated_at = ? AND deleted_at IS NULL`, deleted.DeletedAt, deleted.RevisionNo, deleted.UpdatedAt, id, expectedUpdatedAt)
	if err != nil {
		return Skill{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Skill{}, err
	} else if affected != 1 {
		return Skill{}, skillUpdateConflict(ctx, tx, id)
	}
	if _, err := insertSkillRevision(ctx, tx, deleted, "delete", actor, 0); err != nil {
		return Skill{}, err
	}
	if err := insertSkillAuditEvent(ctx, tx, skillAuditEvent("delete", deleted, actor)); err != nil {
		return Skill{}, err
	}
	if err := tx.Commit(); err != nil {
		return Skill{}, err
	}
	return deleted, nil
}

func (s *skillStore) RestoreSkillAs(ctx context.Context, id string, revisionNo int64, expectedUpdatedAt string, acknowledgeRisk bool, acknowledgedContentHash, actor string) (Skill, error) {
	expectedUpdatedAt = strings.TrimSpace(expectedUpdatedAt)
	if expectedUpdatedAt == "" {
		return Skill{}, errors.New("expected skill updated_at is required")
	}
	acknowledgedContentHash = strings.TrimSpace(acknowledgedContentHash)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Skill{}, err
	}
	defer tx.Rollback()
	current, err := scanSkill(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version,0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE id = ?`, id).Scan(dest...)
	})
	if err != nil {
		return Skill{}, err
	}
	if current.UpdatedAt != expectedUpdatedAt {
		return Skill{}, fmt.Errorf("%w: skill was updated by another client", ErrConflict)
	}
	revision, err := scanSkillRevision(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+skillRevisionColumns+` FROM skill_revisions WHERE skill_id = ? AND revision_no = ?`, id, revisionNo).Scan(dest...)
	})
	if err != nil {
		return Skill{}, err
	}
	if revision.DeletedAt != "" {
		return Skill{}, errors.New("cannot restore a deleted revision")
	}
	candidate := Skill{
		ID: id, Name: revision.Name, Command: revision.Command, Description: revision.Description, Prompt: revision.Prompt,
		Source: revision.Source, Scope: revision.Scope, ProjectID: revision.ProjectID, WorklineID: revision.WorklineID,
		Enabled: revision.Enabled, CreatedAt: current.CreatedAt,
	}
	candidate.Enabled = false
	candidate, err = canonicalSkillRecord(candidate)
	if err != nil {
		return Skill{}, err
	}
	if revision.Enabled && candidate.ScanVerdict == skills.VerdictSafe {
		candidate.Enabled = true
	}
	if revision.Enabled && candidate.ScanVerdict == skills.VerdictReview {
		if !acknowledgeRisk || acknowledgedContentHash != candidate.ContentHash {
			return Skill{}, &SkillRestoreReviewRequiredError{
				ScanVerdict:    candidate.ScanVerdict,
				ScanFindings:   append(json.RawMessage(nil), candidate.ScanFindings...),
				ContentHash:    candidate.ContentHash,
				ScannerVersion: candidate.ScannerVersion,
			}
		}
		candidate.Enabled = true
		candidate.RiskAcknowledgedAt = Now()
		candidate.RiskAcknowledgedBy = normalizeSkillAuditActor(actor)
		candidate.RiskAcknowledgedHash = candidate.ContentHash
		candidate, err = canonicalSkillRecord(candidate)
		if err != nil {
			return Skill{}, err
		}
	}
	if candidate.ScanVerdict == skills.VerdictBlocked {
		candidate.Enabled = false
		candidate.RiskAcknowledgedAt, candidate.RiskAcknowledgedBy, candidate.RiskAcknowledgedHash = "", "", ""
	}
	candidate.RevisionNo = current.RevisionNo + 1
	candidate.CreatedAt = current.CreatedAt
	candidate.UpdatedAt = nextSkillUpdatedAt(current.UpdatedAt)
	if err := validateSkillScopeTx(ctx, tx, candidate); err != nil {
		return Skill{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE skills SET name = ?, command = ?, description = ?, prompt = ?, source = ?, scope = ?, project_id = NULLIF(?, ''), workline_id = NULLIF(?, ''), deleted_at = NULL, revision_no = ?, content_hash = ?, enabled = ?, scan_verdict = ?, scan_findings_json = ?, scanner_version = ?, risk_acknowledged_at = NULLIF(?, ''), risk_acknowledged_by = NULLIF(?, ''), risk_acknowledged_hash = NULLIF(?, ''), updated_at = ? WHERE id = ? AND updated_at = ?`,
		candidate.Name, candidate.Command, candidate.Description, candidate.Prompt, candidate.Source, candidate.Scope, candidate.ProjectID, candidate.WorklineID, candidate.RevisionNo,
		candidate.ContentHash, boolInt(candidate.Enabled), candidate.ScanVerdict, string(candidate.ScanFindings), candidate.ScannerVersion,
		candidate.RiskAcknowledgedAt, candidate.RiskAcknowledgedBy, candidate.RiskAcknowledgedHash, candidate.UpdatedAt, id, expectedUpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return Skill{}, fmt.Errorf("%w: skill command already exists in the target scope", ErrConflict)
		}
		return Skill{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Skill{}, err
	} else if affected != 1 {
		return Skill{}, skillUpdateConflict(ctx, tx, id)
	}
	if _, err := insertSkillRevision(ctx, tx, candidate, "restore", actor, revisionNo); err != nil {
		return Skill{}, err
	}
	if err := insertSkillAuditEvent(ctx, tx, skillAuditEvent("restore", candidate, actor)); err != nil {
		return Skill{}, err
	}
	if err := tx.Commit(); err != nil {
		return Skill{}, err
	}
	return candidate, nil
}
