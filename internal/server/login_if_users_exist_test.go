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
