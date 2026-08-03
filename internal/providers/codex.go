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
	codexMaxErrorDetailBytes     = 512
	codexMaxErrorOutputBytes     = 768

	// Snapshot of explicit Fast tiers from OpenAI's public Codex model catalog.
	// It is used only for the canonical Codex endpoint when the authenticated
	// model catalog is unavailable; unknown model names are never inferred.
	codexFallbackFastModelCatalog = `{"models":[{"slug":"gpt-5.6-sol","additional_speed_tiers":["fast"]},{"slug":"gpt-5.6-terra","additional_speed_tiers":["fast"]},{"slug":"gpt-5.6-luna","additional_speed_tiers":["fast"]},{"slug":"gpt-5.5","additional_speed_tiers":["fast"]},{"slug":"gpt-5.4-mini","additional_speed_tiers":["fast"]}]}`

	// codexCatalogClientVersion is the Codex client generation Autoto reports to
	// the model catalog, which gates it: the endpoint requires the parameter and
	// silently returns *fewer or zero* models for a version below the generation
	// a model belongs to. Autoto's own version is unrelated to that scale, and
	// sending it returned an empty catalog, which then fell through to the seed
	// list above and hid the real per-account model set entirely.
	//
	// Raise this when OpenAI ships a model generation Autoto should offer. A
	// catalog that comes back empty or short is the symptom of it being stale.
	codexCatalogClientVersion = "1.0.0"
)

// codexKnownOfficialModelReasoningEfforts covers a known omission in the
// canonical Codex model catalog: gpt-5.6-sol can support max even when its
// catalog entry does not include supported_reasoning_levels. This is deliberately
// an exact model allow-list, not a model-name heuristic, and is only applied to
// the official ChatGPT Codex endpoint when the catalog has no explicit levels.
var codexKnownOfficialModelReasoningEfforts = map[string][]string{
	"gpt-5.6-sol": {"low", "medium", "high", "xhigh", "max"},
}

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
		ImageGeneration:  true,
		ReasoningEffort:  true,
		ReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
	}
}

// DefaultContextTokenLimit is the context window the current Codex models
// share. It applies to models the configuration does not size explicitly, which
// is the normal case now that the model list comes from the authenticated
// catalog rather than a hand-maintained list; without it those models would
// fall back to the global 120 000-token floor and reject conversations the
// model can comfortably hold.
func (p *CodexProvider) DefaultContextTokenLimit() int { return 272000 }

