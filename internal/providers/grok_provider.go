package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"autoto/internal/config"
	"autoto/internal/grokauth"
	"autoto/internal/subscriptionauth"
)

const (
	grokProductionBaseURL = "https://cli-chat-proxy.grok.com/v1"
	grokClientVersion     = "0.2.93"
	grokMaxResponseBytes  = 16 << 20
)

type GrokProvider struct {
	cfg       config.ProviderConfig
	accounts  *subscriptionProviderAccounts
	client    *http.Client
	baseURL   string
	configErr error
	refresh   subscriptionRefreshFunc
}

func NewGrokProvider(cfg config.ProviderConfig) *GrokProvider {
	cfg = normalizeGrokProviderConfig(cfg)
	apiConfig := cfg
	apiConfig.RequestHeaders = nil
	apiConfig.UserAgent = ""
	client, clientErr := providerHTTPClient(apiConfig, 5*time.Minute)
	refreshConfig := apiConfig
	refreshClient, refreshClientErr := providerHTTPClient(refreshConfig, 30*time.Second)
	provider := &GrokProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderGrok),
		client:    client,
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		configErr: errors.Join(validateProviderRuntimeConfig(cfg), validateGrokProductionBaseURL(cfg.BaseURL), clientErr, refreshClientErr),
	}
	if refreshClient != nil {
		authClient := grokauth.New(refreshClient)
		provider.refresh = func(ctx context.Context, credential subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
			tokens, err := authClient.RefreshTokens(ctx, credential.RefreshToken, credential.TokenEndpoint)
			if err != nil || tokens == nil {
				return subscriptionauth.TokenUpdate{}, err
			}
			return subscriptionauth.TokenUpdate{
				AccessToken:   tokens.AccessToken,
				RefreshToken:  tokens.RefreshToken,
				IDToken:       tokens.IDToken,
				TokenType:     tokens.TokenType,
				ExpiresAt:     tokens.ExpiresAt,
				Email:         tokens.Email,
				Subject:       tokens.Subject,
				TokenEndpoint: credential.TokenEndpoint,
			}, nil
		}
	}
	return provider
}

func newGrokProviderForTest(cfg config.ProviderConfig, client *http.Client, baseURL string) *GrokProvider {
	cfg = normalizeGrokProviderConfig(cfg)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &GrokProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderGrok),
		client:    client,
		baseURL:   cfg.BaseURL,
		configErr: validateSubscriptionTestBaseURL(cfg.BaseURL),
	}
}

func normalizeGrokProviderConfig(cfg config.ProviderConfig) config.ProviderConfig {
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = config.ProviderTypeGrok
	}
	if strings.TrimSpace(cfg.Type) == "" {
		cfg.Type = config.ProviderTypeGrok
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = grokProductionBaseURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "grok-4.5"
	}
	return cfg
}

func validateGrokProductionBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "cli-chat-proxy.grok.com") || strings.TrimRight(parsed.EscapedPath(), "/") != "/v1" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Grok Base URL only allows the official HTTPS CLI chat proxy")
	}
	return nil
}

func (p *GrokProvider) Name() string {
	if p == nil {
		return config.ProviderTypeGrok
	}
	return p.cfg.Name
}

func (p *GrokProvider) Configured() bool {
	return p != nil && p.configErr == nil && p.accounts.configured()
}

func (p *GrokProvider) ConfiguredForScenario(scenario CallScenario) bool {
	return p != nil && p.configErr == nil && p.accounts.configuredForScenario(scenario)
}

func (p *GrokProvider) AvailableForScenario(ctx context.Context, availability ScenarioAvailability) bool {
	return p != nil && p.configErr == nil && p.accounts.availableForScenario(ctx, availability)
}

func (p *GrokProvider) SetAccountTelemetry(telemetry AccountTelemetry) {
	if p != nil {
		p.accounts.setAccountTelemetry(telemetry)
	}
}

