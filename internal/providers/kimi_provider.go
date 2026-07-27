package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"autoto/internal/config"
	"autoto/internal/kimiauth"
	"autoto/internal/subscriptionauth"
)

const kimiProductionBaseURL = "https://api.kimi.com/coding"

type KimiProvider struct {
	cfg       config.ProviderConfig
	accounts  *subscriptionProviderAccounts
	client    *http.Client
	baseURL   string
	configErr error
	refresh   subscriptionRefreshFunc
}

func NewKimiProvider(cfg config.ProviderConfig) *KimiProvider {
	cfg = normalizeKimiProviderConfig(cfg)
	apiConfig := cfg
	apiConfig.RequestHeaders = nil
	apiConfig.UserAgent = ""
	client, clientErr := providerHTTPClient(apiConfig, 5*time.Minute)
	refreshClient, refreshClientErr := providerHTTPClient(apiConfig, 30*time.Second)
	provider := &KimiProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderKimi),
		client:    client,
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		configErr: errors.Join(validateProviderRuntimeConfig(cfg), validateKimiProductionBaseURL(cfg.BaseURL), clientErr, refreshClientErr),
	}
	if refreshClient != nil {
		provider.refresh = func(ctx context.Context, credential subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
			authClient := kimiauth.New(refreshClient, cfg.ClientVersion, credential.DeviceID)
			tokens, err := authClient.RefreshToken(ctx, credential.RefreshToken)
			if err != nil || tokens == nil {
				return subscriptionauth.TokenUpdate{}, err
			}
			return subscriptionauth.TokenUpdate{
				AccessToken:  tokens.AccessToken,
				RefreshToken: tokens.RefreshToken,
				TokenType:    tokens.TokenType,
				ExpiresAt:    tokens.ExpiresAt,
				Scope:        tokens.Scope,
				DeviceID:     credential.DeviceID,
			}, nil
		}
	}
	return provider
}

func newKimiProviderForTest(cfg config.ProviderConfig, client *http.Client, baseURL string) *KimiProvider {
	cfg = normalizeKimiProviderConfig(cfg)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &KimiProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderKimi),
		client:    client,
		baseURL:   cfg.BaseURL,
		configErr: validateSubscriptionTestBaseURL(cfg.BaseURL),
	}
}

func normalizeKimiProviderConfig(cfg config.ProviderConfig) config.ProviderConfig {
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = config.ProviderTypeKimi
	}
	if strings.TrimSpace(cfg.Type) == "" {
		cfg.Type = config.ProviderTypeKimi
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = kimiProductionBaseURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "kimi-k2.7-code"
	}
	return cfg
}

func validateKimiProductionBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "api.kimi.com") || strings.TrimRight(parsed.EscapedPath(), "/") != "/coding" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Kimi Base URL only allows the official HTTPS coding endpoint")
	}
	return nil
}

func (p *KimiProvider) Name() string {
	if p == nil {
		return config.ProviderTypeKimi
	}
	return p.cfg.Name
}

func (p *KimiProvider) Configured() bool {
	return p != nil && p.configErr == nil && p.accounts.configured()
}

func (p *KimiProvider) ConfiguredForScenario(scenario CallScenario) bool {
	return p != nil && p.configErr == nil && p.accounts.configuredForScenario(scenario)
}

func (p *KimiProvider) AvailableForScenario(ctx context.Context, availability ScenarioAvailability) bool {
	return p != nil && p.configErr == nil && p.accounts.availableForScenario(ctx, availability)
}

func (p *KimiProvider) SetAccountTelemetry(telemetry AccountTelemetry) {
	if p != nil {
		p.accounts.setAccountTelemetry(telemetry)
	}
}

func (p *KimiProvider) SetGatewayAccountPolicy(policy GatewayAccountPolicy) {
	if p != nil {
		p.accounts.setGatewayAccountPolicy(policy)
	}
}

func (p *KimiProvider) Capabilities() Capabilities {
	return Capabilities{
		Tools:            true,
		Streaming:        true,
		ImageInput:       true,
		ReasoningEffort:  true,
		ReasoningEfforts: []string{"low", "high"},
	}
}

func (p *KimiProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	return configuredModelCapabilities(p.cfg, model)
}

