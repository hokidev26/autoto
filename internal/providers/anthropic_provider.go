package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
)

const (
	anthropicReasoningStateVersion   = 1
	anthropicReasoningStateProvider  = "anthropic"
	anthropicInterleavedThinkingBeta = "interleaved-thinking-2025-05-14"
)

var anthropicModelVersionPattern = regexp.MustCompile(`claude-(?:opus|sonnet|haiku|mythos|fable)-([0-9]+)(?:[-.]([0-9]+))?`)

type anthropicThinkingSupport struct {
	Known     bool
	Supported bool
	Adaptive  bool
	Enabled   bool
}

type anthropicReasoningState struct {
	Version   int    `json:"version"`
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

type AnthropicProvider struct {
	cfg               config.ProviderConfig
	store             *anthropicauth.Store
	configErr         error
	telemetry         AccountTelemetry
	quotaTelemetry    AccountQuotaTelemetry
	clock             func() time.Time
	gatewayPolicyMu   sync.RWMutex
	gatewayPolicy     GatewayAccountPolicy
	thinkingSupportMu sync.RWMutex
	thinkingSupport   map[string]anthropicThinkingSupport
	oauthRefreshGate  chan struct{}
	oauthRefreshToken func(context.Context, string, *http.Client) (anthropicauth.OAuthTokenResponse, error)
}

func NewAnthropicProvider(cfg config.ProviderConfig) *AnthropicProvider {
	if cfg.Name == "" {
		cfg.Name = anthropicauth.DefaultProviderName
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-5"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	return &AnthropicProvider{
		cfg:               cfg,
		store:             anthropicauth.NewStore(cfg.CredentialStorePath),
		configErr:         validateProviderRuntimeConfig(cfg),
		clock:             time.Now,
		thinkingSupport:   make(map[string]anthropicThinkingSupport),
		oauthRefreshGate:  make(chan struct{}, 1),
		oauthRefreshToken: anthropicauth.RefreshTokens,
	}
}

func (p *AnthropicProvider) Name() string { return p.cfg.Name }

func (p *AnthropicProvider) Configured() bool {
	return p != nil && p.configErr == nil && ((p.store != nil && p.store.Configured()) || strings.TrimSpace(p.cfg.APIKey) != "")
}

func (p *AnthropicProvider) ConfiguredForScenario(scenario CallScenario) bool {
	if scenario != CallScenarioGateway {
		return p.Configured()
	}
	return p != nil && p.configErr == nil && strings.TrimSpace(p.cfg.APIKey) != ""
}

func (p *AnthropicProvider) AvailableForScenario(ctx context.Context, availability ScenarioAvailability) bool {
	if p == nil || p.configErr != nil {
		return false
	}
	candidates, err := p.accountCandidates(ctx, GenerateRequest{
		Scenario:                     availability.EffectiveScenario(),
		AllowSubscriptionCredentials: availability.AllowSubscriptionCredentials,
	})
	return err == nil && len(candidates) > 0
}

func (p *AnthropicProvider) SetGatewayAccountPolicy(policy GatewayAccountPolicy) {
	if p == nil {
		return
	}
	p.gatewayPolicyMu.Lock()
	p.gatewayPolicy = policy
	p.gatewayPolicyMu.Unlock()
}

func (p *AnthropicProvider) gatewayAccountPolicy() GatewayAccountPolicy {
	if p == nil {
		return nil
	}
	p.gatewayPolicyMu.RLock()
	defer p.gatewayPolicyMu.RUnlock()
	return p.gatewayPolicy
}

func (p *AnthropicProvider) Capabilities() Capabilities {
	// ReasoningEfforts is the provider-wide upper bound. ModelCapabilities
	// narrows it per model, because xhigh needs 4.7+ and the manual budget path
	// serves neither xhigh nor max.
	return Capabilities{
		Tools:                 true,
		Streaming:             true,
		ImageInput:            true,
		Reasoning:             true,
		ReasoningEffort:       true,
		ReasoningEfforts:      anthropicBaselineReasoningEfforts,
		NativeReasoningBlocks: true,
	}
}

func (p *AnthropicProvider) DefaultContextTokenLimit() int { return 200000 }

func (p *AnthropicProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	capabilities := configuredModelCapabilities(p.cfg, model)
	// Effort levels are per-model: only 4.7+ serves xhigh, and models still on
	// the manual budget path serve none of the extended levels.
	capabilities.ReasoningEfforts = p.anthropicModelReasoningEfforts(model)
	return capabilities
}

func (p *AnthropicProvider) ListModels(ctx context.Context) ([]string, error) {
	if p == nil {
		return nil, providerUnavailableError(anthropicauth.DefaultProviderName, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	candidates, err := p.accountCandidates(ctx, GenerateRequest{Scenario: CallScenarioInternal})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	models := make([]string, 0)
	var lastErr error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		accountModels, _, requestErr := p.listModelsWithClient(ctx, candidate.client)
		if requestErr != nil {
			lastErr = requestErr
			if shouldTryNextAnthropicAccount(ctx, requestErr) {
				continue
			}
			// The Messages API does not require a model catalog. Third-party
			// Anthropic-compatible endpoints (DeepSeek's /anthropic surface
			// among them) commonly serve /v1/messages without /v1/models, so a
			// 404 there means "no catalog", not "unreachable". Fall back to the
			// same origin's OpenAI-compatible catalog with the same key before
			// reporting the failure.
			if fallbackModels, ok := p.listSameOriginOpenAIModels(ctx, requestErr); ok {
				return fallbackModels, nil
			}
			return nil, sanitizeAnthropicError(ctx, p.cfg.Name, requestErr)
		}
		for _, model := range accountModels {
			if _, exists := seen[model]; exists {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		if lastErr != nil {
			if fallbackModels, ok := p.listSameOriginOpenAIModels(ctx, lastErr); ok {
				return fallbackModels, nil
			}
			return nil, sanitizeAnthropicError(ctx, p.cfg.Name, lastErr)
		}
		if p.cfg.Model != "" {
			models = append(models, p.cfg.Model)
		}
	}
	return models, nil
}

func (p *AnthropicProvider) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	if p == nil {
		return nil, providerUnavailableError(anthropicauth.DefaultProviderName, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.cfg.Model
	}
	// Gate on the chosen model, not the provider baseline: xhigh needs 4.7+ and
	// the manual budget path serves neither xhigh nor max.
	effort, err := normalizeReasoningEffortForCapabilities(
		req.ReasoningEffort,
		CapabilitiesForModel(p.Capabilities(), p.ModelCapabilities(model)),
		p.cfg.Name,
	)
	if err != nil {
		return nil, err
	}
	candidates, err := p.accountCandidates(ctx, req)
	if err != nil {
		return nil, err
	}
	messages, system, durableMessages := anthropicMessages(req.Messages, req.SystemPrompt, model)
	if len(messages) == 0 {
		messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock("Continue.")))
		durableMessages = len(messages)
	}
	maxTokens := p.cfg.MaxTokens
	if req.MaxOutputTokens > 0 && (maxTokens <= 0 || req.MaxOutputTokens < maxTokens) {
		maxTokens = req.MaxOutputTokens
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages:  messages,
		System:    system,
		Tools:     anthropicTools(req.Tools),
	}
	thinkingMode, err := p.applyThinkingConfig(&params, model, effort, req.ReasoningBudgetTokens)
	if err != nil {
		return nil, err
	}
	applyAnthropicPromptCaching(&params, durableMessages, anthropicCacheTTL(p.cfg.CacheTTL))

	out := make(chan Event, 8)
	go func() {
		defer close(out)
		var lastErr error
		for _, candidate := range candidates {
			if ctx.Err() != nil {
				return
			}
			lastErr = nil
			dispatchEmitted := false
			emitDispatch := func() bool {
				if dispatchEmitted {
					return true
				}
				dispatchEmitted = emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, candidate.id))
				return dispatchEmitted
			}
			requestParams := params
			if candidate.oauth {
				// OAuth subscription tokens require Claude Code identity as the
				// first system block; API-key candidates must keep the original
				// params so we only rewrite per OAuth attempt.
				requestParams = withAnthropicOAuthClaudeCodeIdentity(params)
			}
			var response *http.Response
			requestOptions := []option.RequestOption{option.WithResponseInto(&response)}
			if thinkingMode == "enabled" && len(requestParams.Tools) > 0 {
				requestOptions = append(requestOptions, option.WithHeaderAdd("anthropic-beta", anthropicInterleavedThinkingBeta))
			}
			stream := candidate.client.Messages.NewStreaming(ctx, requestParams, requestOptions...)
			var acc anthropic.Message
			// Usage accumulates in Anthropic's raw accounting and is projected
			// through anthropicUsageFromUsage after every fold, because deltas
			// overwrite raw fields and cannot be applied to normalized totals.
			var rawUsage anthropic.Usage
			var usage Usage
			var stopReason string
			emittedContent := false
			for stream.Next() {
				event := stream.Current()
				if accumulateErr := acc.Accumulate(event); accumulateErr != nil {
					lastErr = accumulateErr
					break
				}
				switch typed := event.AsAny().(type) {
				case anthropic.MessageStartEvent:
					rawUsage = typed.Message.Usage
					usage = anthropicUsageFromUsage(rawUsage)
				case anthropic.ContentBlockDeltaEvent:
					switch delta := typed.Delta.AsAny().(type) {
					case anthropic.TextDelta:
						if delta.Text == "" {
							break
						}
						if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "text", Text: delta.Text}) {
							_ = stream.Close()
							return
						}
						emittedContent = true
					case anthropic.ThinkingDelta:
						// Extended thinking is a summary of intent, not an answer, so it
						// deliberately does not set emittedContent: a turn that only thought
						// must still count as having produced nothing.
						if delta.Thinking == "" {
							break
						}
						if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "reasoning", Text: delta.Thinking, BlockType: ContentBlockTypeThinking, BlockIndex: typed.Index}) {
							_ = stream.Close()
							return
						}
					case anthropic.InputJSONDelta:
						// A preview of the tool arguments, not an answer, so like thinking it
						// does not set emittedContent. The accumulated message already holds
						// the block's id and name from content_block_start.
						if delta.PartialJSON == "" {
							break
						}
						toolDelta, ok := anthropicToolCallDeltaEvent(acc, typed.Index, delta.PartialJSON)
						if !ok {
							break
						}
						if !emitDispatch() || !emitProviderEvent(ctx, out, toolDelta) {
							_ = stream.Close()
							return
						}
					}
				case anthropic.ContentBlockStopEvent:
					if blockEvent, ok := anthropicContentBlockEvent(acc, typed.Index, model); ok {
						if !emitDispatch() || !emitProviderEvent(ctx, out, blockEvent) {
							_ = stream.Close()
							return
						}
						emittedContent = true
					}
					if toolEvent, ok := anthropicToolCallEvent(acc, typed.Index); ok {
						if !emitDispatch() || !emitProviderEvent(ctx, out, toolEvent) {
							_ = stream.Close()
							return
						}
						emittedContent = true
					}
				case anthropic.MessageDeltaEvent:
					applyAnthropicDeltaUsage(&rawUsage, typed.Usage)
					usage = anthropicUsageFromUsage(rawUsage)
					if typed.Delta.StopReason != "" {
						stopReason = string(typed.Delta.StopReason)
					}
				}
			}
			if streamErr := stream.Err(); streamErr != nil {
				lastErr = streamErr
			}
			_ = stream.Close()
			if response == nil {
				if apiErr := anthropicAPIError(lastErr); apiErr != nil {
					response = apiErr.Response
				}
			}
			quota := anthropicQuotaSnapshot(p.cfg.Name, candidate.id, response, p.now())
			p.recordAccountQuota(ctx, quota)
			if lastErr != nil {
				if ctx.Err() != nil {
					return
				}
				p.recordAccountAttempt(ctx, candidate.id, false, anthropicResponseStatus(response), lastErr)
				if !emittedContent && shouldTryNextAnthropicAccount(ctx, lastErr) {
					continue
				}
				if !emitDispatch() {
					return
				}
				_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeAnthropicError(ctx, p.cfg.Name, lastErr).Error()})
				return
			}
			p.recordAccountAttempt(ctx, candidate.id, true, anthropicResponseStatus(response), nil)
			if !emitDispatch() {
				return
			}
			if usage != (Usage{}) && !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
				return
			}
			if stopReason == "" {
				stopReason = string(acc.StopReason)
			}
			_ = emitProviderEvent(ctx, out, Event{Type: "done", Done: true, StopReason: stopReason})
			return
		}
		if lastErr != nil && ctx.Err() == nil {
			_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeAnthropicError(ctx, p.cfg.Name, lastErr).Error()})
		}
	}()
	return out, nil
}

