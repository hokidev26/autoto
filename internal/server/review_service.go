package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"autoto/internal/db"
	reviewpkg "autoto/internal/review"
)

type reviewService struct {
	server *Server
}

func (s *Server) reviews() reviewService {
	return reviewService{server: s}
}

func (s *Server) reviewModelCandidates(ctx context.Context, agentID string) []string {
	configured := ""
	conversation := ""
	if s != nil {
		configured = s.configSnapshot().Agent.ReviewModel
		if s.store != nil && strings.TrimSpace(agentID) != "" {
			if agent, err := s.store.GetAgent(ctx, agentID); err == nil {
				conversation = agent.Model
			}
		}
	}
	return reviewpkg.CandidateModels(configured, conversation)
}

type reviewResult struct {
	Status int
	Body   any
}

func (rv reviewService) list(ctx context.Context, agentID string, limit int) ([]reviewPlanSummary, error) {
	s := rv.server
	plans, err := s.store.ListPlans(ctx, agentID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]reviewPlanSummary, 0, len(plans))
	for _, plan := range plans {
		out = append(out, summarizeReviewPlan(plan))
	}
	return out, nil
}

func (rv reviewService) get(ctx context.Context, agentID, planID string) (db.PlanDetail, error) {
	return rv.server.store.GetPlanDetail(ctx, agentID, planID)
}

func (rv reviewService) create(ctx context.Context, agentID string, req createReviewPlanRequest, actor string) (reviewPlanSummary, error) {
	s := rv.server
	if err := validateAPIText("summary", req.Summary, maxReviewPlanSummaryBytes, false, true); err != nil {
		return reviewPlanSummary{}, apiErr(http.StatusBadRequest, err.Error())
	}
	if len(req.Content) == 0 || len(req.Content) > maxReviewPlanContentBytes {
		return reviewPlanSummary{}, apiErr(http.StatusBadRequest, "content must be a bounded structured plan")
	}
	draft, err := reviewpkg.ParsePlanDraft(string(req.Content))
	if err != nil {
		return reviewPlanSummary{}, apiErr(http.StatusBadRequest, "content must match the strict plan schema: "+err.Error())
	}
	canonicalContent, err := json.Marshal(draft)
	if err != nil {
		return reviewPlanSummary{}, apiErr(http.StatusBadRequest, "content could not be normalized")
	}
	if strings.TrimSpace(req.Summary) == "" {
		req.Summary = draft.Goal
	}
	snapshot, err := s.currentPlanSnapshot(ctx, agentID)
	if err != nil {
		return reviewPlanSummary{}, err
	}
	plan, err := s.store.CreatePlan(ctx, db.Plan{
		AgentID:                  agentID,
		Status:                   db.PlanStatusDraft,
		ContentJSON:              canonicalContent,
		Summary:                  req.Summary,
		PolicyGenerationSnapshot: snapshot.PolicyGenerationSnapshot,
		AgentGenerationSnapshot:  snapshot.AgentGenerationSnapshot,
		ToolCatalogDigest:        snapshot.ToolCatalogDigest,
		WorkspaceFingerprint:     snapshot.WorkspaceFingerprint,
	})
	if err != nil {
		return reviewPlanSummary{}, err
	}
	plan, err = s.store.TransitionPlanStatus(ctx, plan.AgentID, plan.ID, plan.Revision, db.PlanStatusInReview)
	if err != nil {
		return reviewPlanSummary{}, err
	}
	reviewResult, reviewerID := reviewpkg.ReviewWithCandidates(ctx, s.providers, s.reviewModelCandidates(ctx, agentID), reviewpkg.Request{
		Subject: "Review manually submitted plan " + plan.ID,
		Draft:   draft,
	})
	decision := db.PlanReviewDecisionComment
	switch reviewResult.Verdict {
	case reviewpkg.VerdictPass:
		decision = db.PlanReviewDecisionApproved
	case reviewpkg.VerdictNeedsHuman, reviewpkg.VerdictBlockRecommended:
		decision = db.PlanReviewDecisionChangesRequested
	}
	if _, err := s.store.CreatePlanReview(ctx, db.PlanReview{
		PlanID: plan.ID, PlanRevision: plan.Revision, ReviewerID: reviewerID,
		Decision: decision, Comment: reviewResult.Reason,
	}); err != nil {
		return reviewPlanSummary{}, err
	}
	if err := s.recordReviewAudit(ctx, "plan.create", actor, plan, "success", "medium"); err != nil {
		return reviewPlanSummary{}, apiErr(http.StatusInternalServerError, "plan was created but audit persistence failed")
	}
	detail, err := s.store.GetPlanDetail(ctx, plan.AgentID, plan.ID)
	if err != nil {
		return reviewPlanSummary{}, err
	}
	s.publishReviewPlanEvent("plan.approval_required", detail)
	return summarizeReviewPlanDetail(detail), nil
}

func (rv reviewService) approve(ctx context.Context, agentID, planID string, req reviewPlanMutationRequest, actorFn func() (string, error)) (reviewResult, error) {
	s := rv.server
	plan, stale, err := s.requireCurrentPlan(ctx, agentID, planID, req.Revision)
	if stale {
		actor, _ := actorFn()
		_ = s.recordReviewAudit(context.WithoutCancel(ctx), "plan.approve", actor, plan, "stale", "medium")
		return reviewResult{Status: http.StatusConflict, Body: plan}, nil
	}
	if err != nil {
		return reviewResult{}, err
	}
	actor, err := actorFn()
	if err != nil {
		return reviewResult{}, apiErr(http.StatusInternalServerError, err.Error())
	}
	approval, err := s.store.CreatePlanApproval(ctx, db.PlanApproval{
		PlanID: plan.ID, PlanRevision: plan.Revision, ApproverID: actor,
		Decision: db.PlanApprovalDecisionApproved, Comment: req.Comment,
	})
	if err != nil {
		return reviewResult{}, err
	}
	detail, err := s.store.GetPlanDetail(ctx, agentID, planID)
	if err != nil {
		return reviewResult{}, err
	}
	if err := s.recordReviewAudit(ctx, "plan.approve", actor, detail.Plan, "success", "high"); err != nil {
		return reviewResult{}, apiErr(http.StatusInternalServerError, "plan was approved but audit persistence failed")
	}
	s.publishReviewPlanEvent("plan.approved", detail)
	return reviewResult{Status: http.StatusOK, Body: map[string]any{"plan": summarizeReviewPlanDetail(detail), "approval": approval, "reviews": detail.Reviews, "runs": detail.Runs}}, nil
}

