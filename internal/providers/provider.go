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
	// TurnControl marks a server-injected message that changes between turns
	// (spec sidecar, silent-progress reminder, continuation control, ...).
	// Providers with prompt caching must keep such messages out of any
	// cache-stable region: the cached prefix has to end before them, or the
	// cache is written every turn and never read.
	TurnControl bool `json:"turnControl,omitempty"`
}

const (
	ContentBlockTypeThinking         = "thinking"
	ContentBlockTypeRedactedThinking = "redacted_thinking"
)

type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Data     []byte `json:"-"`
	Filename string `json:"filename,omitempty"`
	Kind     string `json:"kind,omitempty"`

	// ReasoningText carries complete native reasoning text for provider replay.
	// It is persisted through ProviderStateJSON and deliberately omitted from
	// public content JSON; user-facing reasoning uses db.Message.ReasoningText.
	ReasoningText string `json:"-"`

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
	ReasoningBudgetTokens int64
	MaxOutputTokens       int64
	FastMode              bool
	EnableImageGeneration bool
	// ImageOptions frames a dedicated image-generation request. It is ignored by
	// providers and models that do not generate images.
	ImageOptions ImageOptions
	// SessionKey is a stable opaque key for the conversation this request
	// belongs to (the agent ID for run requests). Providers with session
	// affinity derive a stable per-conversation identifier from it — Gemini
	// Cloud Code scopes its sessionId to it — so per-request randomness does
	// not defeat upstream session caching. One-shot internal calls leave it
	// empty and get a fresh session.
	SessionKey string
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

// Event.Type is one of "dispatch", "text", "reasoning", "content_block",
// "tool_call", "tool_call_delta", "image_generation", "error" or "done".
//
// "reasoning" carries the model's own summary of what it is about to do, in
// Text, and is advisory: providers that do not expose a readable summary (or
// expose only encrypted reasoning, as the Codex backend does) simply never emit
// it, and every consumer must behave identically when it is absent.
//
// "tool_call_delta" is likewise advisory: Text carries the next raw fragment
// of the argument JSON for the tool call identified by ToolCall.ID/Name (Input
// stays empty). It exists so the UI can preview a Write's content while the
// model is still composing the call. Providers that cannot stream arguments
// simply never emit it; the complete "tool_call" remains the source of truth.
type Event struct {
	Type            string
	Text            string
	ToolCall        *ToolCall
	ContentBlock    *ContentBlock
	BlockType       string
	BlockIndex      int64
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

	// NativeReasoningBlocks is an internal routing capability. It indicates that
	// the adapter can replay opaque signed/encrypted reasoning content blocks.
	NativeReasoningBlocks bool `json:"-"`
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
	// ReasoningEfforts overrides the provider-wide list for this model. Codex
	// levels differ per model — only some serve "max" or "ultra" — and asking a
	// model for a level it does not serve is rejected outright, so an empty
	// value here means "no model-specific knowledge, use the provider's list"
	// rather than "no levels".
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
}

// EffectiveReasoningEfforts resolves which levels a specific model accepts.
// Model knowledge wins because it comes from the authenticated catalog, which
// is per-account and more precise than the provider's static baseline.
func EffectiveReasoningEfforts(capabilities Capabilities, model ModelCapabilities) []string {
	if len(model.ReasoningEfforts) > 0 {
		return canonicalReasoningEffortValues(model.ReasoningEfforts)
	}
	return canonicalCapabilities(capabilities).ReasoningEfforts
}

// CapabilitiesForModel folds a model's own effort list into the provider
// capabilities, so a single value can be handed to SupportsReasoningEffort.
func CapabilitiesForModel(capabilities Capabilities, model ModelCapabilities) Capabilities {
	efforts := EffectiveReasoningEfforts(capabilities, model)
	capabilities.ReasoningEfforts = efforts
	capabilities.ReasoningEffort = len(efforts) > 0
	return capabilities
}

type ModelCapabilityProvider interface {
	ModelCapabilities(model string) ModelCapabilities
}

// DefaultContextTokenLimitProvider is an optional provider capability.
// Providers that implement it advertise their protocol-level default context
// window for models that have no explicit ContextTokenLimit configured. The
// runner uses this before falling back to the global 120 000-token floor.
type DefaultContextTokenLimitProvider interface {
	DefaultContextTokenLimit() int
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
	if providerType == "openai" || providerType == "openai-compatible" || providerType == "anthropic" || providerType == config.ProviderTypeGeminiInteractions || providerType == config.ProviderTypeGemini || providerType == config.ProviderTypeGrok || providerType == config.ProviderTypeKimi || providerType == config.ProviderTypeCodex || providerType == config.ProviderTypeKiro {
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
	case config.ProviderTypeGeminiInteractions:
		return NewGeminiInteractions(cfg), nil
	case config.ProviderTypeGemini:
		cfg = normalizeGeminiProviderConfig(cfg)
		if err := validateGeminiProductionBaseURL(cfg.BaseURL); err != nil {
			return nil, err
		}
		return NewGeminiProvider(cfg), nil
	case config.ProviderTypeGrok:
		cfg = normalizeGrokProviderConfig(cfg)
		if err := validateGrokProductionBaseURL(cfg.BaseURL); err != nil {
			return nil, err
		}
		return NewGrokProvider(cfg), nil
	case config.ProviderTypeKimi:
		cfg = normalizeKimiProviderConfig(cfg)
		if err := validateKimiProductionBaseURL(cfg.BaseURL); err != nil {
			return nil, err
		}
		return NewKimiProvider(cfg), nil
	case config.ProviderTypeKiro:
		cfg = normalizeKiroProviderConfig(cfg)
		if err := validateKiroProductionBaseURL(cfg.BaseURL); err != nil {
			return nil, err
		}
		return NewKiroProvider(cfg), nil
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
