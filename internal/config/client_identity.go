package config

import (
	"errors"
	"strings"
)

const (
	// ClientIdentityClaudeCode is the per-provider prompt-identity value that
	// prepends Anthropic's Claude Code CLI identity sentence.
	ClientIdentityClaudeCode = "claude-code"
	// ClientIdentityCodex is the per-provider prompt-identity value that
	// prepends OpenAI's Codex CLI identity sentence.
	ClientIdentityCodex = "codex"
)

// ParseClientIdentity validates a client-supplied identity. Empty and "autoto"
// persist as the Autoto default (no extra identity). Unknown values are
// rejected so a typo cannot silently change what the model is told.
func ParseClientIdentity(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "autoto":
		return "", nil
	case ClientIdentityClaudeCode, ClientIdentityCodex:
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", errors.New("clientIdentity is invalid")
	}
}

// NormalizeClientIdentity maps empty, "autoto", and unknown values to Autoto's
// default. Load-time normalization fails closed instead of refusing to boot.
func NormalizeClientIdentity(value string) string {
	parsed, err := ParseClientIdentity(value)
	if err != nil {
		return ""
	}
	return parsed
}
