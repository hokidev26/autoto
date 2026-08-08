package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/config"
	"autoto/internal/providers"
)

func TestCodexOAuthLoginStartCallbackStoresAndRegistersWithoutSecrets(t *testing.T) {
	home := t.TempDir()
	registry := providers.NewRegistry()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil, registry)
	app.SetConfigPath(filepath.Join(home, "config.json"))

	accessOne := testCodexOAuthJWT("oauth-account", "oauth@example.test", "plus", time.Now().Add(time.Hour).Unix())
	accessTwo := testCodexOAuthJWT("oauth-account", "oauth@example.test", "plus", time.Now().Add(2*time.Hour).Unix())
	const (
		refreshOne = "rt_oauth_login_one"
		refreshTwo = "rt_oauth_login_two"
		idToken    = "id-token-login-fixture"
	)
	var tokenRequests atomic.Int32
	var expectedMu sync.Mutex
	expectedChallenges := map[string]string{}
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		tokenRequests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		code := r.Form.Get("code")
		verifier := r.Form.Get("code_verifier")
		challenge, err := codexauth.PKCEChallenge(verifier)
		if err != nil {
			t.Fatalf("invalid PKCE verifier received: %v", err)
		}
		expectedMu.Lock()
		expected := expectedChallenges[code]
		expectedMu.Unlock()
		if challenge != expected || r.Form.Get("client_id") != "fixture-client" || r.Form.Get("grant_type") != "authorization_code" {
			t.Fatalf("unexpected token exchange: code=%q challenge=%q expected=%q form=%v", code, challenge, expected, r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		switch code {
		case "login-code-one":
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"id_token":%q,"expires_in":3600}`, accessOne, refreshOne, idToken)
		case "login-code-two":
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"id_token":%q,"expires_in":7200}`, accessTwo, refreshTwo, idToken)
		default:
			http.Error(w, "invalid code", http.StatusBadRequest)
		}
	}))
	defer issuer.Close()
	app.codexOAuthTestConfig = &codexOAuthLoginTestConfig{
		Issuer: issuer.URL, ClientID: "fixture-client", ListenAddress: "127.0.0.1:0", HTTPClient: issuer.Client(), SessionTTL: time.Minute,
	}

	first := startCodexOAuthLoginForTest(t, app)
	if first.Status != codexOAuthLoginPending || first.LoginID == "" || first.AuthURL == "" {
		t.Fatalf("unexpected start response: %+v", first)
	}
	firstAuth, err := url.Parse(first.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	if firstAuth.Query().Get("client_id") != "fixture-client" || firstAuth.Query().Get("scope") != codexauth.OAuthScope {
		t.Fatalf("unexpected authorize URL: %s", first.AuthURL)
	}
	redirect, err := url.Parse(firstAuth.Query().Get("redirect_uri"))
	if err != nil || redirect.Hostname() != "localhost" || redirect.Port() == "" || redirect.Port() == "0" {
		t.Fatalf("random callback listener was not reflected safely: redirect=%v err=%v", redirect, err)
	}

	reused := startCodexOAuthLoginForTest(t, app)
	if reused.LoginID != first.LoginID || reused.AuthURL != first.AuthURL || reused.Status != codexOAuthLoginPending {
		t.Fatalf("duplicate start did not reuse active session: first=%+v reused=%+v", first, reused)
	}

	expectedMu.Lock()
	expectedChallenges["login-code-one"] = firstAuth.Query().Get("code_challenge")
	expectedMu.Unlock()
	callback := *redirect
	callback.RawQuery = url.Values{"code": {"login-code-one"}, "state": {firstAuth.Query().Get("state")}}.Encode()
	callbackResponse, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	callbackBody, _ := io.ReadAll(callbackResponse.Body)
	callbackResponse.Body.Close()
	if callbackResponse.StatusCode != http.StatusOK {
		t.Fatalf("callback failed: %d %s", callbackResponse.StatusCode, callbackBody)
	}
	assertCodexOAuthHTMLSecurity(t, callbackResponse)
	assertNoCodexOAuthSecrets(t, callbackBody, accessOne, refreshOne, idToken, "login-code-one")

	status := getCodexOAuthLoginForTest(t, app, first.LoginID)
	if status.Status != codexOAuthLoginCompleted || status.AuthURL != "" || status.Account == nil || status.Account.AccountID != "oauth-account" || status.Account.Email != "oauth@example.test" {
		t.Fatalf("unexpected completed status: %+v", status)
	}
	statusJSON, _ := json.Marshal(status)
	assertNoCodexOAuthSecrets(t, statusJSON, accessOne, refreshOne, idToken, "login-code-one")
	assertCodexOAuthSessionSecretsCleared(t, app, first.LoginID)
	if _, ok := registry.Get(codexauth.DefaultProviderName); !ok {
		t.Fatal("Codex provider was not registered after OAuth login")
	}
	accounts, err := app.codexCredentials.ListAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("OAuth credential was not stored: accounts=%+v err=%v", accounts, err)
	}

	second := startCodexOAuthLoginForTest(t, app)
	if second.LoginID == first.LoginID {
		t.Fatal("terminal login session was unexpectedly reused")
	}
	secondAuth, _ := url.Parse(second.AuthURL)
	secondRedirect, _ := url.Parse(secondAuth.Query().Get("redirect_uri"))
	expectedMu.Lock()
	expectedChallenges["login-code-two"] = secondAuth.Query().Get("code_challenge")
	expectedMu.Unlock()
	secondRedirect.RawQuery = url.Values{"code": {"login-code-two"}, "state": {secondAuth.Query().Get("state")}}.Encode()
	secondCallback, err := http.Get(secondRedirect.String())
	if err != nil {
		t.Fatal(err)
	}
	secondBody, _ := io.ReadAll(secondCallback.Body)
	secondCallback.Body.Close()
	if secondCallback.StatusCode != http.StatusOK {
		t.Fatalf("second callback failed: %d %s", secondCallback.StatusCode, secondBody)
	}
	assertNoCodexOAuthSecrets(t, secondBody, accessTwo, refreshTwo, idToken, "login-code-two")
	accounts, err = app.codexCredentials.ListAccounts()
	if err != nil || len(accounts) != 1 || accounts[0].ID != status.Account.ID {
		t.Fatalf("same OAuth account was duplicated instead of updated: accounts=%+v err=%v", accounts, err)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("expected exactly two one-time token exchanges, got %d", tokenRequests.Load())
	}
}

