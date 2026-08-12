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
	"autoto/internal/tools"
)

type workflowTestTool struct {
	name string
}

func (tool workflowTestTool) Name() string               { return tool.name }
func (workflowTestTool) Description() string             { return "test tool" }
func (workflowTestTool) Schema() any                     { return map[string]any{} }
func (workflowTestTool) Risk(json.RawMessage) tools.Risk { return tools.RiskRead }
func (workflowTestTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	return tools.Result{}, nil
}

func TestWorkflowPreferencesAPI(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	routes := app.Routes()

	recorder := httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/workflow/preferences", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected preferences 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var prefs db.WorkflowPreferences
	if err := json.NewDecoder(recorder.Body).Decode(&prefs); err != nil {
		t.Fatal(err)
	}
	if !prefs.RequireConfirmationForExec || prefs.RequireConfirmationForWrites || !prefs.AllowReadOnlyByDefault {
		t.Fatalf("unexpected default preferences: %+v", prefs)
	}

	requireExec := false
	requireWrites := true
	allowReadOnly := false
	putJSON(t, routes, http.MethodPut, "/api/workflow/preferences", workflowPreferencesRequest{RequireConfirmationForExec: &requireExec, RequireConfirmationForWrites: &requireWrites, AllowReadOnlyByDefault: &allowReadOnly}, http.StatusOK)
	updated, err := store.GetWorkflowPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequireConfirmationForExec || !updated.RequireConfirmationForWrites || updated.AllowReadOnlyByDefault {
		t.Fatalf("unexpected updated preferences: %+v", updated)
	}
}

