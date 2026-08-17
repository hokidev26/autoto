package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func remoteAccessTestServer(t *testing.T) *Server {
	t.Helper()
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Security: config.SecurityConfig{
		AccessPasswordHash:      hash,
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeRestricted,
		CredentialRevision:      1,
	}}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	app := New(cfg, nil, nil, nil)
	app.SetConfigPath(path)
	return app
}

func TestConfiguredRemoteAccessModeFollowsFullAccessSwitch(t *testing.T) {
	cfg := config.Config{Security: config.SecurityConfig{AllowRemoteFullAccess: true, DefaultRemoteAccessMode: remoteAccessModeRestricted}}
	if got := configuredRemoteAccessMode(cfg); got != remoteAccessModeFull {
		t.Fatalf("full access switch should select full mode, got %q", got)
	}
	cfg.Security.AllowRemoteFullAccess = false
	cfg.Security.DefaultRemoteAccessMode = remoteAccessModeFull
	if got := configuredRemoteAccessMode(cfg); got != remoteAccessModeRestricted {
		t.Fatalf("disabled full access switch should select restricted mode, got %q", got)
	}
}

func loginRemoteAccess(t *testing.T, app *Server, mode string) []*http.Cookie {
	t.Helper()
	if mode != remoteAccessModeRestricted && mode != remoteAccessModeFull {
		t.Fatalf("invalid configured remote mode %q", mode)
	}
	app.cfgMu.Lock()
	app.cfg.Security.AllowRemoteFullAccess = mode == remoteAccessModeFull
	app.cfg.Security.DefaultRemoteAccessMode = mode
	app.cfgMu.Unlock()

	requestedMode := remoteAccessModeFull
	if mode == remoteAccessModeFull {
		requestedMode = remoteAccessModeRestricted
	}
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, remoteAccessPath, strings.NewReader("password=Correct-Horse-1!&mode="+requestedMode))
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("login returned %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Result().Cookies()
}

func TestInvalidRemoteAccessCookieOnLoopbackKeepsLocalAdmin(t *testing.T) {
	app := remoteAccessTestServer(t)
	request := newTestRequest(http.MethodGet, "/api/plugins", nil)
	request.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: "garbage-from-another-localhost-app"})
	request.Header.Set(localTokenHeader, app.localToken)
	auth := app.remoteAccessAuthentication(request)
	if auth.Remote {
		t.Fatal("an invalid remote cookie on loopback must not flip the request to remote")
	}
	if !auth.Authenticated || auth.Mode != remoteAccessModeFull {
		t.Fatalf("loopback should keep local-admin authority, got %+v", auth)
	}
	caps := app.capabilitiesForRequest(request)
	if caps.FilesystemScope != "host" || caps.MaxPermissionMode != "bypassPermissions" {
		t.Fatalf("loopback capabilities must stay host-scoped, got %+v", caps)
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code == http.StatusUnauthorized || recorder.Code == http.StatusForbidden {
		t.Fatalf("invalid remote cookie must not block loopback admin, got %d: %s", recorder.Code, recorder.Body.String())
	}

	remote := newTestRequest(http.MethodGet, "/api/plugins", nil)
	remote.Host = "remote.example.test"
	markRemoteHTTPS(remote)
	remote.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: "garbage-from-another-localhost-app"})
	remote.Header.Set(localTokenHeader, app.localToken)
	if !app.remoteAccessAuthentication(remote).Remote {
		t.Fatal("an invalid remote cookie on a remote transport must stay remote")
	}
	remoteRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(remoteRecorder, remote)
	if remoteRecorder.Code != http.StatusUnauthorized && remoteRecorder.Code != http.StatusForbidden {
		t.Fatalf("stale remote cookie must not authenticate, got %d: %s", remoteRecorder.Code, remoteRecorder.Body.String())
	}
}

