package providers

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
)

type anthropicAccountCandidate struct {
	id       string
	priority int
	oauth    bool
	client   anthropic.Client
}

func (p *AnthropicProvider) SetAccountTelemetry(telemetry AccountTelemetry) {
	if p == nil {
		return
	}
	p.telemetry = telemetry
	if quotaTelemetry, ok := telemetry.(AccountQuotaTelemetry); ok {
		p.quotaTelemetry = quotaTelemetry
	}
}

func (p *AnthropicProvider) SetAccountQuotaTelemetry(telemetry AccountQuotaTelemetry) {
	if p != nil {
		p.quotaTelemetry = telemetry
	}
}

func (p *AnthropicProvider) accountCandidates(ctx context.Context, req GenerateRequest) ([]anthropicAccountCandidate, error) {
	if p == nil {
		return nil, providerUnavailableError(anthropicauth.DefaultProviderName, "provider is not configured")
	}
	if ctx == nil {
		return nil, errors.New("Anthropic request context is required")
	}
	scenario := req.EffectiveScenario()
	var granted map[string]struct{}
	var grantErr error
	if scenario == CallScenarioGateway && req.AllowSubscriptionCredentials {
		granted, grantErr = gatewayAccountIDSet(ctx, p.gatewayAccountPolicy(), p.cfg.Name)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	gatewayAccountAllowed := func(id string) bool {
		if scenario != CallScenarioGateway {
			return true
		}
		if !req.AllowSubscriptionCredentials {
			return false
		}
		_, ok := granted[id]
		return ok
	}
	var items []anthropicauth.StoredCredential
	var loadErr error
	if p.store != nil && strings.TrimSpace(p.cfg.CredentialStorePath) != "" {
		items, loadErr = p.store.Load()
	}
	configuredKey := strings.TrimSpace(p.cfg.APIKey)
	managedConfiguredKeyIncluded := false
	candidates := make([]anthropicAccountCandidate, 0, len(items)+1)
	for index := range items {
		item := items[index]
		credential := item.Credential
		if credential.Disabled {
			continue
		}
		if scenario == CallScenarioGateway {
			if credential.AuthType == anthropicauth.AuthTypeProfile || !req.AllowSubscriptionCredentials {
				continue
			}
			if _, ok := granted[credential.ID]; !ok {
				continue
			}
		}
		client, err := p.clientForCredential(ctx, credential)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if credential.AuthType == anthropicauth.AuthTypeAPIKey && sameAnthropicAPIKey(credential.APIKey, configuredKey) {
			managedConfiguredKeyIncluded = true
		}
		candidates = append(candidates, anthropicAccountCandidate{
			id:       credential.ID,
			priority: credential.Priority,
			oauth:    credential.AuthType == anthropicauth.AuthTypeOAuth,
			client:   client,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].id < candidates[j].id
	})
	if configuredKey != "" && !managedConfiguredKeyIncluded && gatewayAccountAllowed(configuredCredentialID) {
		client, err := p.newAnthropicClient(option.WithAPIKey(configuredKey))
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, anthropicAccountCandidate{
			id:       configuredCredentialID,
			priority: int(^uint(0) >> 1),
			client:   client,
		})
	}
	if len(candidates) > 0 {
		return candidates, nil
	}
	if loadErr != nil {
		return nil, providerUnavailableError(p.cfg.Name, "Anthropic 本地凭据库不可用")
	}
	if grantErr != nil {
		return nil, providerUnavailableError(p.cfg.Name, "Gateway account grants are unavailable")
	}
	return nil, providerUnavailableError(p.cfg.Name, "Anthropic credentials are not configured")
}

func sameAnthropicAPIKey(first, second string) bool {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	return first != "" && len(first) == len(second) && subtle.ConstantTimeCompare([]byte(first), []byte(second)) == 1
}

