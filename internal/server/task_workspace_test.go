package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestTaskWorkspaceRequiresMembershipAndOmitsSensitiveAgentFields(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "task-workspace-membership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)

	registerIsolationAdmin(t, app)
	firstCookie := registerCollaborationTestUser(t, app, "workspace-first")
	registerCollaborationTestUser(t, app, "workspace-second")
	firstUser, _, err := store.GetUserByHandle(ctx, "workspace-first")
	if err != nil {
		t.Fatal(err)
	}
	secondUser, _, err := store.GetUserByHandle(ctx, "workspace-second")
	if err != nil {
		t.Fatal(err)
	}
	firstProject, firstWorkline, firstAgent, err := store.CreateProjectForUser(ctx, firstUser.ID, "First workspace", "PRIVATE_DESCRIPTION", t.TempDir(), "fake:first", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, _, secondAgent, err := store.CreateProjectForUser(ctx, secondUser.ID, "Second workspace", "", t.TempDir(), "fake:second", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	emptyAgent, err := store.CreateAgent(ctx, db.Agent{WorklineID: firstWorkline.ID, Type: "primary", Title: "No tasks", Model: "fake:empty", CWD: firstProject.GitPath})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []db.SpecTask{
		{AgentID: firstAgent.ID, Text: "todo", Status: "todo"},
		{AgentID: firstAgent.ID, Text: "doing", Status: "doing"},
		{AgentID: firstAgent.ID, Text: "blocked", Status: "blocked"},
		{AgentID: firstAgent.ID, Text: "done", Status: "done"},
		{AgentID: secondAgent.ID, Text: "other user's task", Status: "todo"},
	} {
		if _, err := store.CreateSpecTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET system_prompt = 'SYSTEM_PROMPT_SECRET', context_summary = 'CONTEXT_SUMMARY_SECRET', error_message = 'ERROR_MESSAGE_SECRET' WHERE id IN (?, ?)`, firstAgent.ID, emptyAgent.ID); err != nil {
		t.Fatal(err)
	}

	unauthenticated := httptest.NewRecorder()
	app.Routes().ServeHTTP(unauthenticated, newTestRequest(http.MethodGet, "/api/task-workspace", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated workspace request to fail, got %d: %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/task-workspace", nil)
	request.AddCookie(firstCookie)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected workspace 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{
		secondProject.ID, secondAgent.ID, "other user's task",
		"systemPrompt", "contextSummary", "errorMessage",
		"SYSTEM_PROMPT_SECRET", "CONTEXT_SUMMARY_SECRET", "ERROR_MESSAGE_SECRET", "PRIVATE_DESCRIPTION",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("task workspace leaked %q: %s", forbidden, body)
		}
	}

	var workspace db.TaskWorkspace
	if err := json.Unmarshal(recorder.Body.Bytes(), &workspace); err != nil {
		t.Fatal(err)
	}
	if len(workspace.Projects) != 1 || workspace.Projects[0].ID != firstProject.ID || len(workspace.Projects[0].Worklines) != 1 {
		t.Fatalf("unexpected member workspace: %+v", workspace)
	}
	agents := workspace.Projects[0].Agents
	if len(agents) != 2 {
		t.Fatalf("expected task and no-task agents, got %+v", agents)
	}
	byID := make(map[string]db.TaskWorkspaceAgent, len(agents))
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	if counts := byID[firstAgent.ID].Counts; counts != (db.SpecTaskStatusCounts{Todo: 1, Doing: 1, Blocked: 1, Done: 1, Total: 4}) {
		t.Fatalf("unexpected status counts: %+v", counts)
	}
	if workspace.Summary.ProjectCount != 1 || workspace.Summary.AgentCount != 2 || workspace.Summary.Total != 4 {
		t.Fatalf("member summary leaked or omitted counts: %+v", workspace.Summary)
	}
	if empty := byID[emptyAgent.ID]; empty.Tasks == nil || len(empty.Tasks) != 0 || empty.SpecRevision != 0 {
		t.Fatalf("no-task Agent missing safe empty board: %+v", empty)
	}
}

func TestAssignSpecTaskAPIHandlesSameProjectAndConflicts(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "assign-task-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, workline, source, err := store.CreateProject(ctx, "Source project", "", t.TempDir(), "fake:source", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateAgent(ctx, db.Agent{WorklineID: workline.ID, Type: "primary", Title: "Target", Model: "fake:target", CWD: project.GitPath})
	if err != nil {
		t.Fatal(err)
	}
	_, _, crossProjectTarget, err := store.CreateProject(ctx, "Other project", "", t.TempDir(), "fake:other", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	board, err := store.CreateSpecTask(ctx, db.SpecTask{AgentID: source.ID, Text: "protected assignment", Status: "doing", Protected: true})
	if err != nil {
		t.Fatal(err)
	}
	task := board.Tasks[0]
	app := New(config.Config{}, store, nil, nil)

	post := func(targetID string, revision int64, acknowledge bool) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"targetAgentId":        targetID,
			"expectedRevision":     revision,
			"acknowledgeProtected": acknowledge,
		})
		if err != nil {
			t.Fatal(err)
		}
		request := newTestRequest(http.MethodPost, "/api/agents/"+source.ID+"/spec/tasks/"+task.ID+"/assign", strings.NewReader(string(payload)))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}

	if response := post(target.ID, task.Revision, false); response.Code != http.StatusConflict {
		t.Fatalf("expected protected conflict, got %d: %s", response.Code, response.Body.String())
	}
	if response := post(target.ID, task.Revision+1, true); response.Code != http.StatusConflict {
		t.Fatalf("expected revision conflict, got %d: %s", response.Code, response.Body.String())
	}
	if response := post(crossProjectTarget.ID, task.Revision, true); response.Code != http.StatusConflict {
		t.Fatalf("expected cross-project conflict, got %d: %s", response.Code, response.Body.String())
	}

	response := post(target.ID, task.Revision, true)
	if response.Code != http.StatusOK {
		t.Fatalf("expected assignment success, got %d: %s", response.Code, response.Body.String())
	}
	var result db.SpecTaskAssignmentResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task.AgentID != target.ID || len(result.SourceBoard.Tasks) != 0 || len(result.TargetBoard.Tasks) != 1 || result.TargetBoard.Tasks[0].ID != task.ID {
		t.Fatalf("unexpected assignment response: %+v", result)
	}
}

func TestTaskWorkspaceAppliesRestrictedRemoteProjectFiltering(t *testing.T) {
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
	store, err := db.Open(ctx, filepath.Join(root, "task-workspace-remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insideProject, _, insideAgent, err := store.CreateProject(ctx, "Inside", "", insidePath, "fake:inside", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	outsideProject, _, outsideAgent, err := store.CreateProject(ctx, "Outside", "", outsidePath, "fake:outside", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: root, DefaultProjectDir: projectsRoot},
		Security: config.SecurityConfig{
			AccessPasswordHash: hash, AllowRemoteFullAccess: true,
			DefaultRemoteAccessMode: remoteAccessModeRestricted, CredentialRevision: 1,
		},
	}, store, nil, nil)
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)

	request := newTestRequest(http.MethodGet, "/api/task-workspace", nil)
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected restricted workspace 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, insideProject.ID) || !strings.Contains(body, insideAgent.ID) || strings.Contains(body, outsideProject.ID) || strings.Contains(body, outsideAgent.ID) || strings.Contains(body, outsidePath) {
		t.Fatalf("restricted workspace leaked an out-of-root project: %s", body)
	}
}
