package peercontrol

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"autoto/internal/network"
)

const (
	defaultClientTimeout               = 15 * time.Second
	maxClientTimeout                   = 60 * time.Second
	defaultMaxResponseBytes      int64 = 1 << 20
	maxResponseBytesLimit        int64 = 4 << 20
	maxRequestBytes                    = 1 << 20
	defaultClientSessionCacheTTL       = 5 * time.Minute
)

var ErrProtocol = errors.New("peer protocol error")

// ClientOptions configure a fixed-origin peer protocol client.
type ClientOptions struct {
	Origin           string
	Identity         *Identity
	PeerIdentity     PublicIdentity
	Transport        http.RoundTripper
	Timeout          time.Duration
	MaxResponseBytes int64
	SessionCacheTTL  time.Duration
	Clock            func() time.Time

	// AllowLoopbackHTTPForTests is the only way to permit plaintext HTTP, and
	// only exact 127.0.0.1 or ::1 origins are accepted when it is set.
	AllowLoopbackHTTPForTests bool
}

type cachedClientSession struct {
	pairingID   string
	bearerToken string
	expiresAt   time.Time
}

// Client exposes only fixed peer protocol operations against one immutable
// origin. It never accepts an arbitrary URL or follows redirects.
type Client struct {
	origin           *url.URL
	identity         *Identity
	peerIdentity     PublicIdentity
	httpClient       *http.Client
	maxResponseBytes int64
	cacheTTL         time.Duration
	clock            func() time.Time

	mu                 sync.Mutex
	sessionEstablishMu sync.Mutex
	closed             bool
	session            cachedClientSession
	onClose            func(*Client)
}

// NewClient validates and freezes the endpoint origin and constructs a strict
// total-timeout, no-redirect HTTP client.
func NewClient(options ClientOptions) (*Client, error) {
	if options.Identity == nil {
		return nil, errors.New("peer client identity is required")
	}
	peerPublicKey, err := decodePublicKey(options.PeerIdentity.PublicKey)
	if err != nil || !validFingerprint(options.PeerIdentity.Fingerprint) {
		return nil, errors.New("peer client pinned host identity is required")
	}
	peerFingerprint, _ := FingerprintPublicKey(peerPublicKey)
	if !constantStringEqual(peerFingerprint, options.PeerIdentity.Fingerprint) {
		return nil, errors.New("peer client host fingerprint does not match public key")
	}
	origin, err := normalizeClientOrigin(options.Origin, options.AllowLoopbackHTTPForTests)
	if err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultClientTimeout
	}
	if timeout <= 0 || timeout > maxClientTimeout {
		return nil, errors.New("peer client timeout is out of bounds")
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < 1 || maxResponseBytes > maxResponseBytesLimit {
		return nil, errors.New("peer client response limit is out of bounds")
	}
	cacheTTL := options.SessionCacheTTL
	if cacheTTL == 0 {
		cacheTTL = defaultClientSessionCacheTTL
	}
	if cacheTTL < time.Second || cacheTTL > 15*time.Minute {
		return nil, errors.New("peer client session cache TTL is out of bounds")
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	transport := options.Transport
	if transport == nil {
		base := network.NewProviderDirectTransport()
		base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		base.ForceAttemptHTTP2 = true
		transport = base
	}
	return &Client{
		origin:       origin,
		identity:     options.Identity,
		peerIdentity: options.PeerIdentity,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxResponseBytes: maxResponseBytes,
		cacheTTL:         cacheTTL,
		clock:            clock,
	}, nil
}

// Close clears the cached plaintext bearer and rejects later operations.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.session = cachedClientSession{}
	onClose := c.onClose
	c.onClose = nil
	c.mu.Unlock()
	if onClose != nil {
		onClose(c)
	}
	return nil
}