func (p *AnthropicProvider) clientForCredential(ctx context.Context, credential anthropicauth.Credential) (anthropic.Client, error) {
	switch credential.AuthType {
	case anthropicauth.AuthTypeProfile:
		if strings.TrimSpace(credential.Profile) == "" {
			return anthropic.Client{}, errors.New("Anthropic profile is empty")
		}
		return p.newAnthropicClient(option.WithProfile(credential.Profile))
	case anthropicauth.AuthTypeAPIKey:
		if strings.TrimSpace(credential.APIKey) == "" {
			return anthropic.Client{}, errors.New("Anthropic API key is empty")
		}
		return p.newAnthropicClient(option.WithAPIKey(credential.APIKey))
	case anthropicauth.AuthTypeOAuth:
		access, err := p.anthropicOAuthAccessToken(ctx, credential)
		if err != nil {
			return anthropic.Client{}, err
		}
		// Subscription OAuth tokens are only accepted when the request looks
		// like Claude Code (headers + first system identity block). Override
		// the Go SDK defaults that would otherwise fingerprint as third-party.
		return p.newAnthropicClient(
			option.WithAuthToken(access),
			option.WithHeader("anthropic-beta", anthropicauth.OAuthMessagesBetaHeader()),
			option.WithHeader("User-Agent", anthropicauth.ClaudeCodeUserAgent),
			option.WithHeader("X-App", anthropicauth.ClaudeCodeAppHeader),
			option.WithHeader("X-Stainless-Lang", "js"),
			option.WithHeader("X-Stainless-Runtime", "node"),
			option.WithHeader("X-Stainless-Package-Version", anthropicauth.ClaudeCodePackageVersion),
			option.WithHeader("X-Stainless-Runtime-Version", anthropicauth.ClaudeCodeRuntimeVersion),
		)
	default:
		return anthropic.Client{}, errors.New("Anthropic auth type is invalid")
	}
}

func (p *AnthropicProvider) newAnthropicClient(auth ...option.RequestOption) (anthropic.Client, error) {
	cfg := config.ProviderConfig{}
	if p != nil {
		cfg = p.cfg
	}
	httpClient, err := providerHTTPClient(cfg, 90*time.Second)
	if err != nil {
		return anthropic.Client{}, err
	}
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
	}
	opts = append(opts, auth...)
	if strings.TrimSpace(cfg.BaseURL) != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return anthropic.NewClient(opts...), nil
}

const anthropicOAuthRefreshAhead = 2 * time.Minute