func (p *CodexProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	model = strings.TrimSpace(model)
	p.modelCapabilitiesMu.RLock()
	capabilities := p.modelCapabilities[model]
	p.modelCapabilitiesMu.RUnlock()
	configured := configuredModelCapabilities(p.cfg, model)
	capabilities.ImageGeneration = configured.ImageGeneration
	capabilities.ImageGenerationKnown = configured.ImageGenerationKnown
	capabilities.ContextTokenLimit = configured.ContextTokenLimit
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
			if shouldTryNextCodexRequestError(requestErr) {
				continue
			}
			return nil, requestErr
		}
		if err := ctx.Err(); err != nil {
			response.Body.Close()
			return nil, err
		}
		if response.StatusCode >= http.StatusMultipleChoices {
			status := response.StatusCode
			var errorCode string
			lastErr, errorCode = codexHTTPErrorDetails(response, used.Credential, p.cfg, "Codex 模型列表请求失败")
			response.Body.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if shouldTryNextCodexCredential(status, errorCode) {
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
			fallbackModels := fallbackCodexModels(p.cfg.Model)
			p.replaceModelCapabilities(fallbackCodexModelCapabilities(p.cfg.BaseURL))
			return fallbackModels, nil
		}
		modelCapabilities = supplementCodexModelCapabilities(modelCapabilities, models, p.cfg.BaseURL)
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
	// Effort is validated against the model, not the provider: the levels above
	// "xhigh" exist only on some models, so the provider-wide list would reject
	// a level the chosen model genuinely serves.
	reasoningEffort, err := normalizeReasoningEffortForCapabilities(req.ReasoningEffort, CapabilitiesForModel(p.Capabilities(), p.ModelCapabilities(model)), p.cfg.Name)
	if err != nil {
		return nil, err
	}
	if req.FastMode && !p.ModelCapabilities(model).FastMode {
		if _, listErr := p.listModelsWithCredentials(ctx, credentials); listErr != nil {
			return nil, listErr
		}
		if !p.ModelCapabilities(model).FastMode {
			return nil, fmt.Errorf("%w by %s provider model %q", ErrFastModeUnsupported, p.cfg.Name, model)
		}
	}
	req.EnableImageGeneration = req.EnableImageGeneration && p.Capabilities().ImageGeneration && p.ModelCapabilities(model).ImageGeneration
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
				if shouldTryNextCodexRequestError(requestErr) {
					continue
				}
				if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, used.Credential.ID)) {
					return
				}
				emitProviderEvent(ctx, out, Event{Type: "error", Text: boundedCodexError(requestErr.Error())})
				return
			}
			if ctx.Err() != nil {
				response.Body.Close()
				return
			}
			if response.StatusCode >= http.StatusMultipleChoices {
				status := response.StatusCode
				var errorCode string
				lastErr, errorCode = codexHTTPErrorDetails(response, used.Credential, p.cfg, "Codex 模型请求失败")
				response.Body.Close()
				if ctx.Err() != nil {
					return
				}
				p.recordAccountAttempt(ctx, used.Credential.ID, false, status, http.StatusText(status), errorCode)
				if ctx.Err() != nil {
					return
				}
				if shouldTryNextCodexCredential(status, errorCode) {
					continue
				}
				if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, used.Credential.ID)) {
					return
				}
				emitProviderEvent(ctx, out, Event{Type: "error", Text: boundedCodexError(lastErr.Error())})
				return
			}
			if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, used.Credential.ID)) {
				response.Body.Close()
				return
			}
			outcome := handleCodexResponsesStream(ctx, out, response.Body, used.Credential, p.cfg)
			response.Body.Close()
			if ctx.Err() != nil || outcome.ErrorCode == "context_canceled" || outcome.ErrorCode == "deadline_exceeded" {
				return
			}
			p.recordAccountAttempt(ctx, used.Credential.ID, outcome.Success, response.StatusCode, http.StatusText(response.StatusCode), outcome.ErrorCode)
			if outcome.Retryable && !outcome.EmittedOutput {
				lastErr = errors.New(outcome.ErrorText)
				continue
			}
			return
		}
		if lastErr == nil {
			lastErr = providerUnavailableError(p.cfg.Name, "没有可用的 Codex OAuth 凭据")
		}
		emitProviderEvent(ctx, out, Event{Type: "error", Text: boundedCodexError(lastErr.Error())})
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
	// Grants are filed under the credential store's provider name, never the
	// user-editable config name, so a renamed Codex provider still matches.
	granted, err := gatewayAccountIDSet(ctx, p.gatewayAccountPolicy(), codexauth.DefaultProviderName)
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
		return item, newCodexCredentialFailure("access_token_expired", providerUnavailableError(p.cfg.Name, "Codex access token 已过期且没有 refresh_token").Error(), true)
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
		return item, newCodexCredentialFailure("refresh_token_missing", providerUnavailableError(p.cfg.Name, "Codex 凭据缺少 refresh_token").Error(), true)
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
	// The `version` header is read by the ChatGPT backend as the *Codex CLI*
	// version and gates model access: newer slugs reject anything below their
	// minimum with "requires a newer version of Codex" (HTTP 400). Autoto's own
	// version is unrelated to that scale, so sending it locked every current
	// model out. Omitting the header lets the backend apply its default.
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
	query.Set("client_version", codexCatalogClientVersion)
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

type codexCredentialFailure struct {
	code      string
	message   string
	retryable bool
}

func newCodexCredentialFailure(code, message string, retryable bool) error {
	return &codexCredentialFailure{
		code:      safeTelemetryCode(code, "credential_error"),
		message:   boundedCodexError(message),
		retryable: retryable,
	}
}

