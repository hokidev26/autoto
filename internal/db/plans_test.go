package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/review"
)

func TestPlanFreshSchemaAndV37MigrationPreserveRunDefaults(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if version := readUserVersion(t, ctx, fresh.DB()); version != CurrentDBVersion {
		t.Fatalf("expected fresh v%d, got v%d", CurrentDBVersion, version)
	}
	for _, table := range []string{"plans", "plan_reviews", "plan_approvals"} {
		if !testTableExists(t, ctx, fresh.DB(), table) {
			t.Fatalf("fresh schema missing %s", table)
		}
	}
	for _, column := range []string{"execution_mode", "plan_id", "policy_generation_snapshot", "agent_generation_snapshot", "tool_catalog_digest", "workspace_fingerprint"} {
		if !testColumnExists(t, ctx, fresh.DB(), "runs", column) {
			t.Fatalf("fresh runs missing %s", column)
		}
	}
	for _, column := range []string{"source_run_id", "status", "revision", "content_json", "policy_generation_snapshot", "agent_generation_snapshot", "tool_catalog_digest", "workspace_fingerprint", "stale_reason"} {
		if !testColumnExists(t, ctx, fresh.DB(), "plans", column) {
			t.Fatalf("fresh plans missing %s", column)
		}
	}
	if !testNamedIndexExists(t, ctx, fresh.DB(), "idx_runs_plan") || !testNamedIndexExists(t, ctx, fresh.DB(), "idx_plans_source_run") {
		t.Fatal("fresh schema missing plan association indexes")
	}

	path := filepath.Join(t.TempDir(), "v37.db")
	raw := openRawDB(t, path)
	v37Schema := strings.TrimSuffix(schemaSQL, planSchemaSQL)
	v37RunColumns := `  execution_mode TEXT NOT NULL DEFAULT 'execute',
  plan_id TEXT REFERENCES plans(id) ON DELETE SET NULL,
  policy_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  agent_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  tool_catalog_digest TEXT NOT NULL DEFAULT '',
  workspace_fingerprint TEXT NOT NULL DEFAULT '',
`
	if got := strings.Count(v37Schema, v37RunColumns); got != 1 {
		raw.Close()
		t.Fatalf("expected one current run metadata block, got %d", got)
	}
	v37Schema = strings.Replace(v37Schema, v37RunColumns, "", 1)
	v37RunChecks := `,
  CHECK (execution_mode IN ('plan', 'execute')),
  CHECK (execution_mode = 'execute' OR plan_id IS NULL),
  CHECK (policy_generation_snapshot >= 0),
  CHECK (agent_generation_snapshot >= 0),
  CHECK (length(CAST(tool_catalog_digest AS BLOB)) <= 512),
  CHECK (length(CAST(workspace_fingerprint AS BLOB)) <= 512)`
	if got := strings.Count(v37Schema, v37RunChecks); got != 1 {
		raw.Close()
		t.Fatalf("expected one current run metadata check block, got %d", got)
	}
	v37Schema = strings.Replace(v37Schema, v37RunChecks, "", 1)
	v37Schema = strings.Replace(v37Schema, "CREATE INDEX IF NOT EXISTS idx_runs_plan ON runs(plan_id, execution_generation DESC, id DESC);\n", "", 1)
	if _, err := raw.ExecContext(ctx, v37Schema); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	now := Now()
	if _, err := raw.ExecContext(ctx, `INSERT INTO agents (id, title, model, permission_mode, status, created_at, updated_at) VALUES ('agent-v37', 'Legacy', 'fake:model', 'acceptEdits', 'idle', ?, ?)`, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, checkpoint_state, source, source_id, permission_mode_cap, execution_generation, trigger_type, execution_device_id, created_at, updated_at) VALUES ('run-v37', 'agent-v37', 'completed', 'none', 'manual', '', '', 1, 'manual', 'local', ?, ?)`, now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 37`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version := readUserVersion(t, ctx, migrated.DB()); version != CurrentDBVersion {
		t.Fatalf("expected migrated v%d, got v%d", CurrentDBVersion, version)
	}
	legacy, err := migrated.GetRun(ctx, "agent-v37", "run-v37")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ExecutionMode != RunExecutionModeExecute || legacy.PlanID != "" || legacy.PolicyGenerationSnapshot != 0 || legacy.AgentGenerationSnapshot != 0 || legacy.ToolCatalogDigest != "" || legacy.WorkspaceFingerprint != "" {
		t.Fatalf("legacy run defaults were not preserved: %+v", legacy)
	}
	for _, table := range []string{"plans", "plan_reviews", "plan_approvals"} {
		if !testTableExists(t, ctx, migrated.DB(), table) {
			t.Fatalf("migrated schema missing %s", table)
		}
	}
}

