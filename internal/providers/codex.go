package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/config"
)

const (
	codexOAuthClientID           = codexauth.OAuthClientID
	codexOAuthRefreshURL         = codexauth.OAuthTokenEndpoint
	codexAccessTokenRefreshAhead = 5 * time.Minute
	codexUnknownExpiryRefreshAge = 8 * 24 * time.Hour
	codexMaxResponseBytes        = 4 << 20

	// Snapshot of explicit Fast tiers from OpenAI's public Codex model catalog.
	// It is used only for the canonical Codex endpoint when the authenticated
	// model catalog is unavailable; unknown model names are never inferred.
	codexFallbackFastModelCatalog = `{"models":[{"slug":"gpt-5.6-sol","additional_speed_tiers":["fast"]},{"slug":"gpt-5.6-terra","additional_speed_tiers":["fast"]},{"slug":"gpt-5.6-luna","additional_speed_tiers":["fast"]},{"slug":"gpt-5.5","additional_speed_tiers":["fast"]},{"slug":"gpt-5.4","additional_speed_tiers":["fast"]}]}`
)

type CodexProvider struct {
	cfg                 config.ProviderConfig
	store               *codexauth.Store
	client              *http.Client
	refreshClient       *http.Client
	refreshEndpoint     string
	clock               func() time.Time
	telemetry           AccountTelemetry
	endpointErr         error
	refreshGate         chan struct{}
	gatewayPolicyMu     sync.RWMutex
	gatewayPolicy       GatewayAccountPolicy
	modelCapabilitiesMu sync.RWMutex
	modelCapabilities   map[string]ModelCapabilities
}

func NewCodexProvider(cfg config.ProviderConfig) *CodexProvider {
	if cfg.Name == "" {
		cfg.Name = codexauth.DefaultProviderName
	}
	if cfg.Model == "" {
		cfg.Model = codexauth.DefaultModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = codexauth.DefaultBaseURL
	}
	refreshEndpoint := codexOAuthRefreshURL
	if configuredEndpoint, err := codexRefreshEndpointForConfig(cfg); err == nil {
		refreshEndpoint = configuredEndpoint
	}
	endpointErr := ValidateCodexProviderConfig(cfg)
	client, clientErr := providerHTTPClient(cfg, 5*time.Minute)
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	client.CheckRedirect = codexRedirectPolicy
	refreshConfig := cfg
	// Custom API headers are scoped to the configured Codex API origin and must
	// never be forwarded to the separate OAuth token endpoint.
	refreshConfig.RequestHeaders = nil
	refreshClient, refreshClientErr := providerHTTPClient(refreshConfig, 5*time.Minute)
	if refreshClient == nil {
		refreshClient = &http.Client{Timeout: 5 * time.Minute}
	}
	refreshClient.CheckRedirect = codexRefreshRedirectPolicy
	if endpointErr == nil {
		if clientErr != nil {
			endpointErr = clientErr
		} else if refreshClientErr != nil {
			endpointErr = refreshClientErr
		}
	}
	return &CodexProvider{
		cfg:               cfg,
		store:             codexauth.NewStore(cfg.CredentialStorePath),
		client:            client,
		refreshClient:     refreshClient,
		refreshEndpoint:   refreshEndpoint,
		clock:             time.Now,
		endpointErr:       endpointErr,
		refreshGate:       make(chan struct{}, 1),
		modelCapabilities: fallbackCodexModelCapabilities(cfg.BaseURL),
	}
}

func (p *CodexProvider) Name() string { return p.cfg.Name }

func (p *CodexProvider) Capabilities() Capabilities {
	return Capabilities{
		Tools:            true,
		Streaming:        true,
		ImageInput:       true,
		ReasoningEffort:  true,
		ReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
	}
}

func (p *CodexProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	model = strings.TrimSpace(model)
	p.modelCapabilitiesMu.RLock()
	capabilities := p.modelCapabilities[model]
	p.modelCapabilitiesMu.RUnlock()
	capabilities.ContextTokenLimit = p.cfg.ModelContextTokenLimit(model)
	return capabilities
}

func (p *CodexProvider) replaceModelCapabilities(capabilities map[string]ModelCapabilities) {
	if p == nil {
		return
	}
	if capabilities == nil {
		capabilities = make(map[string]ModelCapabilities)
	}
	p.modelCapabilitiesMu.Lock()
	p.modelCapabilities = capabilities
	p.modelCapabilitiesMu.Unlock()
}

func (p *CodexProvider) Configured() bool {
	return p != nil && p.endpointErr == nil && p.store != nil && p.store.Configured()
}

func (p *CodexProvider) ConfiguredForScenario(scenario CallScenario) bool {
	if scenario == CallScenarioGateway {
		return false
	}
	return p.Configured()
}