func (e *codexCredentialFailure) Error() string {
	if e == nil {
		return "Codex credential error"
	}
	return e.message
}

func shouldTryNextCodexRequestError(err error) bool {
	var failure *codexCredentialFailure
	return errors.As(err, &failure) && failure.retryable
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
	var failure *codexCredentialFailure
	if errors.As(err, &failure) {
		return safeTelemetryCode(failure.code, "credential_error")
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
		"model":  model,
		"input":  input,
		"store":  false,
		"stream": true,
	}
	// tool_choice and parallel_tool_calls are only valid when tools are
	// present; the Responses API returns 400 when they appear with an empty
	// tool list.
	tools := codexToolParams(req.Tools, req.EnableImageGeneration)
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
		payload["parallel_tool_calls"] = true
	}
	// reasoning.encrypted_content is only meaningful when reasoning is active;
	// include it only then to avoid unexpected 400s on models that do not
	// support the include parameter.
	if reasoningEffort != "" {
		payload["reasoning"] = map[string]any{"effort": reasoningEffort, "summary": "auto"}
		payload["include"] = []string{"reasoning.encrypted_content"}
	}
	if instructions := strings.TrimSpace(req.SystemPrompt); instructions != "" {
		payload["instructions"] = instructions
	}
	// max_output_tokens is deliberately dropped: the ChatGPT Codex backend
	// rejects it outright ("Unsupported parameter: max_output_tokens", HTTP
	// 400). Callers that set a budget — provider message tests, conversation
	// titles, danger reflection, context ask, gateway max_tokens — would fail
	// every request. Output length is bounded by the model's own limit instead.
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
		// The ChatGPT Codex backend answers "System messages are not allowed"
		// (HTTP 400) for system input items — the run's system text belongs in
		// `instructions`. Turn-scoped system controls (spec tasks, silent
		// progress) still need to reach the model, so they ride as developer
		// messages, which the backend accepts with the same precedence.
		if role == "system" {
			role = "developer"
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
			case "image_generation":
				id := strings.TrimSpace(block.GenerationID)
				if id == "" || len(block.Data) == 0 {
					continue
				}
				status := strings.TrimSpace(block.Status)
				if status == "" {
					status = "completed"
				}
				structured = append(structured, map[string]any{
					"type":   "image_generation_call",
					"id":     id,
					"status": status,
					"result": base64.StdEncoding.EncodeToString(block.Data),
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
	return repairCodexToolCallPairing(items)
}

// repairCodexToolCallPairing makes a transcript replayable. The Responses API
// rejects the whole request when a function_call has no matching output ("No
// tool output found for function call X") or an output has no matching call
// ("No tool call found for function call output with call_id X"), so a single
// unpaired item permanently bricks a conversation: every later message replays
// the same history and fails the same way.
//
// Transcripts drift out of pairing for reasons the provider cannot prevent — a
// run killed mid-tool never records the result, a correction supersedes the
// message that held one half of the pair, and a tool result rewritten into
// plain text for a provider without structured tool support leaves its call
// behind. Repairing here keeps the damage recoverable at the boundary that
// actually cares, rather than requiring every producer to stay in lockstep.
//
// Orphaned outputs are dropped; orphaned calls get a synthetic output, which
// preserves the assistant's action in the narrative instead of erasing it.
func repairCodexToolCallPairing(items []map[string]any) []map[string]any {
	calls := make(map[string]bool)
	outputs := make(map[string]bool)
	for _, item := range items {
		id, _ := item["call_id"].(string)
		if id == "" {
			continue
		}
		switch item["type"] {
		case "function_call":
			calls[id] = true
		case "function_call_output":
			outputs[id] = true
		}
	}
	repaired := make([]map[string]any, 0, len(items))
	emitted := make(map[string]bool, len(outputs))
	for _, item := range items {
		id, _ := item["call_id"].(string)
		switch item["type"] {
		case "function_call_output":
			// A duplicate output is as fatal as a missing one, so keep the first.
			if id == "" || !calls[id] || emitted[id] {
				continue
			}
			emitted[id] = true
		case "function_call":
			repaired = append(repaired, item)
			if id != "" && !outputs[id] {
				repaired = append(repaired, map[string]any{
					"type":    "function_call_output",
					"call_id": id,
					"output":  "[tool result unavailable]",
				})
				emitted[id] = true
			}
			continue
		}
		repaired = append(repaired, item)
	}
	return repaired
}

func codexToolParams(tools []ToolSpec, enableImageGeneration bool) []map[string]any {
	out := make([]map[string]any, 0, len(tools)+1)
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
	if enableImageGeneration {
		out = append(out, map[string]any{"type": "image_generation", "output_format": "png"})
	}
	return out
}

type codexStreamOutcome struct {
	Success       bool
	ErrorCode     string
	ErrorText     string
	Retryable     bool
	EmittedOutput bool
}

func handleCodexResponsesStream(ctx context.Context, out chan<- Event, body io.Reader, credential codexauth.Credential, cfg config.ProviderConfig) codexStreamOutcome {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	emittedCalls := map[string]bool{}
	emittedOutput := false
	imageTracker := newImageGenerationTracker()
	streamFailure := func(code, message, fallback, fallbackCode string) codexStreamOutcome {
		safeCode := sanitizeCodexErrorCode(code, credential, cfg)
		if safeCode == "" {
			safeCode = fallbackCode
		}
		errorText := safeCodexEventError(code, message, fallback, credential, cfg)
		retryable := !emittedOutput && isCodexSSEFailoverCode(code)
		if !retryable {
			emitProviderEvent(ctx, out, Event{Type: "error", Text: errorText})
		}
		return codexStreamOutcome{
			ErrorCode:     safeTelemetryCode(safeCode, fallbackCode),
			ErrorText:     errorText,
			Retryable:     retryable,
			EmittedOutput: emittedOutput,
		}
	}
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
		imageEvents, imageErr := imageTracker.processJSON(data)
		if imageErr != nil {
			message := boundedCodexError(imageErr.Error())
			emitProviderEvent(ctx, out, Event{Type: "error", Text: message})
			return codexStreamOutcome{ErrorCode: "invalid_image_generation", ErrorText: message, EmittedOutput: emittedOutput}
		}
		for _, imageEvent := range imageEvents {
			emittedOutput = true
			if !emitProviderEvent(ctx, out, imageEvent) {
				return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
			}
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				emittedOutput = true
				if !emitProviderEvent(ctx, out, Event{Type: "text", Text: event.Delta}) {
					return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
				}
			}
		case "response.reasoning_summary_text.delta":
			// Deliberately does not set emittedOutput: reasoning is not an answer.
			if event.Delta != "" {
				if !emitProviderEvent(ctx, out, Event{Type: "reasoning", Text: event.Delta}) {
					return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
				}
			}
		case "response.output_item.done":
			if call := codexToolCallFromItem(event.Item); call != nil && !emittedCalls[call.ID] {
				emittedCalls[call.ID] = true
				emittedOutput = true
				if !emitProviderEvent(ctx, out, Event{Type: "tool_call", ToolCall: call}) {
					return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
				}
			}
		case "response.completed":
			if !emitCodexResponseToolCalls(ctx, out, event.Response.Output, emittedCalls) {
				return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
			}
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
				}
			}
			emitProviderEvent(ctx, out, Event{Type: "done", Done: true})
			return codexStreamOutcome{Success: true, EmittedOutput: emittedOutput}
		case "response.incomplete":
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
				}
			}
			reason := strings.TrimSpace(event.Response.IncompleteDetails.Reason)
			if reason == "" {
				reason = "incomplete"
			}
			emitProviderEvent(ctx, out, Event{Type: "done", Done: true, StopReason: reason})
			return codexStreamOutcome{Success: true, EmittedOutput: emittedOutput}
		case "response.failed":
			outcome := streamFailure(event.Response.Error.Code, event.Response.Error.Message, "Codex response failed", "response_failed")
			if outcome.Retryable {
				return outcome
			}
			if usage := event.Response.Usage.toUsage(); usage != (Usage{}) {
				if !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
					return codexStreamOutcome{ErrorCode: "context_canceled", EmittedOutput: emittedOutput}
				}
			}
			return outcome
		case "error":
			return streamFailure(event.Code, event.Message, "Codex stream error", "stream_error")
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return codexStreamOutcome{ErrorCode: telemetryErrorCode(ctx.Err()), EmittedOutput: emittedOutput}
		}
		message := "Codex 响应流读取失败"
		emitProviderEvent(ctx, out, Event{Type: "error", Text: message})
		return codexStreamOutcome{ErrorCode: "stream_read_error", ErrorText: message, EmittedOutput: emittedOutput}
	}
	message := "Codex 响应流在完成前关闭"
	emitProviderEvent(ctx, out, Event{Type: "error", Text: message})
	return codexStreamOutcome{ErrorCode: "stream_closed", ErrorText: message, EmittedOutput: emittedOutput}
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

