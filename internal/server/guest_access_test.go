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
		{http.MethodPost, "/api/worklines/w1/conversations"},
		{http.MethodGet, "/api/agents/a1/git/status"},
		{http.MethodGet, "/api/agents/a1/workspace/tree"},
		{http.MethodGet, "/api/agents/a1/context"},
		{http.MethodGet, "/ws/terminal"},
		{http.MethodGet, "/api/background-tasks/t1"},
		{http.MethodPost, "/api/background-tasks/t1/cancel"},
		{http.MethodPost, "/api/preferences/import-local"},
		{http.MethodGet, "/api/users/accounts"},
		{http.MethodPatch, "/api/agents/a1/model"},
		{http.MethodPost, "/api/users/collaborators"},
		{http.MethodPost, "/api/users/operators"},
		{http.MethodPost, "/api/users/guests"},
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
	assertStatus(adminCookie, http.MethodPut, "/api/users/"+teammate.ID+"/memberships", `{"projectIds":[]}`, http.StatusOK, `"handle":"teammate"`)
	assertStatus(adminCookie, http.MethodPost, "/api/users/"+teammate.ID+"/access-keys", `{"label":"desk"}`, http.StatusBadRequest, "only managed for guest accounts")
	assertStatus(adminCookie, http.MethodPut, "/api/users/"+adminUser.ID+"/memberships", `{"projectIds":[]}`, http.StatusBadRequest, "project memberships are only managed for operators, collaborators and guests")
	assertStatus(adminCookie, http.MethodPost, "/api/agents/"+agent.ID+"/messages", `{"text":"hello"}`, http.StatusServiceUnavailable, "agent runner is not initialized")

	deleteSelf := httptest.NewRecorder()
	delReq := newTestRequest(http.MethodDelete, "/api/users/"+adminUser.ID, nil)
	delReq.AddCookie(adminCookie)
	routes.ServeHTTP(deleteSelf, delReq)
	if deleteSelf.Code != http.StatusBadRequest {
		t.Fatalf("delete self: %d %s", deleteSelf.Code, deleteSelf.Body.String())
	}
}

func TestAdminCreatesCollaboratorWithProjectMembership(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "collaborator-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _, agent, err := store.CreateProject(ctx, "Shared", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, secretAgent, err := store.CreateProject(ctx, "Secret", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)
	routes := app.Routes()
	adminCookie := registerCollaborationTestUser(t, app, "host")
	adminUser, _, err := store.GetUserByHandle(ctx, "host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProjectMember(ctx, db.ProjectMember{ProjectID: secret.ID, UserID: adminUser.ID, Role: "owner"}); err != nil {
		t.Fatal(err)
	}

	missingPassword := httptest.NewRecorder()
	missingReq := newTestRequest(http.MethodPost, "/api/users/collaborators", strings.NewReader(`{"handle":"empty-pass"}`))
	missingReq.Header.Set("Content-Type", "application/json")
	missingReq.AddCookie(adminCookie)
	routes.ServeHTTP(missingPassword, missingReq)
	if missingPassword.Code != http.StatusBadRequest {
		t.Fatalf("missing password: %d %s", missingPassword.Code, missingPassword.Body.String())
	}

	createBody, _ := json.Marshal(createCollaboratorRequest{
		Handle:     "teammate",
		Password:   "correct horse battery staple",
		ProjectIDs: []string{project.ID},
	})
	created := httptest.NewRecorder()
	req := newTestRequest(http.MethodPost, "/api/users/collaborators", strings.NewReader(string(createBody)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	routes.ServeHTTP(created, req)
	if created.Code != http.StatusCreated {
		t.Fatalf("create collaborator: %d %s", created.Code, created.Body.String())
	}
	var account userAccountResponse
	if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	if account.Role != db.RoleCollaborator || !account.PasswordSet {
		t.Fatalf("unexpected collaborator payload: %+v", account)
	}
	if len(account.ProjectIDs) != 1 || account.ProjectIDs[0] != project.ID {
		t.Fatalf("collaborator projects = %v, want [%s]", account.ProjectIDs, project.ID)
	}
	if strings.Contains(created.Body.String(), `"accessKey"`) || strings.Contains(created.Body.String(), "atk_") {
		t.Fatalf("collaborator create leaked an access key: %s", created.Body.String())
	}

	login := httptest.NewRecorder()
	loginReq := newTestRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"handle":"teammate","password":"correct horse battery staple"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("collaborator login: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	assertStatus := func(session *http.Cookie, method, path, body string, want int, wantBody string) {
		t.Helper()
		reader := strings.NewReader(body)
		recorder := httptest.NewRecorder()
		request := newTestRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if session != nil {
			request.AddCookie(session)
		}
		routes.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, recorder.Code, want, recorder.Body.String())
		}
		if wantBody != "" && !strings.Contains(recorder.Body.String(), wantBody) {
			t.Fatalf("%s %s: body %q does not contain %q", method, path, recorder.Body.String(), wantBody)
		}
	}

	assertStatus(cookie, http.MethodGet, "/api/agents/"+agent.ID+"/messages", "", http.StatusOK, "")
	assertStatus(cookie, http.MethodPost, "/api/agents/"+agent.ID+"/messages", `{"text":"hello"}`, http.StatusServiceUnavailable, "agent runner is not initialized")
	assertStatus(cookie, http.MethodGet, "/api/workflow/preferences", "", http.StatusOK, "dangerReflectionLevel")
	assertStatus(cookie, http.MethodPut, "/api/workflow/preferences", `{"requireConfirmationForExec":true,"requireConfirmationForWrites":false,"allowReadOnlyByDefault":true}`, http.StatusForbidden, collaboratorAccessDenied)
	assertStatus(cookie, http.MethodGet, "/api/settings", "", http.StatusForbidden, collaboratorAccessDenied)
	assertStatus(cookie, http.MethodPost, "/api/projects", `{"name":"Other"}`, http.StatusForbidden, collaboratorAccessDenied)
	assertStatus(cookie, http.MethodPatch, "/api/projects/"+project.ID+"/navigation-state", `{"archived":true}`, http.StatusForbidden, collaboratorAccessDenied)
	assertStatus(cookie, http.MethodGet, "/api/users/accounts", "", http.StatusForbidden, collaboratorAccessDenied)
	assertStatus(cookie, http.MethodPost, "/api/users/collaborators", `{"handle":"other","password":"correct horse battery staple"}`, http.StatusForbidden, collaboratorAccessDenied)
	assertStatus(cookie, http.MethodGet, "/api/agents/"+secretAgent.ID+"/messages", "", http.StatusNotFound, "resource not found")
	assertStatus(cookie, http.MethodGet, "/api/projects", "", http.StatusOK, `"name":"Shared"`)
	projectsList := httptest.NewRecorder()
	projectsReq := newTestRequest(http.MethodGet, "/api/projects", nil)
	projectsReq.AddCookie(cookie)
	routes.ServeHTTP(projectsList, projectsReq)
	if strings.Contains(projectsList.Body.String(), secret.ID) || strings.Contains(projectsList.Body.String(), `"name":"Secret"`) {
		t.Fatalf("collaborator project list leaked ungranted project: %s", projectsList.Body.String())
	}

	grantBody, _ := json.Marshal(replaceMembershipsRequest{ProjectIDs: []string{project.ID, secret.ID}})
	assertStatus(adminCookie, http.MethodPut, "/api/users/"+account.ID+"/memberships", string(grantBody), http.StatusOK, secret.ID)
	assertStatus(cookie, http.MethodGet, "/api/agents/"+secretAgent.ID+"/messages", "", http.StatusOK, "")
	assertStatus(adminCookie, http.MethodPost, "/api/users/"+account.ID+"/access-keys", `{"label":"desk"}`, http.StatusBadRequest, "only managed for guest accounts")
}