func TestRemoteAccessCookieCarriesOnlySessionTokens(t *testing.T) {
	app := remoteAccessTestServer(t)
	sessionToken, _, err := app.newRemoteAccessSession(remoteAccessModeRestricted)
	if err != nil {
		t.Fatal(err)
	}

	valid := newTestRequest(http.MethodGet, "/api/health", nil)
	valid.Host = "remote.example.test"
	markRemoteHTTPS(valid)
	valid.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: sessionToken})
	validRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(validRecorder, valid)
	if validRecorder.Code != http.StatusOK {
		t.Fatalf("cookie must carry a regular session token, got %d: %s", validRecorder.Code, validRecorder.Body.String())
	}

	forged := newTestRequest(http.MethodGet, "/api/health", nil)
	forged.Host = "remote.example.test"
	markRemoteHTTPS(forged)
	forged.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: app.remoteAccessToken})
	forgedRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(forgedRecorder, forged)
	if forgedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("obsolete singleton token must not authenticate cookies, got %d: %s", forgedRecorder.Code, forgedRecorder.Body.String())
	}

	logout := newTestRequest(http.MethodPost, remoteAccessLogoutPath, nil)
	logout.Host = "remote.example.test"
	markRemoteHTTPS(logout)
	logout.Header.Set("Accept", "application/json")
	logout.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: sessionToken})
	logoutRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusOK {
		t.Fatalf("session logout returned %d: %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}

	revoked := newTestRequest(http.MethodGet, "/api/health", nil)
	revoked.Host = "remote.example.test"
	markRemoteHTTPS(revoked)
	revoked.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: sessionToken})
	revokedRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(revokedRecorder, revoked)
	if revokedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("logout must revoke the session, got %d", revokedRecorder.Code)
	}
}

func TestRemoteAccessSessionsAreIndependentAndLogoutRevokesCurrent(t *testing.T) {
	app := remoteAccessTestServer(t)
	first := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	second := loginRemoteAccess(t, app, remoteAccessModeFull)

	fullRequest := newTestRequest(http.MethodGet, "/api/security/remote-access", nil)
	fullRequest.Host = "remote.example.test"
	markRemoteHTTPS(fullRequest)
	for _, cookie := range second {
		fullRequest.AddCookie(cookie)
	}
	full := httptest.NewRecorder()
	app.Routes().ServeHTTP(full, fullRequest)
	if full.Code != http.StatusOK || !strings.Contains(full.Body.String(), `"mode":"full"`) || !strings.Contains(full.Body.String(), `"filesystemScope":"host"`) || !strings.Contains(full.Body.String(), `"securityAdminAllowed":false`) {
		t.Fatalf("expected full capabilities, got %d: %s", full.Code, full.Body.String())
	}

	logout := httptest.NewRecorder()
	logoutRequest := newTestRequest(http.MethodPost, remoteAccessLogoutPath, nil)
	logoutRequest.Host = "remote.example.test"
	markRemoteHTTPS(logoutRequest)
	logoutRequest.Header.Set("Accept", "application/json")
	for _, cookie := range second {
		logoutRequest.AddCookie(cookie)
	}
	app.Routes().ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout returned %d", logout.Code)
	}

	firstRequest := newTestRequest(http.MethodGet, "/api/security/remote-access", nil)
	firstRequest.Host = "remote.example.test"
	markRemoteHTTPS(firstRequest)
	for _, cookie := range first {
		firstRequest.AddCookie(cookie)
	}
	firstResult := httptest.NewRecorder()
	app.Routes().ServeHTTP(firstResult, firstRequest)
	if firstResult.Code != http.StatusOK || !strings.Contains(firstResult.Body.String(), `"mode":"restricted"`) {
		t.Fatalf("expected the other session to remain valid, got %d: %s", firstResult.Code, firstResult.Body.String())
	}
}

