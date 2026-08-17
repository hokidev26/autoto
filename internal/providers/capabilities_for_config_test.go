package providers

import (
	"strings"
	"testing"

	"autoto/internal/config"
)

func TestCapabilitiesForConfigAdvertisesThinkingEffortWithoutInstance(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.ProviderConfig
		wantEffort  bool
		wantEfforts string
	}{
		{
			// Provider-wide upper bound; per-model narrowing is covered by
			// TestAnthropicModelReasoningEffortsFollowThinkingSupport.
			name:        "anthropic relay",
			cfg:         config.ProviderConfig{Name: "myrelay", Type: "anthropic"},
			wantEffort:  true,
			wantEfforts: "low,medium,high,xhigh,max",
		},
		{
			name:        "claude code relay uses anthropic type",
			cfg:         config.ProviderConfig{Name: "cc", Type: "anthropic", Profile: "claude-code"},
			wantEffort:  true,
			wantEfforts: "low,medium,high,xhigh,max",
		},
		{
			name:        "codex relay keeps xhigh",
			cfg:         config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex},
			wantEffort:  true,
			wantEfforts: "low,medium,high,xhigh",
		},
		{
			name:        "kimi keeps its reduced set",
			cfg:         config.ProviderConfig{Name: "kimi", Type: config.ProviderTypeKimi},
			wantEffort:  true,
			wantEfforts: "low,high",
		},
		{
			name:        "plain openai compatible advertises standard effort",
			cfg:         config.ProviderConfig{Name: "groq", Type: "openai-compatible"},
			wantEffort:  true,
			wantEfforts: "low,medium,high",
		},
		{
			name:        "cliproxyapi profile advertises effort",
			cfg:         config.ProviderConfig{Name: "relay", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI},
			wantEffort:  true,
			wantEfforts: "low,medium,high",
		},
		{
			name:       "kiro advertises no effort",
			cfg:        config.ProviderConfig{Name: "kiro", Type: config.ProviderTypeKiro},
			wantEffort: false,
		},
		{
			name:       "unknown type stays empty",
			cfg:        config.ProviderConfig{Name: "x", Type: "not-a-protocol"},
			wantEffort: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capabilities := CapabilitiesForConfig(test.cfg)
			if capabilities.ReasoningEffort != test.wantEffort {
				t.Fatalf("ReasoningEffort = %v, want %v", capabilities.ReasoningEffort, test.wantEffort)
			}
			if got := strings.Join(capabilities.ReasoningEfforts, ","); got != test.wantEfforts {
				t.Fatalf("ReasoningEfforts = %q, want %q", got, test.wantEfforts)
			}
		})
	}
}

// The static fallback must not drift from the live adapter declarations, since
// the model catalog switches between them depending on registration state.
func TestCapabilitiesForConfigMatchesLiveAdapters(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ProviderConfig
	}{
		{name: "anthropic", cfg: config.ProviderConfig{Name: "a", Type: "anthropic", BaseURL: "https://example.invalid", APIKey: "k"}},
		{name: "openai-compatible cliproxyapi", cfg: config.ProviderConfig{Name: "b", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, BaseURL: "https://example.invalid", APIKey: "k"}},
		{name: "openai-compatible plain", cfg: config.ProviderConfig{Name: "c", Type: "openai-compatible", BaseURL: "https://example.invalid", APIKey: "k"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			live, err := NewProvider(config.NormalizeProviderConfig(test.cfg))
			if err != nil {
				t.Skipf("provider construction unavailable: %v", err)
			}
			want := CapabilitiesFor(live)
			got := CapabilitiesForConfig(config.NormalizeProviderConfig(test.cfg))
			if got.ReasoningEffort != want.ReasoningEffort {
				t.Fatalf("ReasoningEffort = %v, want %v", got.ReasoningEffort, want.ReasoningEffort)
			}
			if a, b := strings.Join(got.ReasoningEfforts, ","), strings.Join(want.ReasoningEfforts, ","); a != b {
				t.Fatalf("ReasoningEfforts = %q, want %q", a, b)
			}
		})
	}
}
