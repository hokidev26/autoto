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

	"autoto/internal/skills"
)

// revalidateSkills closes the upgrade gap between scanner releases. A metadata-
// only pass selects stale or internally inconsistent rows, then only those full
// templates are normalized and scanned. A changed risky result loses its enabled
// state and acknowledgement, requiring an explicit new confirmation.
func (s *skillStore) revalidateSkills(ctx context.Context) error {
	stored, err := s.listSkillsForRevalidation(ctx)
	if err != nil {
		return fmt.Errorf("list skills for revalidation: %w", err)
	}
	if len(stored) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin skill revalidation: %w", err)
	}
	defer tx.Rollback()

	for _, skill := range stored {
		// Derive metadata with state disabled first: a historical safe record may
		// now scan as review/blocked and therefore lacks a valid acknowledgement.
		candidate := skill
		candidate.Enabled = false
		candidate.RiskAcknowledgedAt = ""
		candidate.RiskAcknowledgedBy = ""
		candidate.RiskAcknowledgedHash = ""
		canonical, err := canonicalSkillRecord(candidate)
		if err != nil {
			if err := failClosedSkillRevalidation(ctx, tx, skill, "Stored content can no longer be normalized safely."); err != nil {
				return err
			}
			continue
		}
		metadataConsistent := canonical.Name == skill.Name &&
			canonical.Command == skill.Command &&
			canonical.Description == skill.Description &&
			canonical.Prompt == skill.Prompt &&
			canonical.ContentHash == skill.ContentHash &&
			canonical.ScanVerdict == skill.ScanVerdict &&
			string(canonical.ScanFindings) == string(skill.ScanFindings)
		canonical.ScannerVersion = skills.ScannerVersion
		keepEnabled := skill.Enabled && metadataConsistent && (canonical.ScanVerdict == skills.VerdictSafe ||
			(canonical.ScanVerdict == skills.VerdictReview && validSkillRiskAcknowledgement(skill)))
		canonical.Enabled = keepEnabled
		if keepEnabled && canonical.ScanVerdict == skills.VerdictReview {
			canonical.RiskAcknowledgedAt = strings.TrimSpace(skill.RiskAcknowledgedAt)
			canonical.RiskAcknowledgedBy = strings.TrimSpace(skill.RiskAcknowledgedBy)
			canonical.RiskAcknowledgedHash = strings.TrimSpace(skill.RiskAcknowledgedHash)
		} else {
			canonical.RiskAcknowledgedAt = ""
			canonical.RiskAcknowledgedBy = ""
			canonical.RiskAcknowledgedHash = ""
		}
		stateChanged := canonical.Enabled != skill.Enabled ||
			canonical.RiskAcknowledgedAt != skill.RiskAcknowledgedAt ||
			canonical.RiskAcknowledgedBy != skill.RiskAcknowledgedBy ||
			canonical.RiskAcknowledgedHash != skill.RiskAcknowledgedHash ||
			canonical.ScannerVersion != skill.ScannerVersion
		if metadataConsistent && !stateChanged {
			continue
		}
		canonical.UpdatedAt = nextSkillUpdatedAt(skill.UpdatedAt)
		canonical.RevisionNo = skill.RevisionNo + 1
		result, err := tx.ExecContext(ctx, `UPDATE skills SET name = ?, command = ?, description = ?, prompt = ?, source = ?, content_hash = ?, enabled = ?, scan_verdict = ?, scan_findings_json = ?, scanner_version = ?, risk_acknowledged_at = NULLIF(?, ''), risk_acknowledged_by = NULLIF(?, ''), risk_acknowledged_hash = NULLIF(?, ''), revision_no = ?, updated_at = ? WHERE id = ? AND updated_at = ? AND deleted_at IS NULL`, canonical.Name, canonical.Command, canonical.Description, canonical.Prompt, canonical.Source, canonical.ContentHash, boolInt(canonical.Enabled), canonical.ScanVerdict, string(canonical.ScanFindings), canonical.ScannerVersion, canonical.RiskAcknowledgedAt, canonical.RiskAcknowledgedBy, canonical.RiskAcknowledgedHash, canonical.RevisionNo, canonical.UpdatedAt, canonical.ID, skill.UpdatedAt)
		if err != nil {
			return fmt.Errorf("update revalidated skill %s: %w", skill.ID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count revalidated skill %s: %w", skill.ID, err)
		}
		if affected == 0 {
			continue
		}
		if _, err := insertSkillRevision(ctx, tx, canonical, "revalidate", "scanner_revalidation", 0); err != nil {
			return fmt.Errorf("revision revalidated skill %s: %w", skill.ID, err)
		}
		if err := insertSkillAuditEvents(ctx, tx, canonical, &skill, "scanner_revalidation"); err != nil {
			return fmt.Errorf("audit revalidated skill %s: %w", skill.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit skill revalidation: %w", err)
	}
	return nil
}

// listSkillsForRevalidation deliberately permits invalid historical findings so
// one corrupt row can be disabled instead of preventing the service from opening.
func (s *skillStore) listSkillsForRevalidation(ctx context.Context) ([]Skill, error) {
	// The first pass deliberately excludes prompt and other full content. Store
	// writes keep content, hash, findings, and scanner_version atomic, so current,
	// internally consistent metadata can be trusted without rereading large prompts.
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, command, description, source, content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version, 0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,'') FROM skills WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	candidateIDs := make([]string, 0)
	for rows.Next() {
		var candidate Skill
		var enabled int
		var findings string
		if err := rows.Scan(&candidate.ID, &candidate.Name, &candidate.Command, &candidate.Description, &candidate.Source, &candidate.ContentHash, &enabled, &candidate.ScanVerdict, &findings, &candidate.ScannerVersion, &candidate.RiskAcknowledgedAt, &candidate.RiskAcknowledgedBy, &candidate.RiskAcknowledgedHash); err != nil {
			rows.Close()
			return nil, err
		}
		candidate.Enabled = enabled != 0
		candidate.ScanFindings = json.RawMessage(findings)
		if skillNeedsRevalidation(candidate) {
			candidateIDs = append(candidateIDs, candidate.ID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	items := make([]Skill, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		skill, err := scanSkillForRevalidation(func(dest ...any) error {
			return s.db.QueryRowContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version, 0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE id = ?`, id).Scan(dest...)
		})
		if err != nil {
			return nil, err
		}
		items = append(items, skill)
	}
	return items, nil
}

func scanSkillForRevalidation(scan skillScanner) (Skill, error) {
	var skill Skill
	var enabled int
	var findings string
	if err := scan(&skill.ID, &skill.Name, &skill.Command, &skill.Description, &skill.Prompt, &skill.Source, &skill.Scope, &skill.ProjectID, &skill.WorklineID, &skill.DeletedAt, &skill.RevisionNo, &skill.ContentHash, &enabled, &skill.ScanVerdict, &findings, &skill.ScannerVersion, &skill.RiskAcknowledgedAt, &skill.RiskAcknowledgedBy, &skill.RiskAcknowledgedHash, &skill.CreatedAt, &skill.UpdatedAt); err != nil {
		return Skill{}, err
	}
	skill.Enabled = enabled != 0
	skill.ScanFindings = json.RawMessage(findings)
	return skill, nil
}

func skillNeedsRevalidation(skill Skill) bool {
	if !validSkillName(skill.Name) || !validSkillCommand(skill.Command) || !validSkillDescription(skill.Description) || !validSkillSource(skill.Source) {
		return true
	}
	if skill.ScannerVersion != skills.ScannerVersion || len(skill.ContentHash) != 64 || !isLowerHex(skill.ContentHash) || !validSkillVerdict(skill.ScanVerdict) {
		return true
	}
	findingsVerdict, ok := storedSkillFindingsVerdict(skill.ScanFindings)
	if !ok || findingsVerdict != skill.ScanVerdict {
		return true
	}
	if skill.ScanVerdict == skills.VerdictBlocked && skill.Enabled {
		return true
	}
	hasAcknowledgement := strings.TrimSpace(skill.RiskAcknowledgedAt) != "" || strings.TrimSpace(skill.RiskAcknowledgedBy) != "" || strings.TrimSpace(skill.RiskAcknowledgedHash) != ""
	if skill.Enabled && skill.ScanVerdict == skills.VerdictReview {
		return !validSkillRiskAcknowledgement(skill)
	}
	return hasAcknowledgement
}

func failClosedSkillRevalidation(ctx context.Context, tx *sql.Tx, skill Skill, message string) error {
	findings, err := json.Marshal([]skills.Finding{{Code: "invalid_stored_skill", Severity: skills.VerdictBlocked, Message: message}})
	if err != nil {
		return err
	}
	updatedAt := nextSkillUpdatedAt(skill.UpdatedAt)
	revisionNo := skill.RevisionNo + 1
	result, err := tx.ExecContext(ctx, `UPDATE skills SET enabled = 0, scan_verdict = ?, scan_findings_json = ?, scanner_version = ?, risk_acknowledged_at = NULL, risk_acknowledged_by = NULL, risk_acknowledged_hash = NULL, revision_no = ?, updated_at = ? WHERE id = ? AND updated_at = ? AND deleted_at IS NULL`, skills.VerdictBlocked, string(findings), skills.ScannerVersion, revisionNo, updatedAt, skill.ID, skill.UpdatedAt)
	if err != nil {
		return fmt.Errorf("fail close revalidated skill %s: %w", skill.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count fail-closed skill %s: %w", skill.ID, err)
	}
	if affected == 0 {
		return nil
	}
	canonical := skill
	canonical.Enabled = false
	canonical.ScanVerdict = skills.VerdictBlocked
	canonical.ScanFindings = findings
	canonical.ScannerVersion = skills.ScannerVersion
	canonical.RiskAcknowledgedAt, canonical.RiskAcknowledgedBy, canonical.RiskAcknowledgedHash = "", "", ""
	canonical.UpdatedAt = updatedAt
	canonical.RevisionNo = revisionNo
	if _, err := insertSkillRevision(ctx, tx, canonical, "revalidate", "scanner_revalidation", 0); err != nil {
		return err
	}
	return insertSkillAuditEvents(ctx, tx, canonical, &skill, "scanner_revalidation")
}

func storedSkillFindingsVerdict(raw json.RawMessage) (string, bool) {
	var findings []skills.Finding
	encoded := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(encoded, "[") || !json.Valid(raw) || json.Unmarshal(raw, &findings) != nil {
		return "", false
	}
	verdict := skills.VerdictSafe
	for _, finding := range findings {
		if strings.TrimSpace(finding.Code) == "" || strings.TrimSpace(finding.Message) == "" {
			return "", false
		}
		switch finding.Severity {
		case skills.VerdictReview:
			if verdict == skills.VerdictSafe {
				verdict = skills.VerdictReview
			}
		case skills.VerdictBlocked:
			verdict = skills.VerdictBlocked
		default:
			return "", false
		}
	}
	return verdict, true
}

func (s *skillStore) ListSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version, 0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE deleted_at IS NULL AND scope = 'global' ORDER BY enabled DESC, command COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Skill, 0)
	for rows.Next() {
		skill, err := scanSkill(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, skill)
	}
	return items, rows.Err()
}

func (s *skillStore) ListSkillSummaries(ctx context.Context) ([]SkillSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, command, description, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version, 0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE deleted_at IS NULL AND scope = 'global' ORDER BY enabled DESC, command COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SkillSummary, 0)
	for rows.Next() {
		var item SkillSummary
		var enabled int
		var findings string
		if err := rows.Scan(&item.ID, &item.Name, &item.Command, &item.Description, &item.Source, &item.Scope, &item.ProjectID, &item.WorklineID, &item.RevisionNo, &item.ContentHash, &enabled, &item.ScanVerdict, &findings, &item.ScannerVersion, &item.RiskAcknowledgedAt, &item.RiskAcknowledgedBy, &item.RiskAcknowledgedHash, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		var codes []json.RawMessage
		if json.Unmarshal([]byte(findings), &codes) != nil {
			return nil, errors.New("stored skill scan findings are not valid JSON")
		}
		item.FindingCount = len(codes)
		item.Enabled = enabled != 0
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *skillStore) GetSkill(ctx context.Context, id string) (Skill, error) {
	return scanSkill(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version, 0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE id = ? AND deleted_at IS NULL`, id).Scan(dest...)
	})
}

// GetSkillByCommand returns the complete server skill for a slash command.
// Command matching follows the database's case-insensitive uniqueness rule.
func (s *skillStore) GetSkillByCommand(ctx context.Context, command string) (Skill, error) {
	return scanSkill(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version, 0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE command = ? COLLATE NOCASE AND deleted_at IS NULL AND scope = 'global'`, command).Scan(dest...)
	})
}

func (s *skillStore) CreateSkill(ctx context.Context, skill Skill) (Skill, error) {
	return s.CreateSkillAs(ctx, skill, "system")
}

func (s *skillStore) CreateSkillAs(ctx context.Context, skill Skill, actor string) (Skill, error) {
	canonical, err := canonicalSkillRecord(skill)
	if err != nil {
		return Skill{}, err
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	now := Now()
	canonical.CreatedAt, canonical.UpdatedAt = now, now
	canonical.RevisionNo = 1
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Skill{}, err
	}
	defer tx.Rollback()
	if err := validateSkillScopeTx(ctx, tx, canonical); err != nil {
		return Skill{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO skills (id, name, command, description, prompt, source, scope, project_id, workline_id, deleted_at, revision_no, content_hash, enabled, scan_verdict, scan_findings_json, scanner_version, risk_acknowledged_at, risk_acknowledged_by, risk_acknowledged_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULL, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)`, canonical.ID, canonical.Name, canonical.Command, canonical.Description, canonical.Prompt, canonical.Source, canonical.Scope, canonical.ProjectID, canonical.WorklineID, canonical.RevisionNo, canonical.ContentHash, boolInt(canonical.Enabled), canonical.ScanVerdict, string(canonical.ScanFindings), canonical.ScannerVersion, canonical.RiskAcknowledgedAt, canonical.RiskAcknowledgedBy, canonical.RiskAcknowledgedHash, canonical.CreatedAt, canonical.UpdatedAt); err != nil {
		if isUniqueConstraint(err) {
			return Skill{}, fmt.Errorf("%w: skill command already exists", ErrConflict)
		}
		return Skill{}, err
	}
	if _, err := insertSkillRevision(ctx, tx, canonical, "create", actor, 0); err != nil {
		return Skill{}, err
	}
	if err := insertSkillAuditEvents(ctx, tx, canonical, nil, actor); err != nil {
		return Skill{}, err
	}
	if err := tx.Commit(); err != nil {
		return Skill{}, err
	}
	return canonical, nil
}

func (s *skillStore) UpdateSkill(ctx context.Context, skill Skill) (Skill, error) {
	return s.UpdateSkillAs(ctx, skill, "system")
}

func (s *skillStore) UpdateSkillAs(ctx context.Context, skill Skill, actor string) (Skill, error) {
	if strings.TrimSpace(skill.UpdatedAt) == "" {
		return Skill{}, errors.New("expected skill updated_at is required")
	}
	canonical, err := canonicalSkillRecord(skill)
	if err != nil {
		return Skill{}, err
	}
	expectedUpdatedAt := strings.TrimSpace(skill.UpdatedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Skill{}, err
	}
	defer tx.Rollback()
	previous, err := scanSkill(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT id, name, command, description, prompt, source, scope, COALESCE(project_id,''), COALESCE(workline_id,''), COALESCE(deleted_at,''), COALESCE(revision_no,1), content_hash, enabled, scan_verdict, scan_findings_json, COALESCE(scanner_version, 0), COALESCE(risk_acknowledged_at,''), COALESCE(risk_acknowledged_by,''), COALESCE(risk_acknowledged_hash,''), created_at, updated_at FROM skills WHERE id = ?`, canonical.ID).Scan(dest...)
	})
	if err != nil {
		return Skill{}, err
	}
	if previous.DeletedAt != "" {
		return Skill{}, sql.ErrNoRows
	}
	if strings.TrimSpace(skill.Scope) == "" {
		canonical.Scope, canonical.ProjectID, canonical.WorklineID = previous.Scope, previous.ProjectID, previous.WorklineID
	}
	canonical.CreatedAt, canonical.UpdatedAt = previous.CreatedAt, nextSkillUpdatedAt(previous.UpdatedAt)
	canonical.RevisionNo = previous.RevisionNo + 1
	if err := validateSkillScopeTx(ctx, tx, canonical); err != nil {
		return Skill{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE skills SET name = ?, command = ?, description = ?, prompt = ?, source = ?, scope = ?, project_id = NULLIF(?, ''), workline_id = NULLIF(?, ''), revision_no = ?, content_hash = ?, enabled = ?, scan_verdict = ?, scan_findings_json = ?, scanner_version = ?, risk_acknowledged_at = NULLIF(?, ''), risk_acknowledged_by = NULLIF(?, ''), risk_acknowledged_hash = NULLIF(?, ''), updated_at = ? WHERE id = ? AND updated_at = ? AND deleted_at IS NULL`, canonical.Name, canonical.Command, canonical.Description, canonical.Prompt, canonical.Source, canonical.Scope, canonical.ProjectID, canonical.WorklineID, canonical.RevisionNo, canonical.ContentHash, boolInt(canonical.Enabled), canonical.ScanVerdict, string(canonical.ScanFindings), canonical.ScannerVersion, canonical.RiskAcknowledgedAt, canonical.RiskAcknowledgedBy, canonical.RiskAcknowledgedHash, canonical.UpdatedAt, canonical.ID, expectedUpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return Skill{}, fmt.Errorf("%w: skill command already exists", ErrConflict)
		}
		return Skill{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Skill{}, err
	}
	if affected != 1 {
		return Skill{}, skillUpdateConflict(ctx, tx, canonical.ID)
	}
	if _, err := insertSkillRevision(ctx, tx, canonical, "update", actor, 0); err != nil {
		return Skill{}, err
	}
	if err := insertSkillAuditEvents(ctx, tx, canonical, &previous, actor); err != nil {
		return Skill{}, err
	}
	if err := tx.Commit(); err != nil {
		return Skill{}, err
	}
	return canonical, nil
}

func (s *skillStore) DeleteSkill(ctx context.Context, id string) error {
	return s.DeleteSkillAs(ctx, id, "system")
}

func (s *skillStore) DeleteSkillAs(ctx context.Context, id, actor string) error {
	skill, err := s.GetSkill(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.DeleteSkillCAS(ctx, id, skill.UpdatedAt, actor)
	return err
}

func (s *skillStore) ListSkillAuditEvents(ctx context.Context, skillID string, limit int) ([]SkillAuditEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, action, actor, skill_id, content_hash, scan_verdict, finding_codes_json, COALESCE(risk_acknowledged_at,''), created_at FROM skill_audit_events WHERE skill_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, skillID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]SkillAuditEvent, 0)
	for rows.Next() {
		var event SkillAuditEvent
		var codes string
		if err := rows.Scan(&event.ID, &event.Action, &event.Actor, &event.SkillID, &event.ContentHash, &event.ScanVerdict, &codes, &event.RiskAcknowledgedAt, &event.CreatedAt); err != nil {
			return nil, err
		}
		if !json.Valid([]byte(codes)) {
			return nil, errors.New("stored skill audit finding codes are not valid JSON")
		}
		event.FindingCodes = json.RawMessage(codes)
		events = append(events, event)
	}
	return events, rows.Err()
}

func nextSkillUpdatedAt(previous string) string {
	now := time.Now().UTC()
	if prior, err := time.Parse(time.RFC3339Nano, previous); err == nil && !now.After(prior) {
		now = prior.Add(time.Nanosecond)
	}
	return now.Format(time.RFC3339Nano)
}

func skillUpdateConflict(ctx context.Context, tx *sql.Tx, id string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM skills WHERE id = ?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	return fmt.Errorf("%w: skill was updated by another client", ErrConflict)
}

func insertSkillAuditEvents(ctx context.Context, tx *sql.Tx, current Skill, previous *Skill, actor string) error {
	if previous == nil {
		if err := insertSkillAuditEvent(ctx, tx, skillAuditEvent("create", current, actor)); err != nil {
			return err
		}
		if current.Enabled {
			return insertSkillAuditEvent(ctx, tx, skillAuditEvent("enable", current, actor))
		}
		return nil
	}
	contentChanged := current.Name != previous.Name || current.Command != previous.Command || current.Description != previous.Description || current.Prompt != previous.Prompt || current.ContentHash != previous.ContentHash
	metadataChanged := current.ScanVerdict != previous.ScanVerdict || string(current.ScanFindings) != string(previous.ScanFindings) || current.RiskAcknowledgedAt != previous.RiskAcknowledgedAt || current.RiskAcknowledgedBy != previous.RiskAcknowledgedBy || current.RiskAcknowledgedHash != previous.RiskAcknowledgedHash
	if contentChanged || metadataChanged {
		if err := insertSkillAuditEvent(ctx, tx, skillAuditEvent("update", current, actor)); err != nil {
			return err
		}
	}
	if current.Enabled != previous.Enabled {
		action := "disable"
		if current.Enabled {
			action = "enable"
		}
		if err := insertSkillAuditEvent(ctx, tx, skillAuditEvent(action, current, actor)); err != nil {
			return err
		}
	}
	return nil
}

func skillAuditEvent(action string, skill Skill, actor string) SkillAuditEvent {
	return SkillAuditEvent{Action: action, Actor: normalizeSkillAuditActor(actor), SkillID: skill.ID, ContentHash: skill.ContentHash, ScanVerdict: skill.ScanVerdict, FindingCodes: skillFindingCodes(skill.ScanFindings), RiskAcknowledgedAt: skill.RiskAcknowledgedAt, CreatedAt: Now()}
}

func insertSkillAuditEvent(ctx context.Context, tx *sql.Tx, event SkillAuditEvent) error {
	if event.ID == "" {
		event.ID = NewID()
	}
	if event.CreatedAt == "" {
		event.CreatedAt = Now()
	}
	if !json.Valid(event.FindingCodes) {
		return errors.New("invalid skill audit finding codes")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO skill_audit_events (id, action, actor, skill_id, content_hash, scan_verdict, finding_codes_json, risk_acknowledged_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)`, event.ID, event.Action, event.Actor, event.SkillID, event.ContentHash, event.ScanVerdict, string(event.FindingCodes), event.RiskAcknowledgedAt, event.CreatedAt)
	return err
}

func normalizeSkillAuditActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "system"
	}
	return actor
}

func skillFindingCodes(findings json.RawMessage) json.RawMessage {
	var parsed []skills.Finding
	if json.Unmarshal(findings, &parsed) != nil {
		return json.RawMessage("[]")
	}
	codes := make([]string, 0, len(parsed))
	for _, finding := range parsed {
		if strings.TrimSpace(finding.Code) != "" {
			codes = append(codes, finding.Code)
		}
	}
	encoded, err := json.Marshal(codes)
	if err != nil {
		return json.RawMessage("[]")
	}
	return encoded
}

type skillScanner func(dest ...any) error

func scanSkill(scan skillScanner) (Skill, error) {
	var skill Skill
	var enabled int
	var findings string
	if err := scan(&skill.ID, &skill.Name, &skill.Command, &skill.Description, &skill.Prompt, &skill.Source, &skill.Scope, &skill.ProjectID, &skill.WorklineID, &skill.DeletedAt, &skill.RevisionNo, &skill.ContentHash, &enabled, &skill.ScanVerdict, &findings, &skill.ScannerVersion, &skill.RiskAcknowledgedAt, &skill.RiskAcknowledgedBy, &skill.RiskAcknowledgedHash, &skill.CreatedAt, &skill.UpdatedAt); err != nil {
		return Skill{}, err
	}
	if !json.Valid([]byte(findings)) {
		return Skill{}, errors.New("stored skill scan findings are not valid JSON")
	}
	skill.ScanFindings = json.RawMessage(findings)
	skill.Enabled = enabled != 0
	return skill, nil
}

// canonicalSkillRecord makes the persistent store the trust boundary for skill
// metadata. Callers may supply only editable content and state; scanner fields are
// always regenerated from the normalized content before state validation.
func canonicalSkillRecord(skill Skill) (Skill, error) {
	parsed, err := skills.Normalize(skills.Skill{
		Name:        skill.Name,
		Command:     skill.Command,
		Description: skill.Description,
		Prompt:      skill.Prompt,
	})
	if err != nil {
		return Skill{}, err
	}
	result := skills.Scan(parsed)
	findings, err := json.Marshal(result.Findings)
	if err != nil {
		return Skill{}, fmt.Errorf("encode skill scan findings: %w", err)
	}
	skill.Name = parsed.Name
	skill.Command = parsed.Command
	skill.Description = parsed.Description
	skill.Prompt = parsed.Prompt
	skill.ContentHash = result.Hash
	skill.ScanVerdict = result.Verdict
	skill.Scope = strings.TrimSpace(skill.Scope)
	if skill.Scope == "" {
		skill.Scope = "global"
	}
	skill.ProjectID = strings.TrimSpace(skill.ProjectID)
	skill.WorklineID = strings.TrimSpace(skill.WorklineID)
	skill.ScanFindings = findings
	skill.ScannerVersion = skills.ScannerVersion
	skill.RiskAcknowledgedAt = strings.TrimSpace(skill.RiskAcknowledgedAt)
	skill.RiskAcknowledgedBy = strings.TrimSpace(skill.RiskAcknowledgedBy)
	skill.RiskAcknowledgedHash = strings.TrimSpace(skill.RiskAcknowledgedHash)
	if !skill.Enabled || skill.ScanVerdict != skills.VerdictReview {
		// An acknowledgement authorizes only one enabled review state for the
		// exact scanned content hash. Safe, blocked, and disabled records carry none.
		skill.RiskAcknowledgedAt = ""
		skill.RiskAcknowledgedBy = ""
		skill.RiskAcknowledgedHash = ""
	}
	if err := validateSkill(skill); err != nil {
		return Skill{}, err
	}
	return skill, nil
}

func validSkillRiskAcknowledgement(skill Skill) bool {
	acknowledgedAt := strings.TrimSpace(skill.RiskAcknowledgedAt)
	acknowledgedBy := strings.TrimSpace(skill.RiskAcknowledgedBy)
	acknowledgedHash := strings.TrimSpace(skill.RiskAcknowledgedHash)
	if acknowledgedAt == "" || acknowledgedBy == "" || len(acknowledgedBy) > 200 || acknowledgedHash != skill.ContentHash {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, acknowledgedAt)
	return err == nil
}

func validateSkill(skill Skill) error {
	if !validSkillName(skill.Name) {
		return errors.New("invalid skill name")
	}
	if !validSkillCommand(skill.Command) {
		return errors.New("invalid skill command")
	}
	if !validSkillDescription(skill.Description) {
		return errors.New("invalid skill description")
	}
	if strings.TrimSpace(skill.Prompt) == "" || len(skill.Prompt) > 128*1024 || !utf8.ValidString(skill.Prompt) || strings.ContainsRune(skill.Prompt, 0) {
		return errors.New("invalid skill prompt")
	}
	if !validSkillSource(skill.Source) {
		return errors.New("invalid skill source")
	}
	if !validSkillScopeTarget(skill.Scope, skill.ProjectID, skill.WorklineID) {
		return errors.New("invalid skill scope target")
	}
	if len(skill.ContentHash) != 64 || !isLowerHex(skill.ContentHash) {
		return errors.New("invalid skill content hash")
	}
	if !validSkillVerdict(skill.ScanVerdict) {
		return errors.New("invalid skill scan verdict")
	}
	findingsVerdict, findingsValid := storedSkillFindingsVerdict(skill.ScanFindings)
	if !findingsValid || findingsVerdict != skill.ScanVerdict {
		return errors.New("invalid skill scan findings")
	}
	if skill.ScanVerdict == "blocked" && skill.Enabled {
		return errors.New("blocked skills cannot be enabled")
	}
	if skill.ScanVerdict == skills.VerdictReview && skill.Enabled && !validSkillRiskAcknowledgement(skill) {
		return errors.New("review skills require a valid risk acknowledgement for the current content before enabling")
	}
	return nil
}

func validSkillName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 120 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

func validSkillDescription(description string) bool {
	return strings.TrimSpace(description) != "" && len(description) <= 500 && utf8.ValidString(description) && !strings.ContainsRune(description, 0)
}

func validSkillCommand(command string) bool {
	if len(command) < 2 || len(command) > 64 || command[0] != '/' || strings.ToLower(command) != command {
		return false
	}
	for i := 1; i < len(command); i++ {
		char := command[i]
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_') {
			return false
		}
		if i == 1 && (char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func validSkillScopeTarget(scope, projectID, worklineID string) bool {
	switch scope {
	case "global":
		return projectID == "" && worklineID == ""
	case "project":
		return projectID != "" && worklineID == ""
	case "workspace":
		return projectID != "" && worklineID != ""
	default:
		return false
	}
}

func validSkillSource(source string) bool {
	switch source {
	case "manual", "local_migration", "skill_md":
		return true
	default:
		return false
	}
}

func validSkillVerdict(verdict string) bool {
	switch verdict {
	case "safe", "review", "blocked":
		return true
	default:
		return false
	}
}