func (p *AnthropicProvider) applyThinkingConfig(params *anthropic.MessageNewParams, model, effort string, exactBudget int64) (string, error) {
	if params == nil || (effort == "" && exactBudget <= 0) {
		return "", nil
	}
	support := p.anthropicThinkingSupportForModel(model)
	if support.Known && !support.Supported {
		return "", fmt.Errorf("%w by %s provider (model %q does not support thinking)", ErrReasoningEffortUnsupported, p.cfg.Name, model)
	}
	if exactBudget > 0 {
		if !support.Enabled {
			return "", fmt.Errorf("%w by %s provider (model %q does not support enabled thinking)", ErrReasoningEffortUnsupported, p.cfg.Name, model)
		}
		if exactBudget < 1024 || exactBudget >= params.MaxTokens {
			return "", fmt.Errorf("Anthropic thinking budget_tokens must be at least 1024 and less than max_tokens")
		}
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(exactBudget)
		return "enabled", nil
	}
	if support.Adaptive {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(effort)}
		return "adaptive", nil
	}
	if support.Enabled {
		budget, err := anthropicThinkingBudget(params.MaxTokens, effort)
		if err != nil {
			return "", err
		}
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
		return "enabled", nil
	}
	return "", fmt.Errorf("%w by %s provider (model %q has no supported thinking mode)", ErrReasoningEffortUnsupported, p.cfg.Name, model)
}

