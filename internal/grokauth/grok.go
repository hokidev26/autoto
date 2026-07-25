// Package grokauth implements the xAI OIDC device authorization flow used by Grok CLI.
package grokauth

import (
	"context"
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
	Issuer              = "https://auth.x.ai"
	DiscoveryURL        = Issuer + "/.well-known/openid-configuration"
	ClientID            = "b1a00492-073a-47ea-816f-4c329264a828"
	Scope               = "openid profile email offline_access grok-cli:access api:access"
	OfficialAPIBaseURL  = "https://api.x.ai/v1"
	DefaultAPIBaseURL   = OfficialAPIBaseURL
	CLIProxyBaseURL     = "https://cli-chat-proxy.grok.com/v1"
	CLIChatProxyBaseURL = CLIProxyBaseURL
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	RefreshLeadDuration = 5 * time.Minute
	MaxWait             = 30 * time.Minute
	MaxPollDuration     = MaxWait

	maxResponseBytes = 1 << 20
)

var refreshGroup singleflight.Group

// RefreshLead returns how early callers should refresh an access token.
func RefreshLead() time.Duration { return RefreshLeadDuration }

type Discovery struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	TokenEndpoint           string `json:"-"`
	startedAt               time.Time
}

type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Email        string `json:"email,omitempty"`
	Subject      string `json:"subject,omitempty"`
}

type PollState string

const (
	PollAuthorized PollState = "authorized"
	PollPending    PollState = "pending"
	PollSlowDown   PollState = "slow_down"
)

type PollResult struct {
	State      PollState
	Tokens     *TokenData
	RetryAfter time.Duration
}

type Client struct {
	httpClient      *http.Client
	discoveryURL    string
	allowLoopback   bool
	pollFloor       time.Duration
	slowDownStep    time.Duration
	maximumWaitTime time.Duration
}

// New creates a production client. OAuth hosts are immutable except for xAI
// endpoints returned by validated OIDC discovery.
func New(httpClient *http.Client) *Client {
	return NewClient(httpClient)
}

func NewClient(httpClient *http.Client) *Client {
	return &Client{
		httpClient:      safeHTTPClient(httpClient),
		discoveryURL:    DiscoveryURL,
		pollFloor:       5 * time.Second,
		slowDownStep:    5 * time.Second,
		maximumWaitTime: MaxWait,
	}
}

// newTestClient is deliberately unexported so normal callers cannot redirect
// credentials to arbitrary endpoints.
func newTestClient(httpClient *http.Client, discoveryURL string, pollFloor, slowDownStep time.Duration) *Client {
	client := NewClient(httpClient)
	client.discoveryURL = discoveryURL
	client.allowLoopback = true
	if pollFloor > 0 {
		client.pollFloor = pollFloor
	}
	if slowDownStep > 0 {
		client.slowDownStep = slowDownStep
	}
	return client
}

// ValidateOAuthEndpoint accepts only HTTPS endpoints hosted by x.ai or one of
// its subdomains.
func ValidateOAuthEndpoint(rawURL string) (string, error) {
	return validateOAuthEndpoint(rawURL, false)
}

func validateOAuthEndpoint(rawURL string, allowLoopback bool) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", errors.New("xAI OAuth endpoint 无效")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if allowLoopback && parsed.Scheme == "http" && isLoopbackHost(host) {
		return parsed.String(), nil
	}
	if parsed.Scheme != "https" {
		return "", errors.New("xAI OAuth endpoint 必须使用 HTTPS")
	}
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return "", errors.New("xAI OAuth endpoint 不在 x.ai 域内")
	}
	return parsed.String(), nil
}

func (c *Client) Discover(ctx context.Context) (Discovery, error) {
	ctx = nonNilContext(ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.discoveryURL, nil)
	if err != nil {
		return Discovery{}, errors.New("无法构造 xAI OIDC discovery 请求")
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Discovery{}, ctx.Err()
		}
		return Discovery{}, errors.New("xAI OIDC discovery 请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("xAI OIDC discovery 失败（HTTP %d）", response.StatusCode)
	}
	data, err := readLimited(response.Body)
	if err != nil {
		return Discovery{}, errors.New("xAI OIDC discovery 响应无效")
	}
	var discovery Discovery
	if err := json.Unmarshal(data, &discovery); err != nil {
		return Discovery{}, errors.New("xAI OIDC discovery 响应无效")
	}
	discovery.DeviceAuthorizationEndpoint, err = validateOAuthEndpoint(discovery.DeviceAuthorizationEndpoint, c.allowLoopback)
	if err != nil {
		return Discovery{}, err
	}
	discovery.TokenEndpoint, err = validateOAuthEndpoint(discovery.TokenEndpoint, c.allowLoopback)
	if err != nil {
		return Discovery{}, err
	}
	return discovery, nil
}