// PrepareClaim creates the signed controller proof and the separate plaintext
// one-time secret needed by the HTTPS claim endpoint. The returned request must
// never be logged or persisted.
func (c *Client) PrepareClaim(invitation InvitationEnvelope, displayName, installationID string) (ClaimRequest, error) {
	if err := c.requireOpen(); err != nil {
		return ClaimRequest{}, err
	}
	if err := invitation.Validate(c.now()); err != nil || invitation.Origin != c.origin.String() || !constantStringEqual(invitation.HostPublicKey, c.peerIdentity.PublicKey) || !constantStringEqual(invitation.HostFingerprint, c.peerIdentity.Fingerprint) {
		return ClaimRequest{}, fmt.Errorf("%w: invitation does not match pinned host", ErrProtocol)
	}
	claim, err := NewPairingClaimFromInvitation(invitation, displayName, installationID, c.identity, c.now())
	if err != nil {
		return ClaimRequest{}, err
	}
	proof, err := c.identity.SignPairingClaim(claim)
	if err != nil {
		return ClaimRequest{}, err
	}
	secret, err := invitation.SecretToken()
	if err != nil {
		return ClaimRequest{}, err
	}
	return ClaimRequest{Proof: proof, Secret: secret}, nil
}

// Claim signs the canonical claim with the client's Identity and submits the
// original one-time secret separately. A durable secret hash is never accepted
// as the bearer credential by itself.
func (c *Client) Claim(ctx context.Context, request ClaimRequest) (ClaimResponse, error) {
	if err := c.requireOpen(); err != nil {
		return ClaimResponse{}, err
	}
	secret, err := DecodeInvitationSecretToken(request.Secret)
	if err != nil {
		return ClaimResponse{}, fmt.Errorf("%w: invalid invitation secret", ErrProtocol)
	}
	claim := request.Proof.Claim
	public := c.identity.Public()
	if claim.ProtocolVersion == 0 {
		claim.ProtocolVersion = ProtocolVersion
	}
	if claim.PublicKey == "" {
		claim.PublicKey = public.PublicKey
	}
	if claim.Fingerprint == "" {
		claim.Fingerprint = public.Fingerprint
	}
	if claim.IssuedAt == "" {
		claim.IssuedAt = c.now().Format(time.RFC3339Nano)
	}
	if claim.Nonce == "" {
		nonce, nonceErr := GenerateOpaqueToken()
		if nonceErr != nil {
			return ClaimResponse{}, nonceErr
		}
		claim.Nonce = nonce
	}
	if !constantStringEqual(HashInvitationSecretHex(secret), claim.SecretHash) {
		return ClaimResponse{}, fmt.Errorf("%w: invitation secret does not match proof", ErrProtocol)
	}
	proof, err := c.identity.SignPairingClaim(claim)
	if err != nil {
		return ClaimResponse{}, err
	}
	var response ClaimResponse
	if err := c.doJSON(ctx, http.MethodPost, claimEndpoint, ClaimRequest{Proof: proof, Secret: request.Secret}, "", &response); err != nil {
		return ClaimResponse{}, err
	}
	if response.ProtocolVersion != ProtocolVersion || response.InvitationID != proof.Claim.InvitationID || validateIdentifier(response.InvitationID) != nil || response.Revision < 1 {
		return ClaimResponse{}, fmt.Errorf("%w: invalid claim response", ErrProtocol)
	}
	return response, nil
}

// PollClaim refreshes and signs the original controller proof. Invitation IDs
// alone are not accepted as polling credentials.
func (c *Client) PollClaim(ctx context.Context, request PollClaimRequest) (PollClaimResponse, error) {
	if err := c.requireOpen(); err != nil {
		return PollClaimResponse{}, err
	}
	claim := request.Proof.Claim
	if err := validateIdentifier(claim.InvitationID); err != nil {
		return PollClaimResponse{}, fmt.Errorf("%w: invalid invitation", ErrProtocol)
	}
	claim.IssuedAt = c.now().Format(time.RFC3339Nano)
	nonce, err := GenerateOpaqueToken()
	if err != nil {
		return PollClaimResponse{}, err
	}
	claim.Nonce = nonce
	proof, err := c.identity.SignPairingClaim(claim)
	if err != nil {
		return PollClaimResponse{}, err
	}
	var response PollClaimResponse
	if err := c.doJSON(ctx, http.MethodPost, pollClaimEndpoint, PollClaimRequest{Proof: proof}, "", &response); err != nil {
		return PollClaimResponse{}, err
	}
	if response.ProtocolVersion != ProtocolVersion || response.InvitationID != claim.InvitationID || response.Revision < 1 {
		return PollClaimResponse{}, fmt.Errorf("%w: invalid claim poll response", ErrProtocol)
	}
	return response, nil
}