func (rv reviewService) execute(ctx context.Context, agentID, planID string, req reviewPlanMutationRequest, actorFn func() (string, error)) (reviewResult, error) {
	s := rv.server
	plan, stale, err := s.requireCurrentPlan(ctx, agentID, planID, req.Revision)
	if stale {
		actor, _ := actorFn()
		_ = s.recordReviewAudit(context.WithoutCancel(ctx), "plan.execute", actor, plan, "stale", "high")
		return reviewResult{Status: http.StatusConflict, Body: plan}, nil
	}
	if err != nil {
		return reviewResult{}, err
	}
	if plan.Status != db.PlanStatusApproved {
		return reviewResult{}, apiErr(http.StatusConflict, "plan is not approved")
	}
	actor, err := actorFn()
	if err != nil {
		return reviewResult{}, apiErr(http.StatusInternalServerError, err.Error())
	}
	message, err := s.submitApprovedPlan(ctx, plan, actor)
	if errors.Is(err, errPlanRunnerIntegrationMissing) {
		return reviewResult{}, apiErr(http.StatusServiceUnavailable, err.Error())
	}
	if err != nil {
		return reviewResult{}, err
	}
	detail, err := s.store.GetPlanDetail(ctx, agentID, planID)
	if err != nil {
		return reviewResult{}, err
	}
	if err := s.recordReviewAudit(ctx, "plan.execute", actor, detail.Plan, "success", "high"); err != nil {
		return reviewResult{}, apiErr(http.StatusInternalServerError, "plan execution was accepted but audit persistence failed")
	}
	s.publishReviewPlanEvent("plan.executing", detail)
	return reviewResult{Status: http.StatusAccepted, Body: map[string]any{"plan": summarizeReviewPlanDetail(detail), "message": message, "runId": message.RunID, "mode": db.RunExecutionModeExecute}}, nil
}

func (rv reviewService) cancel(ctx context.Context, agentID, planID string, req reviewPlanMutationRequest, actor string) (reviewPlanSummary, error) {
	s := rv.server
	plan, err := s.store.TransitionPlanStatus(ctx, agentID, planID, req.Revision, db.PlanStatusCancelled)
	if err != nil {
		return reviewPlanSummary{}, err
	}
	if err := s.recordReviewAudit(ctx, "plan.cancel", actor, plan, "success", "medium"); err != nil {
		return reviewPlanSummary{}, apiErr(http.StatusInternalServerError, "plan was cancelled but audit persistence failed")
	}
	detail, detailErr := s.store.GetPlanDetail(ctx, agentID, planID)
	if detailErr != nil {
		return reviewPlanSummary{}, detailErr
	}
	s.publishReviewPlanEvent("plan.cancelled", detail)
	return summarizeReviewPlanDetail(detail), nil
}

func (rv reviewService) replan(ctx context.Context, agentID, planID string, req reviewPlanMutationRequest, actor, permissionModeCap string) (reviewResult, error) {
	s := rv.server
	plan, err := s.store.GetPlan(ctx, agentID, planID)
	if err != nil {
		return reviewResult{}, err
	}
	if plan.Revision != req.Revision {
		return reviewResult{}, fmt.Errorf("%w: plan revision changed", db.ErrConflict)
	}
	if plan.Status == db.PlanStatusExecuting || plan.Status == db.PlanStatusExecuted || plan.Status == db.PlanStatusCancelled {
		return reviewResult{}, apiErr(http.StatusConflict, "plan cannot be replanned from "+plan.Status)
	}
	cancelled, err := s.store.TransitionPlanStatus(ctx, agentID, planID, req.Revision, db.PlanStatusCancelled)
	if err != nil {
		return reviewResult{}, err
	}
	prompt := "Create a revised plan for the previously reviewed goal. Address prior risks and review findings."
	if feedback := strings.TrimSpace(req.Comment); feedback != "" {
		prompt += "\n\nThe reviewer rejected the previous plan with this feedback, and the revised plan must address it:\n" + feedback
	}
	prompt += "\n\nPrevious plan JSON:\n" + string(plan.ContentJSON)
	message, err := s.submitReviewRun(ctx, agentID, prompt, actor, db.RunExecutionModePlan, permissionModeCap, nil)
	if err != nil {
		return reviewResult{}, err
	}
	if err := s.recordReviewAudit(ctx, "plan.replan", actor, cancelled, "success", "medium"); err != nil {
		return reviewResult{}, apiErr(http.StatusInternalServerError, "replan run was accepted but audit persistence failed")
	}
	detail, detailErr := s.store.GetPlanDetail(ctx, agentID, planID)
	if detailErr == nil {
		s.publishReviewPlanEvent("plan.cancelled", detail)
	}
	return reviewResult{Status: http.StatusAccepted, Body: map[string]any{"plan": summarizeReviewPlan(cancelled), "message": message, "runId": message.RunID, "mode": db.RunExecutionModePlan}}, nil
}