type codexReasoningLevel struct {
	Effort string `json:"effort"`
}

type codexModelCatalogEntry struct {
	Slug string `json:"slug"`
	ID   string `json:"id"`
	// SupportedReasoningLevels is the authoritative per-model effort list. The
	// levels genuinely differ — gpt-5.6-terra serves "ultra", gpt-5.6-luna stops
	// at "max", gpt-5.5 stops at "xhigh" — and asking for one a model does not
	// serve is answered with HTTP 400, so this cannot be inferred from the
	// provider or guessed from the model name.
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	FastMode                 *bool                 `json:"fast_mode"`
	SupportsFastMode         *bool                 `json:"supports_fast_mode"`
	ServiceTier              json.RawMessage       `json:"service_tier"`
	ServiceTiers             json.RawMessage       `json:"service_tiers"`
	SupportedServiceTiers    json.RawMessage       `json:"supported_service_tiers"`
	AdditionalSpeedTiers     json.RawMessage       `json:"additional_speed_tiers"`
	SupportedSpeedTiers      json.RawMessage       `json:"supported_speed_tiers"`
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
		if efforts := codexModelEntryReasoningEfforts(item); len(efforts) > 0 {
			current := capabilities[model]
			current.ReasoningEfforts = efforts
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

// codexModelEntryReasoningEfforts keeps only levels Autoto has a control for.
// The catalog may name a level this build does not model yet; offering it would
// produce a picker entry nothing downstream can validate or persist.
func codexModelEntryReasoningEfforts(item codexModelCatalogEntry) []string {
	efforts := make([]string, 0, len(item.SupportedReasoningLevels))
	for _, level := range item.SupportedReasoningLevels {
		if effort := strings.ToLower(strings.TrimSpace(level.Effort)); effort != "" {
			efforts = append(efforts, effort)
		}
	}
	return canonicalReasoningEffortValues(efforts)
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

func supplementCodexModelCapabilities(capabilities map[string]ModelCapabilities, models []string, baseURL string) map[string]ModelCapabilities {
	if strings.TrimRight(strings.TrimSpace(baseURL), "/") != codexauth.DefaultBaseURL {
		return capabilities
	}
	for _, rawModel := range models {
		model := strings.TrimSpace(rawModel)
		knownEfforts, known := codexKnownOfficialModelReasoningEfforts[model]
		if !known || len(knownEfforts) == 0 {
			continue
		}
		current := capabilities[model]
		// The authenticated catalog is authoritative. Only fill an omitted list;
		// never append max to an explicit list that intentionally stops at xhigh.
		if len(current.ReasoningEfforts) > 0 {
			continue
		}
		current.ReasoningEfforts = append([]string(nil), knownEfforts...)
		if capabilities == nil {
			capabilities = make(map[string]ModelCapabilities)
		}
		capabilities[model] = current
	}
	return capabilities
}

func fallbackCodexModelCapabilities(baseURL string) map[string]ModelCapabilities {
	if strings.TrimRight(strings.TrimSpace(baseURL), "/") != codexauth.DefaultBaseURL {
		return make(map[string]ModelCapabilities)
	}
	models, capabilities, err := parseCodexModelCatalog(strings.NewReader(codexFallbackFastModelCatalog))
	if err != nil {
		return make(map[string]ModelCapabilities)
	}
	return supplementCodexModelCapabilities(capabilities, models, baseURL)
}

func fallbackCodexModels(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = codexauth.DefaultModel
	}
	return []string{model}
}

func shouldTryNextCodexCredential(status int, code string) bool {
	if status == http.StatusUnauthorized || status == http.StatusTooManyRequests {
		return true
	}
	return status == http.StatusForbidden && isCodexSSEFailoverCode(code)
}

func isCodexSSEFailoverCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "authentication_error", "invalid_authentication", "invalid_token", "access_token_expired", "token_expired", "unauthorized", "forbidden", "permission_denied", "insufficient_permissions", "rate_limit_error", "rate_limit_exceeded", "too_many_requests", "insufficient_quota":
		return true
	default:
		return false
	}
}