func (p *CodexProvider) AvailableForScenario(ctx context.Context, availability ScenarioAvailability) bool {
	if p == nil || p.endpointErr != nil {
		return false
	}
	credentials, err := p.credentialsForRequest(ctx, GenerateRequest{
		Scenario:                     availability.EffectiveScenario(),
		AllowSubscriptionCredentials: availability.AllowSubscriptionCredentials,
	})
	return err == nil && len(credentials) > 0
}

func (p *CodexProvider) SetGatewayAccountPolicy(policy GatewayAccountPolicy) {
	if p == nil {
		return
	}
	p.gatewayPolicyMu.Lock()
	p.gatewayPolicy = policy
	p.gatewayPolicyMu.Unlock()
}

func (p *CodexProvider) gatewayAccountPolicy() GatewayAccountPolicy {
	if p == nil {
		return nil
	}
	p.gatewayPolicyMu.RLock()
	defer p.gatewayPolicyMu.RUnlock()
	return p.gatewayPolicy
}

func (p *CodexProvider) ListModels(ctx context.Context) ([]string, error) {
	credentials, err := p.credentials()
	if err != nil {
		return nil, err
	}
	return p.listModelsWithCredentials(ctx, credentials)
}

func (p *CodexProvider) listModelsWithCredentials(ctx context.Context, credentials []codexauth.StoredCredential) ([]string, error) {
	if len(credentials) == 0 {
		p.replaceModelCapabilities(fallbackCodexModelCapabilities(p.cfg.BaseURL))
		return fallbackCodexModels(p.cfg.Model), nil
	}
	var lastErr error
	for _, item := range credentials {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, used, requestErr := p.doCredentialRequest(ctx, item, http.MethodGet, p.modelsURL(), nil, "")
		if requestErr != nil {
			if isContextTermination(requestErr) || ctx.Err() != nil {
				return nil, contextError(ctx, requestErr)
			}
			lastErr = requestErr
			continue
		}
		if err := ctx.Err(); err != nil {
			response.Body.Close()
			return nil, err
		}
		if response.StatusCode >= http.StatusMultipleChoices {
			status := response.StatusCode
			lastErr = codexHTTPError(response, used.Credential, "Codex 模型列表请求失败")
			response.Body.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if shouldTryNextCodexCredential(status) {
				continue
			}
			return nil, lastErr
		}
		models, modelCapabilities, parseErr := parseCodexModelCatalog(response.Body)
		response.Body.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		if len(models) == 0 {
			p.replaceModelCapabilities(fallbackCodexModelCapabilities(p.cfg.BaseURL))
			return fallbackCodexModels(p.cfg.Model), nil
		}
		p.replaceModelCapabilities(modelCapabilities)
		return models, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lastErr == nil {
		lastErr = providerUnavailableError(p.cfg.Name, "没有可用的 Codex OAuth 凭据")
	}
	return nil, lastErr
}

func (p *CodexProvider) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	reasoningEffort, err := normalizeReasoningEffortForCapabilities(req.ReasoningEffort, p.Capabilities(), p.cfg.Name)
	if err != nil {
		return nil, err
	}
	credentials, err := p.credentialsForRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(credentials) == 0 {
		return nil, providerUnavailableError(p.cfg.Name, "尚未导入 Codex OAuth 凭据")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.cfg.Model
	}
	if req.FastMode && !p.ModelCapabilities(model).FastMode {
		if _, listErr := p.listModelsWithCredentials(ctx, credentials); listErr != nil {
			return nil, listErr
		}
		if !p.ModelCapabilities(model).FastMode {
			return nil, fmt.Errorf("%w by %s provider model %q", ErrFastModeUnsupported, p.cfg.Name, model)
		}
	}
	payload, err := buildCodexResponsesPayload(req, model, reasoningEffort, p.cfg.InstallationID, p.cfg.ClientVersion)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("构造 Codex 请求失败")
	}

	out := make(chan Event, 8)
	go func() {
		defer close(out)
		var lastErr error
		for _, item := range credentials {
			if ctx.Err() != nil {
				return
			}
			response, used, requestErr := p.doCredentialRequest(ctx, item, http.MethodPost, p.responsesURL(), data, "application/json")
			if requestErr != nil {
				if isContextTermination(requestErr) || ctx.Err() != nil {
					return
				}
				lastErr = requestErr
				p.recordAccountAttempt(ctx, used.Credential.ID, false, 0, "request_failed", telemetryErrorCode(requestErr))
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if ctx.Err() != nil {
				response.Body.Close()
				return
			}
			if response.StatusCode >= http.StatusMultipleChoices {
				status := response.StatusCode
				var errorCode string
				lastErr, errorCode = codexHTTPErrorDetails(response, used.Credential, "Codex 模型请求失败")
				response.Body.Close()
				if ctx.Err() != nil {
					return
				}
				p.recordAccountAttempt(ctx, used.Credential.ID, false, status, http.StatusText(status), errorCode)
				if ctx.Err() != nil {
					return
				}
				if shouldTryNextCodexCredential(status) {
					continue
				}
				if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, used.Credential.ID)) {
					return
				}
				emitProviderEvent(ctx, out, Event{Type: "error", Text: lastErr.Error()})
				return
			}
			if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, used.Credential.ID)) {
				response.Body.Close()
				return
			}
			outcome := handleCodexResponsesStream(ctx, out, response.Body, used.Credential)
			response.Body.Close()
			if ctx.Err() != nil || outcome.ErrorCode == "context_canceled" || outcome.ErrorCode == "deadline_exceeded" {
				return
			}
			p.recordAccountAttempt(ctx, used.Credential.ID, outcome.Success, response.StatusCode, http.StatusText(response.StatusCode), outcome.ErrorCode)
			return
		}
		if lastErr == nil {
			lastErr = providerUnavailableError(p.cfg.Name, "没有可用的 Codex OAuth 凭据")
		}
		emitProviderEvent(ctx, out, Event{Type: "error", Text: lastErr.Error()})
	}()
	return out, nil
}

