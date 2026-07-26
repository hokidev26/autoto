// Package geminiauth implements the Google OAuth and Cloud Code control-plane
// flow used by Gemini CLI subscription accounts.
package geminiauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	ProviderName = "gemini"

	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"

	AuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"

	CloudCodeAPIEndpoint      = "https://cloudcode-pa.googleapis.com"
	CloudCodeDailyAPIEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	CloudCodeAPIVersion       = "v1internal"

	RequestUserAgent       = "antigravity/hub/2.2.1 darwin/arm64"
	OnboardUserAgent       = RequestUserAgent + " google-api-nodejs-client/10.3.0"
	OnboardGoogAPIClientUA = "gl-node/22.21.1"

	RefreshLeadDuration = 5 * time.Minute

	maxResponseBytes  = 1 << 20
	maxOAuthValueSize = 16 << 10
)

var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

var refreshGroup singleflight.Group

// RefreshLead returns how early callers should refresh an access token.
func RefreshLead() time.Duration { return RefreshLeadDuration }

// TokenData is the private OAuth token response. Callers must never serialize it
// into ordinary API responses or logs.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// UserInfo contains only display identity returned by Google's userinfo API.
type UserInfo struct {
	Subject       string `json:"id,omitempty"`
	Email         string `json:"email"`
	VerifiedEmail bool   `json:"verified_email,omitempty"`
	Name          string `json:"name,omitempty"`
	Picture       string `json:"picture,omitempty"`
}

// TierInfo is a token-free Cloud Code subscription tier summary.
type TierInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// ProjectInfo is the safe subset of loadCodeAssist metadata useful to account UI.
type ProjectInfo struct {
	ProjectID    string     `json:"projectId,omitempty"`
	AllowedTiers []TierInfo `json:"allowedTiers,omitempty"`
	CurrentTier  *TierInfo  `json:"currentTier,omitempty"`
}

type clientEndpoints struct {
	auth      string
	token     string
	userInfo  string
	cloudCode string
	daily     string
	loopback  bool
}

type Client struct {
	httpClient   *http.Client
	endpoints    clientEndpoints
	pollInterval time.Duration
	maxAttempts  int
}

// New creates a production client. OAuth and Cloud Code hosts are fixed and
// cannot be redirected by ordinary callers.
func New(httpClient *http.Client) *Client { return NewClient(httpClient) }

func NewClient(httpClient *http.Client) *Client {
	return &Client{
		httpClient: safeHTTPClient(httpClient),
		endpoints: clientEndpoints{
			auth:      AuthEndpoint,
			token:     TokenEndpoint,
			userInfo:  UserInfoEndpoint,
			cloudCode: CloudCodeAPIEndpoint,
			daily:     CloudCodeDailyAPIEndpoint,
		},
		pollInterval: 2 * time.Second,
		maxAttempts:  5,
	}
}

// newTestClient is deliberately unexported so production callers cannot point
// OAuth token exchange or bearer-token requests at arbitrary hosts.
func newTestClient(httpClient *http.Client, endpoints clientEndpoints, pollInterval time.Duration) *Client {
	client := NewClient(httpClient)
	client.endpoints = endpoints
	client.endpoints.loopback = true
	if pollInterval > 0 {
		client.pollInterval = pollInterval
	}
	return client
}

// GenerateState creates a cryptographically random OAuth state value.
func GenerateState() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("无法生成 Gemini OAuth state")
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