func TestAdminCreatesOperatorAndSeesNewProjects(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "operator-admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	shared, _, _, err := store.CreateProject(ctx, "Shared", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)
	routes := app.Routes()
	adminCookie := registerCollaborationTestUser(t, app, "host")

	createBody, _ := json.Marshal(createCollaboratorRequest{
		Handle:     "operator",
		Password:   "correct horse battery staple",
		ProjectIDs: []string{shared.ID},
	})
	created := httptest.NewRecorder()
	req := newTestRequest(http.MethodPost, "/api/users/operators", strings.NewReader(string(createBody)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	routes.ServeHTTP(created, req)
	if created.Code != http.StatusCreated {
		t.Fatalf("create operator: %d %s", created.Code, created.Body.String())
	}
	var account userAccountResponse
	if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	if account.Role != db.RoleOperator {
		t.Fatalf("operator role = %q", account.Role)
	}

	login := httptest.NewRecorder()
	loginReq := newTestRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"handle":"operator","password":"correct horse battery staple"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("operator login: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	settings := httptest.NewRecorder()
	settingsReq := newTestRequest(http.MethodGet, "/api/settings", nil)
	settingsReq.AddCookie(cookie)
	routes.ServeHTTP(settings, settingsReq)
	if settings.Code != http.StatusOK {
		t.Fatalf("operator settings: %d %s", settings.Code, settings.Body.String())
	}

	gitPath := filepath.Join(t.TempDir(), "operator-project")
	createProject, _ := json.Marshal(map[string]string{"name": "Operator project", "gitPath": gitPath})
	createdProject := httptest.NewRecorder()
	createReq := newTestRequest(http.MethodPost, "/api/projects", strings.NewReader(string(createProject)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(cookie)
	routes.ServeHTTP(createdProject, createReq)
	if createdProject.Code != http.StatusCreated && createdProject.Code != http.StatusOK {
		t.Fatalf("operator create project: %d %s", createdProject.Code, createdProject.Body.String())
	}
	if !strings.Contains(createdProject.Body.String(), `"name":"Operator project"`) {
		t.Fatalf("operator create project body: %s", createdProject.Body.String())
	}

	adminList := httptest.NewRecorder()
	adminReq := newTestRequest(http.MethodGet, "/api/projects", nil)
	adminReq.AddCookie(adminCookie)
	routes.ServeHTTP(adminList, adminReq)
	if adminList.Code != http.StatusOK || !strings.Contains(adminList.Body.String(), `"name":"Operator project"`) {
		t.Fatalf("admin should see operator-created project: %d %s", adminList.Code, adminList.Body.String())
	}

	roleBody := `{"role":"collaborator"}`
	roleChange := httptest.NewRecorder()
	roleReq := newTestRequest(http.MethodPatch, "/api/users/"+account.ID+"/role", strings.NewReader(roleBody))
	roleReq.Header.Set("Content-Type", "application/json")
	roleReq.AddCookie(adminCookie)
	routes.ServeHTTP(roleChange, roleReq)
	if roleChange.Code != http.StatusOK || !strings.Contains(roleChange.Body.String(), `"role":"collaborator"`) {
		t.Fatalf("demote operator: %d %s", roleChange.Code, roleChange.Body.String())
	}

	blocked := httptest.NewRecorder()
	blockedReq := newTestRequest(http.MethodGet, "/api/settings", nil)
	blockedReq.AddCookie(cookie)
	routes.ServeHTTP(blocked, blockedReq)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("demoted operator should lose host settings: %d %s", blocked.Code, blocked.Body.String())
	}
}
