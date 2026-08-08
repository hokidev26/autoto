package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestRuntimeSummaryRouteReturnsProcessAndConfigStats(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 9090},
		Paths: config.PathsConfig{
			HomeDir:           filepath.Join(t.TempDir(), "home"),
			DatabasePath:      filepath.Join(t.TempDir(), "autoto.db"),
			DefaultProjectDir: filepath.Join(t.TempDir(), "projects"),
		},
		Background: config.BackgroundConfig{WorkerCount: 12, PerAgentLimit: 6, AllowNestedSubagents: true, MaxSubagentDepth: 3},
		Agent: config.AgentConfig{
			DefaultModel:          "openai:gpt-4.1-mini",
			SummaryModel:          "anthropic:claude-sonnet-4-5",
			ReviewModel:           "openai:gpt-4.1",
			DefaultPermissionMode: "acceptEdits",
			MaxTurns:              120,
			FirstTokenTimeoutMs:   45000,
			MaxTransientRetries:   3,
		},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
			{Name: "openai", Type: "openai", APIKey: "runtime-only", Model: "gpt-4.1-mini"},
			{Name: "local", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8317/v1", Model: "gpt-5.5", APIKeyOptional: true},
			{Name: "manual", Type: "manual", Model: "noop"},
		}},
		Backends: config.BackendsConfig{Instances: []config.BackendConfig{
			{Name: "Local", Kind: "local", BaseURL: "http://127.0.0.1:8000", Active: true},
			{Name: "Cloud", Kind: "cloud", BaseURL: "https://example.invalid"},
		}},
	}

	app := New(cfg, store, nil, nil)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	request.Host = "localhost:7788"
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var body runtimeSummaryResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Version != config.Version || body.Server.Address != "127.0.0.1:9090" || body.Server.ConfigPath != configPath {
		t.Fatalf("unexpected server summary: %+v", body.Server)
	}
	if body.Process.PID <= 0 || body.Process.StartedAt == "" || body.Process.UptimeSeconds < 0 {
		t.Fatalf("unexpected process summary: %+v", body.Process)
	}
	if body.Go.Version == "" || body.Go.CPUs <= 0 || body.Go.Goroutines <= 0 {
		t.Fatalf("unexpected go summary: %+v", body.Go)
	}
	if body.Memory.SysBytes == 0 || body.Memory.HeapInuseBytes == 0 {
		t.Fatalf("unexpected memory summary: %+v", body.Memory)
	}
	if body.Providers.Total != 3 || body.Providers.Configured != 2 {
		t.Fatalf("unexpected provider stats: %+v", body.Providers)
	}
	if body.Backends.Configured != 2 || body.Backends.Active != 1 {
		t.Fatalf("unexpected backend stats: %+v", body.Backends)
	}
	if len(body.Paths) != 4 || body.Agent.DefaultModel != "openai:gpt-4.1-mini" || body.Agent.ReviewModel != "openai:gpt-4.1" || body.Agent.MaxTurns != 120 {
		t.Fatalf("unexpected config summary: paths=%+v agent=%+v", body.Paths, body.Agent)
	}
	if body.Background.WorkerCount != 12 || body.Background.PerAgentLimit != 6 || !body.Background.AllowNestedSubagents || body.Background.MaxSubagentDepth != 3 {
		t.Fatalf("unexpected background summary: %+v", body.Background)
	}
	if body.Security.RemoteAccessRequired || !body.Security.BypassPermissionsAllowed || body.Security.MaxPermissionMode != "bypassPermissions" {
		t.Fatalf("unexpected local security summary: %+v", body.Security)
	}
}

func TestBuildRuntimeSummaryUsesSafeDefaults(t *testing.T) {
	started := time.Now().Add(-2 * time.Minute)
	summary := buildRuntimeSummary(config.Config{}, "", started)
	if summary.Server.Address != "localhost:16888" {
		t.Fatalf("expected safe default address, got %q", summary.Server.Address)
	}
	if summary.Process.UptimeSeconds < 100 {
		t.Fatalf("expected uptime from provided start time, got %+v", summary.Process)
	}
	if summary.Version != config.Version || summary.GeneratedAt == "" {
		t.Fatalf("unexpected metadata: %+v", summary)
	}
}

func TestRuntimeSecuritySummaryUsesCanonicalPasswordEnvName(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{Exposed: true}}, nil, nil, nil)
	request := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	request.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(request)
	summary := app.runtimeSecuritySummaryForRequest(request)
	if !strings.Contains(summary.Message, "AUTOTO_ACCESS_PASSWORD") {
		t.Fatalf("expected canonical password env guidance, got %q", summary.Message)
	}
}

