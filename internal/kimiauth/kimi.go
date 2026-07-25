// Package kimiauth implements Kimi Code's OAuth2 device authorization flow.
package kimiauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	ClientID                    = "17e5f671-d194-4dfb-9706-5516cb48c098"
	AuthHost                    = "https://auth.kimi.com"
	DeviceAuthorizationEndpoint = AuthHost + "/api/oauth/device_authorization"
	TokenEndpoint               = AuthHost + "/api/oauth/token"
	APIBaseURL                  = "https://api.kimi.com/coding"
	KimiAPIBaseURL              = APIBaseURL
	DeviceCodeGrantType         = "urn:ietf:params:oauth:grant-type:device_code"
	RefreshLeadDuration         = 5 * time.Minute
	MaxWait                     = 15 * time.Minute

	maxResponseBytes = 1 << 20
)

var refreshGroup singleflight.Group

// RefreshLead returns how early callers should refresh an access token.
func RefreshLead() time.Duration { return RefreshLeadDuration }

type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	DeviceID                string `json:"device_id,omitempty"`
	startedAt               time.Time
}

type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Scope        string `json:"scope,omitempty"`
	DeviceID     string `json:"device_id"`
}

type AuthBundle struct {
	TokenData TokenData `json:"token_data"`
	DeviceID  string    `json:"device_id"`
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
	httpClient     *http.Client
	clientVersion  string
	deviceID       string
	deviceEndpoint string
	tokenEndpoint  string
	allowLoopback  bool
	pollFloor      time.Duration
	slowDownStep   time.Duration
	maximumWait    time.Duration
}

// New creates a production Kimi device-flow client. Empty device IDs are
// generated in memory and are never read from kimi-cli configuration files.
func New(httpClient *http.Client, clientVersion, deviceID string) *Client {
	return NewClient(httpClient, clientVersion, deviceID)
}

func NewClient(httpClient *http.Client, clientVersion, deviceID string) *Client {
	deviceID = safeHeaderValue(deviceID, "", 256)
	if deviceID == "" {
		deviceID = uuid.NewString()
	}
	clientVersion = safeHeaderValue(clientVersion, "unknown", 128)
	return &Client{
		httpClient:     safeHTTPClient(httpClient),
		clientVersion:  clientVersion,
		deviceID:       deviceID,
		deviceEndpoint: DeviceAuthorizationEndpoint,
		tokenEndpoint:  TokenEndpoint,
		pollFloor:      5 * time.Second,
		slowDownStep:   5 * time.Second,
		maximumWait:    MaxWait,
	}
}

// newTestClient is unexported so production callers cannot override OAuth hosts.
func newTestClient(httpClient *http.Client, clientVersion, deviceID, deviceEndpoint, tokenEndpoint string, pollFloor, slowDownStep time.Duration) *Client {
	client := NewClient(httpClient, clientVersion, deviceID)
	client.deviceEndpoint = deviceEndpoint
	client.tokenEndpoint = tokenEndpoint
	client.allowLoopback = true
	if pollFloor > 0 {
		client.pollFloor = pollFloor
	}
	if slowDownStep > 0 {
		client.slowDownStep = slowDownStep
	}
	return client
}

func (c *Client) DeviceID() string {
	if c == nil {
		return ""
	}
	return c.deviceID
}

func (c *Client) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	ctx = nonNilContext(ctx)
	form := url.Values{"client_id": {ClientID}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.deviceEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("无法构造 Kimi device authorization 请求")
	}
	c.setHeaders(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Kimi device authorization 请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Kimi device authorization 失败（HTTP %d）", response.StatusCode)
	}
	data, err := readLimited(response.Body)
	if err != nil {
		return nil, errors.New("Kimi device authorization 响应无效")
	}
	var device DeviceCodeResponse
	if err := json.Unmarshal(data, &device); err != nil {
		return nil, errors.New("Kimi device authorization 响应无效")
	}
	device.DeviceCode = strings.TrimSpace(device.DeviceCode)
	device.UserCode = strings.TrimSpace(device.UserCode)
	device.VerificationURI = strings.TrimSpace(device.VerificationURI)
	device.VerificationURIComplete = strings.TrimSpace(device.VerificationURIComplete)
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" && device.VerificationURIComplete == "" {
		return nil, errors.New("Kimi device authorization 响应缺少必要字段")
	}
	if device.VerificationURI != "" {
		device.VerificationURI, err = validateVerificationURI(device.VerificationURI, c.allowLoopback)
		if err != nil {
			return nil, errors.New("Kimi device authorization 返回了不受信任的确认网址")
		}
	}
	if device.VerificationURIComplete != "" {
		device.VerificationURIComplete, err = validateVerificationURI(device.VerificationURIComplete, c.allowLoopback)
		if err != nil {
			return nil, errors.New("Kimi device authorization 返回了不受信任的确认网址")
		}
	}
	device.DeviceID = c.deviceID
	device.startedAt = time.Now()
	return &device, nil
}

// Poll performs a single token request.
func (c *Client) Poll(ctx context.Context, device *DeviceCodeResponse) (PollResult, error) {
	if device == nil || strings.TrimSpace(device.DeviceCode) == "" {
		return PollResult{}, errors.New("Kimi device code 无效")
	}
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return PollResult{}, err
	}
	form := url.Values{
		"client_id":   {ClientID},
		"device_code": {strings.TrimSpace(device.DeviceCode)},
		"grant_type":  {DeviceCodeGrantType},
	}
	return c.postToken(ctx, form, "")
}