func anthropicThinkingBudget(maxTokens int64, effort string) (int64, error) {
	if maxTokens < 2048 {
		return 0, fmt.Errorf("Anthropic thinking requires max_tokens of at least 2048")
	}
	var budget int64
	switch effort {
	case "low":
		budget = maxTokens / 4
	case "medium":
		budget = maxTokens / 2
	case "high":
		budget = maxTokens * 3 / 4
	// xhigh and max normally reach the adaptive path instead, which forwards the
	// effort verbatim. They land here only when catalog metadata reports a
	// model as enabled-but-not-adaptive after the picker already offered them,
	// so map them rather than failing the request.
	case "xhigh":
		budget = maxTokens * 7 / 8
	case "max":
		budget = maxTokens - 1024
	default:
		return 0, fmt.Errorf("invalid Anthropic thinking effort %q", effort)
	}
	if budget < 1024 {
		budget = 1024
	}
	if maximum := maxTokens - 1024; budget > maximum {
		budget = maximum
	}
	if budget < 1024 {
		return 0, fmt.Errorf("Anthropic thinking requires at least 1024 budget tokens")
	}
	return budget, nil
}

func (p *AnthropicProvider) anthropicThinkingSupportForModel(model string) anthropicThinkingSupport {
	model = strings.TrimSpace(model)
	if p != nil {
		p.thinkingSupportMu.RLock()
		support, ok := p.thinkingSupport[model]
		p.thinkingSupportMu.RUnlock()
		if ok {
			return support
		}
	}
	return fallbackAnthropicThinkingSupport(model)
}

