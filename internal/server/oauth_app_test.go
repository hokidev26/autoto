package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/oauthapp"
	"autoto/internal/providers"
	"autoto/internal/tools"

	"github.com/go-chi/chi/v5/middleware"
)

func TestOAuthAppPageDoesNotExposeLocalTokenAndDisabledSessionFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "oauth-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	handler := app.Routes()

	pageRequest := httptest.NewRequest(http.MethodGet, "/app", nil)
	pageRequest.RemoteAddr = "127.0.0.1:1234"
	pageRequest.Host = "localhost"
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK {
		t.Fatalf("GET /app status=%d body=%s", pageResponse.Code, pageResponse.Body.String())
	}
	body := pageResponse.Body.String()
	if !strings.Contains(body, "Autoto OAuth App") {
		t.Fatalf("OAuth app page was not served: %s", body)
	}
	if strings.Contains(body, app.localToken) || strings.Contains(body, "AUTOTO_LOCAL_TOKEN=") {
		t.Fatal("OAuth app page exposed the process-local management token")
	}
	if pageResponse.Header().Get("Content-Security-Policy") == "" || pageResponse.Header().Get("Cache-Control") == "" {
		t.Fatalf("OAuth app page is missing security headers: %+v", pageResponse.Header())
	}

	sessionRequest := httptest.NewRequest(http.MethodGet, "/app/auth/session", nil)
	sessionRequest.RemoteAddr = "127.0.0.1:1234"
	sessionRequest.Host = "localhost"
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusServiceUnavailable || !strings.Contains(sessionResponse.Body.String(), "oidc_disabled") {
		t.Fatalf("disabled OAuth app session did not fail closed: status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
}

func TestOAuthAppLoginCookieBindsCallbackAndUsesStrictAttributes(t *testing.T) {
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 provider.URL,
				"authorization_endpoint": provider.URL + "/authorize",
				"token_endpoint":         provider.URL + "/token",
				"jwks_uri":               provider.URL + "/jwks",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "oauth-login-cookie.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Config{Auth: config.AuthConfig{OAuthApp: config.OAuthAppConfig{
		Enabled:         true,
		IssuerURL:       provider.URL,
		ClientID:        "cookie-test-client",
		RedirectURL:     provider.URL + oauthAppCallbackPath,
		SessionTTLHours: 8,
	}}}
	app := New(cfg, store, nil, nil)
	app.SetIntegrationHTTPClient(provider.Client())
	handler := app.Routes()

	loginRequest := httptest.NewRequest(http.MethodGet, "/app/auth/login?returnTo=%2Fapp%2Fsettings", nil)
	loginRequest.RemoteAddr = "127.0.0.1:1234"
	loginRequest.Host = "localhost"
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusFound {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var bindingCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == oauthAppLoginCookieName {
			bindingCookie = cookie
			break
		}
	}
	if bindingCookie == nil {
		t.Fatal("login response did not set the browser-binding cookie")
	}
	if bindingCookie.Value == "" || bindingCookie.Path != oauthAppCallbackPath || bindingCookie.MaxAge <= 0 || bindingCookie.Expires.IsZero() || !bindingCookie.HttpOnly || !bindingCookie.Secure || bindingCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected browser-binding cookie attributes: %+v", bindingCookie)
	}
	location, err := loginResponse.Result().Location()
	if err != nil {
		t.Fatal(err)
	}
	state := location.Query().Get("state")
	if state == "" || strings.Contains(location.String(), bindingCookie.Value) {
		t.Fatalf("authorization redirect did not keep browser binding private: %s", location)
	}

	crossBrowserRequest := httptest.NewRequest(http.MethodGet, oauthAppCallbackPath+"?code=cross-browser-code&state="+state, nil)
	crossBrowserRequest.RemoteAddr = "127.0.0.1:1234"
	crossBrowserRequest.Host = "localhost"
	crossBrowserResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossBrowserResponse, crossBrowserRequest)
	if crossBrowserResponse.Code != http.StatusBadRequest {
		t.Fatalf("cross-browser callback status=%d body=%s", crossBrowserResponse.Code, crossBrowserResponse.Body.String())
	}
	var cleared *http.Cookie
	for _, cookie := range crossBrowserResponse.Result().Cookies() {
		if cookie.Name == oauthAppLoginCookieName {
			cleared = cookie
			break
		}
	}
	if cleared == nil || cleared.Value != "" || cleared.Path != oauthAppCallbackPath || cleared.MaxAge >= 0 || !cleared.HttpOnly || !cleared.Secure || cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("callback did not one-time clear the browser-binding cookie: %+v", cleared)
	}
}