func (p *GrokProvider) SetGatewayAccountPolicy(policy GatewayAccountPolicy) {
	if p != nil {
		p.accounts.setGatewayAccountPolicy(policy)
	}
}

func (p *GrokProvider) Capabilities() Capabilities {
	return Capabilities{
		Tools:            true,
		Streaming:        true,
		ImageInput:       true,
		ReasoningEffort:  true,
		ReasoningEfforts: []string{"low", "medium", "high"},
	}
}

func (p *GrokProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	return configuredModelCapabilities(p.cfg, model)
}

func (p *GrokProvider) ListModels(ctx context.Context) ([]string, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeGrok, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	staticModels := configuredSubscriptionModels(p.cfg)
	accounts, accountErr := p.accounts.enabledAccounts()
	if accountErr != nil {
		if len(staticModels) > 0 {
			return staticModels, nil
		}
		return nil, accountErr
	}
	models := make([]string, 0, len(staticModels))
	seen := make(map[string]struct{}, len(staticModels))
	var lastErr error
	for _, item := range accounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		prepared, err := p.accounts.prepareCredential(ctx, item, p.refresh)
		if err != nil {
			lastErr = err
			p.accounts.recordAttempt(ctx, item.ID, false, 0, "credential_unavailable", err)
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
		if err != nil {
			lastErr = newSubscriptionNetworkError(p.cfg.Name, err)
			p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "request_construction_failed", lastErr)
			continue
		}
		p.applyHeaders(request, prepared.Credential, false)
		response, err := p.client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = newSubscriptionNetworkError(p.cfg.Name, err)
			p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "network_error", lastErr)
			continue
		}
		if response.StatusCode >= http.StatusMultipleChoices {
			status := response.StatusCode
			response.Body.Close()
			lastErr = newSubscriptionHTTPError(p.cfg.Name, status, "models_request_failed")
			p.accounts.recordAttempt(ctx, prepared.ID, false, status, "models_request_failed", lastErr)
			continue
		}
		accountModels, parseErr := parseSubscriptionModelList(response.Body)
		response.Body.Close()
		if parseErr != nil {
			lastErr = newSubscriptionNetworkError(p.cfg.Name, parseErr)
			p.accounts.recordAttempt(ctx, prepared.ID, false, response.StatusCode, "models_response_invalid", lastErr)
			continue
		}
		p.accounts.recordAttempt(ctx, prepared.ID, true, response.StatusCode, "", nil)
		for _, model := range accountModels {
			appendUniqueModel(&models, seen, model)
		}
	}
	for _, model := range staticModels {
		appendUniqueModel(&models, seen, model)
	}
	if len(models) > 0 {
		return models, nil
	}
	if lastErr != nil {
		return nil, sanitizeSubscriptionError(ctx, p.cfg.Name, lastErr)
	}
	return nil, providerUnavailableError(p.cfg.Name, "models are unavailable")
}