// IssueSessionChallenge requests a one-time challenge without a bearer.
func (c *Client) IssueSessionChallenge(ctx context.Context, request IssueSessionChallengeRequest) (IssueSessionChallengeResponse, error) {
	if err := validateIdentifier(request.PairingID); err != nil {
		return IssueSessionChallengeResponse{}, fmt.Errorf("%w: invalid pairing", ErrProtocol)
	}
	var response IssueSessionChallengeResponse
	if err := c.doJSON(ctx, http.MethodPost, issueSessionChallengeEndpoint, request, "", &response); err != nil {
		return IssueSessionChallengeResponse{}, err
	}
	if err := c.validateSessionChallenge(response.Challenge, request.PairingID); err != nil {
		return IssueSessionChallengeResponse{}, err
	}
	return response, nil
}

// EstablishSession signs challenge with the client's Identity, establishes a
// bearer session, and caches it only for a short bounded interval.
func (c *Client) EstablishSession(ctx context.Context, challenge SessionChallenge) (EstablishSessionResponse, error) {
	response, err := c.establishSession(ctx, challenge)
	if err != nil {
		return EstablishSessionResponse{}, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return EstablishSessionResponse{}, ErrClosed
	}
	c.cacheSessionLocked(challenge.PairingID, response)
	c.mu.Unlock()
	return response, nil
}

// GetSnapshot obtains or refreshes a bearer and retries once on 401 because the
// operation is idempotent.
func (c *Client) GetSnapshot(ctx context.Context, request GetSnapshotRequest) (GetSnapshotResponse, error) {
	var response GetSnapshotResponse
	token, err := c.sessionToken(ctx, request.PairingID)
	if err != nil {
		return GetSnapshotResponse{}, err
	}
	err = c.doJSON(ctx, http.MethodPost, getSnapshotEndpoint, request, token, &response)
	if !errors.Is(err, ErrUnauthorized) {
		return response, err
	}
	c.clearSessionToken(token)
	token, refreshErr := c.sessionToken(ctx, request.PairingID)
	if refreshErr != nil {
		return GetSnapshotResponse{}, refreshErr
	}
	response = GetSnapshotResponse{}
	if err := c.doJSON(ctx, http.MethodPost, getSnapshotEndpoint, request, token, &response); err != nil {
		c.clearSessionTokenIfUnauthorized(token, err)
		return GetSnapshotResponse{}, err
	}
	return response, nil
}

// SendTask sends a non-idempotent request. A 401 clears the cached bearer but
// is returned directly; the task is never retried automatically.
func (c *Client) SendTask(ctx context.Context, request SendTaskRequest) (SendTaskResponse, error) {
	token, err := c.sessionToken(ctx, request.PairingID)
	if err != nil {
		return SendTaskResponse{}, err
	}
	var response SendTaskResponse
	if err := c.doJSON(ctx, http.MethodPost, sendTaskEndpoint, request, token, &response); err != nil {
		c.clearSessionTokenIfUnauthorized(token, err)
		return SendTaskResponse{}, err
	}
	return response, nil
}