func fallbackAnthropicThinkingSupport(model string) anthropicThinkingSupport {
	if anthropicModelAtLeast(model, 4, 6) {
		return anthropicThinkingSupport{Adaptive: true, Enabled: true}
	}
	// Claude 4.5 and older default to manual enabled thinking. This conservative
	// fallback also works for private aliases until Models API metadata arrives.
	return anthropicThinkingSupport{Enabled: true}
}

// anthropicModelAtLeast reports whether a model name parses to a family version
// at or above major.minor. Names that do not parse — private relay aliases, for
// example — report false so callers keep their conservative default instead of
// assuming a capability the model may not have.
func anthropicModelAtLeast(model string, major, minor int) bool {
	parsedMajor, parsedMinor, ok := anthropicModelVersion(model)
	if !ok {
		return false
	}
	return parsedMajor > major || (parsedMajor == major && parsedMinor >= minor)
}

func anthropicModelVersion(model string) (int, int, bool) {
	match := anthropicModelVersionPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(model)))
	if len(match) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0, false
	}
	minor := 0
	if len(match) > 2 && match[2] != "" {
		minor, _ = strconv.Atoi(match[2])
	}
	return major, minor, true
}

// anthropicModelReasoningEfforts derives the effort levels one model accepts
// from its resolved thinking support. Adaptive models forward the effort
// verbatim to output_config, so they serve the full range; models on the manual
// budget path only serve the levels anthropicThinkingBudget maps.
func (p *AnthropicProvider) anthropicModelReasoningEfforts(model string) []string {
	support := p.anthropicThinkingSupportForModel(model)
	if support.Known && !support.Supported {
		return nil
	}
	if support.Adaptive {
		if anthropicModelAtLeast(model, 4, 7) {
			return []string{"low", "medium", "high", "xhigh", "max"}
		}
		// 4.6 serves adaptive effort but not xhigh.
		return []string{"low", "medium", "high", "max"}
	}
	if support.Enabled {
		return []string{"low", "medium", "high"}
	}
	return nil
}