func TestConcurrentConfigMutationsPreserveProviderContinuationAndSecurity(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	initialProviderCount := len(cfg.Providers.Instances)
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Security.AccessPasswordHash = hash
	cfg.Security.CredentialRevision = 1
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), agent.NewHub(), cfg.Agent)
	app := New(cfg, store, runner, agent.NewHub(), providers.NewRegistry())
	app.SetConfigPath(configPath)

	const providerCount = 12
	start := make(chan struct{})
	errs := make(chan error, providerCount+2)
	var wg sync.WaitGroup
	run := func(name string, request *http.Request, want int) {
		defer wg.Done()
		<-start
		request.Host = "localhost:7788"
		request.RemoteAddr = "127.0.0.1:1234"
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != want {
			errs <- fmt.Errorf("%s status=%d want=%d body=%s", name, recorder.Code, want, recorder.Body.String())
		}
	}
	for i := 0; i < providerCount; i++ {
		name := fmt.Sprintf("relay-%02d", i)
		payload := fmt.Sprintf(`{"name":%q,"type":"openai-compatible","baseUrl":"http://127.0.0.1:%d/v1","apiKey":"key-%d","model":"model-%d"}`, name, 64000+i, i, i)
		request := newTestRequest(http.MethodPut, "/api/providers/"+name+"/config", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(localTokenHeader, app.localToken)
		wg.Add(1)
		go run(name, request, http.StatusOK)
	}
	continuation := newTestRequest(http.MethodPatch, "/api/runtime/continuation-settings", strings.NewReader(`{"mode":"off","segmentTurns":7,"maxContinuations":0,"maxTotalTurns":7,"maxRunDurationMs":1000,"maxRunTokens":1000}`))
	continuation.Header.Set("Content-Type", "application/json")
	continuation.Header.Set(localTokenHeader, app.localToken)
	wg.Add(1)
	go run("continuation", continuation, http.StatusOK)
	policy := newTestRequest(http.MethodPatch, "/api/security/remote-access/policy", strings.NewReader(`{"allowFullAccess":true,"defaultMode":"restricted","allowRemoteNativePicker":true,"revision":1}`))
	policy.Header.Set("Content-Type", "application/json")
	policy.Header.Set(localTokenHeader, app.localToken)
	wg.Add(1)
	go run("security", policy, http.StatusOK)
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	got := app.configSnapshot()
	wantProviderCount := initialProviderCount + providerCount
	if len(got.Providers.Instances) != wantProviderCount {
		t.Fatalf("concurrent mutations lost providers: got %d want %d", len(got.Providers.Instances), wantProviderCount)
	}
	if got.Agent.AutoContinuationMode != "off" || got.Agent.MaxTotalTurns != 7 {
		t.Fatalf("concurrent mutations lost continuation settings: %+v", got.Agent)
	}
	if !got.Security.AllowRemoteFullAccess || got.Security.DefaultRemoteAccessMode != remoteAccessModeFull || !got.Security.AllowRemoteNativePicker || got.Security.CredentialRevision != 2 {
		t.Fatalf("concurrent mutations lost security policy: %+v", got.Security)
	}
	persisted, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Providers.Instances) != wantProviderCount || persisted.Agent.MaxTotalTurns != 7 || !persisted.Security.AllowRemoteFullAccess || persisted.Security.DefaultRemoteAccessMode != remoteAccessModeFull || !persisted.Security.AllowRemoteNativePicker {
		t.Fatalf("persisted config lost a concurrent mutation: %+v", persisted)
	}
}

func TestRemoteAccessPasswordRotationPersistsHashAndRevokesSessions(t *testing.T) {
	app := remoteAccessTestServer(t)
	cookies := loginRemoteAccess(t, app, remoteAccessModeFull)

	remoteRequest := newTestRequest(http.MethodPut, "/api/security/remote-access/password", strings.NewReader(`{"strategy":"custom","password":"New-Remote-Password-2!","currentPassword":"Correct-Horse-1!"}`))
	remoteRequest.Host = "remote.example.test"
	markRemoteHTTPS(remoteRequest)
	remoteRequest.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		remoteRequest.AddCookie(cookie)
	}
	remote := httptest.NewRecorder()
	app.Routes().ServeHTTP(remote, remoteRequest)
	if remote.Code != http.StatusForbidden || !strings.Contains(remote.Body.String(), "localhost") {
		t.Fatalf("remote password rotation must be host-local only, got %d: %s", remote.Code, remote.Body.String())
	}

	request := newTestRequest(http.MethodPut, "/api/security/remote-access/password", strings.NewReader(`{"strategy":"custom","password":"New-Remote-Password-2!"}`))
	request.Host = "localhost:7788"
	request.RemoteAddr = "127.0.0.1:4321"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("password rotation returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "New-Remote-Password-2!") {
		t.Fatal("custom password must never be echoed")
	}
	cfg := app.configSnapshot()
	if cfg.Security.AccessPasswordHash == "" || cfg.Security.AccessPassword != "" || config.VerifyAccessPassword(cfg.Security.AccessPasswordHash, "New-Remote-Password-2!") == false {
		t.Fatalf("expected persisted password hash only, got %+v", cfg.Security)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}

	stale := newTestRequest(http.MethodGet, "/api/health", nil)
	stale.Host = "remote.example.test"
	markRemoteHTTPS(stale)
	for _, cookie := range cookies {
		stale.AddCookie(cookie)
	}
	staleRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(staleRecorder, stale)
	if staleRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("rotating password must revoke session, got %d", staleRecorder.Code)
	}
}

