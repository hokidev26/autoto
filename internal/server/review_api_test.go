package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func newReviewAPITestServer(t *testing.T) (*db.Store, *Server, string, db.Agent) {
	t.Helper()
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "initial\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Review", "", repo, "fake:test", "acceptEdits")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	app := New(config.Config{
		Paths: config.PathsConfig{DefaultProjectDir: filepath.Dir(repo)},
		Agent: config.AgentConfig{ReviewModel: "fake:review"},
	}, store, nil, nil)
	return store, app, repo, agent
}

func reviewWorkspaceFingerprintForTest(t *testing.T, app *Server, agent db.Agent) string {
	t.Helper()
	fingerprint, err := app.reviewWorkspaceFingerprint(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func TestReviewPlanAPIsPersistApprovalAndMarkStale(t *testing.T) {
	store, app, repo, agent := newReviewAPITestServer(t)
	defer store.Close()

	invalid := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/plans", strings.NewReader(`{"summary":"unsafe partial","content":{"goal":"update README","steps":["edit README"]}}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("expected strict plan schema rejection, got %d: %s", invalid.Code, invalid.Body.String())
	}

	create := httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/plans", strings.NewReader(`{"summary":"Safely update README","content":{"goal":"update README","assumptions":[],"steps":["edit README"],"risks":[],"tests":["go test"],"rollback":[]}}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(create, request)
	if create.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", create.Code, create.Body.String())
	}
	var created reviewPlanSummary
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	plan, err := store.GetPlan(context.Background(), agent.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != db.PlanStatusInReview || plan.Revision != 1 || plan.ToolCatalogDigest == "" || plan.WorkspaceFingerprint == "" || created.ReviewVerdict == "" {
		t.Fatalf("unexpected persisted plan: plan=%+v response=%+v", plan, created)
	}

	list := httptest.NewRecorder()
	app.Routes().ServeHTTP(list, newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/plans", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), "contentJson") {
		t.Fatalf("plan list must not transport full plan content: %s", list.Body.String())
	}
	var summaries []reviewPlanSummary
	if err := json.NewDecoder(list.Body).Decode(&summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != plan.ID || summaries[0].Status != db.PlanStatusInReview {
		t.Fatalf("unexpected plan summaries: %+v", summaries)
	}

	detail := httptest.NewRecorder()
	app.Routes().ServeHTTP(detail, newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/plans/"+plan.ID, nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("expected detail 200, got %d: %s", detail.Code, detail.Body.String())
	}
	var persisted db.PlanDetail
	if err := json.NewDecoder(detail.Body).Decode(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Plan.ID != plan.ID || len(persisted.Plan.ContentJSON) == 0 {
		t.Fatalf("detail did not return durable plan content: %+v", persisted)
	}

	approve := httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/plans/"+plan.ID+"/approve", strings.NewReader(`{"revision":1,"comment":"reviewed"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(approve, request)
	if approve.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d: %s", approve.Code, approve.Body.String())
	}
	approved, err := store.GetPlan(context.Background(), agent.ID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != db.PlanStatusApproved {
		t.Fatalf("expected approved plan, got %+v", approved)
	}

	writeGitTestFile(t, repo, "README.md", "changed\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "workspace changed")

	execute := httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/plans/"+plan.ID+"/execute", strings.NewReader(`{"revision":1}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(execute, request)
	if execute.Code != http.StatusConflict {
		t.Fatalf("expected stale execution conflict, got %d: %s", execute.Code, execute.Body.String())
	}
	var stale db.Plan
	if err := json.NewDecoder(execute.Body).Decode(&stale); err != nil {
		t.Fatal(err)
	}
	if stale.Status != db.PlanStatusStale || stale.StaleReason == "" {
		t.Fatalf("expected stale plan after workspace change, got %+v", stale)
	}
}

func TestReviewWorkspaceFingerprintTracksDirtyFileContent(t *testing.T) {
	store, app, repo, agent := newReviewAPITestServer(t)
	defer store.Close()

	writeGitTestFile(t, repo, "README.md", "first dirty version\n")
	first := reviewWorkspaceFingerprintForTest(t, app, agent)
	if status := strings.TrimSpace(runGitTestOutput(t, repo, "status", "--porcelain=v1")); status != "M README.md" {
		t.Fatalf("expected README to remain modified, got %q", status)
	}

	writeGitTestFile(t, repo, "README.md", "second dirty version\n")
	second := reviewWorkspaceFingerprintForTest(t, app, agent)
	if status := strings.TrimSpace(runGitTestOutput(t, repo, "status", "--porcelain=v1")); status != "M README.md" {
		t.Fatalf("expected README to remain modified, got %q", status)
	}
	if first == second {
		t.Fatal("expected modified tracked file content to change workspace fingerprint")
	}
}

func TestReviewWorkspaceFingerprintTracksStagedContentWithStableStatusAndWorktree(t *testing.T) {
	store, app, repo, agent := newReviewAPITestServer(t)
	defer store.Close()
	original := runGitTestOutput(t, repo, "show", "HEAD:README.md")

	writeGitTestFile(t, repo, "README.md", "first staged version\n")
	runGitTestCommand(t, repo, "add", "README.md")
	writeGitTestFile(t, repo, "README.md", original)
	first := reviewWorkspaceFingerprintForTest(t, app, agent)
	if status := strings.TrimSpace(runGitTestOutput(t, repo, "status", "--porcelain=v1")); status != "MM README.md" {
		t.Fatalf("expected stable staged/worktree status, got %q", status)
	}

	writeGitTestFile(t, repo, "README.md", "second staged version\n")
	runGitTestCommand(t, repo, "add", "README.md")
	writeGitTestFile(t, repo, "README.md", original)
	second := reviewWorkspaceFingerprintForTest(t, app, agent)
	if status := strings.TrimSpace(runGitTestOutput(t, repo, "status", "--porcelain=v1")); status != "MM README.md" {
		t.Fatalf("expected unchanged staged/worktree status, got %q", status)
	}
	if first == second {
		t.Fatal("expected staged content change to change workspace fingerprint")
	}
}

func TestReviewWorkspaceFingerprintTracksUntrackedFileContent(t *testing.T) {
	store, app, repo, agent := newReviewAPITestServer(t)
	defer store.Close()

	writeGitTestFile(t, repo, "scratch.txt", "first untracked version\n")
	first := reviewWorkspaceFingerprintForTest(t, app, agent)
	if status := strings.TrimSpace(runGitTestOutput(t, repo, "status", "--porcelain=v1")); status != "?? scratch.txt" {
		t.Fatalf("expected scratch file to remain untracked, got %q", status)
	}

	writeGitTestFile(t, repo, "scratch.txt", "second untracked version\n")
	second := reviewWorkspaceFingerprintForTest(t, app, agent)
	if status := strings.TrimSpace(runGitTestOutput(t, repo, "status", "--porcelain=v1")); status != "?? scratch.txt" {
		t.Fatalf("expected scratch file to remain untracked, got %q", status)
	}
	if first == second {
		t.Fatal("expected untracked file content to change workspace fingerprint")
	}
}

func TestReviewWorkspaceFingerprintTracksDeletedModeAndSymlinkEntries(t *testing.T) {
	t.Run("deleted", func(t *testing.T) {
		store, app, repo, agent := newReviewAPITestServer(t)
		defer store.Close()
		writeGitTestFile(t, repo, "delete.txt", "delete me\n")
		runGitTestCommand(t, repo, "add", "delete.txt")
		runGitTestCommand(t, repo, "commit", "-m", "add delete target")
		before := reviewWorkspaceFingerprintForTest(t, app, agent)
		if err := os.Remove(filepath.Join(repo, "delete.txt")); err != nil {
			t.Fatal(err)
		}
		after := reviewWorkspaceFingerprintForTest(t, app, agent)
		if before == after {
			t.Fatal("expected deletion to change workspace fingerprint")
		}
	})

	t.Run("mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not expose executable mode changes through os.Chmod")
		}
		store, app, repo, agent := newReviewAPITestServer(t)
		defer store.Close()
		runGitTestCommand(t, repo, "config", "core.fileMode", "true")
		before := reviewWorkspaceFingerprintForTest(t, app, agent)
		if err := os.Chmod(filepath.Join(repo, "README.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		after := reviewWorkspaceFingerprintForTest(t, app, agent)
		if before == after {
			t.Fatal("expected mode change to change workspace fingerprint")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		store, app, repo, agent := newReviewAPITestServer(t)
		defer store.Close()
		writeGitTestFile(t, repo, "entry", "regular file\n")
		runGitTestCommand(t, repo, "add", "entry")
		runGitTestCommand(t, repo, "commit", "-m", "add regular entry")
		before := reviewWorkspaceFingerprintForTest(t, app, agent)
		entryPath := filepath.Join(repo, "entry")
		if err := os.Remove(entryPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("README.md", entryPath); err != nil {
			t.Fatal(err)
		}
		after := reviewWorkspaceFingerprintForTest(t, app, agent)
		if before == after {
			t.Fatal("expected symlink change to change workspace fingerprint")
		}
	})
}

func TestReviewWorkspaceFingerprintRejectsPathAndTotalByteBudgetExhaustion(t *testing.T) {
	t.Run("path count", func(t *testing.T) {
		store, app, repo, agent := newReviewAPITestServer(t)
		defer store.Close()
		for index := 0; index <= reviewWorkspaceFingerprintMaxPaths; index++ {
			writeGitTestFile(t, repo, fmt.Sprintf("untracked/%03d.txt", index), "x")
		}
		if _, err := app.reviewWorkspaceFingerprint(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "path count exceeds") {
			t.Fatalf("expected path count limit rejection, got %v", err)
		}
	})

	t.Run("total bytes", func(t *testing.T) {
		store, app, repo, agent := newReviewAPITestServer(t)
		defer store.Close()
		fileBytes := int(reviewWorkspaceFingerprintMaxFileBytes/2) + 1
		fileCount := int(reviewWorkspaceFingerprintMaxTotalBytes/int64(fileBytes)) + 1
		contents := strings.Repeat("x", fileBytes)
		for index := 0; index < fileCount; index++ {
			writeGitTestFile(t, repo, fmt.Sprintf("large/%d.bin", index), contents)
		}
		if _, err := app.reviewWorkspaceFingerprint(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "total byte budget") {
			t.Fatalf("expected total byte limit rejection, got %v", err)
		}
	})
}

type planModeTestProvider struct{}

func (planModeTestProvider) Name() string { return "fake" }
func (planModeTestProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Tools: true, Streaming: true}
}
func (planModeTestProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test", "review"}, nil
}
func (planModeTestProvider) Generate(_ context.Context, request providers.GenerateRequest) (<-chan providers.Event, error) {
	out := make(chan providers.Event, 2)
	if strings.Contains(request.SystemPrompt, "isolated plan reviewer") {
		out <- providers.Event{Type: "text", Text: `{"verdict":"pass","reason":"safe"}`}
	} else {
		out <- providers.Event{Type: "text", Text: `{"goal":"inspect","assumptions":[],"steps":["read"],"risks":[],"tests":["go test"],"rollback":[]}`}
	}
	out <- providers.Event{Type: "done", Done: true, StopReason: "end_turn"}
	close(out)
	return out, nil
}

func TestConversationMessageContextFreezesReadOnlyRunCap(t *testing.T) {
	store, _, _, createdAgent := newReviewAPITestServer(t)
	defer store.Close()
	registry := providers.NewRegistry()
	registry.Register(planModeTestProvider{})
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	runner := agentpkg.NewRunner(store, registry, toolRegistry, agentpkg.NewHub(), config.AgentConfig{ReviewModel: "fake:review", MaxTurns: 2})
	app := New(config.Config{Agent: config.AgentConfig{ReviewModel: "fake:review"}}, store, runner, agentpkg.NewHub(), registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+createdAgent.ID+"/messages", strings.NewReader(`{"text":"summarize public information","mode":"plan","context":"conversation"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected conversation submission 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var message db.Message
	if err := json.NewDecoder(recorder.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(context.Background(), createdAgent.ID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Source != db.RunSourceConversation || run.PermissionModeCap != "readOnly" || run.ExecutionMode != db.RunExecutionModeExecute {
		t.Fatalf("conversation run did not freeze the chat-only boundary: %+v", run)
	}
	if run.CheckpointState != db.RunCheckpointNone || run.BaseHead != "" || run.CheckpointRepoRoot != "" || run.GitSnapshotAt != "" {
		t.Fatalf("conversation run must not create a git checkpoint: %+v", run)
	}
	if run.ToolCatalogDigest != "" || run.WorkspaceFingerprint != "" || run.AutoContinuationMode != "off" {
		t.Fatalf("conversation run must not freeze project workspace state: %+v", run)
	}
	persistedAgent, err := store.GetAgent(context.Background(), createdAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedAgent.PermissionMode != "acceptEdits" {
		t.Fatalf("conversation cap must not rewrite the project permission default: %+v", persistedAgent)
	}

	goalRecorder := httptest.NewRecorder()
	goalRequest := newTestRequest(http.MethodPost, "/api/agents/"+createdAgent.ID+"/messages", strings.NewReader(`{"text":"/goal change files","context":"conversation"}`))
	goalRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(goalRecorder, goalRequest)
	if goalRecorder.Code != http.StatusForbidden {
		t.Fatalf("conversation context must reject project goal commands, got %d: %s", goalRecorder.Code, goalRecorder.Body.String())
	}
}

func TestStandaloneConversationForcesConversationBoundaryRegardlessOfClientContext(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "standalone-message.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, createdAgent, err := store.CreateStandaloneConversation(ctx, "Standalone", "fake:test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET plan_mode = 1 WHERE id = ?`, createdAgent.ID); err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(planModeTestProvider{})
	hub := agentpkg.NewHub()
	runner := agentpkg.NewRunner(store, registry, tools.NewRegistry(), hub, config.AgentConfig{MaxTurns: 2})
	app := New(config.Config{}, store, runner, hub, registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+createdAgent.ID+"/messages", strings.NewReader(`{"text":"summarize public information","mode":"plan","context":"project"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected standalone submission 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var message db.Message
	if err := json.NewDecoder(recorder.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(ctx, createdAgent.ID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Source != db.RunSourceConversation || run.PermissionModeCap != "readOnly" || run.ExecutionMode != db.RunExecutionModeExecute || run.CheckpointState != db.RunCheckpointNone {
		t.Fatalf("standalone message escaped conversation boundary: %+v", run)
	}

	goalRecorder := httptest.NewRecorder()
	goalRequest := newTestRequest(http.MethodPost, "/api/agents/"+createdAgent.ID+"/messages", strings.NewReader(`{"text":"/goal change files","context":"project"}`))
	goalRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(goalRecorder, goalRequest)
	if goalRecorder.Code != http.StatusForbidden {
		t.Fatalf("standalone conversation accepted project goal command: %d %s", goalRecorder.Code, goalRecorder.Body.String())
	}
	waitForAgentIdle(t, store, createdAgent.ID)
}

func TestPlanMessageModeFreezesRunRestoresAgentDefaultAndPersistsReview(t *testing.T) {
	store, _, _, agent := newReviewAPITestServer(t)
	defer store.Close()
	registry := providers.NewRegistry()
	registry.Register(planModeTestProvider{})
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	runner := agentpkg.NewRunner(store, registry, toolRegistry, agentpkg.NewHub(), config.AgentConfig{ReviewModel: "fake:review", MaxTurns: 2})
	app := New(config.Config{Agent: config.AgentConfig{ReviewModel: "fake:review"}}, store, runner, agentpkg.NewHub(), registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/messages", strings.NewReader(`{"text":"produce a plan","mode":"plan"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected plan submission 202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var message db.Message
	if err := json.NewDecoder(recorder.Body).Decode(&message); err != nil {
		t.Fatal(err)
	}
	if recorder.Header().Get("X-Autoto-Run-Mode") != db.RunExecutionModePlan {
		t.Fatalf("expected frozen plan mode response header, got %q", recorder.Header().Get("X-Autoto-Run-Mode"))
	}
	run, err := store.GetRun(context.Background(), agent.ID, message.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionMode != db.RunExecutionModePlan {
		t.Fatalf("run did not freeze requested mode: %+v", run)
	}
	updatedAgent, err := store.GetAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedAgent.PlanMode {
		t.Fatalf("explicit plan mode must not rewrite the agent default: %+v", updatedAgent)
	}
	deadline := time.Now().Add(2 * time.Second)
	var plan db.Plan
	for {
		plans, listErr := store.ListPlans(context.Background(), agent.ID, 10)
		completedRun, runErr := store.GetRun(context.Background(), agent.ID, message.RunID)
		if listErr == nil && runErr == nil && completedRun.Status == "completed" && len(plans) == 1 {
			detail, detailErr := store.GetPlanDetail(context.Background(), agent.ID, plans[0].ID)
			if detailErr == nil && len(detail.Reviews) == 1 && detail.Reviews[0].Decision == db.PlanReviewDecisionApproved {
				plan = plans[0]
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for persisted plan review")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if plan.ToolCatalogDigest == "" || plan.WorkspaceFingerprint == "" || plan.PolicyGenerationSnapshot < 1 || plan.AgentGenerationSnapshot < 1 {
		t.Fatalf("plan run did not persist a complete safety snapshot: %+v", plan)
	}

	approve := httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/plans/"+plan.ID+"/approve", strings.NewReader(fmt.Sprintf(`{"revision":%d}`, plan.Revision)))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(approve, request)
	if approve.Code != http.StatusOK {
		t.Fatalf("expected plan approval 200, got %d: %s", approve.Code, approve.Body.String())
	}

	execute := httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/plans/"+plan.ID+"/execute", strings.NewReader(fmt.Sprintf(`{"revision":%d}`, plan.Revision)))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(execute, request)
	if execute.Code != http.StatusAccepted {
		t.Fatalf("expected approved plan execution 202, got %d: %s", execute.Code, execute.Body.String())
	}
	var executionResponse struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&executionResponse); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for {
		executed, getErr := store.GetPlan(context.Background(), agent.ID, plan.ID)
		executionRun, runErr := store.GetRun(context.Background(), agent.ID, executionResponse.RunID)
		currentAgent, agentErr := store.GetAgent(context.Background(), agent.ID)
		if getErr == nil && runErr == nil && agentErr == nil && executed.Status == db.PlanStatusExecuted && executionRun.Status == "completed" && currentAgent.Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for approved plan execution: plan=%+v planErr=%v run=%+v runErr=%v agent=%+v agentErr=%v", executed, getErr, executionRun, runErr, currentAgent, agentErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReplanCancelsOldPlanAndStartsNewPlanRun(t *testing.T) {
	store, _, repo, agent := newReviewAPITestServer(t)
	defer store.Close()
	registry := providers.NewRegistry()
	registry.Register(planModeTestProvider{})
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	runner := agentpkg.NewRunner(store, registry, toolRegistry, agentpkg.NewHub(), config.AgentConfig{ReviewModel: "fake:review", MaxTurns: 2})
	app := New(config.Config{Paths: config.PathsConfig{DefaultProjectDir: filepath.Dir(repo)}, Agent: config.AgentConfig{ReviewModel: "fake:review"}}, store, runner, agentpkg.NewHub(), registry)

	submit := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/messages", strings.NewReader(`{"text":"produce a plan","mode":"plan"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(submit, request)
	if submit.Code != http.StatusAccepted {
		t.Fatalf("expected initial plan submission 202, got %d: %s", submit.Code, submit.Body.String())
	}
	var initialMessage db.Message
	if err := json.NewDecoder(submit.Body).Decode(&initialMessage); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var oldPlan db.Plan
	for {
		plans, listErr := store.ListPlans(context.Background(), agent.ID, 10)
		run, runErr := store.GetRun(context.Background(), agent.ID, initialMessage.RunID)
		if listErr == nil && runErr == nil && run.Status == "completed" && len(plans) == 1 && plans[0].Status == db.PlanStatusInReview {
			oldPlan = plans[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for initial plan: plans=%+v listErr=%v runErr=%v", plans, listErr, runErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	const replanFeedback = "step 2 touches production data; add a dry-run step first"
	replan := httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/plans/"+oldPlan.ID+"/replan", strings.NewReader(fmt.Sprintf(`{"revision":%d,"comment":%q}`, oldPlan.Revision, replanFeedback)))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(replan, request)
	if replan.Code != http.StatusAccepted {
		t.Fatalf("expected replan 202, got %d: %s", replan.Code, replan.Body.String())
	}
	var replanResponse struct {
		RunID   string     `json:"runId"`
		Message db.Message `json:"message"`
	}
	if err := json.NewDecoder(replan.Body).Decode(&replanResponse); err != nil {
		t.Fatal(err)
	}
	// The reviewer's feedback must reach the replan prompt, otherwise the next
	// revision cannot know why the previous one was rejected.
	if !strings.Contains(replanResponse.Message.ContentText, replanFeedback) {
		t.Fatalf("replan prompt does not carry the reviewer feedback: %q", replanResponse.Message.ContentText)
	}
	cancelled, err := store.GetPlan(context.Background(), agent.ID, oldPlan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != db.PlanStatusCancelled {
		t.Fatalf("old plan was not cancelled before replanning: %+v", cancelled)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		plans, listErr := store.ListPlans(context.Background(), agent.ID, 10)
		run, runErr := store.GetRun(context.Background(), agent.ID, replanResponse.RunID)
		currentAgent, agentErr := store.GetAgent(context.Background(), agent.ID)
		if listErr == nil && runErr == nil && agentErr == nil && run.Status == "completed" && currentAgent.Status == "idle" && len(plans) == 2 && plans[0].ID != oldPlan.ID && plans[0].Status == db.PlanStatusInReview {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for replacement plan: plans=%+v listErr=%v run=%+v runErr=%v agent=%+v agentErr=%v", plans, listErr, run, runErr, currentAgent, agentErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProjectAndAgentDefaultPlanModeAndPatch(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plan-mode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := t.TempDir()
	app := New(config.Config{
		Paths: config.PathsConfig{DefaultProjectDir: workspace},
		Agent: config.AgentConfig{DefaultModel: "fake:test", DefaultPermissionMode: "acceptEdits", DefaultStartInPlanMode: true},
	}, store, nil, nil)

	projectPayload, err := json.Marshal(map[string]string{"name": "Plan default", "gitPath": workspace})
	if err != nil {
		t.Fatal(err)
	}
	projectRecorder := httptest.NewRecorder()
	projectRequest := newTestRequest(http.MethodPost, "/api/projects", strings.NewReader(string(projectPayload)))
	projectRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(projectRecorder, projectRequest)
	if projectRecorder.Code != http.StatusCreated {
		t.Fatalf("expected project create 201, got %d: %s", projectRecorder.Code, projectRecorder.Body.String())
	}
	var projectResponse struct {
		Agent db.Agent `json:"agent"`
	}
	if err := json.NewDecoder(projectRecorder.Body).Decode(&projectResponse); err != nil {
		t.Fatal(err)
	}
	if !projectResponse.Agent.PlanMode {
		t.Fatalf("project primary agent did not inherit default plan mode: %+v", projectResponse.Agent)
	}

	agentPayload, err := json.Marshal(map[string]string{"title": "Secondary", "model": "fake:test", "permissionMode": "acceptEdits", "cwd": workspace})
	if err != nil {
		t.Fatal(err)
	}
	createAgent := httptest.NewRecorder()
	agentRequest := newTestRequest(http.MethodPost, "/api/agents", strings.NewReader(string(agentPayload)))
	agentRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(createAgent, agentRequest)
	if createAgent.Code != http.StatusCreated {
		t.Fatalf("expected agent create 201, got %d: %s", createAgent.Code, createAgent.Body.String())
	}
	var created db.Agent
	if err := json.NewDecoder(createAgent.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !created.PlanMode {
		t.Fatalf("agent did not inherit default plan mode: %+v", created)
	}

	patch := httptest.NewRecorder()
	patchRequest := newTestRequest(http.MethodPatch, "/api/agents/"+created.ID+"/plan-mode", strings.NewReader(`{"planMode":false}`))
	patchRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(patch, patchRequest)
	if patch.Code != http.StatusOK {
		t.Fatalf("expected plan mode patch 200, got %d: %s", patch.Code, patch.Body.String())
	}
	var updated db.Agent
	if err := json.NewDecoder(patch.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.PlanMode || updated.EntityGeneration <= created.EntityGeneration || updated.PermissionGeneration <= created.PermissionGeneration {
		t.Fatalf("plan mode patch did not update security generations: before=%+v after=%+v", created, updated)
	}

	if _, err := store.GetAgent(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
}