func (p *AnthropicProvider) rememberAnthropicThinkingSupport(models []anthropic.ModelInfo) {
	if p == nil || len(models) == 0 {
		return
	}
	p.thinkingSupportMu.Lock()
	defer p.thinkingSupportMu.Unlock()
	if p.thinkingSupport == nil {
		p.thinkingSupport = make(map[string]anthropicThinkingSupport)
	}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		thinking := model.Capabilities.Thinking
		if id == "" || !model.Capabilities.JSON.Thinking.Valid() && !thinking.JSON.Supported.Valid() && !thinking.JSON.Types.Valid() {
			continue
		}
		p.thinkingSupport[id] = anthropicThinkingSupport{
			Known:     true,
			Supported: thinking.Supported,
			Adaptive:  thinking.Types.Adaptive.Supported,
			Enabled:   thinking.Types.Enabled.Supported,
		}
	}
}

// anthropicUsageFromUsage projects Anthropic's disjoint accounting — where
// input_tokens excludes everything read from or written to the prompt cache —
// onto the convention every other adapter reports: InputTokens is the whole
// prompt and CachedInputTokens the subset served from cache. Consumers
// (pricing, context calibration) subtract the cached share, so passing the
// disjoint figures through unchanged double-counted warm-cache turns.
func anthropicUsageFromUsage(usage anthropic.Usage) Usage {
	return Usage{
		InputTokens:       usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens,
		OutputTokens:      usage.OutputTokens,
		CachedInputTokens: usage.CacheReadInputTokens,
		ReasoningTokens:   usage.OutputTokensDetails.ThinkingTokens,
	}
}

// applyAnthropicDeltaUsage folds a message_delta usage report into the raw
// Anthropic accounting. Deltas carry cumulative totals for whichever fields
// they include, so a present field overwrites; the fold stays in raw terms
// because a delta that updates input_tokens without the cache fields cannot
// be applied to an already-normalized total.
func applyAnthropicDeltaUsage(usage *anthropic.Usage, delta anthropic.MessageDeltaUsage) {
	if delta.JSON.InputTokens.Valid() {
		usage.InputTokens = delta.InputTokens
	}
	if delta.JSON.OutputTokens.Valid() {
		usage.OutputTokens = delta.OutputTokens
	}
	if delta.JSON.CacheReadInputTokens.Valid() {
		usage.CacheReadInputTokens = delta.CacheReadInputTokens
	}
	if delta.JSON.CacheCreationInputTokens.Valid() {
		usage.CacheCreationInputTokens = delta.CacheCreationInputTokens
	}
	if delta.JSON.OutputTokensDetails.Valid() {
		usage.OutputTokensDetails.ThinkingTokens = delta.OutputTokensDetails.ThinkingTokens
	}
}

func emitAnthropicToolCall(out chan<- Event, message anthropic.Message, index int64) {
	if event, ok := anthropicToolCallEvent(message, index); ok {
		out <- event
	}
}

// NewAnthropicThinkingContentBlock creates an internal signed thinking block.
// Provider-private state remains opaque to callers and is omitted from public
// ContentBlock JSON.
func NewAnthropicThinkingContentBlock(model, thinking, signature string) ContentBlock {
	state, _ := json.Marshal(anthropicReasoningState{
		Version:   anthropicReasoningStateVersion,
		Provider:  anthropicReasoningStateProvider,
		Model:     strings.TrimSpace(model),
		Signature: signature,
	})
	return ContentBlock{Type: ContentBlockTypeThinking, ReasoningText: thinking, ProviderState: state}
}

// NewAnthropicRedactedThinkingContentBlock creates an internal encrypted
// reasoning block that can be replayed without exposing its data publicly.
func NewAnthropicRedactedThinkingContentBlock(model, data string) ContentBlock {
	state, _ := json.Marshal(anthropicReasoningState{
		Version:  anthropicReasoningStateVersion,
		Provider: anthropicReasoningStateProvider,
		Model:    strings.TrimSpace(model),
		Data:     data,
	})
	return ContentBlock{Type: ContentBlockTypeRedactedThinking, ProviderState: state}
}

