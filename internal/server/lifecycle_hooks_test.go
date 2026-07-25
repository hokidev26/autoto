package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/db"
	"autoto/internal/hooks"
)

type lifecycleAPIGateway struct {
	fail  bool
	last  hooks.ShellRequest
	event hooks.Event
}

func (gateway *lifecycleAPIGateway) ExecuteShell(ctx context.Context, request hooks.ShellRequest) (hooks.GatewayResult, error) {
	gateway.last = request
	event, ok := hooks.EventFromContext(ctx)
	if !ok {
		return hooks.GatewayResult{}, errors.New("lifecycle test event context is missing")
	}
	gateway.event = event
	if gateway.fail {
		return hooks.GatewayResult{}, errors.New("Authorization: Bearer private-gateway-token")
	}
	return hooks.GatewayResult{Output: []byte(`{"ok":true,"token":"private-output"}`)}, nil
}
func (*lifecycleAPIGateway) ExecuteHTTP(context.Context, hooks.HTTPRequest) (hooks.GatewayResult, error) {
	return hooks.GatewayResult{}, errors.New("unexpected HTTP")
}
func (*lifecycleAPIGateway) ExecuteLLM(context.Context, hooks.LLMRequest) (hooks.GatewayResult, error) {
	return hooks.GatewayResult{}, errors.New("unexpected LLM")
}

func lifecycleAPITestAPI(t *testing.T, gateway hooks.Gateway) (*LifecycleHooksAPI, *db.Store, db.Agent) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "api-hooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, _, agent, err := store.CreateProject(context.Background(), "Lifecycle Hooks", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	return NewLifecycleHooksAPI(store, gateway), store, agent
}

func lifecycleAPIRequest(t *testing.T, api *LifecycleHooksAPI, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	api.Routes().ServeHTTP(recorder, request)
	return recorder
}

func lifecycleShellBody(expected any, name string) string {
	value := map[string]any{"name": name, "description": "test hook", "enabled": true, "event": "tool.after", "scope": map[string]any{"kind": "global"}, "priority": 20, "filter": map[string]any{"toolNames": []string{"read_*"}}, "mode": "async", "failurePolicy": "retry", "action": map[string]any{"kind": "shell", "shell": map[string]any{"executable": "audit-helper", "args": []string{"--json"}, "cwd": "tools/hooks", "secretRefs": map[string]string{"AUDIT_TOKEN": "env:AUDIT_TOKEN"}, "canonicalStdinV1": true}}}
	if expected != nil {
		value["expectedRevision"] = expected
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func decodeLifecycleHookAPIResponse(t *testing.T, recorder *httptest.ResponseRecorder) lifecycleHookResponse {
	t.Helper()
	var response lifecycleHookResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	return response
}

func TestLifecycleHooksAPICRUDCASAndWriteOnlySecrets(t *testing.T) {
	api, store, _ := lifecycleAPITestAPI(t, &lifecycleAPIGateway{})
	createdResponse := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/", lifecycleShellBody(nil, "Audit"))
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", createdResponse.Code, createdResponse.Body.String())
	}
	if strings.Contains(createdResponse.Body.String(), "env:AUDIT_TOKEN") || strings.Contains(createdResponse.Body.String(), "secretRefs") {
		t.Fatalf("secret ref echoed: %s", createdResponse.Body.String())
	}
	created := decodeLifecycleHookAPIResponse(t, createdResponse)
	if created.Revision != 1 || !created.Action.SecretConfigured["AUDIT_TOKEN"] {
		t.Fatalf("created=%+v", created)
	}
	list := lifecycleAPIRequest(t, api, http.MethodGet, "/api/lifecycle-hooks/", "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "env:AUDIT_TOKEN") {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	patchBody := strings.Replace(lifecycleShellBody(created.Revision, "Updated"), `,"secretRefs":{"AUDIT_TOKEN":"env:AUDIT_TOKEN"}`, "", 1)
	updatedResponse := lifecycleAPIRequest(t, api, http.MethodPatch, "/api/lifecycle-hooks/"+created.ID, patchBody)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("patch=%d %s body=%s", updatedResponse.Code, updatedResponse.Body.String(), patchBody)
	}
	updated := decodeLifecycleHookAPIResponse(t, updatedResponse)
	if updated.Revision != 2 || updated.Name != "Updated" || !updated.Action.SecretConfigured["AUDIT_TOKEN"] {
		t.Fatalf("updated=%+v", updated)
	}
	stored, err := store.GetLifecycleHook(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Action.Shell.SecretRefs["AUDIT_TOKEN"] != "env:AUDIT_TOKEN" {
		t.Fatalf("stored refs=%v", stored.Action.Shell.SecretRefs)
	}
	stale := lifecycleAPIRequest(t, api, http.MethodPatch, "/api/lifecycle-hooks/"+created.ID, lifecycleShellBody(1, "Stale"))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale=%d %s", stale.Code, stale.Body.String())
	}
	badDelete := lifecycleAPIRequest(t, api, http.MethodDelete, "/api/lifecycle-hooks/"+created.ID+"?expectedRevision=1", "")
	if badDelete.Code != http.StatusConflict {
		t.Fatalf("bad delete=%d %s", badDelete.Code, badDelete.Body.String())
	}
	deleted := lifecycleAPIRequest(t, api, http.MethodDelete, "/api/lifecycle-hooks/"+created.ID+"?expectedRevision=2", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
}

func TestLifecycleHooksAPIStrictValidation(t *testing.T) {
	api, _, _ := lifecycleAPITestAPI(t, &lifecycleAPIGateway{})
	invalid := []string{
		`{"name":"x","unknown":true}`,
		strings.Replace(lifecycleShellBody(nil, "bad"), `"cwd":"tools/hooks"`, `"cwd":"C:\\workspace"`, 1),
		strings.Replace(lifecycleShellBody(nil, "bad"), `"canonicalStdinV1":true`, `"canonicalStdinV1":true,"token":"plaintext"`, 1),
		strings.Replace(lifecycleShellBody(nil, "bad"), `"mode":"async"`, `"event":"run.before","mode":"async"`, 1),
	}
	for _, body := range invalid {
		response := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/", body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid body returned %d: %s body=%s", response.Code, response.Body.String(), body)
		}
	}
	oversized := `{"name":"` + strings.Repeat("x", int(lifecycleHookRequestBytes)) + `"}`
	response := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/", oversized)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized=%d", response.Code)
	}
}