// UpdateAgentRuntime patches the host agent's model, thinking strength, or
// permission mode. It is not retried after a 401, because a duplicate patch
// could still race a later local change on the host.
func (c *Client) UpdateAgentRuntime(ctx context.Context, request UpdateAgentRuntimeRequest) (UpdateAgentRuntimeResponse, error) {
	token, err := c.sessionToken(ctx, request.PairingID)
	if err != nil {
		return UpdateAgentRuntimeResponse{}, err
	}
	var response UpdateAgentRuntimeResponse
	if err := c.doJSON(ctx, http.MethodPost, updateAgentRuntimeEndpoint, request, token, &response); err != nil {
		c.clearSessionTokenIfUnauthorized(token, err)
		return UpdateAgentRuntimeResponse{}, err
	}
	return response, nil
}

// ResolveApproval sends a non-idempotent approval decision and never retries it
// automatically after an authorization failure.
func (c *Client) ResolveApproval(ctx context.Context, request ResolveApprovalRequest) (ResolveApprovalResponse, error) {
	token, err := c.sessionToken(ctx, request.PairingID)
	if err != nil {
		return ResolveApprovalResponse{}, err
	}
	var response ResolveApprovalResponse
	if err := c.doJSON(ctx, http.MethodPost, resolveApprovalEndpoint, request, token, &response); err != nil {
		c.clearSessionTokenIfUnauthorized(token, err)
		return ResolveApprovalResponse{}, err
	}
	return response, nil
}

// ExecutionHeartbeat reports device liveness. It is idempotent, so a stale
// bearer is refreshed and the call retried once, exactly like GetSnapshot.
func (c *Client) ExecutionHeartbeat(ctx context.Context, request ExecutionHeartbeatRequest) (ExecutionHeartbeatResponse, error) {
	var response ExecutionHeartbeatResponse
	token, err := c.sessionToken(ctx, request.PairingID)
	if err != nil {
		return ExecutionHeartbeatResponse{}, err
	}
	err = c.doJSON(ctx, http.MethodPost, executionHeartbeatEndpoint, request, token, &response)
	if !errors.Is(err, ErrUnauthorized) {
		return response, err
	}
	c.clearSessionToken(token)
	token, refreshErr := c.sessionToken(ctx, request.PairingID)
	if refreshErr != nil {
		return ExecutionHeartbeatResponse{}, refreshErr
	}
	response = ExecutionHeartbeatResponse{}
	if err := c.doJSON(ctx, http.MethodPost, executionHeartbeatEndpoint, request, token, &response); err != nil {
		c.clearSessionTokenIfUnauthorized(token, err)
		return ExecutionHeartbeatResponse{}, err
	}
	return response, nil
}

// ClaimExecutionTask leases the next queued task. Claiming consumes host state
// and increments the attempt count, so it is never retried automatically: a
// silent retry would burn an attempt on a task this device may already hold.
func (c *Client) ClaimExecutionTask(ctx context.Context, request ExecutionClaimRequest) (ExecutionClaimResponse, error) {
	token, err := c.sessionToken(ctx, request.PairingID)
	if err != nil {
		return ExecutionClaimResponse{}, err
	}
	var response ExecutionClaimResponse
	if err := c.doJSON(ctx, http.MethodPost, executionClaimEndpoint, request, token, &response); err != nil {
		c.clearSessionTokenIfUnauthorized(token, err)
		return ExecutionClaimResponse{}, err
	}
	if task := response.Task; task != nil {
		if validateIdentifier(task.TaskID) != nil || validateIdentifier(task.AgentID) != nil || task.Revision < 1 || !c.now().Before(task.LeaseUntil) {
			return ExecutionClaimResponse{}, fmt.Errorf("%w: invalid execution task", ErrProtocol)
		}
	}
	return response, nil
}

