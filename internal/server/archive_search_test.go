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

func TestArchiveSearchFindsArchivedTranscriptsAndRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "archive-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, _, archivedAgent, err := store.CreateProject(ctx, "Archived workspace", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, liveAgent, err := store.CreateProject(ctx, "Live workspace", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: archivedAgent.ID, Role: "user", ContentText: "the dashboard heatmap uses api_key=supersecret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: liveAgent.ID, Role: "user", ContentText: "the live heatmap should stay out of archive search"}); err != nil {
		t.Fatal(err)
	}
	archived := true
	if _, err := store.UpdateAgentNavigationState(ctx, archivedAgent.ID, nil, &archived); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/archive/search?q=heatmap", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "supersecret") {
		t.Fatalf("archive search leaked a secret: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("expected redacted snippet: %s", body)
	}
	if strings.Contains(body, liveAgent.ID) {
		t.Fatalf("live conversation leaked into archive search: %s", body)
	}

	var response archiveSearchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Query != "heatmap" || len(response.Results) != 1 || response.Results[0].AgentID != archivedAgent.ID {
		t.Fatalf("unexpected search payload: %+v", response)
	}
	if len(response.Results[0].Matches) != 1 || response.Results[0].Matches[0].Snippet == "" {
		t.Fatalf("expected a snippet: %+v", response.Results[0])
	}
}

func TestArchiveSearchRejectsEmptyQuery(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "archive-search-empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/archive/search?q=%20", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