func TestRemotePolicyMutationRequiresHostLocalAuthority(t *testing.T) {
	app := remoteAccessTestServer(t)
	restrictedCookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	fullCookies := loginRemoteAccess(t, app, remoteAccessModeFull)
	body := `{"allowFullAccess":false,"defaultMode":"restricted","allowRemoteNativePicker":false,"revision":1,"currentPassword":"Correct-Horse-1!"}`

	for name, cookies := range map[string][]*http.Cookie{
		"restricted": restrictedCookies,
		"full":       fullCookies,
	} {
		request := newTestRequest(http.MethodPatch, "/api/security/remote-access/policy", strings.NewReader(body))
		request.Host = "remote.example.test"
		markRemoteHTTPS(request)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(localTokenHeader, app.localToken)
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		app.Routes().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "localhost") {
			t.Fatalf("%s remote session must not change host policy, got %d: %s", name, response.Code, response.Body.String())
		}
	}

	localRequest := newTestRequest(http.MethodPatch, "/api/security/remote-access/policy", strings.NewReader(body))
	localRequest.Host = "localhost:7788"
	localRequest.RemoteAddr = "127.0.0.1:4321"
	localRequest.Header.Set("Content-Type", "application/json")
	localRequest.Header.Set(localTokenHeader, app.localToken)
	local := httptest.NewRecorder()
	app.Routes().ServeHTTP(local, localRequest)
	if local.Code != http.StatusOK {
		t.Fatalf("host-local policy mutation returned %d: %s", local.Code, local.Body.String())
	}
	cfg := app.configSnapshot()
	if cfg.Security.AllowRemoteFullAccess || cfg.Security.DefaultRemoteAccessMode != remoteAccessModeRestricted || cfg.Security.CredentialRevision != 2 {
		t.Fatalf("unexpected persisted policy: %+v", cfg.Security)
	}

	for name, cookies := range map[string][]*http.Cookie{
		"restricted": restrictedCookies,
		"full":       fullCookies,
	} {
		staleRequest := newTestRequest(http.MethodGet, "/api/health", nil)
		staleRequest.Host = "remote.example.test"
		markRemoteHTTPS(staleRequest)
		for _, cookie := range cookies {
			staleRequest.AddCookie(cookie)
		}
		stale := httptest.NewRecorder()
		app.Routes().ServeHTTP(stale, staleRequest)
		if stale.Code != http.StatusUnauthorized {
			t.Fatalf("policy mutation must revoke the %s session, got %d", name, stale.Code)
		}
	}
}

