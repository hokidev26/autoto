package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestMemoriesAPICRUDQueryPinArchiveAndRestore(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routes := New(config.Config{}, store, nil, nil).Routes()

	first := memoryAPICall(t, routes, http.MethodPost, "/api/memories", map[string]any{
		"content":  "Remember Café deployment",
		"keywords": []string{" Project ", "CAFÉ"},
		"pinned":   false,
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", first.Code, first.Body.String())
	}
	created := decodeMemoryResponse(t, first)
	if created.ID == "" || created.Content != "Remember Café deployment" || created.Pinned {
		t.Fatalf("unexpected created memory: %+v", created)
	}
	if len(created.Keywords) != 2 || created.Keywords[0] != "project" || created.Keywords[1] != "café" {
		t.Fatalf("expected normalized keywords, got %+v", created.Keywords)
	}

	second := memoryAPICall(t, routes, http.MethodPost, "/api/memories", map[string]any{
		"content":  "Pinned release checklist",
		"keywords": []string{"release"},
		"pinned":   true,
	})
	if second.Code != http.StatusCreated {
		t.Fatalf("expected second create 201, got %d: %s", second.Code, second.Body.String())
	}
	pinned := decodeMemoryResponse(t, second)

	listedResponse := memoryAPICall(t, routes, http.MethodGet, "/api/memories", nil)
	if listedResponse.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listedResponse.Code, listedResponse.Body.String())
	}
	listed := decodeMemoryListResponse(t, listedResponse)
	if len(listed) != 2 || listed[0].ID != pinned.ID || !listed[0].Pinned {
		t.Fatalf("expected pinned memory first, got %+v", listed)
	}

	queryResponse := memoryAPICall(t, routes, http.MethodGet, "/api/memories?q="+url.QueryEscape("café"), nil)
	if queryResponse.Code != http.StatusOK {
		t.Fatalf("expected query 200, got %d: %s", queryResponse.Code, queryResponse.Body.String())
	}
	queried := decodeMemoryListResponse(t, queryResponse)
	if len(queried) != 1 || queried[0].ID != created.ID {
		t.Fatalf("unexpected query result: %+v", queried)
	}

	getResponse := memoryAPICall(t, routes, http.MethodGet, "/api/memories/"+created.ID, nil)
	if getResponse.Code != http.StatusOK || decodeMemoryResponse(t, getResponse).ID != created.ID {
		t.Fatalf("expected get 200 for created memory, got %d: %s", getResponse.Code, getResponse.Body.String())
	}

	patchResponse := memoryAPICall(t, routes, http.MethodPatch, "/api/memories/"+created.ID, map[string]any{
		"content":  "Updated deployment preference",
		"keywords": []string{" Updated ", "Go"},
		"pinned":   true,
	})
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("expected patch 200, got %d: %s", patchResponse.Code, patchResponse.Body.String())
	}
	updated := decodeMemoryResponse(t, patchResponse)
	if updated.Content != "Updated deployment preference" || !updated.Pinned || len(updated.Keywords) != 2 || updated.Keywords[0] != "updated" {
		t.Fatalf("unexpected updated memory: %+v", updated)
	}

	archiveResponse := memoryAPICall(t, routes, http.MethodPatch, "/api/memories/"+created.ID, map[string]any{"archived": true})
	if archiveResponse.Code != http.StatusOK {
		t.Fatalf("expected archive 200, got %d: %s", archiveResponse.Code, archiveResponse.Body.String())
	}
	archived := decodeMemoryResponse(t, archiveResponse)
	if archived.ArchivedAt == "" {
		t.Fatalf("expected archived timestamp: %+v", archived)
	}
	if _, err := time.Parse(time.RFC3339Nano, archived.ArchivedAt); err != nil {
		t.Fatalf("expected RFC3339 archived timestamp, got %q: %v", archived.ArchivedAt, err)
	}
	if archived.Content != updated.Content || !archived.Pinned || len(archived.Keywords) != len(updated.Keywords) {
		t.Fatalf("archive-only patch did not preserve fields: before=%+v after=%+v", updated, archived)
	}

	hiddenResponse := memoryAPICall(t, routes, http.MethodGet, "/api/memories?q=updated", nil)
	if hiddenResponse.Code != http.StatusOK || len(decodeMemoryListResponse(t, hiddenResponse)) != 0 {
		t.Fatalf("archived memory must be hidden by default, got %d: %s", hiddenResponse.Code, hiddenResponse.Body.String())
	}
	includedResponse := memoryAPICall(t, routes, http.MethodGet, "/api/memories?q=updated&includeArchived=true", nil)
	included := decodeMemoryListResponse(t, includedResponse)
	if includedResponse.Code != http.StatusOK || len(included) != 1 || included[0].ID != created.ID || included[0].ArchivedAt == "" {
		t.Fatalf("expected archived memory when requested, got %d: %+v", includedResponse.Code, included)
	}

	restoreResponse := memoryAPICall(t, routes, http.MethodPatch, "/api/memories/"+created.ID, map[string]any{"archived": false})
	if restoreResponse.Code != http.StatusOK {
		t.Fatalf("expected restore 200, got %d: %s", restoreResponse.Code, restoreResponse.Body.String())
	}
	restored := decodeMemoryResponse(t, restoreResponse)
	if restored.ArchivedAt != "" || restored.Content != updated.Content || !restored.Pinned {
		t.Fatalf("unexpected restored memory: %+v", restored)
	}
	visibleAgain := decodeMemoryListResponse(t, memoryAPICall(t, routes, http.MethodGet, "/api/memories?q=updated", nil))
	if len(visibleAgain) != 1 || visibleAgain[0].ID != created.ID {
		t.Fatalf("expected restored memory in default list, got %+v", visibleAgain)
	}

	deleteResponse := memoryAPICall(t, routes, http.MethodDelete, "/api/memories/"+created.ID, nil)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var deleted map[string]bool
	if err := json.NewDecoder(deleteResponse.Body).Decode(&deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted["deleted"] {
		t.Fatalf("unexpected delete response: %+v", deleted)
	}

	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		response := memoryAPICall(t, routes, method, "/api/memories/missing", map[string]any{"pinned": true})
		if response.Code != http.StatusNotFound {
			t.Fatalf("expected %s missing memory to return 404, got %d: %s", method, response.Code, response.Body.String())
		}
	}
}

func TestMemoriesAPIRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routes := New(config.Config{}, store, nil, nil).Routes()

	invalidCreates := []string{
		`{"content":"valid","keywords":[],"pinned":false,"unknown":true}`,
		`{"content":"valid","keywords":[],"pinned":"true"}`,
		`{"content":"   ","keywords":[],"pinned":false}`,
		`{"content":"valid","keywords":[" "],"pinned":false}`,
	}
	for _, body := range invalidCreates {
		response := memoryAPIRawCall(routes, http.MethodPost, "/api/memories", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected invalid create to return 400, got %d for %s: %s", response.Code, body, response.Body.String())
		}
	}

	createdResponse := memoryAPICall(t, routes, http.MethodPost, "/api/memories", map[string]any{
		"content": "valid", "keywords": []string{"keyword"}, "pinned": false,
	})
	created := decodeMemoryResponse(t, createdResponse)
	invalidPatches := []string{
		`{"archived":"true"}`,
		`{"pinned":1}`,
		`{"unknown":true}`,
		`{"content":"   "}`,
		`{"keywords":[""]}`,
	}
	for _, body := range invalidPatches {
		response := memoryAPIRawCall(routes, http.MethodPatch, "/api/memories/"+created.ID, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected invalid patch to return 400, got %d for %s: %s", response.Code, body, response.Body.String())
		}
	}

	invalidQuery := memoryAPICall(t, routes, http.MethodGet, "/api/memories?includeArchived=not-a-bool", nil)
	if invalidQuery.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid includeArchived to return 400, got %d: %s", invalidQuery.Code, invalidQuery.Body.String())
	}

	if got := statusFromMemoryError(fmt.Errorf("%w: duplicate", db.ErrConflict)); got != http.StatusConflict {
		t.Fatalf("expected conflict mapping 409, got %d", got)
	}
}

func memoryAPICall(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	return memoryAPIRequest(handler, method, target, encoded)
}

func memoryAPIRawCall(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	return memoryAPIRequest(handler, method, target, []byte(body))
}

func memoryAPIRequest(handler http.Handler, method, target string, body []byte) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := newTestRequest(method, target, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeMemoryResponse(t *testing.T, recorder *httptest.ResponseRecorder) db.Memory {
	t.Helper()
	var memory db.Memory
	if err := json.NewDecoder(recorder.Body).Decode(&memory); err != nil {
		t.Fatal(err)
	}
	return memory
}

func decodeMemoryListResponse(t *testing.T, recorder *httptest.ResponseRecorder) []db.Memory {
	t.Helper()
	var memories []db.Memory
	if err := json.NewDecoder(recorder.Body).Decode(&memories); err != nil {
		t.Fatal(err)
	}
	return memories
}

func TestMemoryIDORAndRestrictedRemoteMutations(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "memories-acl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{
		Auth: config.AuthConfig{RegistrationOpen: true},
		Security: config.SecurityConfig{
			AccessPasswordHash:      hash,
			AllowRemoteFullAccess:   true,
			DefaultRemoteAccessMode: remoteAccessModeRestricted,
			CredentialRevision:      1,
		},
	}, store, nil, nil)

	ownerCookie := registerCollaborationTestUser(t, app, "mem-owner")
	outsiderCookie := registerCollaborationTestUser(t, app, "mem-outsider")
	owner, _, err := store.GetUserByHandle(ctx, "mem-owner")
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProjectForUser(ctx, owner.ID, "Owned", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.CreateMemory(ctx, db.Memory{AgentID: agent.ID, Content: "private note", Keywords: []string{"private"}})
	if err != nil {
		t.Fatal(err)
	}

	outsiderGet := httptest.NewRecorder()
	req := newTestRequest(http.MethodGet, "/api/memories/"+memory.ID, nil)
	req.AddCookie(outsiderCookie)
	app.Routes().ServeHTTP(outsiderGet, req)
	if outsiderGet.Code != http.StatusNotFound {
		t.Fatalf("outsider must not read another conversation's memory, got %d: %s", outsiderGet.Code, outsiderGet.Body.String())
	}

	ownerGet := httptest.NewRecorder()
	req = newTestRequest(http.MethodGet, "/api/memories/"+memory.ID, nil)
	req.AddCookie(ownerCookie)
	app.Routes().ServeHTTP(ownerGet, req)
	if ownerGet.Code != http.StatusOK {
		t.Fatalf("owner should read their memory, got %d: %s", ownerGet.Code, ownerGet.Body.String())
	}

	sessionToken, _, err := app.newRemoteAccessSession(remoteAccessModeRestricted)
	if err != nil {
		t.Fatal(err)
	}
	remotePatch := httptest.NewRecorder()
	req = newTestRequest(http.MethodPatch, "/api/memories/"+memory.ID, strings.NewReader(`{"content":"pwned"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "remote.example.test"
	markRemoteHTTPS(req)
	req.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: sessionToken})
	app.Routes().ServeHTTP(remotePatch, req)
	if remotePatch.Code != http.StatusForbidden {
		t.Fatalf("restricted remote must not mutate memories, got %d: %s", remotePatch.Code, remotePatch.Body.String())
	}
}
