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

func TestGuestObserveAllowlist(t *testing.T) {
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/ui/styles.css"},
		{http.MethodHead, "/ui/app.js"},
		{http.MethodGet, "/api/health"},
		{http.MethodGet, "/api/auth/me"},
		{http.MethodGet, "/api/preferences"},
		{http.MethodPatch, "/api/preferences"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/projects"},
		{http.MethodGet, "/api/projects/p1"},
		{http.MethodGet, "/api/projects/p1/worklines"},
		{http.MethodGet, "/api/worklines/w1"},
		{http.MethodGet, "/api/worklines/w1/agents"},
		{http.MethodGet, "/api/agents/a1"},
		{http.MethodGet, "/api/agents/a1/messages"},
		{http.MethodGet, "/api/agents/a1/live-snapshot"},
		{http.MethodGet, "/api/agents/a1/messages/m1/attachments/x"},
		{http.MethodGet, "/api/agents/a1/messages/m1/generated-images/g1"},
		{http.MethodGet, "/api/agents/a1/runs"},
		{http.MethodGet, "/api/agents/a1/runs/r1"},
		{http.MethodGet, "/api/agents/a1/runs/r1/tool-calls"},
		{http.MethodGet, "/api/agents/a1/tool-calls/pending"},
		{http.MethodGet, "/api/agents/a1/tool-calls/t1"},
		{http.MethodGet, "/ws/agent"},
	}
	denied := []struct{ method, path string }{
		{http.MethodPost, "/api/agents/a1/messages"},
		{http.MethodPost, "/api/agents/a1/tool-calls/t1/approval"},
		{http.MethodGet, "/api/settings"},
		{http.MethodGet, "/api/models"},
		{http.MethodGet, "/api/providers"},
		{http.MethodPost, "/api/conversations"},
		{http.MethodPost, "/api/projects"},
		{http.MethodGet, "/api/agents/a1/git/status"},
		{http.MethodGet, "/api/agents/a1/workspace/tree"},
		{http.MethodGet, "/api/agents/a1/context"},
		{http.MethodGet, "/ws/terminal"},
		{http.MethodPost, "/api/preferences/import-local"},
		{http.MethodGet, "/api/users/accounts"},
		{http.MethodPatch, "/api/agents/a1/model"},
	}
	for _, item := range allowed {
		req := httptest.NewRequest(item.method, item.path, nil)
		if !guestObserveAllowed(req) {
			t.Fatalf("expected allow %s %s", item.method, item.path)
		}
	}
	for _, item := range denied {
		req := httptest.NewRequest(item.method, item.path, nil)
		if guestObserveAllowed(req) {
			t.Fatalf("expected deny %s %s", item.method, item.path)
		}
	}
}

