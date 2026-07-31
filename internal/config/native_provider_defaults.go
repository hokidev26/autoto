package config

import (
	"fmt"
	"strings"
)

const (
	ProviderTypeGemini             = "gemini"
	ProviderTypeGeminiInteractions = "gemini-interactions"
	ProviderTypeGrok               = "grok"
	ProviderTypeKimi               = "kimi"
	ProviderTypeKiro               = "kiro"
)

func defaultGeminiProvider() ProviderConfig {
	return ProviderConfig{
		Name:    ProviderTypeGemini,
		Type:    ProviderTypeGemini,
		BaseURL: "https://cloudcode-pa.googleapis.com",
		Model:   getenv("GEMINI_MODEL", "gemini-3-flash"),
		Models: []ProviderModelConfig{
			{Name: "claude-opus-4-6-thinking", ContextTokenLimit: 200000},
			{Name: "claude-sonnet-4-6", ContextTokenLimit: 200000},
			{Name: "gemini-3-flash", ContextTokenLimit: 1048576},
			{Name: "gemini-3-flash-agent", ContextTokenLimit: 1048576},
			{Name: "gemini-3.1-flash-image", ImageGeneration: true},
			{Name: "gemini-pro-agent", ContextTokenLimit: 1048576},
			{Name: "gemini-3.1-pro-low", ContextTokenLimit: 1048576},
			{Name: "gpt-oss-120b-medium", ContextTokenLimit: 114000},
			{Name: "gemini-3.1-flash-lite", ContextTokenLimit: 1048576},
			{Name: "gemini-3.5-flash-low", ContextTokenLimit: 1048576},
			{Name: "gemini-3.5-flash-extra-low", ContextTokenLimit: 1048576},
			{Name: "gemini-3.6-flash-high", ContextTokenLimit: 1048576},
		},
	}
}

func defaultGrokProvider() ProviderConfig {
	return ProviderConfig{
		Name:    ProviderTypeGrok,
		Type:    ProviderTypeGrok,
		BaseURL: "https://cli-chat-proxy.grok.com/v1",
		Model:   getenv("GROK_MODEL", "grok-4.5"),
		Models: []ProviderModelConfig{
			{Name: "grok-build-0.1", ContextTokenLimit: 256000},
			{Name: "grok-4.5", ContextTokenLimit: 500000},
			{Name: "grok-4.3", ContextTokenLimit: 1000000},
			{Name: "grok-4.20-0309-reasoning", ContextTokenLimit: 2000000},
			{Name: "grok-4.20-0309-non-reasoning", ContextTokenLimit: 2000000},
			{Name: "grok-4.20-multi-agent-0309", ContextTokenLimit: 2000000},
			{Name: "grok-3-mini", ContextTokenLimit: 131072},
			{Name: "grok-3-mini-fast", ContextTokenLimit: 131072},
			{Name: "grok-composer-2.5-fast", ContextTokenLimit: 200000},
		},
	}
}

func defaultKimiProvider() ProviderConfig {
	return ProviderConfig{
		Name:    ProviderTypeKimi,
		Type:    ProviderTypeKimi,
		BaseURL: "https://api.kimi.com/coding",
		Model:   getenv("KIMI_MODEL", "kimi-k2.7-code"),
		Models: []ProviderModelConfig{
			{Name: "kimi-k2", ContextTokenLimit: 131072},
			{Name: "kimi-k2-thinking", ContextTokenLimit: 131072},
			{Name: "kimi-k2.5", ContextTokenLimit: 262144},
			{Name: "kimi-k2.6", ContextTokenLimit: 262144},
			{Name: "kimi-k2.7-code", ContextTokenLimit: 262144},
			{Name: "kimi-k2.7-code-highspeed", ContextTokenLimit: 262144},
			{Name: "kimi-k3", ContextTokenLimit: 262144},
		},
	}
}

func defaultKiroProvider() ProviderConfig {
	return ProviderConfig{
		Name:  ProviderTypeKiro,
		Type:  ProviderTypeKiro,
		Model: "claude-sonnet-4-6",
		Models: []ProviderModelConfig{
			{Name: "claude-sonnet-5", ContextTokenLimit: 200000},
			{Name: "claude-opus-5", ContextTokenLimit: 200000},
			{Name: "claude-opus-4-6", ContextTokenLimit: 200000},
			{Name: "claude-sonnet-4-6", ContextTokenLimit: 200000},
			{Name: "claude-haiku-4-5", ContextTokenLimit: 200000},
			{Name: "gpt-5.6-sol", ContextTokenLimit: 128000},
			{Name: "gpt-5.6-terra", ContextTokenLimit: 128000},
			{Name: "gpt-5.6-luna", ContextTokenLimit: 128000},
		},
	}
}

func ensureNativeBuiltinProviders(providers ProvidersConfig) ProvidersConfig {
	for _, provider := range []ProviderConfig{defaultGeminiProvider(), defaultGrokProvider(), defaultKimiProvider(), defaultKiroProvider()} {
		// A native subscription provider is only "already present" when a matching
		// entry has the correct native type. An unrelated provider that merely
		// shares the name (for example a legacy gemini-interactions relay named
		// "gemini") must not suppress the native OAuth provider; in that case the
		// native provider is seeded under a distinct, non-colliding name.
		typedMatch := false
		nameTaken := false
		for _, existing := range providers.Instances {
			sameName := strings.EqualFold(strings.TrimSpace(existing.Name), provider.Name)
			if sameName && strings.EqualFold(strings.TrimSpace(existing.Type), provider.Type) {
				typedMatch = true
				break
			}
			if sameName {
				nameTaken = true
			}
		}
		if typedMatch {
			continue
		}
		if nameTaken {
			fallbackName, alreadySeeded := nativeProviderFallbackName(providers.Instances, provider.Type)
			if alreadySeeded {
				continue
			}
			provider.Name = fallbackName
		}
		providers.Instances = append(providers.Instances, provider)
	}
	return providers
}

func nativeProviderFallbackName(instances []ProviderConfig, providerType string) (string, bool) {
	base := providerType + "-oauth"
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		occupied := false
		for _, existing := range instances {
			if !strings.EqualFold(strings.TrimSpace(existing.Name), candidate) {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(existing.Type), providerType) {
				return candidate, true
			}
			occupied = true
			break
		}
		if !occupied {
			return candidate, false
		}
	}
}

func applyNativeProviderDefaults(provider *ProviderConfig) bool {
	if provider == nil {
		return false
	}
	var defaults ProviderConfig
	switch strings.TrimSpace(provider.Type) {
	case ProviderTypeGemini:
		defaults = defaultGeminiProvider()
	case ProviderTypeGrok:
		defaults = defaultGrokProvider()
	case ProviderTypeKimi:
		defaults = defaultKimiProvider()
	case ProviderTypeKiro:
		defaults = defaultKiroProvider()
	default:
		return false
	}
	if provider.BaseURL == "" {
		provider.BaseURL = defaults.BaseURL
	}
	if provider.Model == "" {
		provider.Model = defaults.Model
	}
	if len(provider.Models) == 0 {
		provider.Models = append([]ProviderModelConfig(nil), defaults.Models...)
	}
	return true
}
