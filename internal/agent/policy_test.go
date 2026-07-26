package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/review"
	"autoto/internal/tools"
)

// These fixtures define the model-visible product contract independently from
// the production allowlists while keeping repeated policy tests consistent.
var (
	expectedPlanToolSurface = []string{
		"AskUserQuestion",
		"ContextAsk",
		"EndPipeline",
		"Glob",
		"Grep",
		// LS and TodoWrite are read-only: a plan run must be able to list a
		// directory and record the plan it is producing.
		"LS",
		"Read",
		"StartPipeline",
		"TodoWrite",
		"WebFetch",
		"WebSearch",
	}
	expectedConversationToolSurface = []string{"WebFetch", "WebSearch", "AskUserQuestion"}
)

func toolSpecNames(specs []providers.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func sortedToolSpecNames(specs []providers.ToolSpec) []string {
	names := toolSpecNames(specs)
	sort.Strings(names)
	return names
}

func TestPlanPolicyOnlyExposesCoreReadAllowlist(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	runner := &Runner{tools: registry}
	policy := PolicyContext{ExecutionMode: ExecutionModePlan}
	snapshot, err := runner.snapshotToolsForPolicy(context.Background(), tools.ResolutionContext{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	names := sortedToolSpecNames(snapshot.specs)
	if !reflect.DeepEqual(names, expectedPlanToolSurface) {
		t.Fatalf("unexpected plan tool surface: got=%v want=%v", names, expectedPlanToolSurface)
	}
	for _, hidden := range []string{"Write", "Edit", "Bash", "MCPListTools", "MCPCallTool"} {
		if _, ok := snapshot.tools[hidden]; !ok {
			t.Fatalf("full snapshot must retain %s for final gateway classification", hidden)
		}
	}
}

func TestConversationPolicyOnlyExposesPublicResearchTools(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	runner := &Runner{tools: registry}
	policy := PolicyContext{ExecutionMode: ExecutionModeExecute, PermissionMode: "readOnly", Conversation: true}
	snapshot, err := runner.snapshotToolsForPolicy(context.Background(), tools.ResolutionContext{AgentID: "agent-1", CWD: "/project"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	names := toolSpecNames(snapshot.specs)
	if !reflect.DeepEqual(names, expectedConversationToolSurface) {
		t.Fatalf("unexpected conversation tool surface: got=%v want=%v", names, expectedConversationToolSurface)
	}
	if len(snapshot.tools) != len(expectedConversationToolSurface) {
		t.Fatalf("conversation snapshot retained project tools: %v", snapshot.tools)
	}
	for _, name := range expectedConversationToolSurface {
		if _, ok := snapshot.tools[name]; !ok {
			t.Fatalf("conversation snapshot omitted %s", name)
		}
		if result, denied := planToolDeniedResult(policy, tools.Call{Name: name}, tools.RiskRead); denied || result.IsError {
			t.Fatalf("conversation gateway rejected allowed tool %s: %+v denied=%v", name, result, denied)
		}
	}
	for _, denied := range []struct {
		name string
		risk tools.Risk
	}{
		{name: "Read", risk: tools.RiskRead},
		{name: "Glob", risk: tools.RiskRead},
		{name: "Bash", risk: tools.RiskExec},
		{name: "MCPListTools", risk: tools.RiskRead},
	} {
		result, blocked := planToolDeniedResult(policy, tools.Call{Name: denied.name}, denied.risk)
		if !blocked || !result.IsError || !strings.Contains(result.Output, "conversation context") {
			t.Fatalf("conversation gateway allowed %s/%s: %+v blocked=%v", denied.name, denied.risk, result, blocked)
		}
	}
}

func TestPipelineToolsOnlyAppearForReadOnlyOrPlanPolicies(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	runner := &Runner{tools: registry}
	for _, test := range []struct {
		name      string
		policy    PolicyContext
		wantShown bool
	}{
		{name: "readOnly execute", policy: PolicyContext{ExecutionMode: ExecutionModeExecute, PermissionMode: "readOnly"}, wantShown: true},
		{name: "plan", policy: PolicyContext{ExecutionMode: ExecutionModePlan, PermissionMode: "acceptEdits"}, wantShown: true},
		{name: "accept edits", policy: PolicyContext{ExecutionMode: ExecutionModeExecute, PermissionMode: "acceptEdits"}, wantShown: false},
		{name: "bypass", policy: PolicyContext{ExecutionMode: ExecutionModeExecute, PermissionMode: "bypassPermissions"}, wantShown: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := runner.snapshotToolsForPolicy(context.Background(), tools.ResolutionContext{}, test.policy)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, spec := range snapshot.specs {
				seen[spec.Name] = true
			}
			for _, name := range []string{"StartPipeline", "EndPipeline"} {
				if seen[name] != test.wantShown {
					t.Fatalf("%s visibility=%v want=%v", name, seen[name], test.wantShown)
				}
				if _, retained := snapshot.tools[name]; !retained {
					t.Fatalf("full snapshot did not retain %s", name)
				}
			}
		})
	}
}

func TestPlanPolicyFinalGatewayRejectsWriteExecDangerMCPAndPlugin(t *testing.T) {
	policy := PolicyContext{ExecutionMode: ExecutionModePlan}
	for _, test := range []struct {
		name string
		risk tools.Risk
	}{
		{name: "Write", risk: tools.RiskWrite},
		{name: "Bash", risk: tools.RiskExec},
		{name: "MCPCallTool", risk: tools.RiskExec},
		{name: "plugin__demo__read", risk: tools.RiskRead},
		{name: "Bash", risk: tools.RiskDanger},
	} {
		t.Run(test.name+"/"+string(test.risk), func(t *testing.T) {
			result, denied := planToolDeniedResult(policy, tools.Call{Name: test.name}, test.risk)
			if !denied || !result.IsError || !strings.Contains(result.Output, "plan execution mode") {
				t.Fatalf("plan gateway allowed %s/%s: %+v denied=%v", test.name, test.risk, result, denied)
			}
		})
	}
	for _, name := range expectedPlanToolSurface {
		if result, denied := planToolDeniedResult(policy, tools.Call{Name: name}, tools.RiskRead); denied || result.IsError {
			t.Fatalf("plan gateway rejected allowed tool %s: %+v denied=%v", name, result, denied)
		}
	}
}

func TestExecuteToolDirectlyRespectsPlanMode(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET plan_mode = 1 WHERE id = ?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	runner := NewRunner(store, nil, registry, NewHub(), config.AgentConfig{})
	result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{Name: "Write", Input: json.RawMessage(`{"file_path":"blocked.txt","content":"no"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, "plan execution mode") {
		t.Fatalf("direct execution bypassed plan policy: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("plan-mode direct write changed filesystem: %v", err)
	}
}

func TestExecuteToolForRunUsesDurablePlanMode(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	runner := NewRunner(store, nil, registry, NewHub(), config.AgentConfig{})
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running", ExecutionMode: db.RunExecutionModePlan})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.ExecuteToolForRun(ctx, agent.ID, run.ID, tools.Call{Name: "Write", Input: json.RawMessage(`{"file_path":"blocked.txt","content":"no"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, "plan execution mode") {
		t.Fatalf("durable plan run bypassed policy: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("durable plan run changed filesystem: %v", err)
	}
}

func TestSubmitUserMessageWithModeFreezesRunWithoutMutatingAgentDefault(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: `{"goal":"Plan","assumptions":[],"steps":["inspect"],"risks":[],"tests":["go test"],"rollback":[]}`},
		{Type: "done", Done: true},
	}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
	message, err := runner.SubmitUserMessageWithMode(ctx, agent.ID, "make a plan", "api", ExecutionModePlan)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(ctx, agent.ID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionMode != db.RunExecutionModePlan {
		t.Fatalf("explicit mode was not frozen on run: %+v", run)
	}
	persistedAgent, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedAgent.PlanMode {
		t.Fatalf("explicit run mode mutated agent default: %+v", persistedAgent)
	}
	waitForRunSettled(t, store, runner, agent.ID, message.RunID)
}

func TestPlanRunUsesDurableModeAndReadToolSurface(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET plan_mode = 1 WHERE id = ?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: `{"goal":"Plan","assumptions":["a"],"steps":["s"],"risks":["r"],"tests":["t"],"rollback":["rb"]}`},
		{Type: "done", Done: true},
	}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
	runner.Run(ctx, agent.ID)
	if provider.requestCount() != 1 {
		t.Fatalf("expected one plan model request, got %d", provider.requestCount())
	}
	names := sortedToolSpecNames(provider.request(0).Tools)
	if !reflect.DeepEqual(names, expectedPlanToolSurface) {
		t.Fatalf("unexpected plan tool specs: got=%v want=%v", names, expectedPlanToolSurface)
	}
	runs, err := store.ListRuns(ctx, agent.ID, 1)
	if err != nil || len(runs) != 1 || runs[0].ExecutionMode != db.RunExecutionModePlan || runs[0].Status != "completed" {
		t.Fatalf("plan run was not durably completed in plan mode: runs=%+v err=%v", runs, err)
	}
}

func TestExecutionModeForRunUsesDurableRunFieldAndFailsClosed(t *testing.T) {
	if got := executionModeForRun(db.Run{ExecutionMode: db.RunExecutionModeExecute}); got != ExecutionModeExecute {
		t.Fatalf("unexpected execute mode: %s", got)
	}
	if got := executionModeForRun(db.Run{ExecutionMode: db.RunExecutionModePlan}); got != ExecutionModePlan {
		t.Fatalf("unexpected plan mode: %s", got)
	}
	if got := executionModeForRun(db.Run{ExecutionMode: "invalid"}); got != ExecutionModePlan {
		t.Fatalf("unknown durable mode must fail closed: %s", got)
	}
}

func TestReviewerPassDoesNotApproveOrExecutePlan(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: `{"verdict":"pass","reason":"structured draft is complete"}`},
		{Type: "done", Done: true},
	}}}
	registry := providers.NewRegistry()
	registry.Register(provider)
	runner := NewRunner(store, registry, nil, NewHub(), config.AgentConfig{})
	runner.SetReviewService(review.NewService(registry, "fake:reviewer"))
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running", ExecutionMode: db.RunExecutionModePlan})
	if err != nil {
		t.Fatal(err)
	}
	_, result, err := runner.persistAndReviewPlan(ctx, PolicyContext{AgentID: agent.ID, RunID: run.ID, ExecutionMode: ExecutionModePlan}, `{"goal":"Implement review","assumptions":["store API exists"],"steps":["persist draft"],"risks":["bad output"],"tests":["go test"],"rollback":["revert"]}`)
	if err != nil || result.Verdict != review.VerdictPass {
		t.Fatalf("unexpected reviewer result=%+v err=%v", result, err)
	}
	plan, err := store.GetLatestPlan(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != db.PlanStatusInReview {
		t.Fatalf("reviewer pass must not approve plan: %+v", plan)
	}
	reviews, err := store.ListPlanReviews(ctx, agent.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].ReviewerID != "model:fake:reviewer" || reviews[0].Decision != db.PlanReviewDecisionApproved {
		t.Fatalf("expected configured reviewer identity without approval: %+v", reviews)
	}
	if _, err := store.CreateRunForPlan(ctx, plan.ID, db.Run{Status: "pending"}); err == nil {
		t.Fatal("reviewer pass must not authorize an execution run")
	}
}

func TestPersistAndReviewPlanUsesConcreteStoreWithoutApproval(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := NewRunner(store, nil, nil, NewHub(), config.AgentConfig{})
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running", ExecutionMode: db.RunExecutionModePlan})
	if err != nil {
		t.Fatal(err)
	}
	policy := PolicyContext{AgentID: agent.ID, RunID: run.ID, ExecutionMode: ExecutionModePlan}
	output := `{"goal":"Implement review","assumptions":["store API exists"],"steps":["persist draft"],"risks":["bad output"],"tests":["go test"],"rollback":["revert"]}`
	text, result, err := runner.persistAndReviewPlan(ctx, policy, output)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != review.VerdictUnavailable || !json.Valid([]byte(text)) {
		t.Fatalf("unexpected unavailable review result=%+v text=%s", result, text)
	}
	plan, err := store.GetLatestPlan(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var storedDraft review.PlanDraft
	if err := json.Unmarshal(plan.ContentJSON, &storedDraft); err != nil || plan.Status != db.PlanStatusInReview || plan.Summary != "Implement review" || storedDraft.Goal != "Implement review" {
		t.Fatalf("plan was not strictly persisted for review: plan=%+v draft=%+v err=%v", plan, storedDraft, err)
	}
	reviews, err := store.ListPlanReviews(ctx, agent.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].ReviewerID != "system:reviewer-unavailable" || reviews[0].Decision != db.PlanReviewDecisionComment || reviews[0].Comment != "review service is not configured" {
		t.Fatalf("expected unavailable review record, got %+v", reviews)
	}
	if _, _, err := runner.persistAndReviewPlan(ctx, policy, "not JSON"); err == nil {
		t.Fatal("expected strict plan draft failure")
	}
}
