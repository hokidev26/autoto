package providers

import (
	"strings"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
)

// CodexCLIIdentity is the short first-party identity sentence Autoto may
// prepend when a provider is configured for Codex CLI flavor. It is not the
// official Codex system prompt.
const CodexCLIIdentity = "You are Codex, OpenAI's official CLI for Codex."

// ClientIdentityText returns the stable identity sentence for a provider
// setting, or empty when the provider should keep Autoto's own prompt.
func ClientIdentityText(identity string) string {
	switch config.NormalizeClientIdentity(identity) {
	case config.ClientIdentityClaudeCode:
		return anthropicauth.ClaudeCodeIdentity
	case config.ClientIdentityCodex:
		return CodexCLIIdentity
	default:
		return ""
	}
}

// WithClientIdentity prepends a configured CLI identity to a system prompt.
// The Autoto runtime prompt and safety boundary stay after it. Empty identity
// and an already-prefixed prompt are left unchanged.
func WithClientIdentity(systemPrompt, identity string) string {
	text := ClientIdentityText(identity)
	if text == "" {
		return systemPrompt
	}
	trimmed := strings.TrimSpace(systemPrompt)
	if trimmed == "" {
		return text
	}
	if trimmed == text || strings.HasPrefix(trimmed, text+"\n") {
		return systemPrompt
	}
	return text + "\n\n" + systemPrompt
}

func applyConfiguredClientIdentity(cfg config.ProviderConfig, req GenerateRequest) GenerateRequest {
	if req.EffectiveScenario() == CallScenarioGateway {
		return req
	}
	req.SystemPrompt = WithClientIdentity(req.SystemPrompt, cfg.ClientIdentity)
	return req
}
