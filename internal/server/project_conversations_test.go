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

func TestCreateProjectConversationReusesProjectAndHonorsIdempotency(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "project-conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := t.TempDir()
	app := New(config.Config{
		Paths: config.PathsConfig{DefaultProjectDir: t.TempDir()},
		Agent: config.AgentConfig{DefaultModel: "fake:default", DefaultPermissionMode: "acceptEdits"},
	}, store, nil, nil)

	create := func(target, body string, headers map[string]string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPost, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}
	firstPayload, err := json.Marshal(map[string]string{"name": "Demo", "gitPath": workspace, "model": "fake:first"})
	if err != nil {
		t.Fatal(err)
	}
	first := create("/api/projects", string(firstPayload), nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", first.Code, first.Body.String())
	}
	var initial struct {
		Project db.Project `json:"project"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Project.ID == "" {
		t.Fatal("project create did not return project")
	}

	path := "/api/projects/" + initial.Project.ID + "/conversations"
	second := create(path, `{"title":"Demo second","model":"fake:second"}`, map[string]string{"Idempotency-Key": "action-1"})
	if second.Code != http.StatusCreated {
		t.Fatalf("first explicit conversation status=%d body=%s", second.Code, second.Body.String())
	}
	var secondBody struct {
		Project  db.Project  `json:"project"`
		Workline db.Workline `json:"workline"`
		Agent    db.Agent    `json:"agent"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	duplicate := create(path, `{"title":"should-not-create","model":"fake:other"}`, map[string]string{"Idempotency-Key": "action-1"})
	if duplicate.Code != http.StatusOK {
		t.Fatalf("idempotent retry status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	var duplicateBody struct {
		Workline db.Workline `json:"workline"`
		Agent    db.Agent    `json:"agent"`
	}
	if err := json.Unmarshal(duplicate.Body.Bytes(), &duplicateBody); err != nil {
		t.Fatal(err)
	}
	if duplicateBody.Workline.ID != secondBody.Workline.ID || duplicateBody.Agent.ID != secondBody.Agent.ID {
		t.Fatalf("idempotent retry returned different hierarchy: first=%+v retry=%+v", secondBody, duplicateBody)
	}

	third := create(path, `{"title":"Demo third","model":"fake:third"}`, map[string]string{"Idempotency-Key": "action-2"})
	if third.Code != http.StatusCreated {
		t.Fatalf("second explicit conversation status=%d body=%s", third.Code, third.Body.String())
	}
	worklines, err := store.ListWorklinesByProject(ctx, initial.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(worklines) != 3 {
		t.Fatalf("expected one original plus two explicit conversations, got %d", len(worklines))
	}

	// A stale frontend cache must still reuse the filesystem project when the
	// directory flow falls back to the legacy project-create endpoint.
	fallbackPayload, err := json.Marshal(map[string]any{"name": "Demo", "gitPath": workspace, "model": "fake:fallback", "forceNewConversation": true, "idempotencyKey": "action-3"})
	if err != nil {
		t.Fatal(err)
	}
	fallback := create("/api/projects", string(fallbackPayload), map[string]string{"Idempotency-Key": "action-3"})
	if fallback.Code != http.StatusCreated {
		t.Fatalf("fallback directory conversation status=%d body=%s", fallback.Code, fallback.Body.String())
	}
	var fallbackBody struct {
		Project  db.Project  `json:"project"`
		Workline db.Workline `json:"workline"`
		Agent    db.Agent    `json:"agent"`
	}
	if err := json.Unmarshal(fallback.Body.Bytes(), &fallbackBody); err != nil {
		t.Fatal(err)
	}
	if fallbackBody.Project.ID != initial.Project.ID || fallbackBody.Workline.ID == secondBody.Workline.ID || fallbackBody.Agent.ID == secondBody.Agent.ID {
		t.Fatalf("fallback endpoint did not reuse project with a new conversation: %+v", fallbackBody)
	}
	fallbackRetryPayload, err := json.Marshal(map[string]any{"name": "changed", "gitPath": workspace, "forceNewConversation": true, "idempotencyKey": "action-3"})
	if err != nil {
		t.Fatal(err)
	}
	fallbackRetry := create("/api/projects", string(fallbackRetryPayload), map[string]string{"Idempotency-Key": "action-3"})
	if fallbackRetry.Code != http.StatusOK || !strings.Contains(fallbackRetry.Body.String(), fallbackBody.Agent.ID) {
		t.Fatalf("fallback idempotency was not honored: %d %s", fallbackRetry.Code, fallbackRetry.Body.String())
	}
	worklines, err = store.ListWorklinesByProject(ctx, initial.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(worklines) != 4 {
		t.Fatalf("expected four root conversations after fallback, got %d", len(worklines))
	}

	navigation := httptest.NewRecorder()
	app.Routes().ServeHTTP(navigation, newTestRequest(http.MethodGet, "/api/navigation", nil))
	if navigation.Code != http.StatusOK {
		t.Fatalf("navigation status=%d body=%s", navigation.Code, navigation.Body.String())
	}
	var nav struct {
		Conversations []db.NavigationConversation `json:"conversations"`
	}
	if err := json.Unmarshal(navigation.Body.Bytes(), &nav); err != nil {
		t.Fatal(err)
	}
	if len(nav.Conversations) != 4 {
		t.Fatalf("expected all project conversations in navigation, got %d: %+v", len(nav.Conversations), nav.Conversations)
	}

	archived := true
	if _, err := store.UpdateAgentNavigationState(ctx, secondBody.Agent.ID, nil, &archived); err != nil {
		t.Fatal(err)
	}
	navigation = httptest.NewRecorder()
	app.Routes().ServeHTTP(navigation, newTestRequest(http.MethodGet, "/api/navigation", nil))
	if navigation.Code != http.StatusOK || strings.Contains(navigation.Body.String(), secondBody.Agent.ID) {
		t.Fatalf("archiving one conversation changed navigation unexpectedly: %d %s", navigation.Code, navigation.Body.String())
	}
	if !strings.Contains(navigation.Body.String(), initial.Project.ID) {
		t.Fatalf("shared project disappeared after archiving one agent: %s", navigation.Body.String())
	}
}

func TestCreateWorklineConversationReusesWorklineAndHonorsIdempotency(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "workline-conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspace := t.TempDir()
	app := New(config.Config{
		Paths: config.PathsConfig{DefaultProjectDir: t.TempDir()},
		Agent: config.AgentConfig{DefaultModel: "fake:default", DefaultPermissionMode: "acceptEdits", DefaultStartInPlanMode: true},
	}, store, nil, nil)

	create := func(target, body string, headers map[string]string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPost, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}
	firstPayload, err := json.Marshal(map[string]string{"name": "Demo", "gitPath": workspace, "model": "fake:first"})
	if err != nil {
		t.Fatal(err)
	}
	first := create("/api/projects", string(firstPayload), nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", first.Code, first.Body.String())
	}
	var initial struct {
		Project  db.Project  `json:"project"`
		Workline db.Workline `json:"workline"`
		Agent    db.Agent    `json:"agent"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}

	path := "/api/worklines/" + initial.Workline.ID + "/conversations"
	second := create(path, `{"title":"Follow-up","model":"fake:second"}`, map[string]string{"Idempotency-Key": "workline-1"})
	if second.Code != http.StatusCreated {
		t.Fatalf("workline conversation status=%d body=%s", second.Code, second.Body.String())
	}
	var secondBody struct {
		Project  db.Project  `json:"project"`
		Workline db.Workline `json:"workline"`
		Agent    db.Agent    `json:"agent"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if secondBody.Project.ID != initial.Project.ID || secondBody.Workline.ID != initial.Workline.ID || secondBody.Agent.ID == initial.Agent.ID {
		t.Fatalf("expected another agent on the same workline: %+v", secondBody)
	}
	if secondBody.Agent.WorklineID != initial.Workline.ID || secondBody.Agent.Type != "primary" || secondBody.Agent.CWD != initial.Project.GitPath || !secondBody.Agent.PlanMode {
		t.Fatalf("unexpected workline conversation agent: %+v", secondBody.Agent)
	}
	duplicate := create(path, `{"title":"should-not-create","model":"fake:other"}`, map[string]string{"Idempotency-Key": "workline-1"})
	if duplicate.Code != http.StatusOK {
		t.Fatalf("idempotent retry status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	var duplicateBody struct {
		Agent db.Agent `json:"agent"`
	}
	if err := json.Unmarshal(duplicate.Body.Bytes(), &duplicateBody); err != nil {
		t.Fatal(err)
	}
	if duplicateBody.Agent.ID != secondBody.Agent.ID {
		t.Fatalf("idempotent retry returned a different agent: first=%s retry=%s", secondBody.Agent.ID, duplicateBody.Agent.ID)
	}

	missing := create("/api/worklines/missing/conversations", `{"title":"Nope","model":"fake:x"}`, nil)
	if missing.Code != http.StatusNotFound && missing.Code != http.StatusBadRequest {
		t.Fatalf("missing workline status=%d body=%s", missing.Code, missing.Body.String())
	}
}