// AnthropicReasoningContentBlockState decodes the opaque state carried by an
// Anthropic thinking or redacted_thinking block.
func AnthropicReasoningContentBlockState(block ContentBlock) (model, signature, data string, ok bool) {
	if block.Type != ContentBlockTypeThinking && block.Type != ContentBlockTypeRedactedThinking {
		return "", "", "", false
	}
	var state anthropicReasoningState
	if len(block.ProviderState) == 0 || json.Unmarshal(block.ProviderState, &state) != nil {
		return "", "", "", false
	}
	if state.Version != anthropicReasoningStateVersion || state.Provider != anthropicReasoningStateProvider {
		return "", "", "", false
	}
	return strings.TrimSpace(state.Model), state.Signature, state.Data, true
}

func anthropicContentBlockEvent(message anthropic.Message, index int64, model string) (Event, bool) {
	if index < 0 || index >= int64(len(message.Content)) {
		return Event{}, false
	}
	block := message.Content[index]
	var content ContentBlock
	switch block.Type {
	case "text":
		if block.Text == "" {
			return Event{}, false
		}
		content = ContentBlock{Type: "text", Text: block.Text}
	case "tool_use":
		if block.ID == "" || block.Name == "" {
			return Event{}, false
		}
		input := block.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		content = ContentBlock{Type: "tool_use", ToolUseID: block.ID, ToolName: block.Name, Input: input}
	case ContentBlockTypeThinking:
		if block.Signature == "" {
			return Event{}, false
		}
		content = NewAnthropicThinkingContentBlock(model, block.Thinking, block.Signature)
	case ContentBlockTypeRedactedThinking:
		if block.Data == "" {
			return Event{}, false
		}
		content = NewAnthropicRedactedThinkingContentBlock(model, block.Data)
	default:
		return Event{}, false
	}
	return Event{Type: "content_block", ContentBlock: &content, BlockType: content.Type, BlockIndex: index}, true
}

func anthropicToolCallDeltaEvent(message anthropic.Message, index int64, partial string) (Event, bool) {
	if index < 0 || index >= int64(len(message.Content)) {
		return Event{}, false
	}
	block := message.Content[index]
	if block.Type != "tool_use" || block.ID == "" || block.Name == "" {
		return Event{}, false
	}
	return Event{Type: "tool_call_delta", Text: partial, ToolCall: &ToolCall{ID: block.ID, Name: block.Name}, BlockIndex: index}, true
}

func anthropicToolCallEvent(message anthropic.Message, index int64) (Event, bool) {
	if index < 0 || index >= int64(len(message.Content)) {
		return Event{}, false
	}
	block := message.Content[index]
	if block.Type != "tool_use" || block.ID == "" || block.Name == "" {
		return Event{}, false
	}
	input := block.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	return Event{Type: "tool_call", ToolCall: &ToolCall{ID: block.ID, Name: block.Name, Input: input}}, true
}

// withAnthropicOAuthClaudeCodeIdentity returns params whose first system text
// block is the Claude Code identity string. The original params value is not
// mutated (System is replaced with a new slice).
func withAnthropicOAuthClaudeCodeIdentity(params anthropic.MessageNewParams) anthropic.MessageNewParams {
	if hasAnthropicClaudeCodeIdentity(params.System) {
		return params
	}
	system := make([]anthropic.TextBlockParam, 0, len(params.System)+1)
	system = append(system, anthropic.TextBlockParam{Text: anthropicauth.ClaudeCodeIdentity})
	system = append(system, params.System...)
	params.System = system
	return params
}

func hasAnthropicClaudeCodeIdentity(system []anthropic.TextBlockParam) bool {
	if len(system) == 0 {
		return false
	}
	return strings.TrimSpace(system[0].Text) == anthropicauth.ClaudeCodeIdentity
}