func (c *Client) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	discovery, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	ctx = nonNilContext(ctx)
	form := url.Values{"client_id": {ClientID}, "scope": {Scope}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.DeviceAuthorizationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("无法构造 xAI device authorization 请求")
	}
	setFormHeaders(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("xAI device authorization 请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("xAI device authorization 失败（HTTP %d）", response.StatusCode)
	}
	data, err := readLimited(response.Body)
	if err != nil {
		return nil, errors.New("xAI device authorization 响应无效")
	}
	var device DeviceCodeResponse
	if err := json.Unmarshal(data, &device); err != nil {
		return nil, errors.New("xAI device authorization 响应无效")
	}
	device.DeviceCode = strings.TrimSpace(device.DeviceCode)
	device.UserCode = strings.TrimSpace(device.UserCode)
	device.VerificationURI = strings.TrimSpace(device.VerificationURI)
	device.VerificationURIComplete = strings.TrimSpace(device.VerificationURIComplete)
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" && device.VerificationURIComplete == "" {
		return nil, errors.New("xAI device authorization 响应缺少必要字段")
	}
	if device.VerificationURI != "" {
		device.VerificationURI, err = validateOAuthEndpoint(device.VerificationURI, c.allowLoopback)
		if err != nil {
			return nil, errors.New("xAI device authorization 返回了不受信任的确认网址")
		}
	}
	if device.VerificationURIComplete != "" {
		device.VerificationURIComplete, err = validateOAuthEndpoint(device.VerificationURIComplete, c.allowLoopback)
		if err != nil {
			return nil, errors.New("xAI device authorization 返回了不受信任的确认网址")
		}
	}
	device.TokenEndpoint = discovery.TokenEndpoint
	device.startedAt = time.Now()
	return &device, nil
}

// Poll performs one device-token exchange. Pending and slow_down are returned
// as states rather than errors so callers may drive their own UI loop.
func (c *Client) Poll(ctx context.Context, device *DeviceCodeResponse) (PollResult, error) {
	if device == nil || strings.TrimSpace(device.DeviceCode) == "" {
		return PollResult{}, errors.New("xAI device code 无效")
	}
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return PollResult{}, err
	}
	tokenEndpoint := strings.TrimSpace(device.TokenEndpoint)
	if tokenEndpoint == "" {
		discovery, err := c.Discover(ctx)
		if err != nil {
			return PollResult{}, err
		}
		tokenEndpoint = discovery.TokenEndpoint
	}
	tokenEndpoint, err := validateOAuthEndpoint(tokenEndpoint, c.allowLoopback)
	if err != nil {
		return PollResult{}, err
	}
	form := url.Values{
		"grant_type":  {DeviceCodeGrantType},
		"device_code": {strings.TrimSpace(device.DeviceCode)},
		"client_id":   {ClientID},
	}
	return c.postToken(ctx, tokenEndpoint, form, "")
}

