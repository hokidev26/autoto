package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"autoto/internal/db"
	"autoto/internal/review"
)

const (
	// PlanReflectionSourcePrefix tags the one automatic plan-mode retry so a
	// second needs_human cannot start a third run.
	PlanReflectionSourcePrefix = "plan-reflection:"
	// PlanReflectionPromptPrefix is the UI contract for the automatic replan
	// trigger message. The remainder of the prompt follows a newline.
	PlanReflectionPromptPrefix = "PLAN_REFLECTION_REPLAN"
)

const planDraftSystemPrompt = `
PLAN EXECUTION MODE IS ACTIVE.

You may inspect only the explicitly provided read-only tools. Do not attempt to write files, execute commands, invoke MCP, invoke plugins, or ask for approval to do so. A review or a future approval never authorizes execution in this mode.

Your final response must be exactly one JSON object, with no markdown or additional prose, matching this schema:
{"goal":"string","assumptions":["string"],"steps":["string"],"risks":["string"],"tests":["string"],"rollback":["string"]}

Every listed field is required. State uncertainties as assumptions or risks; do not invent implementation results.
`

var _ review.PlanStore = (*db.Store)(nil)

// planProviders holds the isolated reviewer and the snapshot callbacks used to
// bind plan and background-task runs to control-plane state. The mutex is
// private to this holder so snapshot lookup never shares a lock with the run loop.
type planProviders struct {
	mu                         sync.RWMutex
	reviewer                   *review.Service
	planSnapshotProvider       func(context.Context, string) (db.PlanSnapshot, error)
	backgroundSnapshotProvider func(context.Context, string) (db.PlanSnapshot, error)
	deferredReviews            map[string]review.Result
}

func (p *planProviders) setReviewer(service *review.Service) {
	p.mu.Lock()
	p.reviewer = service
	p.mu.Unlock()
}

func (p *planProviders) setPlanSnapshot(provider func(context.Context, string) (db.PlanSnapshot, error)) {
	p.mu.Lock()
	p.planSnapshotProvider = provider
	p.mu.Unlock()
}

func (p *planProviders) planSnapshot() func(context.Context, string) (db.PlanSnapshot, error) {
	p.mu.RLock()
	provider := p.planSnapshotProvider
	p.mu.RUnlock()
	return provider
}

func (p *planProviders) setBackgroundSnapshot(provider func(context.Context, string) (db.PlanSnapshot, error)) {
	p.mu.Lock()
	p.backgroundSnapshotProvider = provider
	p.mu.Unlock()
}

func (p *planProviders) backgroundSnapshot() func(context.Context, string) (db.PlanSnapshot, error) {
	p.mu.RLock()
	provider := p.backgroundSnapshotProvider
	p.mu.RUnlock()
	return provider
}

func (p *planProviders) rememberDeferredReview(runID string, result review.Result) {
	runID = strings.TrimSpace(runID)
	if p == nil || runID == "" {
		return
	}
	p.mu.Lock()
	if p.deferredReviews == nil {
		p.deferredReviews = make(map[string]review.Result)
	}
	p.deferredReviews[runID] = result
	p.mu.Unlock()
}

