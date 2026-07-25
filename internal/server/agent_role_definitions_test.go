package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
)

func newProfileDefinitionTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "definitions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Server{store: store}
}

func definitionHandlerRequest(t *testing.T, handler http.HandlerFunc, method, target, body, id string) *httptest.ResponseRecorder {
	t.Helper()
	request := newTestRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if id != "" {
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", id)
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func TestAgentRoleDefinitionHandlersSeparateSummaryAndBody(t *testing.T) {
	app := newProfileDefinitionTestServer(t)
	body := `{"scope":{"scope":"global"},"key":"safe-review","displayName":"Safe review","summary":"Review safely","definition":{"version":1,"key":"safe-review","displayName":"Safe review","baseRole":"reviewer","toolAllowlist":["Read","Grep"]}}`
	createdResponse := definitionHandlerRequest(t, app.createAgentRoleDefinition, http.MethodPost, "/api/agent-role-definitions", body, "")
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created db.AgentRoleDefinition
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.DefinitionJSON) == 0 {
		t.Fatal("create response omitted definition body")
	}

	listResponse := definitionHandlerRequest(t, app.listAgentRoleDefinitions, http.MethodGet, "/api/agent-role-definitions?scope=global", "", "")
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", listResponse.Code, listResponse.Body.String())
	}
	if strings.Contains(listResponse.Body.String(), "toolAllowlist") || strings.Contains(listResponse.Body.String(), `"definition"`) {
		t.Fatalf("list leaked body: %s", listResponse.Body.String())
	}

	getResponse := definitionHandlerRequest(t, app.getAgentRoleDefinition, http.MethodGet, "/api/agent-role-definitions/"+created.ID, "", created.ID)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), "toolAllowlist") {
		t.Fatalf("detail missing body: %d %s", getResponse.Code, getResponse.Body.String())
	}

	updateBody := `{"expectedRevision":1,"key":"safe-review","displayName":"Safe review","summary":"Updated","definition":{"version":1,"key":"safe-review","displayName":"Safe review","baseRole":"reviewer","toolAllowlist":["Read"]}}`
	updatedResponse := definitionHandlerRequest(t, app.updateAgentRoleDefinition, http.MethodPut, "/api/agent-role-definitions/"+created.ID, updateBody, created.ID)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	staleResponse := definitionHandlerRequest(t, app.updateAgentRoleDefinition, http.MethodPut, "/api/agent-role-definitions/"+created.ID, updateBody, created.ID)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale update = %d: %s", staleResponse.Code, staleResponse.Body.String())
	}
}

func TestEffectiveProfileHandlersUseRunnerSnapshots(t *testing.T) {
	app := newProfileDefinitionTestServer(t)
	app.runner = agentpkg.NewRunner(app.store, nil, newCoreToolRegistry(), agentpkg.NewHub(), config.AgentConfig{})

	ctx := context.Background()
	_, _, agentRecord, err := app.store.CreateProject(ctx, "Effective profiles", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	promptResponse := definitionHandlerRequest(t, app.getEffectivePrompt, http.MethodGet, "/api/effective-prompt?agentId="+agentRecord.ID, "", "")
	if promptResponse.Code != http.StatusOK {
		t.Fatalf("effective prompt = %d: %s", promptResponse.Code, promptResponse.Body.String())
	}
	var prompt agentpkg.EffectivePromptPreview
	if err := json.Unmarshal(promptResponse.Body.Bytes(), &prompt); err != nil {
		t.Fatal(err)
	}
	if prompt.AgentID != agentRecord.ID || len(prompt.Layers) == 0 {
		t.Fatalf("unexpected effective prompt snapshot: %+v", prompt)
	}

	rolesResponse := definitionHandlerRequest(t, app.listEffectiveChildRoles, http.MethodGet, "/api/effective-child-roles?agentId="+agentRecord.ID, "", "")
	if rolesResponse.Code != http.StatusOK {
		t.Fatalf("effective child roles = %d: %s", rolesResponse.Code, rolesResponse.Body.String())
	}
	var roles agentpkg.EffectiveChildRolePreview
	if err := json.Unmarshal(rolesResponse.Body.Bytes(), &roles); err != nil {
		t.Fatal(err)
	}
	if roles.AgentID != agentRecord.ID {
		t.Fatalf("unexpected effective child-role snapshot: %+v", roles)
	}

	ambiguous := definitionHandlerRequest(t, app.getEffectivePrompt, http.MethodGet, "/api/effective-prompt?agentId="+agentRecord.ID+"&extra=true", "", "")
	if ambiguous.Code != http.StatusBadRequest {
		t.Fatalf("unexpected query fields = %d: %s", ambiguous.Code, ambiguous.Body.String())
	}
}

func TestAgentRoleDefinitionHandlersStrictValidationAndReservedEndpoints(t *testing.T) {
	app := newProfileDefinitionTestServer(t)
	invalidBodies := []string{
		`{}`,
		`{"scope":{"scope":"global"},"key":"x","displayName":"X","definition":{"version":1,"key":"x","displayName":"X","baseRole":"general"},"unknown":true}`,
		`{"scope":{"scope":"global"},"key":"x","displayName":"X","definition":{"version":1,"key":"x","displayName":"X","baseRole":"general","basePrompt":"override"}}`,
		`{"scope":{"scope":"global"},"key":"x","key":"y","displayName":"X","definition":{"version":1,"key":"x","displayName":"X","baseRole":"general"}}`,
		`{"scope":{"scope":"global"},"key":"x","displayName":"X","definition":{"version":1,"key":"x","key":"y","displayName":"X","baseRole":"general"}}`,
		`{"scope":{"scope":"global"},"key":"x","displayName":"X","definition":{"version":1,"key":"x","displayName":"X","baseRole":"general"}} {}`,
	}
	for _, body := range invalidBodies {
		response := definitionHandlerRequest(t, app.createAgentRoleDefinition, http.MethodPost, "/api/agent-role-definitions", body, "")
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid body returned %d: %s", response.Code, response.Body.String())
		}
	}
	duplicateScope := definitionHandlerRequest(t, app.listAgentRoleDefinitions, http.MethodGet, "/api/agent-role-definitions?scope=global&scope=project", "", "")
	if duplicateScope.Code != http.StatusBadRequest {
		t.Fatalf("duplicate scope = %d", duplicateScope.Code)
	}

	effectivePrompt := definitionHandlerRequest(t, app.getEffectivePrompt, http.MethodGet, "/api/effective-prompt", "", "")
	if effectivePrompt.Code != http.StatusBadRequest {
		t.Fatalf("effective prompt missing agentId = %d %s", effectivePrompt.Code, effectivePrompt.Body.String())
	}
	effectiveRoles := definitionHandlerRequest(t, app.listEffectiveChildRoles, http.MethodGet, "/api/effective-child-roles", "", "")
	if effectiveRoles.Code != http.StatusBadRequest {
		t.Fatalf("effective roles missing agentId = %d %s", effectiveRoles.Code, effectiveRoles.Body.String())
	}
}
