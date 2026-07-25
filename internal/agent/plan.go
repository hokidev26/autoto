package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/db"
	"autoto/internal/review"
)

const planDraftSystemPrompt = `
PLAN EXECUTION MODE IS ACTIVE.

You may inspect only the explicitly provided read-only tools. Do not attempt to write files, execute commands, invoke MCP, invoke plugins, or ask for approval to do so. A review or a future approval never authorizes execution in this mode.

Your final response must be exactly one JSON object, with no markdown or additional prose, matching this schema:
{"goal":"string","assumptions":["string"],"steps":["string"],"risks":["string"],"tests":["string"],"rollback":["string"]}

Every listed field is required. State uncertainties as assumptions or risks; do not invent implementation results.
`

var _ review.PlanStore = (*db.Store)(nil)

// SetReviewService installs the isolated reviewer. It is intentionally not a
// normal Agent and receives no tools or execution capabilities.
func (r *Runner) SetReviewService(service *review.Service) {
	if r == nil {
		return
	}
	r.planMu.Lock()
	r.reviewer = service
	r.planMu.Unlock()
}

func (r *Runner) reviewerService() *review.Service {
	if r == nil {
		return nil
	}
	r.planMu.RLock()
	defer r.planMu.RUnlock()
	return r.reviewer
}

// SetPlanSnapshotProvider installs the control-plane snapshot boundary used to
// bind plan runs to policy, Agent, tool-catalog, and workspace state.
func (r *Runner) SetPlanSnapshotProvider(provider func(context.Context, string) (db.PlanSnapshot, error)) {
	if r == nil {
		return
	}
	r.planMu.Lock()
	r.planSnapshotProvider = provider
	r.planMu.Unlock()
}

func (r *Runner) currentPlanSnapshot(ctx context.Context, agentID string) (db.PlanSnapshot, bool, error) {
	if r == nil {
		return db.PlanSnapshot{}, false, nil
	}
	r.planMu.RLock()
	provider := r.planSnapshotProvider
	r.planMu.RUnlock()
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
		return "", review.Result{}, fmt.Errorf("plan persistence store is not configured")
	}
	if strings.TrimSpace(policy.AgentID) == "" || strings.TrimSpace(policy.RunID) == "" {
		return "", review.Result{}, fmt.Errorf("plan execution mode requires durable agent and run ids")
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

	result := review.Result{Verdict: review.VerdictUnavailable, Reason: "review service is not configured"}
	reviewerID := "system:reviewer-unavailable"
	if service := r.reviewerService(); service != nil {
		reviewerID = service.ReviewerID()
		result, _ = service.Review(ctx, review.Request{Subject: "Review plan draft for run " + policy.RunID, Draft: draft})
	}
	if err := r.store.PersistPlanReview(ctx, policy.RunID, reviewerID, result); err != nil {
		return "", review.Result{}, fmt.Errorf("persist plan review: %w", err)
	}
	if plan, planErr := r.store.GetPlanBySourceRun(ctx, policy.RunID); planErr == nil {
		r.publish(Event{Type: "plan.approval_required", AgentID: policy.AgentID, Data: map[string]any{
			"plan": map[string]any{
				"id": plan.ID, "agentId": plan.AgentID, "status": plan.Status, "revision": plan.Revision,
				"summary": plan.Summary, "goal": draft.Goal, "steps": draft.Steps, "risks": draft.Risks,
				"tests": declaredPlanTests(draft.Tests), "reviewVerdict": result.Verdict, "reviewFindings": []string{result.Reason},
				"createdAt": plan.CreatedAt, "updatedAt": plan.UpdatedAt,
			},
		}})
	}
	encoded, err := json.Marshal(draft)
	if err != nil {
		return "", review.Result{}, fmt.Errorf("encode persisted plan draft: %w", err)
	}
	return string(encoded), result, nil
}
