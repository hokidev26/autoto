package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/agent"
	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/gitsnapshot"
	reviewpkg "autoto/internal/review"
)

const (
	maxReviewPlanSummaryBytes = 4096
	maxReviewPlanContentBytes = 256 << 10

	reviewWorkspaceFingerprintMaxPaths       = 256
	reviewWorkspaceFingerprintMaxFileBytes   = 1 << 20
	reviewWorkspaceFingerprintMaxTotalBytes  = 4 << 20
	reviewWorkspaceFingerprintStatusMaxBytes = 512 << 10
)

var (
	errPlanStale                    = errors.New("plan is stale")
	errPlanRunnerIntegrationMissing = errors.New("runner plan execution integration is unavailable")
)

type createReviewPlanRequest struct {
	Summary string          `json:"summary"`
	Content json.RawMessage `json:"content"`
}

type reviewPlanMutationRequest struct {
	Revision int64  `json:"revision"`
	Comment  string `json:"comment,omitempty"`
}

type reviewPlanTestSummary struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

type reviewPlanSummary struct {
	ID             string                  `json:"id"`
	AgentID        string                  `json:"agentId"`
	Status         string                  `json:"status"`
	Revision       int64                   `json:"revision"`
	Summary        string                  `json:"summary,omitempty"`
	Goal           string                  `json:"goal,omitempty"`
	Steps          []string                `json:"steps"`
	Risks          []string                `json:"risks"`
	Tests          []reviewPlanTestSummary `json:"tests"`
	ReviewVerdict  string                  `json:"reviewVerdict,omitempty"`
	ReviewFindings []string                `json:"reviewFindings"`
	StaleReason    string                  `json:"staleReason,omitempty"`
	CreatedAt      string                  `json:"createdAt"`
	UpdatedAt      string                  `json:"updatedAt"`
}

func declaredReviewPlanTests(tests []string) []reviewPlanTestSummary {
	out := make([]reviewPlanTestSummary, 0, len(tests))
	for _, test := range tests {
		if text := strings.TrimSpace(test); text != "" {
			out = append(out, reviewPlanTestSummary{Text: text, Status: "declared"})
		}
	}
	return out
}

func summarizeReviewPlan(plan db.Plan) reviewPlanSummary {
	summary := reviewPlanSummary{
		ID: plan.ID, AgentID: plan.AgentID, Status: plan.Status, Revision: plan.Revision,
		Summary: plan.Summary, StaleReason: plan.StaleReason, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt,
		Steps: []string{}, Risks: []string{}, Tests: []reviewPlanTestSummary{}, ReviewFindings: []string{},
	}
	var draft reviewpkg.PlanDraft
	if json.Unmarshal(plan.ContentJSON, &draft) == nil {
		summary.Goal = draft.Goal
		summary.Steps = append(summary.Steps, draft.Steps...)
		summary.Risks = append(summary.Risks, draft.Risks...)
		summary.Tests = declaredReviewPlanTests(draft.Tests)
	}
	return summary
}

func summarizeReviewPlanDetail(detail db.PlanDetail) reviewPlanSummary {
	summary := summarizeReviewPlan(detail.Plan)
	if len(detail.Reviews) == 0 {
		return summary
	}
	latest := detail.Reviews[len(detail.Reviews)-1]
	switch latest.Decision {
	case db.PlanReviewDecisionApproved:
		summary.ReviewVerdict = string(reviewpkg.VerdictPass)
	case db.PlanReviewDecisionChangesRequested:
		summary.ReviewVerdict = string(reviewpkg.VerdictNeedsHuman)
	default:
		summary.ReviewVerdict = string(reviewpkg.VerdictUnavailable)
	}
	for _, item := range detail.Reviews {
		if finding := strings.TrimSpace(item.Comment); finding != "" {
			summary.ReviewFindings = append(summary.ReviewFindings, finding)
		}
	}
	return summary
}

