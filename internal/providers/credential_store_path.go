package providers

import (
	"autoto/internal/anthropicauth"
	"autoto/internal/codexauth"
	"autoto/internal/config"
	"autoto/internal/subscriptionauth"
)

// ApplyCredentialStorePath fills in where a provider's OAuth accounts live.
//
// This exists as one shared function because it used to be written out twice —
// once when the process builds its registry at startup, once when a provider
// edit rebuilds it — and the two copies drifted. The startup copy was missing
// the subscription case, so Grok, Gemini and Kimi came up with an empty path,
// reported themselves unconfigured, and vanished from the model picker on every
// restart; saving any provider rebuilt the registry through the complete copy
// and brought them back, which made the fault look intermittent rather than
// deterministic. Both callers now share this, so the paths cannot diverge again.
func ApplyCredentialStorePath(providerCfg config.ProviderConfig, homeDir string) config.ProviderConfig {
	switch providerCfg.Type {
	case config.ProviderTypeCodex:
		providerCfg.CredentialStorePath = codexauth.DefaultStoreDir(homeDir)
	case config.ProviderTypeGemini, config.ProviderTypeGrok, config.ProviderTypeKimi, config.ProviderTypeKiro:
		providerCfg.CredentialStorePath = subscriptionauth.DefaultStoreDir(homeDir, providerCfg.Type)
	}
	// Anthropic keys off the provider name as well as the type, because only the
	// canonical anthropic provider owns the shared account store.
	if providerCfg.Name == anthropicauth.DefaultProviderName && providerCfg.Type == "anthropic" {
		providerCfg.CredentialStorePath = anthropicauth.DefaultStoreDir(homeDir)
	}
	return providerCfg
}