func TestLifecycleHooksAPITestRejectsAgentOutsideHookScope(t *testing.T) {
	gateway := &lifecycleAPIGateway{}
	api, store, scopedAgent := lifecycleAPITestAPI(t, gateway)
	workline, err := store.GetWorkline(context.Background(), scopedAgent.WorklineID)
	if err != nil {
		t.Fatal(err)
	}
	hook, err := store.CreateLifecycleHook(context.Background(), hooks.Hook{
		Name: "project hook", Enabled: true, Event: hooks.EventToolAfter,
		Scope: hooks.Scope{Kind: hooks.ScopeProject, ID: workline.ProjectID}, Mode: hooks.ModeSync, FailurePolicy: hooks.FailureContinue,
		Action: hooks.Action{Kind: hooks.ActionShell, Shell: &hooks.ShellAction{Executable: "audit-helper", CanonicalStdinV1: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, otherAgent, err := store.CreateProject(context.Background(), "Other", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"event":{"name":"tool.after","agentId":"` + otherAgent.ID + `","toolName":"read_file"}}`
	response := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/"+hook.ID+"/test", body)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-project hook test=%d %s", response.Code, response.Body.String())
	}
	if gateway.event.AgentID != "" {
		t.Fatalf("cross-project hook reached executor: %+v", gateway.event)
	}

	globalHook, err := store.CreateLifecycleHook(context.Background(), hooks.Hook{
		Name: "global hook", Enabled: true, Event: hooks.EventToolAfter,
		Scope: hooks.Scope{Kind: hooks.ScopeGlobal}, Mode: hooks.ModeSync, FailurePolicy: hooks.FailureContinue,
		Action: hooks.Action{Kind: hooks.ActionShell, Shell: &hooks.ShellAction{Executable: "audit-helper", CanonicalStdinV1: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	api.authorizeTestAgent = func(w http.ResponseWriter, _ *http.Request, _ db.Agent) bool {
		writeError(w, http.StatusForbidden, "selected agent is not accessible")
		return false
	}
	response = lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/"+globalHook.ID+"/test", `{"event":{"name":"tool.after","agentId":"`+scopedAgent.ID+`"}}`)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "not accessible") {
		t.Fatalf("unauthorized global hook test=%d %s", response.Code, response.Body.String())
	}
}

func TestLifecycleHooksAPITestHistoryFailureRetryAndCancel(t *testing.T) {
	gateway := &lifecycleAPIGateway{}
	api, store, agent := lifecycleAPITestAPI(t, gateway)
	createdResponse := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/", lifecycleShellBody(nil, "Executable"))
	created := decodeLifecycleHookAPIResponse(t, createdResponse)
	missingAgent := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/"+created.ID+"/test", `{"event":{"name":"tool.after"}}`)
	if missingAgent.Code != http.StatusBadRequest {
		t.Fatalf("missing agent test=%d %s", missingAgent.Code, missingAgent.Body.String())
	}
	testBody := `{"event":{"name":"tool.after","agentId":"` + agent.ID + `","toolName":"read_file","payload":{"input":{"path":"README.md"}}}}`
	tested := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/"+created.ID+"/test", testBody)
	if tested.Code != http.StatusOK {
		t.Fatalf("test=%d %s", tested.Code, tested.Body.String())
	}
	if len(gateway.last.Stdin) == 0 || !strings.Contains(string(gateway.last.Stdin), `"schemaVersion":1`) {
		t.Fatalf("canonical stdin=%s", gateway.last.Stdin)
	}
	if gateway.event.RunKind != hooks.RunKindHookTest || !strings.HasPrefix(gateway.event.RunID, "hook-test-") {
		t.Fatalf("hook test did not use its isolated run identity: %+v", gateway.event)
	}
	runs, err := store.ListRuns(context.Background(), agent.ID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("hook testing polluted ordinary run history: runs=%+v err=%v", runs, err)
	}
	if strings.Contains(tested.Body.String(), "private-output") {
		t.Fatalf("test leaked output: %s", tested.Body.String())
	}
	history := lifecycleAPIRequest(t, api, http.MethodGet, "/api/lifecycle-hooks/"+created.ID+"/history?limit=10", "")
	if history.Code != http.StatusOK {
		t.Fatalf("history=%d %s", history.Code, history.Body.String())
	}
	var historyBody struct {
		History []lifecycleHookHistoryItem `json:"history"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &historyBody); err != nil {
		t.Fatal(err)
	}
	if len(historyBody.History) != 1 || historyBody.History[0].Execution.Status != hooks.ExecutionSucceeded || len(historyBody.History[0].Attempts) != 1 {
		t.Fatalf("history=%+v", historyBody)
	}
	gateway.fail = true
	failedResponse := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/"+created.ID+"/test", testBody)
	if failedResponse.Code != http.StatusBadGateway {
		t.Fatalf("failed test=%d %s", failedResponse.Code, failedResponse.Body.String())
	}
	if strings.Contains(failedResponse.Body.String(), "private-gateway-token") {
		t.Fatalf("gateway error leaked: %s", failedResponse.Body.String())
	}
	var failedBody struct {
		Execution db.LifecycleHookExecution `json:"execution"`
	}
	if err := json.Unmarshal(failedResponse.Body.Bytes(), &failedBody); err != nil {
		t.Fatal(err)
	}
	if failedBody.Execution.Status != hooks.ExecutionFailed {
		t.Fatalf("failed=%+v", failedBody)
	}
	retried := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hook-executions/"+failedBody.Execution.ID+"/retry", "")
	if retried.Code != http.StatusCreated {
		t.Fatalf("retry=%d %s", retried.Code, retried.Body.String())
	}
	var retry db.LifecycleHookExecution
	if err := json.Unmarshal(retried.Body.Bytes(), &retry); err != nil {
		t.Fatal(err)
	}
	if retry.RetryOfExecutionID != failedBody.Execution.ID || retry.Status != hooks.ExecutionPending {
		t.Fatalf("retry=%+v", retry)
	}
	cancelled := lifecycleAPIRequest(t, api, http.MethodPost, "/api/lifecycle-hooks/executions/"+retry.ID+"/cancel", "")
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel=%d %s", cancelled.Code, cancelled.Body.String())
	}
	var cancel db.LifecycleHookExecution
	if err := json.Unmarshal(cancelled.Body.Bytes(), &cancel); err != nil {
		t.Fatal(err)
	}
	if cancel.Status != hooks.ExecutionCancelled || !cancel.CancelRequested {
		t.Fatalf("cancel=%+v", cancel)
	}
	storedHistory, err := store.ListLifecycleHookExecutions(context.Background(), created.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedHistory) != 3 {
		t.Fatalf("stored history=%+v", storedHistory)
	}
}