func (c *Client) Wait(ctx context.Context, device *DeviceCodeResponse) (*AuthBundle, error) {
	if device == nil {
		return nil, errors.New("Kimi device code 无效")
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
	deadline := startedAt.Add(c.maximumWait)
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
			return nil, errors.New("Kimi device code 已过期")
		}
		result, err := c.Poll(ctx, device)
		if err != nil {
			return nil, err
		}
		switch result.State {
		case PollAuthorized:
			return &AuthBundle{TokenData: *result.Tokens, DeviceID: c.deviceID}, nil
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
			return nil, errors.New("Kimi device authorization 状态无效")
		}
	}
}

// PollForToken waits for authorization and returns token data directly.
func (c *Client) PollForToken(ctx context.Context, device *DeviceCodeResponse) (*TokenData, error) {
	bundle, err := c.Wait(ctx, device)
	if err != nil {
		return nil, err
	}
	return &bundle.TokenData, nil
}

func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*TokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || strings.ContainsRune(refreshToken, 0) {
		return nil, errors.New("Kimi refresh token 无效")
	}
	keyHash := sha256.Sum256([]byte(c.tokenEndpoint + "\x00" + refreshToken + "\x00" + c.deviceID))
	key := hex.EncodeToString(keyHash[:])
	result, err, _ := refreshGroup.Do(key, func() (any, error) {
		form := url.Values{
			"client_id":     {ClientID},
			"grant_type":    {"refresh_token"},
			"refresh_token": {refreshToken},
		}
		poll, postErr := c.postToken(context.WithoutCancel(nonNilContext(ctx)), form, refreshToken)
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
		return nil, errors.New("Kimi token refresh 结果无效")
	}
	copy := *tokens
	return &copy, nil
}

func (c *Client) postToken(ctx context.Context, form url.Values, previousRefresh string) (PollResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return PollResult{}, errors.New("无法构造 Kimi token 请求")
	}
	c.setHeaders(request)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return PollResult{}, ctx.Err()
		}
		return PollResult{}, errors.New("Kimi token 请求失败")
	}
	defer response.Body.Close()
	data, err := readLimited(response.Body)
	if err != nil {
		return PollResult{}, errors.New("Kimi token 响应无效")
	}
	var payload struct {
		Error            string  `json:"error"`
		ErrorDescription string  `json:"error_description"`
		AccessToken      string  `json:"access_token"`
		RefreshToken     string  `json:"refresh_token"`
		TokenType        string  `json:"token_type"`
		ExpiresIn        float64 `json:"expires_in"`
		Scope            string  `json:"scope"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return PollResult{}, errors.New("Kimi token 响应无效")
	}
	switch strings.TrimSpace(payload.Error) {
	case "authorization_pending":
		return PollResult{State: PollPending, RetryAfter: c.pollFloor}, nil
	case "slow_down":
		return PollResult{State: PollSlowDown, RetryAfter: c.pollFloor + c.slowDownStep}, nil
	case "expired_token":
		return PollResult{}, errors.New("Kimi device code 已过期")
	case "access_denied":
		return PollResult{}, errors.New("Kimi device authorization 已被拒绝")
	case "":
	default:
		return PollResult{}, errors.New("Kimi OAuth token 请求被拒绝")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return PollResult{}, fmt.Errorf("Kimi token 被拒绝（HTTP %d）", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return PollResult{}, fmt.Errorf("Kimi token 请求失败（HTTP %d）", response.StatusCode)
	}
	payload.AccessToken = strings.TrimSpace(payload.AccessToken)
	payload.RefreshToken = strings.TrimSpace(payload.RefreshToken)
	payload.TokenType = strings.TrimSpace(payload.TokenType)
	payload.Scope = strings.TrimSpace(payload.Scope)
	if payload.AccessToken == "" {
		return PollResult{}, errors.New("Kimi token 响应缺少 access_token")
	}
	if payload.RefreshToken == "" {
		payload.RefreshToken = previousRefresh
	}
	tokens := &TokenData{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
		Scope:        payload.Scope,
		DeviceID:     c.deviceID,
	}
	if payload.ExpiresIn > 0 {
		tokens.ExpiresIn = int64(payload.ExpiresIn)
		tokens.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn * float64(time.Second))).UTC().Format(time.RFC3339Nano)
	}
	return PollResult{State: PollAuthorized, Tokens: tokens}, nil
}

func (c *Client) setHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Msh-Platform", "Autoto")
	request.Header.Set("X-Msh-Version", c.clientVersion)
	request.Header.Set("X-Msh-Device-Name", safeHeaderValue(hostname(), "unknown", 256))
	request.Header.Set("X-Msh-Device-Model", safeHeaderValue(deviceModel(), "unknown", 256))
	request.Header.Set("X-Msh-Device-Id", c.deviceID)
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "unknown"
	}
	return name
}

func deviceModel() string {
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

func safeHeaderValue(value, fallback string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBytes || strings.ContainsAny(value, "\x00\r\n") {
		return fallback
	}
	return value
}

func validateVerificationURI(raw string, allowLoopback bool) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", errors.New("Kimi 确认网址无效")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if allowLoopback && parsed.Scheme == "http" && isLoopbackHost(host) {
		return parsed.String(), nil
	}
	if parsed.Scheme != "https" || host != "kimi.com" && !strings.HasSuffix(host, ".kimi.com") {
		return "", errors.New("Kimi 确认网址不受信任")
	}
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
