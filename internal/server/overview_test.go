package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type overviewBlockingProvider struct {
	started chan struct{}
	once    sync.Once
}

func (p *overviewBlockingProvider) Name() string { return "fake" }
func (p *overviewBlockingProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}
func (p *overviewBlockingProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	p.once.Do(func() { close(p.started) })
	return make(chan providers.Event), nil
}

func TestOverviewReturnsSafeBoundedOrderedLocalSummary(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "overview-local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, workline, primary, err := store.CreateProject(ctx, "Overview", "PROJECT_DESCRIPTION_SECRET", t.TempDir(), "fake:overview", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	agents := []db.Agent{primary}
	for index := 1; index < 10; index++ {
		agent, createErr := store.CreateAgent(ctx, db.Agent{
			WorklineID: workline.ID, Type: "subagent", Title: fmt.Sprintf("Agent %02d", index),
			Model: "fake:model", SystemPrompt: "SYSTEM_PROMPT_SECRET", PermissionMode: "acceptEdits", CWD: "/secret/workspace/path",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		agents = append(agents, agent)
	}

	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	for index, agent := range agents {
		status := "idle"
		if index%2 == 0 {
			status = "running"
		}
		activity := base.Add(time.Duration(index) * time.Minute).Format(time.RFC3339Nano)
		if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET title = ?, status = ?, last_message_at = ?, system_prompt = 'SYSTEM_PROMPT_SECRET', context_summary = 'CONTEXT_SUMMARY_SECRET', error_message = 'AGENT_ERROR_SECRET', updated_at = ? WHERE id = ?`, fmt.Sprintf("Agent %02d", index), status, activity, activity, agent.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateRun(ctx, db.Run{
			ID: fmt.Sprintf("run-%02d", index), AgentID: agent.ID, Status: "running",
			StartedAt: activity, CreatedAt: activity, UpdatedAt: activity, ErrorMessage: "RUN_ERROR_SECRET",
		}); err != nil {
			t.Fatal(err)
		}
	}

	statuses := []string{"todo", "doing", "todo", "doing", "done", "todo", "doing", "todo", "doing", "todo", "done", "todo"}
	for index, status := range statuses {
		if _, err := store.CreateSpecTask(ctx, db.SpecTask{
			ID: fmt.Sprintf("task-%02d", index), AgentID: primary.ID, Text: fmt.Sprintf("Task %02d", index), Status: status,
		}); err != nil {
			t.Fatal(err)
		}
		updatedAt := base.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		if _, err := store.DB().ExecContext(ctx, `UPDATE spec_tasks SET updated_at = ? WHERE id = ?`, updatedAt, fmt.Sprintf("task-%02d", index)); err != nil {
			t.Fatal(err)
		}
	}

	for index := 0; index < 10; index++ {
		nextRunAt := base.Add(time.Duration(index+1) * time.Hour).Format(time.RFC3339Nano)
		outcome := "success"
		if index < 2 {
			outcome = "failure"
		}
		if _, err := store.CreateSchedule(ctx, db.Schedule{
			ID: fmt.Sprintf("schedule-%02d", index), Name: fmt.Sprintf("Schedule %02d", index), AgentID: agents[index].ID,
			Expression: "@daily", Timezone: "UTC", Prompt: "SCHEDULE_PROMPT_SECRET", PermissionMode: "readOnly",
			Enabled: true, NextRunAt: nextRunAt, LastOutcome: outcome, LastError: "SCHEDULE_ERROR_SECRET",
		}); err != nil {
			t.Fatal(err)
		}
	}
	insertOverviewPendingApproval(t, store, primary.ID, "approval-local-1", "TOOL_INPUT_SECRET")
	insertOverviewPendingApproval(t, store, primary.ID, "approval-local-2", "SECOND_TOOL_INPUT_SECRET")

	app := New(config.Config{}, store, nil, nil)
	app.clock = func() time.Time { return base.Add(90 * time.Minute) }
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/overview", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected overview 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, forbidden := range []string{
		"prompt", "instructions", "cwd", "secret", "systemPrompt", "contextSummary", "errorMessage",
		"SYSTEM_PROMPT_SECRET", "CONTEXT_SUMMARY_SECRET", "AGENT_ERROR_SECRET", "RUN_ERROR_SECRET",
		"SCHEDULE_PROMPT_SECRET", "SCHEDULE_ERROR_SECRET", "TOOL_INPUT_SECRET", "PROJECT_DESCRIPTION_SECRET", "/secret/workspace/path",
	} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("overview leaked %q: %s", forbidden, body)
		}
	}

	var response overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.CapturedAt != base.Add(90*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("unexpected capturedAt: %s", response.CapturedAt)
	}
	if response.Summary.Conversations != 10 || response.Summary.RunningAgents != 0 {
		t.Fatalf("persisted agent status must not be reported as realtime activity: %+v", response.Summary)
	}
	if response.Summary.Tasks != (overviewTaskSummary{Total: 12, Todo: 6, Doing: 4, Done: 2}) {
		t.Fatalf("unexpected task summary: %+v", response.Summary.Tasks)
	}
	if response.Summary.ActiveRuns != 0 || len(response.ActiveRuns) != 0 || response.Summary.PendingApprovals != 2 {
		t.Fatalf("durable run residue must not be reported as realtime activity: %+v runs=%+v", response.Summary, response.ActiveRuns)
	}
	if response.Summary.Schedules != (overviewScheduleSummary{Total: 10, Enabled: 10, Due: 1, Failed: 2}) {
		t.Fatalf("unexpected schedule summary: %+v", response.Summary.Schedules)
	}
	if len(response.RecentConversations) != overviewRecentConversationLimit || response.RecentConversations[0].Title != "Agent 09" {
		t.Fatalf("recent conversations were not bounded/sorted: %+v", response.RecentConversations)
	}
	if len(response.ActiveTasks) != overviewActiveTaskLimit {
		t.Fatalf("active tasks were not bounded: %+v", response.ActiveTasks)
	}
	for index, task := range response.ActiveTasks {
		if index < 4 && task.Status != "doing" {
			t.Fatalf("doing tasks must be first: %+v", response.ActiveTasks)
		}
		if task.Priority != "normal" || task.ProjectID != project.ID || task.AgentID != primary.ID {
			t.Fatalf("unexpected active task projection: %+v", task)
		}
	}
	if len(response.UpcomingSchedules) != overviewUpcomingScheduleLimit || response.UpcomingSchedules[0].ID != "schedule-01" || response.UpcomingSchedules[7].ID != "schedule-08" {
		t.Fatalf("upcoming schedules must exclude due items and remain bounded/sorted: %+v", response.UpcomingSchedules)
	}

	assertOverviewContractKeys(t, recorder.Body.Bytes())
}

func TestOverviewMatchesTaskWorkspaceAuthenticationAndFiltersMembership(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "overview-membership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)
	firstCookie := registerCollaborationTestUser(t, app, "overview-first")
	registerCollaborationTestUser(t, app, "overview-second")
	firstUser, _, err := store.GetUserByHandle(ctx, "overview-first")
	if err != nil {
		t.Fatal(err)
	}
	secondUser, _, err := store.GetUserByHandle(ctx, "overview-second")
	if err != nil {
		t.Fatal(err)
	}
	firstProject, _, firstAgent, err := store.CreateProjectForUser(ctx, firstUser.ID, "First project", "FIRST_DESCRIPTION_SECRET", t.TempDir(), "fake:first", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, _, secondAgent, err := store.CreateProjectForUser(ctx, secondUser.ID, "Second project", "SECOND_DESCRIPTION_SECRET", t.TempDir(), "fake:second", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSpecTask(ctx, db.SpecTask{ID: "first-task", AgentID: firstAgent.ID, Text: "First task", Status: "doing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSpecTask(ctx, db.SpecTask{ID: "second-task", AgentID: secondAgent.ID, Text: "Second private task", Status: "doing"}); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	for id, agentID := range map[string]string{"first-run": firstAgent.ID, "second-run": secondAgent.ID} {
		if _, err := store.CreateRun(ctx, db.Run{ID: id, AgentID: agentID, Status: "running", StartedAt: startedAt, CreatedAt: startedAt, UpdatedAt: startedAt}); err != nil {
			t.Fatal(err)
		}
	}
	nextRunAt := app.clock().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	for id, agentID := range map[string]string{"first-schedule": firstAgent.ID, "second-schedule": secondAgent.ID} {
		if _, err := store.CreateSchedule(ctx, db.Schedule{
			ID: id, Name: id, AgentID: agentID, Expression: "@daily", Timezone: "UTC", Prompt: "PRIVATE_SCHEDULE_PROMPT",
			PermissionMode: "readOnly", Enabled: true, NextRunAt: nextRunAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	insertOverviewPendingApproval(t, store, firstAgent.ID, "first-approval", "FIRST_APPROVAL_SECRET")
	insertOverviewPendingApproval(t, store, secondAgent.ID, "second-approval", "SECOND_APPROVAL_SECRET")

	unauthenticated := httptest.NewRecorder()
	app.Routes().ServeHTTP(unauthenticated, newTestRequest(http.MethodGet, "/api/overview", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("overview must match task workspace unauthenticated behavior, got %d: %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/overview", nil)
	request.AddCookie(firstCookie)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected member overview 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{
		secondProject.ID, secondAgent.ID, "second-task", "Second private task", "second-run", "second-schedule", "second-approval",
		"SECOND_APPROVAL_SECRET", "PRIVATE_SCHEDULE_PROMPT", "FIRST_APPROVAL_SECRET", "FIRST_DESCRIPTION_SECRET", "SECOND_DESCRIPTION_SECRET",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("member overview leaked %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{firstProject.ID, firstAgent.ID, "first-task", "first-schedule"} {
		if !strings.Contains(body, required) {
			t.Fatalf("member overview omitted %q: %s", required, body)
		}
	}
	var response overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.Conversations != 1 || response.Summary.Tasks.Total != 1 || response.Summary.ActiveRuns != 0 || response.Summary.PendingApprovals != 1 || response.Summary.Schedules.Total != 1 {
		t.Fatalf("membership summary included another user's resources: %+v", response.Summary)
	}
}

func TestOverviewExcludesArchivedAgentsAndLegacyConversationFlow(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "overview-archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, workline, activeAgent, err := store.CreateProject(ctx, "Visible", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	archivedAgent, err := store.CreateAgent(ctx, db.Agent{WorklineID: workline.ID, Type: "subagent", Title: "Archived secret", Model: "fake:test", PermissionMode: "acceptEdits", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	conversationProject, _, conversationAgent, err := store.CreateStandaloneConversation(ctx, "Standalone conversation", "fake:test")
	if err != nil {
		t.Fatal(err)
	}
	for id, agentID := range map[string]string{"visible-task": activeAgent.ID, "archived-task": archivedAgent.ID} {
		if _, err := store.CreateSpecTask(ctx, db.SpecTask{ID: id, AgentID: agentID, Text: id, Status: "doing"}); err != nil {
			t.Fatal(err)
		}
	}
	for id, agentID := range map[string]string{"visible-schedule": activeAgent.ID, "archived-schedule": archivedAgent.ID, "conversation-schedule": conversationAgent.ID} {
		if _, err := store.CreateSchedule(ctx, db.Schedule{ID: id, Name: id, AgentID: agentID, Expression: "@daily", Timezone: "UTC", Prompt: "safe", PermissionMode: "readOnly", Enabled: true, NextRunAt: "2026-07-20T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	insertOverviewPendingApproval(t, store, activeAgent.ID, "visible-approval", "VISIBLE_SECRET")
	insertOverviewPendingApproval(t, store, archivedAgent.ID, "archived-approval", "ARCHIVED_SECRET")
	insertOverviewPendingApproval(t, store, conversationAgent.ID, "conversation-approval", "CONVERSATION_SECRET")
	archived := true
	if _, err := store.UpdateAgentNavigationState(ctx, archivedAgent.ID, nil, &archived); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	app.clock = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/overview", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected overview 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{archivedAgent.ID, "Archived secret", "archived-task", "archived-schedule", "archived-approval", "ARCHIVED_SECRET"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("archived agent resource leaked %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{project.ID, activeAgent.ID, "visible-task", "visible-schedule"} {
		if !strings.Contains(body, required) {
			t.Fatalf("overview omitted visible resource %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{conversationProject.ID, conversationAgent.ID, "conversation-schedule", "conversation-approval"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("legacy conversation-flow resource leaked into overview: %q in %s", forbidden, body)
		}
	}
	var response overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.Conversations != 1 || response.Summary.Tasks.Total != 1 || response.Summary.PendingApprovals != 1 || response.Summary.Schedules.Total != 1 {
		t.Fatalf("archived or conversation-flow filtering was incorrect: %+v", response.Summary)
	}
}

func TestOverviewFiltersRestrictedRemoteFilesystemScope(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	insidePath := filepath.Join(projectsRoot, "inside")
	outsidePath := filepath.Join(root, "outside")
	if err := os.MkdirAll(insidePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsidePath, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(ctx, filepath.Join(root, "overview-remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insideProject, _, insideAgent, err := store.CreateProject(ctx, "Inside", "", insidePath, "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	outsideProject, _, outsideAgent, err := store.CreateProject(ctx, "Outside", "", outsidePath, "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	conversationProject, _, conversationAgent, err := store.CreateStandaloneConversation(ctx, "Remote conversation", "fake:test")
	if err != nil {
		t.Fatal(err)
	}
	for id, agentID := range map[string]string{"inside-task": insideAgent.ID, "outside-task": outsideAgent.ID} {
		if _, err := store.CreateSpecTask(ctx, db.SpecTask{ID: id, AgentID: agentID, Text: id, Status: "doing"}); err != nil {
			t.Fatal(err)
		}
	}
	for id, agentID := range map[string]string{"inside-schedule": insideAgent.ID, "outside-schedule": outsideAgent.ID, "remote-conversation-schedule": conversationAgent.ID} {
		if _, err := store.CreateSchedule(ctx, db.Schedule{ID: id, Name: id, AgentID: agentID, Expression: "@daily", Timezone: "UTC", Prompt: "safe", PermissionMode: "readOnly", Enabled: true, NextRunAt: "2026-07-20T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	insertOverviewPendingApproval(t, store, insideAgent.ID, "inside-approval", "INSIDE_SECRET")
	insertOverviewPendingApproval(t, store, outsideAgent.ID, "outside-approval", "OUTSIDE_SECRET")
	insertOverviewPendingApproval(t, store, conversationAgent.ID, "conversation-remote-approval", "CONVERSATION_SECRET")
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{HomeDir: root, DefaultProjectDir: projectsRoot},
		Security: config.SecurityConfig{
			AccessPasswordHash: hash, AllowRemoteFullAccess: true,
			DefaultRemoteAccessMode: remoteAccessModeRestricted, CredentialRevision: 1,
		},
	}
	app := New(cfg, store, nil, nil)
	app.clock = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	request := newTestRequest(http.MethodGet, "/api/overview", nil)
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected restricted overview 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{outsideProject.ID, outsideAgent.ID, outsidePath, "outside-task", "outside-schedule", "outside-approval", "OUTSIDE_SECRET"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("restricted overview leaked %q: %s", forbidden, body)
		}
	}
	for _, required := range []string{insideProject.ID, insideAgent.ID, "inside-task", "inside-schedule"} {
		if !strings.Contains(body, required) {
			t.Fatalf("restricted overview omitted %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{conversationProject.ID, conversationAgent.ID, "remote-conversation-schedule", "conversation-remote-approval"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("restricted overview exposed removed conversation-flow resource %q: %s", forbidden, body)
		}
	}
	var response overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.Conversations != 1 || response.Summary.Tasks.Total != 1 || response.Summary.PendingApprovals != 1 || response.Summary.Schedules.Total != 1 {
		t.Fatalf("restricted overview summary escaped filesystem scope: %+v", response.Summary)
	}
}

func TestOverviewUsesRunnerRealtimeStateForActiveRuns(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "overview-runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, workline, liveAgent, err := store.CreateProject(ctx, "Runtime", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	staleAgent, err := store.CreateAgent(ctx, db.Agent{WorklineID: workline.ID, Type: "subagent", Title: "Stale", Model: "fake:test", PermissionMode: "acceptEdits", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	staleAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := store.CreateRun(ctx, db.Run{ID: "stale-run", AgentID: staleAgent.ID, Status: "running", StartedAt: staleAt, CreatedAt: staleAt, UpdatedAt: staleAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAgentStatus(ctx, staleAgent.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: liveAgent.ID, Role: "user", ContentText: "wait"}); err != nil {
		t.Fatal(err)
	}
	provider := &overviewBlockingProvider{started: make(chan struct{})}
	registry := providers.NewRegistry()
	registry.Register(provider)
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	hub := agentpkg.NewHub()
	runner := agentpkg.NewRunner(store, registry, toolRegistry, hub, config.AgentConfig{MaxTurns: 2})
	app := New(config.Config{}, store, runner, hub, registry)

	done := make(chan struct{})
	go func() {
		runner.Run(ctx, liveAgent.ID)
		close(done)
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	defer func() {
		_, _ = runner.Interrupt(context.Background(), liveAgent.ID)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("runner did not stop after interrupt")
		}
	}()

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/overview", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected overview 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response overviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.ActiveRuns != 1 || response.Summary.RunningAgents != 1 || len(response.ActiveRuns) != 1 {
		t.Fatalf("overview did not match runner realtime state: summary=%+v runs=%+v", response.Summary, response.ActiveRuns)
	}
	if response.ActiveRuns[0].AgentID != liveAgent.ID || response.ActiveRuns[0].ID == "stale-run" {
		t.Fatalf("durable residue displaced the live run: %+v", response.ActiveRuns)
	}
	assertJSONArrayObjectKeys(t, mustOverviewRawField(t, recorder.Body.Bytes(), "activeRuns"), "id", "agentId", "agentTitle", "status", "startedAt")
}

func TestOverviewHandlesUnavailableStoreAndMatchesFrontendLimits(t *testing.T) {
	if overviewRecentConversationLimit != 8 || overviewActiveTaskLimit != 8 || overviewActiveRunLimit != 6 || overviewUpcomingScheduleLimit != 8 {
		t.Fatalf("backend overview limits drifted from normalizeOverviewPayload")
	}
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/overview", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "overview unavailable") {
		t.Fatalf("nil store must fail closed without internal details: %d %s", recorder.Code, recorder.Body.String())
	}
}

func mustOverviewRawField(t *testing.T, payload []byte, field string) json.RawMessage {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	return response[field]
}

func insertOverviewPendingApproval(t *testing.T, store *db.Store, agentID, id, secret string) {
	t.Helper()
	now := db.Now()
	if _, err := store.DB().ExecContext(context.Background(), `
INSERT INTO agent_tool_calls (id, agent_id, tool_use_id, tool_name, input_json, status, execution_device_id, created_at, updated_at)
VALUES (?, ?, ?, 'Bash', ?, 'pending_approval', 'local', ?, ?)`, id, agentID, id, `{"command":"`+secret+`"}`, now, now); err != nil {
		t.Fatal(err)
	}
}

func assertOverviewContractKeys(t *testing.T, payload []byte) {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, response, "capturedAt", "summary", "recentConversations", "activeTasks", "activeRuns", "upcomingSchedules")
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(response["summary"], &summary); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, summary, "conversations", "runningAgents", "tasks", "activeRuns", "pendingApprovals", "schedules")
	var taskSummary map[string]json.RawMessage
	if err := json.Unmarshal(summary["tasks"], &taskSummary); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, taskSummary, "total", "todo", "doing", "done")
	var scheduleSummary map[string]json.RawMessage
	if err := json.Unmarshal(summary["schedules"], &scheduleSummary); err != nil {
		t.Fatal(err)
	}
	assertJSONKeys(t, scheduleSummary, "total", "enabled", "due", "failed")

	assertJSONArrayObjectKeys(t, response["recentConversations"], "id", "title", "status", "projectId", "projectName", "updatedAt")
	assertJSONArrayObjectKeys(t, response["activeTasks"], "id", "title", "status", "priority", "agentId", "agentTitle", "projectId", "projectName", "updatedAt")
	assertJSONArrayObjectKeys(t, response["activeRuns"], "id", "agentId", "agentTitle", "status", "startedAt")
	assertJSONArrayObjectKeys(t, response["upcomingSchedules"], "id", "name", "agentId", "agentTitle", "nextRunAt", "timezone", "lastOutcome")
}

func assertJSONKeys(t *testing.T, value map[string]json.RawMessage, expected ...string) {
	t.Helper()
	if len(value) != len(expected) {
		t.Fatalf("unexpected JSON keys: got=%v expected=%v", mapKeys(value), expected)
	}
	for _, key := range expected {
		if _, ok := value[key]; !ok {
			t.Fatalf("missing JSON key %q in %v", key, mapKeys(value))
		}
	}
}

func assertJSONArrayObjectKeys(t *testing.T, raw json.RawMessage, expected ...string) {
	t.Helper()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		return
	}
	assertJSONKeys(t, items[0], expected...)
}

func mapKeys(value map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}