// ReportExecutionTask advances a leased task. The revision in the request makes
// the host side a compare-and-swap, so this is not retried automatically either.
func (c *Client) ReportExecutionTask(ctx context.Context, request ExecutionReportRequest) (ExecutionReportResponse, error) {
	token, err := c.sessionToken(ctx, request.PairingID)
	if err != nil {
		return ExecutionReportResponse{}, err
	}
	var response ExecutionReportResponse
	if err := c.doJSON(ctx, http.MethodPost, executionReportEndpoint, request, token, &response); err != nil {
		c.clearSessionTokenIfUnauthorized(token, err)
		return ExecutionReportResponse{}, err
	}
	return response, nil
}

func (c *Client) establishSession(ctx context.Context, challenge SessionChallenge) (EstablishSessionResponse, error) {
	if err := c.requireOpen(); err != nil {
		return EstablishSessionResponse{}, err
	}
	now := c.now()
	if err := c.validateSessionChallenge(challenge, challenge.PairingID); err != nil {
		return EstablishSessionResponse{}, err
	}
	signed, err := SignChallenge(c.identity, challenge.PairingID, challenge.Challenge, challenge.EndpointOrigin, challenge.ExpiresAt)
	if err != nil {
		return EstablishSessionResponse{}, err
	}
	request := EstablishSessionRequest{PairingID: challenge.PairingID, SignedChallenge: signed}
	var response EstablishSessionResponse
	if err := c.doJSON(ctx, http.MethodPost, establishSessionEndpoint, request, "", &response); err != nil {
		return EstablishSessionResponse{}, err
	}
	if validateRandomToken(response.BearerToken) != nil || !now.Before(response.ExpiresAt) || response.ExpiresAt.After(now.Add(24*time.Hour)) {
		return EstablishSessionResponse{}, fmt.Errorf("%w: invalid session response", ErrProtocol)
	}
	return response, nil
}

func (c *Client) sessionToken(ctx context.Context, pairingID string) (string, error) {
	if err := validateIdentifier(pairingID); err != nil {
		return "", fmt.Errorf("%w: invalid pairing", ErrProtocol)
	}
	if c == nil {
		return "", ErrClosed
	}
	c.sessionEstablishMu.Lock()
	defer c.sessionEstablishMu.Unlock()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", ErrClosed
	}
	now := c.now()
	if c.session.pairingID == pairingID && c.session.bearerToken != "" && now.Before(c.session.expiresAt) {
		token := c.session.bearerToken
		c.mu.Unlock()
		return token, nil
	}
	c.session = cachedClientSession{}
	c.mu.Unlock()

	var issued IssueSessionChallengeResponse
	if err := c.doJSON(ctx, http.MethodPost, issueSessionChallengeEndpoint, IssueSessionChallengeRequest{PairingID: pairingID}, "", &issued); err != nil {
		return "", err
	}
	challenge := issued.Challenge
	if err := c.validateSessionChallenge(challenge, pairingID); err != nil {
		return "", err
	}
	response, err := c.establishSession(ctx, challenge)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", ErrClosed
	}
	c.cacheSessionLocked(pairingID, response)
	return c.session.bearerToken, nil
}

func (c *Client) cacheSessionLocked(pairingID string, response EstablishSessionResponse) {
	expiresAt := response.ExpiresAt.UTC()
	cacheLimit := c.now().Add(c.cacheTTL)
	if cacheLimit.Before(expiresAt) {
		expiresAt = cacheLimit
	}
	c.session = cachedClientSession{pairingID: pairingID, bearerToken: response.BearerToken, expiresAt: expiresAt}
}

func (c *Client) clearSessionToken(token string) {
	c.mu.Lock()
	if constantStringEqual(c.session.bearerToken, token) {
		c.session = cachedClientSession{}
	}
	c.mu.Unlock()
}

func (c *Client) clearSessionTokenIfUnauthorized(token string, err error) {
	if errors.Is(err, ErrUnauthorized) {
		c.clearSessionToken(token)
	}
}

