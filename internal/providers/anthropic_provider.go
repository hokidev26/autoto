package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
)

type AnthropicProvider struct {
	cfg               config.ProviderConfig
	store             *anthropicauth.Store
	configErr         error
	telemetry         AccountTelemetry
	quotaTelemetry    AccountQuotaTelemetry
	clock             func() time.Time
	gatewayPolicyMu   sync.RWMutex
	gatewayPolicy     GatewayAccountPolicy
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
	return Capabilities{Tools: true, Streaming: true, ImageInput: true}
}

func (p *AnthropicProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	return configuredModelCapabilities(p.cfg, model)
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
	if _, err := normalizeReasoningEffort(req.ReasoningEffort, false, p.cfg.Name); err != nil {
		return nil, err
	}
	candidates, err := p.accountCandidates(ctx, req)
	if err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	messages, system := anthropicMessages(req.Messages, req.SystemPrompt)
	if len(messages) == 0 {
		messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock("Continue.")))
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
	applyAnthropicPromptCaching(&params)

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
			stream := candidate.client.Messages.NewStreaming(ctx, requestParams, option.WithResponseInto(&response))
			var acc anthropic.Message
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
					usage = anthropicUsageFromUsage(typed.Message.Usage)
				case anthropic.ContentBlockDeltaEvent:
					if delta, ok := typed.Delta.AsAny().(anthropic.TextDelta); ok && delta.Text != "" {
						if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "text", Text: delta.Text}) {
							_ = stream.Close()
							return
						}
						emittedContent = true
					}
				case anthropic.ContentBlockStopEvent:
					if toolEvent, ok := anthropicToolCallEvent(acc, typed.Index); ok {
						if !emitDispatch() || !emitProviderEvent(ctx, out, toolEvent) {
							_ = stream.Close()
							return
						}
						emittedContent = true
					}
				case anthropic.MessageDeltaEvent:
					applyAnthropicDeltaUsage(&usage, typed.Usage)
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

func anthropicUsageFromUsage(usage anthropic.Usage) Usage {
	return Usage{
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		CachedInputTokens: usage.CacheReadInputTokens,
		ReasoningTokens:   usage.OutputTokensDetails.ThinkingTokens,
	}
}

func applyAnthropicDeltaUsage(usage *Usage, delta anthropic.MessageDeltaUsage) {
	if delta.JSON.InputTokens.Valid() {
		usage.InputTokens = delta.InputTokens
	}
	if delta.JSON.OutputTokens.Valid() {
		usage.OutputTokens = delta.OutputTokens
	}
	if delta.JSON.CacheReadInputTokens.Valid() {
		usage.CachedInputTokens = delta.CacheReadInputTokens
	}
	if delta.JSON.OutputTokensDetails.Valid() {
		usage.ReasoningTokens = delta.OutputTokensDetails.ThinkingTokens
	}
}

func emitAnthropicToolCall(out chan<- Event, message anthropic.Message, index int64) {
	if event, ok := anthropicToolCallEvent(message, index); ok {
		out <- event
	}
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

func anthropicMessages(messages []Message, systemPrompt string) ([]anthropic.MessageParam, []anthropic.TextBlockParam) {
	out := make([]anthropic.MessageParam, 0, len(messages))
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
			content := anthropicContentBlocks(blocks)
			if len(content) > 0 {
				out = append(out, anthropic.NewAssistantMessage(content...))
			}
		case "system":
			content := strings.TrimSpace(contentBlocksText(blocks))
			if content != "" {
				system = append(system, anthropic.TextBlockParam{Text: content})
			}
		default:
			content := anthropicContentBlocks(blocks)
			if len(content) > 0 {
				out = append(out, anthropic.NewUserMessage(content...))
			}
		}
	}
	return out, system
}

func anthropicContentBlocks(blocks []ContentBlock) []anthropic.ContentBlockParamUnion {
	out := make([]anthropic.ContentBlockParamUnion, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
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

func applyAnthropicPromptCaching(params *anthropic.MessageNewParams) {
	if params == nil || anthropicPromptCacheFootprint(*params) < anthropicPromptCacheMinBytes {
		return
	}
	cacheControl := anthropic.NewCacheControlEphemeralParam()
	cacheControl.TTL = anthropic.CacheControlEphemeralTTLTTL5m
	if len(params.System) > 0 {
		params.System[len(params.System)-1].CacheControl = cacheControl
	}
	setLastAnthropicToolCache(params.Tools, cacheControl)
	setLastAnthropicMessageCache(params.Messages, cacheControl)
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
