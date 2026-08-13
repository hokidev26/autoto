package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/review"
)

const (
	PlanStatusDraft     = "draft"
	PlanStatusInReview  = "in_review"
	PlanStatusApproved  = "approved"
	PlanStatusRejected  = "rejected"
	PlanStatusStale     = "stale"
	PlanStatusExecuting = "executing"
	PlanStatusExecuted  = "executed"
	PlanStatusCancelled = "cancelled"

	PlanReviewDecisionComment          = "comment"
	PlanReviewDecisionApproved         = "approved"
	PlanReviewDecisionChangesRequested = "changes_requested"

	PlanApprovalDecisionApproved = "approved"
	PlanApprovalDecisionRejected = "rejected"
)

type Plan struct {
	ID                       string          `json:"id"`
	AgentID                  string          `json:"agentId"`
	SourceRunID              string          `json:"sourceRunId,omitempty"`
	Status                   string          `json:"status"`
	Revision                 int64           `json:"revision"`
	ContentJSON              json.RawMessage `json:"contentJson"`
	Summary                  string          `json:"summary,omitempty"`
	PolicyGenerationSnapshot int64           `json:"policyGenerationSnapshot"`
	AgentGenerationSnapshot  int64           `json:"agentGenerationSnapshot"`
	ToolCatalogDigest        string          `json:"toolCatalogDigest,omitempty"`
	WorkspaceFingerprint     string          `json:"workspaceFingerprint,omitempty"`
	StaleReason              string          `json:"staleReason,omitempty"`
	CreatedAt                string          `json:"createdAt"`
	UpdatedAt                string          `json:"updatedAt"`
}

type PlanReview struct {
	ID           string `json:"id"`
	PlanID       string `json:"planId"`
	PlanRevision int64  `json:"planRevision"`
	ReviewerID   string `json:"reviewerId"`
	Decision     string `json:"decision"`
	Comment      string `json:"comment,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type PlanApproval struct {
	ID           string `json:"id"`
	PlanID       string `json:"planId"`
	PlanRevision int64  `json:"planRevision"`
	ApproverID   string `json:"approverId"`
	Decision     string `json:"decision"`
	Comment      string `json:"comment,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type PlanSnapshot struct {
	PolicyGenerationSnapshot int64  `json:"policyGenerationSnapshot"`
	AgentGenerationSnapshot  int64  `json:"agentGenerationSnapshot"`
	ToolCatalogDigest        string `json:"toolCatalogDigest,omitempty"`
	WorkspaceFingerprint     string `json:"workspaceFingerprint,omitempty"`
}

type PlanDetail struct {
	Plan      Plan           `json:"plan"`
	Reviews   []PlanReview   `json:"reviews"`
	Approvals []PlanApproval `json:"approvals"`
	Runs      []Run          `json:"runs"`
}

const planColumns = `id, agent_id, COALESCE(source_run_id,''), status, revision, content_json, summary, policy_generation_snapshot, agent_generation_snapshot, tool_catalog_digest, workspace_fingerprint, COALESCE(stale_reason,''), created_at, updated_at`

type planScanner func(dest ...any) error

func scanPlan(scan planScanner) (Plan, error) {
	var plan Plan
	var content string
	if err := scan(
		&plan.ID, &plan.AgentID, &plan.SourceRunID, &plan.Status, &plan.Revision, &content, &plan.Summary,
		&plan.PolicyGenerationSnapshot, &plan.AgentGenerationSnapshot, &plan.ToolCatalogDigest, &plan.WorkspaceFingerprint,
		&plan.StaleReason, &plan.CreatedAt, &plan.UpdatedAt,
	); err != nil {
		return Plan{}, err
	}
	if !json.Valid([]byte(content)) {
		return Plan{}, errors.New("stored plan content is invalid JSON")
	}
	plan.ContentJSON = json.RawMessage(content)
	return plan, nil
}

func scanPlanReview(scan func(dest ...any) error) (PlanReview, error) {
	var review PlanReview
	err := scan(&review.ID, &review.PlanID, &review.PlanRevision, &review.ReviewerID, &review.Decision, &review.Comment, &review.CreatedAt)
	return review, err
}

