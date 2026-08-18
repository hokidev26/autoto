package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestNavigationReturnsProjectsAndSafeConversations(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, err = store.DB().ExecContext(ctx, `
INSERT INTO projects (id, name, description, status, flow_mode, git_path, created_at, updated_at) VALUES
  ('project-chat', 'Chat', 'conversation project', 'active', 'workspace', '/repos/chat', '2026-01-01T00:00:00Z', '2026-01-03T00:00:00Z'),
  ('project-empty', 'Empty', 'project without agents', 'active', 'workspace', '/repos/empty', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z');
INSERT INTO worklines (id, project_id, title, status, role, branch, is_root, created_at, updated_at) VALUES
  ('workline-chat', 'project-chat', 'main', 'active', 'root', 'main', 1, '2026-01-01T00:00:00Z', '2026-01-03T01:00:00Z'),
  ('workline-empty', 'project-empty', 'main', 'active', 'root', 'main', 1, '2026-01-01T00:00:00Z', '2026-01-02T01:00:00Z');
INSERT INTO agents (id, workline_id, type, title, model, system_prompt, permission_mode, status, cwd, message_count, last_message_at, context_summary, error_message, created_at, updated_at) VALUES
  ('agent-chat', 'workline-chat', 'primary', 'Chat agent', 'model-safe', 'SYSTEM_PROMPT_SECRET', 'acceptEdits', 'idle', '/repos/chat', 7, '2026-02-01T00:00:00Z', 'CONTEXT_SUMMARY_SECRET', 'ERROR_MESSAGE_SECRET', '2026-01-01T00:00:00Z', '2026-01-04T00:00:00Z'),
  ('agent-review', 'workline-chat', 'subagent', 'Review agent', 'model-review', 'SYSTEM_PROMPT_SECRET_2', 'readOnly', 'running', '/repos/chat', 3, '2026-01-31T00:00:00Z', 'CONTEXT_SUMMARY_SECRET_2', 'ERROR_MESSAGE_SECRET_2', '2026-01-02T00:00:00Z', '2026-01-31T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/navigation", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, forbidden := range []string{"systemPrompt", "contextSummary", "errorMessage", "SYSTEM_PROMPT_SECRET", "CONTEXT_SUMMARY_SECRET", "ERROR_MESSAGE_SECRET"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("navigation response leaked %q: %s", forbidden, body)
		}
	}

	var response struct {
		Projects      []db.Project                 `json:"projects"`
		Conversations []map[string]json.RawMessage `json:"conversations"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Projects) != 2 {
		t.Fatalf("expected projects to include the empty project, got %+v", response.Projects)
	}
	seenEmpty := false
	for _, project := range response.Projects {
		if project.ID == "project-empty" {
			seenEmpty = true
		}
	}
	if !seenEmpty {
		t.Fatalf("empty project missing from projects: %+v", response.Projects)
	}
	if len(response.Conversations) != 2 {
		t.Fatalf("expected both agents from the same project in conversations, got %+v", response.Conversations)
	}
	conversation := response.Conversations[0]
	allowedKeys := map[string]bool{
		"context": true, "projectId": true, "projectName": true, "projectPath": true, "projectUpdatedAt": true, "projectPinned": true,
		"worklineId": true, "worklineTitle": true, "worklineRole": true, "worklineBranch": true, "worklineUpdatedAt": true,
		"agentId": true, "agentTitle": true, "agentType": true, "agentStatus": true, "agentPinned": true, "model": true, "permissionMode": true,
		"cwd": true, "messageCount": true, "lastActivityAt": true,
	}
	if len(conversation) != len(allowedKeys) {
		t.Fatalf("unexpected navigation conversation fields: %+v", conversation)
	}
	for key := range conversation {
		if !allowedKeys[key] {
			t.Fatalf("unsafe or unexpected navigation conversation field %q", key)
		}
	}
	if string(conversation["agentId"]) != `"agent-chat"` || string(conversation["lastActivityAt"]) != `"2026-02-01T00:00:00Z"` {
		t.Fatalf("unexpected conversation payload: %+v", conversation)
	}
	second := response.Conversations[1]
	if string(second["projectId"]) != `"project-chat"` || string(second["agentId"]) != `"agent-review"` || string(second["agentStatus"]) != `"running"` {
		t.Fatalf("expected the second agent to remain grouped under the same project: %+v", second)
	}
}

