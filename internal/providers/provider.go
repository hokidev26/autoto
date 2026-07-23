package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"autoto/internal/config"
)

type Message struct {
	Role    string         `json:"role"`
	Content string         `json:"content"`
	Blocks  []ContentBlock `json:"blocks,omitempty"`
}

type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Data     []byte `json:"-"`
	Filename string `json:"filename,omitempty"`
	Kind     string `json:"kind,omitempty"`

	AssetID       string `json:"assetId,omitempty"`
	GenerationID  string `json:"generationId,omitempty"`
	Status        string `json:"status,omitempty"`
	OutputIndex   int64  `json:"outputIndex,omitempty"`
	PartialIndex  int64  `json:"partialIndex,omitempty"`
	RevisedPrompt string `json:"revisedPrompt,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`

	ToolUseID string          `json:"toolUseId,omitempty"`
	ToolName  string          `json:"toolName,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    string          `json:"output,omitempty"`
	IsError   bool            `json:"isError,omitempty"`

	// ProviderState carries opaque adapter state (for example Gemini thought
	// signatures). It is persisted separately from public message JSON.
	ProviderState json.RawMessage `json:"-"`
}

type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schema      any    `json:"schema,omitempty"`
}

type ToolCall struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Input         json.RawMessage `json:"input,omitempty"`
	ProviderState json.RawMessage `json:"-"`
}

type Usage struct {
	InputTokens       int64 `json:"inputTokens,omitempty"`
	OutputTokens      int64 `json:"outputTokens,omitempty"`
	CachedInputTokens int64 `json:"cachedInputTokens,omitempty"`
	ReasoningTokens   int64 `json:"reasoningTokens,omitempty"`
}

type CallScenario string

const (
	CallScenarioInternal CallScenario = "internal"
	CallScenarioGateway  CallScenario = "gateway"
)

var ErrGatewayOAuthUnsupported = errors.New("gateway calls do not support OAuth providers")

type GenerateRequest struct {
	Model                 string
	SystemPrompt          string
	Messages              []Message
	Tools                 []ToolSpec
	ReasoningEffort       string
	MaxOutputTokens       int64
	FastMode              bool
	EnableImageGeneration bool
	// Scenario identifies the caller boundary. The zero value is treated as an
	// internal call for backwards compatibility.
	Scenario CallScenario
	// AllowSubscriptionCredentials is a separate, explicit authorization bit for
	// callers that may use granted subscription or managed account credentials.
	// A Gateway scenario alone never enables those credentials.
	AllowSubscriptionCredentials bool
}

func (r GenerateRequest) EffectiveScenario() CallScenario {
	if r.Scenario == "" {
		return CallScenarioInternal
	}
	return r.Scenario
}

// GatewayAccountPolicy supplies the stable provider account IDs explicitly
// granted for Gateway sharing. Implementations may live in db-backed packages;
// providers depend only on this small interface to avoid an import cycle.
type GatewayAccountPolicy interface {
	ListSharedGatewayAccountIDs(context.Context, string) ([]string, error)
}

// ScenarioAvailability describes the credential boundary used by dynamic
// readiness checks. Its zero value is an internal call with no subscription
// credential authorization.
type ScenarioAvailability struct {
	Scenario                     CallScenario
	AllowSubscriptionCredentials bool
}

func (a ScenarioAvailability) EffectiveScenario() CallScenario {
	if a.Scenario == "" {
		return CallScenarioInternal
	}
	return a.Scenario
}

// ScenarioAvailabilityProvider reports live readiness when credentials depend
// on request-scoped grants rather than configuration alone.
type ScenarioAvailabilityProvider interface {
	AvailableForScenario(context.Context, ScenarioAvailability) bool
}

// configuredCredentialID attributes requests to a config-held credential slot
// without storing the API key or a reversible key-derived identifier.
const configuredCredentialID = "configured"

type DispatchInfo struct {
	Provider     string
	Model        string
	CredentialID string
}

type ImageGeneration struct {
	GenerationID  string `json:"generationId"`
	Status        string `json:"status"`
	OutputIndex   int64  `json:"outputIndex"`
	PartialIndex  int64  `json:"partialIndex"`
	RevisedPrompt string `json:"revisedPrompt,omitempty"`
	Data          []byte `json:"-"`
	MIME          string `json:"mime,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
}

type Event struct {
	Type            string
	Text            string
	ToolCall        *ToolCall
	ImageGeneration *ImageGeneration
	Usage           *Usage
	StopReason      string
	Done            bool
	// Dispatch reports the concrete target selected by an adapter. Nil preserves
	// the historical event contract for providers that do not report attribution.
	Dispatch *DispatchInfo
}