func TestOAuthAppCallbackRequestLogRedactsAllQueryValues(t *testing.T) {
	var output bytes.Buffer
	formatter := &redactingLogFormatter{delegate: &middleware.DefaultLogFormatter{Logger: log.New(&output, "", 0), NoColor: true}}
	request := httptest.NewRequest(http.MethodGet, oauthAppCallbackPath+"?code=authorization-code-secret&state=state-secret&error_description=provider-secret&session_state=session-secret", nil)
	entry := formatter.NewLogEntry(request)
	entry.Write(http.StatusBadRequest, 0, http.Header{}, time.Millisecond, nil)
	logged := output.String()
	for _, secret := range []string{"authorization-code-secret", "state-secret", "provider-secret", "session-secret"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("callback request log leaked %q: %s", secret, logged)
		}
	}
	for _, key := range []string{"code=%5BREDACTED%5D", "state=%5BREDACTED%5D", "error_description=%5BREDACTED%5D", "session_state=%5BREDACTED%5D"} {
		if !strings.Contains(logged, key) {
			t.Fatalf("callback request log did not preserve redacted key %q: %s", key, logged)
		}
	}
}

func TestOAuthAppLoginPolicyRechecksVerifiedEmailForExistingIdentities(t *testing.T) {
	appConfig := config.OAuthAppConfig{AllowedEmailDomains: []string{"example.com"}}
	if !oauthAppAllowsIdentity(appConfig, oauthapp.Identity{Email: "person@example.com", EmailVerified: true}) {
		t.Fatal("verified identity in an allowed domain was rejected")
	}
	for _, identity := range []oauthapp.Identity{
		{Email: "person@example.com", EmailVerified: false},
		{Email: "person@other.test", EmailVerified: true},
	} {
		if oauthAppAllowsIdentity(appConfig, identity) {
			t.Fatalf("login policy stopped applying to an existing identity: %+v", identity)
		}
	}
}