func TestNavigationHidesStandaloneConversationContainersButRetainsArchiveCleanupProjection(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "standalone-navigation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaceProject, _, _, err := store.CreateProject(ctx, "Workspace", "", t.TempDir(), "fake:workspace", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	conversationProject, _, conversationAgent, err := store.CreateStandaloneConversation(ctx, "Standalone", "fake:chat")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/navigation", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response navigationResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Projects) != 1 || response.Projects[0].ID != workspaceProject.ID {
		t.Fatalf("conversation container leaked into project navigation: %+v", response.Projects)
	}
	for _, conversation := range response.Conversations {
		if conversation.AgentID == conversationAgent.ID || conversation.ProjectID == conversationProject.ID || conversation.Context == db.ProjectFlowModeConversation {
			t.Fatalf("legacy conversation-flow record leaked into navigation: %+v", response.Conversations)
		}
	}

	archived := true
	if _, err := store.UpdateAgentNavigationState(ctx, conversationAgent.ID, nil, &archived); err != nil {
		t.Fatal(err)
	}
	archiveRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(archiveRecorder, newTestRequest(http.MethodGet, "/api/navigation?includeArchived=true", nil))
	if archiveRecorder.Code != http.StatusOK {
		t.Fatalf("expected archive projection 200, got %d: %s", archiveRecorder.Code, archiveRecorder.Body.String())
	}
	var archiveResponse navigationResponse
	if err := json.NewDecoder(archiveRecorder.Body).Decode(&archiveResponse); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(archiveResponse.Projects, func(project db.Project) bool { return project.ID == conversationProject.ID }) {
		t.Fatalf("archive projection lost legacy container needed for cleanup: %+v", archiveResponse.Projects)
	}
	if !slices.ContainsFunc(archiveResponse.Conversations, func(conversation db.NavigationConversation) bool {
		return conversation.AgentID == conversationAgent.ID && conversation.Context == db.ProjectFlowModeConversation && conversation.AgentArchivedAt != ""
	}) {
		t.Fatalf("archive projection lost legacy conversation needed for cleanup: %+v", archiveResponse.Conversations)
	}
}