func TestSensitiveGuardAllowsOnlyLocalOrFullRemoteAuthority(t *testing.T) {
	app := remoteAccessTestServer(t)
	restrictedCookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	fullCookies := loginRemoteAccess(t, app, remoteAccessModeFull)
	handler := app.sensitiveLocalTokenGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	restrictedRequest := newTestRequest(http.MethodPost, "/sensitive", nil)
	restrictedRequest.Host = "remote.example.test"
	markRemoteHTTPS(restrictedRequest)
	restrictedRequest.Header.Set(localTokenHeader, app.localToken)
	for _, cookie := range restrictedCookies {
		restrictedRequest.AddCookie(cookie)
	}
	restricted := httptest.NewRecorder()
	handler.ServeHTTP(restricted, restrictedRequest)
	if restricted.Code != http.StatusForbidden {
		t.Fatalf("restricted remote session must not pass the sensitive guard, got %d", restricted.Code)
	}

	fullRequest := newTestRequest(http.MethodPost, "/sensitive", nil)
	fullRequest.Host = "remote.example.test"
	markRemoteHTTPS(fullRequest)
	for _, cookie := range fullCookies {
		fullRequest.AddCookie(cookie)
	}
	full := httptest.NewRecorder()
	handler.ServeHTTP(full, fullRequest)
	if full.Code != http.StatusNoContent {
		t.Fatalf("full remote session should pass the sensitive guard without the local token, got %d: %s", full.Code, full.Body.String())
	}

	localRequest := newTestRequest(http.MethodPost, "/sensitive", nil)
	localRequest.Host = "localhost:7788"
	localRequest.Header.Set(localTokenHeader, app.localToken)
	local := httptest.NewRecorder()
	handler.ServeHTTP(local, localRequest)
	if local.Code != http.StatusNoContent {
		t.Fatalf("canonical local request should pass the sensitive guard, got %d", local.Code)
	}
}

func TestFullRemoteAccessGuardRejectsRestrictedAdministrativeMutations(t *testing.T) {
	app := remoteAccessTestServer(t)
	restrictedCookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	fullCookies := loginRemoteAccess(t, app, remoteAccessModeFull)
	handler := app.fullRemoteAccessGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	restrictedRequest := newTestRequest(http.MethodPost, "/admin", nil)
	restrictedRequest.Host = "remote.example.test"
	markRemoteHTTPS(restrictedRequest)
	restrictedRequest.Header.Set(localTokenHeader, app.localToken)
	for _, cookie := range restrictedCookies {
		restrictedRequest.AddCookie(cookie)
	}
	restricted := httptest.NewRecorder()
	handler.ServeHTTP(restricted, restrictedRequest)
	if restricted.Code != http.StatusForbidden {
		t.Fatalf("restricted remote authority reached an administrative mutation: %d %s", restricted.Code, restricted.Body.String())
	}

	fullRequest := newTestRequest(http.MethodPost, "/admin", nil)
	fullRequest.Host = "remote.example.test"
	markRemoteHTTPS(fullRequest)
	for _, cookie := range fullCookies {
		fullRequest.AddCookie(cookie)
	}
	full := httptest.NewRecorder()
	handler.ServeHTTP(full, fullRequest)
	if full.Code != http.StatusNoContent {
		t.Fatalf("full remote authority should reach administrative mutations, got %d: %s", full.Code, full.Body.String())
	}
}

func TestGeneratedRemotePasswordIsReturnedOnceAndPersistedOnlyAsHash(t *testing.T) {
	app := remoteAccessTestServer(t)
	request := newTestRequest(http.MethodPut, "/api/security/remote-access/password", strings.NewReader(`{"strategy":"generate"}`))
	request.Host = "localhost:7788"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("generated password request returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		GeneratedPassword string `json:"generatedPassword"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.GeneratedPassword) < 32 || !strings.HasPrefix(response.GeneratedPassword, "At-") {
		t.Fatalf("expected a strong generated password, got %q", response.GeneratedPassword)
	}
	cfg := app.configSnapshot()
	if cfg.Security.AccessPassword != "" || !config.VerifyAccessPassword(cfg.Security.AccessPasswordHash, response.GeneratedPassword) {
		t.Fatalf("generated password was not retained as a verifiable hash: %+v", cfg.Security)
	}
	persisted, err := os.ReadFile(app.configPathSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), response.GeneratedPassword) || strings.Contains(string(persisted), `"accessPassword":`) {
		t.Fatal("generated password must never be persisted in plaintext")
	}
	settingsRequest := newTestRequest(http.MethodGet, "/api/security/remote-access", nil)
	settingsRequest.Host = "localhost:7788"
	settings := httptest.NewRecorder()
	app.Routes().ServeHTTP(settings, settingsRequest)
	if settings.Code != http.StatusOK || strings.Contains(settings.Body.String(), response.GeneratedPassword) || strings.Contains(settings.Body.String(), "generatedPassword") {
		t.Fatalf("generated password must be one-time response data, got %d: %s", settings.Code, settings.Body.String())
	}
}