// The permissions panel saves the three confirmation toggles without a
// dangerReflectionLevel field, relying on the server to keep the stored level.
// This pins that contract: a PUT that omits the field must not resurrect the
// default and silently re-enable a gate the user turned off elsewhere.
func TestWorkflowPreferencesPutWithoutLevelKeepsStoredLevel(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	routes := app.Routes()

	off := "off"
	requireExec := true
	requireWrites := false
	allowReadOnly := true
	putJSON(t, routes, http.MethodPut, "/api/workflow/preferences", workflowPreferencesRequest{RequireConfirmationForExec: &requireExec, RequireConfirmationForWrites: &requireWrites, AllowReadOnlyByDefault: &allowReadOnly, DangerReflectionLevel: &off}, http.StatusOK)

	// The same payload shape the workbench panel sends: toggles only, no level.
	recorder := httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPut, "/api/workflow/preferences", strings.NewReader(`{"requireConfirmationForExec":false,"requireConfirmationForWrites":true,"allowReadOnlyByDefault":true}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected toggles-only PUT 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	prefs, err := store.GetWorkflowPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.DangerReflectionLevel != "off" {
		t.Fatalf("a PUT without dangerReflectionLevel must keep the stored level, got %q", prefs.DangerReflectionLevel)
	}
	if prefs.RequireConfirmationForExec || !prefs.RequireConfirmationForWrites {
		t.Fatalf("the toggles themselves must still update: %+v", prefs)
	}
}

func TestWorkflowPreferencesAPIRejectsMissingFields(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodPut, "/api/workflow/preferences", strings.NewReader(`{"requireConfirmationForExec":false}`)))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "all workflow preference fields are required") {
		t.Fatalf("expected missing-field rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
	prefs, err := store.GetWorkflowPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !prefs.RequireConfirmationForExec || prefs.RequireConfirmationForWrites || !prefs.AllowReadOnlyByDefault {
		t.Fatalf("missing-field request changed defaults: %+v", prefs)
	}
}

func TestToolPermissionRulesAPI(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	routes := app.Routes()

	recorder := httptest.NewRecorder()
	req := newTestRequest(http.MethodPost, "/api/workflow/tool-permissions", strings.NewReader(`{"mode":"acceptEdits","toolName":"Bash","risk":"exec","decision":"ask","priority":7,"enabled":true,"description":"confirm bash"}`))
	routes.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var created db.ToolPermissionRule
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Decision != "ask" || created.Priority != 7 || !created.Enabled {
		t.Fatalf("unexpected created rule: %+v", created)
	}

	disabled := false
	deny := "deny"
	recorder = httptest.NewRecorder()
	body, _ := json.Marshal(toolPermissionRuleRequest{Decision: &deny, Enabled: &disabled})
	req = newTestRequest(http.MethodPatch, "/api/workflow/tool-permissions/"+created.ID, strings.NewReader(string(body)))
	routes.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected patch 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var updated db.ToolPermissionRule
	if err := json.NewDecoder(recorder.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Decision != "deny" || updated.Enabled {
		t.Fatalf("unexpected updated rule: %+v", updated)
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/workflow/tool-permissions", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var rules []db.ToolPermissionRule
	if err := json.NewDecoder(recorder.Body).Decode(&rules); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].ID != created.ID {
		t.Fatalf("unexpected rules list: %+v", rules)
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodDelete, "/api/workflow/tool-permissions/"+created.ID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	rules, err = store.ListToolPermissionRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected no rules after delete, got %+v", rules)
	}
}

func TestToolPermissionRulesAPIUsesConfiguredRegistry(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	app := New(config.Config{}, store, nil, nil)
	app.SetToolRegistry(registry)
	app.SetPluginService(&fakePluginService{
		plugins: map[string]db.Plugin{
			"plugin-1": {ID: "plugin-1", Slug: "safe-plugin", Enabled: true, Status: "healthy"},
		},
		configured: map[string]map[string]bool{},
	})
	routes := app.Routes()

	registry.Register(workflowTestTool{name: "DynamicTool"})
	recorder := httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/workflow/tool-permissions", strings.NewReader(`{"toolName":"DynamicTool","risk":"read","decision":"ask"}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected dynamic tool rule create 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var created db.ToolPermissionRule
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/workflow/tool-permissions", strings.NewReader(`{"toolName":"plugin__safe-plugin__echo","risk":"exec","decision":"ask"}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected namespaced plugin tool rule create 201, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/workflow/tool-permissions", strings.NewReader(`{"toolName":"plugin__missing__echo","risk":"exec","decision":"ask"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown plugin tool rule rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/workflow/tool-permissions", strings.NewReader(`{"toolName":"plugin__bad path__echo","risk":"exec","decision":"ask"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed plugin tool rule rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/workflow/tool-permissions", strings.NewReader(`{"toolName":"UnknownTool","risk":"read","decision":"ask"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown tool create rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPatch, "/api/workflow/tool-permissions/"+created.ID, strings.NewReader(`{"toolName":"UnknownTool"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown tool update rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}

	registry.Register(workflowTestTool{name: "DynamicToolV2"})
	recorder = httptest.NewRecorder()
	routes.ServeHTTP(recorder, newTestRequest(http.MethodPatch, "/api/workflow/tool-permissions/"+created.ID, strings.NewReader(`{"toolName":"DynamicToolV2"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected dynamically registered tool update 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestToolPermissionRulesAPIRejectsInvalidValues(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	routes := app.Routes()

	cases := []string{
		`{"mode":"root","toolName":"Bash","risk":"exec","decision":"ask"}`,
		`{"mode":"acceptEdits","toolName":"Shell","risk":"exec","decision":"ask"}`,
		`{"mode":"acceptEdits","toolName":"Bash","risk":"network","decision":"ask"}`,
		`{"mode":"acceptEdits","toolName":"Bash","risk":"exec","decision":"maybe"}`,
		`{"mode":"acceptEdits","toolName":"Bash","risk":"danger","decision":"allow"}`,
		`{"mode":"acceptEdits","toolName":"Bash","risk":"*","decision":"allow"}`,
		`{"mode":"acceptEdits","toolName":"Bash","risk":"exec","decision":"ask","priority":10001}`,
	}
	for _, body := range cases {
		recorder := httptest.NewRecorder()
		routes.ServeHTTP(recorder, newTestRequest(http.MethodPost, "/api/workflow/tool-permissions", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, recorder.Code, recorder.Body.String())
		}
	}
}
