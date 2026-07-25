package providers

import (
	"context"
	"strings"
	"testing"

	"autoto/internal/config"
)

type registryTestProvider struct{ name string }

type staticGatewayAccountPolicy struct {
	ids map[string][]string
	err error
}

func (p staticGatewayAccountPolicy) ListSharedGatewayAccountIDs(_ context.Context, provider string) ([]string, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]string(nil), p.ids[provider]...), nil
}

type scenarioAvailabilityTestProvider struct {
	registryTestProvider
	available bool
	seen      ScenarioAvailability
}

func (p *scenarioAvailabilityTestProvider) AvailableForScenario(_ context.Context, availability ScenarioAvailability) bool {
	p.seen = availability
	return p.available
}

type capabilityRegistryTestProvider struct {
	registryTestProvider
	capabilities Capabilities
}

func (p capabilityRegistryTestProvider) Capabilities() Capabilities { return p.capabilities }

func (p registryTestProvider) Name() string { return p.name }
func (registryTestProvider) ListModels(context.Context) ([]string, error) {
	return nil, nil
}
func (registryTestProvider) Generate(context.Context, GenerateRequest) (<-chan Event, error) {
	out := make(chan Event)
	close(out)
	return out, nil
}

func TestCapabilitiesForUnknownProviderIsEmpty(t *testing.T) {
	if got := CapabilitiesFor(registryTestProvider{name: "unknown"}); got.Tools || got.Streaming || got.ImageInput || got.ReasoningEffort || len(got.ReasoningEfforts) != 0 {
		t.Fatalf("expected no optional capabilities, got %+v", got)
	}
}

func TestAvailableForScenarioUsesExplicitDynamicAuthorization(t *testing.T) {
	provider := &scenarioAvailabilityTestProvider{
		registryTestProvider: registryTestProvider{name: "dynamic"},
		available:            true,
	}
	availability := ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}
	if !AvailableForScenario(context.Background(), provider, false, availability) {
		t.Fatal("dynamic availability result was ignored")
	}
	if provider.seen != availability {
		t.Fatalf("dynamic availability input changed: got %+v want %+v", provider.seen, availability)
	}
	if (GenerateRequest{Scenario: CallScenarioGateway}).AllowSubscriptionCredentials {
		t.Fatal("Gateway scenario must not implicitly authorize subscription credentials")
	}
}

func TestCapabilitiesCanonicalizeLegacyReasoningBoolean(t *testing.T) {
	legacy := capabilityRegistryTestProvider{
		registryTestProvider: registryTestProvider{name: "legacy"},
		capabilities:         Capabilities{ReasoningEffort: true},
	}
	if got := CapabilitiesFor(legacy); !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("legacy reasoning capability was not canonicalized: %+v", got)
	}

	explicit := capabilityRegistryTestProvider{
		registryTestProvider: registryTestProvider{name: "explicit"},
		capabilities:         Capabilities{ReasoningEfforts: []string{" xhigh ", "medium", "unknown", "low", "medium"}},
	}
	if got := CapabilitiesFor(explicit); !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,xhigh" {
		t.Fatalf("explicit reasoning capability was not canonicalized: %+v", got)
	}
}

func TestBuiltInProvidersDeclareCapabilities(t *testing.T) {
	for _, test := range []struct {
		name           string
		provider       Provider
		wantImageInput bool
	}{
		{name: "OpenAI official", provider: NewOpenAIOfficial(config.ProviderConfig{Name: "openai", Type: "openai"}), wantImageInput: true},
		{name: "Anthropic", provider: NewAnthropicProvider(config.ProviderConfig{Name: "anthropic", Type: "anthropic"}), wantImageInput: true},
		{name: "OpenAI-compatible", provider: NewOpenAICompatible(config.ProviderConfig{Name: "relay", Type: "openai-compatible"})},
		{name: "Gemini interactions", provider: NewGeminiInteractions(config.ProviderConfig{Name: "gemini", Type: "gemini-interactions"}), wantImageInput: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := CapabilitiesFor(test.provider)
			if !got.Tools || !got.Streaming || got.ImageInput != test.wantImageInput {
				t.Fatalf("capabilities = %+v, want tools and streaming with ImageInput=%v", got, test.wantImageInput)
			}
		})
	}
}