func TestEnvironmentAccessPasswordCanBeRotatedLocally(t *testing.T) {
	environmentPassword := "Environment-Remote-Password-1!"
	app := New(config.Config{Security: config.SecurityConfig{
		AccessPassword:          environmentPassword,
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeRestricted,
		CredentialRevision:      1,
	}}, nil, nil, nil)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, app.configSnapshot()); err != nil {
		t.Fatal(err)
	}
	app.SetConfigPath(path)

	request := newTestRequest(http.MethodPut, "/api/security/remote-access/password", strings.NewReader(`{"strategy":"generate"}`))
	request.Host = "localhost:7788"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("environment-backed local password generation returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		GeneratedPassword string `json:"generatedPassword"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.GeneratedPassword == "" {
		t.Fatal("expected a one-time generated password")
	}
	cfg := app.configSnapshot()
	if cfg.Security.AccessPassword != "" || !config.VerifyAccessPassword(cfg.Security.AccessPasswordHash, response.GeneratedPassword) {
		t.Fatalf("expected local hash to replace environment credential, got %+v", cfg.Security)
	}
	if app.verifyRemoteAccessPassword(environmentPassword) {
		t.Fatal("the replaced environment password must no longer authenticate")
	}
	if !app.verifyRemoteAccessPassword(response.GeneratedPassword) {
		t.Fatal("the generated local password must authenticate")
	}
	settings := newTestRequest(http.MethodGet, "/api/security/remote-access", nil)
	settings.Host = "localhost:7788"
	settings.Header.Set(localTokenHeader, app.localToken)
	settingsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(settingsRecorder, settings)
	if settingsRecorder.Code != http.StatusOK || !strings.Contains(settingsRecorder.Body.String(), `"source":"config"`) || !strings.Contains(settingsRecorder.Body.String(), `"defaultMode":"full"`) {
		t.Fatalf("expected config-backed credential and switch-derived full mode after rotation, got %d: %s", settingsRecorder.Code, settingsRecorder.Body.String())
	}
}

