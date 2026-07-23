package anthropicauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

// Anthropic subscription (Claude Pro/Max) OAuth parameters. These mirror the
// first-party Claude login flow: authorization happens at claude.ai, the code
// is exchanged at the console token endpoint, and the resulting access token is
// sent to the Messages API as a Bearer token together with the OAuthBetaHeader.
//
// The redirect URI is the console callback registered for this client, so the
// browser shows a "code#state" string the user copies back into Autoto (manual
// paste flow) instead of an automatic loopback callback.
//
// If Anthropic changes any of these, only this block needs updating.
const (
	OAuthClientID          = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	OAuthAuthorizeEndpoint = "https://claude.ai/oauth/authorize"
	OAuthTokenEndpoint     = "https://console.anthropic.com/v1/oauth/token"
	OAuthRedirectURI       = "https://console.anthropic.com/oauth/code/callback"
	OAuthScope             = "org:create_api_key user:profile user:inference"
	// OAuthBetaHeader is the anthropic-beta value that authorizes OAuth bearer
	// tokens on the Messages API.
	OAuthBetaHeader = "oauth-2025-04-20"

	oauthMaxTokenResponseBytes = 1 << 20
)

// PKCE holds a code verifier/challenge pair for the S256 method.
type PKCE struct {
	Verifier  string
	Challenge string
}

// OAuthTokenResponse is the subset of the token endpoint response Autoto keeps.
type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// NewOAuthState returns a random, URL-safe state value.
func NewOAuthState() (string, error) {
	return oauthRandomURLString(32)
}

// NewPKCE returns a fresh verifier/challenge pair.
func NewPKCE() (PKCE, error) {
	verifier, err := oauthRandomURLString(32)
	if err != nil {
		return PKCE{}, err
	}
	challenge, err := PKCEChallenge(verifier)
	if err != nil {
		return PKCE{}, err
	}
	return PKCE{Verifier: verifier, Challenge: challenge}, nil
}

// PKCEChallenge derives the S256 challenge for a verifier.
func PKCEChallenge(verifier string) (string, error) {
	verifier = strings.TrimSpace(verifier)
	if len(verifier) < 43 || len(verifier) > 128 {
		return "", errors.New("PKCE verifier 長度無效")
	}
	for _, char := range verifier {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", char) {
			continue
		}
		return "", errors.New("PKCE verifier 包含無效字元")
	}
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// BuildAuthorizeURL constructs the browser authorization URL for the Claude
// login. The user opens it, signs in, and copies back the displayed code.
func BuildAuthorizeURL(state, challenge string) (string, error) {
	state = strings.TrimSpace(state)
	challenge = strings.TrimSpace(challenge)
	if state == "" || strings.ContainsRune(state, 0) {
		return "", errors.New("OAuth state 無效")
	}
	if challenge == "" || strings.ContainsRune(challenge, 0) {
		return "", errors.New("PKCE challenge 無效")
	}
	endpoint, err := neturl.Parse(OAuthAuthorizeEndpoint)
	if err != nil {
		return "", errors.New("Anthropic OAuth authorize 端點無效")
	}
	query := endpoint.Query()
	query.Set("code", "true")
	query.Set("response_type", "code")
	query.Set("client_id", OAuthClientID)
	query.Set("redirect_uri", OAuthRedirectURI)
	query.Set("scope", OAuthScope)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

// ParseAuthorizationCode splits the pasted "code#state" value the console
// callback shows. The state segment is optional; when present it must match the
// login session's state (verified by the caller).
func ParseAuthorizationCode(raw string) (code, state string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if idx := strings.IndexRune(raw, '#'); idx >= 0 {
		return strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+1:])
	}
	return raw, ""
}

// ExchangeAuthorizationCode trades a one-time code for tokens. Errors omit the
// code, verifier, and upstream body.
func ExchangeAuthorizationCode(ctx context.Context, code, verifier, state string, httpClient *http.Client) (OAuthTokenResponse, error) {
	code = strings.TrimSpace(code)
	verifier = strings.TrimSpace(verifier)
	if code == "" || strings.ContainsRune(code, 0) {
		return OAuthTokenResponse{}, errors.New("OAuth authorization code 無效")
	}
	if _, err := PKCEChallenge(verifier); err != nil {
		return OAuthTokenResponse{}, err
	}
	payload := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     OAuthClientID,
		"code":          code,
		"redirect_uri":  OAuthRedirectURI,
		"code_verifier": verifier,
	}
	if state = strings.TrimSpace(state); state != "" {
		payload["state"] = state
	}
	return postOAuthToken(ctx, payload, httpClient)
}

// RefreshTokens exchanges a refresh token for a fresh access token.
func RefreshTokens(ctx context.Context, refreshToken string, httpClient *http.Client) (OAuthTokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || strings.ContainsRune(refreshToken, 0) {
		return OAuthTokenResponse{}, errors.New("OAuth refresh token 無效")
	}
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     OAuthClientID,
		"refresh_token": refreshToken,
	}
	tokens, err := postOAuthToken(ctx, payload, httpClient)
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	// Some refresh responses omit a new refresh token; keep the previous one.
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	return tokens, nil
}

func postOAuthToken(ctx context.Context, payload map[string]string, httpClient *http.Client) (OAuthTokenResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return OAuthTokenResponse{}, errors.New("無法構造 Anthropic OAuth token 請求")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return OAuthTokenResponse{}, errors.New("無法構造 Anthropic OAuth token 請求")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	client := oauthHTTPClient(httpClient)
	response, err := client.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return OAuthTokenResponse{}, ctx.Err()
		}
		return OAuthTokenResponse{}, errors.New("Anthropic OAuth token 請求失敗")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return OAuthTokenResponse{}, fmt.Errorf("Anthropic OAuth token 交換失敗（HTTP %d）", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, oauthMaxTokenResponseBytes+1))
	if err != nil || len(data) > oauthMaxTokenResponseBytes {
		return OAuthTokenResponse{}, errors.New("Anthropic OAuth token 回應無效")
	}
	var tokens OAuthTokenResponse
	if err := json.Unmarshal(data, &tokens); err != nil {
		return OAuthTokenResponse{}, errors.New("Anthropic OAuth token 回應無效")
	}
	tokens.AccessToken = strings.TrimSpace(tokens.AccessToken)
	tokens.RefreshToken = strings.TrimSpace(tokens.RefreshToken)
	tokens.TokenType = strings.TrimSpace(tokens.TokenType)
	if tokens.AccessToken == "" {
		return OAuthTokenResponse{}, errors.New("Anthropic OAuth token 回應缺少 access_token")
	}
	return tokens, nil
}

func oauthRandomURLString(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		return "", errors.New("無法生成安全隨機 OAuth 參數")
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func oauthHTTPClient(configured *http.Client) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if configured != nil {
		dup := *configured
		client = &dup
		if client.Timeout <= 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}
