package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/geminiauth"
	"autoto/internal/grokauth"
	"autoto/internal/kimiauth"
	"autoto/internal/providers"
	"autoto/internal/subscriptionauth"
)

func TestGeminiOAuthLoginCallbackStoresAccountWithoutLeakingSecrets(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	provider := config.NormalizeProviderConfig(config.ProviderConfig{Name: config.ProviderTypeGemini, Type: config.ProviderTypeGemini})
	cfg := config.Config{Paths: config.PathsConfig{HomeDir: home}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}}}
	app := New(cfg, nil, nil, nil, registry)
	app.SetConfigPath(filepath.Join(home, "config.json"))
	app.subscriptionOAuthTestConfig = &subscriptionOAuthLoginTestConfig{GeminiListenAddress: "127.0.0.1:0", SessionTTL: time.Minute}
	fake := &fakeGeminiOAuthLoginClient{
		tokens: &geminiauth.TokenData{
			AccessToken: "gemini-access-secret", RefreshToken: "gemini-refresh-secret", IDToken: "gemini-id-secret",
			TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		},
		userinfo:  geminiauth.UserInfo{Subject: "gemini-subject", Email: "gemini@example.test"},
		projectID: "gemini-project",
	}
	app.geminiOAuthClientFactory = func() geminiOAuthLoginClient { return fake }

	started, body := startSubscriptionOAuthLoginForTest(t, app, subscriptionauth.ProviderGemini)
	if started.Status != subscriptionOAuthLoginPending || started.LoginID == "" || started.AuthURL == "" {
		t.Fatalf("Gemini OAuth start response invalid: %+v", started)
	}
	assertSubscriptionOAuthLoginResponseSafe(t, body, "gemini-access-secret", "gemini-refresh-secret", "gemini-id-secret")
	authorizeURL, err := url.Parse(started.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	redirectURI, err := url.Parse(authorizeURL.Query().Get("redirect_uri"))
	if err != nil || redirectURI.Hostname() != "localhost" || redirectURI.Port() == "" {
		t.Fatalf("Gemini callback URI invalid: %v err=%v", redirectURI, err)
	}

	mismatch := *redirectURI
	mismatch.RawQuery = url.Values{"code": {"gemini-code-secret"}, "state": {"wrong-state"}}.Encode()
	mismatchResponse, err := http.Get(mismatch.String())
	if err != nil {
		t.Fatal(err)
	}
	mismatchResponse.Body.Close()
	if mismatchResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("Gemini state mismatch returned %d", mismatchResponse.StatusCode)
	}

	callback := *redirectURI
	callback.RawQuery = url.Values{"code": {"gemini-code-secret"}, "state": {authorizeURL.Query().Get("state")}}.Encode()
	callbackResponse, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	callbackBody, _ := io.ReadAll(callbackResponse.Body)
	callbackResponse.Body.Close()
	if callbackResponse.StatusCode != http.StatusOK {
		t.Fatalf("Gemini callback failed: %d %s", callbackResponse.StatusCode, callbackBody)
	}
	for _, secret := range []string{"gemini-code-secret", "gemini-access-secret", "gemini-refresh-secret", "gemini-id-secret"} {
		if strings.Contains(string(callbackBody), secret) {
			t.Fatalf("Gemini callback HTML leaked secret %q", secret)
		}
	}
	if callbackResponse.Header.Get("Content-Security-Policy") != subscriptionOAuthCallbackCSP {
		t.Fatalf("Gemini callback CSP missing: %q", callbackResponse.Header.Get("Content-Security-Policy"))
	}

	completed, completedBody := getSubscriptionOAuthLoginForTest(t, app, subscriptionauth.ProviderGemini, started.LoginID)
	if completed.Status != subscriptionOAuthLoginCompleted || completed.AuthURL != "" || completed.Account == nil {
		t.Fatalf("Gemini completed response invalid: %+v", completed)
	}
	if completed.Account.Email != "gemini@example.test" || completed.Account.ProjectID != "gemini-project" {
		t.Fatalf("Gemini account metadata missing: %+v", completed.Account)
	}
	assertSubscriptionOAuthLoginResponseSafe(t, completedBody, "gemini-code-secret", "gemini-access-secret", "gemini-refresh-secret", "gemini-id-secret")
	if _, ok := registry.Get(subscriptionauth.ProviderGemini); !ok {
		t.Fatal("Gemini Provider was not registered after login")
	}
	store, err := app.nativeSubscriptionCredentialStore(subscriptionauth.ProviderGemini)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetByID(completed.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "gemini-access-secret" || stored.RefreshToken != "gemini-refresh-secret" || stored.ProjectID != "gemini-project" {
		t.Fatal("Gemini OAuth tokens or project metadata were not stored")
	}
	app.subscriptionOAuthMu.Lock()
	session := app.subscriptionOAuthLogins[started.LoginID]
	if session == nil || session.state != "" || session.redirectURI != "" || session.authURL != "" {
		app.subscriptionOAuthMu.Unlock()
		t.Fatalf("Gemini terminal session retained secrets: %+v", session)
	}
	app.subscriptionOAuthMu.Unlock()
}