func TestBuiltInProvidersExposeConfiguredModelContextLimits(t *testing.T) {
	const limit = 345678
	model := "configured-model"
	cfg := config.ProviderConfig{Model: model, Models: []config.ProviderModelConfig{{Name: model, ContextTokenLimit: limit}}}
	for _, provider := range []Provider{
		NewOpenAIOfficial(cfg),
		NewAnthropicProvider(cfg),
		NewOpenAICompatible(cfg),
		NewGeminiInteractions(cfg),
	} {
		if got := ModelCapabilitiesFor(provider, model).ContextTokenLimit; got != limit {
			t.Fatalf("%T context limit = %d, want %d", provider, got, limit)
		}
	}

	codex := NewCodexProvider(config.ProviderConfig{
		Name:    "codex",
		Type:    config.ProviderTypeCodex,
		BaseURL: "https://chatgpt.com/backend-api/codex",
		Model:   "gpt-5.5",
		Models:  []config.ProviderModelConfig{{Name: "gpt-5.5", ContextTokenLimit: limit}},
	})
	capabilities := ModelCapabilitiesFor(codex, "gpt-5.5")
	if capabilities.ContextTokenLimit != limit || !capabilities.FastMode || !capabilities.FastModeKnown {
		t.Fatalf("Codex did not merge configured context and Fast capabilities: %+v", capabilities)
	}
}

func TestNewProviderBuildsKnownTypes(t *testing.T) {
	for _, providerType := range []string{"openai", "anthropic", "openai-compatible", config.ProviderTypeGeminiInteractions, config.ProviderTypeGemini, config.ProviderTypeGrok, config.ProviderTypeKimi} {
		provider, err := NewProvider(config.ProviderConfig{Name: providerType, Type: providerType})
		if err != nil {
			t.Fatalf("NewProvider(%q): %v", providerType, err)
		}
		if provider.Name() != providerType {
			t.Fatalf("NewProvider(%q) name = %q", providerType, provider.Name())
		}
	}
	if _, err := NewProvider(config.ProviderConfig{Type: "unknown"}); err == nil {
		t.Fatal("expected unsupported provider type error")
	}
}

func TestRegistryDefaultUsesModelPrefixThenConfigOrder(t *testing.T) {
	registry := NewRegistry()
	registry.Register(registryTestProvider{name: "first"})
	registry.Register(registryTestProvider{name: "second"})
	configs := []config.ProviderConfig{{Name: "first"}, {Name: "second"}}

	if !registry.SetDefaultFromConfig("second:model", configs) {
		t.Fatal("expected default selection")
	}
	provider, err := registry.Default()
	if err != nil || provider.Name() != "second" {
		t.Fatalf("expected prefixed default second, provider=%v err=%v", provider, err)
	}

	if !registry.SetDefaultFromConfig("unprefixed-model", configs) {
		t.Fatal("expected config-order default selection")
	}
	provider, err = registry.Default()
	if err != nil || provider.Name() != "first" {
		t.Fatalf("expected config-order default first, provider=%v err=%v", provider, err)
	}
}

func TestRegistryUnregisterClearsDefaultAndDisabledConfigsAreSkipped(t *testing.T) {
	registry := NewRegistry()
	registry.Register(registryTestProvider{name: "enabled"})
	registry.Register(registryTestProvider{name: "disabled"})
	configs := []config.ProviderConfig{
		{Name: "disabled", Disabled: true},
		{Name: "enabled"},
	}
	if !registry.SetDefaultFromConfig("disabled:model", configs) {
		t.Fatal("expected enabled fallback to become default")
	}
	provider, err := registry.Default()
	if err != nil || provider.Name() != "enabled" {
		t.Fatalf("disabled config selected as default: provider=%v err=%v", provider, err)
	}
	if !registry.Unregister("enabled") {
		t.Fatal("expected unregister to remove provider")
	}
	if _, _, err := registry.Resolve("enabled:model"); err == nil {
		t.Fatal("unregistered provider must not resolve")
	}
	if _, err := registry.Default(); err == nil {
		t.Fatal("unregistering default must clear stale default")
	}
}

