// Package kiroauth implements Kiro's token refresh flow.
// Authentication is done by pasting a refreshToken from ~/.kiro/credentials.json.
package kiroauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	DefaultRegion       = "us-east-1"
	RefreshLeadDuration = 5 * time.Minute
	maxResponseBytes    = 1 << 20
)

// AllowedRegions lists the Kiro auth regions.
var AllowedRegions = []string{
	"us-east-1",
	"us-west-2",
	"eu-central-1",
	"ap-northeast-1",
	"ap-southeast-1",
}

var refreshGroup singleflight.Group

// RefreshLead returns how early callers should refresh an access token.
func RefreshLead() time.Duration { return RefreshLeadDuration }

// TokenData holds the result of a successful Kiro token refresh.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ProfileArn   string `json:"profile_arn"`
	Region       string `json:"region"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// refreshRequest is the JSON body sent to the Kiro refresh endpoint.
type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// refreshResponse maps the raw Kiro API response fields.
type refreshResponse struct {
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
	ProfileArn   string  `json:"profileArn"`
	ExpiresIn    float64 `json:"expiresIn"`
}

// RefreshEndpoint returns the Kiro token refresh URL for the given region.
func RefreshEndpoint(region string) string {
	return "https://prod." + region + ".auth.desktop.kiro.dev/refreshToken"
}

// ValidateRegion returns an error if region is not in AllowedRegions.
func ValidateRegion(region string) error {
	for _, r := range AllowedRegions {
		if r == region {
			return nil
		}
	}
	return fmt.Errorf("kiro: unsupported region %q; allowed: %s", region, strings.Join(AllowedRegions, ", "))
}

// RegionFromProfileArn extracts the region component from a Kiro/CodeWhisperer
// profile ARN (e.g. "arn:aws:codewhisperer:us-east-1:1234567890:profile/xxx").
// Returns DefaultRegion if the ARN is empty or cannot be parsed.
func RegionFromProfileArn(arn string) string {
	arn = strings.TrimSpace(arn)
	if arn == "" {
		return DefaultRegion
	}
	// ARN format: arn:partition:service:region:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 5 || strings.TrimSpace(parts[3]) == "" {
		return DefaultRegion
	}
	region := strings.TrimSpace(parts[3])
	if err := ValidateRegion(region); err != nil {
		return DefaultRegion
	}
	return region
}

// Client performs Kiro token refreshes.
type Client struct {
	httpClient      *http.Client
	refreshEndpoint string // overridable for tests
}

// New creates a production Kiro client that hits the real refresh endpoints.
func New(httpClient *http.Client) *Client {
	return &Client{httpClient: safeHTTPClient(httpClient)}
}

// newTestClient is unexported so production callers cannot override endpoints.
func newTestClient(httpClient *http.Client, endpoint string) *Client {
	return &Client{httpClient: safeHTTPClient(httpClient), refreshEndpoint: endpoint}
}

// RefreshToken exchanges a refresh token for a new access token.
// region must be one of AllowedRegions; if empty, DefaultRegion is used.
func (c *Client) RefreshToken(ctx context.Context, refreshToken, region string) (*TokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || strings.ContainsRune(refreshToken, 0) {
		return nil, errors.New("kiro: refresh token 无效")
	}
	region = strings.TrimSpace(region)
	if region == "" {
		region = DefaultRegion
	}
	if err := ValidateRegion(region); err != nil {
		return nil, err
	}

	endpoint := c.endpoint(region)

	// Deduplicate concurrent refreshes with the same token.
	keyHash := sha256.Sum256([]byte(endpoint + "\x00" + refreshToken))
	key := hex.EncodeToString(keyHash[:])

	result, err, _ := refreshGroup.Do(key, func() (any, error) {
		return c.doRefresh(context.WithoutCancel(nonNilContext(ctx)), refreshToken, endpoint, region)
	})
	if err != nil {
		return nil, err
	}
	td, ok := result.(*TokenData)
	if !ok || td == nil {
		return nil, errors.New("kiro: token refresh 结果无效")
	}
	copy := *td
	return &copy, nil
}

func (c *Client) endpoint(region string) string {
	if c.refreshEndpoint != "" {
		return c.refreshEndpoint
	}
	return RefreshEndpoint(region)
}

func (c *Client) doRefresh(ctx context.Context, refreshToken, endpoint, region string) (*TokenData, error) {
	body, err := json.Marshal(refreshRequest{RefreshToken: refreshToken})
	if err != nil {
		return nil, errors.New("kiro: 无法构造 refresh 请求")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("kiro: 无法构造 refresh 请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("kiro: refresh 请求失败")
	}
	defer resp.Body.Close()

	data, err := readLimited(resp.Body)
	if err != nil {
		return nil, errors.New("kiro: refresh 响应过大或读取失败")
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("kiro: refresh token 被拒绝（HTTP %d）", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro: refresh 请求失败（HTTP %d）", resp.StatusCode)
	}

	var raw refreshResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("kiro: refresh 响应无效")
	}

	raw.AccessToken = strings.TrimSpace(raw.AccessToken)
	raw.RefreshToken = strings.TrimSpace(raw.RefreshToken)
	raw.ProfileArn = strings.TrimSpace(raw.ProfileArn)

	if raw.AccessToken == "" {
		return nil, errors.New("kiro: refresh 响应缺少 accessToken")
	}
	if raw.RefreshToken == "" {
		raw.RefreshToken = refreshToken
	}

	// Derive region from ProfileArn if available; fallback to request region.
	resolvedRegion := region
	if raw.ProfileArn != "" {
		if r := RegionFromProfileArn(raw.ProfileArn); r != DefaultRegion || region == DefaultRegion {
			resolvedRegion = r
		}
	}

	td := &TokenData{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ProfileArn:   raw.ProfileArn,
		Region:       resolvedRegion,
	}
	if raw.ExpiresIn > 0 {
		td.ExpiresIn = int64(raw.ExpiresIn)
		td.ExpiresAt = time.Now().Add(time.Duration(raw.ExpiresIn * float64(time.Second))).UTC().Format(time.RFC3339Nano)
	}
	return td, nil
}

func safeHTTPClient(configured *http.Client) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if configured != nil {
		cp := *configured
		client = &cp
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
