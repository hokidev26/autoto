package kiroauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestValidateRegion(t *testing.T) {
	valid := []string{"us-east-1", "us-west-2", "eu-central-1", "ap-northeast-1", "ap-southeast-1"}
	for _, r := range valid {
		if err := ValidateRegion(r); err != nil {
			t.Errorf("ValidateRegion(%q) unexpected error: %v", r, err)
		}
	}
	invalid := []string{"", "us-east-2", "eu-west-1", "ap-south-1", "bad-region"}
	for _, r := range invalid {
		if err := ValidateRegion(r); err == nil {
			t.Errorf("ValidateRegion(%q) expected error, got nil", r)
		}
	}
}

func TestRegionFromProfileArn(t *testing.T) {
	cases := []struct {
		arn  string
		want string
	}{
		{"arn:aws:codewhisperer:us-east-1:1234567890:profile/xxx", "us-east-1"},
		{"arn:aws:codewhisperer:us-west-2:1234567890:profile/xxx", "us-west-2"},
		{"arn:aws:codewhisperer:eu-central-1:1234567890:profile/xxx", "eu-central-1"},
		{"arn:aws:codewhisperer:ap-northeast-1:1234567890:profile/xxx", "ap-northeast-1"},
		{"arn:aws:codewhisperer:ap-southeast-1:1234567890:profile/xxx", "ap-southeast-1"},
		// Unknown region in ARN → default
		{"arn:aws:codewhisperer:eu-west-1:1234567890:profile/xxx", DefaultRegion},
		// Malformed ARN → default
		{"not-an-arn", DefaultRegion},
		{"", DefaultRegion},
		{"arn:aws:codewhisperer", DefaultRegion},
	}
	for _, tc := range cases {
		got := RegionFromProfileArn(tc.arn)
		if got != tc.want {
			t.Errorf("RegionFromProfileArn(%q) = %q, want %q", tc.arn, got, tc.want)
		}
	}
}

func TestRefreshEndpoint(t *testing.T) {
	got := RefreshEndpoint("us-east-1")
	want := "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
	if got != want {
		t.Errorf("RefreshEndpoint = %q, want %q", got, want)
	}
}

func TestRefreshTokenSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if body["refreshToken"] == "" {
			t.Error("missing refreshToken in request body")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken":  "new-access-token",
			"refreshToken": "new-refresh-token",
			"profileArn":   "arn:aws:codewhisperer:us-east-1:1234567890:profile/abc",
			"expiresIn":    3600,
		})
	}))
	defer server.Close()

	client := newTestClient(server.Client(), server.URL)
	td, err := client.RefreshToken(context.Background(), "old-refresh-token", "us-east-1")
	if err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if td.AccessToken != "new-access-token" {
		t.Errorf("AccessToken = %q, want %q", td.AccessToken, "new-access-token")
	}
	if td.RefreshToken != "new-refresh-token" {
		t.Errorf("RefreshToken = %q, want %q", td.RefreshToken, "new-refresh-token")
	}
	if td.ProfileArn != "arn:aws:codewhisperer:us-east-1:1234567890:profile/abc" {
		t.Errorf("ProfileArn = %q", td.ProfileArn)
	}
	if td.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", td.Region)
	}
	if td.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d, want 3600", td.ExpiresIn)
	}
	if td.ExpiresAt == "" {
		t.Error("ExpiresAt should be set")
	}
}

func TestRefreshTokenPreservesOldRefreshIfMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "new-access",
			// refreshToken intentionally omitted
		})
	}))
	defer server.Close()

	client := newTestClient(server.Client(), server.URL)
	td, err := client.RefreshToken(context.Background(), "original-refresh", "us-east-1")
	if err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if td.RefreshToken != "original-refresh" {
		t.Errorf("RefreshToken = %q, want %q", td.RefreshToken, "original-refresh")
	}
}

func TestRefreshTokenHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestClient(server.Client(), server.URL)
	_, err := client.RefreshToken(context.Background(), "some-token", "us-east-1")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

func TestRefreshTokenInvalidRegion(t *testing.T) {
	client := New(nil)
	_, err := client.RefreshToken(context.Background(), "some-token", "bad-region")
	if err == nil {
		t.Fatal("expected error for invalid region, got nil")
	}
}

func TestRefreshTokenEmptyToken(t *testing.T) {
	client := New(nil)
	_, err := client.RefreshToken(context.Background(), "", "us-east-1")
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

func TestRefreshTokenDeduplicatesConcurrent(t *testing.T) {
	var callCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken":  "dedup-access",
			"refreshToken": "dedup-refresh",
		})
	}))
	defer server.Close()

	client := newTestClient(server.Client(), server.URL)
	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = client.RefreshToken(context.Background(), "same-token", "us-east-1")
		}()
	}
	wg.Wait()
	// singleflight merges concurrent identical calls; may be 1 or a few
	if n := callCount.Load(); n > int32(goroutines) {
		t.Errorf("expected deduplication, got %d calls for %d goroutines", n, goroutines)
	}
}