func codexHTTPErrorDetails(response *http.Response, credential codexauth.Credential, cfg config.ProviderConfig, prefix string) (error, string) {
	code, message, detail := codexJSONErrorFields(response.Body)
	safeCode := sanitizeCodexErrorCode(code, credential, cfg)
	if safeCode == "" {
		safeCode = fmt.Sprintf("http_%d", response.StatusCode)
	}
	message = codexUpstreamHint(safeCode, message, detail, credential, cfg)
	var text string
	switch {
	case safeCode != "" && message != "":
		text = fmt.Sprintf("%s：HTTP %d (%s)：%s", prefix, response.StatusCode, safeCode, message)
	case safeCode != "":
		text = fmt.Sprintf("%s：HTTP %d (%s)", prefix, response.StatusCode, safeCode)
	default:
		text = fmt.Sprintf("%s：HTTP %d", prefix, response.StatusCode)
	}
	return errors.New(boundedCodexError(text)), safeTelemetryCode(safeCode, fmt.Sprintf("http_%d", response.StatusCode))
}

// codexJSONErrorFields splits an upstream failure body into a code, a free-form
// upstream message, and the ChatGPT backend's `detail` field. detail is returned
// separately because the two message channels carry different trust: see
// codexUpstreamHint.
func codexJSONErrorFields(reader io.Reader) (string, string, string) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return "", "", ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return "", "", ""
	}
	code := codexRawJSONString(root["code"])
	message := codexRawJSONString(root["message"])
	if description := codexRawJSONString(root["error_description"]); message == "" {
		message = description
	}
	detail := codexRawJSONString(root["detail"])
	if rawError := root["error"]; len(rawError) > 0 {
		if text := codexRawJSONString(rawError); text != "" {
			if code == "" {
				code = text
			}
		} else {
			var nested map[string]json.RawMessage
			if json.Unmarshal(rawError, &nested) == nil {
				if nestedCode := codexRawJSONString(nested["code"]); nestedCode != "" {
					code = nestedCode
				}
				if nestedMessage := codexRawJSONString(nested["message"]); nestedMessage != "" {
					message = nestedMessage
				} else if nestedDescription := codexRawJSONString(nested["error_description"]); nestedDescription != "" {
					message = nestedDescription
				}
				// Request-validation rejections arrive as type
				// "invalid_request_error" with a null code, so the code alone
				// cannot classify them. The message names the offending input
				// ("No tool output found for function call X") and is the only
				// way to tell a malformed transcript from an outage.
				if codexRawJSONString(nested["type"]) == "invalid_request_error" && detail == "" {
					detail = codexRawJSONString(nested["message"])
				}
			}
		}
	}
	return strings.TrimSpace(code), strings.TrimSpace(message), strings.TrimSpace(detail)
}