func TestRestrictedRemoteNavigationAllowsFilesystemFreeConversations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(root, "remote-conversation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, conversationAgent, err := store.CreateStandaloneConversation(ctx, "Remote chat", "fake:chat")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{
		Paths:    config.PathsConfig{HomeDir: root, DefaultProjectDir: filepath.Join(root, "projects")},
		Security: config.SecurityConfig{AccessPasswordHash: hash, DefaultRemoteAccessMode: remoteAccessModeRestricted, CredentialRevision: 1},
	}, store, nil, nil)
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	request := newTestRequest(http.MethodGet, "/api/navigation", nil)
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), conversationAgent.ID) || strings.Contains(recorder.Body.String(), `"context":"conversation"`) {
		t.Fatalf("restricted remote navigation exposed removed conversation-flow data: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestNavigationStateRoutesPinArchiveAndRestore(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "navigation-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _, agent, err := store.CreateProject(ctx, "Navigation state", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)
	patch := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPatch, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}

	projectResponse := patch("/api/projects/"+project.ID+"/navigation-state", `{"pinned":true}`)
	if projectResponse.Code != http.StatusOK {
		t.Fatalf("expected project pin 200, got %d: %s", projectResponse.Code, projectResponse.Body.String())
	}
	var pinnedProject db.Project
	if err := json.Unmarshal(projectResponse.Body.Bytes(), &pinnedProject); err != nil {
		t.Fatal(err)
	}
	if !pinnedProject.Pinned || pinnedProject.ArchivedAt != "" {
		t.Fatalf("unexpected pinned project response: %+v", pinnedProject)
	}

	agentResponse := patch("/api/agents/"+agent.ID+"/navigation-state", `{"archived":true}`)
	if agentResponse.Code != http.StatusOK {
		t.Fatalf("expected agent archive 200, got %d: %s", agentResponse.Code, agentResponse.Body.String())
	}
	var archivedAgent db.Agent
	if err := json.Unmarshal(agentResponse.Body.Bytes(), &archivedAgent); err != nil {
		t.Fatal(err)
	}
	if archivedAgent.ArchivedAt == "" {
		t.Fatalf("expected archived agent timestamp: %+v", archivedAgent)
	}

	defaultNavigation := httptest.NewRecorder()
	app.Routes().ServeHTTP(defaultNavigation, newTestRequest(http.MethodGet, "/api/navigation", nil))
	if defaultNavigation.Code != http.StatusOK || !strings.Contains(defaultNavigation.Body.String(), project.ID) || strings.Contains(defaultNavigation.Body.String(), agent.ID) {
		t.Fatalf("default navigation should retain project and hide archived conversation: %d %s", defaultNavigation.Code, defaultNavigation.Body.String())
	}
	archivedNavigation := httptest.NewRecorder()
	app.Routes().ServeHTTP(archivedNavigation, newTestRequest(http.MethodGet, "/api/navigation?includeArchived=true", nil))
	if archivedNavigation.Code != http.StatusOK || !strings.Contains(archivedNavigation.Body.String(), agent.ID) || !strings.Contains(archivedNavigation.Body.String(), `"agentArchivedAt"`) || !strings.Contains(archivedNavigation.Body.String(), `"projectPinned":true`) {
		t.Fatalf("archived navigation should expose safe state fields: %d %s", archivedNavigation.Code, archivedNavigation.Body.String())
	}

	restoreResponse := patch("/api/narrators/"+agent.ID+"/navigation-state", `{"archived":false,"pinned":true}`)
	if restoreResponse.Code != http.StatusOK {
		t.Fatalf("expected narrator alias restore 200, got %d: %s", restoreResponse.Code, restoreResponse.Body.String())
	}
	projectArchive := patch("/api/projects/"+project.ID+"/navigation-state", `{"archived":true}`)
	if projectArchive.Code != http.StatusOK {
		t.Fatalf("expected project archive 200, got %d: %s", projectArchive.Code, projectArchive.Body.String())
	}
	defaultNavigation = httptest.NewRecorder()
	app.Routes().ServeHTTP(defaultNavigation, newTestRequest(http.MethodGet, "/api/navigation", nil))
	if strings.Contains(defaultNavigation.Body.String(), project.ID) || strings.Contains(defaultNavigation.Body.String(), agent.ID) {
		t.Fatalf("archived project and conversations should be hidden by default: %s", defaultNavigation.Body.String())
	}
	archivedNavigation = httptest.NewRecorder()
	app.Routes().ServeHTTP(archivedNavigation, newTestRequest(http.MethodGet, "/api/navigation?includeArchived=1", nil))
	if !strings.Contains(archivedNavigation.Body.String(), project.ID) || !strings.Contains(archivedNavigation.Body.String(), agent.ID) || !strings.Contains(archivedNavigation.Body.String(), `"projectArchivedAt"`) {
		t.Fatalf("includeArchived should retain archived project hierarchy: %s", archivedNavigation.Body.String())
	}

	for _, invalid := range []string{`{}`, `{"pinned":null}`, `{"pinned":"true"}`, `{"unknown":true}`} {
		if response := patch("/api/projects/"+project.ID+"/navigation-state", invalid); response.Code != http.StatusBadRequest {
			t.Fatalf("expected invalid patch 400 for %s, got %d: %s", invalid, response.Code, response.Body.String())
		}
	}
}