func (p *CodexProvider) credentialsForRequest(ctx context.Context, req GenerateRequest) ([]codexauth.StoredCredential, error) {
	if req.EffectiveScenario() == CallScenarioGateway && !req.AllowSubscriptionCredentials {
		return nil, fmt.Errorf("%w: Codex provider %q requires explicit subscription credential sharing", ErrGatewayOAuthUnsupported, p.cfg.Name)
	}
	credentials, err := p.credentials()
	if err != nil {
		return nil, err
	}
	if req.EffectiveScenario() != CallScenarioGateway {
		return credentials, nil
	}
	granted, err := gatewayAccountIDSet(ctx, p.gatewayAccountPolicy(), p.cfg.Name)
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "Gateway account grants are unavailable")
	}
	filtered := make([]codexauth.StoredCredential, 0, len(credentials))
	for _, item := range credentials {
		if _, ok := granted[item.Credential.ID]; ok {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return nil, providerUnavailableError(p.cfg.Name, "Gateway has no authorized Codex account")
	}
	return filtered, nil
}

func (p *CodexProvider) credentials() ([]codexauth.StoredCredential, error) {
	if p != nil && p.endpointErr != nil {
		return nil, providerUnavailableError(codexauth.DefaultProviderName, p.endpointErr.Error())
	}
	if p == nil || p.store == nil || strings.TrimSpace(p.store.Dir()) == "" {
		return nil, providerUnavailableError(codexauth.DefaultProviderName, "本地凭据库未配置")
	}
	items, err := p.store.Load()
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, err.Error())
	}
	out := make([]codexauth.StoredCredential, 0, len(items))
	for _, item := range items {
		if item.Credential.Disabled {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Credential.Priority < out[j].Credential.Priority
	})
	return out, nil
}

func (p *CodexProvider) doCredentialRequest(ctx context.Context, item codexauth.StoredCredential, method, endpoint string, body []byte, contentType string) (*http.Response, codexauth.StoredCredential, error) {
	if err := ctx.Err(); err != nil {
		return nil, item, err
	}
	prepared, err := p.prepareCredential(ctx, item)
	if err != nil {
		return nil, item, err
	}
	response, err := p.sendCredentialRequest(ctx, prepared.Credential, method, endpoint, body, contentType)
	if err != nil {
		return nil, prepared, err
	}
	if response.StatusCode != http.StatusUnauthorized || prepared.Credential.RefreshToken == "" {
		return response, prepared, nil
	}
	response.Body.Close()
	if err := ctx.Err(); err != nil {
		return nil, prepared, err
	}
	refreshed, err := p.refreshCredential(ctx, prepared)
	if err != nil {
		return nil, prepared, err
	}
	response, err = p.sendCredentialRequest(ctx, refreshed.Credential, method, endpoint, body, contentType)
	return response, refreshed, err
}

func (p *CodexProvider) prepareCredential(ctx context.Context, item codexauth.StoredCredential) (codexauth.StoredCredential, error) {
	if err := ctx.Err(); err != nil {
		return item, err
	}
	credential := item.Credential
	expiresAt := credential.ExpiresAt()
	needsRefresh := credential.AccessToken == ""
	if !expiresAt.IsZero() && !expiresAt.After(p.now().Add(codexAccessTokenRefreshAhead)) {
		needsRefresh = true
	}
	if expiresAt.IsZero() && credential.RefreshToken != "" {
		if lastRefresh, err := time.Parse(time.RFC3339, credential.LastRefresh); err == nil && p.now().Sub(lastRefresh) >= codexUnknownExpiryRefreshAge {
			needsRefresh = true
		}
	}
	if !needsRefresh {
		return item, nil
	}
	if credential.RefreshToken == "" {
		return item, providerUnavailableError(p.cfg.Name, "Codex access token 已过期且没有 refresh_token")
	}
	return p.refreshCredential(ctx, item)
}