func scanPlanApproval(scan func(dest ...any) error) (PlanApproval, error) {
	var approval PlanApproval
	err := scan(&approval.ID, &approval.PlanID, &approval.PlanRevision, &approval.ApproverID, &approval.Decision, &approval.Comment, &approval.CreatedAt)
	return approval, err
}

func validPlanStatus(status string) bool {
	switch status {
	case PlanStatusDraft, PlanStatusInReview, PlanStatusApproved, PlanStatusRejected, PlanStatusStale, PlanStatusExecuting, PlanStatusExecuted, PlanStatusCancelled:
		return true
	default:
		return false
	}
}

func validPlanReviewDecision(decision string) bool {
	switch decision {
	case PlanReviewDecisionComment, PlanReviewDecisionApproved, PlanReviewDecisionChangesRequested:
		return true
	default:
		return false
	}
}

func validPlanApprovalDecision(decision string) bool {
	return decision == PlanApprovalDecisionApproved || decision == PlanApprovalDecisionRejected
}

func normalizePlanContent(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(value) > 262144 || !json.Valid(value) {
		return nil, errors.New("invalid plan content JSON")
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, errors.New("invalid plan content JSON")
	}
	switch decoded.(type) {
	case map[string]any, []any:
	default:
		return nil, errors.New("plan content JSON must be an object or array")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(canonical), nil
}

func canonicalPlan(plan Plan, create bool) (Plan, error) {
	plan.ID = strings.TrimSpace(plan.ID)
	plan.AgentID = strings.TrimSpace(plan.AgentID)
	plan.SourceRunID = strings.TrimSpace(plan.SourceRunID)
	plan.Status = strings.TrimSpace(plan.Status)
	plan.Summary = strings.TrimSpace(plan.Summary)
	plan.ToolCatalogDigest = strings.TrimSpace(plan.ToolCatalogDigest)
	plan.WorkspaceFingerprint = strings.TrimSpace(plan.WorkspaceFingerprint)
	plan.StaleReason = strings.TrimSpace(plan.StaleReason)
	if plan.Status == "" {
		plan.Status = PlanStatusDraft
	}
	if !validPlanStatus(plan.Status) {
		return Plan{}, errors.New("invalid plan status")
	}
	if !create && plan.Status != PlanStatusDraft {
		return Plan{}, errors.New("plan content updates must reset to draft")
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
	}{
		{"plan id", plan.ID, 128, create && plan.ID != ""},
		{"plan agent id", plan.AgentID, 128, true},
		{"plan source run id", plan.SourceRunID, 128, false},
		{"plan summary", plan.Summary, 4096, false},
		{"plan tool catalog digest", plan.ToolCatalogDigest, 512, false},
		{"plan workspace fingerprint", plan.WorkspaceFingerprint, 512, false},
		{"plan stale reason", plan.StaleReason, 4096, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, false); err != nil {
			return Plan{}, err
		}
	}
	if plan.PolicyGenerationSnapshot < 0 || plan.AgentGenerationSnapshot < 0 {
		return Plan{}, errors.New("plan generation snapshots must not be negative")
	}
	content, err := normalizePlanContent(plan.ContentJSON)
	if err != nil {
		return Plan{}, err
	}
	plan.ContentJSON = content
	return plan, nil
}

func fillPlanSnapshotsTx(ctx context.Context, tx *sql.Tx, plan *Plan) error {
	var agentGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(entity_generation,1) FROM agents WHERE id = ?`, plan.AgentID).Scan(&agentGeneration); err != nil {
		return err
	}
	if plan.AgentGenerationSnapshot == 0 {
		plan.AgentGenerationSnapshot = agentGeneration
	}
	if plan.PolicyGenerationSnapshot == 0 {
		plan.PolicyGenerationSnapshot = 1
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(policy_generation,1) FROM workflow_preferences WHERE id = 'default'`).Scan(&plan.PolicyGenerationSnapshot); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (s *planStore) CreatePlan(ctx context.Context, plan Plan) (Plan, error) {
	canonical, err := canonicalPlan(plan, true)
	if err != nil {
		return Plan{}, err
	}
	if canonical.Status != PlanStatusDraft {
		return Plan{}, errors.New("new plans must start as draft")
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	now := Now()
	if canonical.CreatedAt == "" {
		canonical.CreatedAt = now
	}
	if canonical.UpdatedAt == "" {
		canonical.UpdatedAt = canonical.CreatedAt
	}
	if canonical.CreatedAt, err = canonicalP2P3Time("plan created_at", canonical.CreatedAt, true); err != nil {
		return Plan{}, err
	}
	if canonical.UpdatedAt, err = canonicalP2P3Time("plan updated_at", canonical.UpdatedAt, true); err != nil {
		return Plan{}, err
	}
	canonical.Revision = 1

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	if canonical.SourceRunID != "" {
		var sourceAgentID string
		if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM runs WHERE id = ?`, canonical.SourceRunID).Scan(&sourceAgentID); err != nil {
			return Plan{}, err
		}
		if sourceAgentID != canonical.AgentID {
			return Plan{}, fmt.Errorf("%w: plan source run belongs to another agent", ErrConflict)
		}
	}
	if err := fillPlanSnapshotsTx(ctx, tx, &canonical); err != nil {
		return Plan{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plans (id, agent_id, source_run_id, status, revision, content_json, summary, policy_generation_snapshot, agent_generation_snapshot, tool_catalog_digest, workspace_fingerprint, stale_reason, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
		canonical.ID, canonical.AgentID, canonical.SourceRunID, canonical.Status, canonical.Revision, string(canonical.ContentJSON), canonical.Summary,
		canonical.PolicyGenerationSnapshot, canonical.AgentGenerationSnapshot, canonical.ToolCatalogDigest, canonical.WorkspaceFingerprint,
		canonical.StaleReason, canonical.CreatedAt, canonical.UpdatedAt,
	); err != nil {
		if isUniqueConstraint(err) {
			return Plan{}, fmt.Errorf("%w: plan already exists", ErrConflict)
		}
		return Plan{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, err
	}
	return canonical, nil
}

func (s *planStore) GetPlan(ctx context.Context, agentID, planID string) (Plan, error) {
	agentID = strings.TrimSpace(agentID)
	planID = strings.TrimSpace(planID)
	if agentID == "" || planID == "" {
		return Plan{}, sql.ErrNoRows
	}
	return scanPlan(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE agent_id = ? AND id = ?`, agentID, planID).Scan(dest...)
	})
}

