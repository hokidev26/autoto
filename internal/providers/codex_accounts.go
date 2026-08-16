package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/config"
)

type ProviderAccountAttempt struct {
	Provider    string
	AccountID   string
	Success     bool
	HTTPStatus  int
	StatusCode  string
	ErrorCode   string
	AttemptedAt time.Time
}

type AccountTelemetry interface {
	RecordProviderAccountAttempt(context.Context, ProviderAccountAttempt) error
}

type ProviderAccountQuotaSnapshot struct {
	Provider  string                   `json:"-"`
	AccountID string                   `json:"-"`
	Requests  AccountRateLimitSnapshot `json:"requests"`
	// Tokens carries a combined token budget for upstreams that do not split
	// input from output. The subscription CLI proxies report it this way.
	Tokens       AccountRateLimitSnapshot `json:"tokens"`
	InputTokens  AccountRateLimitSnapshot `json:"input_tokens"`
	OutputTokens AccountRateLimitSnapshot `json:"output_tokens"`
	Models       []string                 `json:"models,omitempty"`
	// ModelQuotas carries per-model remaining share for upstreams that report a
	// fraction with no absolute limit, so there is no remaining/limit pair to
	// put in the buckets above. Google's Cloud Code is the case in point.
	ModelQuotas []AccountModelQuotaSnapshot `json:"model_quotas,omitempty"`
	RetryAfter  string                      `json:"retry_after,omitempty"`
	FetchedAt   time.Time                   `json:"fetched_at"`
}

// AccountModelQuotaSnapshot is a per-model remaining percentage. RemainingPercent
// is 0-100 and always meaningful: upstreams that omit the figure when exhausted
// are normalized to 0 by the parser rather than left absent, because an absent
// value rendered as "unknown" hides exactly the state users care about.
type AccountModelQuotaSnapshot struct {
	Model            string `json:"model"`
	DisplayName      string `json:"displayName,omitempty"`
	RemainingPercent int    `json:"remainingPercent"`
	Reset            string `json:"reset,omitempty"`
	Disabled         bool   `json:"disabled,omitempty"`
}

type AccountRateLimitSnapshot struct {
	Limit     string `json:"limit,omitempty"`
	Remaining string `json:"remaining,omitempty"`
	Reset     string `json:"reset,omitempty"`
}

type AccountQuotaTelemetry interface {
	UpdateProviderAccountQuota(context.Context, string, string, any, time.Time) error
}

var errCodexQuotaUnauthorized = errors.New("codex quota unauthorized")

const codexQuotaUnauthorizedCode = "quota_unauthorized"

func ValidateCodexProviderConfig(cfg config.ProviderConfig) error {
	if _, err := codexRefreshEndpointForConfig(cfg); err != nil {
		return err
	}
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("Codex Base URL 无效")
	}
	if cfg.CodexAllowInsecureTestEndpoint {
		if !codexLoopbackHost(base.Hostname()) || (base.Scheme != "http" && base.Scheme != "https") {
			return errors.New("Codex 测试端点必须是 loopback HTTP(S)")
		}
		if cfg.CodexUsageURL != "" {
			usage, usageErr := url.Parse(cfg.CodexUsageURL)
			if usageErr != nil || usage.User != nil || !sameCodexOrigin(base, usage) {
				return errors.New("Codex 测试额度端点必须与 Base URL 同源")
			}
		}
		return nil
	}
	if base.Scheme != "https" || !officialCodexHost(base.Hostname()) || strings.TrimRight(base.EscapedPath(), "/") != "/backend-api/codex" {
		return errors.New("Codex Base URL 仅允许官方 HTTPS /backend-api/codex 端点")
	}
	if port := base.Port(); port != "" && port != "443" {
		return errors.New("Codex Base URL 仅允许官方 HTTPS 端口")
	}
	if strings.TrimSpace(cfg.CodexUsageURL) != "" {
		return errors.New("生产 Codex 配置不允许自定义额度端点")
	}
	return nil
}

func officialCodexHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "chatgpt.com" || host == "chat.openai.com"
}

func codexLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameCodexOrigin(first, second *url.URL) bool {
	if first == nil || second == nil {
		return false
	}
	return strings.EqualFold(first.Scheme, second.Scheme) && strings.EqualFold(first.Host, second.Host)
}

// codexRefreshEndpointForConfig returns the immutable production OAuth endpoint.
// A loopback override exists solely for in-process tests that opt into the
// existing insecure-test endpoint mode; it is intentionally not persisted.
func codexRefreshEndpointForConfig(cfg config.ProviderConfig) (string, error) {
	raw := strings.TrimSpace(cfg.CodexRefreshURLForTest)
	if raw == "" {
		return codexOAuthRefreshURL, nil
	}
	if !cfg.CodexAllowInsecureTestEndpoint {
		return "", errors.New("Codex OAuth refresh endpoint 仅允许显式 loopback 测试注入")
	}
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.Opaque != "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.RawFragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("Codex OAuth 测试 refresh endpoint 无效")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return "", errors.New("Codex OAuth 测试 refresh endpoint 必须使用 HTTP(S)")
	}
	if !codexLoopbackHost(endpoint.Hostname()) {
		return "", errors.New("Codex OAuth 测试 refresh endpoint 必须是 loopback")
	}
	return endpoint.String(), nil
}

func codexRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if !sameCodexOrigin(origin, req.URL) || (strings.EqualFold(origin.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https")) {
		return errors.New("Codex redirect blocked")
	}
	return nil
}

// Refresh requests contain the refresh token in their body. OAuth refreshes do
// not need redirects, so reject every redirect before a request can be replayed.
func codexRefreshRedirectPolicy(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func (p *CodexProvider) SyncAccount(ctx context.Context, id string) (codexauth.AccountSummary, codexauth.QuotaSnapshot, error) {
	if p != nil && p.endpointErr != nil {
		return codexauth.AccountSummary{}, codexauth.QuotaSnapshot{}, providerUnavailableError(codexauth.DefaultProviderName, p.endpointErr.Error())
	}
	if p == nil || p.store == nil {
		return codexauth.AccountSummary{}, codexauth.QuotaSnapshot{}, providerUnavailableError(codexauth.DefaultProviderName, "本地凭据库未配置")
	}
	item, err := p.store.GetByID(id)
	if err != nil {
		return codexauth.AccountSummary{}, codexauth.QuotaSnapshot{}, err
	}
	prepared, err := p.prepareCredential(ctx, item)
	if err != nil {
		return codexauth.AccountSummary{}, codexauth.QuotaSnapshot{}, err
	}
	quota, err := p.fetchAccountQuota(ctx, prepared.Credential)
	if errors.Is(err, errCodexQuotaUnauthorized) && prepared.Credential.RefreshToken != "" {
		prepared, err = p.refreshCredential(ctx, prepared)
		if err == nil {
			quota, err = p.fetchAccountQuota(ctx, prepared.Credential)
		}
	}
	if errors.Is(err, errCodexQuotaUnauthorized) {
		p.recordAccountAttempt(ctx, prepared.Credential.ID, false, http.StatusUnauthorized, codexQuotaUnauthorizedCode, codexQuotaUnauthorizedCode)
	}
	if err != nil {
		return codexauth.Summary(prepared), codexauth.QuotaSnapshot{}, err
	}
	p.attachResetCredits(ctx, prepared, &quota)
	if quota.PlanType != "" && quota.PlanType != prepared.Credential.PlanType {
		prepared.Credential.PlanType = quota.PlanType
		if err := p.store.Update(prepared); err != nil {
			return codexauth.AccountSummary{}, codexauth.QuotaSnapshot{}, providerUnavailableError(p.cfg.Name, "Codex 套餐信息无法保存")
		}
	}
	return codexauth.Summary(prepared), quota, nil
}

func (p *CodexProvider) fetchAccountQuota(ctx context.Context, credential codexauth.Credential) (codexauth.QuotaSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return codexauth.QuotaSnapshot{}, err
	}
	if strings.TrimSpace(credential.AccessToken) == "" {
		return codexauth.QuotaSnapshot{}, providerUnavailableError(p.cfg.Name, "Codex 凭据缺少 access_token")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.usageURL(), nil)
	if err != nil {
		return codexauth.QuotaSnapshot{}, providerUnavailableError(p.cfg.Name, "无法构造 Codex 额度请求")
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	if credential.AccountID != "" {
		request.Header.Set("ChatGPT-Account-ID", credential.AccountID)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", p.userAgent())
	request.Header.Set("originator", "autoto")
	request.Header.Set("OpenAI-Beta", codexQuotaBetaHeader)
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return codexauth.QuotaSnapshot{}, ctx.Err()
		}
		return codexauth.QuotaSnapshot{}, providerUnavailableError(p.cfg.Name, "Codex 额度请求失败")
	}
	defer response.Body.Close()
	if err := ctx.Err(); err != nil {
		return codexauth.QuotaSnapshot{}, err
	}
	if response.StatusCode == http.StatusUnauthorized {
		return codexauth.QuotaSnapshot{}, errCodexQuotaUnauthorized
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		return codexauth.QuotaSnapshot{}, fmt.Errorf("Codex 额度请求失败：HTTP %d", response.StatusCode)
	}
	quota, err := parseCodexQuota(response.Body, p.now())
	if ctx.Err() != nil {
		return codexauth.QuotaSnapshot{}, ctx.Err()
	}
	if err != nil {
		return codexauth.QuotaSnapshot{}, providerUnavailableError(p.cfg.Name, "Codex 额度响应无效")
	}
	return quota, nil
}