func TestCodexOAuthLoginStartReusesExchangingSession(t *testing.T) {
	startedExchange := make(chan struct{}, 1)
	releaseExchange := make(chan struct{})
	access := testCodexOAuthJWT("exchange-account", "exchange@example.test", "plus", time.Now().Add(time.Hour).Unix())
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case startedExchange <- struct{}{}:
		default:
		}
		<-releaseExchange
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt_exchange_fixture"}`, access)
	}))
	defer issuer.Close()
	app := newCodexOAuthLoginTestServer(t, issuer, time.Minute)
	started := startCodexOAuthLoginForTest(t, app)
	authURL, _ := url.Parse(started.AuthURL)
	callback, _ := url.Parse(authURL.Query().Get("redirect_uri"))
	callback.RawQuery = url.Values{"code": {"exchange-code"}, "state": {authURL.Query().Get("state")}}.Encode()

	callbackResult := make(chan error, 1)
	go func() {
		response, err := http.Get(callback.String())
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("callback returned HTTP %d", response.StatusCode)
			}
		}
		callbackResult <- err
	}()
	select {
	case <-startedExchange:
	case <-time.After(time.Second):
		t.Fatal("token exchange did not start")
	}
	reused := startCodexOAuthLoginForTest(t, app)
	if reused.LoginID != started.LoginID || reused.AuthURL != started.AuthURL || reused.Status != codexOAuthLoginExchanging {
		t.Fatalf("exchanging session was not reused: started=%+v reused=%+v", started, reused)
	}
	close(releaseExchange)
	if err := <-callbackResult; err != nil {
		t.Fatal(err)
	}
	assertCodexOAuthSessionSecretsCleared(t, app, started.LoginID)
}