// anthropicMessages returns the converted conversation, the system blocks, and
// the count of leading durable messages.
//
// System-role messages are hoisted into the system blocks except when they are
// turn controls. Anthropic's prompt-cache hierarchy is tools → system →
// messages, so a per-turn control hoisted into the system array changes the
// system blocks every turn and invalidates the cached conversation history
// behind them. Turn controls are therefore emitted as standalone user messages
// after the durable conversation (the Messages API combines consecutive
// same-role messages into one turn), and the durable count lets the caller
// place the message cache breakpoint on content that the next turn replays
// verbatim.
func anthropicMessages(messages []Message, systemPrompt, model string) ([]anthropic.MessageParam, []anthropic.TextBlockParam, int) {
	out := make([]anthropic.MessageParam, 0, len(messages))
	controls := make([]anthropic.MessageParam, 0, 2)
	system := make([]anthropic.TextBlockParam, 0, 1)
	if strings.TrimSpace(systemPrompt) != "" {
		system = append(system, anthropic.TextBlockParam{Text: systemPrompt})
	}
	for _, message := range messages {
		blocks := normalizeContentBlocks(message)
		if len(blocks) == 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			content := anthropicContentBlocks(blocks, true, model)
			if len(content) > 0 {
				out = append(out, anthropic.NewAssistantMessage(content...))
			}
		case "system":
			if message.TurnControl {
				// Collected separately instead of merged into the previous
				// user message: the control must stay a distinct trailing
				// message so the cache breakpoint can end before it.
				if content := anthropicContentBlocks(blocks, false, model); len(content) > 0 {
					controls = append(controls, anthropic.NewUserMessage(content...))
				}
				continue
			}
			content := strings.TrimSpace(contentBlocksText(blocks))
			if content != "" {
				system = append(system, anthropic.TextBlockParam{Text: content})
			}
		default:
			content := anthropicContentBlocks(blocks, false, model)
			if len(content) > 0 {
				out = appendAnthropicUserContent(out, content)
			}
		}
	}
	durable := len(out)
	out = append(out, controls...)
	return out, system, durable
}

// appendAnthropicUserContent folds user content into the preceding user message
// instead of emitting a second one.
//
// The Messages API requires every tool_result answering one assistant turn to
// live in the single user message that follows it. Autoto stores one message per
// tool result, so a turn with parallel tool calls is persisted as N separate
// user messages; replaying that verbatim is rejected with "Invalid message
// sequence: tool_use and tool_result blocks must be correctly paired and
// ordered", which poisons the conversation permanently because every later
// request replays the same history.
//
// Merging is done on the emitted slice rather than on the input messages so that
// entries which vanish during conversion — hoisted system messages, assistant
// messages whose only block was thinking from another model — still leave the
// surrounding user messages adjacent. Block order inside the merged message is
// preserved: tool_result blocks do not have to lead, only to share one message.
func appendAnthropicUserContent(out []anthropic.MessageParam, content []anthropic.ContentBlockParamUnion) []anthropic.MessageParam {
	if last := len(out) - 1; last >= 0 && out[last].Role == anthropic.MessageParamRoleUser {
		out[last].Content = append(out[last].Content, content...)
		return out
	}
	return append(out, anthropic.NewUserMessage(content...))
}

func anthropicContentBlocks(blocks []ContentBlock, assistant bool, model string) []anthropic.ContentBlockParamUnion {
	out := make([]anthropic.ContentBlockParamUnion, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ContentBlockTypeThinking:
			if !assistant {
				continue
			}
			sourceModel, signature, _, ok := AnthropicReasoningContentBlockState(block)
			if !ok || signature == "" || (sourceModel != "" && sourceModel != strings.TrimSpace(model)) {
				continue
			}
			out = append(out, anthropic.NewThinkingBlock(signature, block.ReasoningText))
		case ContentBlockTypeRedactedThinking:
			if !assistant {
				continue
			}
			sourceModel, _, data, ok := AnthropicReasoningContentBlockState(block)
			if !ok || data == "" || (sourceModel != "" && sourceModel != strings.TrimSpace(model)) {
				continue
			}
			out = append(out, anthropic.NewRedactedThinkingBlock(data))
		case "tool_use":
			input := any(map[string]any{})
			if len(block.Input) > 0 {
				input = json.RawMessage(block.Input)
			}
			if block.ToolUseID != "" && block.ToolName != "" {
				out = append(out, anthropic.NewToolUseBlock(block.ToolUseID, input, block.ToolName))
			}
		case "tool_result":
			if block.ToolUseID != "" {
				out = append(out, anthropic.NewToolResultBlock(block.ToolUseID, block.Output, block.IsError))
			}
		case "image":
			if len(block.Data) > 0 {
				mimeType := strings.TrimSpace(block.MIMEType)
				if mimeType == "" {
					mimeType = "image/png"
				}
				out = append(out, anthropic.NewImageBlockBase64(mimeType, base64.StdEncoding.EncodeToString(block.Data)))
				continue
			}
			name := strings.TrimSpace(block.Filename)
			if name == "" {
				name = "image"
			}
			out = append(out, anthropic.NewTextBlock("[图片附件 "+name+" 已上传；当前缺少可传递的图片数据。]"))
		default:
			text := strings.TrimSpace(block.Text)
			if text != "" {
				out = append(out, anthropic.NewTextBlock(text))
			}
		}
	}
	return out
}