func TestPlanLifecycleCASApprovalsStalenessAndExecutionRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Plans", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	generations, err := store.GetPermissionGenerations(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreatePlan(ctx, Plan{AgentID: agent.ID, Status: PlanStatusApproved, ContentJSON: []byte(`{}`)}); err == nil {
		t.Fatal("new plans must not bypass draft state")
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO plans (id, agent_id, status, revision, content_json, created_at, updated_at) VALUES ('invalid-plan', ?, 'invalid', 1, '{}', ?, ?)`, agent.ID, Now(), Now()); err == nil {
		t.Fatal("fresh schema must enforce plan status")
	}
	created, err := store.CreatePlan(ctx, Plan{
		AgentID:                  agent.ID,
		ContentJSON:              []byte(`{"goal":"ship","steps":["edit"]}`),
		Summary:                  "Ship safely",
		PolicyGenerationSnapshot: generations.Policy,
		AgentGenerationSnapshot:  agent.EntityGeneration,
		ToolCatalogDigest:        "tools-v1",
		WorkspaceFingerprint:     "workspace-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != PlanStatusDraft || created.Revision != 1 || created.ID == "" {
		t.Fatalf("unexpected created plan: %+v", created)
	}

	updated, err := store.UpdatePlanCAS(ctx, Plan{
		ID:                       created.ID,
		AgentID:                  agent.ID,
		ContentJSON:              []byte(`{"goal":"ship v2","steps":["edit","test"]}`),
		Summary:                  "Ship version two",
		PolicyGenerationSnapshot: generations.Policy,
		AgentGenerationSnapshot:  agent.EntityGeneration,
		ToolCatalogDigest:        "tools-v2",
		WorkspaceFingerprint:     "workspace-v2",
	}, created.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Status != PlanStatusDraft || updated.StaleReason != "" {
		t.Fatalf("unexpected CAS update: %+v", updated)
	}
	if _, err := store.UpdatePlan(ctx, updated, created.Revision); !IsConflict(err) {
		t.Fatalf("stale update should conflict, got %v", err)
	}

	inReview, err := store.TransitionPlanStatus(ctx, agent.ID, updated.ID, updated.Revision, PlanStatusInReview)
	if err != nil {
		t.Fatal(err)
	}
	if inReview.Status != PlanStatusInReview || inReview.Revision != updated.Revision {
		t.Fatalf("unexpected review transition: %+v", inReview)
	}
	if _, err := store.TransitionPlanStatus(ctx, agent.ID, updated.ID, updated.Revision, PlanStatusApproved); !IsConflict(err) {
		t.Fatalf("direct approval must be rejected, got %v", err)
	}
	reviewRecord, err := store.CreatePlanReview(ctx, PlanReview{PlanID: updated.ID, PlanRevision: updated.Revision, ReviewerID: "reviewer-1", Decision: PlanReviewDecisionApproved, Comment: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	if reviewRecord.ID == "" || reviewRecord.CreatedAt == "" {
		t.Fatalf("unexpected review record: %+v", reviewRecord)
	}
	approval, err := store.CreatePlanApproval(ctx, PlanApproval{PlanID: updated.ID, PlanRevision: updated.Revision, ApproverID: "owner-1", Decision: PlanApprovalDecisionApproved, Comment: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	if approval.ID == "" {
		t.Fatal("expected approval id")
	}
	approved, err := store.GetPlan(ctx, agent.ID, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != PlanStatusApproved || approved.Revision != updated.Revision {
		t.Fatalf("approval should transition the current revision: %+v", approved)
	}
	if _, err := store.CreatePlanApproval(ctx, PlanApproval{PlanID: updated.ID, PlanRevision: updated.Revision, ApproverID: "owner-1", Decision: PlanApprovalDecisionApproved}); !IsConflict(err) {
		t.Fatalf("duplicate approval should conflict, got %v", err)
	}

	run, err := store.CreateRunForPlan(ctx, updated.ID, Run{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionMode != RunExecutionModeExecute || run.PlanID != updated.ID || run.PolicyGenerationSnapshot != updated.PolicyGenerationSnapshot || run.AgentGenerationSnapshot != updated.AgentGenerationSnapshot || run.ToolCatalogDigest != updated.ToolCatalogDigest || run.WorkspaceFingerprint != updated.WorkspaceFingerprint {
		t.Fatalf("execution run did not inherit approved plan snapshots: %+v", run)
	}
	executing, err := store.GetPlan(ctx, agent.ID, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executing.Status != PlanStatusExecuting {
		t.Fatalf("execution run should transition plan to executing: %+v", executing)
	}
	if err := store.UpdateRunStatus(ctx, run.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	executed, err := store.GetPlanForRun(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executed.ID != updated.ID || executed.Status != PlanStatusExecuted {
		t.Fatalf("completed run should complete plan: %+v", executed)
	}
	detail, err := store.GetPlanDetail(ctx, agent.ID, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Reviews) != 1 || len(detail.Approvals) != 1 || len(detail.Runs) != 1 || detail.Runs[0].ID != run.ID {
		t.Fatalf("unexpected plan detail: %+v", detail)
	}
	linkedRuns, err := store.ListRunsByPlan(ctx, agent.ID, updated.ID, 10)
	if err != nil || len(linkedRuns) != 1 || linkedRuns[0].ID != run.ID {
		t.Fatalf("unexpected linked runs: %+v err=%v", linkedRuns, err)
	}
	plans, err := store.ListPlans(ctx, agent.ID, 10)
	if err != nil || len(plans) != 1 || plans[0].ID != updated.ID {
		t.Fatalf("unexpected plan list: %+v err=%v", plans, err)
	}

	staleCandidate, err := store.CreatePlan(ctx, Plan{AgentID: agent.ID, ContentJSON: []byte(`{"goal":"later"}`), PolicyGenerationSnapshot: 1, AgentGenerationSnapshot: 1, ToolCatalogDigest: "tools-old", WorkspaceFingerprint: "workspace-old"})
	if err != nil {
		t.Fatal(err)
	}
	count, err := store.MarkPlansStale(ctx, agent.ID, PlanSnapshot{PolicyGenerationSnapshot: 2, AgentGenerationSnapshot: 1, ToolCatalogDigest: "tools-old", WorkspaceFingerprint: "workspace-old"}, "policy changed")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one stale plan, got %d", count)
	}
	stale, err := store.GetPlan(ctx, agent.ID, staleCandidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != PlanStatusStale || stale.StaleReason != "policy changed" {
		t.Fatalf("unexpected stale plan: %+v", stale)
	}
	singleStale, err := store.CreatePlan(ctx, Plan{AgentID: agent.ID, ContentJSON: []byte(`{"goal":"single stale"}`)})
	if err != nil {
		t.Fatal(err)
	}
	marked, err := store.MarkPlanStale(ctx, agent.ID, singleStale.ID, singleStale.Revision, "workspace changed")
	if err != nil {
		t.Fatal(err)
	}
	if marked.Status != PlanStatusStale || marked.StaleReason != "workspace changed" {
		t.Fatalf("unexpected individually stale plan: %+v", marked)
	}

	direct, err := store.CreateRun(ctx, Run{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if direct.ExecutionMode != RunExecutionModeExecute || direct.PlanID != "" || direct.PolicyGenerationSnapshot < 1 || direct.AgentGenerationSnapshot < 1 {
		t.Fatalf("ordinary run should receive safe execute defaults: %+v", direct)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE runs SET execution_mode = 'invalid' WHERE id = ?`, direct.ID); err == nil {
		t.Fatal("fresh schema must enforce run execution mode")
	}
	snapshot, err := store.ReadAgentLiveSnapshot(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestPlan == nil {
		t.Fatal("live snapshot must include latest plan")
	}
}