func TestCodexOAuthCallbackRejectsMethodHostAndStateWithoutExchange(t *testing.T) {
	var tokenRequests atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenRequests.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"should-not-be-returned"}`))
	}))
	defer issuer.Close()
	app := newCodexOAuthLoginTestServer(t, issuer, time.Minute)
	started := startCodexOAuthLoginForTest(t, app)
	authURL, _ := url.Parse(started.AuthURL)
	redirect, _ := url.Parse(authURL.Query().Get("redirect_uri"))

	postRequest, _ := http.NewRequest(http.MethodPost, redirect.String(), nil)
	postResponse, err := http.DefaultClient.Do(postRequest)
	if err != nil {
		t.Fatal(err)
	}
	postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected method rejection, got %d", postResponse.StatusCode)
	}

	wrongHostURL := *redirect
	wrongHostURL.Host = "127.0.0.1:" + redirect.Port()
	wrongHostURL.RawQuery = url.Values{"code": {"host-code"}, "state": {authURL.Query().Get("state")}}.Encode()
	wrongHostRequest, _ := http.NewRequest(http.MethodGet, wrongHostURL.String(), nil)
	wrongHostRequest.Host = "evil.example.test"
	wrongHostResponse, err := http.DefaultClient.Do(wrongHostRequest)
	if err != nil {
		t.Fatal(err)
	}
	wrongHostBody, _ := io.ReadAll(wrongHostResponse.Body)
	wrongHostResponse.Body.Close()
	if wrongHostResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected Host rejection, got %d %s", wrongHostResponse.StatusCode, wrongHostBody)
	}
	assertCodexOAuthHTMLSecurity(t, wrongHostResponse)

	missingPortURL := *redirect
	missingPortURL.Host = "127.0.0.1:" + redirect.Port()
	missingPortURL.RawQuery = url.Values{"code": {"missing-port-code"}, "state": {authURL.Query().Get("state")}}.Encode()
	missingPortRequest, _ := http.NewRequest(http.MethodGet, missingPortURL.String(), nil)
	missingPortRequest.Host = "localhost"
	missingPortResponse, err := http.DefaultClient.Do(missingPortRequest)
	if err != nil {
		t.Fatal(err)
	}
	missingPortBody, _ := io.ReadAll(missingPortResponse.Body)
	missingPortResponse.Body.Close()
	if missingPortResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected missing Host port rejection, got %d %s", missingPortResponse.StatusCode, missingPortBody)
	}
	assertCodexOAuthHTMLSecurity(t, missingPortResponse)

	wrongState := *redirect
	wrongState.RawQuery = url.Values{"code": {"state-code"}, "state": {"wrong-state-fixture"}}.Encode()
	wrongStateResponse, err := http.Get(wrongState.String())
	if err != nil {
		t.Fatal(err)
	}
	wrongStateBody, _ := io.ReadAll(wrongStateResponse.Body)
	wrongStateResponse.Body.Close()
	if wrongStateResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected state rejection, got %d %s", wrongStateResponse.StatusCode, wrongStateBody)
	}
	assertNoCodexOAuthSecrets(t, wrongStateBody, "host-code", "state-code", "wrong-state-fixture")
	if tokenRequests.Load() != 0 {
		t.Fatalf("invalid callback reached token endpoint %d times", tokenRequests.Load())
	}
	status := getCodexOAuthLoginForTest(t, app, started.LoginID)
	if status.Status != codexOAuthLoginPending {
		t.Fatalf("invalid callback consumed login session: %+v", status)
	}
	cancelCodexOAuthLoginForTest(t, app, started.LoginID)

	escaped := startCodexOAuthLoginForTest(t, app)
	escapedAuth, _ := url.Parse(escaped.AuthURL)
	escapedCallback, _ := url.Parse(escapedAuth.Query().Get("redirect_uri"))
	escapedCallback.RawQuery = url.Values{"error": {`<script>alert("token")</script>`}, "state": {escapedAuth.Query().Get("state")}}.Encode()
	escapedResponse, err := http.Get(escapedCallback.String())
	if err != nil {
		t.Fatal(err)
	}
	escapedBody, _ := io.ReadAll(escapedResponse.Body)
	escapedResponse.Body.Close()
	if escapedResponse.StatusCode != http.StatusBadRequest || strings.Contains(string(escapedBody), "<script>") || strings.Contains(string(escapedBody), "alert(") {
		t.Fatalf("OAuth error HTML was not safely escaped: %d %s", escapedResponse.StatusCode, escapedBody)
	}
	assertCodexOAuthHTMLSecurity(t, escapedResponse)
	if tokenRequests.Load() != 0 {
		t.Fatalf("OAuth authorization error reached token endpoint %d times", tokenRequests.Load())
	}
	assertCodexOAuthSessionSecretsCleared(t, app, escaped.LoginID)
}

func TestCodexOAuthTokenFailureIsSafeAndOneTime(t *testing.T) {
	const (
		code          = "failed-code-fixture"
		upstreamToken = "upstream-token-fixture"
	)
	var tokenRequests atomic.Int32
	var verifierMu sync.Mutex
	var receivedVerifier string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenRequests.Add(1)
		_ = r.ParseForm()
		verifierMu.Lock()
		receivedVerifier = r.Form.Get("code_verifier")
		verifierMu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"code=%s token=%s verifier=%s"}}`, code, upstreamToken, r.Form.Get("code_verifier"))
	}))
	defer issuer.Close()
	app := newCodexOAuthLoginTestServer(t, issuer, time.Minute)
	started := startCodexOAuthLoginForTest(t, app)
	authURL, _ := url.Parse(started.AuthURL)
	callback, _ := url.Parse(authURL.Query().Get("redirect_uri"))
	callback.RawQuery = url.Values{"code": {code}, "state": {authURL.Query().Get("state")}}.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected safe token failure, got %d %s", response.StatusCode, body)
	}
	verifierMu.Lock()
	verifier := receivedVerifier
	verifierMu.Unlock()
	assertNoCodexOAuthSecrets(t, body, code, upstreamToken, verifier)
	status := getCodexOAuthLoginForTest(t, app, started.LoginID)
	if status.Status != codexOAuthLoginFailed || status.AuthURL != "" || status.Error == "" {
		t.Fatalf("unexpected failed status: %+v", status)
	}
	statusJSON, _ := json.Marshal(status)
	assertNoCodexOAuthSecrets(t, statusJSON, code, upstreamToken, verifier)
	assertCodexOAuthSessionSecretsCleared(t, app, started.LoginID)

	second, err := http.Get(callback.String())
	if err == nil {
		second.Body.Close()
	}
	if tokenRequests.Load() != 1 {
		t.Fatalf("callback was exchanged more than once: %d", tokenRequests.Load())
	}
}