func (p *planProviders) takeDeferredReview(runID string) (review.Result, bool) {
	runID = strings.TrimSpace(runID)
	if p == nil || runID == "" {
		return review.Result{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result, ok := p.deferredReviews[runID]
	if ok {
		delete(p.deferredReviews, runID)
	}
	return result, ok
}

// SetReviewService installs the isolated reviewer. It is intentionally not a
// normal Agent and receives no tools or execution capabilities.
func (r *Runner) SetReviewService(service *review.Service) {
	if r == nil {
		return
	}
	r.plans.setReviewer(service)
}

func (r *Runner) reviewModelCandidates(ctx context.Context, agentID string) []string {
	conversation := ""
	if r != nil && r.store != nil && strings.TrimSpace(agentID) != "" {
		if agent, err := r.store.GetAgent(ctx, agentID); err == nil {
			conversation = agent.Model
		}
	}
	return review.CandidateModels(r.ReviewModel(), conversation)
}

// SetPlanSnapshotProvider installs the control-plane snapshot boundary used to
// bind plan runs to policy, Agent, tool-catalog, and workspace state.
func (r *Runner) SetPlanSnapshotProvider(provider func(context.Context, string) (db.PlanSnapshot, error)) {
	if r == nil {
		return
	}
	r.plans.setPlanSnapshot(provider)
}

func (r *Runner) currentPlanSnapshot(ctx context.Context, agentID string) (db.PlanSnapshot, bool, error) {
	if r == nil {
		return db.PlanSnapshot{}, false, nil
	}
	provider := r.plans.planSnapshot()
	if provider == nil {
		return db.PlanSnapshot{}, false, nil
	}
	snapshot, err := provider(ctx, agentID)
	return snapshot, true, err
}

// SetBackgroundTaskSnapshotProvider installs the non-Git runtime snapshot used
// to revalidate durable background tasks whose tool catalog is frozen without a
// plan workspace fingerprint. Plan snapshots remain independently Git-strict.
func (r *Runner) SetBackgroundTaskSnapshotProvider(provider func(context.Context, string) (db.PlanSnapshot, error)) {
	if r == nil {
		return
	}
	r.plans.setBackgroundSnapshot(provider)
}

func (r *Runner) currentBackgroundTaskSnapshot(ctx context.Context, agentID string) (db.PlanSnapshot, bool, error) {
	if r == nil {
		return db.PlanSnapshot{}, false, nil
	}
	provider := r.plans.backgroundSnapshot()
	if provider == nil {
		return db.PlanSnapshot{}, false, nil
	}
	snapshot, err := provider(ctx, agentID)
	return snapshot, true, err
}

func (r *Runner) bindPlanRunSnapshot(ctx context.Context, run db.Run) (db.Run, error) {
	if run.ExecutionMode != db.RunExecutionModePlan || strings.TrimSpace(run.PlanID) != "" {
		return run, nil
	}
	snapshot, configured, err := r.currentPlanSnapshot(ctx, run.AgentID)
	if err != nil {
		return db.Run{}, fmt.Errorf("capture plan safety snapshot: %w", err)
	}
	if !configured {
		return run, nil
	}
	run.PolicyGenerationSnapshot = snapshot.PolicyGenerationSnapshot
	run.AgentGenerationSnapshot = snapshot.AgentGenerationSnapshot
	run.ToolCatalogDigest = snapshot.ToolCatalogDigest
	run.WorkspaceFingerprint = snapshot.WorkspaceFingerprint
	return run, nil
}

func samePlanSnapshot(plan db.Plan, snapshot db.PlanSnapshot) bool {
	return plan.PolicyGenerationSnapshot == snapshot.PolicyGenerationSnapshot &&
		plan.AgentGenerationSnapshot == snapshot.AgentGenerationSnapshot &&
		plan.ToolCatalogDigest == snapshot.ToolCatalogDigest &&
		plan.WorkspaceFingerprint == snapshot.WorkspaceFingerprint
}

type planDeclaredTest struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

func declaredPlanTests(tests []string) []planDeclaredTest {
	out := make([]planDeclaredTest, 0, len(tests))
	for _, test := range tests {
		if text := strings.TrimSpace(test); text != "" {
			out = append(out, planDeclaredTest{Text: text, Status: "declared"})
		}
	}
	return out
}

func (r *Runner) publishPlanRunStatus(ctx context.Context, runID, eventType string) {
	if r == nil || r.store == nil || strings.TrimSpace(runID) == "" {
		return
	}
	run, err := r.store.GetRunByID(ctx, runID)
	if err != nil || strings.TrimSpace(run.PlanID) == "" {
		return
	}
	plan, err := r.store.GetPlanByID(ctx, run.PlanID)
	if err != nil {
		return
	}
	data := map[string]any{
		"id": plan.ID, "agentId": plan.AgentID, "status": plan.Status, "revision": plan.Revision,
		"summary": plan.Summary, "staleReason": plan.StaleReason, "createdAt": plan.CreatedAt, "updatedAt": plan.UpdatedAt,
		"steps": []string{}, "risks": []string{}, "tests": []planDeclaredTest{},
	}
	var draft review.PlanDraft
	if json.Unmarshal(plan.ContentJSON, &draft) == nil {
		data["goal"] = draft.Goal
		data["steps"] = append([]string{}, draft.Steps...)
		data["risks"] = append([]string{}, draft.Risks...)
		data["tests"] = declaredPlanTests(draft.Tests)
	}
	r.publish(Event{Type: eventType, AgentID: plan.AgentID, Data: map[string]any{"plan": data}})
}

// SubmitApprovedPlan performs a second stale check at the Runner boundary,
// creates an execute-mode Run durably linked to the approved plan, and only
// then schedules the normal Agent loop.
func (r *Runner) SubmitApprovedPlan(ctx context.Context, planID, createdBy string) (db.Message, error) {
	if r == nil || r.store == nil {
		return db.Message{}, errors.New("agent runner is not initialized")
	}
	plan, err := r.store.GetPlanByID(ctx, strings.TrimSpace(planID))
	if err != nil {
		return db.Message{}, err
	}
	if plan.Status != db.PlanStatusApproved {
		return db.Message{}, fmt.Errorf("%w: plan is not approved", db.ErrConflict)
	}
	if err := r.EnsureLocalExecution(ctx, plan.AgentID); err != nil {
		return db.Message{}, err
	}
	if snapshot, configured, snapshotErr := r.currentPlanSnapshot(ctx, plan.AgentID); snapshotErr != nil {
		return db.Message{}, fmt.Errorf("validate approved plan snapshot: %w", snapshotErr)
	} else if configured && !samePlanSnapshot(plan, snapshot) {
		_, _ = r.store.MarkPlanStale(context.WithoutCancel(ctx), plan.AgentID, plan.ID, plan.Revision, "agent permissions, policy, workspace, Git, tools, or plugins changed")
		return db.Message{}, fmt.Errorf("%w: approved plan inputs changed", db.ErrConflict)
	}
	if strings.TrimSpace(createdBy) == "local-api" {
		createdBy = "api"
	}
	prompt := "Execute the approved plan exactly as reviewed. Do not expand its scope.\n\nApproved plan ID: " + plan.ID + "\nApproved plan JSON:\n" + string(plan.ContentJSON)
	message, err := r.store.AddMessage(ctx, db.Message{AgentID: plan.AgentID, Role: "user", ContentText: prompt, CreatedBy: createdBy})
	if err != nil {
		return db.Message{}, err
	}
	run, err := r.store.CreateRunForPlan(ctx, plan.ID, db.Run{
		AgentID:          plan.AgentID,
		TriggerMessageID: message.ID,
		Status:           "pending",
		Source:           "manual",
		SourceID:         plan.ID,
		TriggerType:      "manual",
	})
	if err != nil {
		return db.Message{}, err
	}
	if err := r.store.AssignMessageRun(ctx, plan.AgentID, message.ID, run.ID); err != nil {
		return db.Message{}, err
	}
	message.RunID = run.ID
	r.publish(Event{Type: "message.created", AgentID: plan.AgentID, MessageID: message.ID, Text: prompt, Data: mergeEventData(map[string]any{"planId": plan.ID, "executionMode": db.RunExecutionModeExecute}, run.ID)})
	go r.runWithRun(context.Background(), plan.AgentID, run.ID, message.ID)
	return message, nil
}

// persistAndReviewPlan uses the concrete Store APIs to record a strict draft,
// transition it to review, and persist the isolated verdict. A reviewer pass
// never creates a PlanApproval or changes a run into execute mode.
func (r *Runner) persistAndReviewPlan(ctx context.Context, policy PolicyContext, assistantText string) (string, review.Result, error) {
	if !policy.IsPlan() {
		return assistantText, review.Result{}, nil
	}
	if r == nil || r.store == nil {
		return "", review.Result{}, errors.New("plan persistence store is not configured")
	}
	if strings.TrimSpace(policy.AgentID) == "" || strings.TrimSpace(policy.RunID) == "" {
		return "", review.Result{}, errors.New("plan execution mode requires durable agent and run ids")
	}
	draft, err := review.ParsePlanDraft(assistantText)
	if err != nil {
		return "", review.Result{}, fmt.Errorf("plan draft must be strict structured JSON: %w", err)
	}
	if err := r.store.PersistPlanDraft(ctx, policy.RunID, draft); err != nil {
		return "", review.Result{}, fmt.Errorf("persist plan draft: %w", err)
	}
	if err := r.store.TriggerPlanReview(ctx, policy.RunID); err != nil {
		return "", review.Result{}, fmt.Errorf("trigger plan review: %w", err)
	}

	result, reviewerID := review.ReviewWithCandidates(ctx, r.providers, r.reviewModelCandidates(ctx, policy.AgentID), review.Request{
		Subject: "Review plan draft for run " + policy.RunID,
		Draft:   draft,
	})
	if err := r.store.PersistPlanReview(ctx, policy.RunID, reviewerID, result); err != nil {
		return "", review.Result{}, fmt.Errorf("persist plan review: %w", err)
	}
	sourceRun, sourceErr := r.store.GetRunByID(ctx, policy.RunID)
	if sourceErr == nil && shouldAutoReflectPlan(sourceRun, result.Verdict) {
		// Defer plan.approval_required until after unregisterRun. Auto-replan
		// either replaces this in_review draft or publishes the event then.
		r.plans.rememberDeferredReview(policy.RunID, result)
	} else if plan, planErr := r.store.GetPlanBySourceRun(ctx, policy.RunID); planErr == nil {
		r.publishPlanApprovalRequired(plan, draft, result)
	}
	encoded, err := json.Marshal(draft)
	if err != nil {
		return "", review.Result{}, fmt.Errorf("encode persisted plan draft: %w", err)
	}
	return string(encoded), result, nil
}

func hasPlanReflectionSourceID(sourceID string) bool {
	return strings.HasPrefix(strings.TrimSpace(sourceID), PlanReflectionSourcePrefix)
}

func shouldAutoReflectPlan(run db.Run, verdict review.ReviewVerdict) bool {
	if strings.TrimSpace(run.ExecutionMode) != db.RunExecutionModePlan {
		return false
	}
	if verdict != review.VerdictNeedsHuman {
		return false
	}
	return !hasPlanReflectionSourceID(run.SourceID)
}

func (r *Runner) maybeStartPlanReflectionReplan(agentID, sourceRunID string, allowStart bool) bool {
	if r == nil {
		return false
	}
	result, ok := r.plans.takeDeferredReview(sourceRunID)
	if !ok {
		return false
	}
	ctx := context.Background()
	if allowStart {
		started, err := r.startPlanReflectionReplan(ctx, agentID, sourceRunID, result)
		if err != nil {
			slog.Error("plan reflection replan failed", "agentId", agentID, "runId", sourceRunID, "error", err)
		}
		if started {
			return true
		}
	}
	r.publishPlanApprovalRequiredFromRun(ctx, sourceRunID, result)
	return false
}

func (r *Runner) startPlanReflectionReplan(ctx context.Context, agentID, sourceRunID string, result review.Result) (bool, error) {
	if r == nil || r.store == nil {
		return false, errors.New("agent runner is not initialized")
	}
	agentID = strings.TrimSpace(agentID)
	sourceRunID = strings.TrimSpace(sourceRunID)
	if agentID == "" || sourceRunID == "" {
		return false, nil
	}
	sourceRun, err := r.store.GetRunByID(ctx, sourceRunID)
	if err != nil {
		return false, err
	}
	if sourceRun.AgentID != agentID || !shouldAutoReflectPlan(sourceRun, result.Verdict) {
		return false, nil
	}
	plan, err := r.store.GetPlanBySourceRun(ctx, sourceRunID)
	if err != nil {
		return false, err
	}
	if plan.Status != db.PlanStatusInReview {
		return false, nil
	}
	if err := r.EnsureLocalExecution(ctx, agentID); err != nil {
		return false, err
	}
	cancelled, err := r.store.TransitionPlanStatus(ctx, agentID, plan.ID, plan.Revision, db.PlanStatusCancelled)
	if err != nil {
		return false, err
	}
	reason := strings.TrimSpace(result.Reason)
	prompt := PlanReflectionPromptPrefix + "\nRevise the previous plan so it achieves the stated goal. The isolated reviewer requested this correction:\n" + reason + "\n\nPrevious plan JSON:\n" + string(plan.ContentJSON)
	message, err := r.store.AddMessage(ctx, db.Message{AgentID: agentID, Role: "user", ContentText: prompt, CreatedBy: "api"})
	if err != nil {
		return false, err
	}
	runRequest, err := r.bindPlanRunSnapshot(ctx, db.Run{
		AgentID:          agentID,
		TriggerMessageID: message.ID,
		Status:           "pending",
		Source:           db.RunSourceManual,
		SourceID:         PlanReflectionSourcePrefix + plan.ID,
		TriggerType:      "internal",
		ExecutionMode:    db.RunExecutionModePlan,
	})
	if err != nil {
		return false, err
	}
	runRequest, err = r.prepareContinuationRun(ctx, runRequest)
	if err != nil {
		return false, err
	}
	run, err := r.store.CreateRun(ctx, runRequest)
	if err != nil {
		return false, err
	}
	if err := r.store.AssignMessageRun(ctx, agentID, message.ID, run.ID); err != nil {
		return false, err
	}
	message.RunID = run.ID
	r.publishPlanLifecycle("plan.cancelled", cancelled, result)
	r.publish(Event{Type: "message.created", AgentID: agentID, MessageID: message.ID, Text: prompt, Data: mergeEventData(map[string]any{"executionMode": db.RunExecutionModePlan}, run.ID)})
	go r.runWithRun(context.Background(), agentID, run.ID, message.ID)
	return true, nil
}

func (r *Runner) publishPlanApprovalRequiredFromRun(ctx context.Context, sourceRunID string, result review.Result) {
	if r == nil || r.store == nil {
		return
	}
	plan, err := r.store.GetPlanBySourceRun(ctx, sourceRunID)
	if err != nil {
		return
	}
	var draft review.PlanDraft
	if json.Unmarshal(plan.ContentJSON, &draft) != nil {
		draft = review.PlanDraft{}
	}
	r.publishPlanApprovalRequired(plan, draft, result)
}

func (r *Runner) publishPlanApprovalRequired(plan db.Plan, draft review.PlanDraft, result review.Result) {
	r.publish(Event{Type: "plan.approval_required", AgentID: plan.AgentID, Data: map[string]any{"plan": r.planEventPayload(plan, draft, result)}})
}

func (r *Runner) publishPlanLifecycle(eventType string, plan db.Plan, result review.Result) {
	var draft review.PlanDraft
	if json.Unmarshal(plan.ContentJSON, &draft) != nil {
		draft = review.PlanDraft{}
	}
	r.publish(Event{Type: eventType, AgentID: plan.AgentID, Data: map[string]any{"plan": r.planEventPayload(plan, draft, result)}})
}

func (r *Runner) planEventPayload(plan db.Plan, draft review.PlanDraft, result review.Result) map[string]any {
	return map[string]any{
		"id": plan.ID, "agentId": plan.AgentID, "status": plan.Status, "revision": plan.Revision,
		"summary": plan.Summary, "goal": draft.Goal, "steps": draft.Steps, "risks": draft.Risks,
		"tests": declaredPlanTests(draft.Tests), "reviewVerdict": result.Verdict, "reviewFindings": []string{result.Reason},
		"createdAt": plan.CreatedAt, "updatedAt": plan.UpdatedAt,
	}
}
