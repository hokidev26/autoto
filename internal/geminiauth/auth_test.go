package geminiauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

func localEndpoints(base string) clientEndpoints {
	return clientEndpoints{
		auth:      base + "/authorize",
		token:     base + "/token",
		userInfo:  base + "/userinfo",
		cloudCode: base,
		daily:     base,
	}
}

func TestConstantsStateAndAuthURL(t *testing.T) {
	if ProviderName != "gemini" || ClientID != "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com" || ClientSecret != "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" {
		t.Fatal("Gemini OAuth constants changed unexpectedly")
	}
	if CloudCodeAPIEndpoint != "https://cloudcode-pa.googleapis.com" || CloudCodeDailyAPIEndpoint != "https://daily-cloudcode-pa.googleapis.com" || CloudCodeAPIVersion != "v1internal" {
		t.Fatal("Gemini Cloud Code constants changed unexpectedly")
	}
	if RefreshLead() != 5*time.Minute || len(Scopes) != 5 {
		t.Fatalf("unexpected refresh lead/scopes: %v %#v", RefreshLead(), Scopes)
	}
	first, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 40 || first == second || strings.Contains(first, "=") {
		t.Fatalf("unexpected OAuth states: %q %q", first, second)
	}

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), time.Millisecond)
	redirect := "http://127.0.0.1:16888/api/providers/gemini/oauth/callback"
	authURL, err := client.BuildAuthURL(first, redirect)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Path != "/authorize" || query.Get("client_id") != ClientID || query.Get("redirect_uri") != redirect || query.Get("state") != first || query.Get("response_type") != "code" || query.Get("access_type") != "offline" || query.Get("prompt") != "consent" {
		t.Fatalf("unexpected auth URL: %s", authURL)
	}
	for _, scope := range Scopes {
		if !strings.Contains(query.Get("scope"), scope) {
			t.Fatalf("auth URL missing scope %q: %s", scope, query.Get("scope"))
		}
	}
	for _, unsafeRedirect := range []string{"https://example.com/callback", "http://example.com/callback", "javascript:alert(1)", "http://user@127.0.0.1/callback", "http://127.0.0.1/callback#fragment"} {
		if _, err := client.BuildAuthURL(first, unsafeRedirect); err == nil {
			t.Fatalf("unsafe redirect %q was accepted", unsafeRedirect)
		}
	}
}

func TestExchangeCodeAndRefreshTokens(t *testing.T) {
	refreshGroup = singleflight.Group{}
	t.Cleanup(func() { refreshGroup = singleflight.Group{} })
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != ClientSecret {
			t.Fatalf("missing Google client credentials: %v", r.PostForm)
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			if r.PostForm.Get("code") != "code-1" || r.PostForm.Get("redirect_uri") != "http://localhost:16888/callback" {
				t.Fatalf("unexpected exchange form: %v", r.PostForm)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "id_token": "id-1", "token_type": "Bearer", "expires_in": 3600})
		case "refresh_token":
			refreshCalls.Add(1)
			if r.PostForm.Get("refresh_token") != "refresh-1" {
				t.Fatalf("unexpected refresh form: %v", r.PostForm)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "token_type": "Bearer", "expires_in": 1800})
		default:
			t.Fatalf("unexpected grant type: %v", r.PostForm)
		}
	}))
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), time.Millisecond)
	tokens, err := client.ExchangeCode(context.Background(), "code-1", "http://localhost:16888/callback")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access-1" || tokens.RefreshToken != "refresh-1" || tokens.IDToken != "id-1" || tokens.ExpiresAt == "" {
		t.Fatalf("unexpected exchange tokens: %+v", tokens)
	}
	refreshed, err := client.RefreshTokens(context.Background(), "refresh-1")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "access-2" || refreshed.RefreshToken != "" || refreshed.ExpiresAt == "" {
		t.Fatalf("refresh response should leave omitted refresh token empty: %+v", refreshed)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls.Load())
	}
}

func TestRefreshTokensSingleflight(t *testing.T) {
	refreshGroup = singleflight.Group{}
	t.Cleanup(func() { refreshGroup = singleflight.Group{} })
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "expires_in": 3600})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), time.Millisecond)
	results := make(chan error, 2)
	go func() { _, err := client.RefreshTokens(context.Background(), "shared-refresh"); results <- err }()
	<-started
	go func() { _, err := client.RefreshTokens(context.Background(), "shared-refresh"); results <- err }()
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("singleflight calls before release = %d, want 1", calls.Load())
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("singleflight calls = %d, want 1", calls.Load())
	}
}

func TestFetchUserInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/userinfo" || r.Header.Get("Authorization") != "Bearer access-secret" || r.Header.Get("User-Agent") != RequestUserAgent {
			t.Fatalf("unexpected userinfo request: path=%s headers=%v", r.URL.Path, r.Header)
		}
		_ = json.NewEncoder(w).Encode(UserInfo{Subject: "subject-1", Email: " user@example.test ", VerifiedEmail: true, Name: " User "})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), time.Millisecond)
	info, err := client.FetchUserInfo(context.Background(), "access-secret")
	if err != nil {
		t.Fatal(err)
	}
	if info.Subject != "subject-1" || info.Email != "user@example.test" || info.Name != "User" || !info.VerifiedEmail {
		t.Fatalf("unexpected userinfo: %+v", info)
	}
}