func TestGrokDeviceOAuthLoginReusesPendingSessionAndStoresAccount(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	provider := config.NormalizeProviderConfig(config.ProviderConfig{Name: config.ProviderTypeGrok, Type: config.ProviderTypeGrok})
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}}}, nil, nil, nil, registry)
	fake := &fakeGrokOAuthLoginClient{
		device: &grokauth.DeviceCodeResponse{
			DeviceCode: "grok-device-secret", UserCode: "GROK-CODE", VerificationURI: "https://auth.x.ai/activate",
			VerificationURIComplete: "https://auth.x.ai/activate?user_code=GROK-CODE", ExpiresIn: 600,
			TokenEndpoint: "https://auth.x.ai/oauth/token",
		},
		result: make(chan *grokauth.TokenData, 1),
	}
	app.grokOAuthClientFactory = func() grokOAuthLoginClient { return fake }

	started, body := startSubscriptionOAuthLoginForTest(t, app, subscriptionauth.ProviderGrok)
	if started.Status != subscriptionOAuthLoginPending || started.UserCode != "GROK-CODE" || started.AuthURL == "" {
		t.Fatalf("Grok device start response invalid: %+v", started)
	}
	assertSubscriptionOAuthLoginResponseSafe(t, body, "grok-device-secret")
	reused, _ := startSubscriptionOAuthLoginForTest(t, app, subscriptionauth.ProviderGrok)
	if reused.LoginID != started.LoginID || reused.UserCode != started.UserCode {
		t.Fatalf("Grok pending session was not reused: first=%+v reused=%+v", started, reused)
	}

	fake.result <- &grokauth.TokenData{
		AccessToken: "grok-access-secret", RefreshToken: "grok-refresh-secret", IDToken: "grok-id-secret",
		TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		Email: "grok@example.test", Subject: "grok-subject",
	}
	completed, completedBody := waitForSubscriptionOAuthStatus(t, app, subscriptionauth.ProviderGrok, started.LoginID, subscriptionOAuthLoginCompleted)
	if completed.Account == nil || completed.Account.Email != "grok@example.test" || completed.Account.TokenEndpoint != "https://auth.x.ai/oauth/token" {
		t.Fatalf("Grok account summary invalid: %+v", completed.Account)
	}
	assertSubscriptionOAuthLoginResponseSafe(t, completedBody, "grok-device-secret", "grok-access-secret", "grok-refresh-secret", "grok-id-secret")
	store, err := app.nativeSubscriptionCredentialStore(subscriptionauth.ProviderGrok)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetByID(completed.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "grok-access-secret" || stored.RefreshToken != "grok-refresh-secret" || stored.TokenEndpoint != "https://auth.x.ai/oauth/token" {
		t.Fatal("Grok OAuth credentials were not stored correctly")
	}
	if _, ok := registry.Get(subscriptionauth.ProviderGrok); !ok {
		t.Fatal("Grok Provider was not registered after login")
	}
}