func TestReasoningCapabilitiesAreProviderSpecific(t *testing.T) {
	if got := CapabilitiesFor(NewOpenAIOfficial(config.ProviderConfig{})); !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("official OpenAI provider should support the standard reasoning efforts, got %+v", got)
	}
	if got := CapabilitiesFor(NewCodexProvider(config.ProviderConfig{Type: config.ProviderTypeCodex})); !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high,xhigh" {
		t.Fatalf("Codex provider should declare xhigh, got %+v", got)
	}
	if CapabilitiesFor(NewAnthropicProvider(config.ProviderConfig{})).ReasoningEffort {
		t.Fatal("Anthropic provider should not claim OpenAI reasoning effort support")
	}
	if CapabilitiesFor(NewOpenAICompatible(config.ProviderConfig{})).ReasoningEffort {
		t.Fatal("ordinary compatible provider should not claim reasoning effort support")
	}
	if got := CapabilitiesFor(NewOpenAICompatible(config.ProviderConfig{Profile: config.ProviderProfileCLIProxyAPI})); !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("CLIProxyAPI profile should support the standard reasoning efforts, got %+v", got)
	}
}

func TestNewProviderRejectsInvalidRuntimeIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  config.ProviderConfig
		want string
	}{
		{name: "client-version", cfg: config.ProviderConfig{Type: "openai", ClientVersion: "1.2.3\r\nCodex"}, want: "client version"},
		{name: "installation-id", cfg: config.ProviderConfig{Type: "openai-compatible", InstallationID: "not-a-uuid"}, want: "installation ID"},
		{name: "non-v4-installation-id", cfg: config.ProviderConfig{Type: "openai", InstallationID: "123e4567-e89b-12d3-a456-426614174000"}, want: "UUIDv4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewProvider(test.cfg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected identity validation error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestRegistryDefaultSkipsUnconfiguredProviderAdapters(t *testing.T) {
	registry := NewRegistry()
	openAI := NewOpenAIOfficial(config.ProviderConfig{Name: "openai", Type: "openai", Model: "gpt"})
	anthropic := NewAnthropicProvider(config.ProviderConfig{Name: "anthropic", Type: "anthropic", Model: "claude"})
	compatible := NewOpenAICompatible(config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65510/v1", Model: "model"})
	local := NewOpenAICompatible(config.ProviderConfig{Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, BaseURL: "http://127.0.0.1:8317/v1", Model: "model", APIKeyOptional: true})
	for _, provider := range []Provider{openAI, anthropic, compatible, local} {
		registry.Register(provider)
	}
	configs := []config.ProviderConfig{
		{Name: "openai", Type: "openai", Model: "gpt"},
		{Name: "anthropic", Type: "anthropic", Model: "claude"},
		{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65510/v1", Model: "model"},
		{Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, BaseURL: "http://127.0.0.1:8317/v1", Model: "model", APIKeyOptional: true},
	}
	if !registry.SetDefaultFromConfig("openai:gpt", configs) {
		t.Fatal("expected configured CLIProxyAPI fallback")
	}
	provider, err := registry.Default()
	if err != nil || provider.Name() != "cliproxyapi" {
		t.Fatalf("unconfigured API-key providers became default: provider=%v err=%v", provider, err)
	}
}

func TestNewProviderRejectsUnsafeCustomBaseURLs(t *testing.T) {
	for _, providerType := range []string{"openai-compatible", "openai", "anthropic"} {
		t.Run(providerType, func(t *testing.T) {
			_, err := NewProvider(config.ProviderConfig{Name: "custom", Type: providerType, BaseURL: "http://169.254.169.254/v1", Model: "model", APIKey: "fixture-key"})
			if err == nil {
				t.Fatal("metadata base URL must be rejected")
			}
			if strings.Contains(err.Error(), "fixture-key") || strings.Contains(err.Error(), "169.254") {
				t.Fatalf("unsafe URL error leaked sensitive input: %v", err)
			}
		})
	}
	if _, err := NewProvider(config.ProviderConfig{Name: "local", Type: "openai-compatible", BaseURL: "http://127.0.0.1:11434/v1", Model: "model", APIKeyOptional: true}); err != nil {
		t.Fatalf("loopback provider base URL must remain supported: %v", err)
	}
}

func TestRegistryRejectsAggregateWithoutSource(t *testing.T) {
	registry := NewRegistry()
	if _, _, err := registry.Resolve("aggregate:fast"); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("expected missing aggregate source error, got %v", err)
	}
	if _, _, err := registry.Resolve("aggregate:"); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected empty aggregate name error, got %v", err)
	}
}
