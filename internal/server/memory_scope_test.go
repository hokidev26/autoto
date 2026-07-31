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
)

func memoryScopeRoutes(t *testing.T) (http.Handler, *db.Store, db.Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, _, agent, err := store.CreateProject(ctx, "Scope", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	return New(config.Config{}, store, nil, nil).Routes(), store, agent
}

func decodeRetainResponse(t *testing.T, recorder *httptest.ResponseRecorder) retainAgentContextResponse {
	t.Helper()
	var response retainAgentContextResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRetainContextSummaryCreatesConversationOwnedMemory(t *testing.T) {
	routes, store, agent := memoryScopeRoutes(t)
	ctx := context.Background()

	// Nothing to retain yet: the endpoint must refuse rather than store an empty
	// memory the user would then have to clean up.
	empty := memoryAPICall(t, routes, http.MethodPost, "/api/agents/"+agent.ID+"/context/retain", map[string]any{})
	if empty.Code != http.StatusConflict {
		t.Fatalf("expected 409 without a summary, got %d: %s", empty.Code, empty.Body.String())
	}

	if err := store.UpdateAgentContextSummary(ctx, agent.ID, "decided to ship on Tuesdays", "", 20); err != nil {
		t.Fatal(err)
	}
	created := memoryAPICall(t, routes, http.MethodPost, "/api/agents/"+agent.ID+"/context/retain", map[string]any{
		"keywords": []string{" Tuesday "},
		"pinned":   true,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("expected retain 201, got %d: %s", created.Code, created.Body.String())
	}
	response := decodeRetainResponse(t, created)
	if response.Memory.AgentID != agent.ID {
		t.Fatalf("retained memory must belong to the conversation: %+v", response.Memory)
	}
	if response.Memory.Content != "decided to ship on Tuesdays" || !response.Memory.Pinned {
		t.Fatalf("unexpected retained memory: %+v", response.Memory)
	}
	if len(response.Memory.Keywords) != 1 || response.Memory.Keywords[0] != "tuesday" {
		t.Fatalf("keywords must be normalized: %+v", response.Memory.Keywords)
	}

	// The rolling summary is untouched: retaining copies it, it does not move it.
	current, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(current.ContextSummary) != "decided to ship on Tuesdays" {
		t.Fatalf("retain must not consume the rolling summary, got %q", current.ContextSummary)
	}

	missing := memoryAPICall(t, routes, http.MethodPost, "/api/agents/missing-agent/context/retain", map[string]any{})
	if missing.Code != http.StatusNotFound && missing.Code != http.StatusBadRequest {
		t.Fatalf("expected retain on an unknown agent to fail, got %d: %s", missing.Code, missing.Body.String())
	}
}

func TestMemoriesAPIScopeFilteringAndOwnedCreation(t *testing.T) {
	routes, _, agent := memoryScopeRoutes(t)

	globalCreated := memoryAPICall(t, routes, http.MethodPost, "/api/memories", map[string]any{
		"content":  "global note",
		"keywords": []string{"global"},
	})
	if globalCreated.Code != http.StatusCreated {
		t.Fatalf("expected global create 201, got %d: %s", globalCreated.Code, globalCreated.Body.String())
	}
	globalMemory := decodeMemoryResponse(t, globalCreated)
	if globalMemory.AgentID != "" {
		t.Fatalf("a memory without agentId must stay global: %+v", globalMemory)
	}

	ownedCreated := memoryAPICall(t, routes, http.MethodPost, "/api/memories", map[string]any{
		"content": "owned note",
		"agentId": agent.ID,
	})
	if ownedCreated.Code != http.StatusCreated {
		t.Fatalf("expected owned create 201, got %d: %s", ownedCreated.Code, ownedCreated.Body.String())
	}
	ownedMemory := decodeMemoryResponse(t, ownedCreated)
	if ownedMemory.AgentID != agent.ID {
		t.Fatalf("expected owned memory, got %+v", ownedMemory)
	}

	all := decodeMemoryListResponse(t, memoryAPICall(t, routes, http.MethodGet, "/api/memories", nil))
	if len(all) != 2 {
		t.Fatalf("default listing must include both scopes, got %+v", all)
	}

	globals := decodeMemoryListResponse(t, memoryAPICall(t, routes, http.MethodGet, "/api/memories?scope=global", nil))
	if len(globals) != 1 || globals[0].ID != globalMemory.ID {
		t.Fatalf("global scope must exclude owned memories, got %+v", globals)
	}

	owned := decodeMemoryListResponse(t, memoryAPICall(t, routes, http.MethodGet, "/api/memories?scope=agent&agentId="+agent.ID, nil))
	if len(owned) != 1 || owned[0].ID != ownedMemory.ID {
		t.Fatalf("agent scope must list only that conversation, got %+v", owned)
	}

	invalidScope := memoryAPICall(t, routes, http.MethodGet, "/api/memories?scope=sideways", nil)
	if invalidScope.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown scope, got %d: %s", invalidScope.Code, invalidScope.Body.String())
	}
	missingAgent := memoryAPICall(t, routes, http.MethodGet, "/api/memories?scope=agent", nil)
	if missingAgent.Code != http.StatusBadRequest && missingAgent.Code != http.StatusNotFound {
		t.Fatalf("expected agent scope without an id to fail, got %d: %s", missingAgent.Code, missingAgent.Body.String())
	}
	unknownOwner := memoryAPICall(t, routes, http.MethodPost, "/api/memories", map[string]any{
		"content": "orphan",
		"agentId": "missing-agent",
	})
	if unknownOwner.Code == http.StatusCreated {
		t.Fatalf("a memory owned by an unknown conversation must be rejected: %s", unknownOwner.Body.String())
	}
}
