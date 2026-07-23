package anthropicauth

import (
	"net/url"
	"strings"
	"testing"
)

func TestNewPKCEProducesValidChallenge(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	if len(pkce.Verifier) < 43 || len(pkce.Verifier) > 128 {
		t.Fatalf("verifier length invalid: %d", len(pkce.Verifier))
	}
	challenge, err := PKCEChallenge(pkce.Verifier)
	if err != nil {
		t.Fatalf("PKCEChallenge: %v", err)
	}
	if challenge != pkce.Challenge {
		t.Fatalf("challenge mismatch: %q vs %q", challenge, pkce.Challenge)
	}
}

func TestBuildAuthorizeURLIncludesRequiredParams(t *testing.T) {
	raw, err := BuildAuthorizeURL("state-123", "challenge-abc")
	if err != nil {
		t.Fatalf("BuildAuthorizeURL: %v", err)
	}
	if !strings.HasPrefix(raw, OAuthAuthorizeEndpoint+"?") {
		t.Fatalf("unexpected authorize endpoint: %s", raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	query := parsed.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             OAuthClientID,
		"redirect_uri":          OAuthRedirectURI,
		"scope":                 OAuthScope,
		"code_challenge":        "challenge-abc",
		"code_challenge_method": "S256",
		"state":                 "state-123",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("authorize query %q = %q, want %q", key, got, want)
		}
	}
}

func TestBuildAuthorizeURLRejectsEmptyInputs(t *testing.T) {
	if _, err := BuildAuthorizeURL("", "challenge"); err == nil {
		t.Fatal("expected error for empty state")
	}
	if _, err := BuildAuthorizeURL("state", ""); err == nil {
		t.Fatal("expected error for empty challenge")
	}
}

func TestParseAuthorizationCodeSplitsState(t *testing.T) {
	cases := []struct {
		raw   string
		code  string
		state string
	}{
		{"abc#xyz", "abc", "xyz"},
		{"  abc#xyz  ", "abc", "xyz"},
		{"onlycode", "onlycode", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		code, state := ParseAuthorizationCode(tc.raw)
		if code != tc.code || state != tc.state {
			t.Fatalf("ParseAuthorizationCode(%q) = (%q,%q), want (%q,%q)", tc.raw, code, state, tc.code, tc.state)
		}
	}
}

func TestOAuthCredentialRoundTripsThroughStore(t *testing.T) {
	store := NewStore(t.TempDir())
	item, err := store.Create(CreateRequest{
		AuthType:       AuthTypeOAuth,
		OAuthAccess:    "access-token",
		OAuthRefresh:   "refresh-token",
		OAuthExpiresAt: "2099-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("create oauth credential: %v", err)
	}
	if item.Credential.AuthType != AuthTypeOAuth || item.Credential.OAuthAccess != "access-token" {
		t.Fatalf("unexpected stored credential: %+v", item.Credential)
	}
	// Summary must never leak the tokens.
	summary := Summary(item)
	if strings.Contains(summary.Source, "access-token") || summary.Profile != "" {
		t.Fatalf("summary leaked material: %+v", summary)
	}
	// Refresh persistence.
	updated, err := store.UpdateOAuthTokens(item.Credential.ID, "access-2", "refresh-2", "2099-02-02T00:00:00Z")
	if err != nil {
		t.Fatalf("update oauth tokens: %v", err)
	}
	if updated.Credential.OAuthAccess != "access-2" || updated.Credential.OAuthRefresh != "refresh-2" {
		t.Fatalf("tokens not updated: %+v", updated.Credential)
	}
}