func (p *CodexProvider) refreshCredential(ctx context.Context, item codexauth.StoredCredential) (codexauth.StoredCredential, error) {
	select {
	case p.refreshGate <- struct{}{}:
		defer func() { <-p.refreshGate }()
	case <-ctx.Done():
		return item, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return item, err
	}
	if currentItems, err := p.store.Load(); err == nil {
		for _, current := range currentItems {
			if current.Filename != item.Filename {
				continue
			}
			if current.Credential.AccessToken != item.Credential.AccessToken || current.Credential.RefreshToken != item.Credential.RefreshToken || current.Credential.IDToken != item.Credential.IDToken {
				return current, nil
			}
			item = current
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return item, err
	}
	credential := item.Credential
	if credential.RefreshToken == "" {
		return item, providerUnavailableError(p.cfg.Name, "Codex 凭据缺少 refresh_token")
	}
	payload := map[string]string{
		"client_id":     codexOAuthClientID,
		"grant_type":    "refresh_token",
		"refresh_token": credential.RefreshToken,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return item, providerUnavailableError(p.cfg.Name, "无法构造 OAuth 刷新请求")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.refreshEndpoint, bytes.NewReader(data))
	if err != nil {
		return item, providerUnavailableError(p.cfg.Name, "无法构造 OAuth 刷新请求")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", p.userAgent())
	response, err := p.refreshClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return item, ctx.Err()
		}
		return item, providerUnavailableError(p.cfg.Name, "Codex OAuth 刷新请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices {
		return item, codexRefreshError(response.StatusCode, response.Body)
	}
	var refreshed struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := decodeLimitedJSON(response.Body, codexMaxResponseBytes, &refreshed); err != nil {
		return item, providerUnavailableError(p.cfg.Name, "Codex OAuth 刷新响应无效")
	}
	if strings.TrimSpace(refreshed.AccessToken) == "" && credential.AccessToken == "" {
		return item, providerUnavailableError(p.cfg.Name, "Codex OAuth 刷新响应缺少 access_token")
	}
	if refreshed.AccessToken != "" {
		credential.AccessToken = strings.TrimSpace(refreshed.AccessToken)
		credential.Expired = ""
	}
	if refreshed.RefreshToken != "" {
		credential.RefreshToken = strings.TrimSpace(refreshed.RefreshToken)
	}
	if refreshed.IDToken != "" {
		credential.IDToken = strings.TrimSpace(refreshed.IDToken)
	}
	now := p.now().UTC()
	credential.LastRefresh = now.Format(time.RFC3339)
	applyCodexJWTMetadata(&credential)
	if credential.Expired == "" && refreshed.ExpiresIn > 0 {
		credential.Expired = now.Add(time.Duration(refreshed.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	item.Credential = credential
	if err := p.store.Update(item); err != nil {
		return item, providerUnavailableError(p.cfg.Name, "刷新后的 Codex 凭据无法保存")
	}
	return item, nil
}

func (p *CodexProvider) sendCredentialRequest(ctx context.Context, credential codexauth.Credential, method, endpoint string, body []byte, contentType string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "无法构造 Codex 请求")
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	if credential.AccountID != "" {
		request.Header.Set("ChatGPT-Account-ID", credential.AccountID)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Accept", "text/event-stream, application/json")
	request.Header.Set("User-Agent", p.userAgent())
	request.Header.Set("originator", "autoto")
	if p.cfg.ClientVersion != "" {
		request.Header.Set("version", p.cfg.ClientVersion)
	}
	if p.cfg.InstallationID != "" {
		request.Header.Set("x-codex-installation-id", p.cfg.InstallationID)
	}
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, providerUnavailableError(p.cfg.Name, "Codex 直连请求失败")
	}
	return response, nil
}

func (p *CodexProvider) responsesURL() string {
	return strings.TrimRight(p.cfg.BaseURL, "/") + "/responses"
}

func (p *CodexProvider) modelsURL() string {
	base := strings.TrimRight(p.cfg.BaseURL, "/") + "/models"
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	version := strings.TrimSpace(p.cfg.ClientVersion)
	if version == "" {
		version = config.Version
	}
	query.Set("client_version", version)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (p *CodexProvider) userAgent() string {
	version := strings.TrimSpace(p.cfg.ClientVersion)
	if version == "" {
		version = config.Version
	}
	return "autoto/" + version
}

func (p *CodexProvider) now() time.Time {
	if p.clock != nil {
		return p.clock()
	}
	return time.Now()
}

func (p *CodexProvider) recordAccountAttempt(ctx context.Context, accountID string, success bool, httpStatus int, statusCode, errorCode string) {
	if p == nil || p.telemetry == nil || strings.TrimSpace(accountID) == "" || ctx.Err() != nil {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = p.telemetry.RecordProviderAccountAttempt(recordCtx, ProviderAccountAttempt{
		Provider:    p.cfg.Name,
		AccountID:   accountID,
		Success:     success,
		HTTPStatus:  httpStatus,
		StatusCode:  safeTelemetryCode(statusCode, ""),
		ErrorCode:   safeTelemetryCode(errorCode, ""),
		AttemptedAt: p.now().UTC(),
	})
}

func isContextTermination(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func contextError(ctx context.Context, fallback error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return fallback
}

func telemetryErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "network_error"
}

func safeTelemetryCode(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if len(value) > 128 {
		value = value[:128]
	}
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._:-", char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func buildCodexResponsesPayload(req GenerateRequest, model, reasoningEffort, installationID, clientVersion string) (map[string]any, error) {
	input := codexResponseInput(req.Messages)
	if len(input) == 0 {
		input = []map[string]any{{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "Continue."}},
		}}
	}
	payload := map[string]any{
		"model":               model,
		"input":               input,
		"tool_choice":         "auto",
		"parallel_tool_calls": len(req.Tools) > 0,
		"store":               false,
		"stream":              true,
		"include":             []string{"reasoning.encrypted_content"},
	}
	if instructions := strings.TrimSpace(req.SystemPrompt); instructions != "" {
		payload["instructions"] = instructions
	}
	if tools := codexToolParams(req.Tools); len(tools) > 0 {
		payload["tools"] = tools
	}
	if reasoningEffort != "" {
		payload["reasoning"] = map[string]any{"effort": reasoningEffort}
	}
	if req.MaxOutputTokens > 0 {
		payload["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.FastMode {
		payload["service_tier"] = "priority"
	}
	metadata := map[string]string{}
	if installationID != "" {
		metadata["x-codex-installation-id"] = installationID
	}
	if clientVersion != "" {
		metadata["client-version"] = clientVersion
	}
	if len(metadata) > 0 {
		payload["client_metadata"] = metadata
	}
	return payload, nil
}

func codexResponseInput(messages []Message) []map[string]any {
	items := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			role = "user"
		}
		blocks := normalizeContentBlocks(message)
		if len(blocks) == 0 {
			continue
		}
		content := make([]map[string]any, 0, len(blocks))
		structured := make([]map[string]any, 0, len(blocks))
		for _, block := range blocks {
			switch block.Type {
			case "tool_use":
				callID := strings.TrimSpace(block.ToolUseID)
				name := strings.TrimSpace(block.ToolName)
				if callID != "" && name != "" {
					structured = append(structured, map[string]any{
						"type":      "function_call",
						"call_id":   callID,
						"name":      name,
						"arguments": openAIToolArgumentsString(block.Input),
					})
				}
			case "tool_result":
				callID := strings.TrimSpace(block.ToolUseID)
				if callID != "" {
					structured = append(structured, map[string]any{
						"type":    "function_call_output",
						"call_id": callID,
						"output":  openAIToolResultOutput(block),
					})
				}
			case "image":
				if len(block.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(block.MIMEType)
				if mimeType == "" {
					mimeType = "image/png"
				}
				content = append(content, map[string]any{
					"type":      "input_image",
					"image_url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(block.Data),
				})
			default:
				text := strings.TrimSpace(block.Text)
				if text == "" {
					continue
				}
				contentType := "input_text"
				if role == "assistant" {
					contentType = "output_text"
				}
				content = append(content, map[string]any{"type": contentType, "text": text})
			}
		}
		if len(content) > 0 {
			items = append(items, map[string]any{"type": "message", "role": role, "content": content})
		}
		items = append(items, structured...)
	}
	return items
}

func codexToolParams(tools []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		item := map[string]any{
			"type":       "function",
			"name":       name,
			"parameters": openAIToolSchema(tool.Schema),
			"strict":     false,
		}
		if description := strings.TrimSpace(tool.Description); description != "" {
			item["description"] = description
		}
		out = append(out, item)
	}
	return out
}

type codexStreamOutcome struct {
	Success   bool
	ErrorCode string
}

func handleCodexResponsesStream(ctx context.Context, out chan<- Event, body io.Reader, credential codexauth.Credential) codexStreamOutcome {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2<<20)
	emittedCalls := map[string]bool{}
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
		var event codexStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" && !emitProviderEvent(ctx, out, Event{Type: "text", Text: event.Delta}) {
				return codexStreamOutcome{ErrorCode: "context_canceled"}
			}
		case "response.output_item.done":
			if call := codexToolCallFromItem(event.Item); call != nil && !emittedCalls[call.ID] {
				emittedCalls[call.ID] = true
				if !emitProviderEvent(ctx, out, Event{Type: "tool_call", ToolCall: call}) {
					return codexStreamOutcome{ErrorCode: "context_canceled"}
				}
			}
		case "response.completed":
			if !emitCodexResponseToolCalls(ctx, out, event.Response.Output, emittedCalls) {
				return codexStreamOutcome{ErrorCode: "context_canceled"}
			}
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return codexStreamOutcome{ErrorCode: "context_canceled"}
				}
			}
			emitProviderEvent(ctx, out, Event{Type: "done", Done: true})
			return codexStreamOutcome{Success: true}
		case "response.incomplete":
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return codexStreamOutcome{ErrorCode: "context_canceled"}
				}
			}
			reason := strings.TrimSpace(event.Response.IncompleteDetails.Reason)
			if reason == "" {
				reason = "incomplete"
			}
			emitProviderEvent(ctx, out, Event{Type: "done", Done: true, StopReason: reason})
			return codexStreamOutcome{Success: true}
		case "response.failed":
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return codexStreamOutcome{ErrorCode: "context_canceled"}
				}
			}
			message := safeCodexEventError(event.Response.Error.Code, event.Response.Error.Message, "Codex response failed", credential)
			emitProviderEvent(ctx, out, Event{Type: "error", Text: message})
			return codexStreamOutcome{ErrorCode: safeTelemetryCode(event.Response.Error.Code, "response_failed")}
		case "error":
			message := safeCodexEventError(event.Code, event.Message, "Codex stream error", credential)
			emitProviderEvent(ctx, out, Event{Type: "error", Text: message})
			return codexStreamOutcome{ErrorCode: safeTelemetryCode(event.Code, "stream_error")}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return codexStreamOutcome{ErrorCode: telemetryErrorCode(ctx.Err())}
		}
		emitProviderEvent(ctx, out, Event{Type: "error", Text: "Codex 响应流读取失败"})
		return codexStreamOutcome{ErrorCode: "stream_read_error"}
	}
	emitProviderEvent(ctx, out, Event{Type: "error", Text: "Codex 响应流在完成前关闭"})
	return codexStreamOutcome{ErrorCode: "stream_closed"}
}