// anthropicOAuthAccessToken returns a usable access token for an OAuth
// credential, refreshing through the request context and merging concurrent
// refreshes. An actually expired token is never returned after refresh failure.
func (p *AnthropicProvider) anthropicOAuthAccessToken(ctx context.Context, credential anthropicauth.Credential) (string, error) {
	if ctx == nil {
		return "", errors.New("Anthropic request context is required")
	}
	access := strings.TrimSpace(credential.OAuthAccess)
	if access == "" {
		return "", errors.New("Anthropic OAuth access token is empty")
	}
	needsRefresh, expired, err := p.anthropicOAuthExpiry(credential)
	if err != nil {
		return "", providerUnavailableError(p.cfg.Name, "Anthropic OAuth expiry is invalid")
	}
	if !needsRefresh {
		return access, nil
	}
	if strings.TrimSpace(credential.OAuthRefresh) == "" {
		if expired {
			return "", providerUnavailableError(p.cfg.Name, "Anthropic OAuth access token is expired")
		}
		return access, nil
	}

	select {
	case p.oauthRefreshGate <- struct{}{}:
		defer func() { <-p.oauthRefreshGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if p.store != nil && strings.TrimSpace(credential.ID) != "" {
		if current, currentErr := p.store.GetByID(credential.ID); currentErr == nil {
			if current.Credential.Disabled {
				return "", providerUnavailableError(p.cfg.Name, "Anthropic OAuth account is disabled")
			}
			if current.Credential.AuthType == anthropicauth.AuthTypeOAuth {
				credential = current.Credential
				access = strings.TrimSpace(credential.OAuthAccess)
				needsRefresh, expired, err = p.anthropicOAuthExpiry(credential)
				if err != nil {
					return "", providerUnavailableError(p.cfg.Name, "Anthropic OAuth expiry is invalid")
				}
				if access == "" {
					return "", errors.New("Anthropic OAuth access token is empty")
				}
				if !needsRefresh {
					return access, nil
				}
			}
		}
	}
	refreshToken := strings.TrimSpace(credential.OAuthRefresh)
	if refreshToken == "" {
		if expired {
			return "", providerUnavailableError(p.cfg.Name, "Anthropic OAuth access token is expired")
		}
		return access, nil
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	refreshConfig := p.cfg
	// Provider API headers belong to the configured Anthropic API origin and
	// must not be forwarded to the separate OAuth token endpoint.
	refreshConfig.RequestHeaders = nil
	httpClient, clientErr := providerHTTPClient(refreshConfig, 30*time.Second)
	if clientErr != nil {
		return "", providerUnavailableError(p.cfg.Name, "Anthropic OAuth refresh client is unavailable")
	}
	refresh := p.oauthRefreshToken
	if refresh == nil {
		refresh = anthropicauth.RefreshTokens
	}
	tokens, refreshErr := refresh(refreshCtx, refreshToken, httpClient)
	if refreshErr != nil || strings.TrimSpace(tokens.AccessToken) == "" {
		if refreshCtx.Err() != nil {
			return "", refreshCtx.Err()
		}
		_, expired, expiryErr := p.anthropicOAuthExpiry(credential)
		if expiryErr != nil || expired {
			return "", providerUnavailableError(p.cfg.Name, "Anthropic OAuth refresh failed for an expired token")
		}
		return access, nil
	}
	tokens.AccessToken = strings.TrimSpace(tokens.AccessToken)
	tokens.RefreshToken = strings.TrimSpace(tokens.RefreshToken)
	expiresAt := ""
	if tokens.ExpiresIn > 0 {
		expiresAt = p.now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	if p.store != nil && strings.TrimSpace(credential.ID) != "" {
		_, _ = p.store.UpdateOAuthTokens(credential.ID, tokens.AccessToken, tokens.RefreshToken, expiresAt)
	}
	return tokens.AccessToken, nil
}

func (p *AnthropicProvider) anthropicOAuthExpiry(credential anthropicauth.Credential) (needsRefresh, expired bool, err error) {
	raw := strings.TrimSpace(credential.OAuthExpiresAt)
	if raw == "" {
		return false, false, nil
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return false, false, err
	}
	now := p.now()
	return !now.Before(expiresAt.Add(-anthropicOAuthRefreshAhead)), !now.Before(expiresAt), nil
}

func (p *AnthropicProvider) SyncAccount(ctx context.Context, id string) (anthropicauth.AccountSummary, []string, ProviderAccountQuotaSnapshot, error) {
	if p == nil || p.store == nil {
		return anthropicauth.AccountSummary{}, nil, ProviderAccountQuotaSnapshot{}, providerUnavailableError(anthropicauth.DefaultProviderName, "本地凭据库未配置")
	}
	if p.configErr != nil {
		return anthropicauth.AccountSummary{}, nil, ProviderAccountQuotaSnapshot{}, p.configErr
	}
	if err := ctx.Err(); err != nil {
		return anthropicauth.AccountSummary{}, nil, ProviderAccountQuotaSnapshot{}, err
	}
	item, err := p.store.GetByID(id)
	if err != nil {
		return anthropicauth.AccountSummary{}, nil, ProviderAccountQuotaSnapshot{}, err
	}
	client, err := p.clientForCredential(ctx, item.Credential)
	if err != nil {
		return anthropicauth.AccountSummary{}, nil, ProviderAccountQuotaSnapshot{}, providerUnavailableError(p.cfg.Name, "Anthropic account credential is invalid")
	}
	models, response, err := p.listModelsWithClient(ctx, client)
	quota := anthropicQuotaSnapshot(p.cfg.Name, item.Credential.ID, response, p.now())
	quota.Models = append([]string(nil), models...)
	p.recordAccountQuota(ctx, quota)
	if err != nil {
		return anthropicauth.Summary(item), nil, quota, sanitizeAnthropicError(ctx, p.cfg.Name, err)
	}
	return anthropicauth.Summary(item), models, quota, nil
}

func (p *AnthropicProvider) listModelsWithClient(ctx context.Context, client anthropic.Client) ([]string, *http.Response, error) {
	var response *http.Response
	page, err := client.Models.List(ctx, anthropic.ModelListParams{}, option.WithResponseInto(&response))
	if err != nil {
		if apiErr := anthropicAPIError(err); apiErr != nil && apiErr.Response != nil {
			response = apiErr.Response
		}
		return nil, response, err
	}
	p.rememberAnthropicThinkingSupport(page.Data)
	models := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		if id := strings.TrimSpace(model.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, response, nil
}

func (p *AnthropicProvider) recordAccountAttempt(ctx context.Context, accountID string, success bool, httpStatus int, err error) {
	if p == nil || p.telemetry == nil || strings.TrimSpace(accountID) == "" || ctx == nil || ctx.Err() != nil {
		return
	}
	status, errorType := anthropicErrorMetadata(err)
	if httpStatus > 0 {
		status = httpStatus
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = p.telemetry.RecordProviderAccountAttempt(recordCtx, ProviderAccountAttempt{
		Provider:    p.cfg.Name,
		AccountID:   accountID,
		Success:     success,
		HTTPStatus:  status,
		StatusCode:  safeTelemetryCode(http.StatusText(status), ""),
		ErrorCode:   safeTelemetryCode(errorType, ""),
		AttemptedAt: p.now().UTC(),
	})
}

func (p *AnthropicProvider) recordAccountQuota(ctx context.Context, snapshot ProviderAccountQuotaSnapshot) {
	if p == nil || p.quotaTelemetry == nil || strings.TrimSpace(snapshot.AccountID) == "" || ctx == nil || ctx.Err() != nil || snapshot.FetchedAt.IsZero() || !hasAnthropicQuotaData(snapshot) {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = p.quotaTelemetry.UpdateProviderAccountQuota(recordCtx, snapshot.Provider, snapshot.AccountID, snapshot, snapshot.FetchedAt)
}

func hasAnthropicQuotaData(snapshot ProviderAccountQuotaSnapshot) bool {
	limits := []AccountRateLimitSnapshot{snapshot.Requests, snapshot.InputTokens, snapshot.OutputTokens}
	for _, limit := range limits {
		if limit.Limit != "" || limit.Remaining != "" || limit.Reset != "" {
			return true
		}
	}
	return snapshot.RetryAfter != "" || len(snapshot.Models) > 0
}

func (p *AnthropicProvider) now() time.Time {
	if p != nil && p.clock != nil {
		return p.clock()
	}
	return time.Now()
}

func anthropicResponseStatus(response *http.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}

func anthropicQuotaSnapshot(provider, accountID string, response *http.Response, fetchedAt time.Time) ProviderAccountQuotaSnapshot {
	if response == nil {
		return ProviderAccountQuotaSnapshot{}
	}
	header := response.Header
	return ProviderAccountQuotaSnapshot{
		Provider:  provider,
		AccountID: accountID,
		Requests: AccountRateLimitSnapshot{
			Limit:     header.Get("anthropic-ratelimit-requests-limit"),
			Remaining: header.Get("anthropic-ratelimit-requests-remaining"),
			Reset:     header.Get("anthropic-ratelimit-requests-reset"),
		},
		InputTokens: AccountRateLimitSnapshot{
			Limit:     header.Get("anthropic-ratelimit-input-tokens-limit"),
			Remaining: header.Get("anthropic-ratelimit-input-tokens-remaining"),
			Reset:     header.Get("anthropic-ratelimit-input-tokens-reset"),
		},
		OutputTokens: AccountRateLimitSnapshot{
			Limit:     header.Get("anthropic-ratelimit-output-tokens-limit"),
			Remaining: header.Get("anthropic-ratelimit-output-tokens-remaining"),
			Reset:     header.Get("anthropic-ratelimit-output-tokens-reset"),
		},
		RetryAfter: header.Get("retry-after"),
		FetchedAt:  fetchedAt.UTC(),
	}
}

func shouldTryNextAnthropicAccount(ctx context.Context, err error) bool {
	if err == nil || (ctx != nil && ctx.Err() != nil) || isContextTermination(err) {
		return false
	}
	if apiErr := anthropicAPIError(err); apiErr != nil {
		status := apiErr.StatusCode
		errorType := string(apiErr.Type())
		return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError || errorType == "rate_limit_error" || errorType == "overloaded_error"
	}
	var netErr net.Error
	var urlErr *url.Error
	return errors.As(err, &netErr) || errors.As(err, &urlErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func anthropicAPIError(err error) *anthropic.Error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return nil
}

func anthropicErrorMetadata(err error) (int, string) {
	if apiErr := anthropicAPIError(err); apiErr != nil {
		return apiErr.StatusCode, string(apiErr.Type())
	}
	if isContextTermination(err) {
		return 0, telemetryErrorCode(err)
	}
	if err != nil {
		return 0, "network_error"
	}
	return 0, ""
}

func sanitizeAnthropicError(ctx context.Context, provider string, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if isContextTermination(err) {
		return err
	}
	if apiErr := anthropicAPIError(err); apiErr != nil {
		typeName := strings.TrimSpace(string(apiErr.Type()))
		if typeName == "" {
			typeName = http.StatusText(apiErr.StatusCode)
		}
		if typeName == "" {
			return fmt.Errorf("%s request failed: HTTP %d", provider, apiErr.StatusCode)
		}
		return fmt.Errorf("%s request failed: HTTP %d (%s)", provider, apiErr.StatusCode, typeName)
	}
	return providerUnavailableError(provider, "Anthropic request failed")
}
