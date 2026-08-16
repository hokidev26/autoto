package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/review"
	"autoto/internal/tools"
)

const planReflectionDraftJSON = `{"goal":"Inspect the requested location","assumptions":["workspace is readable"],"steps":["read the files"],"risks":["stale evidence"],"tests":["go test"],"rollback":["none"]}`

type planReflectionTestProvider struct {
	mu            sync.Mutex
	reviewCalls   int
	reviewResults []string
}

func (p *planReflectionTestProvider) Name() string { return "fake" }
func (p *planReflectionTestProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Tools: true, Streaming: true}
}
func (p *planReflectionTestProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test", "review"}, nil
}
func (p *planReflectionTestProvider) Generate(_ context.Context, request providers.GenerateRequest) (<-chan providers.Event, error) {
	out := make(chan providers.Event, 2)
	if strings.Contains(request.SystemPrompt, "isolated plan reviewer") {
		text := `{"verdict":"pass","reason":"viable"}`
		p.mu.Lock()
		if p.reviewCalls < len(p.reviewResults) {
			text = p.reviewResults[p.reviewCalls]
		}
		p.reviewCalls++
		p.mu.Unlock()
		out <- providers.Event{Type: "text", Text: text}
	} else {
		out <- providers.Event{Type: "text", Text: planReflectionDraftJSON}
	}
	out <- providers.Event{Type: "done", Done: true, StopReason: "end_turn"}
	close(out)
	return out, nil
}

func newPlanReflectionRunner(t *testing.T, store *db.Store, provider providers.Provider, reviewModel string) *Runner {
	t.Helper()
	registry := providers.NewRegistry()
	registry.Register(provider)
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	runner := NewRunner(store, registry, toolRegistry, NewHub(), config.AgentConfig{ReviewModel: reviewModel, MaxTurns: 2})
	if strings.TrimSpace(reviewModel) != "" {
		runner.SetReviewService(review.NewService(registry, reviewModel))
	}
	return runner
}

func TestShouldAutoReflectPlanOneRetryGate(t *testing.T) {
	original := db.Run{ExecutionMode: db.RunExecutionModePlan}
	if !shouldAutoReflectPlan(original, review.VerdictNeedsHuman) {
		t.Fatal("untagged plan-mode needs_human should auto-reflect")
	}
	for _, verdict := range []review.ReviewVerdict{review.VerdictPass, review.VerdictUnavailable, review.VerdictBlockRecommended} {
		if shouldAutoReflectPlan(original, verdict) {
			t.Fatalf("verdict %s must not auto-reflect", verdict)
		}
	}
	tagged := db.Run{ExecutionMode: db.RunExecutionModePlan, SourceID: PlanReflectionSourcePrefix + "plan-1"}
	if shouldAutoReflectPlan(tagged, review.VerdictNeedsHuman) {
		t.Fatal("plan-reflection source id must not start a second retry")
	}
	if shouldAutoReflectPlan(db.Run{ExecutionMode: db.RunExecutionModeExecute}, review.VerdictNeedsHuman) {
		t.Fatal("execute-mode runs must not auto-reflect")
	}
}

