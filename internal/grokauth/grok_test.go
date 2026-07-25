package grokauth

import (
	"context"
	"encoding/base64"
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

func TestValidateOAuthEndpointAllowlist(t *testing.T) {
	for _, endpoint := range []string{
		"https://auth.x.ai/oauth/token",
		"https://accounts.auth.x.ai/device",
		"https://x.ai/oauth/token",
	} {
		if _, err := ValidateOAuthEndpoint(endpoint); err != nil {
			t.Fatalf("ValidateOAuthEndpoint(%q) error = %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"http://auth.x.ai/oauth/token",
		"https://x.ai.evil.test/oauth/token",
		"https://evil.test/oauth/token",
		"https://user@auth.x.ai/oauth/token",
		"javascript:alert(1)",
	} {
		if _, err := ValidateOAuthEndpoint(endpoint); err == nil {
			t.Fatalf("ValidateOAuthEndpoint(%q) unexpectedly succeeded", endpoint)
		}
	}
}

func TestDiscoverAndStartDeviceFlow(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(Discovery{
				DeviceAuthorizationEndpoint: server.URL + "/device",
				TokenEndpoint:               server.URL + "/token",
			})
		case "/device":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("scope") != Scope {
				t.Fatalf("unexpected device form: %v", r.PostForm)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "device-1", "user_code": "ABCD-EFGH",
				"verification_uri": server.URL + "/verify", "expires_in": 600, "interval": 5,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(server.Client(), server.URL+"/.well-known/openid-configuration", time.Millisecond, 2*time.Millisecond)
	discovery, err := client.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if discovery.TokenEndpoint != server.URL+"/token" {
		t.Fatalf("unexpected token endpoint: %+v", discovery)
	}
	device, err := client.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceCode != "device-1" || device.UserCode != "ABCD-EFGH" || device.TokenEndpoint != server.URL+"/token" {
		t.Fatalf("unexpected device response: %+v", device)
	}
}

func TestDiscoverRejectsUntrustedReturnedEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Discovery{
			DeviceAuthorizationEndpoint: "https://auth.x.ai/oauth/device",
			TokenEndpoint:               "https://evil.example/oauth/token",
		})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), server.URL, time.Millisecond, time.Millisecond)
	if _, err := client.Discover(context.Background()); err == nil {
		t.Fatal("expected untrusted discovery endpoint rejection")
	}
}

func TestWaitHandlesPendingAndRepeatedSlowDown(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("grant_type") != DeviceCodeGrantType || r.PostForm.Get("device_code") != "device-1" || r.PostForm.Get("client_id") != ClientID {
			t.Fatalf("unexpected poll form: %v", r.PostForm)
		}
		switch polls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
		case 2, 3:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-new", "refresh_token": "refresh-new", "id_token": fakeJWT("user@x.ai", "sub-1"),
				"token_type": "Bearer", "expires_in": 3600,
			})
		}
	}))
	defer server.Close()
	client := newTestClient(server.Client(), server.URL, time.Millisecond, 2*time.Millisecond)
	if client.slowDownStep != 2*time.Millisecond {
		t.Fatalf("test slow-down step = %v", client.slowDownStep)
	}
	production := NewClient(nil)
	if production.slowDownStep != 5*time.Second {
		t.Fatalf("production slow-down step = %v, want 5s", production.slowDownStep)
	}
	tokens, err := client.Wait(context.Background(), &DeviceCodeResponse{
		DeviceCode: "device-1", TokenEndpoint: server.URL, ExpiresIn: 5, Interval: 0, startedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 4 || tokens.AccessToken != "access-new" || tokens.Email != "user@x.ai" || tokens.Subject != "sub-1" {
		t.Fatalf("unexpected polling result: polls=%d tokens=%+v", polls.Load(), tokens)
	}
}

func TestPollDenialExpiryAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), server.URL, time.Millisecond, time.Millisecond)
	_, err := client.Poll(context.Background(), &DeviceCodeResponse{DeviceCode: "device", TokenEndpoint: server.URL})
	if err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("denial error = %v", err)
	}
	_, err = client.Wait(context.Background(), &DeviceCodeResponse{DeviceCode: "device", TokenEndpoint: server.URL, ExpiresIn: 1, startedAt: time.Now().Add(-2 * time.Second)})
	if err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("expiry error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Wait(ctx, &DeviceCodeResponse{DeviceCode: "device", TokenEndpoint: server.URL, ExpiresIn: 60})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestRefreshTokensRotatesAndPreservesRefreshToken(t *testing.T) {
	refreshGroup = singleflight.Group{}
	t.Cleanup(func() { refreshGroup = singleflight.Group{} })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("client_id") != ClientID {
			t.Fatalf("unexpected refresh form: %v", r.PostForm)
		}
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-2", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "expires_in": 3600})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), server.URL, time.Millisecond, time.Millisecond)
	first, err := client.RefreshTokens(context.Background(), "refresh-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if first.RefreshToken != "refresh-2" {
		t.Fatalf("refresh token did not rotate: %+v", first)
	}
	second, err := client.RefreshTokens(context.Background(), "refresh-2", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken != "refresh-2" {
		t.Fatalf("omitted refresh token did not preserve old value: %+v", second)
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
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), server.URL, time.Millisecond, time.Millisecond)
	results := make(chan error, 2)
	go func() { _, err := client.RefreshTokens(context.Background(), "shared", server.URL); results <- err }()
	<-started
	go func() { _, err := client.RefreshTokens(context.Background(), "shared", server.URL); results <- err }()
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

func fakeJWT(email, subject string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]string{"email": email, "sub": subject})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestRefreshRequestDoesNotLeakTokenInErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error","refresh_token":"secret-refresh"}`))
	}))
	defer server.Close()
	client := newTestClient(server.Client(), server.URL, time.Millisecond, time.Millisecond)
	_, err := client.RefreshTokens(context.Background(), "secret-refresh", server.URL)
	if err == nil {
		t.Fatal("expected refresh failure")
	}
	if strings.Contains(err.Error(), "secret-refresh") {
		t.Fatalf("error leaked refresh token: %v", err)
	}
}

func TestConstants(t *testing.T) {
	if Issuer != "https://auth.x.ai" || DiscoveryURL != Issuer+"/.well-known/openid-configuration" || OfficialAPIBaseURL != "https://api.x.ai/v1" || CLIProxyBaseURL != "https://cli-chat-proxy.grok.com/v1" {
		t.Fatal("xAI endpoint constants changed unexpectedly")
	}
	if RefreshLead() != 5*time.Minute {
		t.Fatalf("RefreshLead() = %v", RefreshLead())
	}
	if _, err := url.Parse(DiscoveryURL); err != nil {
		t.Fatal(err)
	}
}