func TestKimiDeviceOAuthLoginCanBeCancelledWithoutSavingCredentials(t *testing.T) {
	home := t.TempDir()
	provider := config.NormalizeProviderConfig(config.ProviderConfig{Name: config.ProviderTypeKimi, Type: config.ProviderTypeKimi})
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}}}, nil, nil, nil, providers.NewRegistry())
	fake := &fakeKimiOAuthLoginClient{
		device: &kimiauth.DeviceCodeResponse{
			DeviceCode: "kimi-device-secret", UserCode: "KIMI-CODE", VerificationURI: "https://auth.kimi.com/device",
			VerificationURIComplete: "https://auth.kimi.com/device?user_code=KIMI-CODE", ExpiresIn: 600, DeviceID: "kimi-device-id",
		},
		result:    make(chan *kimiauth.AuthBundle),
		cancelled: make(chan struct{}),
	}
	app.kimiOAuthClientFactory = func() kimiOAuthLoginClient { return fake }

	started, body := startSubscriptionOAuthLoginForTest(t, app, subscriptionauth.ProviderKimi)
	assertSubscriptionOAuthLoginResponseSafe(t, body, "kimi-device-secret")
	cancelled, cancelledBody := cancelSubscriptionOAuthLoginForTest(t, app, subscriptionauth.ProviderKimi, started.LoginID)
	if cancelled.Status != subscriptionOAuthLoginCancelled || cancelled.AuthURL != "" || cancelled.UserCode != "" {
		t.Fatalf("Kimi cancellation response invalid: %+v", cancelled)
	}
	assertSubscriptionOAuthLoginResponseSafe(t, cancelledBody, "kimi-device-secret")
	select {
	case <-fake.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Kimi device wait was not cancelled")
	}
	store, err := app.nativeSubscriptionCredentialStore(subscriptionauth.ProviderKimi)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("cancelled Kimi login stored credentials: %+v", accounts)
	}
}