func anthropicTools(specs []ToolSpec) []anthropic.ToolUnionParam {
	if len(specs) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(specs))
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		schema := anthropicToolInputSchema(spec.Schema)
		tool := anthropic.ToolUnionParamOfTool(schema, name)
		if tool.OfTool != nil && strings.TrimSpace(spec.Description) != "" {
			tool.OfTool.Description = param.NewOpt(spec.Description)
		}
		out = append(out, tool)
	}
	return out
}

const anthropicPromptCacheMinBytes = 4096

// anthropicCacheTTL maps the provider CacheTTL setting to the SDK constant.
// Only "1h" opts into the long-lived cache (2x write cost instead of 1.25x);
// everything else stays on the 5m default. The 1h option exists for runs that
// can sit idle beyond five minutes between turns — approval waits being the
// common case — where an expired cache re-writes the whole prefix at full
// price on the next turn.
func anthropicCacheTTL(configured string) anthropic.CacheControlEphemeralTTL {
	if strings.TrimSpace(strings.ToLower(configured)) == "1h" {
		return anthropic.CacheControlEphemeralTTLTTL1h
	}
	return anthropic.CacheControlEphemeralTTLTTL5m
}

// applyAnthropicPromptCaching marks the cache breakpoints: last system block,
// last tool, and the last durable message. Trailing turn-control messages
// change every turn, so a breakpoint on them would be written but never read;
// durableMessages bounds the search to content the next turn replays verbatim.
func applyAnthropicPromptCaching(params *anthropic.MessageNewParams, durableMessages int, ttl anthropic.CacheControlEphemeralTTL) {
	if params == nil || anthropicPromptCacheFootprint(*params) < anthropicPromptCacheMinBytes {
		return
	}
	cacheControl := anthropic.NewCacheControlEphemeralParam()
	cacheControl.TTL = ttl
	if len(params.System) > 0 {
		params.System[len(params.System)-1].CacheControl = cacheControl
	}
	setLastAnthropicToolCache(params.Tools, cacheControl)
	if durableMessages > len(params.Messages) {
		durableMessages = len(params.Messages)
	}
	setLastAnthropicMessageCache(params.Messages[:durableMessages], cacheControl)
}

func anthropicPromptCacheFootprint(params anthropic.MessageNewParams) int {
	total := 0
	for _, block := range params.System {
		total += len(strings.TrimSpace(block.Text))
	}
	total += marshaledAnthropicParamLen(params.Tools)
	total += marshaledAnthropicParamLen(params.Messages)
	return total
}

func marshaledAnthropicParamLen(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(data)
}

func setLastAnthropicToolCache(tools []anthropic.ToolUnionParam, cacheControl anthropic.CacheControlEphemeralParam) bool {
	for i := len(tools) - 1; i >= 0; i-- {
		if control := tools[i].GetCacheControl(); control != nil {
			*control = cacheControl
			return true
		}
	}
	return false
}

func setLastAnthropicMessageCache(messages []anthropic.MessageParam, cacheControl anthropic.CacheControlEphemeralParam) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if setLastAnthropicContentCache(messages[i].Content, cacheControl) {
			return true
		}
	}
	return false
}

func setLastAnthropicContentCache(blocks []anthropic.ContentBlockParamUnion, cacheControl anthropic.CacheControlEphemeralParam) bool {
	for i := len(blocks) - 1; i >= 0; i-- {
		if control := blocks[i].GetCacheControl(); control != nil {
			*control = cacheControl
			return true
		}
	}
	return false
}

func anthropicToolInputSchema(schema any) anthropic.ToolInputSchemaParam {
	input := anthropic.ToolInputSchemaParam{Properties: map[string]any{}}
	if object, ok := schema.(map[string]any); ok {
		if properties, ok := object["properties"]; ok {
			input.Properties = properties
		}
		if required, ok := stringSlice(object["required"]); ok {
			input.Required = required
		}
	}
	return input
}

func stringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}