// BuildAuthURL creates Google's consent URL for a loopback callback.
func (c *Client) BuildAuthURL(state, redirectURI string) (string, error) {
	state = strings.TrimSpace(state)
	if state == "" || len(state) > 1024 || strings.ContainsAny(state, "\x00\r\n") {
		return "", errors.New("Gemini OAuth state 无效")
	}
	redirectURI, err := validateRedirectURI(redirectURI)
	if err != nil {
		return "", err
	}
	authEndpoint, err := c.validatedEndpoint(c.endpoints.auth, endpointAuth)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(authEndpoint)
	if err != nil {
		return "", errors.New("Gemini OAuth 授权端点无效")
	}
	query := parsed.Query()
	query.Set("access_type", "offline")
	query.Set("client_id", ClientID)
	query.Set("prompt", "consent")
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(Scopes, " "))
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// ExchangeCode exchanges an authorization code for Google OAuth tokens.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenData, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > maxOAuthValueSize || strings.ContainsAny(code, "\x00\r\n") {
		return nil, errors.New("Gemini OAuth authorization code 无效")
	}
	redirectURI, err := validateRedirectURI(redirectURI)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"code":          {code},
		"client_id":     {ClientID},
		"client_secret": {ClientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	return c.postToken(nonNilContext(ctx), form)
}

// ExchangeCodeForTokens is a compatibility alias for ExchangeCode.
func (c *Client) ExchangeCodeForTokens(ctx context.Context, code, redirectURI string) (*TokenData, error) {
	return c.ExchangeCode(ctx, code, redirectURI)
}

// RefreshTokens refreshes a Gemini access token. Google commonly omits a new
// refresh_token; in that case RefreshToken remains empty so the Store can retain
// the previous value atomically.
func (c *Client) RefreshTokens(ctx context.Context, refreshToken string) (*TokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || len(refreshToken) > maxOAuthValueSize || strings.ContainsRune(refreshToken, 0) {
		return nil, errors.New("Gemini refresh token 无效")
	}
	keyHash := sha256.Sum256([]byte(refreshToken))
	key := hex.EncodeToString(keyHash[:])
	result, err, _ := refreshGroup.Do(key, func() (any, error) {
		form := url.Values{
			"client_id":     {ClientID},
			"client_secret": {ClientSecret},
			"refresh_token": {refreshToken},
			"grant_type":    {"refresh_token"},
		}
		return c.postToken(context.WithoutCancel(nonNilContext(ctx)), form)
	})
	if err != nil {
		return nil, err
	}
	tokens, ok := result.(*TokenData)
	if !ok || tokens == nil {
		return nil, errors.New("Gemini token refresh 结果无效")
	}
	copy := *tokens
	return &copy, nil
}