func (p *CodexProvider) usageURL() string {
	if explicit := strings.TrimSpace(p.cfg.CodexUsageURL); explicit != "" {
		return explicit
	}
	parsed, err := url.Parse(strings.TrimSpace(p.cfg.BaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	const codexSuffix = "/backend-api/codex"
	if strings.HasSuffix(path, codexSuffix) {
		parsed.Path = strings.TrimSuffix(path, codexSuffix) + "/backend-api/wham/usage"
	} else {
		parsed.Path = path + "/backend-api/wham/usage"
	}
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func parseCodexQuota(reader io.Reader, now time.Time) (codexauth.QuotaSnapshot, error) {
	data, err := io.ReadAll(io.LimitReader(reader, codexMaxResponseBytes+1))
	if err != nil || len(data) > codexMaxResponseBytes {
		return codexauth.QuotaSnapshot{}, errors.New("invalid quota response")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return codexauth.QuotaSnapshot{}, errors.New("invalid quota response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return codexauth.QuotaSnapshot{}, errors.New("invalid quota response")
	}
	quota := codexauth.QuotaSnapshot{
		PlanType:             flexString(root, "plan_type", "planType", "subscription_plan", "subscriptionPlan"),
		RateLimitReachedType: flexString(root, "rate_limit_reached_type", "rateLimitReachedType"),
		FetchedAt:            now.UTC().Format(time.RFC3339Nano),
	}
	rateLimit := flexMap(root, "rate_limit", "rateLimit")
	if rateLimit == nil {
		rateLimit = root
	}
	quota.PrimaryWindow = parseRateLimitWindow(flexMap(rateLimit, "primary_window", "primaryWindow", "primary"))
	quota.SecondaryWindow = parseRateLimitWindow(flexMap(rateLimit, "secondary_window", "secondaryWindow", "secondary"))
	quota.Credits = parseCreditBalance(flexMap(root, "credits", "credit_balance", "creditBalance"))
	quota.RateLimitResetCredits = parseResetCreditsMap(flexMap(root, "rate_limit_reset_credits", "rateLimitResetCredits"))
	for _, raw := range flexSlice(root, "additional_rate_limits", "additionalRateLimits") {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		limits := flexMap(item, "rate_limit", "rateLimit")
		if limits == nil {
			limits = item
		}
		additional := codexauth.AdditionalRateLimit{
			Name:            flexString(item, "name", "label", "limit_name", "limitName"),
			Model:           flexString(item, "model", "model_id", "modelId"),
			PrimaryWindow:   parseRateLimitWindow(flexMap(limits, "primary_window", "primaryWindow", "primary")),
			SecondaryWindow: parseRateLimitWindow(flexMap(limits, "secondary_window", "secondaryWindow", "secondary")),
		}
		if additional.Name != "" || additional.Model != "" || additional.PrimaryWindow != nil || additional.SecondaryWindow != nil {
			quota.AdditionalRateLimits = append(quota.AdditionalRateLimits, additional)
		}
	}
	stampCodexWindowResetAt(quota.PrimaryWindow, now)
	stampCodexWindowResetAt(quota.SecondaryWindow, now)
	for i := range quota.AdditionalRateLimits {
		stampCodexWindowResetAt(quota.AdditionalRateLimits[i].PrimaryWindow, now)
		stampCodexWindowResetAt(quota.AdditionalRateLimits[i].SecondaryWindow, now)
	}
	return quota, nil
}

func parseRateLimitWindow(values map[string]any) *codexauth.RateLimitWindow {
	if values == nil {
		return nil
	}
	window := &codexauth.RateLimitWindow{
		UsedPercent:        clampPercent(flexFloat(values, "used_percent", "usedPercent", "usage_percent", "usagePercent")),
		LimitWindowSeconds: flexInt64(values, "limit_window_seconds", "limitWindowSeconds", "window_seconds", "windowSeconds"),
		ResetAfterSeconds:  flexInt64(values, "reset_after_seconds", "resetAfterSeconds", "reset_in_seconds", "resetInSeconds"),
		ResetAt:            flexTime(values, "reset_at", "resetAt", "resets_at", "resetsAt"),
	}
	return window
}

func parseCreditBalance(values map[string]any) *codexauth.CreditBalance {
	if values == nil {
		return nil
	}
	return &codexauth.CreditBalance{
		HasCredits: flexBool(values, "has_credits", "hasCredits", "available"),
		Unlimited:  flexBool(values, "unlimited", "is_unlimited", "isUnlimited"),
		Balance:    flexFloat(values, "balance", "amount", "remaining"),
	}
}

func flexMap(values map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value, ok := values[key].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func flexSlice(values map[string]any, keys ...string) []any {
	for _, key := range keys {
		if value, ok := values[key].([]any); ok {
			return value
		}
	}
	return nil
}

func flexString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := values[key].(type) {
		case string:
			if text := strings.TrimSpace(value); text != "" {
				return text
			}
		case json.Number:
			return value.String()
		}
	}
	return ""
}

func flexFloat(values map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch value := values[key].(type) {
		case json.Number:
			parsed, _ := value.Float64()
			return parsed
		case float64:
			return value
		case string:
			parsed, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
			return parsed
		}
	}
	return 0
}

func flexInt64(values map[string]any, keys ...string) int64 {
	value := flexFloat(values, keys...)
	if value <= 0 || value > math.MaxInt64 {
		return 0
	}
	return int64(value)
}

func flexBool(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		switch value := values[key].(type) {
		case bool:
			return value
		case string:
			parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
			return parsed
		case json.Number:
			parsed, _ := value.Int64()
			return parsed != 0
		}
	}
	return false
}

func flexTime(values map[string]any, keys ...string) string {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		if text, ok := value.(string); ok {
			text = strings.TrimSpace(text)
			if parsed, err := time.Parse(time.RFC3339, text); err == nil {
				return parsed.UTC().Format(time.RFC3339)
			}
			if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
				return unixQuotaTime(parsed)
			}
		}
		if number, ok := value.(json.Number); ok {
			if parsed, err := number.Int64(); err == nil {
				return unixQuotaTime(parsed)
			}
		}
	}
	return ""
}

func unixQuotaTime(value int64) string {
	if value > 1_000_000_000_000 {
		value /= 1000
	}
	if value <= 0 {
		return ""
	}
	return time.Unix(value, 0).UTC().Format(time.RFC3339)
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