type codexStreamEvent struct {
	Type     string            `json:"type"`
	Delta    string            `json:"delta"`
	Code     string            `json:"code"`
	Message  string            `json:"message"`
	Item     codexResponseItem `json:"item"`
	Response struct {
		Output            []codexResponseItem `json:"output"`
		Usage             codexResponseUsage  `json:"usage"`
		Error             codexResponseError  `json:"error"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type codexResponseItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type codexResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type codexResponseUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	InputTokenDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokenDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u codexResponseUsage) toUsage() Usage {
	return Usage{
		InputTokens:       u.InputTokens,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.InputTokenDetails.CachedTokens,
		ReasoningTokens:   u.OutputTokenDetails.ReasoningTokens,
	}
}

func codexToolCallFromItem(item codexResponseItem) *ToolCall {
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

func emitCodexResponseToolCalls(ctx context.Context, out chan<- Event, items []codexResponseItem, emitted map[string]bool) bool {
	for _, item := range items {
		call := codexToolCallFromItem(item)
		if call == nil || emitted[call.ID] {
			continue
		}
		emitted[call.ID] = true
		if !emitProviderEvent(ctx, out, Event{Type: "tool_call", ToolCall: call}) {
			return false
		}
	}
	return true
}

func emitProviderEvent(ctx context.Context, out chan<- Event, event Event) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

type codexModelCatalogEntry struct {
	Slug                  string          `json:"slug"`
	ID                    string          `json:"id"`
	FastMode              *bool           `json:"fast_mode"`
	SupportsFastMode      *bool           `json:"supports_fast_mode"`
	ServiceTier           json.RawMessage `json:"service_tier"`
	ServiceTiers          json.RawMessage `json:"service_tiers"`
	SupportedServiceTiers json.RawMessage `json:"supported_service_tiers"`
	AdditionalSpeedTiers  json.RawMessage `json:"additional_speed_tiers"`
	SupportedSpeedTiers   json.RawMessage `json:"supported_speed_tiers"`
}

func parseCodexModels(reader io.Reader) ([]string, error) {
	models, _, err := parseCodexModelCatalog(reader)
	return models, err
}

func parseCodexModelCatalog(reader io.Reader) ([]string, map[string]ModelCapabilities, error) {
	var body struct {
		Models []codexModelCatalogEntry `json:"models"`
		Data   []codexModelCatalogEntry `json:"data"`
	}
	if err := decodeLimitedJSON(reader, codexMaxResponseBytes, &body); err != nil {
		return nil, nil, errors.New("Codex 模型列表响应无效")
	}
	seen := map[string]struct{}{}
	capabilities := make(map[string]ModelCapabilities)
	models := make([]string, 0, len(body.Models)+len(body.Data))
	appendEntry := func(item codexModelCatalogEntry) {
		model := strings.TrimSpace(item.Slug)
		if model == "" {
			model = strings.TrimSpace(item.ID)
		}
		if model == "" {
			return
		}
		if supportsFastMode, known := codexModelEntryFastModeCapability(item); known {
			current := capabilities[model]
			current.FastMode = current.FastMode || supportsFastMode
			current.FastModeKnown = true
			capabilities[model] = current
		}
		if _, exists := seen[model]; exists {
			return
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	for _, item := range body.Models {
		appendEntry(item)
	}
	for _, item := range body.Data {
		appendEntry(item)
	}
	sort.Strings(models)
	return models, capabilities, nil
}

func codexModelEntryFastModeCapability(item codexModelCatalogEntry) (bool, bool) {
	supported := false
	known := false
	for _, value := range []*bool{item.FastMode, item.SupportsFastMode} {
		if value == nil {
			continue
		}
		known = true
		supported = supported || *value
	}
	for _, value := range []json.RawMessage{
		item.ServiceTier,
		item.ServiceTiers,
		item.SupportedServiceTiers,
		item.AdditionalSpeedTiers,
		item.SupportedSpeedTiers,
	} {
		fieldSupportsFastMode, fieldKnown := codexSpeedTierCapability(value)
		known = known || fieldKnown
		supported = supported || fieldSupportsFastMode
	}
	return supported, known
}

func codexSpeedTierCapability(value json.RawMessage) (bool, bool) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return false, false
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return false, false
	}
	return codexSpeedTierSupportsFastMode(decoded), true
}

func codexSpeedTierSupportsFastMode(value any) bool {
	switch typed := value.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "fast", "priority":
			return true
		default:
			return false
		}
	case []any:
		for _, item := range typed {
			if codexSpeedTierSupportsFastMode(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if normalizedKey == "fast_mode" || normalizedKey == "supports_fast_mode" {
				if enabled, ok := item.(bool); ok && enabled {
					return true
				}
			}
			switch normalizedKey {
			case "service_tier", "service_tiers", "tier", "name", "id", "slug", "value", "speed", "mode", "additional_speed_tiers", "supported_speed_tiers":
				if codexSpeedTierSupportsFastMode(item) {
					return true
				}
			}
		}
	}
	return false
}

func decodeLimitedJSON(reader io.Reader, limit int64, dst any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit {
		return errors.New("response too large")
	}
	return json.Unmarshal(data, dst)
}

func fallbackCodexModelCapabilities(baseURL string) map[string]ModelCapabilities {
	if strings.TrimRight(strings.TrimSpace(baseURL), "/") != codexauth.DefaultBaseURL {
		return make(map[string]ModelCapabilities)
	}
	_, capabilities, err := parseCodexModelCatalog(strings.NewReader(codexFallbackFastModelCatalog))
	if err != nil {
		return make(map[string]ModelCapabilities)
	}
	return capabilities
}

func fallbackCodexModels(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = codexauth.DefaultModel
	}
	return []string{model}
}

func shouldTryNextCodexCredential(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func codexHTTPError(response *http.Response, credential codexauth.Credential, prefix string) error {
	err, _ := codexHTTPErrorDetails(response, credential, prefix)
	return err
}

func codexHTTPErrorDetails(response *http.Response, credential codexauth.Credential, prefix string) (error, string) {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = decodeLimitedJSON(response.Body, 64<<10, &body)
	code := strings.TrimSpace(body.Error.Code)
	message := strings.TrimSpace(body.Error.Message)
	if code == "" {
		code = strings.TrimSpace(body.Code)
	}
	if message == "" {
		message = strings.TrimSpace(body.Message)
	}
	code = sanitizeCodexErrorCode(code, credential)
	message = redactCodexSecrets(message, credential)
	if len(message) > 512 {
		message = message[:512]
	}
	safeCode := safeTelemetryCode(code, fmt.Sprintf("http_%d", response.StatusCode))
	if code != "" && message != "" {
		return fmt.Errorf("%s：HTTP %d (%s)：%s", prefix, response.StatusCode, code, message), safeCode
	}
	if code != "" {
		return fmt.Errorf("%s：HTTP %d (%s)", prefix, response.StatusCode, code), safeCode
	}
	if message != "" {
		return fmt.Errorf("%s：HTTP %d：%s", prefix, response.StatusCode, message), safeCode
	}
	return fmt.Errorf("%s：HTTP %d", prefix, response.StatusCode), safeCode
}

func codexRefreshError(status int, reader io.Reader) error {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Code string `json:"code"`
	}
	_ = decodeLimitedJSON(reader, 64<<10, &body)
	code := strings.ToLower(strings.TrimSpace(body.Error.Code))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(body.Code))
	}
	switch code {
	case "refresh_token_expired":
		return providerUnavailableError(codexauth.DefaultProviderName, "refresh_token 已过期，请重新导入凭据")
	case "refresh_token_reused":
		return providerUnavailableError(codexauth.DefaultProviderName, "refresh_token 已被使用，请重新导入最新凭据")
	case "refresh_token_invalidated":
		return providerUnavailableError(codexauth.DefaultProviderName, "refresh_token 已被撤销，请重新登录后导入")
	default:
		return providerUnavailableError(codexauth.DefaultProviderName, fmt.Sprintf("OAuth 刷新失败（HTTP %d）", status))
	}
}

func safeCodexEventError(code, message, fallback string, credential codexauth.Credential) string {
	code = sanitizeCodexErrorCode(code, credential)
	message = redactCodexSecrets(strings.TrimSpace(message), credential)
	if len(message) > 512 {
		message = message[:512]
	}
	if code != "" && message != "" {
		return fmt.Sprintf("%s (%s)：%s", fallback, code, message)
	}
	if code != "" {
		return fmt.Sprintf("%s (%s)", fallback, code)
	}
	if message != "" {
		return fallback + "：" + message
	}
	return fallback
}

func redactCodexSecrets(message string, credential codexauth.Credential) string {
	for _, secret := range []string{credential.AccessToken, credential.RefreshToken, credential.IDToken} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func sanitizeCodexErrorCode(code string, credential codexauth.Credential) string {
	code = strings.TrimSpace(redactCodexSecrets(code, credential))
	if code == "" {
		return ""
	}
	lower := strings.ToLower(code)
	if strings.Contains(code, "[redacted]") || strings.Contains(lower, "bearer") || strings.HasPrefix(code, "eyJ") || looksLikeCodexSecretToken(code) || looksLikeEmbeddedJWT(code) {
		return "redacted"
	}
	if len(code) > 64 {
		return "invalid_upstream_code"
	}
	sanitized := safeTelemetryCode(code, "invalid_upstream_code")
	if sanitized == "" {
		return "invalid_upstream_code"
	}
	return sanitized
}

func looksLikeCodexSecretToken(value string) bool {
	lower := strings.ToLower(value)
	return hasCodexSecretPrefix(lower, "sk-") || hasCodexSecretPrefix(lower, "rt_")
}

func hasCodexSecretPrefix(value, prefix string) bool {
	for offset := 0; offset < len(value); {
		index := strings.Index(value[offset:], prefix)
		if index < 0 {
			return false
		}
		index += offset
		boundary := index == 0 || !isCodexCodeAlphaNumeric(value[index-1])
		if boundary && len(value)-(index+len(prefix)) >= 4 {
			return true
		}
		offset = index + len(prefix)
	}
	return false
}

func isCodexCodeAlphaNumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}

func looksLikeEmbeddedJWT(value string) bool {
	for _, field := range strings.FieldsFunc(value, func(char rune) bool {
		return !(char >= 'A' && char <= 'Z') && !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' && char != '.'
	}) {
		parts := strings.Split(field, ".")
		if len(parts) == 3 && len(parts[0]) >= 8 && len(parts[1]) >= 8 {
			return true
		}
	}
	return false
}

func applyCodexJWTMetadata(credential *codexauth.Credential) {
	if credential == nil {
		return
	}
	access := parseCodexJWTMetadata(credential.AccessToken)
	idToken := parseCodexJWTMetadata(credential.IDToken)
	if access.AccountID == "" {
		access.AccountID = idToken.AccountID
	}
	if access.Email == "" {
		access.Email = idToken.Email
	}
	if access.PlanType == "" {
		access.PlanType = idToken.PlanType
	}
	if access.ExpiresAt == 0 {
		access.ExpiresAt = idToken.ExpiresAt
	}
	if access.AccountID != "" {
		credential.AccountID = access.AccountID
	}
	if access.Email != "" {
		credential.Email = access.Email
	}
	if access.PlanType != "" {
		credential.PlanType = access.PlanType
	}
	if access.ExpiresAt > 0 {
		credential.Expired = time.Unix(access.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
}

type codexJWTMetadata struct {
	AccountID string
	Email     string
	PlanType  string
	ExpiresAt int64
}

func parseCodexJWTMetadata(token string) codexJWTMetadata {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return codexJWTMetadata{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return codexJWTMetadata{}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return codexJWTMetadata{}
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	return codexJWTMetadata{
		AccountID: firstCodexString(auth, "chatgpt_account_id", "account_id"),
		Email:     firstCodexString(profile, "email"),
		PlanType:  firstCodexString(auth, "chatgpt_plan_type", "plan_type"),
		ExpiresAt: codexInt64(claims["exp"]),
	}
}

func firstCodexString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func codexInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}