func TestRuntimeSecuritySummaryReflectsLocalUnauthenticatedRestrictedAndFullRequests(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{
		AccessPassword:          "secret",
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeRestricted,
	}}, nil, nil, nil)

	local := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	local.Host = "localhost:7788"
	local.RemoteAddr = "127.0.0.1:4321"
	localSummary := app.runtimeSecuritySummaryForRequest(local)
	if localSummary.Mode != "local" || localSummary.CurrentRequestRemote || localSummary.MaxPermissionMode != "bypassPermissions" || !localSummary.BypassPermissionsAllowed {
		t.Fatalf("unexpected local summary: %+v", localSummary)
	}

	unauthenticated := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	unauthenticated.Host = "remote.example.test"
	markRemoteHTTPS(unauthenticated)
	unauthSummary := app.runtimeSecuritySummaryForRequest(unauthenticated)
	if unauthSummary.Mode != "remote-unauthenticated" || !unauthSummary.CurrentRequestRemote || unauthSummary.MaxPermissionMode != "acceptEdits" || unauthSummary.BypassPermissionsAllowed {
		t.Fatalf("unexpected unauthenticated summary: %+v", unauthSummary)
	}

	restrictedCookies := loginRuntimeRemoteAccess(t, app, remoteAccessModeRestricted)
	restricted := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	restricted.Host = "remote.example.test"
	markRemoteHTTPS(restricted)
	for _, cookie := range restrictedCookies {
		restricted.AddCookie(cookie)
	}
	restrictedSummary := app.runtimeSecuritySummaryForRequest(restricted)
	if restrictedSummary.Mode != "remote-restricted" || restrictedSummary.MaxPermissionMode != "acceptEdits" || restrictedSummary.BypassPermissionsAllowed || restrictedSummary.RemoteTerminalAllowed {
		t.Fatalf("unexpected restricted summary: %+v", restrictedSummary)
	}

	fullCookies := loginRuntimeRemoteAccess(t, app, remoteAccessModeFull)
	full := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	full.Host = "remote.example.test"
	markRemoteHTTPS(full)
	for _, cookie := range fullCookies {
		full.AddCookie(cookie)
	}
	fullSummary := app.runtimeSecuritySummaryForRequest(full)
	if fullSummary.Mode != "remote-full" || fullSummary.MaxPermissionMode != "bypassPermissions" || !fullSummary.BypassPermissionsAllowed || !fullSummary.RemoteTerminalAllowed {
		t.Fatalf("unexpected full summary: %+v", fullSummary)
	}
}

func loginRuntimeRemoteAccess(t *testing.T, app *Server, mode string) []*http.Cookie {
	t.Helper()
	app.cfgMu.Lock()
	app.cfg.Security.AllowRemoteFullAccess = mode == remoteAccessModeFull
	app.cfg.Security.DefaultRemoteAccessMode = mode
	app.cfgMu.Unlock()

	request := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=secret"))
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://remote.example.test")
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("remote %s login returned %d: %s", mode, recorder.Code, recorder.Body.String())
	}
	return recorder.Result().Cookies()
}

func TestRuntimeSecuritySummaryReflectsRequestAuthority(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{
		AccessPassword:          "secret",
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeFull,
	}}, nil, nil, nil)

	local := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	local.Host = "localhost:7788"
	localSummary := app.runtimeSecuritySummaryForRequest(local)
	if localSummary.Mode != "local" || localSummary.CurrentRequestRemote || !localSummary.BypassPermissionsAllowed {
		t.Fatalf("unexpected local summary: %+v", localSummary)
	}

	unauthenticated := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	unauthenticated.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(unauthenticated)
	unauthenticatedSummary := app.runtimeSecuritySummaryForRequest(unauthenticated)
	if unauthenticatedSummary.Mode != "remote-unauthenticated" || !unauthenticatedSummary.CurrentRequestRemote || unauthenticatedSummary.BypassPermissionsAllowed {
		t.Fatalf("unexpected unauthenticated remote summary: %+v", unauthenticatedSummary)
	}

	restricted := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	restricted.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(restricted)
	restricted.Header.Set("Authorization", "Bearer secret")
	restrictedSummary := app.runtimeSecuritySummaryForRequest(restricted)
	if restrictedSummary.Mode != "remote-restricted" || restrictedSummary.MaxPermissionMode != "acceptEdits" || restrictedSummary.RemoteTerminalAllowed {
		t.Fatalf("unexpected restricted remote summary: %+v", restrictedSummary)
	}

	fullToken, _, err := app.newRemoteAccessSession(remoteAccessModeFull)
	if err != nil {
		t.Fatal(err)
	}
	full := newTestRequest(http.MethodGet, "/api/runtime/summary", nil)
	full.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(full)
	full.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: fullToken})
	fullSummary := app.runtimeSecuritySummaryForRequest(full)
	if fullSummary.Mode != "remote-full" || fullSummary.MaxPermissionMode != "bypassPermissions" || !fullSummary.RemoteTerminalAllowed {
		t.Fatalf("unexpected full remote summary: %+v", fullSummary)
	}
}

func TestBuildRuntimeSummaryReflectsExposedSecurityDefaults(t *testing.T) {
	summary := buildRuntimeSummary(config.Config{Security: config.SecurityConfig{Exposed: true, AccessPassword: "secret"}}, "", time.Now())
	if !summary.Security.RemoteAccessRequired || summary.Security.BypassPermissionsAllowed || summary.Security.MaxPermissionMode != "acceptEdits" {
		t.Fatalf("unexpected exposed security summary: %+v", summary.Security)
	}
	if !summary.Security.AccessPasswordConfigured {
		t.Fatalf("expected access password to be reported as configured: %+v", summary.Security)
	}
}