func TestSubscriptionOAuthLoginRoutesRejectRemoteAccess(t *testing.T) {
	cfg := config.Config{
		Paths:    config.PathsConfig{HomeDir: t.TempDir()},
		Security: config.SecurityConfig{AllowRemoteFullAccess: true, DefaultRemoteAccessMode: remoteAccessModeFull},
	}
	app := New(cfg, nil, nil, nil, providers.NewRegistry())
	token, err := app.newRemoteAccessSessionForConfig(remoteAccessModeFull, app.configSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/providers/oauth/gemini/login/start"},
		{http.MethodGet, "/api/providers/oauth/grok/login/fixture"},
		{http.MethodDelete, "/api/providers/oauth/kimi/login/fixture"},
	} {
		request := newTestRequest(test.method, test.path, nil)
		request.Host = "remote.example.test"
		markRemoteHTTPS(request)
		request.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: token})
		response := httptest.NewRecorder()
		app.Routes().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("remote %s %s was not rejected: %d %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

type fakeGeminiOAuthLoginClient struct {
	tokens    *geminiauth.TokenData
	userinfo  geminiauth.UserInfo
	projectID string
}

func (f *fakeGeminiOAuthLoginClient) BuildAuthURL(state, redirectURI string) (string, error) {
	parsed, _ := url.Parse("https://accounts.google.com/o/oauth2/v2/auth")
	query := parsed.Query()
	query.Set("state", state)
	query.Set("redirect_uri", redirectURI)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (f *fakeGeminiOAuthLoginClient) ExchangeCode(context.Context, string, string) (*geminiauth.TokenData, error) {
	return f.tokens, nil
}

func (f *fakeGeminiOAuthLoginClient) FetchUserInfo(context.Context, string) (geminiauth.UserInfo, error) {
	return f.userinfo, nil
}

func (f *fakeGeminiOAuthLoginClient) FetchProjectID(context.Context, string) (string, error) {
	return f.projectID, nil
}

type fakeGrokOAuthLoginClient struct {
	device *grokauth.DeviceCodeResponse
	result chan *grokauth.TokenData
}

func (f *fakeGrokOAuthLoginClient) StartDeviceFlow(context.Context) (*grokauth.DeviceCodeResponse, error) {
	copy := *f.device
	return &copy, nil
}

func (f *fakeGrokOAuthLoginClient) Wait(ctx context.Context, _ *grokauth.DeviceCodeResponse) (*grokauth.TokenData, error) {
	select {
	case tokens := <-f.result:
		return tokens, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type fakeKimiOAuthLoginClient struct {
	device    *kimiauth.DeviceCodeResponse
	result    chan *kimiauth.AuthBundle
	cancelled chan struct{}
	once      sync.Once
}

func (f *fakeKimiOAuthLoginClient) StartDeviceFlow(context.Context) (*kimiauth.DeviceCodeResponse, error) {
	copy := *f.device
	return &copy, nil
}

func (f *fakeKimiOAuthLoginClient) Wait(ctx context.Context, _ *kimiauth.DeviceCodeResponse) (*kimiauth.AuthBundle, error) {
	select {
	case result := <-f.result:
		return result, nil
	case <-ctx.Done():
		f.once.Do(func() { close(f.cancelled) })
		return nil, ctx.Err()
	}
}

func startSubscriptionOAuthLoginForTest(t *testing.T, app *Server, provider string) (subscriptionOAuthLoginResponse, []byte) {
	t.Helper()
	response := subscriptionOAuthAPIRequest(t, app, http.MethodPost, "/api/providers/oauth/"+provider+"/login/start", http.StatusOK)
	var payload subscriptionOAuthLoginResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload, response.Body.Bytes()
}

func getSubscriptionOAuthLoginForTest(t *testing.T, app *Server, provider, loginID string) (subscriptionOAuthLoginResponse, []byte) {
	t.Helper()
	response := subscriptionOAuthAPIRequest(t, app, http.MethodGet, "/api/providers/oauth/"+provider+"/login/"+loginID, http.StatusOK)
	var payload subscriptionOAuthLoginResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload, response.Body.Bytes()
}

func cancelSubscriptionOAuthLoginForTest(t *testing.T, app *Server, provider, loginID string) (subscriptionOAuthLoginResponse, []byte) {
	t.Helper()
	response := subscriptionOAuthAPIRequest(t, app, http.MethodDelete, "/api/providers/oauth/"+provider+"/login/"+loginID, http.StatusOK)
	var payload subscriptionOAuthLoginResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload, response.Body.Bytes()
}

func waitForSubscriptionOAuthStatus(t *testing.T, app *Server, provider, loginID string, status subscriptionOAuthLoginStatus) (subscriptionOAuthLoginResponse, []byte) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, body := getSubscriptionOAuthLoginForTest(t, app, provider, loginID)
		if response.Status == status {
			return response, body
		}
		if response.Status == subscriptionOAuthLoginFailed || response.Status == subscriptionOAuthLoginCancelled || response.Status == subscriptionOAuthLoginExpired {
			t.Fatalf("OAuth login reached unexpected terminal state: %+v", response)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("OAuth login did not reach %s", status)
	return subscriptionOAuthLoginResponse{}, nil
}

func subscriptionOAuthAPIRequest(t *testing.T, app *Server, method, path string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	request := newTestRequest(method, path, nil)
	request.Header.Set(localTokenHeader, app.localToken)
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	return response
}

func assertSubscriptionOAuthLoginResponseSafe(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	text := string(body)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("OAuth login response leaked secret %q: %s", secret, text)
		}
	}
	for _, field := range []string{"device_code", "access_token", "refresh_token", "id_token", "client_secret"} {
		if strings.Contains(strings.ToLower(text), field) {
			t.Fatalf("OAuth login response included sensitive field %q: %s", field, text)
		}
	}
}
