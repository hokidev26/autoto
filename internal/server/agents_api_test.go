package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestMessageContextPermissionCapsStayNarrow(t *testing.T) {
	tests := []struct {
		context    string
		wantCap    string
		wantSource string
		wantErr    bool
	}{
		{context: "conversation", wantCap: "readOnly", wantSource: db.RunSourceConversation},
		{context: "project", wantCap: "", wantSource: db.RunSourceManual},
		{context: "", wantCap: "", wantSource: db.RunSourceManual},
		{context: "unknown", wantErr: true},
	}
	for _, test := range tests {
		got, err := messageContextPermissionModeCap(test.context)
		if test.wantErr {
			if err == nil {
				t.Fatalf("messageContextPermissionModeCap(%q) expected an error", test.context)
			}
			continue
		}
		if err != nil || got != test.wantCap {
			t.Fatalf("messageContextPermissionModeCap(%q) = %q, %v; want %q", test.context, got, err, test.wantCap)
		}
		if source := messageContextRunSource(test.context); source != test.wantSource {
			t.Fatalf("messageContextRunSource(%q) = %q; want %q", test.context, source, test.wantSource)
		}
	}
	if got := narrowPermissionModeCaps("acceptEdits", "readOnly"); got != "readOnly" {
		t.Fatalf("conversation cap must narrow remote/project permissions, got %q", got)
	}
	if got := narrowPermissionModeCaps("", "acceptEdits"); got != "acceptEdits" {
		t.Fatalf("remote cap must remain effective, got %q", got)
	}
}

func TestAgentCollectionsAndCreationRespectMembership(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)

	registerIsolationAdmin(t, app)
	ownerCookie := registerCollaborationTestUser(t, app, "owner")
	owner, _, err := store.GetUserByHandle(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerProject, ownerWorkline, ownerAgent, err := store.CreateProjectForUser(ctx, owner.ID, "Owner", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_ = ownerProject

	outsiderCookie := registerCollaborationTestUser(t, app, "outsider")
	outsider, _, err := store.GetUserByHandle(ctx, "outsider")
	if err != nil {
		t.Fatal(err)
	}
	_, outsiderWorkline, outsiderAgent, err := store.CreateProjectForUser(ctx, outsider.ID, "Outsider", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	for _, prefix := range []string{"/api/agents", "/api/narrators"} {
		ownerResponse := agentAPIRequest(app.Routes(), http.MethodGet, prefix, nil, ownerCookie)
		if ownerResponse.Code != http.StatusOK || !bytes.Contains(ownerResponse.Body.Bytes(), []byte(ownerAgent.ID)) || bytes.Contains(ownerResponse.Body.Bytes(), []byte(outsiderAgent.ID)) {
			t.Fatalf("owner %s collection leaked another tenant: %d %s", prefix, ownerResponse.Code, ownerResponse.Body.String())
		}
		outsiderResponse := agentAPIRequest(app.Routes(), http.MethodGet, prefix, nil, outsiderCookie)
		if outsiderResponse.Code != http.StatusOK || !bytes.Contains(outsiderResponse.Body.Bytes(), []byte(outsiderAgent.ID)) || bytes.Contains(outsiderResponse.Body.Bytes(), []byte(ownerAgent.ID)) {
			t.Fatalf("outsider %s collection leaked another tenant: %d %s", prefix, outsiderResponse.Code, outsiderResponse.Body.String())
		}
		unauthenticated := agentAPIRequest(app.Routes(), http.MethodGet, prefix, nil, nil)
		if unauthenticated.Code != http.StatusUnauthorized {
			t.Fatalf("installed-user %s collection must require login, got %d: %s", prefix, unauthenticated.Code, unauthenticated.Body.String())
		}
	}

	for _, payload := range []map[string]any{
		{"worklineId": outsiderWorkline.ID, "title": "forbidden workline", "model": "fake:test", "permissionMode": "acceptEdits", "cwd": t.TempDir()},
		{"worklineId": ownerWorkline.ID, "parentAgentId": outsiderAgent.ID, "title": "forbidden parent", "model": "fake:test", "permissionMode": "acceptEdits", "cwd": t.TempDir()},
	} {
		response := agentAPIRequest(app.Routes(), http.MethodPost, "/api/agents", payload, ownerCookie)
		if response.Code != http.StatusNotFound {
			t.Fatalf("cross-tenant agent creation must be hidden, got %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestAgentTitleUpdatePersistsAndRejectsStaleGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, created, err := store.CreateProject(ctx, "Titles", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.GetAgent(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)

	updatedResponse := agentAPIRequest(app.Routes(), http.MethodPatch, "/api/agents/"+agent.ID+"/title", map[string]any{
		"title": "  Editable conversation  ", "entityGeneration": agent.EntityGeneration,
	}, nil)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update title: %d %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updated db.Agent
	if err := json.NewDecoder(updatedResponse.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Editable conversation" || updated.EntityGeneration != agent.EntityGeneration+1 {
		t.Fatalf("unexpected updated agent: %+v", updated)
	}
	persisted, err := store.GetAgent(ctx, agent.ID)
	if err != nil || persisted.Title != updated.Title {
		t.Fatalf("title did not persist: %+v err=%v", persisted, err)
	}

	stale := agentAPIRequest(app.Routes(), http.MethodPatch, "/api/narrators/"+agent.ID+"/title", map[string]any{
		"title": "Stale title", "entityGeneration": agent.EntityGeneration,
	}, nil)
	if stale.Code != http.StatusConflict {
		t.Fatalf("expected stale title conflict, got %d: %s", stale.Code, stale.Body.String())
	}
	for _, payload := range []map[string]any{
		{"title": ""},
		{"title": "line one\nline two"},
		{"title": nil},
		{"title": "valid", "unknown": true},
	} {
		response := agentAPIRequest(app.Routes(), http.MethodPatch, "/api/agents/"+agent.ID+"/title", payload, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected invalid title rejection for %+v, got %d: %s", payload, response.Code, response.Body.String())
		}
	}
}

func TestAgentCollectionsRemainCompatibleWithoutUsers(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, workline, agent, err := store.CreateProject(ctx, "Standalone", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET system_prompt = 'SYSTEM_PROMPT_SECRET' WHERE id = ?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)

	listed := agentAPIRequest(app.Routes(), http.MethodGet, "/api/agents", nil, nil)
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(agent.ID)) {
		t.Fatalf("userless installation must list agents, got %d: %s", listed.Code, listed.Body.String())
	}
	if bytes.Contains(listed.Body.Bytes(), []byte("SYSTEM_PROMPT_SECRET")) {
		t.Fatalf("agent collection leaked the system prompt: %s", listed.Body.String())
	}
	detail := agentAPIRequest(app.Routes(), http.MethodGet, "/api/agents/"+agent.ID, nil, nil)
	if detail.Code != http.StatusOK || bytes.Contains(detail.Body.Bytes(), []byte("SYSTEM_PROMPT_SECRET")) {
		t.Fatalf("agent detail leaked the system prompt: %d %s", detail.Code, detail.Body.String())
	}
	created := agentAPIRequest(app.Routes(), http.MethodPost, "/api/narrators", map[string]any{
		"worklineId": workline.ID, "title": "compatible", "model": "fake:test", "permissionMode": "acceptEdits", "cwd": t.TempDir(),
	}, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("userless installation must create agents, got %d: %s", created.Code, created.Body.String())
	}
}

func agentAPIRequest(handler http.Handler, method, target string, payload any, cookie *http.Cookie) *httptest.ResponseRecorder {
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		body = bytes.NewReader(encoded)
	}
	request := newTestRequest(method, target, body)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