// codexUpstreamHint decides how much of an upstream failure body may reach the
// operator, and is the single place that decision is made.
//
// `detail` is the ChatGPT Codex backend's request-rejection channel. It names
// the parameter, model or account condition the request got wrong — "Unsupported
// parameter: max_output_tokens", "System messages are not allowed", "requires a
// newer version of Codex" — and describes the request rather than the account,
// so it is always forwarded. Without it a rejected request surfaced only as
// "HTTP 400 (http_400)", which named no cause at all.
//
// `message` is the arbitrary upstream error channel and may carry unbounded
// internal diagnostics, so it stays restricted to codes that have been vetted as
// actionable. Both paths run through redactCodexSecrets, which scrubs tokens,
// JWTs and proxy credentials and bounds the length.
func codexUpstreamHint(code, message, detail string, credential codexauth.Credential, cfg config.ProviderConfig) string {
	hint := detail
	if hint == "" && codexSafeUpstreamHintCode(code) {
		hint = message
	}
	if hint == "" {
		return ""
	}
	redacted, _ := redactCodexSecrets(strings.TrimSpace(hint), credential, cfg)
	return redacted
}

func codexSafeUpstreamHintCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "image_generation_not_enabled":
		return true
	default:
		return false
	}
}

func codexRawJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func codexRefreshError(status int, reader io.Reader) error {
	code, description, detail := codexJSONErrorFields(reader)
	if description == "" {
		description = detail
	}
	canonical := classifyCodexRefreshFailure(code, description, status)
	message := fmt.Sprintf("Codex OAuth 刷新失败（HTTP %d）", status)
	retryable := false
	switch canonical {
	case "refresh_token_expired":
		message = "Codex refresh_token 已过期，请重新导入凭据"
		retryable = true
	case "refresh_token_reused":
		message = "Codex refresh_token 已被使用，请重新导入最新凭据"
		retryable = true
	case "refresh_token_invalidated":
		message = "Codex refresh_token 已被撤销，请重新登录后导入"
		retryable = true
	case "invalid_grant", "invalid_refresh_token":
		message = "Codex refresh_token 无效，请重新登录后导入"
		retryable = true
	case "oauth_unauthorized", "oauth_rate_limited":
		retryable = true
	}
	return newCodexCredentialFailure(canonical, providerUnavailableError(codexauth.DefaultProviderName, message).Error(), retryable)
}