func TestNavigationRequiresMembershipWhenUsersExist(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "membership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)
	registerIsolationAdmin(t, app)
	firstCookie := registerCollaborationTestUser(t, app, "navigation-first")
	registerCollaborationTestUser(t, app, "navigation-second")
	first, _, err := store.GetUserByHandle(ctx, "navigation-first")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.GetUserByHandle(ctx, "navigation-second")
	if err != nil {
		t.Fatal(err)
	}
	firstProject, _, firstAgent, err := store.CreateProjectForUser(ctx, first.ID, "First project", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, _, secondAgent, err := store.CreateProjectForUser(ctx, second.ID, "Second project", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	firstConversationProject, _, firstConversationAgent, err := store.CreateStandaloneConversationForUser(ctx, first.ID, "First chat", "fake:test")
	if err != nil {
		t.Fatal(err)
	}
	secondConversationProject, _, secondConversationAgent, err := store.CreateStandaloneConversationForUser(ctx, second.ID, "Second chat", "fake:test")
	if err != nil {
		t.Fatal(err)
	}

	unauthenticated := httptest.NewRecorder()
	app.Routes().ServeHTTP(unauthenticated, newTestRequest(http.MethodGet, "/api/navigation", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("expected navigation to require a user session, got %d: %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/navigation", nil)
	request.AddCookie(firstCookie)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected member navigation 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{firstProject.ID, firstAgent.ID} {
		if !strings.Contains(body, expected) {
			t.Fatalf("member navigation omitted %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{firstConversationProject.ID, firstConversationAgent.ID, secondProject.ID, secondAgent.ID, secondConversationProject.ID, secondConversationAgent.ID} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("member navigation leaked %q: %s", forbidden, body)
		}
	}
	var memberNavigation navigationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &memberNavigation); err != nil {
		t.Fatal(err)
	}
	for _, project := range memberNavigation.Projects {
		if project.ID == firstConversationProject.ID {
			t.Fatalf("owned conversation container leaked into project array: %+v", memberNavigation.Projects)
		}
	}

	unauthenticatedPatch := httptest.NewRecorder()
	unauthenticatedPatchRequest := newTestRequest(http.MethodPatch, "/api/projects/"+firstProject.ID+"/navigation-state", strings.NewReader(`{"pinned":true}`))
	unauthenticatedPatchRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(unauthenticatedPatch, unauthenticatedPatchRequest)
	if unauthenticatedPatch.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated navigation patch 401, got %d: %s", unauthenticatedPatch.Code, unauthenticatedPatch.Body.String())
	}

	crossTenantPatch := httptest.NewRecorder()
	crossTenantPatchRequest := newTestRequest(http.MethodPatch, "/api/agents/"+secondAgent.ID+"/navigation-state", strings.NewReader(`{"archived":true}`))
	crossTenantPatchRequest.Header.Set("Content-Type", "application/json")
	crossTenantPatchRequest.AddCookie(firstCookie)
	app.Routes().ServeHTTP(crossTenantPatch, crossTenantPatchRequest)
	if crossTenantPatch.Code != http.StatusNotFound {
		t.Fatalf("expected cross-tenant navigation patch 404, got %d: %s", crossTenantPatch.Code, crossTenantPatch.Body.String())
	}

	ownPatch := httptest.NewRecorder()
	ownPatchRequest := newTestRequest(http.MethodPatch, "/api/agents/"+firstAgent.ID+"/navigation-state", strings.NewReader(`{"pinned":true}`))
	ownPatchRequest.Header.Set("Content-Type", "application/json")
	ownPatchRequest.AddCookie(firstCookie)
	app.Routes().ServeHTTP(ownPatch, ownPatchRequest)
	if ownPatch.Code != http.StatusOK {
		t.Fatalf("expected own navigation patch 200, got %d: %s", ownPatch.Code, ownPatch.Body.String())
	}
}

func TestNavigationUsesLocalRequestGuard(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/navigation", nil)
	request.Host = "localhost:7788"
	request.Header.Set("Origin", "http://evil.test")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected navigation route to remain behind local request guard, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