func (c *Client) postToken(ctx context.Context, form url.Values) (*TokenData, error) {
	endpoint, err := c.validatedEndpoint(c.endpoints.token, endpointToken)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("无法构造 Gemini OAuth token 请求")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Gemini OAuth token 请求失败")
	}
	defer response.Body.Close()
	data, err := readLimited(response.Body)
	if err != nil {
		return nil, errors.New("Gemini OAuth token 响应无效")
	}
	var payload struct {
		Error        string `json:"error"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errors.New("Gemini OAuth token 响应无效")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || strings.TrimSpace(payload.Error) != "" {
		return nil, fmt.Errorf("Gemini OAuth token 请求被拒绝（HTTP %d）", response.StatusCode)
	}
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	payload.RefreshToken = strings.TrimSpace(payload.RefreshToken)
	payload.IDToken = strings.TrimSpace(payload.IDToken)
	payload.TokenType = strings.TrimSpace(payload.TokenType)
	if payload.AccessToken == "" {
		return nil, errors.New("Gemini OAuth token 响应缺少 access_token")
	}
	tokens := &TokenData{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		TokenType:    payload.TokenType,
		ExpiresIn:    payload.ExpiresIn,
	}
	if payload.ExpiresIn > 0 {
		tokens.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	return tokens, nil
}

// FetchUserInfo retrieves the token-free display identity for an account.
func (c *Client) FetchUserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || len(accessToken) > maxOAuthValueSize || strings.ContainsRune(accessToken, 0) {
		return UserInfo{}, errors.New("Gemini access token 无效")
	}
	endpoint, err := c.validatedEndpoint(c.endpoints.userInfo, endpointUserInfo)
	if err != nil {
		return UserInfo{}, err
	}
	ctx = nonNilContext(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return UserInfo{}, errors.New("无法构造 Gemini userinfo 请求")
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", RequestUserAgent)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return UserInfo{}, ctx.Err()
		}
		return UserInfo{}, errors.New("Gemini userinfo 请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return UserInfo{}, fmt.Errorf("Gemini userinfo 请求失败（HTTP %d）", response.StatusCode)
	}
	data, err := readLimited(response.Body)
	if err != nil {
		return UserInfo{}, errors.New("Gemini userinfo 响应无效")
	}
	var info UserInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return UserInfo{}, errors.New("Gemini userinfo 响应无效")
	}
	info.Subject = strings.TrimSpace(info.Subject)
	info.Email = strings.TrimSpace(info.Email)
	info.Name = strings.TrimSpace(info.Name)
	info.Picture = strings.TrimSpace(info.Picture)
	if info.Email == "" {
		return UserInfo{}, errors.New("Gemini userinfo 响应缺少 email")
	}
	return info, nil
}

// FetchProjectID resolves the Cloud Code project and onboards an account when
// loadCodeAssist reports no project yet.
func (c *Client) FetchProjectID(ctx context.Context, accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || len(accessToken) > maxOAuthValueSize || strings.ContainsRune(accessToken, 0) {
		return "", errors.New("Gemini access token 无效")
	}
	ctx = nonNilContext(ctx)
	load, err := c.loadCodeAssist(ctx, accessToken)
	if err != nil {
		return "", err
	}
	info := parseProjectInfo(load)
	if info.ProjectID != "" {
		return info.ProjectID, nil
	}
	return c.onboardUser(ctx, accessToken, defaultTierID(info))
}

func (c *Client) loadCodeAssist(ctx context.Context, accessToken string) (map[string]any, error) {
	base, err := c.validatedEndpoint(c.endpoints.cloudCode, endpointCloudCode)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(base, "/") + "/" + CloudCodeAPIVersion + ":loadCodeAssist"
	body, err := json.Marshal(map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY"}})
	if err != nil {
		return nil, errors.New("无法编码 Gemini loadCodeAssist 请求")
	}
	response, err := c.postCloudCode(ctx, endpoint, accessToken, RequestUserAgent, "", body)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(response, &payload); err != nil {
		return nil, errors.New("Gemini loadCodeAssist 响应无效")
	}
	return payload, nil
}

func (c *Client) onboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
	base, err := c.validatedEndpoint(c.endpoints.daily, endpointDailyCloudCode)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(base, "/") + "/" + CloudCodeAPIVersion + ":onboardUser"
	if strings.TrimSpace(tierID) == "" {
		tierID = "free-tier"
	}
	body, err := json.Marshal(map[string]any{
		"tier_id": tierID,
		"metadata": map[string]string{
			"ide_type":    "ANTIGRAVITY",
			"ide_version": "2.2.1",
			"ide_name":    "antigravity",
		},
	})
	if err != nil {
		return "", errors.New("无法编码 Gemini onboardUser 请求")
	}
	attempts := c.maxAttempts
	if attempts <= 0 {
		attempts = 5
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		response, requestErr := c.postCloudCode(ctx, endpoint, accessToken, OnboardUserAgent, OnboardGoogAPIClientUA, body)
		if requestErr != nil {
			return "", requestErr
		}
		var payload map[string]any
		if err := json.Unmarshal(response, &payload); err != nil {
			return "", errors.New("Gemini onboardUser 响应无效")
		}
		done, _ := payload["done"].(bool)
		if done {
			responsePayload, _ := payload["response"].(map[string]any)
			projectID := parseProjectInfo(responsePayload).ProjectID
			if projectID == "" {
				return "", errors.New("Gemini onboardUser 响应缺少 project ID")
			}
			return projectID, nil
		}
		if attempt+1 >= attempts {
			break
		}
		timer := time.NewTimer(c.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", fmt.Errorf("Gemini onboardUser 在 %d 次尝试后仍未完成", attempts)
}

func (c *Client) postCloudCode(ctx context.Context, endpoint, accessToken, userAgent, googAPIClient string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("无法构造 Gemini Cloud Code 请求")
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)
	if googAPIClient != "" {
		request.Header.Set("X-Goog-Api-Client", googAPIClient)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Gemini Cloud Code 请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Gemini Cloud Code 请求失败（HTTP %d）", response.StatusCode)
	}
	data, err := readLimited(response.Body)
	if err != nil {
		return nil, errors.New("Gemini Cloud Code 响应无效")
	}
	return data, nil
}

func parseProjectInfo(data map[string]any) ProjectInfo {
	info := ProjectInfo{ProjectID: extractProjectID(data)}
	if data == nil {
		return info
	}
	if tiers, ok := data["allowedTiers"].([]any); ok {
		for _, raw := range tiers {
			if tierMap, ok := raw.(map[string]any); ok {
				if tier := parseTier(tierMap); tier.ID != "" {
					info.AllowedTiers = append(info.AllowedTiers, tier)
				}
			}
		}
	}
	if raw, ok := data["currentTier"].(map[string]any); ok {
		tier := parseTier(raw)
		if tier.ID != "" {
			info.CurrentTier = &tier
		}
	}
	return info
}

func extractProjectID(data map[string]any) string {
	for _, key := range []string{"cloudaicompanionProject", "projectId", "project"} {
		value, exists := data[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case string:
			if value := strings.TrimSpace(typed); value != "" {
				return value
			}
		case map[string]any:
			for _, nestedKey := range []string{"id", "projectId", "project"} {
				if value := stringValue(typed[nestedKey]); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func parseTier(data map[string]any) TierInfo {
	tier := TierInfo{ID: stringValue(data["id"]), Name: stringValue(data["name"])}
	tier.IsDefault, _ = data["isDefault"].(bool)
	return tier
}

func defaultTierID(info ProjectInfo) string {
	for _, tier := range info.AllowedTiers {
		if tier.IsDefault && tier.ID != "" {
			return tier.ID
		}
	}
	if info.CurrentTier != nil && info.CurrentTier.ID != "" {
		return info.CurrentTier.ID
	}
	return "free-tier"
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

type endpointKind int

const (
	endpointAuth endpointKind = iota
	endpointToken
	endpointUserInfo
	endpointCloudCode
	endpointDailyCloudCode
)

func (c *Client) validatedEndpoint(raw string, kind endpointKind) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", errors.New("Gemini 服务端点无效")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if c.endpoints.loopback && parsed.Scheme == "http" && isLoopbackHost(host) {
		return parsed.String(), nil
	}
	if parsed.Scheme != "https" {
		return "", errors.New("Gemini 服务端点必须使用 HTTPS")
	}
	allowedHost := ""
	switch kind {
	case endpointAuth:
		allowedHost = "accounts.google.com"
	case endpointToken:
		allowedHost = "oauth2.googleapis.com"
	case endpointUserInfo:
		allowedHost = "www.googleapis.com"
	case endpointCloudCode:
		allowedHost = "cloudcode-pa.googleapis.com"
	case endpointDailyCloudCode:
		allowedHost = "daily-cloudcode-pa.googleapis.com"
	}
	if host != allowedHost {
		return "", errors.New("Gemini 服务端点主机不受信任")
	}
	return parsed.String(), nil
}

func validateRedirectURI(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n") {
		return "", errors.New("Gemini OAuth redirect URI 无效")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", errors.New("Gemini OAuth redirect URI 无效")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if parsed.Scheme != "http" || !isLoopbackHost(host) {
		return "", errors.New("Gemini OAuth redirect URI 必须是本机 HTTP 回调")
	}
	return parsed.String(), nil
}

func safeHTTPClient(configured *http.Client) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if configured != nil {
		copy := *configured
		client = &copy
		if client.Timeout <= 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

func readLimited(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, errors.New("response too large")
	}
	return data, nil
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
