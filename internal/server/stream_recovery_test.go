package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
)

func newStreamRecoveryServer(t *testing.T) (*db.Store, *Server, db.Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "stream.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, agentRecord, err := store.CreateProject(ctx, "Stream", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, New(config.Config{}, store, nil, agentpkg.NewHub()), agentRecord
}

func TestAgentStreamStateUsesETagAndExecutionGeneration(t *testing.T) {
	store, app, agentRecord := newStreamRecoveryServer(t)
	defer store.Close()
	if _, err := store.CreateRun(context.Background(), db.Run{AgentID: agentRecord.ID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	handler := app.Routes()
	path := "/api/v2/agents/" + agentRecord.ID + "/stream-state"
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, newTestRequest(http.MethodGet, path, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected stream-state ETag")
	}
	var payload agentStreamStateResponse
	if err := json.Unmarshal(first.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExecutionGeneration != 1 || payload.Protocol != agentpkg.ProtocolVersion {
		t.Fatalf("unexpected stream state: %+v", payload)
	}
	secondRequest := newTestRequest(http.MethodGet, path, nil)
	secondRequest.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d: %s", second.Code, second.Body.String())
	}
}

func TestAgentLiveSnapshotReturnsMissedExecutionsSpecAndChildren(t *testing.T) {
	store, app, agentRecord := newStreamRecoveryServer(t)
	defer store.Close()
	ctx := context.Background()
	first, err := store.CreateRun(ctx, db.Run{AgentID: agentRecord.ID, Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRun(ctx, first.ID, "interrupted", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, db.Run{AgentID: agentRecord.ID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSpecTask(ctx, db.SpecTask{AgentID: agentRecord.ID, Text: "protected", Status: "todo", Protected: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(ctx, db.Agent{ParentAgentID: agentRecord.ID, Type: "subagent", Title: "child", Model: agentRecord.Model, PermissionMode: "readOnly", CWD: agentRecord.CWD}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agentRecord.ID, Role: "assistant", ContentText: "visible progress", ContentJSON: json.RawMessage(`[{"type":"text","text":"visible progress"}]`)}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	path := "/api/v2/agents/" + agentRecord.ID + "/live-snapshot?afterExecutionGeneration=0"
	app.Routes().ServeHTTP(response, newTestRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload agentLiveSnapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExecutionGeneration != 2 || len(payload.ExecutionsSince) != 2 {
		t.Fatalf("unexpected execution recovery: generation=%d runs=%+v", payload.ExecutionGeneration, payload.ExecutionsSince)
	}
	if payload.Spec == nil || len(payload.Spec.Tasks) != 1 || len(payload.ChildAgents) != 1 {
		t.Fatalf("snapshot did not include spec/children: %+v", payload)
	}
	if len(payload.Messages) != 1 || payload.Messages[0].ContentText != "visible progress" || strings.Contains(string(payload.Messages[0].ContentJSON), "server_") {
		t.Fatalf("snapshot did not recover only durable assistant progress: %+v", payload.Messages)
	}
}

func TestAgentLiveSnapshotIncludesProviderRetryWithoutErrorText(t *testing.T) {
	store, app, agentRecord := newStreamRecoveryServer(t)
	defer store.Close()
	const secret = "SECRET_PROVIDER_RETRY_DETAIL"
	app.hub.Publish(agentpkg.Event{
		Type:    "agent.provider_error_retry",
		AgentID: agentRecord.ID,
		Data: map[string]any{
			"attempt":     3,
			"maxAttempts": 6,
			"error":       secret,
			"runId":       "run-retry",
		},
	})
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, newTestRequest(http.MethodGet, "/api/v2/agents/"+agentRecord.ID+"/live-snapshot", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("live snapshot leaked the provider retry error text")
	}
	var payload agentLiveSnapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ProviderRetry == nil || payload.ProviderRetry.Attempt != 3 || payload.ProviderRetry.MaxAttempts != 6 || payload.ProviderRetry.RunID != "run-retry" {
		t.Fatalf("provider retry snapshot = %+v", payload.ProviderRetry)
	}
}