func TestCodexOAuthLoginCancellationAndTimeout(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"unused-token"}`))
	}))
	defer issuer.Close()
	app := newCodexOAuthLoginTestServer(t, issuer, 40*time.Millisecond)

	cancelled := startCodexOAuthLoginForTest(t, app)
	cancelStatus := cancelCodexOAuthLoginForTest(t, app, cancelled.LoginID)
	if cancelStatus.Status != codexOAuthLoginCancelled || cancelStatus.AuthURL != "" {
		t.Fatalf("unexpected cancellation response: %+v", cancelStatus)
	}
	if status := getCodexOAuthLoginForTest(t, app, cancelled.LoginID); status.Status != codexOAuthLoginCancelled {
		t.Fatalf("cancelled login status changed: %+v", status)
	}
	assertCodexOAuthSessionSecretsCleared(t, app, cancelled.LoginID)

	expiring := startCodexOAuthLoginForTest(t, app)
	deadline := time.Now().Add(time.Second)
	for {
		status := getCodexOAuthLoginForTest(t, app, expiring.LoginID)
		if status.Status == codexOAuthLoginExpired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("login did not expire: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertCodexOAuthSessionSecretsCleared(t, app, expiring.LoginID)
}

func TestListenCodexOAuthCallbackSkipsIPv6PortCollision(t *testing.T) {
	occupiedIPv6, err := net.Listen("tcp6", net.JoinHostPort("::1", "0"))
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer occupiedIPv6.Close()
	occupiedPort, err := listenerPort(occupiedIPv6)
	if err != nil {
		t.Fatal(err)
	}
	occupiedAddress := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", occupiedPort))
	ipv4Probe, err := net.Listen("tcp4", occupiedAddress)
	if err != nil {
		t.Skipf("IPv6 fixture also reserved the IPv4 port: %v", err)
	}
	_ = ipv4Probe.Close()

	listeners, selectedPort, err := listenCodexOAuthCallback([]string{occupiedAddress, net.JoinHostPort("127.0.0.1", "0")})
	if err != nil {
		t.Fatal(err)
	}
	defer closeCodexOAuthCallbackListeners(listeners)
	if selectedPort == occupiedPort {
		t.Fatalf("callback reused port %d despite an IPv6 listener owning localhost", occupiedPort)
	}
	if len(listeners) != 2 {
		t.Fatalf("expected IPv4 and IPv6 callback listeners, got %d", len(listeners))
	}
	families := map[string]bool{}
	for _, listener := range listeners {
		address, ok := listener.Addr().(*net.TCPAddr)
		if !ok || address.Port != selectedPort {
			t.Fatalf("callback listener did not share selected port %d: %v", selectedPort, listener.Addr())
		}
		if address.IP.To4() != nil {
			families["ipv4"] = true
		} else {
			families["ipv6"] = true
		}
	}
	if !families["ipv4"] || !families["ipv6"] {
		t.Fatalf("callback listeners did not cover both loopback families: %v", families)
	}
}

func TestCodexOAuthCallbackAcceptsIPv6LocalhostConnection(t *testing.T) {
	if !codexOAuthIPv6LoopbackAvailable() {
		t.Skip("IPv6 loopback is unavailable")
	}
	issuer := httptest.NewServer(http.NotFoundHandler())
	defer issuer.Close()
	app := newCodexOAuthLoginTestServer(t, issuer, time.Minute)
	started := startCodexOAuthLoginForTest(t, app)
	authURL, err := url.Parse(started.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	callback.Host = net.JoinHostPort("::1", callback.Port())
	callback.RawQuery = url.Values{"code": {"unused-code"}, "state": {"wrong-state"}}.Encode()
	request, err := http.NewRequest(http.MethodGet, callback.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = net.JoinHostPort("localhost", callback.Port())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "OAuth state") {
		t.Fatalf("IPv6 callback did not reach the Autoto handler: %d %s", response.StatusCode, body)
	}
	assertCodexOAuthHTMLSecurity(t, response)
	if status := getCodexOAuthLoginForTest(t, app, started.LoginID); status.Status != codexOAuthLoginPending {
		t.Fatalf("invalid IPv6 callback consumed the login session: %+v", status)
	}
	cancelCodexOAuthLoginForTest(t, app, started.LoginID)
}

func TestCodexOAuthLoginRoutesRejectRemoteAccess(t *testing.T) {
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
		target string
	}{
		{http.MethodPost, "/api/providers/oauth/codex/login/start"},
		{http.MethodGet, "/api/providers/oauth/codex/login/fixture"},
		{http.MethodDelete, "/api/providers/oauth/codex/login/fixture"},
	} {
		request := newTestRequest(test.method, test.target, nil)
		request.Host = "remote.example.test"
		markRemoteHTTPS(request)
		request.AddCookie(&http.Cookie{Name: remoteAccessCookieName, Value: token})
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("remote %s %s was not rejected: %d %s", test.method, test.target, recorder.Code, recorder.Body.String())
		}
	}
}

func TestCodexOAuthLoginStartRouteRequiresCanonicalTokenAndSameOrigin(t *testing.T) {
	issuer := httptest.NewServer(http.NotFoundHandler())
	defer issuer.Close()
	app := newCodexOAuthLoginTestServer(t, issuer, time.Minute)

	missing := httptest.NewRecorder()
	app.Routes().ServeHTTP(missing, newTestRequest(http.MethodPost, "/api/providers/oauth/codex/login/start", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing canonical token was accepted: %d %s", missing.Code, missing.Body.String())
	}
	wrongRequest := newTestRequest(http.MethodPost, "/api/providers/oauth/codex/login/start", nil)
	wrongRequest.Header.Set(localTokenHeader, "wrong-token")
	wrong := httptest.NewRecorder()
	app.Routes().ServeHTTP(wrong, wrongRequest)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong canonical token was accepted: %d %s", wrong.Code, wrong.Body.String())
	}
	legacyRequest := newTestRequest(http.MethodPost, "/api/providers/oauth/codex/login/start", nil)
	legacyRequest.Header.Set(nonCanonicalLocalTokenHeader, app.localToken)
	legacy := httptest.NewRecorder()
	app.Routes().ServeHTTP(legacy, legacyRequest)
	if legacy.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token header was accepted: %d %s", legacy.Code, legacy.Body.String())
	}
	crossSiteRequest := authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/login/start", nil)
	crossSiteRequest.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite := httptest.NewRecorder()
	app.Routes().ServeHTTP(crossSite, crossSiteRequest)
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site login start was accepted: %d %s", crossSite.Code, crossSite.Body.String())
	}

	valid := startCodexOAuthLoginForTest(t, app)
	cancelCodexOAuthLoginForTest(t, app, valid.LoginID)
}

func newCodexOAuthLoginTestServer(t *testing.T, issuer *httptest.Server, ttl time.Duration) *Server {
	t.Helper()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: t.TempDir()}}, nil, nil, nil, providers.NewRegistry())
	app.codexOAuthTestConfig = &codexOAuthLoginTestConfig{
		Issuer: issuer.URL, ClientID: "fixture-client", ListenAddress: "127.0.0.1:0", HTTPClient: issuer.Client(), SessionTTL: ttl,
	}
	return app
}

func startCodexOAuthLoginForTest(t *testing.T, app *Server) codexOAuthLoginResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, authenticatedCodexRequest(app, http.MethodPost, "/api/providers/oauth/codex/login/start", bytes.NewReader(nil)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("OAuth login start failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var response codexOAuthLoginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func getCodexOAuthLoginForTest(t *testing.T, app *Server, loginID string) codexOAuthLoginResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, authenticatedCodexRequest(app, http.MethodGet, "/api/providers/oauth/codex/login/"+loginID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("OAuth login status failed: %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "authUrl") || strings.Contains(recorder.Body.String(), "code_verifier") || strings.Contains(recorder.Body.String(), "access_token") {
		t.Fatalf("OAuth login GET leaked secret-bearing fields: %s", recorder.Body.String())
	}
	var response codexOAuthLoginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func cancelCodexOAuthLoginForTest(t *testing.T, app *Server, loginID string) codexOAuthLoginResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, authenticatedCodexRequest(app, http.MethodDelete, "/api/providers/oauth/codex/login/"+loginID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("OAuth login cancellation failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var response codexOAuthLoginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func assertCodexOAuthSessionSecretsCleared(t *testing.T, app *Server, loginID string) {
	t.Helper()
	app.codexOAuthMu.Lock()
	defer app.codexOAuthMu.Unlock()
	session := app.codexOAuthLogin
	if session == nil || session.loginID != loginID {
		t.Fatal("Codex OAuth terminal session was not retained for status lookup")
	}
	if session.status == codexOAuthLoginPending || session.status == codexOAuthLoginExchanging {
		t.Fatalf("Codex OAuth session is not terminal: %s", session.status)
	}
	if session.state != "" || session.verifier != "" || session.authURL != "" || session.redirectURI != "" {
		t.Fatal("Codex OAuth terminal session retained one-time secret material")
	}
}

func assertCodexOAuthHTMLSecurity(t *testing.T, response *http.Response) {
	t.Helper()
	if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") || response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Content-Security-Policy") != codexOAuthCallbackCSP {
		t.Fatalf("OAuth callback security headers missing: %v", response.Header)
	}
}

func assertNoCodexOAuthSecrets(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(body), secret) {
			t.Fatalf("OAuth response leaked secret %q: %s", secret, body)
		}
	}
}

func testCodexOAuthJWT(accountID, email, plan string, expiresAt int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims, _ := json.Marshal(map[string]any{
		"exp":                            expiresAt,
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": accountID, "chatgpt_plan_type": plan},
		"https://api.openai.com/profile": map[string]any{"email": email},
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}
