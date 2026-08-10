package agent

import (
	"testing"

	"autoto/internal/config"
	"autoto/internal/providers"
)

// The run path resolves agent.Model once and then carries the provider and the
// bare model name separately. Looking a window up from that bare name re-enters
// resolution with no prefix, which falls through to the default provider: a
// different adapter, with no configured limit for this model, whose protocol
// default is 200 000. The user sees their configured 1 000 000 in the context
// panel and a 200 000 ceiling in the run that dies, from the same process.
func TestContextTokenLimitKeepsProviderPrefix(t *testing.T) {
	registry := providers.NewRegistry()

	// The model the agent actually runs, configured with a large window, under a
	// provider that is deliberately not the default.
	relay := providers.NewAnthropicProvider(config.ProviderConfig{
		Name:    "relay",
		Type:    "anthropic",
		BaseURL: "https://relay.invalid",
		APIKey:  "test-key",
		Models: []config.ProviderModelConfig{
			{Name: "claude-opus-5", ContextTokenLimit: 1000000},
		},
	})
	registry.Register(relay)

	// Some other Anthropic-protocol provider is the default, exactly as a real
	// install ends up when the default model names a different vendor.
	other := providers.NewAnthropicProvider(config.ProviderConfig{
		Name:    "other",
		Type:    "anthropic",
		BaseURL: "https://other.invalid",
		APIKey:  "test-key",
	})
	registry.Register(other)
	if err := registry.SetDefault("other"); err != nil {
		t.Fatalf("set default: %v", err)
	}

	runner := &Runner{providers: registry}

	qualified, qualifiedOrigin := runner.contextTokenLimitWithOrigin("relay:claude-opus-5")
	if qualified != 1000000 {
		t.Fatalf("qualified model limit = %d, want 1000000", qualified)
	}
	if qualifiedOrigin != contextLimitOriginModel {
		t.Fatalf("qualified origin = %q, want %q", qualifiedOrigin, contextLimitOriginModel)
	}

	// The bare name is what the run path used to pass. It resolves to the default
	// provider, which has no limit for this model, so its protocol default answers.
	// Pinned here so the trap this fix closes cannot be reintroduced unnoticed as
	// a harmless-looking simplification.
	bare, bareOrigin := runner.contextTokenLimitWithOrigin("claude-opus-5")
	if bare != 200000 || bareOrigin != contextLimitOriginProtocol {
		t.Fatalf("bare model limit = %d (origin %q), want 200000 from %q", bare, bareOrigin, contextLimitOriginProtocol)
	}

	// The seam the run path now goes through: it holds a provider and a bare model
	// name, and must recover the same window as the qualified form.
	restored, restoredOrigin := runner.contextTokenLimitWithOrigin(modelWithProvider(relay.Name(), "claude-opus-5"))
	if restored != qualified {
		t.Fatalf("limit via modelWithProvider = %d (origin %q), want %d: the run path is measuring turns against another provider's window", restored, restoredOrigin, qualified)
	}
	if restoredOrigin != contextLimitOriginModel {
		t.Fatalf("origin via modelWithProvider = %q, want %q", restoredOrigin, contextLimitOriginModel)
	}
}

// modelWithProvider is what the run path must use to ask about a window it has
// already resolved, so no lookup can silently land on a different adapter.
func TestModelWithProviderRestoresPrefix(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     string
	}{
		{provider: "relay", model: "claude-opus-5", want: "relay:claude-opus-5"},
		{provider: "relay", model: "relay:claude-opus-5", want: "relay:claude-opus-5"},
		{provider: "", model: "claude-opus-5", want: "claude-opus-5"},
		{provider: "relay", model: "", want: ""},
		{provider: "  relay  ", model: "  claude-opus-5  ", want: "relay:claude-opus-5"},
		// An aggregate is its own resolution rule and must not be re-prefixed.
		{provider: "aggregate", model: "aggregate:fast", want: "aggregate:fast"},
	}
	for _, tc := range cases {
		if got := modelWithProvider(tc.provider, tc.model); got != tc.want {
			t.Fatalf("modelWithProvider(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}
