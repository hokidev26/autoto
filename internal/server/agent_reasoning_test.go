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
	"autoto/internal/providers"
)

func TestUpdateAgentReasoningEffortRoute(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "gemini:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/reasoning-effort", strings.NewReader(`{"reasoningEffort":"high"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var updated db.Agent
	if err := json.NewDecoder(recorder.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.ReasoningEffort != "high" {
		t.Fatalf("unexpected agent response: %+v", updated)
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/reasoning-effort", strings.NewReader(`{"reasoningEffort":"maximum"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid effort to be rejected, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateAgentSummaryModelRoute(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "summary-model.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "ready:chat", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "ready"})
	registry.Register(fakeModelProvider{name: "compact"})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "ready", Type: "openai-compatible", APIKeyOptional: true},
		{Name: "compact", Type: "openai-compatible", APIKeyOptional: true},
	}}}, store, nil, nil, registry)

	patch := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/summary-model", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}
	set := patch(`{"summaryModel":"compact:mini"}`)
	if set.Code != http.StatusOK {
		t.Fatalf("set summary model: %d %s", set.Code, set.Body.String())
	}
	var updated db.Agent
	if err := json.NewDecoder(set.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.SummaryModel != "compact:mini" {
		t.Fatalf("unexpected summary model: %+v", updated)
	}
	got, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SummaryModel != "compact:mini" {
		t.Fatalf("summary model did not persist: %+v", got)
	}
	if response := patch(`{"summaryModel":"missing:model"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown summary model should be rejected: %d %s", response.Code, response.Body.String())
	}
	clear := patch(`{"summaryModel":""}`)
	if clear.Code != http.StatusOK {
		t.Fatalf("clear summary model: %d %s", clear.Code, clear.Body.String())
	}
	var cleared db.Agent
	if err := json.NewDecoder(clear.Body).Decode(&cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.SummaryModel != "" {
		t.Fatalf("empty summary model must inherit host default: %+v", cleared)
	}
	got, err = store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SummaryModel != "" {
		t.Fatalf("cleared summary model did not persist: %+v", got)
	}
}
