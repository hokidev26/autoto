package providers

import (
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
)

// Every provider type that owns OAuth accounts must get a store path. The bug
// this guards against was silent: an empty path makes the account store report
// "not configured", so the provider simply disappears from the model picker
// instead of failing loudly.
func TestApplyCredentialStorePathCoversEveryAccountProvider(t *testing.T) {
	const home = "/home/user/.autoto"

	for _, test := range []struct {
		name         string
		providerName string
		providerType string
		wantPath     bool
	}{
		{name: "codex", providerName: "codex", providerType: config.ProviderTypeCodex, wantPath: true},
		{name: "grok", providerName: "grok", providerType: config.ProviderTypeGrok, wantPath: true},
		{name: "kimi", providerName: "kimi", providerType: config.ProviderTypeKimi, wantPath: true},
		// Antigravity is conventionally named gemini-oauth rather than gemini, so
		// the subscription case must key off the type, not the name.
		{name: "gemini as gemini-oauth", providerName: "gemini-oauth", providerType: config.ProviderTypeGemini, wantPath: true},
		{name: "anthropic", providerName: "anthropic", providerType: "anthropic", wantPath: true},
		// A renamed anthropic provider does not own the shared account store.
		{name: "renamed anthropic", providerName: "anthropic-alt", providerType: "anthropic", wantPath: false},
		// Plain API-key providers keep no OAuth accounts.
		{name: "openai", providerName: "zz", providerType: "openai", wantPath: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			applied := ApplyCredentialStorePath(config.ProviderConfig{
				Name: test.providerName,
				Type: test.providerType,
			}, home)
			path := strings.TrimSpace(applied.CredentialStorePath)
			if test.wantPath && path == "" {
				t.Fatalf("%s (%s) got no credential store path; the provider would report itself unconfigured", test.providerName, test.providerType)
			}
			if !test.wantPath && path != "" {
				t.Fatalf("%s (%s) unexpectedly got path %q", test.providerName, test.providerType, path)
			}
			// filepath.Join normalizes separators, so compare cleaned paths
			// rather than raw strings.
			if test.wantPath && !strings.HasPrefix(filepath.Clean(path), filepath.Clean(home)) {
				t.Fatalf("path %q is not under the configured home %q", path, home)
			}
		})
	}
}

// The subscription providers must not share one store between them.
func TestApplyCredentialStorePathSeparatesSubscriptionProviders(t *testing.T) {
	const home = "/home/user/.autoto"
	seen := map[string]string{}
	for _, providerType := range []string{config.ProviderTypeGemini, config.ProviderTypeGrok, config.ProviderTypeKimi} {
		path := ApplyCredentialStorePath(config.ProviderConfig{Name: providerType, Type: providerType}, home).CredentialStorePath
		if other, clash := seen[path]; clash {
			t.Fatalf("%s and %s resolve to the same store %q", providerType, other, path)
		}
		seen[path] = providerType
	}
}
