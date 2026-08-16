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

func TestLoginIfUsersExistGuard(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "login-if-users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Auth: config.AuthConfig{RegistrationOpen: true},
		Security: config.SecurityConfig{
			AccessPasswordHash:      hash,
			AllowRemoteFullAccess:   true,
			DefaultRemoteAccessMode: remoteAccessModeRestricted,
			CredentialRevision:      1,
		},
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	app := New(cfg, store, nil, nil)
	app.SetConfigPath(configPath)
	routes := app.Routes()

	surfaces := []string{
		"/api/fs/directories",
		"/api/mcp/servers",
		"/api/schedules",
		"/api/schedules/missing-schedule/runs",
	}

	for _, path := range surfaces {
		recorder := httptest.NewRecorder()
		routes.ServeHTTP(recorder, newTestRequest(http.MethodGet, path, nil))
		if recorder.Code == http.StatusUnauthorized {
			t.Fatalf("no local users: GET %s must keep local-first access, got 401: %s", path, recorder.Body.String())
		}
	}

	register := httptest.NewRecorder()
	registerReq := newTestRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"handle":"Alice","password":"correct horse battery staple"}`))
	registerReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(register, registerReq)
	if register.Code != http.StatusCreated {
		t.Fatalf("expected registration 201, got %d: %s", register.Code, register.Body.String())
	}

	for _, path := range surfaces {
		recorder := httptest.NewRecorder()
		routes.ServeHTTP(recorder, newTestRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "login required") {
			t.Fatalf("with local users: unauthenticated GET %s must be 401 login required, got %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}

	for _, path := range surfaces {
		req := newTestRequest(http.MethodGet, path, nil)
		req.Host = "remote.example.test"
		markRemoteHTTPS(req)
		recorder := httptest.NewRecorder()
		routes.ServeHTTP(recorder, req)
		if recorder.Code == http.StatusOK {
			t.Fatalf("unauthenticated remote GET %s must not skip both remote and local auth, got 200: %s", path, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "login required") {
			t.Fatalf("unauthenticated remote GET %s must fail as a remote session, not local login, got %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}

	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	sessionCookie := register.Result().Cookies()[0]
	for _, path := range surfaces {
		req := newTestRequest(http.MethodGet, path, nil)
		req.Host = "remote.example.test"
		markRemoteHTTPS(req)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		routes.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "login required") {
			t.Fatalf("remote session with local users must still require a local account for GET %s, got %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
	for _, path := range surfaces {
		req := newTestRequest(http.MethodGet, path, nil)
		req.Host = "remote.example.test"
		markRemoteHTTPS(req)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		req.AddCookie(sessionCookie)
		recorder := httptest.NewRecorder()
		routes.ServeHTTP(recorder, req)
		if recorder.Code == http.StatusUnauthorized && strings.Contains(recorder.Body.String(), "login required") {
			t.Fatalf("remote session plus local login must not be rejected as login required for GET %s, got %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRemoteAccountSessionSkipsAccessPasswordPage(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "remote-account-lock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Auth: config.AuthConfig{RegistrationOpen: true},
		Security: config.SecurityConfig{
			AccessPasswordHash:      hash,
			AllowRemoteFullAccess:   false,
			DefaultRemoteAccessMode: remoteAccessModeRestricted,
			CredentialRevision:      1,
		},
	}
	app := New(cfg, store, nil, nil)
	routes := app.Routes()

	register := httptest.NewRecorder()
	registerReq := newTestRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"handle":"Alice","password":"correct horse battery staple"}`))
	registerReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(register, registerReq)
	if register.Code != http.StatusCreated {
		t.Fatalf("expected registration 201, got %d: %s", register.Code, register.Body.String())
	}

	index := httptest.NewRecorder()
	indexReq := newTestRequest(http.MethodGet, "/", nil)
	indexReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(indexReq)
	indexReq.Header.Set("Accept", "text/html")
	routes.ServeHTTP(index, indexReq)
	if index.Code != http.StatusOK {
		t.Fatalf("with local accounts, remote GET / must serve the app, got %d: %s", index.Code, index.Body.String())
	}
	body := index.Body.String()
	if !strings.Contains(body, `id="accountSessionOverlay"`) {
		t.Fatalf("expected the in-app account overlay, got %s", body)
	}
	if strings.Contains(body, "安全解鎖 Autoto") || strings.Contains(body, "remoteAccessPassword") || strings.Contains(body, "window.AUTOTO_LOCAL_TOKEN=") {
		t.Fatalf("remote index must not show the access-password page or leak the local token, got %s", body)
	}

	passwordPage := httptest.NewRecorder()
	passwordReq := newTestRequest(http.MethodGet, remoteAccessPath, nil)
	passwordReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(passwordReq)
	passwordReq.Header.Set("Accept", "text/html")
	routes.ServeHTTP(passwordPage, passwordReq)
	if passwordPage.Code != http.StatusSeeOther || passwordPage.Header().Get("Location") != "/" {
		t.Fatalf("GET %s with local accounts must redirect to the app, got %d %s", remoteAccessPath, passwordPage.Code, passwordPage.Header().Get("Location"))
	}

	status := httptest.NewRecorder()
	statusReq := newTestRequest(http.MethodGet, "/api/auth/status", nil)
	statusReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(statusReq)
	routes.ServeHTTP(status, statusReq)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"hasUsers":true`) {
		t.Fatalf("remote /api/auth/status must load the overlay, got %d: %s", status.Code, status.Body.String())
	}

	blocked := httptest.NewRecorder()
	blockedReq := newTestRequest(http.MethodGet, "/api/schedules", nil)
	blockedReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(blockedReq)
	routes.ServeHTTP(blocked, blockedReq)
	if blocked.Code == http.StatusOK {
		t.Fatalf("remote APIs other than account boot must stay closed before login, got 200: %s", blocked.Body.String())
	}

	login := httptest.NewRecorder()
	loginReq := newTestRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"handle":"Alice","password":"correct horse battery staple"}`))
	loginReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(loginReq)
	loginReq.Header.Set("Content-Type", "application/json")
	routes.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("remote account login without the access-password page must succeed, got %d: %s", login.Code, login.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == authSessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("remote account login must set the account session cookie")
	}

	allowed := httptest.NewRecorder()
	allowedReq := newTestRequest(http.MethodGet, "/api/schedules", nil)
	allowedReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(allowedReq)
	allowedReq.AddCookie(sessionCookie)
	routes.ServeHTTP(allowed, allowedReq)
	if allowed.Code == http.StatusUnauthorized {
		t.Fatalf("account session must authenticate the remote request, got %d: %s", allowed.Code, allowed.Body.String())
	}
}