func (p *GrokProvider) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeGrok, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	reasoningEffort, err := normalizeReasoningEffortForCapabilities(req.ReasoningEffort, p.Capabilities(), p.cfg.Name)
	if err != nil {
		return nil, err
	}
	accounts, err := p.accounts.accountsForRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.cfg.Model
	}
	payload := buildGrokResponsesPayload(req, model, reasoningEffort)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "request payload could not be constructed")
	}

	out := make(chan Event, 8)
	go func() {
		defer close(out)
		var lastErr error
		for _, item := range accounts {
			if ctx.Err() != nil {
				return
			}
			prepared, prepareErr := p.accounts.prepareCredential(ctx, item, p.refresh)
			if prepareErr != nil {
				if ctx.Err() != nil {
					return
				}
				lastErr = prepareErr
				p.accounts.recordAttempt(ctx, item.ID, false, 0, "credential_unavailable", prepareErr)
				continue
			}
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(data))
			if requestErr != nil {
				lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
				p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "request_construction_failed", lastErr)
				continue
			}
			p.applyHeaders(request, prepared.Credential, true)
			response, requestErr := p.client.Do(request)
			if requestErr != nil {
				if ctx.Err() != nil {
					return
				}
				lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
				p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "network_error", lastErr)
				if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
					continue
				}
				p.emitGrokFinalError(ctx, out, model, prepared.ID, lastErr)
				return
			}
			if response.StatusCode >= http.StatusMultipleChoices {
				status := response.StatusCode
				response.Body.Close()
				lastErr = newSubscriptionHTTPError(p.cfg.Name, status, "model_request_failed")
				p.accounts.recordAttempt(ctx, prepared.ID, false, status, "model_request_failed", lastErr)
				if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
					continue
				}
				p.emitGrokFinalError(ctx, out, model, prepared.ID, lastErr)
				return
			}
			dispatchEmitted := false
			emitDispatch := func() bool {
				if dispatchEmitted {
					return true
				}
				dispatchEmitted = emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, prepared.ID))
				return dispatchEmitted
			}
			outcome := handleGrokResponsesStream(ctx, out, response.Body, emitDispatch, p.cfg.Name)
			response.Body.Close()
			if ctx.Err() != nil {
				return
			}
			if outcome.err != nil {
				lastErr = outcome.err
				p.accounts.recordAttempt(ctx, prepared.ID, false, response.StatusCode, outcome.code, outcome.err)
				if shouldTryNextSubscriptionAccount(ctx, outcome.err, outcome.emittedContent) {
					continue
				}
				if !emitDispatch() {
					return
				}
				_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, outcome.err).Error()})
				return
			}
			p.accounts.recordAttempt(ctx, prepared.ID, true, response.StatusCode, "", nil)
			return
		}
		if lastErr == nil {
			lastErr = providerUnavailableError(p.cfg.Name, "no usable subscription account")
		}
		if ctx.Err() == nil {
			_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, lastErr).Error()})
		}
	}()
	return out, nil
}

func (p *GrokProvider) emitGrokFinalError(ctx context.Context, out chan<- Event, model, accountID string, err error) {
	if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, accountID)) {
		return
	}
	_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, err).Error()})
}

func (p *GrokProvider) applyHeaders(request *http.Request, credential subscriptionauth.Credential, streaming bool) {
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential.AccessToken))
	if streaming {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	request.Header.Set("x-grok-client-version", grokClientVersion)
	request.Header.Set("User-Agent", "xai-grok-workspace/"+grokClientVersion)
}

func buildGrokResponsesPayload(req GenerateRequest, model, reasoningEffort string) map[string]any {
	payload := map[string]any{"model": model, "stream": true}
	if strings.TrimSpace(req.SystemPrompt) != "" {
		payload["instructions"] = req.SystemPrompt
	}
	if req.MaxOutputTokens > 0 {
		payload["max_output_tokens"] = req.MaxOutputTokens
	}
	if reasoningEffort != "" {
		payload["reasoning"] = map[string]any{"effort": reasoningEffort}
	}
	if len(req.Tools) > 0 || openAIMessagesRequireStructuredInput(req.Messages) {
		payload["input"] = openAIResponseInput(req.Messages)
	} else {
		input := renderTranscript(req.Messages)
		if input == "" {
			input = "Continue."
		}
		payload["input"] = input
	}
	if tools := openAIToolParams(req.Tools, false); len(tools) > 0 {
		payload["tools"] = tools
	}
	return payload
}

type grokStreamOutcome struct {
	emittedContent bool
	err            error
	code           string
}

