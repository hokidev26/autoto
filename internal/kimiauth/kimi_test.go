package kimiauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

func TestNewClientGeneratesDeviceIDWithoutReadingFiles(t *testing.T) {
	clientA := NewClient(nil, "1.2.3", "")
	clientB := NewClient(nil, "1.2.3", "")
	if _, err := uuid.Parse(clientA.DeviceID()); err != nil {
		t.Fatalf("generated device ID is not a UUID: %q", clientA.DeviceID())
	}
	if clientA.DeviceID() == clientB.DeviceID() {
		t.Fatal("separate clients unexpectedly reused a generated device ID")
	}
	provided := NewClient(nil, "1.2.3", " device-fixed ")
	if provided.DeviceID() != "device-fixed" {
		t.Fatalf("provided device ID = %q", provided.DeviceID())
	}
}

func TestStartDeviceFlowSendsRequiredHeadersAndReturnsDeviceID(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("client_id") != ClientID {
			t.Fatalf("client_id = %q", r.PostForm.Get("client_id"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "device-code", "user_code": "ABCD-EFGH",
			"verification_uri_complete": "https://auth.kimi.com/device?code=ABCD-EFGH",
			"expires_in":                600, "interval": 5,
		})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), "9.8.7", "device-fixed", server.URL, server.URL, time.Millisecond, 2*time.Millisecond)
	device, err := client.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceID != "device-fixed" || device.DeviceCode != "device-code" {
		t.Fatalf("unexpected device response: %+v", device)
	}
	want := map[string]string{
		"X-Msh-Platform":  "Autoto",
		"X-Msh-Version":   "9.8.7",
		"X-Msh-Device-Id": "device-fixed",
	}
	for header, value := range want {
		if gotHeaders.Get(header) != value {
			t.Fatalf("%s = %q, want %q", header, gotHeaders.Get(header), value)
		}
	}
	if gotHeaders.Get("X-Msh-Device-Name") == "" || gotHeaders.Get("X-Msh-Device-Model") == "" {
		t.Fatalf("missing device headers: %v", gotHeaders)
	}
}

func TestWaitHandlesPendingSlowDownAndReturnsDeviceID(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("X-Msh-Device-Id") != "device-1" || r.Header.Get("X-Msh-Version") != "1.0.0" {
			t.Fatalf("missing token request headers: %v", r.Header)
		}
		if r.PostForm.Get("grant_type") != DeviceCodeGrantType || r.PostForm.Get("device_code") != "code-1" {
			t.Fatalf("unexpected token form: %v", r.PostForm)
		}
		switch polls.Add(1) {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access", "refresh_token": "refresh", "token_type": "Bearer", "scope": "openid", "expires_in": 3600,
			})
		}
	}))
	defer server.Close()
	client := newTestClient(server.Client(), "1.0.0", "device-1", server.URL, server.URL, time.Millisecond, 2*time.Millisecond)
	if NewClient(nil, "1", "device").slowDownStep != 5*time.Second {
		t.Fatal("production slow_down increment must be five seconds")
	}
	bundle, err := client.Wait(context.Background(), &DeviceCodeResponse{DeviceCode: "code-1", ExpiresIn: 5, startedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 3 || bundle.DeviceID != "device-1" || bundle.TokenData.DeviceID != "device-1" || bundle.TokenData.AccessToken != "access" {
		t.Fatalf("unexpected authorization result: polls=%d bundle=%+v", polls.Load(), bundle)
	}
}

func TestPollDenialExpiryAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), "1", "device", server.URL, server.URL, time.Millisecond, time.Millisecond)
	_, err := client.Poll(context.Background(), &DeviceCodeResponse{DeviceCode: "code"})
	if err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("denial error = %v", err)
	}
	_, err = client.Wait(context.Background(), &DeviceCodeResponse{DeviceCode: "code", ExpiresIn: 1, startedAt: time.Now().Add(-2 * time.Second)})
	if err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("expiry error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Wait(ctx, &DeviceCodeResponse{DeviceCode: "code", ExpiresIn: 60})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestRefreshTokenRotatesAndPreservesDeviceID(t *testing.T) {
	refreshGroup = singleflight.Group{}
	t.Cleanup(func() { refreshGroup = singleflight.Group{} })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("client_id") != ClientID || r.Header.Get("X-Msh-Device-Id") != "device-refresh" {
			t.Fatalf("unexpected refresh request: form=%v headers=%v", r.PostForm, r.Header)
		}
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-2", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "expires_in": 3600})
	}))
	defer server.Close()
	client := newTestClient(server.Client(), "1", "device-refresh", server.URL, server.URL, time.Millisecond, time.Millisecond)
	first, err := client.RefreshToken(context.Background(), "refresh-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.RefreshToken != "refresh-2" || first.DeviceID != "device-refresh" {
		t.Fatalf("refresh rotation failed: %+v", first)
	}
	second, err := client.RefreshToken(context.Background(), "refresh-2")
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken != "refresh-2" || second.DeviceID != "device-refresh" {
		t.Fatalf("refresh preservation failed: %+v", second)
	}
}

func TestRefreshTokenSingleflight(t *testing.T) {
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
	client := newTestClient(server.Client(), "1", "device", server.URL, server.URL, time.Millisecond, time.Millisecond)
	results := make(chan error, 2)
	go func() { _, err := client.RefreshToken(context.Background(), "shared"); results <- err }()
	<-started
	go func() { _, err := client.RefreshToken(context.Background(), "shared"); results <- err }()
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

func TestRefreshErrorIsLimitedAndDoesNotLeakToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error","refresh_token":"secret-refresh"}`))
	}))
	defer server.Close()
	client := newTestClient(server.Client(), "1", "device", server.URL, server.URL, time.Millisecond, time.Millisecond)
	_, err := client.RefreshToken(context.Background(), "secret-refresh")
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if strings.Contains(err.Error(), "secret-refresh") {
		t.Fatalf("error leaked refresh token: %v", err)
	}
}

func TestConstants(t *testing.T) {
	if ClientID != "17e5f671-d194-4dfb-9706-5516cb48c098" || AuthHost != "https://auth.kimi.com" || TokenEndpoint != AuthHost+"/api/oauth/token" || APIBaseURL != "https://api.kimi.com/coding" {
		t.Fatal("Kimi constants changed unexpectedly")
	}
	if RefreshLead() != 5*time.Minute {
		t.Fatalf("RefreshLead() = %v", RefreshLead())
	}
}