type reviewStateSummary struct {
	ReviewModel      string `json:"reviewModel"`
	ReviewerReady    bool   `json:"reviewerReady"`
	RunnerIntegrated bool   `json:"runnerIntegrated"`
	FrozenMode       string `json:"frozenMode,omitempty"`
	FrozenRunID      string `json:"frozenRunId,omitempty"`
	PlanCount        int    `json:"planCount"`
}

type agentReviewState struct {
	ActivePlan          *reviewPlanSummary `json:"activePlan,omitempty"`
	PendingPlanApproval *reviewPlanSummary `json:"pendingPlanApproval,omitempty"`
	Review              reviewStateSummary `json:"review"`
}

func (s *Server) listReviewPlans(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, "limit"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plans, err := s.store.ListPlans(r.Context(), chi.URLParam(r, "id"), limit)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	out := make([]reviewPlanSummary, 0, len(plans))
	for _, plan := range plans {
		out = append(out, summarizeReviewPlan(plan))
	}
	writeJSON(w, http.StatusOK, out)
}

// createReviewPlan is an administrative/manual entry point. Normal plan-mode
// runs persist a structured plan through the Runner's PlanStore boundary.
func (s *Server) createReviewPlan(w http.ResponseWriter, r *http.Request) {
	var req createReviewPlanRequest
	if err := decodeLimitedJSON(w, r, &req, maxReviewPlanContentBytes+4096); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAPIText("summary", req.Summary, maxReviewPlanSummaryBytes, false, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Content) == 0 || len(req.Content) > maxReviewPlanContentBytes {
		writeError(w, http.StatusBadRequest, "content must be a bounded structured plan")
		return
	}
	draft, err := reviewpkg.ParsePlanDraft(string(req.Content))
	if err != nil {
		writeError(w, http.StatusBadRequest, "content must match the strict plan schema: "+err.Error())
		return
	}
	canonicalContent, err := json.Marshal(draft)
	if err != nil {
		writeError(w, http.StatusBadRequest, "content could not be normalized")
		return
	}
	if strings.TrimSpace(req.Summary) == "" {
		req.Summary = draft.Goal
	}
	actor, err := s.reviewActor(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	snapshot, err := s.currentPlanSnapshot(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	plan, err := s.store.CreatePlan(r.Context(), db.Plan{
		AgentID:                  chi.URLParam(r, "id"),
		Status:                   db.PlanStatusDraft,
		ContentJSON:              canonicalContent,
		Summary:                  req.Summary,
		PolicyGenerationSnapshot: snapshot.PolicyGenerationSnapshot,
		AgentGenerationSnapshot:  snapshot.AgentGenerationSnapshot,
		ToolCatalogDigest:        snapshot.ToolCatalogDigest,
		WorkspaceFingerprint:     snapshot.WorkspaceFingerprint,
	})
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	plan, err = s.store.TransitionPlanStatus(r.Context(), plan.AgentID, plan.ID, plan.Revision, db.PlanStatusInReview)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	reviewResult := reviewpkg.Result{Verdict: reviewpkg.VerdictUnavailable, Reason: "review service is not configured"}
	reviewerID := "system:reviewer-unavailable"
	if s.reviewer != nil {
		reviewerID = s.reviewer.ReviewerID()
		reviewResult, _ = s.reviewer.Review(r.Context(), reviewpkg.Request{Subject: "Review manually submitted plan " + plan.ID, Draft: draft})
	}
	decision := db.PlanReviewDecisionComment
	switch reviewResult.Verdict {
	case reviewpkg.VerdictPass:
		decision = db.PlanReviewDecisionApproved
	case reviewpkg.VerdictNeedsHuman, reviewpkg.VerdictBlockRecommended:
		decision = db.PlanReviewDecisionChangesRequested
	}
	if _, err := s.store.CreatePlanReview(r.Context(), db.PlanReview{
		PlanID: plan.ID, PlanRevision: plan.Revision, ReviewerID: reviewerID,
		Decision: decision, Comment: reviewResult.Reason,
	}); err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if err := s.recordReviewAudit(r.Context(), "plan.create", actor, plan, "success", "medium"); err != nil {
		writeError(w, http.StatusInternalServerError, "plan was created but audit persistence failed")
		return
	}
	detail, err := s.store.GetPlanDetail(r.Context(), plan.AgentID, plan.ID)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	s.publishReviewPlanEvent("plan.approval_required", detail)
	writeJSON(w, http.StatusCreated, summarizeReviewPlanDetail(detail))
}

func (s *Server) getReviewPlan(w http.ResponseWriter, r *http.Request) {
	detail, err := s.store.GetPlanDetail(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "planId"))
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) approveReviewPlan(w http.ResponseWriter, r *http.Request) {
	var req reviewPlanMutationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Revision < 1 {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	if err := validateAPIText("comment", req.Comment, 16<<10, false, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID, planID := chi.URLParam(r, "id"), chi.URLParam(r, "planId")
	plan, stale, err := s.requireCurrentPlan(r.Context(), agentID, planID, req.Revision)
	if stale {
		actor, _ := s.reviewActor(r)
		_ = s.recordReviewAudit(context.WithoutCancel(r.Context()), "plan.approve", actor, plan, "stale", "medium")
		writeJSON(w, http.StatusConflict, plan)
		return
	}
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	actor, err := s.reviewActor(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	approval, err := s.store.CreatePlanApproval(r.Context(), db.PlanApproval{
		PlanID: plan.ID, PlanRevision: plan.Revision, ApproverID: actor,
		Decision: db.PlanApprovalDecisionApproved, Comment: req.Comment,
	})
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	detail, err := s.store.GetPlanDetail(r.Context(), agentID, planID)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if err := s.recordReviewAudit(r.Context(), "plan.approve", actor, detail.Plan, "success", "high"); err != nil {
		writeError(w, http.StatusInternalServerError, "plan was approved but audit persistence failed")
		return
	}
	s.publishReviewPlanEvent("plan.approved", detail)
	writeJSON(w, http.StatusOK, map[string]any{"plan": summarizeReviewPlanDetail(detail), "approval": approval, "reviews": detail.Reviews, "runs": detail.Runs})
}

func (s *Server) executeReviewPlan(w http.ResponseWriter, r *http.Request) {
	var req reviewPlanMutationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Revision < 1 {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	agentID, planID := chi.URLParam(r, "id"), chi.URLParam(r, "planId")
	if err := s.enforceRemotePermissionCap(r, agentID); err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	plan, stale, err := s.requireCurrentPlan(r.Context(), agentID, planID, req.Revision)
	if stale {
		actor, _ := s.reviewActor(r)
		_ = s.recordReviewAudit(context.WithoutCancel(r.Context()), "plan.execute", actor, plan, "stale", "high")
		writeJSON(w, http.StatusConflict, plan)
		return
	}
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if plan.Status != db.PlanStatusApproved {
		writeError(w, http.StatusConflict, "plan is not approved")
		return
	}
	actor, err := s.reviewActor(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	message, err := s.submitApprovedPlan(r.Context(), plan, actor)
	if errors.Is(err, errPlanRunnerIntegrationMissing) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	detail, err := s.store.GetPlanDetail(r.Context(), agentID, planID)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if err := s.recordReviewAudit(r.Context(), "plan.execute", actor, detail.Plan, "success", "high"); err != nil {
		writeError(w, http.StatusInternalServerError, "plan execution was accepted but audit persistence failed")
		return
	}
	s.publishReviewPlanEvent("plan.executing", detail)
	writeJSON(w, http.StatusAccepted, map[string]any{"plan": summarizeReviewPlanDetail(detail), "message": message, "runId": message.RunID, "mode": db.RunExecutionModeExecute})
}

func (s *Server) cancelReviewPlan(w http.ResponseWriter, r *http.Request) {
	var req reviewPlanMutationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Revision < 1 {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	agentID, planID := chi.URLParam(r, "id"), chi.URLParam(r, "planId")
	actor, err := s.reviewActor(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	plan, err := s.store.TransitionPlanStatus(r.Context(), agentID, planID, req.Revision, db.PlanStatusCancelled)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if err := s.recordReviewAudit(r.Context(), "plan.cancel", actor, plan, "success", "medium"); err != nil {
		writeError(w, http.StatusInternalServerError, "plan was cancelled but audit persistence failed")
		return
	}
	detail, detailErr := s.store.GetPlanDetail(r.Context(), agentID, planID)
	if detailErr != nil {
		writeReviewServiceError(w, detailErr)
		return
	}
	s.publishReviewPlanEvent("plan.cancelled", detail)
	writeJSON(w, http.StatusOK, summarizeReviewPlanDetail(detail))
}

func (s *Server) replanReviewPlan(w http.ResponseWriter, r *http.Request) {
	var req reviewPlanMutationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Revision < 1 {
		writeError(w, http.StatusBadRequest, "revision is required")
		return
	}
	agentID, planID := chi.URLParam(r, "id"), chi.URLParam(r, "planId")
	plan, err := s.store.GetPlan(r.Context(), agentID, planID)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if plan.Revision != req.Revision {
		writeReviewServiceError(w, fmt.Errorf("%w: plan revision changed", db.ErrConflict))
		return
	}
	if plan.Status == db.PlanStatusExecuting || plan.Status == db.PlanStatusExecuted || plan.Status == db.PlanStatusCancelled {
		writeError(w, http.StatusConflict, "plan cannot be replanned from "+plan.Status)
		return
	}
	actor, err := s.reviewActor(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cancelled, err := s.store.TransitionPlanStatus(r.Context(), agentID, planID, req.Revision, db.PlanStatusCancelled)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	prompt := "Create a revised plan for the previously reviewed goal. Address prior risks and review findings. Previous plan JSON:\n" + string(plan.ContentJSON)
	message, err := s.submitReviewRun(r.Context(), agentID, prompt, actor, db.RunExecutionModePlan, s.remotePermissionModeCapForRequest(r), nil)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	if err := s.recordReviewAudit(r.Context(), "plan.replan", actor, cancelled, "success", "medium"); err != nil {
		writeError(w, http.StatusInternalServerError, "replan run was accepted but audit persistence failed")
		return
	}
	detail, detailErr := s.store.GetPlanDetail(r.Context(), agentID, planID)
	if detailErr == nil {
		s.publishReviewPlanEvent("plan.cancelled", detail)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"plan": summarizeReviewPlan(cancelled), "message": message, "runId": message.RunID, "mode": db.RunExecutionModePlan})
}

func (s *Server) reviewActor(r *http.Request) (string, error) {
	user, ok, err := s.currentUser(r)
	if err != nil {
		return "", err
	}
	if ok {
		return user.ID, nil
	}
	return "local-api", nil
}

func (s *Server) recordReviewAudit(ctx context.Context, action, actor string, plan db.Plan, outcome, risk string) error {
	return s.recordAudit(ctx, audit.Event{
		Category: "review", Action: action, Actor: actor, AgentID: plan.AgentID,
		SubjectType: "review_plan", SubjectID: plan.ID, Outcome: outcome, Risk: risk,
		Details: map[string]any{
			"status": plan.Status, "revision": plan.Revision, "staleReason": plan.StaleReason,
		},
	})
}

func (s *Server) publishReviewPlanEvent(eventType string, detail db.PlanDetail) {
	if s == nil || s.hub == nil {
		return
	}
	s.hub.Publish(agent.Event{Type: eventType, AgentID: detail.Plan.AgentID, Data: map[string]any{"plan": summarizeReviewPlanDetail(detail)}})
}

func writeReviewServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows), db.IsNotFound(err):
		writeError(w, http.StatusNotFound, "review plan not found")
	case errors.Is(err, errPlanStale), db.IsConflict(err):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errPlanRunnerIntegrationMissing):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case strings.Contains(strings.ToLower(err.Error()), "git") || strings.Contains(strings.ToLower(err.Error()), "workspace"):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) reviewModeForMessage(ctx context.Context, agentID, raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw != "" {
		if raw != db.RunExecutionModePlan && raw != db.RunExecutionModeExecute {
			return "", errors.New("mode must be plan or execute")
		}
		return raw, nil
	}
	if s.store == nil {
		return "", errors.New("agent store is unavailable")
	}
	agent, err := s.store.GetAgent(ctx, strings.TrimSpace(agentID))
	if err != nil {
		return "", err
	}
	if agent.PlanMode {
		return db.RunExecutionModePlan, nil
	}
	return db.RunExecutionModeExecute, nil
}

// submitReviewRun freezes the requested mode directly on the new Run. The
// Agent's persisted default is never changed, including transiently.
func (s *Server) submitReviewRun(ctx context.Context, agentID, text, createdBy, mode, permissionModeCap string, attachments []db.Attachment) (db.Message, error) {
	return s.submitReviewRunWithSource(ctx, agentID, text, createdBy, mode, permissionModeCap, db.RunSourceManual, attachments)
}

func (s *Server) submitReviewRunWithSource(ctx context.Context, agentID, text, createdBy, mode, permissionModeCap, runSource string, attachments []db.Attachment) (db.Message, error) {
	if s.runner == nil {
		return db.Message{}, errors.New("agent runner is not initialized")
	}
	var executionMode agent.ExecutionMode
	switch mode {
	case db.RunExecutionModePlan:
		executionMode = agent.ExecutionModePlan
	case db.RunExecutionModeExecute:
		executionMode = agent.ExecutionModeExecute
	default:
		return db.Message{}, errors.New("invalid run execution mode")
	}
	if strings.TrimSpace(createdBy) == "local-api" {
		createdBy = "api"
	}
	return s.runner.SubmitUserMessageWithModePermissionCapAndSource(ctx, agentID, text, createdBy, executionMode, permissionModeCap, runSource, attachments...)
}

// approvedPlanRunner is intentionally narrow: it must create a durable
// db.CreateRunForPlan execution before scheduling the loop. Calling the legacy
// generic message submission here could detach a run from the approved plan.
type approvedPlanRunner interface {
	SubmitApprovedPlan(context.Context, string, string) (db.Message, error)
}

func (s *Server) submitApprovedPlan(ctx context.Context, plan db.Plan, actor string) (db.Message, error) {
	if s.runner == nil {
		return db.Message{}, errors.New("agent runner is not initialized")
	}
	typed, ok := any(s.runner).(approvedPlanRunner)
	if !ok {
		return db.Message{}, errPlanRunnerIntegrationMissing
	}
	return typed.SubmitApprovedPlan(ctx, plan.ID, actor)
}

func (s *Server) requireCurrentPlan(ctx context.Context, agentID, planID string, expectedRevision int64) (db.Plan, bool, error) {
	plan, err := s.store.GetPlan(ctx, agentID, planID)
	if err != nil {
		return db.Plan{}, false, err
	}
	if expectedRevision < 1 || plan.Revision != expectedRevision {
		return db.Plan{}, false, fmt.Errorf("%w: plan revision changed", db.ErrConflict)
	}
	snapshot, err := s.currentPlanSnapshot(ctx, agentID)
	if err != nil {
		return db.Plan{}, false, err
	}
	if plan.PolicyGenerationSnapshot == snapshot.PolicyGenerationSnapshot &&
		plan.AgentGenerationSnapshot == snapshot.AgentGenerationSnapshot &&
		plan.ToolCatalogDigest == snapshot.ToolCatalogDigest &&
		plan.WorkspaceFingerprint == snapshot.WorkspaceFingerprint {
		return plan, false, nil
	}
	stale, err := s.store.MarkPlanStale(ctx, agentID, planID, expectedRevision, "agent permissions, policy, workspace, Git, tools, or plugins changed")
	if err != nil {
		return db.Plan{}, false, err
	}
	return stale, true, errPlanStale
}

func (s *Server) currentPlanSnapshot(ctx context.Context, agentID string) (db.PlanSnapshot, error) {
	if s.store == nil {
		return db.PlanSnapshot{}, errors.New("review store is unavailable")
	}
	agent, err := s.store.GetAgent(ctx, strings.TrimSpace(agentID))
	if err != nil {
		return db.PlanSnapshot{}, err
	}
	generations, err := s.store.GetPermissionGenerations(ctx, agent.ID)
	if err != nil {
		return db.PlanSnapshot{}, err
	}
	workspace, err := s.reviewWorkspaceFingerprint(ctx, agent)
	if err != nil {
		return db.PlanSnapshot{}, err
	}
	toolDigest, err := s.reviewToolCatalogDigest(ctx, agent, generations.Permission)
	if err != nil {
		return db.PlanSnapshot{}, err
	}
	return db.PlanSnapshot{
		PolicyGenerationSnapshot: generations.Policy,
		AgentGenerationSnapshot:  agent.EntityGeneration,
		ToolCatalogDigest:        toolDigest,
		WorkspaceFingerprint:     workspace,
	}, nil
}

func (s *Server) reviewWorkspaceFingerprint(ctx context.Context, agent db.Agent) (string, error) {
	workspace, err := filepath.Abs(strings.TrimSpace(agent.CWD))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return "", fmt.Errorf("workspace unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("workspace must be a directory")
	}
	repoRoot, _, err := runGitCommand(ctx, workspace, 4096, 3*time.Second, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("workspace must be a Git repository before a plan can be approved")
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if err := s.validateReviewRepoBoundary(ctx, agent, repoRoot); err != nil {
		return "", err
	}
	head, _, err := runGitCommand(ctx, repoRoot, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", errors.New("workspace Git HEAD is unavailable")
	}
	status, truncated, err := runGitCommand(ctx, repoRoot, reviewWorkspaceFingerprintStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return "", errors.New("workspace Git status is unavailable")
	}
	if truncated {
		return "", fmt.Errorf("workspace Git status exceeds the %d-byte review fingerprint limit", reviewWorkspaceFingerprintStatusMaxBytes)
	}
	entries, err := gitsnapshot.ParsePorcelainV1NoRenames(status)
	if err != nil {
		return "", fmt.Errorf("workspace Git status could not be parsed for review fingerprinting: %w", err)
	}
	if len(entries) > reviewWorkspaceFingerprintMaxPaths {
		return "", fmt.Errorf("workspace dirty path count exceeds the %d-path review fingerprint limit", reviewWorkspaceFingerprintMaxPaths)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	budget := &gitsnapshot.FingerprintBudget{
		MaxFileBytes:  reviewWorkspaceFingerprintMaxFileBytes,
		MaxTotalBytes: reviewWorkspaceFingerprintMaxTotalBytes,
	}
	parts := make([]string, 0, 4+len(entries)*5)
	parts = append(parts, "review-workspace-v3", workspace, repoRoot, strings.TrimSpace(head))
	for index, entry := range entries {
		if index > 0 && entry.Path == entries[index-1].Path {
			return "", fmt.Errorf("workspace Git status reported duplicate path %q", entry.Path)
		}
		indexFingerprint, err := gitRunIndexFingerprint(ctx, repoRoot, entry.Path)
		if err != nil {
			return "", fmt.Errorf("could not fingerprint staged workspace path %q: %w", entry.Path, err)
		}
		worktreeFingerprint, err := gitsnapshot.WorktreeFingerprintWithBudget(ctx, repoRoot, entry.Path, budget)
		if err != nil {
			return "", fmt.Errorf("could not fingerprint dirty workspace path %q: %w", entry.Path, err)
		}
		parts = append(parts, entry.Path, entry.IndexStatus, entry.WorktreeStatus, indexFingerprint, worktreeFingerprint)
	}
	return reviewHashParts(parts...), nil
}

func (s *Server) validateReviewRepoBoundary(ctx context.Context, agent db.Agent, repoRoot string) error {
	if strings.TrimSpace(agent.WorklineID) != "" {
		workline, project, err := s.worklineAndProject(ctx, agent.WorklineID)
		if err != nil {
			return err
		}
		if s.projectAllowsRepoRoot(project, repoRoot) || pathWithin(workline.WorktreePath, repoRoot) {
			return nil
		}
		return errors.New("workspace Git repository is outside the configured project boundary")
	}
	if root := strings.TrimSpace(s.configSnapshot().Paths.DefaultProjectDir); root != "" && pathWithin(root, repoRoot) {
		return nil
	}
	return errors.New("workspace Git repository is outside the configured project boundary")
}

func (s *Server) reviewToolCatalogDigest(ctx context.Context, agent db.Agent, permissionGeneration int64) (string, error) {
	type pluginRevision struct {
		ID       string `json:"id"`
		Slug     string `json:"slug"`
		Version  string `json:"version"`
		Revision int64  `json:"revision"`
		Enabled  bool   `json:"enabled"`
		Status   string `json:"status"`
	}
	items := make([]pluginRevision, 0)
	if s.plugins != nil {
		plugins, err := s.plugins.List(ctx)
		if err != nil {
			return "", fmt.Errorf("list plugin revisions: %w", err)
		}
		for _, plugin := range plugins {
			items = append(items, pluginRevision{ID: plugin.ID, Slug: plugin.Slug, Version: plugin.Version, Revision: plugin.Revision, Enabled: plugin.Enabled, Status: plugin.Status})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })

	target := db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeGlobal}
	if strings.TrimSpace(agent.WorklineID) != "" {
		workline, err := s.store.GetWorkline(ctx, agent.WorklineID)
		if err != nil {
			return "", fmt.Errorf("resolve tool availability scope: %w", err)
		}
		target = db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeWorkspace, ProjectID: workline.ProjectID, WorkspaceID: workline.ID}
	}
	toolNames := s.toolRegistrySnapshot().Names()
	availability, err := s.store.ResolveToolAvailabilities(ctx, target, toolNames)
	if err != nil {
		return "", fmt.Errorf("resolve tool availability digest: %w", err)
	}
	decisions := make([]db.ToolAvailabilityDecision, 0, len(toolNames))
	for _, name := range toolNames {
		decisions = append(decisions, availability[name])
	}
	encoded, err := json.Marshal(struct {
		PermissionGeneration int64                         `json:"permissionGeneration"`
		Tools                []string                      `json:"tools"`
		Availability         []db.ToolAvailabilityDecision `json:"availability"`
		Plugins              []pluginRevision              `json:"plugins"`
	}{PermissionGeneration: permissionGeneration, Tools: toolNames, Availability: decisions, Plugins: items})
	if err != nil {
		return "", err
	}
	return reviewHash(string(encoded)), nil
}

func (s *Server) agentReviewState(ctx context.Context, agentID string, latestRun *db.Run) (agentReviewState, error) {
	state := agentReviewState{Review: reviewStateSummary{
		ReviewModel:      s.configSnapshot().Agent.ReviewModel,
		ReviewerReady:    s.reviewer != nil,
		RunnerIntegrated: s.runner != nil,
	}}
	plans, err := s.store.ListPlans(ctx, agentID, 100)
	if err != nil {
		return agentReviewState{}, err
	}
	state.Review.PlanCount = len(plans)
	for _, plan := range plans {
		summary := summarizeReviewPlan(plan)
		if plan.Status == db.PlanStatusExecuting || plan.Status == db.PlanStatusInReview || plan.Status == db.PlanStatusApproved || plan.Status == db.PlanStatusStale {
			if detail, detailErr := s.store.GetPlanDetail(ctx, plan.AgentID, plan.ID); detailErr == nil {
				summary = summarizeReviewPlanDetail(detail)
			}
		}
		switch plan.Status {
		case db.PlanStatusExecuting, db.PlanStatusApproved, db.PlanStatusStale:
			if state.ActivePlan == nil {
				state.ActivePlan = &summary
			}
		case db.PlanStatusInReview:
			if state.PendingPlanApproval == nil {
				state.PendingPlanApproval = &summary
			}
		}
	}
	if latestRun != nil {
		state.Review.FrozenMode = latestRun.ExecutionMode
		state.Review.FrozenRunID = latestRun.ID
	}
	return state, nil
}

func reviewHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func reviewHashParts(values ...string) string {
	hash := sha256.New()
	var length [8]byte
	for _, value := range values {
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
