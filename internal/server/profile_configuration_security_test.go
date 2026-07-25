package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/hooks"
)

func newProfileConfigurationRouteServer(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "profile-routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil), store
}

func profileConfigurationRequest(t *testing.T, app *Server, method, path, body string, configure ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := newTestRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, apply := range configure {
		if apply != nil {
			apply(request)
		}
	}
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	return response
}

func TestProfileConfigurationRoutesRequireLoginAfterInstallation(t *testing.T) {
	app, _ := newProfileConfigurationRouteServer(t)
	_ = registerCollaborationTestUser(t, app, "installed-user")
	for _, path := range []string{
		"/api/optional-tools/catalog",
		"/api/agent-role-definitions?scope=global",
		"/api/prompt-definitions?scope=global",
		"/api/effective-prompt?agentId=missing-agent",
		"/api/effective-child-roles?agentId=missing-agent",
		"/api/lifecycle-hooks",
	} {
		response := profileConfigurationRequest(t, app, http.MethodGet, path, "")
		if response.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s = %d: %s", path, response.Code, response.Body.String())
		}
	}
}

func TestProfileConfigurationRestrictedRemoteWritesAreForbidden(t *testing.T) {
	app, _ := newProfileConfigurationRouteServer(t)
	remote := withRemotePreferencesSession(t, app, remoteAccessModeRestricted)
	roleBody := `{"scope":{"scope":"global"},"key":"safe-review","displayName":"Safe review","definition":{"version":1,"key":"safe-review","displayName":"Safe review","baseRole":"reviewer"}}`
	promptBody := `{"scope":{"scope":"global"},"key":"guidance","displayName":"Guidance","layer":"global_user","content":"Keep changes bounded."}`
	hookBody := `{"name":"Audit","enabled":true,"event":"tool.after","scope":{"kind":"global"},"priority":0,"mode":"async","failurePolicy":"continue","action":{"kind":"shell","shell":{"executable":"audit-helper","canonicalStdinV1":true}}}`
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/api/optional-tools/rules", `{"toolName":"read","scope":"global","state":"disabled","expectedRevision":0}`},
		{http.MethodPost, "/api/agent-role-definitions", roleBody},
		{http.MethodPost, "/api/prompt-definitions", promptBody},
		{http.MethodPost, "/api/lifecycle-hooks", hookBody},
	} {
		response := profileConfigurationRequest(t, app, test.method, test.path, test.body, remote)
		if response.Code != http.StatusForbidden {
			t.Errorf("restricted remote %s %s = %d: %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func TestProfileConfigurationCrossProjectResourcesReturnNotFound(t *testing.T) {
	ctx := context.Background()
	app, store := newProfileConfigurationRouteServer(t)
	ownerCookie := registerCollaborationTestUser(t, app, "profile-owner")
	owner, _, err := store.GetUserByHandle(ctx, "profile-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.CreateProjectForUser(ctx, owner.ID, "Owner", "", t.TempDir(), "fake:test", "acceptEdits"); err != nil {
		t.Fatal(err)
	}
	_ = registerCollaborationTestUser(t, app, "profile-outsider")
	outsider, _, err := store.GetUserByHandle(ctx, "profile-outsider")
	if err != nil {
		t.Fatal(err)
	}
	outsiderProject, _, outsiderAgent, err := store.CreateProjectForUser(ctx, outsider.ID, "Outsider", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	rule, err := store.SetToolAvailabilityRuleCAS(ctx, db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeProject, ProjectID: outsiderProject.ID}, "read", db.ToolAvailabilityDisabled, 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	role, err := store.CreateAgentRoleDefinition(ctx, db.AgentRoleDefinitionInput{
		Scope: db.DefinitionScopeTarget{Scope: db.DefinitionScopeProject, ProjectID: outsiderProject.ID}, Key: "outsider-role", DisplayName: "Outsider role",
		DefinitionJSON: json.RawMessage(`{"version":1,"key":"outsider-role","displayName":"Outsider role","baseRole":"reviewer"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := store.CreatePromptDefinition(ctx, db.PromptDefinitionInput{
		Scope: db.DefinitionScopeTarget{Scope: db.DefinitionScopeProject, ProjectID: outsiderProject.ID}, Key: "outsider-prompt", DisplayName: "Outsider prompt", Layer: db.PromptLayerGlobalUser, Content: "PRIVATE_PROMPT_BODY",
	})
	if err != nil {
		t.Fatal(err)
	}
	hook, err := store.CreateLifecycleHook(ctx, hooks.Hook{
		Name: "Outsider hook", Enabled: true, Event: hooks.EventToolAfter, Scope: hooks.Scope{Kind: hooks.ScopeAgent, ID: outsiderAgent.ID}, Mode: hooks.ModeAsync, FailurePolicy: hooks.FailureContinue,
		Action: hooks.Action{Kind: hooks.ActionHTTP, HTTP: &hooks.HTTPAction{URL: "https://private.example.test/hook", Method: http.MethodPost, SecretRefs: map[string]string{"Authorization": "env:PRIVATE_HOOK_TOKEN"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	withOwner := func(request *http.Request) { request.AddCookie(ownerCookie) }
	for _, path := range []string{
		"/api/optional-tools/rules?scope=project&projectId=" + outsiderProject.ID,
		"/api/optional-tools/rules/" + rule.ID + "/revisions",
		"/api/agent-role-definitions/" + role.ID,
		"/api/prompt-definitions/" + prompt.ID,
		"/api/effective-prompt?agentId=" + outsiderAgent.ID,
		"/api/effective-child-roles?agentId=" + outsiderAgent.ID,
		"/api/lifecycle-hooks/" + hook.ID,
		"/api/lifecycle-hooks/" + hook.ID + "/history",
	} {
		response := profileConfigurationRequest(t, app, http.MethodGet, path, "", withOwner)
		if response.Code != http.StatusNotFound {
			t.Errorf("cross-project %s = %d: %s", path, response.Code, response.Body.String())
		}
		for _, secret := range []string{"PRIVATE_PROMPT_BODY", "private.example.test", "PRIVATE_HOOK_TOKEN", "Outsider hook"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Errorf("cross-project response leaked %q: %s", secret, response.Body.String())
			}
		}
	}
}

func TestProfileConfigurationGlobalLocalAndFullRemoteCompatibilityAndHookSecretRedaction(t *testing.T) {
	app, _ := newProfileConfigurationRouteServer(t)
	roleBody := `{"scope":{"scope":"global"},"key":"local-role","displayName":"Local role","definition":{"version":1,"key":"local-role","displayName":"Local role","baseRole":"general"}}`
	if response := profileConfigurationRequest(t, app, http.MethodPost, "/api/agent-role-definitions", roleBody); response.Code != http.StatusCreated {
		t.Fatalf("local global role create = %d: %s", response.Code, response.Body.String())
	}

	fullRemote := withRemotePreferencesSession(t, app, remoteAccessModeFull)
	promptBody := `{"scope":{"scope":"global"},"key":"remote-guidance","displayName":"Remote guidance","layer":"global_user","content":"Full remote is allowed."}`
	if response := profileConfigurationRequest(t, app, http.MethodPost, "/api/prompt-definitions", promptBody, fullRemote); response.Code != http.StatusCreated {
		t.Fatalf("full remote global prompt create = %d: %s", response.Code, response.Body.String())
	}

	hookBody := `{"name":"Secret hook","enabled":true,"event":"tool.after","scope":{"kind":"global"},"priority":0,"mode":"async","failurePolicy":"continue","action":{"kind":"http","http":{"url":"https://hooks.example.test/event","method":"POST","secretRefs":{"Authorization":"env:SUPER_PRIVATE_HOOK_TOKEN"}}}}`
	created := profileConfigurationRequest(t, app, http.MethodPost, "/api/lifecycle-hooks", hookBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("local hook create = %d: %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "env:SUPER_PRIVATE_HOOK_TOKEN") || strings.Contains(created.Body.String(), "secretRefs") {
		t.Fatalf("hook response echoed secret refs: %s", created.Body.String())
	}
	if !strings.Contains(created.Body.String(), `"secretConfigured":{"Authorization":true}`) {
		t.Fatalf("hook response omitted write-only secret status: %s", created.Body.String())
	}
}