func newDispatchEvent(provider, model, credentialID string) Event {
	return Event{Type: "dispatch", Dispatch: &DispatchInfo{
		Provider:     strings.TrimSpace(provider),
		Model:        strings.TrimSpace(model),
		CredentialID: strings.TrimSpace(credentialID),
	}}
}

type Provider interface {
	Name() string
	ListModels(ctx context.Context) ([]string, error)
	Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error)
}

// Capabilities are optional provider features. Providers that do not implement
// CapabilityProvider are treated as supporting no optional features.
type Capabilities struct {
	Tools            bool     `json:"tools"`
	Streaming        bool     `json:"streaming"`
	ImageInput       bool     `json:"imageInput"`
	ImageGeneration  bool     `json:"imageGeneration"`
	Reasoning        bool     `json:"reasoning,omitempty"`
	ReasoningEffort  bool     `json:"reasoningEffort"`
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
}

type CapabilityProvider interface {
	Capabilities() Capabilities
}

// ModelCapabilities are optional features that can differ between models of
// the same provider. Unknown models default to no optional model features.
type ModelCapabilities struct {
	FastMode             bool `json:"fastMode"`
	FastModeKnown        bool `json:"-"`
	ImageGeneration      bool `json:"imageGeneration"`
	ImageGenerationKnown bool `json:"-"`
	ContextTokenLimit    int  `json:"contextTokenLimit"`
}

type ModelCapabilityProvider interface {
	ModelCapabilities(model string) ModelCapabilities
}

// ConfigurationProvider reports whether a runtime provider currently has the
// credentials required to serve requests. It is intentionally optional so API
// key providers can continue using config-derived readiness.
type ConfigurationProvider interface {
	Configured() bool
}

// ScenarioConfigurationProvider can apply stricter credential eligibility at a
// caller boundary. Gateway implementations use this to exclude OAuth/profile
// credentials even when the same provider remains configured for internal use.
type ScenarioConfigurationProvider interface {
	ConfiguredForScenario(CallScenario) bool
}

func ConfiguredFor(provider Provider, fallback bool) bool {
	if provider, ok := provider.(ConfigurationProvider); ok {
		return provider.Configured()
	}
	return fallback
}

func ConfiguredForScenario(provider Provider, fallback bool, scenario CallScenario) bool {
	if provider, ok := provider.(ScenarioConfigurationProvider); ok {
		return provider.ConfiguredForScenario(scenario)
	}
	return ConfiguredFor(provider, fallback)
}

func AvailableForScenario(ctx context.Context, provider Provider, fallback bool, availability ScenarioAvailability) bool {
	if provider, ok := provider.(ScenarioAvailabilityProvider); ok {
		return provider.AvailableForScenario(ctx, availability)
	}
	return ConfiguredForScenario(provider, fallback, availability.EffectiveScenario())
}

func gatewayAccountIDSet(ctx context.Context, policy GatewayAccountPolicy, provider string) (map[string]struct{}, error) {
	if policy == nil {
		return nil, nil
	}
	ids, err := policy.ListSharedGatewayAccountIDs(ctx, strings.TrimSpace(provider))
	if err != nil {
		return nil, err
	}
	granted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			granted[id] = struct{}{}
		}
	}
	return granted, nil
}

func CapabilitiesFor(provider Provider) Capabilities {
	if provider, ok := provider.(CapabilityProvider); ok {
		return canonicalCapabilities(provider.Capabilities())
	}
	return Capabilities{}
}

func ModelCapabilitiesFor(provider Provider, model string) ModelCapabilities {
	if provider, ok := provider.(ModelCapabilityProvider); ok {
		return provider.ModelCapabilities(strings.TrimSpace(model))
	}
	return ModelCapabilities{}
}

func configuredModelCapabilities(cfg config.ProviderConfig, model string) ModelCapabilities {
	imageGeneration, imageGenerationKnown := cfg.ModelImageGeneration(model)
	return ModelCapabilities{
		ImageGeneration:      imageGeneration,
		ImageGenerationKnown: imageGenerationKnown,
		ContextTokenLimit:    cfg.ModelContextTokenLimit(model),
	}
}

