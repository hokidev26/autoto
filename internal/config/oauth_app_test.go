package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestOAuthAppConfigValidationAndScopes(t *testing.T) {
	cfg := OAuthAppConfig{
		Enabled:             true,
		IssuerURL:           "https://id.example.test/tenant/",
		ClientID:            "autoto-app",
		RedirectURL:         "https://app.example.test/app/auth/callback",
		AllowedEmailDomains: []string{"EXAMPLE.COM", "example.com"},
		SessionTTLHours:     12,
		AllowReadOnlyTasks:  true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	normalized := cfg.Normalized()
	if normalized.IssuerURL != "https://id.example.test/tenant/" || !reflect.DeepEqual(normalized.AllowedEmailDomains, []string{"example.com"}) {
		t.Fatalf("unexpected normalized config: %+v", normalized)
	}
	if want := []string{OAuthAppScopeProjectsRead, OAuthAppScopeAgentsRead, OAuthAppScopeMessagesRead, OAuthAppScopeAgentsRunReadOnly}; !reflect.DeepEqual(cfg.Scopes(), want) {
		t.Fatalf("unexpected app scopes: got=%v want=%v", cfg.Scopes(), want)
	}
}

func TestOAuthAppConfigRejectsUnsafeURLsAndEmailDomains(t *testing.T) {
	base := OAuthAppConfig{Enabled: true, IssuerURL: "https://id.example.test", ClientID: "client", RedirectURL: "https://app.example.test/app/auth/callback", SessionTTLHours: 8}
	for name, mutate := range map[string]func(*OAuthAppConfig){
		"issuer http":       func(cfg *OAuthAppConfig) { cfg.IssuerURL = "http://id.example.test" },
		"issuer query":      func(cfg *OAuthAppConfig) { cfg.IssuerURL = "https://id.example.test?secret=x" },
		"redirect fragment": func(cfg *OAuthAppConfig) { cfg.RedirectURL += "#fragment" },
		"redirect path":     func(cfg *OAuthAppConfig) { cfg.RedirectURL = "https://app.example.test/callback" },
		"userinfo":          func(cfg *OAuthAppConfig) { cfg.IssuerURL = "https://user@id.example.test" },
		"bad domain":        func(cfg *OAuthAppConfig) { cfg.AllowedEmailDomains = []string{"bad_domain"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
	loopback := base
	loopback.IssuerURL = "http://127.0.0.1:8080"
	loopback.RedirectURL = "http://localhost:7788/app/auth/callback"
	if err := loopback.Validate(); err != nil {
		t.Fatalf("loopback development URLs should be allowed: %v", err)
	}
}

func TestOAuthAppClientSecretUsesOnlyNamedEnvironmentVariable(t *testing.T) {
	const name = "AUTOTO_TEST_OIDC_CLIENT_SECRET"
	t.Setenv(name, "top-secret")
	cfg := OAuthAppConfig{ClientSecretEnv: name}
	secret, err := cfg.ClientSecret()
	if err != nil || secret != "top-secret" {
		t.Fatalf("unexpected secret resolution: secret=%q err=%v", secret, err)
	}
	if strings.Contains(strings.TrimSpace(strings.ReplaceAll(os.Getenv(name), secret, "")), secret) {
		t.Fatal("unexpected secret handling")
	}
	missing := OAuthAppConfig{ClientSecretEnv: "AUTOTO_TEST_OIDC_MISSING"}
	if _, err := missing.ClientSecret(); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("missing secret must fail without leaking values: %v", err)
	}
}

func TestOAuthAppAllowsOnlyVerifiedConfiguredEmailDomains(t *testing.T) {
	cfg := OAuthAppConfig{AllowedEmailDomains: []string{"example.com"}}
	if !cfg.AllowsEmail("person@EXAMPLE.com", true) {
		t.Fatal("expected verified allowed email")
	}
	for _, test := range []struct {
		email    string
		verified bool
	}{
		{email: "person@example.com", verified: false},
		{email: "person@other.test", verified: true},
		{email: "not-an-email", verified: true},
	} {
		if cfg.AllowsEmail(test.email, test.verified) {
			t.Fatalf("unexpectedly allowed %+v", test)
		}
	}
}