func (s *planStore) GetPlanByID(ctx context.Context, planID string) (Plan, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return Plan{}, sql.ErrNoRows
	}
	return scanPlan(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE id = ?`, planID).Scan(dest...)
	})
}

func (s *planStore) GetPlanBySourceRun(ctx context.Context, runID string) (Plan, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Plan{}, sql.ErrNoRows
	}
	return scanPlan(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE source_run_id = ?`, runID).Scan(dest...)
	})
}

func (s *planStore) GetLatestPlan(ctx context.Context, agentID string) (Plan, error) {
	return scanPlan(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE agent_id = ? ORDER BY updated_at DESC, id DESC LIMIT 1`, strings.TrimSpace(agentID)).Scan(dest...)
	})
}

func (s *planStore) ListPlans(ctx context.Context, agentID string, limit int) ([]Plan, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+planColumns+` FROM plans WHERE agent_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, strings.TrimSpace(agentID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := make([]Plan, 0)
	for rows.Next() {
		plan, err := scanPlan(rows.Scan)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (s *planStore) UpdatePlan(ctx context.Context, plan Plan, expectedRevision int64) (Plan, error) {
	if expectedRevision < 1 {
		return Plan{}, errors.New("plan expected revision must be positive")
	}
	canonical, err := canonicalPlan(plan, false)
	if err != nil {
		return Plan{}, err
	}
	canonical.ID = strings.TrimSpace(plan.ID)
	if err := validateP2P3Text("plan id", canonical.ID, 128, true, false); err != nil {
		return Plan{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	current, err := scanPlan(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE agent_id = ? AND id = ?`, canonical.AgentID, canonical.ID).Scan(dest...)
	})
	if err != nil {
		return Plan{}, err
	}
	if current.Revision != expectedRevision {
		return Plan{}, fmt.Errorf("%w: plan revision changed", ErrConflict)
	}
	if current.Status == PlanStatusExecuting || current.Status == PlanStatusExecuted || current.Status == PlanStatusCancelled {
		return Plan{}, fmt.Errorf("%w: plan cannot be edited from %s", ErrConflict, current.Status)
	}
	if err := fillPlanSnapshotsTx(ctx, tx, &canonical); err != nil {
		return Plan{}, err
	}
	canonical.Status = PlanStatusDraft
	canonical.StaleReason = ""
	canonical.Revision = current.Revision + 1
	canonical.CreatedAt = current.CreatedAt
	canonical.UpdatedAt = nextP2P3UpdatedAt(current.UpdatedAt)
	result, err := tx.ExecContext(ctx, `UPDATE plans SET status = ?, revision = ?, content_json = ?, summary = ?, policy_generation_snapshot = ?, agent_generation_snapshot = ?, tool_catalog_digest = ?, workspace_fingerprint = ?, stale_reason = NULL, updated_at = ? WHERE id = ? AND agent_id = ? AND revision = ?`,
		canonical.Status, canonical.Revision, string(canonical.ContentJSON), canonical.Summary,
		canonical.PolicyGenerationSnapshot, canonical.AgentGenerationSnapshot, canonical.ToolCatalogDigest, canonical.WorkspaceFingerprint,
		canonical.UpdatedAt, canonical.ID, canonical.AgentID, expectedRevision,
	)
	if err != nil {
		return Plan{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Plan{}, err
	} else if affected != 1 {
		return Plan{}, fmt.Errorf("%w: plan revision changed", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, err
	}
	return canonical, nil
}

// UpdatePlanCAS is an explicit alias for callers that prefer CAS terminology.
func (s *planStore) UpdatePlanCAS(ctx context.Context, plan Plan, expectedRevision int64) (Plan, error) {
	return s.UpdatePlan(ctx, plan, expectedRevision)
}

// PersistPlanDraft implements review.PlanStore. It binds a structured plan-mode
// result to exactly one originating Run, so a later execute Run can reference a
// reviewed and approved durable plan rather than model text alone.
func (s *planStore) PersistPlanDraft(ctx context.Context, runID string, draft review.PlanDraft) error {
	runID = strings.TrimSpace(runID)
	if err := validateP2P3Text("plan source run id", runID, 128, true, false); err != nil {
		return err
	}
	content, err := json.Marshal(draft)
	if err != nil {
		return err
	}
	content, err = normalizePlanContent(content)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var agentID, executionMode, toolCatalogDigest, workspaceFingerprint string
	var policyGeneration, agentGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT agent_id, COALESCE(execution_mode,'execute'), COALESCE(policy_generation_snapshot,0), COALESCE(agent_generation_snapshot,0), COALESCE(tool_catalog_digest,''), COALESCE(workspace_fingerprint,'') FROM runs WHERE id = ?`, runID).Scan(&agentID, &executionMode, &policyGeneration, &agentGeneration, &toolCatalogDigest, &workspaceFingerprint); err != nil {
		return err
	}
	if executionMode != RunExecutionModePlan {
		return fmt.Errorf("%w: only plan-mode runs can persist plan drafts", ErrConflict)
	}
	now := Now()
	var current Plan
	current, err = scanPlan(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE source_run_id = ?`, runID).Scan(dest...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		plan := Plan{
			ID:                       NewID(),
			AgentID:                  agentID,
			SourceRunID:              runID,
			Status:                   PlanStatusDraft,
			Revision:                 1,
			ContentJSON:              content,
			Summary:                  strings.TrimSpace(draft.Goal),
			PolicyGenerationSnapshot: policyGeneration,
			AgentGenerationSnapshot:  agentGeneration,
			ToolCatalogDigest:        toolCatalogDigest,
			WorkspaceFingerprint:     workspaceFingerprint,
			CreatedAt:                now,
			UpdatedAt:                now,
		}
		if err := fillPlanSnapshotsTx(ctx, tx, &plan); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO plans (id, agent_id, source_run_id, status, revision, content_json, summary, policy_generation_snapshot, agent_generation_snapshot, tool_catalog_digest, workspace_fingerprint, stale_reason, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
			plan.ID, plan.AgentID, plan.SourceRunID, plan.Status, plan.Revision, string(plan.ContentJSON), plan.Summary,
			plan.PolicyGenerationSnapshot, plan.AgentGenerationSnapshot, plan.ToolCatalogDigest, plan.WorkspaceFingerprint, plan.CreatedAt, plan.UpdatedAt,
		)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if current.Status != PlanStatusDraft {
			return fmt.Errorf("%w: plan draft is already under review", ErrConflict)
		}
		updatedAt := nextP2P3UpdatedAt(current.UpdatedAt)
		result, err := tx.ExecContext(ctx, `UPDATE plans SET revision = revision + 1, content_json = ?, summary = ?, policy_generation_snapshot = ?, agent_generation_snapshot = ?, tool_catalog_digest = ?, workspace_fingerprint = ?, stale_reason = NULL, updated_at = ? WHERE id = ? AND source_run_id = ? AND status = ?`,
			string(content), strings.TrimSpace(draft.Goal), policyGeneration, agentGeneration, toolCatalogDigest, workspaceFingerprint, updatedAt, current.ID, runID, PlanStatusDraft,
		)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return err
		} else if affected != 1 {
			return fmt.Errorf("%w: plan draft changed", ErrConflict)
		}
	}
	return tx.Commit()
}

// TriggerPlanReview advances a persisted draft into the review-only state.
// Repeated calls are idempotent while the plan remains in review.
func (s *planStore) TriggerPlanReview(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if err := validateP2P3Text("plan source run id", runID, 128, true, false); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plan, err := scanPlan(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE source_run_id = ?`, runID).Scan(dest...)
	})
	if err != nil {
		return err
	}
	if plan.Status == PlanStatusInReview {
		return tx.Commit()
	}
	if plan.Status != PlanStatusDraft {
		return fmt.Errorf("%w: plan cannot enter review from %s", ErrConflict, plan.Status)
	}
	result, err := tx.ExecContext(ctx, `UPDATE plans SET status = ?, updated_at = ? WHERE id = ? AND status = ?`, PlanStatusInReview, nextP2P3UpdatedAt(plan.UpdatedAt), plan.ID, PlanStatusDraft)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("%w: plan status changed", ErrConflict)
	}
	return tx.Commit()
}

// PersistPlanReview records an isolated reviewer verdict with the durable
// reviewer identity, but deliberately does not approve execution. A distinct
// PlanApproval remains required.
func (s *planStore) PersistPlanReview(ctx context.Context, runID, reviewerID string, result review.Result) error {
	plan, err := s.GetPlanBySourceRun(ctx, runID)
	if err != nil {
		return err
	}
	decision := PlanReviewDecisionComment
	switch result.Verdict {
	case review.VerdictPass:
		decision = PlanReviewDecisionApproved
	case review.VerdictNeedsHuman, review.VerdictBlockRecommended:
		decision = PlanReviewDecisionChangesRequested
	}
	_, err = s.CreatePlanReview(ctx, PlanReview{
		PlanID:       plan.ID,
		PlanRevision: plan.Revision,
		ReviewerID:   strings.TrimSpace(reviewerID),
		Decision:     decision,
		Comment:      strings.TrimSpace(result.Reason),
	})
	return err
}

func canTransitionPlanStatus(from, to string) bool {
	switch from {
	case PlanStatusDraft:
		return to == PlanStatusInReview || to == PlanStatusStale || to == PlanStatusCancelled
	case PlanStatusInReview:
		return to == PlanStatusStale || to == PlanStatusCancelled
	case PlanStatusApproved:
		return to == PlanStatusExecuting || to == PlanStatusStale || to == PlanStatusCancelled
	case PlanStatusRejected:
		return to == PlanStatusDraft || to == PlanStatusCancelled
	case PlanStatusStale:
		return to == PlanStatusDraft || to == PlanStatusCancelled
	case PlanStatusExecuting:
		return to == PlanStatusExecuted || to == PlanStatusApproved || to == PlanStatusStale
	default:
		return false
	}
}

func (s *planStore) TransitionPlanStatus(ctx context.Context, agentID, planID string, expectedRevision int64, status string) (Plan, error) {
	if expectedRevision < 1 {
		return Plan{}, errors.New("plan expected revision must be positive")
	}
	status = strings.TrimSpace(status)
	if !validPlanStatus(status) || status == PlanStatusStale {
		return Plan{}, errors.New("invalid plan status transition")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	current, err := scanPlan(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE agent_id = ? AND id = ?`, strings.TrimSpace(agentID), strings.TrimSpace(planID)).Scan(dest...)
	})
	if err != nil {
		return Plan{}, err
	}
	previousStatus := current.Status
	if current.Revision != expectedRevision || !canTransitionPlanStatus(previousStatus, status) {
		return Plan{}, fmt.Errorf("%w: plan cannot transition from %s to %s", ErrConflict, previousStatus, status)
	}
	current.Status = status
	current.StaleReason = ""
	current.UpdatedAt = nextP2P3UpdatedAt(current.UpdatedAt)
	result, err := tx.ExecContext(ctx, `UPDATE plans SET status = ?, stale_reason = NULL, updated_at = ? WHERE id = ? AND agent_id = ? AND revision = ? AND status = ?`, current.Status, current.UpdatedAt, current.ID, current.AgentID, expectedRevision, previousStatus)
	if err != nil {
		return Plan{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Plan{}, err
	} else if affected != 1 {
		return Plan{}, fmt.Errorf("%w: plan status changed", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, err
	}
	return current, nil
}

func (s *planStore) CreatePlanReview(ctx context.Context, review PlanReview) (PlanReview, error) {
	review.ID = strings.TrimSpace(review.ID)
	review.PlanID = strings.TrimSpace(review.PlanID)
	review.ReviewerID = strings.TrimSpace(review.ReviewerID)
	review.Decision = strings.TrimSpace(review.Decision)
	review.Comment = strings.TrimSpace(review.Comment)
	if review.ID == "" {
		review.ID = NewID()
	}
	if review.Decision == "" {
		review.Decision = PlanReviewDecisionComment
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
	}{
		{"plan review id", review.ID, 128, true},
		{"plan review plan id", review.PlanID, 128, true},
		{"plan reviewer id", review.ReviewerID, 200, true},
		{"plan review comment", review.Comment, 16384, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, false); err != nil {
			return PlanReview{}, err
		}
	}
	if review.PlanRevision < 1 || !validPlanReviewDecision(review.Decision) {
		return PlanReview{}, errors.New("invalid plan review")
	}
	if review.CreatedAt == "" {
		review.CreatedAt = Now()
	}
	var err error
	if review.CreatedAt, err = canonicalP2P3Time("plan review created_at", review.CreatedAt, true); err != nil {
		return PlanReview{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanReview{}, err
	}
	defer tx.Rollback()
	var revision int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT revision, status FROM plans WHERE id = ?`, review.PlanID).Scan(&revision, &status); err != nil {
		return PlanReview{}, err
	}
	if revision != review.PlanRevision || status != PlanStatusInReview {
		return PlanReview{}, fmt.Errorf("%w: plan is not available for review", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_reviews (id, plan_id, plan_revision, reviewer_id, decision, comment, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, review.ID, review.PlanID, review.PlanRevision, review.ReviewerID, review.Decision, review.Comment, review.CreatedAt); err != nil {
		if isUniqueConstraint(err) {
			return PlanReview{}, fmt.Errorf("%w: plan review already exists", ErrConflict)
		}
		return PlanReview{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlanReview{}, err
	}
	return review, nil
}

func (s *planStore) ListPlanReviews(ctx context.Context, agentID, planID string) ([]PlanReview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.plan_id, r.plan_revision, r.reviewer_id, r.decision, r.comment, r.created_at FROM plan_reviews r JOIN plans p ON p.id = r.plan_id WHERE p.agent_id = ? AND r.plan_id = ? ORDER BY r.created_at ASC, r.id ASC`, strings.TrimSpace(agentID), strings.TrimSpace(planID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reviews := make([]PlanReview, 0)
	for rows.Next() {
		review, err := scanPlanReview(rows.Scan)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, review)
	}
	return reviews, rows.Err()
}

func (s *planStore) CreatePlanApproval(ctx context.Context, approval PlanApproval) (PlanApproval, error) {
	approval.ID = strings.TrimSpace(approval.ID)
	approval.PlanID = strings.TrimSpace(approval.PlanID)
	approval.ApproverID = strings.TrimSpace(approval.ApproverID)
	approval.Decision = strings.TrimSpace(approval.Decision)
	approval.Comment = strings.TrimSpace(approval.Comment)
	if approval.ID == "" {
		approval.ID = NewID()
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
	}{
		{"plan approval id", approval.ID, 128, true},
		{"plan approval plan id", approval.PlanID, 128, true},
		{"plan approver id", approval.ApproverID, 200, true},
		{"plan approval comment", approval.Comment, 16384, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, false); err != nil {
			return PlanApproval{}, err
		}
	}
	if approval.PlanRevision < 1 || !validPlanApprovalDecision(approval.Decision) {
		return PlanApproval{}, errors.New("invalid plan approval")
	}
	if approval.CreatedAt == "" {
		approval.CreatedAt = Now()
	}
	var err error
	if approval.CreatedAt, err = canonicalP2P3Time("plan approval created_at", approval.CreatedAt, true); err != nil {
		return PlanApproval{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanApproval{}, err
	}
	defer tx.Rollback()
	var revision int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT revision, status FROM plans WHERE id = ?`, approval.PlanID).Scan(&revision, &status); err != nil {
		return PlanApproval{}, err
	}
	if revision != approval.PlanRevision || status != PlanStatusInReview {
		return PlanApproval{}, fmt.Errorf("%w: plan is not available for approval", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_approvals (id, plan_id, plan_revision, approver_id, decision, comment, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, approval.ID, approval.PlanID, approval.PlanRevision, approval.ApproverID, approval.Decision, approval.Comment, approval.CreatedAt); err != nil {
		if isUniqueConstraint(err) {
			return PlanApproval{}, fmt.Errorf("%w: approver already decided this plan revision", ErrConflict)
		}
		return PlanApproval{}, err
	}
	status = PlanStatusApproved
	if approval.Decision == PlanApprovalDecisionRejected {
		status = PlanStatusRejected
	}
	result, err := tx.ExecContext(ctx, `UPDATE plans SET status = ?, stale_reason = NULL, updated_at = ? WHERE id = ? AND revision = ? AND status = ?`, status, nextP2P3UpdatedAt(approval.CreatedAt), approval.PlanID, approval.PlanRevision, PlanStatusInReview)
	if err != nil {
		return PlanApproval{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return PlanApproval{}, err
	} else if affected != 1 {
		return PlanApproval{}, fmt.Errorf("%w: plan status changed", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return PlanApproval{}, err
	}
	return approval, nil
}

func (s *planStore) ListPlanApprovals(ctx context.Context, agentID, planID string) ([]PlanApproval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.plan_id, a.plan_revision, a.approver_id, a.decision, a.comment, a.created_at FROM plan_approvals a JOIN plans p ON p.id = a.plan_id WHERE p.agent_id = ? AND a.plan_id = ? ORDER BY a.created_at ASC, a.id ASC`, strings.TrimSpace(agentID), strings.TrimSpace(planID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	approvals := make([]PlanApproval, 0)
	for rows.Next() {
		approval, err := scanPlanApproval(rows.Scan)
		if err != nil {
			return nil, err
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

func (s *planStore) MarkPlanStale(ctx context.Context, agentID, planID string, expectedRevision int64, reason string) (Plan, error) {
	if expectedRevision < 1 {
		return Plan{}, errors.New("plan expected revision must be positive")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "plan inputs changed"
	}
	if err := validateP2P3Text("plan stale reason", reason, 4096, true, false); err != nil {
		return Plan{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	current, err := scanPlan(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE agent_id = ? AND id = ?`, strings.TrimSpace(agentID), strings.TrimSpace(planID)).Scan(dest...)
	})
	if err != nil {
		return Plan{}, err
	}
	if current.Revision != expectedRevision || current.Status == PlanStatusExecuting || current.Status == PlanStatusExecuted || current.Status == PlanStatusCancelled {
		return Plan{}, fmt.Errorf("%w: plan cannot be marked stale", ErrConflict)
	}
	current.Status = PlanStatusStale
	current.StaleReason = reason
	current.UpdatedAt = nextP2P3UpdatedAt(current.UpdatedAt)
	result, err := tx.ExecContext(ctx, `UPDATE plans SET status = ?, stale_reason = ?, updated_at = ? WHERE id = ? AND agent_id = ? AND revision = ?`, current.Status, current.StaleReason, current.UpdatedAt, current.ID, current.AgentID, expectedRevision)
	if err != nil {
		return Plan{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Plan{}, err
	} else if affected != 1 {
		return Plan{}, fmt.Errorf("%w: plan revision changed", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, err
	}
	return current, nil
}

func (s *planStore) MarkPlansStale(ctx context.Context, agentID string, snapshot PlanSnapshot, reason string) (int64, error) {
	agentID = strings.TrimSpace(agentID)
	if err := validateP2P3Text("plan agent id", agentID, 128, true, false); err != nil {
		return 0, err
	}
	if snapshot.PolicyGenerationSnapshot < 0 || snapshot.AgentGenerationSnapshot < 0 {
		return 0, errors.New("plan generation snapshots must not be negative")
	}
	snapshot.ToolCatalogDigest = strings.TrimSpace(snapshot.ToolCatalogDigest)
	snapshot.WorkspaceFingerprint = strings.TrimSpace(snapshot.WorkspaceFingerprint)
	if err := validateP2P3Text("plan tool catalog digest", snapshot.ToolCatalogDigest, 512, false, false); err != nil {
		return 0, err
	}
	if err := validateP2P3Text("plan workspace fingerprint", snapshot.WorkspaceFingerprint, 512, false, false); err != nil {
		return 0, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "plan inputs changed"
	}
	if err := validateP2P3Text("plan stale reason", reason, 4096, true, false); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE plans SET status = ?, stale_reason = ?, updated_at = ? WHERE agent_id = ? AND status IN (?, ?, ?) AND (policy_generation_snapshot <> ? OR agent_generation_snapshot <> ? OR tool_catalog_digest <> ? OR workspace_fingerprint <> ?)`,
		PlanStatusStale, reason, Now(), agentID, PlanStatusDraft, PlanStatusInReview, PlanStatusApproved,
		snapshot.PolicyGenerationSnapshot, snapshot.AgentGenerationSnapshot, snapshot.ToolCatalogDigest, snapshot.WorkspaceFingerprint,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *planStore) CreateRunForPlan(ctx context.Context, planID string, run Run) (Run, error) {
	plan, err := s.GetPlanByID(ctx, planID)
	if err != nil {
		return Run{}, err
	}
	if run.AgentID != "" && strings.TrimSpace(run.AgentID) != plan.AgentID {
		return Run{}, fmt.Errorf("%w: plan belongs to another agent", ErrConflict)
	}
	run.AgentID = plan.AgentID
	run.PlanID = plan.ID
	run.ExecutionMode = RunExecutionModeExecute
	if run.PolicyGenerationSnapshot == 0 {
		run.PolicyGenerationSnapshot = plan.PolicyGenerationSnapshot
	}
	if run.AgentGenerationSnapshot == 0 {
		run.AgentGenerationSnapshot = plan.AgentGenerationSnapshot
	}
	if run.ToolCatalogDigest == "" {
		run.ToolCatalogDigest = plan.ToolCatalogDigest
	}
	if run.WorkspaceFingerprint == "" {
		run.WorkspaceFingerprint = plan.WorkspaceFingerprint
	}
	return s.CreateRun(ctx, run)
}

func (s *planStore) ListPlanRuns(ctx context.Context, agentID, planID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, runSelectSQL+` WHERE agent_id = ? AND plan_id = ? ORDER BY execution_generation DESC, id DESC LIMIT ?`, strings.TrimSpace(agentID), strings.TrimSpace(planID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *planStore) ListRunsByPlan(ctx context.Context, agentID, planID string, limit int) ([]Run, error) {
	return s.ListPlanRuns(ctx, agentID, planID, limit)
}

func (s *planStore) GetPlanForRun(ctx context.Context, agentID, runID string) (Plan, error) {
	return scanPlan(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE id = (SELECT plan_id FROM runs WHERE agent_id = ? AND id = ?)`, strings.TrimSpace(agentID), strings.TrimSpace(runID)).Scan(dest...)
	})
}

func (s *planStore) GetPlanDetail(ctx context.Context, agentID, planID string) (PlanDetail, error) {
	plan, err := s.GetPlan(ctx, agentID, planID)
	if err != nil {
		return PlanDetail{}, err
	}
	reviews, err := s.ListPlanReviews(ctx, agentID, planID)
	if err != nil {
		return PlanDetail{}, err
	}
	approvals, err := s.ListPlanApprovals(ctx, agentID, planID)
	if err != nil {
		return PlanDetail{}, err
	}
	runs, err := s.ListPlanRuns(ctx, agentID, planID, 100)
	if err != nil {
		return PlanDetail{}, err
	}
	return PlanDetail{Plan: plan, Reviews: reviews, Approvals: approvals, Runs: runs}, nil
}