func TestFetchProjectIDFromLoadCodeAssist(t *testing.T) {
	var dailyCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-project" || r.Header.Get("User-Agent") != RequestUserAgent {
			t.Fatalf("unexpected Cloud Code headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			metadata, _ := body["metadata"].(map[string]any)
			if metadata["ideType"] != "ANTIGRAVITY" {
				t.Fatalf("unexpected load metadata: %v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": map[string]any{"id": "project-existing"}})
		case "/v1internal:onboardUser":
			dailyCalls.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), time.Millisecond)
	projectID, err := client.FetchProjectID(context.Background(), "access-project")
	if err != nil {
		t.Fatal(err)
	}
	if projectID != "project-existing" || dailyCalls.Load() != 0 {
		t.Fatalf("unexpected project resolution: id=%q onboard=%d", projectID, dailyCalls.Load())
	}
}

func TestFetchProjectIDOnboardsWithDefaultTierAndPolls(t *testing.T) {
	var onboardCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_ = json.NewEncoder(w).Encode(map[string]any{"allowedTiers": []any{map[string]any{"id": "paid-tier", "isDefault": true}}})
		case "/v1internal:onboardUser":
			if r.Header.Get("Authorization") != "Bearer access-onboard" || r.Header.Get("User-Agent") != OnboardUserAgent || r.Header.Get("X-Goog-Api-Client") != OnboardGoogAPIClientUA {
				t.Fatalf("unexpected onboard headers: %v", r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["tier_id"] != "paid-tier" {
				t.Fatalf("unexpected onboard tier: %v", body)
			}
			metadata, _ := body["metadata"].(map[string]any)
			if metadata["ide_type"] != "ANTIGRAVITY" || metadata["ide_version"] != "2.2.1" || metadata["ide_name"] != "antigravity" {
				t.Fatalf("unexpected onboard metadata: %v", body)
			}
			if onboardCalls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"done": false})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "response": map[string]any{"projectId": "project-onboarded"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), time.Millisecond)
	projectID, err := client.FetchProjectID(context.Background(), "access-onboard")
	if err != nil {
		t.Fatal(err)
	}
	if projectID != "project-onboarded" || onboardCalls.Load() != 2 {
		t.Fatalf("unexpected onboard result: id=%q calls=%d", projectID, onboardCalls.Load())
	}
}

func TestFetchProjectIDRespectsContextCancellation(t *testing.T) {
	firstOnboard := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_ = json.NewEncoder(w).Encode(map[string]any{"allowedTiers": []any{}})
		case "/v1internal:onboardUser":
			once.Do(func() { close(firstOnboard) })
			_ = json.NewEncoder(w).Encode(map[string]any{"done": false})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), 250*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.FetchProjectID(ctx, "access-cancel")
		result <- err
	}()
	<-firstOnboard
	cancel()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FetchProjectID did not stop after cancellation")
	}
}

func TestOAuthErrorsDoNotLeakSecretsAndRedirectsAreNotFollowed(t *testing.T) {
	secretCode := "authorization-code-secret"
	secretRefresh := "refresh-token-secret"
	var redirected atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", sink.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = w.Write([]byte(`{"error":"server_error","code":"` + secretCode + `","refresh_token":"` + secretRefresh + `"}`))
	}))
	defer server.Close()
	client := newTestClient(server.Client(), localEndpoints(server.URL), time.Millisecond)
	_, err := client.ExchangeCode(context.Background(), secretCode, "http://localhost:16888/callback")
	if err == nil {
		t.Fatal("expected exchange failure")
	}
	for _, secret := range []string{secretCode, secretRefresh, ClientSecret} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("OAuth error leaked secret %q: %v", secret, err)
		}
	}
	if redirected.Load() {
		t.Fatal("OAuth POST redirect was followed")
	}
}

func TestProductionEndpointAllowlist(t *testing.T) {
	client := NewClient(nil)
	client.endpoints.token = "https://example.com/oauth/token"
	_, err := client.ExchangeCode(context.Background(), "code", "http://localhost:16888/callback")
	if err == nil || !strings.Contains(err.Error(), "不受信任") {
		t.Fatalf("untrusted token endpoint error = %v", err)
	}
	client.endpoints.token = "http://127.0.0.1/token"
	_, err = client.ExchangeCode(context.Background(), "code", "http://localhost:16888/callback")
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure production token endpoint error = %v", err)
	}
}

func TestParseProjectInfoAndTierFallback(t *testing.T) {
	info := parseProjectInfo(map[string]any{
		"project": map[string]any{"projectId": "project-safe"},
		"allowedTiers": []any{
			map[string]any{"id": "tier-a", "name": "A"},
			map[string]any{"id": "tier-b", "name": "B", "isDefault": true},
		},
		"currentTier": map[string]any{"id": "tier-a"},
		"credits":     map[string]any{"remaining": 12.5, "limit": 20.0, "resetAt": "2026-08-01T00:00:00Z"},
	})
	if info.ProjectID != "project-safe" || len(info.AllowedTiers) != 2 || info.CurrentTier == nil || info.Credits == nil || defaultTierID(info) != "tier-b" {
		t.Fatalf("unexpected parsed project info: %+v", info)
	}
	if defaultTierID(ProjectInfo{}) != "free-tier" {
		t.Fatal("missing tier should fall back to free-tier")
	}
}