// Wait polls until authorization succeeds, is denied, expires, or is cancelled.
func (c *Client) Wait(ctx context.Context, device *DeviceCodeResponse) (*TokenData, error) {
	if device == nil {
		return nil, errors.New("xAI device code 无效")
	}
	ctx = nonNilContext(ctx)
	interval := time.Duration(device.Interval) * time.Second
	if interval < c.pollFloor {
		interval = c.pollFloor
	}
	startedAt := device.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	deadline := startedAt.Add(c.maximumWaitTime)
	if device.ExpiresIn > 0 {
		codeDeadline := startedAt.Add(time.Duration(device.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}
	first := true
	for {
		if !first {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		first = false
		if !time.Now().Before(deadline) {
			return nil, errors.New("xAI device code 已过期")
		}
		result, err := c.Poll(ctx, device)
		if err != nil {
			return nil, err
		}
		switch result.State {
		case PollAuthorized:
			return result.Tokens, nil
		case PollSlowDown:
			interval += c.slowDownStep
			if result.RetryAfter > interval {
				interval = result.RetryAfter
			}
		case PollPending:
			if result.RetryAfter > interval {
				interval = result.RetryAfter
			}
		default:
			return nil, errors.New("xAI device authorization 状态无效")
		}
	}
}

// PollForToken is a compatibility alias for Wait.
func (c *Client) PollForToken(ctx context.Context, device *DeviceCodeResponse) (*TokenData, error) {
	return c.Wait(ctx, device)
}

func (c *Client) RefreshTokens(ctx context.Context, refreshToken, tokenEndpoint string) (*TokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || strings.ContainsRune(refreshToken, 0) {
		return nil, errors.New("xAI refresh token 无效")
	}
	endpoint, err := validateOAuthEndpoint(tokenEndpoint, c.allowLoopback)
	if err != nil {
		if strings.TrimSpace(tokenEndpoint) != "" {
			return nil, err
		}
		discovery, discoverErr := c.Discover(ctx)
		if discoverErr != nil {
			return nil, discoverErr
		}
		endpoint = discovery.TokenEndpoint
	}
	keyHash := sha256.Sum256([]byte(endpoint + "\x00" + refreshToken))
	key := hex.EncodeToString(keyHash[:])
	result, err, _ := refreshGroup.Do(key, func() (any, error) {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {ClientID},
			"refresh_token": {refreshToken},
		}
		poll, postErr := c.postToken(context.WithoutCancel(nonNilContext(ctx)), endpoint, form, refreshToken)
		if postErr != nil {
			return nil, postErr
		}
		return poll.Tokens, nil
	})
	if err != nil {
		return nil, err
	}
	tokens, ok := result.(*TokenData)
	if !ok || tokens == nil {
		return nil, errors.New("xAI token refresh 结果无效")
	}
	copy := *tokens
	return &copy, nil
}

func (c *Client) postToken(ctx context.Context, endpoint string, form url.Values, previousRefresh string) (PollResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return PollResult{}, errors.New("无法构造 xAI token 请求")
	}
	setFormHeaders(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return PollResult{}, ctx.Err()
		}
		return PollResult{}, errors.New("xAI token 请求失败")
	}
	defer response.Body.Close()
	data, err := readLimited(response.Body)
	if err != nil {
		return PollResult{}, errors.New("xAI token 响应无效")
	}
	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		IDToken          string `json:"id_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return PollResult{}, errors.New("xAI token 响应无效")
	}
	switch strings.TrimSpace(payload.Error) {
	case "authorization_pending":
		return PollResult{State: PollPending, RetryAfter: c.pollFloor}, nil
	case "slow_down":
		return PollResult{State: PollSlowDown, RetryAfter: c.pollFloor + c.slowDownStep}, nil
	case "expired_token":
		return PollResult{}, errors.New("xAI device code 已过期")
	case "access_denied":
		return PollResult{}, errors.New("xAI device authorization 已被拒绝")
	case "":
	default:
		return PollResult{}, errors.New("xAI OAuth token 请求被拒绝")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return PollResult{}, fmt.Errorf("xAI token 请求失败（HTTP %d）", response.StatusCode)
	}
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	payload.RefreshToken = strings.TrimSpace(payload.RefreshToken)
	payload.IDToken = strings.TrimSpace(payload.IDToken)
	payload.TokenType = strings.TrimSpace(payload.TokenType)
	if payload.AccessToken == "" {
		return PollResult{}, errors.New("xAI token 响应缺少 access_token")
	}
	if payload.RefreshToken == "" {
		payload.RefreshToken = previousRefresh
	}
	email, subject := ParseJWTIdentity(payload.IDToken)
	tokens := &TokenData{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		TokenType:    payload.TokenType,
		ExpiresIn:    payload.ExpiresIn,
		Email:        email,
		Subject:      subject,
	}
	if payload.ExpiresIn > 0 {
		tokens.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	return PollResult{State: PollAuthorized, Tokens: tokens}, nil
}

// ParseJWTIdentity extracts unverified display identity from JWT claims. It is
// not an authentication verifier and deliberately returns empty strings on any
// malformed input.
func ParseJWTIdentity(token string) (email, subject string) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || len(parts[1]) > maxResponseBytes {
		return "", ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Email   string `json:"email"`
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		return "", ""
	}
	return strings.TrimSpace(claims.Email), strings.TrimSpace(claims.Subject)
}

func setFormHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
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