func TestGuestAccessIsServerEnforced(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "guest-access.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _, agent, err := store.CreateProject(ctx, "Shared", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)
	routes := app.Routes()
	adminCookie := registerCollaborationTestUser(t, app, "host")

	createBody, _ := json.Marshal(createGuestRequest{
		Handle:     "viewer",
		Password:   "correct horse battery staple",
		ProjectIDs: []string{project.ID},
		IssueKey:   true,
		KeyLabel:   "phone",
	})
	created := httptest.NewRecorder()
	req := newTestRequest(http.MethodPost, "/api/users/guests", strings.NewReader(string(createBody)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	routes.ServeHTTP(created, req)
	if created.Code != http.StatusCreated {
		t.Fatalf("create guest: %d %s", created.Code, created.Body.String())
	}
	var guest createGuestResponse
	if err := json.Unmarshal(created.Body.Bytes(), &guest); err != nil {
		t.Fatal(err)
	}
	if guest.Role != "guest" || guest.AccessKey == "" || !strings.HasPrefix(guest.AccessKey, userAccessKeyPrefix) {
		t.Fatalf("unexpected guest payload: %+v", guest)
	}

	login := httptest.NewRecorder()
	loginReq := newTestRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"handle":"viewer","password":"correct horse battery staple"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("guest password login: %d %s", login.Code, login.Body.String())
	}
	guestCookie := login.Result().Cookies()[0]

	assertStatus := func(cookie *http.Cookie, method, path, body string, want int, wantBody string) {
		t.Helper()
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		recorder := httptest.NewRecorder()
		request := newTestRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			request.AddCookie(cookie)
		}
		routes.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, recorder.Code, want, recorder.Body.String())
		}
		if wantBody != "" && !strings.Contains(recorder.Body.String(), wantBody) {
			t.Fatalf("%s %s: body %q does not contain %q", method, path, recorder.Body.String(), wantBody)
		}
	}

	assertStatus(guestCookie, http.MethodGet, "/api/agents/"+agent.ID+"/messages", "", http.StatusOK, "")
	assertStatus(guestCookie, http.MethodPost, "/api/agents/"+agent.ID+"/messages", `{"text":"hello"}`, http.StatusForbidden, guestAccessDenied)
	assertStatus(guestCookie, http.MethodGet, "/api/settings", "", http.StatusForbidden, guestAccessDenied)
	assertStatus(guestCookie, http.MethodGet, "/api/models", "", http.StatusForbidden, guestAccessDenied)
	assertStatus(guestCookie, http.MethodGet, "/api/users/accounts", "", http.StatusForbidden, guestAccessDenied)
	assertStatus(adminCookie, http.MethodGet, "/api/users/accounts", "", http.StatusOK, `"handle":"viewer"`)
	accountsList := httptest.NewRecorder()
	accountsReq := newTestRequest(http.MethodGet, "/api/users/accounts", nil)
	accountsReq.AddCookie(adminCookie)
	routes.ServeHTTP(accountsList, accountsReq)
	if strings.Contains(accountsList.Body.String(), `"tokenHash"`) || strings.Contains(accountsList.Body.String(), `"accessKey"`) || strings.Contains(accountsList.Body.String(), "atk_") {
		t.Fatalf("account listing leaked access key material: %s", accountsList.Body.String())
	}

	secret, _, secretAgent, err := store.CreateProject(ctx, "Secret", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	adminUser, _, err := store.GetUserByHandle(ctx, "host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProjectMember(ctx, db.ProjectMember{ProjectID: secret.ID, UserID: adminUser.ID, Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	assertStatus(guestCookie, http.MethodGet, "/api/agents/"+secretAgent.ID+"/messages", "", http.StatusNotFound, "resource not found")
	assertStatus(guestCookie, http.MethodGet, "/api/projects", "", http.StatusOK, `"name":"Shared"`)
	projectsList := httptest.NewRecorder()
	projectsReq := newTestRequest(http.MethodGet, "/api/projects", nil)
	projectsReq.AddCookie(guestCookie)
	routes.ServeHTTP(projectsList, projectsReq)
	if projectsList.Code != http.StatusOK {
		t.Fatalf("guest list projects: %d %s", projectsList.Code, projectsList.Body.String())
	}
	if strings.Contains(projectsList.Body.String(), secret.ID) || strings.Contains(projectsList.Body.String(), `"name":"Secret"`) {
		t.Fatalf("guest project list leaked ungranted project: %s", projectsList.Body.String())
	}

	prefs := httptest.NewRecorder()
	prefReq := newTestRequest(http.MethodGet, "/api/preferences", nil)
	prefReq.AddCookie(guestCookie)
	routes.ServeHTTP(prefs, prefReq)
	if prefs.Code != http.StatusOK {
		t.Fatalf("guest get preferences: %d %s", prefs.Code, prefs.Body.String())
	}
	var preferences accountPreferencesResponse
	if err := json.Unmarshal(prefs.Body.Bytes(), &preferences); err != nil {
		t.Fatal(err)
	}
	profileBody, _ := json.Marshal(map[string]any{
		"expectedRevision": preferences.Revision,
		"profile": map[string]any{
			"displayName":    "Viewer",
			"roleLabel":      "Guest",
			"avatarInitials": "VW",
			"workspaceLabel": "Shared",
		},
	})
	assertStatus(guestCookie, http.MethodPatch, "/api/preferences", string(profileBody), http.StatusOK, `"displayName":"Viewer"`)
	modelBody, _ := json.Marshal(map[string]any{
		"expectedRevision": preferences.Revision + 1,
		"preferredModel":   "fake:test",
	})
	assertStatus(guestCookie, http.MethodPatch, "/api/preferences", string(modelBody), http.StatusForbidden, guestAccessDenied)

	keyLogin := httptest.NewRecorder()
	keyReq := newTestRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"accessKey":"`+guest.AccessKey+`"}`))
	keyReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(keyLogin, keyReq)
	if keyLogin.Code != http.StatusOK || !strings.Contains(keyLogin.Body.String(), `"handle":"viewer"`) {
		t.Fatalf("access key login: %d %s", keyLogin.Code, keyLogin.Body.String())
	}

	keyOnlyBody, _ := json.Marshal(createGuestRequest{Handle: "key-guest", ProjectIDs: []string{project.ID}})
	keyOnly := httptest.NewRecorder()
	keyOnlyReq := newTestRequest(http.MethodPost, "/api/users/guests", strings.NewReader(string(keyOnlyBody)))
	keyOnlyReq.Header.Set("Content-Type", "application/json")
	keyOnlyReq.AddCookie(adminCookie)
	routes.ServeHTTP(keyOnly, keyOnlyReq)
	if keyOnly.Code != http.StatusCreated {
		t.Fatalf("key-only guest: %d %s", keyOnly.Code, keyOnly.Body.String())
	}
	var keyGuest createGuestResponse
	if err := json.Unmarshal(keyOnly.Body.Bytes(), &keyGuest); err != nil {
		t.Fatal(err)
	}
	passwordLogin := httptest.NewRecorder()
	passwordReq := newTestRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"handle":"key-guest","password":"correct horse battery staple"}`))
	passwordReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(passwordLogin, passwordReq)
	if passwordLogin.Code != http.StatusUnauthorized {
		t.Fatalf("key-only guest password login must fail, got %d %s", passwordLogin.Code, passwordLogin.Body.String())
	}
	keyOnlyLogin := httptest.NewRecorder()
	keyOnlyLoginReq := newTestRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"accessKey":"`+keyGuest.AccessKey+`"}`))
	keyOnlyLoginReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(keyOnlyLogin, keyOnlyLoginReq)
	if keyOnlyLogin.Code != http.StatusOK {
		t.Fatalf("key-only guest key login: %d %s", keyOnlyLogin.Code, keyOnlyLogin.Body.String())
	}

	collaborator := registerCollaborationTestUser(t, app, "teammate")
	assertStatus(collaborator, http.MethodGet, "/api/users/accounts", "", http.StatusForbidden, "administrator access required")
	assertStatus(collaborator, http.MethodGet, "/api/settings", "", http.StatusOK, "")
	teammate, _, err := store.GetUserByHandle(ctx, "teammate")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(adminCookie, http.MethodPut, "/api/users/"+teammate.ID+"/memberships", `{"projectIds":[]}`, http.StatusBadRequest, "only managed for guest accounts")
	assertStatus(adminCookie, http.MethodPost, "/api/users/"+teammate.ID+"/access-keys", `{"label":"desk"}`, http.StatusBadRequest, "only managed for guest accounts")
	assertStatus(adminCookie, http.MethodPost, "/api/agents/"+agent.ID+"/messages", `{"text":"hello"}`, http.StatusServiceUnavailable, "agent runner is not initialized")

	deleteSelf := httptest.NewRecorder()
	delReq := newTestRequest(http.MethodDelete, "/api/users/"+adminUser.ID, nil)
	delReq.AddCookie(adminCookie)
	routes.ServeHTTP(deleteSelf, delReq)
	if deleteSelf.Code != http.StatusBadRequest {
		t.Fatalf("delete self: %d %s", deleteSelf.Code, deleteSelf.Body.String())
	}
}