func TestPlanStorePersistsPlanModeDraftAndReviewWithoutApproval(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "plan-store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Plan Store", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	planRun, err := store.CreateRun(ctx, Run{AgentID: agent.ID, ExecutionMode: RunExecutionModePlan})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistPlanDraft(ctx, planRun.ID, review.PlanDraft{Goal: "Investigate", Assumptions: []string{"clean tree"}, Steps: []string{"read"}, Risks: []string{"unknown"}, Tests: []string{"go test"}, Rollback: []string{"none"}}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.GetPlanBySourceRun(ctx, planRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AgentID != agent.ID || plan.SourceRunID != planRun.ID || plan.Status != PlanStatusDraft || plan.Summary != "Investigate" {
		t.Fatalf("unexpected persisted draft: %+v", plan)
	}
	if err := store.TriggerPlanReview(ctx, planRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.TriggerPlanReview(ctx, planRun.ID); err != nil {
		t.Fatalf("trigger review should be idempotent: %v", err)
	}
	if err := store.PersistPlanReview(ctx, planRun.ID, "", review.Result{Verdict: review.VerdictPass, Reason: "looks bounded"}); err == nil {
		t.Fatal("plan review must require a durable reviewer identity")
	}
	if err := store.PersistPlanReview(ctx, planRun.ID, "model:reviewer-v1", review.Result{Verdict: review.VerdictPass, Reason: "looks bounded"}); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetPlanBySourceRun(ctx, planRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != PlanStatusInReview {
		t.Fatalf("reviewer verdict must not approve execution: %+v", current)
	}
	reviews, err := store.ListPlanReviews(ctx, agent.ID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].Decision != PlanReviewDecisionApproved || reviews[0].ReviewerID != "model:reviewer-v1" {
		t.Fatalf("unexpected persisted reviewer result: %+v", reviews)
	}
	if _, err := store.CreateRunForPlan(ctx, current.ID, Run{}); !IsConflict(err) {
		t.Fatalf("unapproved plan must not create an execution run, got %v", err)
	}
	if err := store.PersistPlanDraft(ctx, planRun.ID, review.PlanDraft{Goal: "Changed", Steps: []string{"read"}}); !IsConflict(err) {
		t.Fatalf("reviewing draft must not be replaced, got %v", err)
	}

	executeRun, err := store.CreateRun(ctx, Run{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistPlanDraft(ctx, executeRun.ID, review.PlanDraft{Goal: "wrong mode"}); !IsConflict(err) {
		t.Fatalf("execute-mode run must not persist a plan draft, got %v", err)
	}
}

func TestRecoverInterruptedRunRestoresExecutingPlanAfterRestartIdempotently(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery-plans.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Recovery Plans", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	plan, err := store.CreatePlan(ctx, Plan{AgentID: agent.ID, ContentJSON: []byte(`{"goal":"recover execution"}`)})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	inReview, err := store.TransitionPlanStatus(ctx, agent.ID, plan.ID, plan.Revision, PlanStatusInReview)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.CreatePlanApproval(ctx, PlanApproval{PlanID: plan.ID, PlanRevision: inReview.Revision, ApproverID: "owner-1", Decision: PlanApprovalDecisionApproved}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	run, err := store.CreateRunForPlan(ctx, plan.ID, Run{Status: "running"})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecoverInterruptedRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.GetRun(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "interrupted" || recovered.ErrorMessage != "process restarted" {
		t.Fatalf("unexpected recovered run: %+v", recovered)
	}
	restored, err := store.GetPlan(ctx, agent.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != PlanStatusApproved {
		t.Fatalf("interrupted execution must return plan to approved: %+v", restored)
	}
	if err := store.RecoverInterruptedRun(ctx, run.ID); err != nil {
		t.Fatalf("recovery retry must be idempotent: %v", err)
	}
	stillApproved, err := store.GetPlan(ctx, agent.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillApproved.Status != PlanStatusApproved {
		t.Fatalf("recovery retry changed restored plan: %+v", stillApproved)
	}

	terminal, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverInterruptedRun(ctx, terminal.ID); err == nil {
		t.Fatal("completed run must not be recovered")
	}
}

func testNamedIndexExists(t *testing.T, ctx context.Context, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) bool {
	t.Helper()
	var count int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func TestListPlanReviewsForAgentReturnsReviewsAcrossPlans(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "plan-reviews-agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Reviews Agent", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, other, err := store.CreateProject(ctx, "Other Agent", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	planA, err := store.CreatePlan(ctx, Plan{AgentID: agent.ID, ContentJSON: []byte(`{"goal":"a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	planB, err := store.CreatePlan(ctx, Plan{AgentID: agent.ID, ContentJSON: []byte(`{"goal":"b"}`)})
	if err != nil {
		t.Fatal(err)
	}
	otherPlan, err := store.CreatePlan(ctx, Plan{AgentID: other.ID, ContentJSON: []byte(`{"goal":"other"}`)})
	if err != nil {
		t.Fatal(err)
	}
	planA, err = store.TransitionPlanStatus(ctx, agent.ID, planA.ID, planA.Revision, PlanStatusInReview)
	if err != nil {
		t.Fatal(err)
	}
	planB, err = store.TransitionPlanStatus(ctx, agent.ID, planB.ID, planB.Revision, PlanStatusInReview)
	if err != nil {
		t.Fatal(err)
	}
	otherPlan, err = store.TransitionPlanStatus(ctx, other.ID, otherPlan.ID, otherPlan.Revision, PlanStatusInReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlanReview(ctx, PlanReview{PlanID: planA.ID, PlanRevision: planA.Revision, ReviewerID: "reviewer-a", Decision: PlanReviewDecisionApproved, Comment: "review-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlanReview(ctx, PlanReview{PlanID: planB.ID, PlanRevision: planB.Revision, ReviewerID: "reviewer-b", Decision: PlanReviewDecisionChangesRequested, Comment: "review-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePlanReview(ctx, PlanReview{PlanID: otherPlan.ID, PlanRevision: otherPlan.Revision, ReviewerID: "reviewer-other", Decision: PlanReviewDecisionComment, Comment: "review-other"}); err != nil {
		t.Fatal(err)
	}

	reviews, err := store.ListPlanReviewsForAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 {
		t.Fatalf("expected reviews for both plans in one list, got %+v", reviews)
	}
	if reviews[0].PlanID != planA.ID || reviews[0].Comment != "review-a" || reviews[1].PlanID != planB.ID || reviews[1].Comment != "review-b" {
		t.Fatalf("expected agent reviews in created_at order, got %+v", reviews)
	}
	for _, review := range reviews {
		if review.PlanID == otherPlan.ID {
			t.Fatalf("other agent's review leaked into the list: %+v", reviews)
		}
	}
}