func (p *KimiProvider) ListModels(ctx context.Context) ([]string, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeKimi, "provider is not configured")
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
		if strings.TrimSpace(item.DeviceID) == "" {
			lastErr = providerUnavailableError(p.cfg.Name, "Kimi account device ID is missing")
			p.accounts.recordAttempt(ctx, item.ID, false, 0, "device_id_missing", lastErr)
			continue
		}
		prepared, err := p.accounts.prepareCredential(ctx, item, p.refresh)
		if err != nil {
			lastErr = err
			p.accounts.recordAttempt(ctx, item.ID, false, 0, "credential_unavailable", err)
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
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

func (p *KimiProvider) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeKimi, "provider is not configured")
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
	payload := map[string]any{
		"model":          model,
		"messages":       openAICompatibleMessages(req),
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
	}
	if messages, ok := payload["messages"].([]map[string]any); ok && len(messages) == 0 {
		payload["messages"] = []map[string]any{{"role": "user", "content": "Continue."}}
	}
	if req.MaxOutputTokens > 0 {
		payload["max_tokens"] = req.MaxOutputTokens
	}
	if reasoningEffort != "" {
		payload["reasoning_effort"] = reasoningEffort
	}
	if tools := openAICompatibleTools(req.Tools); len(tools) > 0 {
		payload["tools"] = tools
	}
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
			if strings.TrimSpace(item.DeviceID) == "" {
				lastErr = providerUnavailableError(p.cfg.Name, "Kimi account device ID is missing")
				p.accounts.recordAttempt(ctx, item.ID, false, 0, "device_id_missing", lastErr)
				continue
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
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(data))
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
				p.emitKimiFinalError(ctx, out, model, prepared.ID, lastErr)
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
				p.emitKimiFinalError(ctx, out, model, prepared.ID, lastErr)
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
			isSSE := strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
			outcome := consumeOpenAICompatibleAttempt(ctx, out, response.Body, isSSE, emitDispatch, p.cfg.Name)
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

func (p *KimiProvider) emitKimiFinalError(ctx context.Context, out chan<- Event, model, accountID string, err error) {
	if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, accountID)) {
		return
	}
	_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, err).Error()})
}

func (p *KimiProvider) applyHeaders(request *http.Request, credential subscriptionauth.Credential, streaming bool) {
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential.AccessToken))
	if streaming {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	request.Header.Set("X-Msh-Platform", "Autoto")
	request.Header.Set("X-Msh-Version", safeSubscriptionHeader(p.cfg.ClientVersion, "unknown", 128))
	request.Header.Set("X-Msh-Device-Name", safeSubscriptionHeader(subscriptionHostname(), "unknown", 256))
	request.Header.Set("X-Msh-Device-Model", safeSubscriptionHeader(subscriptionDeviceModel(), "unknown", 256))
	request.Header.Set("X-Msh-Device-Id", safeSubscriptionHeader(credential.DeviceID, "", 256))
}

type openAICompatibleAttemptOutcome struct {
	emittedContent bool
	err            error
	code           string
}

func consumeOpenAICompatibleAttempt(ctx context.Context, out chan<- Event, reader io.Reader, stream bool, emitDispatch func() bool, provider string) openAICompatibleAttemptOutcome {
	attemptEvents := make(chan Event, 8)
	go func() {
		defer close(attemptEvents)
		if stream {
			handleOpenAICompatibleStream(attemptEvents, reader)
			return
		}
		handleOpenAICompatibleJSON(attemptEvents, reader)
	}()
	outcome := openAICompatibleAttemptOutcome{}
	for {
		select {
		case <-ctx.Done():
			return openAICompatibleAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
		case event, ok := <-attemptEvents:
			if !ok {
				if outcome.err == nil {
					outcome.err = newSubscriptionNetworkError(provider, io.EOF)
					outcome.code = "stream_closed"
				}
				return outcome
			}
			if event.Type == "error" {
				outcome.err = newSubscriptionNetworkError(provider, io.ErrUnexpectedEOF)
				outcome.code = "invalid_upstream_response"
				continue
			}
			if outcome.err != nil {
				continue
			}
			if event.Type == "text" || event.Type == "tool_call" {
				outcome.emittedContent = true
			}
			if !emitDispatch() || !emitProviderEvent(ctx, out, event) {
				return openAICompatibleAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
			}
			if event.Type == "done" {
				return outcome
			}
		}
	}
}

func safeSubscriptionHeader(value, fallback string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes || strings.ContainsAny(value, "\x00\r\n") {
		return fallback
	}
	return value
}

func subscriptionHostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return name
}

func subscriptionDeviceModel() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS " + runtime.GOARCH
	case "windows":
		return "Windows " + runtime.GOARCH
	case "linux":
		return "Linux " + runtime.GOARCH
	default:
		return runtime.GOOS + " " + runtime.GOARCH
	}
}

var _ Provider = (*KimiProvider)(nil)
var _ ConfigurationProvider = (*KimiProvider)(nil)
var _ ScenarioConfigurationProvider = (*KimiProvider)(nil)
var _ ScenarioAvailabilityProvider = (*KimiProvider)(nil)
var _ CapabilityProvider = (*KimiProvider)(nil)
var _ ModelCapabilityProvider = (*KimiProvider)(nil)

// WarmupTokens proactively refreshes expired/near-expiry OAuth tokens at
// startup so the first real request does not pay a cold-start TLS penalty.
func (p *KimiProvider) WarmupTokens(ctx context.Context) {
	if p == nil || p.accounts == nil || p.configErr != nil {
		return
	}
	p.accounts.warmupTokens(ctx, p.refresh)
}