func (c *Client) requireOpen() error {
	if c == nil {
		return ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	return nil
}

func (c *Client) validateSessionChallenge(challenge SessionChallenge, pairingID string) error {
	now := c.now()
	if challenge.ProtocolVersion != ProtocolVersion || validateIdentifier(pairingID) != nil || challenge.PairingID != pairingID || challenge.EndpointOrigin != c.origin.String() || validateRandomToken(challenge.Challenge) != nil || !now.Before(challenge.ExpiresAt) || challenge.ExpiresAt.After(now.Add(5*time.Minute+ChallengeClockSkew)) {
		return fmt.Errorf("%w: invalid session challenge response", ErrProtocol)
	}
	if err := VerifyHostSessionChallenge(challenge, c.peerIdentity, now); err != nil {
		return fmt.Errorf("%w: host session challenge authentication failed", ErrUnauthorized)
	}
	return nil
}

func (c *Client) now() time.Time {
	if c == nil || c.clock == nil {
		return time.Now().UTC()
	}
	return c.clock().UTC()
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, requestValue any, bearerToken string, responseValue any) error {
	if err := c.requireOpen(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(requestValue)
	if err != nil || len(payload) > maxRequestBytes {
		return fmt.Errorf("%w: invalid request body", ErrProtocol)
	}
	requestURL := *c.origin
	requestURL.Path = endpoint
	requestURL.RawPath = ""
	requestURL.RawQuery = ""
	requestURL.Fragment = ""
	if !c.isFixedOriginURL(&requestURL) {
		return fmt.Errorf("%w: request origin changed", ErrProtocol)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: create request", ErrProtocol)
	}
	request.Host = c.origin.Host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if bearerToken != "" {
		if validateRandomToken(bearerToken) != nil || !c.isFixedOriginURL(request.URL) || request.Host != c.origin.Host {
			return ErrUnauthorized
		}
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: peer request failed", ErrProtocol)
	}
	defer response.Body.Close()
	if response.Request == nil || !c.isFixedOriginURL(response.Request.URL) || response.Request.Host != c.origin.Host || response.Request.URL.Path != endpoint || response.Request.URL.RawQuery != "" || response.Request.URL.Fragment != "" {
		return fmt.Errorf("%w: response origin changed", ErrProtocol)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusGone:
			return ErrUnauthorized
		case http.StatusConflict, http.StatusLocked, http.StatusTooManyRequests:
			return ErrConflict
		case http.StatusServiceUnavailable:
			return ErrDisabled
		default:
			return fmt.Errorf("%w: unexpected HTTP status", ErrProtocol)
		}
	}
	if response.ContentLength > c.maxResponseBytes {
		return fmt.Errorf("%w: response body is too large", ErrProtocol)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil || int64(len(body)) > c.maxResponseBytes {
		return fmt.Errorf("%w: response body is too large", ErrProtocol)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(responseValue); err != nil {
		return fmt.Errorf("%w: invalid response JSON", ErrProtocol)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: invalid response JSON", ErrProtocol)
	}
	return nil
}

func (c *Client) isFixedOriginURL(candidate *url.URL) bool {
	return c != nil && c.origin != nil && candidate != nil &&
		candidate.Scheme == c.origin.Scheme && strings.EqualFold(candidate.Host, c.origin.Host) &&
		candidate.User == nil && candidate.Opaque == ""
}

func normalizeClientOrigin(value string, allowLoopbackHTTP bool) (*url.URL, error) {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 {
		return nil, errors.New("invalid peer client origin")
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return nil, errors.New("peer client endpoint must be an origin without userinfo, path, query, or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.ContainsAny(hostname, "\x00/\\%") {
		return nil, errors.New("invalid peer client origin host")
	}
	if scheme != "https" {
		if scheme != "http" || !allowLoopbackHTTP || hostname != "127.0.0.1" && hostname != "::1" {
			return nil, errors.New("peer client origin must use HTTPS")
		}
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, errors.New("invalid peer client origin port")
		}
	}
	if address := net.ParseIP(hostname); address != nil && strings.Contains(parsed.Host, "[") != strings.Contains(hostname, ":") {
		return nil, errors.New("invalid peer client origin host")
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed, nil
}