// NewProvider constructs a provider adapter from a normalized provider config.
func NewProvider(cfg config.ProviderConfig) (Provider, error) {
	providerType := strings.TrimSpace(cfg.Type)
	if providerType == "openai" || providerType == "openai-compatible" || providerType == "anthropic" || providerType == "gemini-interactions" || providerType == config.ProviderTypeCodex {
		if err := validateProviderRuntimeConfig(cfg); err != nil {
			return nil, err
		}
	}
	switch providerType {
	case config.ProviderTypeCodex:
		if err := ValidateCodexProviderConfig(cfg); err != nil {
			return nil, err
		}
		return NewCodexProvider(cfg), nil
	case "openai":
		return NewOpenAIOfficial(cfg), nil
	case "anthropic":
		return NewAnthropicProvider(cfg), nil
	case "openai-compatible":
		return NewOpenAICompatible(cfg), nil
	case "gemini-interactions":
		return NewGeminiInteractions(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q", cfg.Type)
	}
}

type Registry struct {
	mu              sync.RWMutex
	providers       map[string]Provider
	defaultName     string
	aggregateSource AggregateSource
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

func (r *Registry) Register(provider Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.Name()] = provider
}

// Unregister removes a provider from runtime resolution. If it was selected as
// the default, the default is cleared so callers cannot receive a stale adapter.
// Call SetDefaultFromConfig after unregistering when a safe fallback exists.
func (r *Registry) Unregister(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; !ok {
		return false
	}
	delete(r.providers, name)
	if r.defaultName == name {
		r.defaultName = ""
	}
	return true
}

// SetAggregateSource configures the runtime source used by aggregate providers.
// Replacing the source affects aggregate providers that were already resolved.
func (r *Registry) SetAggregateSource(source AggregateSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aggregateSource = source
}

func (r *Registry) aggregateSourceSnapshot() AggregateSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.aggregateSource
}

func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[name]
	return provider, ok
}

func (r *Registry) Resolve(model string) (Provider, string, error) {
	providerName, modelName := SplitModel(model)
	if strings.EqualFold(providerName, aggregateProviderPrefix) {
		if providerName != aggregateProviderPrefix {
			return nil, "", fmt.Errorf("aggregate model prefix must be %q", aggregateProviderPrefix)
		}
		if r.aggregateSourceSnapshot() == nil {
			return nil, "", errors.New("aggregate provider source is not configured")
		}
		return newAggregateProvider(r, modelName), modelName, nil
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), aggregateProviderPrefix+":") {
		return nil, "", errors.New("aggregate name must not be empty")
	}
	if providerName != "" {
		provider, ok := r.Get(providerName)
		if !ok {
			return nil, "", fmt.Errorf("provider %q is not registered", providerName)
		}
		return provider, modelName, nil
	}
	provider, err := r.Default()
	if err != nil {
		return nil, "", err
	}
	return provider, modelName, nil
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SetDefault explicitly selects the provider used for unprefixed model names.
func (r *Registry) SetDefault(name string) error {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[name]; !ok {
		return fmt.Errorf("provider %q is not registered", name)
	}
	r.defaultName = name
	return nil
}

// SetDefaultFromConfig selects a default deterministically: first the provider
// named by the default model prefix, then the first registered provider in
// configuration order. It returns false when no configured provider is registered.
func (r *Registry) SetDefaultFromConfig(defaultModel string, configs []config.ProviderConfig) bool {
	preferred, _ := SplitModel(defaultModel)
	known := make(map[string]bool, len(configs))
	disabled := make(map[string]bool, len(configs))
	for _, cfg := range configs {
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			continue
		}
		known[name] = true
		disabled[name] = cfg.Disabled
	}

	candidates := make([]string, 0, len(configs)+1)
	if preferred != "" && (!known[preferred] || !disabled[preferred]) {
		candidates = append(candidates, preferred)
	}
	for _, cfg := range configs {
		name := strings.TrimSpace(cfg.Name)
		if name != "" && !cfg.Disabled && name != preferred {
			candidates = append(candidates, name)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range candidates {
		provider, ok := r.providers[name]
		if !ok {
			continue
		}
		// Known runtime adapters report readiness from their live credentials.
		// Providers without this optional interface are retained for backwards
		// compatible registry use (notably tests and external adapters).
		if configured, ok := provider.(ConfigurationProvider); ok && !configured.Configured() {
			continue
		}
		r.defaultName = name
		return true
	}
	r.defaultName = ""
	return false
}

func (r *Registry) Default() (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.defaultName == "" {
		return nil, errors.New("no default provider configured")
	}
	provider, ok := r.providers[r.defaultName]
	if !ok {
		return nil, fmt.Errorf("default provider %q is not registered", r.defaultName)
	}
	return provider, nil
}

func SplitModel(model string) (providerName string, modelName string) {
	model = strings.TrimSpace(model)
	parts := strings.SplitN(model, ":", 2)
	if len(parts) != 2 {
		return "", model
	}
	providerName = strings.TrimSpace(parts[0])
	modelName = strings.TrimSpace(parts[1])
	if providerName == "" || modelName == "" {
		return "", model
	}
	return providerName, modelName
}