func TestRestrictedAndFullRemoteFilesystemScopes(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{HomeDir: root, DefaultProjectDir: projects},
		Security: config.SecurityConfig{
			AccessPasswordHash:      hash,
			AllowRemoteFullAccess:   true,
			DefaultRemoteAccessMode: remoteAccessModeRestricted,
			CredentialRevision:      1,
		},
	}
	path := filepath.Join(root, "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	app := New(cfg, nil, nil, nil)
	app.SetConfigPath(path)
	restrictedCookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	fullCookies := loginRemoteAccess(t, app, remoteAccessModeFull)
	target := "/api/fs/directories?path=" + url.QueryEscape(outside)

	restrictedRequest := newTestRequest(http.MethodGet, target, nil)
	restrictedRequest.Host = "remote.example.test"
	markRemoteHTTPS(restrictedRequest)
	for _, cookie := range restrictedCookies {
		restrictedRequest.AddCookie(cookie)
	}
	restricted := httptest.NewRecorder()
	app.Routes().ServeHTTP(restricted, restrictedRequest)
	if restricted.Code != http.StatusBadRequest || !strings.Contains(restricted.Body.String(), `"error":"invalid request"`) {
		t.Fatalf("restricted filesystem scope escaped the project root: %d %s", restricted.Code, restricted.Body.String())
	}
	if strings.Contains(restricted.Body.String(), "path escapes default project directory") || strings.Contains(restricted.Body.String(), outside) {
		t.Fatalf("restricted remote session leaked filesystem detail: %s", restricted.Body.String())
	}

	fullRequest := newTestRequest(http.MethodGet, target, nil)
	fullRequest.Host = "remote.example.test"
	markRemoteHTTPS(fullRequest)
	for _, cookie := range fullCookies {
		fullRequest.AddCookie(cookie)
	}
	full := httptest.NewRecorder()
	app.Routes().ServeHTTP(full, fullRequest)
	var listing struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	if full.Code != http.StatusOK {
		t.Fatalf("full filesystem scope did not browse the host directory: %d %s", full.Code, full.Body.String())
	}
	if err := json.NewDecoder(full.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 1 || !sameFilesystemProjectPath(listing.Entries[0].Path, filepath.Join(outside, "child")) {
		t.Fatalf("full filesystem scope returned the wrong directory listing: %+v", listing.Entries)
	}
}

func TestRestrictedRemoteHidesExistingResourcesOutsideProjectRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	insidePath := filepath.Join(projectsRoot, "inside")
	outsidePath := filepath.Join(root, "outside")
	for _, path := range []string{insidePath, outsidePath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := db.Open(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insideProject, _, insideAgent, err := store.CreateProject(ctx, "Inside", "", insidePath, "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	outsideProject, _, outsideAgent, err := store.CreateProject(ctx, "Outside", "", outsidePath, "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Paths: config.PathsConfig{HomeDir: root, DefaultProjectDir: projectsRoot},
		Security: config.SecurityConfig{
			AccessPasswordHash:      hash,
			AllowRemoteFullAccess:   true,
			DefaultRemoteAccessMode: remoteAccessModeRestricted,
			CredentialRevision:      1,
		},
	}
	app := New(cfg, store, nil, nil)
	restrictedCookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	fullCookies := loginRemoteAccess(t, app, remoteAccessModeFull)

	outsideRequest := newTestRequest(http.MethodGet, "/api/agents/"+outsideAgent.ID, nil)
	outsideRequest.Host = "remote.example.test"
	markRemoteHTTPS(outsideRequest)
	for _, cookie := range restrictedCookies {
		outsideRequest.AddCookie(cookie)
	}
	outsideResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(outsideResponse, outsideRequest)
	if outsideResponse.Code != http.StatusNotFound {
		t.Fatalf("restricted session should hide an out-of-root Agent, got %d: %s", outsideResponse.Code, outsideResponse.Body.String())
	}

	insideRequest := newTestRequest(http.MethodGet, "/api/agents/"+insideAgent.ID, nil)
	insideRequest.Host = "remote.example.test"
	markRemoteHTTPS(insideRequest)
	for _, cookie := range restrictedCookies {
		insideRequest.AddCookie(cookie)
	}
	insideResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(insideResponse, insideRequest)
	if insideResponse.Code != http.StatusOK {
		t.Fatalf("restricted session should retain in-root Agent access, got %d: %s", insideResponse.Code, insideResponse.Body.String())
	}

	listRequest := newTestRequest(http.MethodGet, "/api/agents", nil)
	listRequest.Host = "remote.example.test"
	markRemoteHTTPS(listRequest)
	for _, cookie := range restrictedCookies {
		listRequest.AddCookie(cookie)
	}
	listResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), insideAgent.ID) || strings.Contains(listResponse.Body.String(), outsideAgent.ID) {
		t.Fatalf("restricted Agent collection leaked an out-of-root resource: %d %s", listResponse.Code, listResponse.Body.String())
	}

	navigationRequest := newTestRequest(http.MethodGet, "/api/navigation", nil)
	navigationRequest.Host = "remote.example.test"
	markRemoteHTTPS(navigationRequest)
	for _, cookie := range restrictedCookies {
		navigationRequest.AddCookie(cookie)
	}
	navigationResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(navigationResponse, navigationRequest)
	if navigationResponse.Code != http.StatusOK || !strings.Contains(navigationResponse.Body.String(), insideProject.ID) || strings.Contains(navigationResponse.Body.String(), outsideProject.ID) || strings.Contains(navigationResponse.Body.String(), outsidePath) {
		t.Fatalf("restricted navigation leaked an out-of-root project: %d %s", navigationResponse.Code, navigationResponse.Body.String())
	}

	fullRequest := newTestRequest(http.MethodGet, "/api/agents/"+outsideAgent.ID, nil)
	fullRequest.Host = "remote.example.test"
	markRemoteHTTPS(fullRequest)
	for _, cookie := range fullCookies {
		fullRequest.AddCookie(cookie)
	}
	fullResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(fullResponse, fullRequest)
	if fullResponse.Code != http.StatusOK {
		t.Fatalf("full session should retain host Agent access, got %d: %s", fullResponse.Code, fullResponse.Body.String())
	}
}

