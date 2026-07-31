package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func newArchiveDeletionServer(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "archive-delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return New(config.Config{}, store, nil, nil), store
}

func archiveViaAPI(t *testing.T, app *Server, path string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPatch, path, strings.NewReader(`{"archived":true}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("archive %s failed with %d: %s", path, recorder.Code, recorder.Body.String())
	}
}

func deleteViaAPI(t *testing.T, app *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodDelete, path, nil))
	return recorder
}

func TestDeleteArchivedProjectRoute(t *testing.T) {
	app, store := newArchiveDeletionServer(t)
	ctx := context.Background()
	project, _, _, err := store.CreateProject(ctx, "Doomed", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	// Not archived yet: the route must refuse rather than delete.
	if response := deleteViaAPI(t, app, "/api/projects/"+project.ID); response.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a live project, got %d: %s", response.Code, response.Body.String())
	}

	archiveViaAPI(t, app, "/api/projects/"+project.ID+"/navigation-state")
	if response := deleteViaAPI(t, app, "/api/projects/"+project.ID); response.Code != http.StatusOK {
		t.Fatalf("expected 200 after archiving, got %d: %s", response.Code, response.Body.String())
	}
	if response := deleteViaAPI(t, app, "/api/projects/"+project.ID); response.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on repeat delete, got %d: %s", response.Code, response.Body.String())
	}
}

func TestDeleteArchivedConversationRoute(t *testing.T) {
	app, store := newArchiveDeletionServer(t)
	ctx := context.Background()
	_, _, agent, err := store.CreateProject(ctx, "Workspace", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	if response := deleteViaAPI(t, app, "/api/agents/"+agent.ID); response.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a live conversation, got %d: %s", response.Code, response.Body.String())
	}

	archiveViaAPI(t, app, "/api/agents/"+agent.ID+"/navigation-state")
	if response := deleteViaAPI(t, app, "/api/agents/"+agent.ID); response.Code != http.StatusOK {
		t.Fatalf("expected 200 after archiving, got %d: %s", response.Code, response.Body.String())
	}
	if _, err := store.GetAgent(ctx, agent.ID); !db.IsNotFound(err) {
		t.Fatalf("expected agent gone, got %v", err)
	}
}

func TestDeleteArchivedConversationRouteRejectsActiveRun(t *testing.T) {
	app, store := newArchiveDeletionServer(t)
	ctx := context.Background()
	_, _, agent, err := store.CreateProject(ctx, "Busy", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	archiveViaAPI(t, app, "/api/agents/"+agent.ID+"/navigation-state")
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, created_at, updated_at) VALUES (?, ?, 'running', ?, ?)`, db.NewID(), agent.ID, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}

	response := deleteViaAPI(t, app, "/api/agents/"+agent.ID)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409 while a run is active, got %d: %s", response.Code, response.Body.String())
	}
	if _, err := store.GetAgent(ctx, agent.ID); err != nil {
		t.Fatalf("agent must survive a refused delete: %v", err)
	}
}

func TestDeleteArchivedRoutesRejectUnknownIDs(t *testing.T) {
	app, _ := newArchiveDeletionServer(t)
	for _, path := range []string{"/api/projects/missing", "/api/agents/missing"} {
		if response := deleteViaAPI(t, app, path); response.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d: %s", path, response.Code, response.Body.String())
		}
	}
}