func TestOAuthAppBFFEnforcesMembershipScopesAndResponseRedaction(t *testing.T) {
	fixture := newOAuthAppServerFixture(t, false, false)

	session := fixture.request(t, http.MethodGet, "/app/auth/session", nil, "")
	if session.Code != http.StatusOK || !strings.Contains(session.Body.String(), `"authenticated":true`) {
		t.Fatalf("session response: status=%d body=%s", session.Code, session.Body.String())
	}
	if strings.Contains(session.Body.String(), fixture.token) || strings.Contains(session.Body.String(), "tokenHash") {
		t.Fatal("session response exposed an application credential")
	}

	projects := fixture.request(t, http.MethodGet, "/app/api/projects", nil, "")
	if projects.Code != http.StatusOK || !strings.Contains(projects.Body.String(), fixture.project.Name) {
		t.Fatalf("projects response: status=%d body=%s", projects.Code, projects.Body.String())
	}
	if strings.Contains(projects.Body.String(), fixture.project.GitPath) || strings.Contains(projects.Body.String(), "gitPath") {
		t.Fatalf("projects response exposed a filesystem path: %s", projects.Body.String())
	}

	agents := fixture.request(t, http.MethodGet, "/app/api/projects/"+fixture.project.ID+"/agents", nil, "")
	if agents.Code != http.StatusOK || !strings.Contains(agents.Body.String(), fixture.agent.Title) {
		t.Fatalf("agents response: status=%d body=%s", agents.Code, agents.Body.String())
	}
	for _, forbidden := range []string{fixture.agent.Model, fixture.agent.CWD, "systemPrompt", "permissionMode"} {
		if forbidden != "" && strings.Contains(agents.Body.String(), forbidden) {
			t.Fatalf("agents response exposed internal field %q: %s", forbidden, agents.Body.String())
		}
	}

	messages := fixture.request(t, http.MethodGet, "/app/api/projects/"+fixture.project.ID+"/agents/"+fixture.agent.ID+"/messages", nil, "")
	if messages.Code != http.StatusOK || !strings.Contains(messages.Body.String(), "visible message") {
		t.Fatalf("messages response: status=%d body=%s", messages.Code, messages.Body.String())
	}
	for _, forbidden := range []string{"hidden-json-secret", "provider-state-secret", fixture.user.ID, "contentJson", "providerState"} {
		if strings.Contains(messages.Body.String(), forbidden) {
			t.Fatalf("messages response exposed internal value %q: %s", forbidden, messages.Body.String())
		}
	}

	otherUser, err := fixture.store.CreateUser(context.Background(), "other-user", "other-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, _, _, err := fixture.store.CreateProjectForUser(context.Background(), otherUser.ID, "Other", "", t.TempDir(), "fake:other", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := fixture.request(t, http.MethodGet, "/app/api/projects/"+otherProject.ID+"/agents", nil, "")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-project access status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}

	blockedSubmit := fixture.request(t, http.MethodPost, "/app/api/projects/"+fixture.project.ID+"/agents/"+fixture.agent.ID+"/messages", []byte(`{"content":"should not run"}`), "application/json")
	if blockedSubmit.Code != http.StatusForbidden || !strings.Contains(blockedSubmit.Body.String(), "insufficient_scope") {
		t.Fatalf("disabled read-only submission was not scope-gated: status=%d body=%s", blockedSubmit.Code, blockedSubmit.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/app/auth/logout", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "localhost"
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.AddCookie(&http.Cookie{Name: oauthAppSessionCookieName, Value: fixture.token})
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := fixture.store.GetOAuthAppSessionByTokenHash(context.Background(), db.HashSessionToken(fixture.token)); err != nil {
		t.Fatalf("cross-site logout revoked the session: %v", err)
	}
}

func TestOAuthAppMessagesExcludeToolResultsAndRedactCredentialText(t *testing.T) {
	fixture := newOAuthAppServerFixture(t, false, false)
	if _, err := fixture.store.AddMessage(context.Background(), db.Message{
		AgentID:      fixture.agent.ID,
		Role:         "user",
		ParentToolID: "tool-result-1",
		ContentText:  "tool output bearer tool-result-secret",
		CreatedAt:    db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.AddMessage(context.Background(), db.Message{
		AgentID:     fixture.agent.ID,
		Role:        "user",
		ContentText: "legacy tool output legacy-tool-result-secret",
		ContentJSON: json.RawMessage(`[{"type":"tool_result","output":"legacy-tool-result-secret"}]`),
		CreatedAt:   db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.AddMessage(context.Background(), db.Message{
		AgentID:     fixture.agent.ID,
		Role:        "assistant",
		ContentText: "Authorization: Bearer bearer-secret-value api_key=api-key-secret https://example.test/callback?token=query-secret",
		CreatedAt:   db.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	response := fixture.request(t, http.MethodGet, "/app/api/projects/"+fixture.project.ID+"/agents/"+fixture.agent.ID+"/messages", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{"tool-result-secret", "tool-result-1", "legacy-tool-result-secret", "bearer-secret-value", "api-key-secret", "query-secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("messages response exposed credential or tool result %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "[redacted]") {
		t.Fatalf("messages response did not mark redacted credential text: %s", body)
	}
}

func TestOAuthAppReadOnlySubmissionRequiresCurrentPolicyAndPersistedScope(t *testing.T) {
	fixture := newOAuthAppServerFixture(t, true, false)
	fixture.server.cfgMu.Lock()
	fixture.server.cfg.Auth.OAuthApp.AllowReadOnlyTasks = false
	fixture.server.cfgMu.Unlock()

	response := fixture.request(t, http.MethodPost, "/app/api/projects/"+fixture.project.ID+"/agents/"+fixture.agent.ID+"/messages", []byte(`{"content":"must remain revoked"}`), "application/json")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "insufficient_scope") {
		t.Fatalf("old session retained read-only submission after policy revocation: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOAuthAppReadOnlySubmissionCreatesReadOnlyCappedRun(t *testing.T) {
	fixture := newOAuthAppServerFixture(t, true, true)
	response := fixture.request(t, http.MethodPost, "/app/api/projects/"+fixture.project.ID+"/agents/"+fixture.agent.ID+"/messages", []byte(`{"content":"inspect the project without writing"}`), "application/json")
	if response.Code != http.StatusAccepted {
		t.Fatalf("read-only submission status=%d body=%s", response.Code, response.Body.String())
	}
	runs, err := fixture.store.ListRuns(context.Background(), fixture.agent.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("read-only submission did not create a run")
	}
	run := runs[0]
	if run.PermissionModeCap != "readOnly" || run.ExecutionMode != db.RunExecutionModeExecute {
		t.Fatalf("OAuth app run was not capped to readOnly: %+v", run)
	}
	var submitted oauthAppMessageView
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	var createdBy string
	if err := fixture.store.DB().QueryRowContext(context.Background(), `SELECT COALESCE(created_by, '') FROM agent_messages WHERE id = ?`, submitted.ID).Scan(&createdBy); err != nil {
		t.Fatal(err)
	}
	if createdBy != fixture.user.ID {
		t.Fatalf("OAuth app message audit user = %q", createdBy)
	}
}

type oauthAppServerFixture struct {
	server  *Server
	store   *db.Store
	handler http.Handler
	token   string
	user    db.User
	project db.Project
	agent   db.Agent
}

func newOAuthAppServerFixture(t *testing.T, allowReadOnlyTasks, withRunner bool) oauthAppServerFixture {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "oauth-app-server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	user, err := store.CreateUser(ctx, "oauth-user", "local-login-disabled-by-random-hash")
	if err != nil {
		t.Fatal(err)
	}
	project, _, primary, err := store.CreateProjectForUser(ctx, user.ID, "OAuth Project", "safe description", filepath.Join(t.TempDir(), "secret-worktree"), "fake:internal-model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{
		AgentID:           primary.ID,
		Role:              "assistant",
		ContentText:       "visible message",
		ContentJSON:       json.RawMessage(`[{"type":"text","text":"hidden-json-secret"}]`),
		ProviderStateJSON: json.RawMessage(`{"secret":"provider-state-secret"}`),
		CreatedBy:         user.ID,
		CreatedAt:         db.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	appConfig := config.OAuthAppConfig{
		Enabled:            true,
		IssuerURL:          "https://issuer.example",
		ClientID:           "oauth-app-client",
		RedirectURL:        "https://app.example/app/auth/callback",
		SessionTTLHours:    8,
		AllowReadOnlyTasks: allowReadOnlyTasks,
	}
	cfg := config.Config{Auth: config.AuthConfig{OAuthApp: appConfig}}
	var runner *agentpkg.Runner
	var hub *agentpkg.Hub
	var registry *providers.Registry
	if withRunner {
		registry = providers.NewRegistry()
		hub = agentpkg.NewHub()
		runner = agentpkg.NewRunner(store, registry, tools.NewRegistry(), hub, config.AgentConfig{MaxTurns: 1})
	}
	app := New(cfg, store, runner, hub, registry)
	token := "oauth-app-session-" + db.NewID()
	if _, err := store.CreateOAuthAppSession(ctx, db.OAuthAppSession{
		TokenHash: db.HashSessionToken(token),
		UserID:    user.ID,
		Scopes:    oauthAppLocalScopes(appConfig),
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	return oauthAppServerFixture{server: app, store: store, handler: app.Routes(), token: token, user: user, project: project, agent: primary}
}

func (fixture oauthAppServerFixture) request(t *testing.T, method, path string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "localhost"
	request.AddCookie(&http.Cookie{Name: oauthAppSessionCookieName, Value: fixture.token})
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}