type grokStreamEvent struct {
	Type     string           `json:"type"`
	Delta    string           `json:"delta"`
	Text     string           `json:"text"`
	Code     string           `json:"code"`
	Message  string           `json:"message"`
	Item     grokResponseItem `json:"item"`
	Response struct {
		Output []grokResponseItem `json:"output"`
		Usage  grokResponseUsage  `json:"usage"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type grokResponseItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type grokResponseUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	InputTokenDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokenDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u grokResponseUsage) toUsage() Usage {
	return Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedInputTokens: u.InputTokenDetails.CachedTokens, ReasoningTokens: u.OutputTokenDetails.ReasoningTokens}
}

func handleGrokResponsesStream(ctx context.Context, out chan<- Event, body io.Reader, emitDispatch func() bool, provider string) grokStreamOutcome {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), grokMaxResponseBytes)
	emittedCalls := make(map[string]bool)
	sawTextDelta := false
	outcome := grokStreamOutcome{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event grokStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return grokStreamOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, io.ErrUnexpectedEOF), code: "invalid_stream_event"}
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "text", Text: event.Delta}) {
					return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
				}
				sawTextDelta = true
				outcome.emittedContent = true
			}
		case "response.output_text.done":
			if !sawTextDelta && event.Text != "" {
				if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "text", Text: event.Text}) {
					return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
				}
				outcome.emittedContent = true
			}
		case "response.output_item.done":
			if call := grokToolCall(event.Item); call != nil && !emittedCalls[call.ID] {
				emittedCalls[call.ID] = true
				if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "tool_call", ToolCall: call}) {
					return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
				}
				outcome.emittedContent = true
			}
		case "response.completed":
			for _, item := range event.Response.Output {
				call := grokToolCall(item)
				if call == nil || emittedCalls[call.ID] {
					continue
				}
				emittedCalls[call.ID] = true
				if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "tool_call", ToolCall: call}) {
					return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
				}
				outcome.emittedContent = true
			}
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
				}
			}
			if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "done", Done: true}) {
				return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
			}
			return outcome
		case "response.incomplete":
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
				}
			}
			reason := strings.TrimSpace(event.Response.IncompleteDetails.Reason)
			if reason == "" {
				reason = "incomplete"
			}
			if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "done", Done: true, StopReason: reason}) {
				return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
			}
			return outcome
		case "response.failed":
			return grokStreamOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, errors.New("response failed")), code: safeTelemetryCode(event.Response.Error.Code, "response_failed")}
		case "error":
			return grokStreamOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, errors.New("stream error")), code: safeTelemetryCode(event.Code, "stream_error")}
		}
	}
	if ctx.Err() != nil {
		return grokStreamOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
	}
	if err := scanner.Err(); err != nil {
		return grokStreamOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, err), code: "stream_read_error"}
	}
	return grokStreamOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, io.EOF), code: "stream_closed"}
}

func grokToolCall(item grokResponseItem) *ToolCall {
	if item.Type != "function_call" {
		return nil
	}
	id := strings.TrimSpace(item.CallID)
	if id == "" {
		id = strings.TrimSpace(item.ID)
	}
	name := strings.TrimSpace(item.Name)
	if id == "" || name == "" {
		return nil
	}
	return &ToolCall{ID: id, Name: name, Input: openAIToolArgumentsRaw(item.Arguments)}
}

func parseSubscriptionModelList(reader io.Reader) ([]string, error) {
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	decoder := json.NewDecoder(io.LimitReader(reader, grokMaxResponseBytes+1))
	if err := decoder.Decode(&body); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(body.Data)+len(body.Models))
	seen := make(map[string]struct{}, len(body.Data)+len(body.Models))
	for _, item := range body.Data {
		appendUniqueModel(&models, seen, item.ID)
	}
	for _, item := range body.Models {
		model := item.ID
		if strings.TrimSpace(model) == "" {
			model = item.Name
		}
		appendUniqueModel(&models, seen, model)
	}
	return models, nil
}

var _ Provider = (*GrokProvider)(nil)
var _ ConfigurationProvider = (*GrokProvider)(nil)
var _ ScenarioConfigurationProvider = (*GrokProvider)(nil)
var _ ScenarioAvailabilityProvider = (*GrokProvider)(nil)
var _ CapabilityProvider = (*GrokProvider)(nil)
var _ ModelCapabilityProvider = (*GrokProvider)(nil)