func TestNeedsHumanStartsOnePlanModeReflectionReplan(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &planReflectionTestProvider{reviewResults: []string{
		`{"verdict":"needs_human","reason":"evidence is for the wrong city; fetch the requested location"}`,
		`{"verdict":"pass","reason":"viable"}`,
	}}
	runner := newPlanReflectionRunner(t, store, provider, "fake:review")

	message, err := runner.SubmitUserMessageWithMode(ctx, agent.ID, "produce a plan", "api", ExecutionModePlan)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(8 * time.Second)
	var runs []db.Run
	var plans []db.Plan
	for {
		listedRuns, runErr := store.ListRuns(ctx, agent.ID, 10)
		listedPlans, planErr := store.ListPlans(ctx, agent.ID, 10)
		if runErr == nil && planErr == nil {
			runs, plans = listedRuns, listedPlans
			if !runner.IsAgentRunning(agent.ID) && len(runs) == 2 && len(plans) == 2 {
				first, second := reflectionRunPair(runs, message.RunID)
				if first.ID != "" && first.Status == "completed" && second.Status == "completed" &&
					strings.HasPrefix(second.SourceID, PlanReflectionSourcePrefix) &&
					second.ExecutionMode == db.RunExecutionModePlan && strings.TrimSpace(second.PlanID) == "" &&
					planBySource(plans, first.ID).Status == db.PlanStatusCancelled &&
					planBySource(plans, second.ID).Status == db.PlanStatusInReview {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for plan reflection replan: runs=%+v plans=%+v running=%v", runs, plans, runner.IsAgentRunning(agent.ID))
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, run := range runs {
		if run.ExecutionMode != db.RunExecutionModePlan || strings.TrimSpace(run.PlanID) != "" {
			t.Fatalf("reflection must not create an execute run: %+v", run)
		}
	}
	_, reflectionRun := reflectionRunPair(runs, message.RunID)
	if reflectionRun.Source != db.RunSourceManual || reflectionRun.TriggerType != "internal" {
		t.Fatalf("reflection run must be attended internal: %+v", reflectionRun)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundPrompt := false
	for _, item := range messages {
		if strings.HasPrefix(item.ContentText, PlanReflectionPromptPrefix) {
			foundPrompt = true
			if !strings.Contains(item.ContentText, "wrong city") || !strings.Contains(item.ContentText, "Previous plan JSON:") {
				t.Fatalf("reflection prompt missing reviewer correction: %q", item.ContentText)
			}
		}
	}
	if !foundPrompt {
		t.Fatal("expected a PLAN_REFLECTION_REPLAN trigger message")
	}
}

func TestUnavailableReviewDoesNotAutoReplan(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newPlanReflectionRunner(t, store, &planReflectionTestProvider{}, "")

	message, err := runner.SubmitUserMessageWithMode(ctx, agent.ID, "produce a plan", "api", ExecutionModePlan)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunSettled(t, store, runner, agent.ID, message.RunID)

	runs, err := store.ListRuns(ctx, agent.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("unavailable review must not start a reflection run: runs=%+v err=%v", runs, err)
	}
	if strings.HasPrefix(runs[0].SourceID, PlanReflectionSourcePrefix) {
		t.Fatalf("unavailable review created a reflection source id: %+v", runs[0])
	}
	plans, err := store.ListPlans(ctx, agent.ID, 10)
	if err != nil || len(plans) != 1 || plans[0].Status != db.PlanStatusInReview {
		t.Fatalf("unavailable review must leave the plan in_review: %+v err=%v", plans, err)
	}
}

func TestReviewerPassDoesNotAutoReplanOrAuthorizeExecute(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newPlanReflectionRunner(t, store, &planReflectionTestProvider{}, "fake:review")

	message, err := runner.SubmitUserMessageWithMode(ctx, agent.ID, "produce a plan", "api", ExecutionModePlan)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunSettled(t, store, runner, agent.ID, message.RunID)

	runs, err := store.ListRuns(ctx, agent.ID, 10)
	if err != nil || len(runs) != 1 || strings.HasPrefix(runs[0].SourceID, PlanReflectionSourcePrefix) {
		t.Fatalf("reviewer pass must not start a reflection run: runs=%+v err=%v", runs, err)
	}
	plans, err := store.ListPlans(ctx, agent.ID, 10)
	if err != nil || len(plans) != 1 || plans[0].Status != db.PlanStatusInReview {
		t.Fatalf("reviewer pass must leave the plan in_review: %+v err=%v", plans, err)
	}
	if _, err := store.CreateRunForPlan(ctx, plans[0].ID, db.Run{Status: "pending"}); err == nil {
		t.Fatal("reviewer pass must not authorize CreateRunForPlan")
	}
}

func TestStartPlanReflectionReplanSkipsTaggedRun(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := NewRunner(store, nil, nil, NewHub(), config.AgentConfig{})

	source, err := store.CreateRun(ctx, db.Run{
		AgentID: agent.ID, Status: "completed", ExecutionMode: db.RunExecutionModePlan,
		Source: db.RunSourceManual, TriggerType: "internal", SourceID: PlanReflectionSourcePrefix + "prior-plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistPlanDraft(ctx, source.ID, review.PlanDraft{
		Goal: "Inspect", Assumptions: []string{"readable"}, Steps: []string{"read"}, Risks: []string{"stale"}, Tests: []string{"go test"}, Rollback: []string{"none"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TriggerPlanReview(ctx, source.ID); err != nil {
		t.Fatal(err)
	}

	started, err := runner.startPlanReflectionReplan(ctx, agent.ID, source.ID, review.Result{
		Verdict: review.VerdictNeedsHuman, Reason: "evidence is for the wrong city; fetch the requested location",
	})
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("tagged plan-reflection run must not start another replan")
	}
	runs, err := store.ListRuns(ctx, agent.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected no third run, got %+v err=%v", runs, err)
	}
	plan, err := store.GetPlanBySourceRun(ctx, source.ID)
	if err != nil || plan.Status != db.PlanStatusInReview {
		t.Fatalf("tagged needs_human must keep the plan in_review: %+v err=%v", plan, err)
	}
}

func reflectionRunPair(runs []db.Run, firstRunID string) (db.Run, db.Run) {
	var first, second db.Run
	for _, run := range runs {
		if run.ID == firstRunID {
			first = run
			continue
		}
		second = run
	}
	return first, second
}

func planBySource(plans []db.Plan, sourceRunID string) db.Plan {
	for _, plan := range plans {
		if plan.SourceRunID == sourceRunID {
			return plan
		}
	}
	return db.Plan{}
}