func classifyCodexRefreshFailure(code, description string, status int) string {
	normalizedCode := strings.ToLower(strings.TrimSpace(code))
	detail := normalizedCode + " " + strings.ToLower(strings.TrimSpace(description))
	switch {
	case strings.Contains(detail, "reus") || strings.Contains(detail, "already used"):
		return "refresh_token_reused"
	case strings.Contains(detail, "expir"):
		return "refresh_token_expired"
	case strings.Contains(detail, "revok") || strings.Contains(detail, "invalidat"):
		return "refresh_token_invalidated"
	}
	switch normalizedCode {
	case "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
		return normalizedCode
	case "invalid_grant":
		return "invalid_grant"
	case "invalid_refresh_token", "refresh_token_invalid":
		return "invalid_refresh_token"
	}
	if status == http.StatusUnauthorized {
		return "oauth_unauthorized"
	}
	if status == http.StatusTooManyRequests {
		return "oauth_rate_limited"
	}
	return fmt.Sprintf("oauth_http_%d", status)
}

func safeCodexEventError(code, message, fallback string, credential codexauth.Credential, cfg config.ProviderConfig) string {
	safeCode := sanitizeCodexErrorCode(code, credential, cfg)
	// SSE failure events carry only the free-form upstream message channel, so
	// the vetted-code restriction applies; see codexUpstreamHint.
	message = codexUpstreamHint(safeCode, message, "", credential, cfg)
	var text string
	switch {
	case safeCode != "" && message != "":
		text = fmt.Sprintf("%s (%s)：%s", fallback, safeCode, message)
	case safeCode != "":
		text = fmt.Sprintf("%s (%s)", fallback, safeCode)
	case message != "":
		text = fallback + "：" + message
	default:
		text = fallback
	}
	return boundedCodexError(text)
}