func TestRemotePasswordHeadersShareTheLoginFailureLimit(t *testing.T) {
	app := remoteAccessTestServer(t)
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	app.clock = func() time.Time { return now }
	for attempt := 1; attempt <= remoteAccessMaxFailures; attempt++ {
		request := newTestRequest(http.MethodGet, "/api/health", nil)
		request.Host = "remote.example.test"
		markRemoteHTTPS(request)
		request.RemoteAddr = "203.0.113.80:4444"
		request.Header.Set(remoteAccessHeader, "wrong-password")
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, request)
		want := http.StatusUnauthorized
		if attempt == remoteAccessMaxFailures {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("password header attempt %d returned %d, want %d: %s", attempt, recorder.Code, want, recorder.Body.String())
		}
	}
	lockedValid := newTestRequest(http.MethodGet, "/api/health", nil)
	lockedValid.Host = "remote.example.test"
	markRemoteHTTPS(lockedValid)
	lockedValid.RemoteAddr = "203.0.113.80:4444"
	lockedValid.Header.Set(remoteAccessHeader, "Correct-Horse-1!")
	lockedRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(lockedRecorder, lockedValid)
	if lockedRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("locked password header should remain rate limited, got %d", lockedRecorder.Code)
	}

	now = now.Add(remoteAccessLockDuration + time.Second)
	valid := newTestRequest(http.MethodGet, "/api/health", nil)
	valid.Host = "remote.example.test"
	markRemoteHTTPS(valid)
	valid.RemoteAddr = "203.0.113.80:4444"
	valid.Header.Set(remoteAccessHeader, "Correct-Horse-1!")
	validRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(validRecorder, valid)
	if validRecorder.Code != http.StatusOK {
		t.Fatalf("valid password header should recover after the lock expires, got %d: %s", validRecorder.Code, validRecorder.Body.String())
	}
}

func TestRemoteSessionExpiresAndStoresOnlyTokenHash(t *testing.T) {
	app := remoteAccessTestServer(t)
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	app.clock = func() time.Time { return now }
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	var sessionToken string
	for _, cookie := range cookies {
		if cookie.Name == remoteAccessCookieName {
			sessionToken = cookie.Value
		}
	}
	if sessionToken == "" {
		t.Fatal("expected a remote session cookie")
	}
	app.remoteAccessMu.Lock()
	if _, storedRaw := app.remoteAccessSessions[sessionToken]; storedRaw {
		app.remoteAccessMu.Unlock()
		t.Fatal("remote session map must not be keyed by the raw token")
	}
	for key, session := range app.remoteAccessSessions {
		if key == sessionToken || session.TokenHash == sessionToken || key != session.TokenHash {
			app.remoteAccessMu.Unlock()
			t.Fatal("remote session storage exposed or mismatched the raw token")
		}
	}
	app.remoteAccessMu.Unlock()

	now = now.Add(remoteAccessSessionTTL + time.Second)
	request := newTestRequest(http.MethodGet, "/api/health", nil)
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired remote session should be rejected, got %d", recorder.Code)
	}
}

func TestRemoteWebSocketValidationUsesSessionWithoutLocalToken(t *testing.T) {
	app := remoteAccessTestServer(t)
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	request := newTestRequest(http.MethodGet, "/ws/agent?id=test", nil)
	request.Host = "remote.example.test"
	markRemoteHTTPS(request)
	request.Header.Set("Origin", "https://remote.example.test")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	if !app.validateWebSocketRequest(recorder, request) {
		t.Fatalf("valid remote session should authorize websocket without local token: %d %s", recorder.Code, recorder.Body.String())
	}

	invalid := newTestRequest(http.MethodGet, "/ws/agent?id=test", nil)
	invalid.Host = "remote.example.test"
	markRemoteHTTPS(invalid)
	invalid.Header.Set("Origin", "https://remote.example.test")
	invalid.Header.Set(localTokenHeader, app.localToken)
	invalidRecorder := httptest.NewRecorder()
	if app.validateWebSocketRequest(invalidRecorder, invalid) || invalidRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("canonical local token alone must not authorize a remote websocket, got %d", invalidRecorder.Code)
	}
}
