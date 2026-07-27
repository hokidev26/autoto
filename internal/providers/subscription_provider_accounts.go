package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"autoto/internal/config"
	"autoto/internal/subscriptionauth"
)

const subscriptionAccessTokenRefreshAhead = 5 * time.Minute

type subscriptionRefreshFunc func(context.Context, subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error)

type subscriptionProviderAccounts struct {
	cfg              config.ProviderConfig
	expectedProvider string
	store            *subscriptionauth.Store
	clock            func() time.Time

	telemetryMu sync.RWMutex
	telemetry   AccountTelemetry

	gatewayPolicyMu sync.RWMutex
	gatewayPolicy   GatewayAccountPolicy

	refreshMu    sync.Mutex
	refreshGates map[string]chan struct{}
}

func newSubscriptionProviderAccounts(cfg config.ProviderConfig, expectedProvider string) *subscriptionProviderAccounts {
	return &subscriptionProviderAccounts{
		cfg:              cfg,
		expectedProvider: strings.ToLower(strings.TrimSpace(expectedProvider)),
		store:            subscriptionauth.NewStore(cfg.CredentialStorePath),
		clock:            time.Now,
		refreshGates:     make(map[string]chan struct{}),
	}
}

func (a *subscriptionProviderAccounts) setAccountTelemetry(telemetry AccountTelemetry) {
	if a == nil {
		return
	}
	a.telemetryMu.Lock()
	a.telemetry = telemetry
	a.telemetryMu.Unlock()
}

func (a *subscriptionProviderAccounts) setGatewayAccountPolicy(policy GatewayAccountPolicy) {
	if a == nil {
		return
	}
	a.gatewayPolicyMu.Lock()
	a.gatewayPolicy = policy
	a.gatewayPolicyMu.Unlock()
}

func (a *subscriptionProviderAccounts) gatewayAccountPolicy() GatewayAccountPolicy {
	if a == nil {
		return nil
	}
	a.gatewayPolicyMu.RLock()
	defer a.gatewayPolicyMu.RUnlock()
	return a.gatewayPolicy
}

func (a *subscriptionProviderAccounts) configured() bool {
	if a == nil {
		return false
	}
	accounts, err := a.enabledAccounts()
	return err == nil && len(accounts) > 0
}

func (a *subscriptionProviderAccounts) configuredForScenario(scenario CallScenario) bool {
	if scenario == CallScenarioGateway {
		// Gateway eligibility additionally depends on request-scoped sharing and
		// an explicit account grant, which this static interface cannot express.
		return false
	}
	return a.configured()
}

func (a *subscriptionProviderAccounts) availableForScenario(ctx context.Context, availability ScenarioAvailability) bool {
	if a == nil || ctx == nil {
		return false
	}
	accounts, err := a.accountsForRequest(ctx, GenerateRequest{
		Scenario:                     availability.EffectiveScenario(),
		AllowSubscriptionCredentials: availability.AllowSubscriptionCredentials,
	})
	return err == nil && len(accounts) > 0
}

func (a *subscriptionProviderAccounts) enabledAccounts() ([]subscriptionauth.StoredCredential, error) {
	if a == nil || a.store == nil || strings.TrimSpace(a.store.Dir()) == "" {
		return nil, providerUnavailableError(a.providerName(), "subscription credential store is not configured")
	}
	items, err := a.store.Load()
	if err != nil {
		return nil, providerUnavailableError(a.providerName(), "subscription credential store is unavailable")
	}
	accounts := make([]subscriptionauth.StoredCredential, 0, len(items))
	for _, item := range items {
		if item.Disabled || strings.ToLower(strings.TrimSpace(item.Provider)) != a.expectedProvider {
			continue
		}
		accounts = append(accounts, item)
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].Priority != accounts[j].Priority {
			return accounts[i].Priority < accounts[j].Priority
		}
		return accounts[i].ID < accounts[j].ID
	})
	return accounts, nil
}

func (a *subscriptionProviderAccounts) accountsForRequest(ctx context.Context, req GenerateRequest) ([]subscriptionauth.StoredCredential, error) {
	if ctx == nil {
		return nil, errors.New("subscription provider request context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accounts, err := a.enabledAccounts()
	if err != nil {
		return nil, err
	}
	if req.EffectiveScenario() != CallScenarioGateway {
		if len(accounts) == 0 {
			return nil, providerUnavailableError(a.providerName(), "subscription credentials are not configured")
		}
		return accounts, nil
	}
	if !req.AllowSubscriptionCredentials {
		return nil, providerUnavailableError(a.providerName(), "Gateway subscription credential sharing is not authorized")
	}
	granted, err := gatewayAccountIDSet(ctx, a.gatewayAccountPolicy(), a.providerName())
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, providerUnavailableError(a.providerName(), "Gateway account grants are unavailable")
	}
	filtered := make([]subscriptionauth.StoredCredential, 0, len(accounts))
	for _, item := range accounts {
		if _, ok := granted[item.ID]; ok {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return nil, providerUnavailableError(a.providerName(), "Gateway has no authorized subscription account")
	}
	return filtered, nil
}

func (a *subscriptionProviderAccounts) prepareCredential(ctx context.Context, item subscriptionauth.StoredCredential, refresh subscriptionRefreshFunc) (subscriptionauth.StoredCredential, error) {
	if ctx == nil {
		return item, errors.New("subscription provider request context is required")
	}
	if err := ctx.Err(); err != nil {
		return item, err
	}
	needsRefresh, expired, err := a.credentialExpiry(item.Credential)
	if err != nil {
		return item, providerUnavailableError(a.providerName(), "OAuth access token expiry is invalid")
	}
	if !needsRefresh {
		if strings.TrimSpace(item.AccessToken) == "" {
			return item, providerUnavailableError(a.providerName(), "OAuth access token is empty")
		}
		return item, nil
	}
	if strings.TrimSpace(item.RefreshToken) == "" || refresh == nil {
		if expired || strings.TrimSpace(item.AccessToken) == "" {
			return item, providerUnavailableError(a.providerName(), "OAuth access token is expired")
		}
		return item, nil
	}

	gate := a.refreshGate(item.ID)
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return item, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return item, err
	}

	current, currentErr := a.store.GetByID(item.ID)
	if currentErr != nil {
		return item, providerUnavailableError(a.providerName(), "OAuth account is unavailable")
	}
	if current.Disabled || strings.ToLower(strings.TrimSpace(current.Provider)) != a.expectedProvider {
		return item, providerUnavailableError(a.providerName(), "OAuth account is disabled or belongs to another provider")
	}
	item = current
	needsRefresh, expired, err = a.credentialExpiry(item.Credential)
	if err != nil {
		return item, providerUnavailableError(a.providerName(), "OAuth access token expiry is invalid")
	}
	if !needsRefresh {
		if strings.TrimSpace(item.AccessToken) == "" {
			return item, providerUnavailableError(a.providerName(), "OAuth access token is empty")
		}
		return item, nil
	}
	if strings.TrimSpace(item.RefreshToken) == "" {
		if expired || strings.TrimSpace(item.AccessToken) == "" {
			return item, providerUnavailableError(a.providerName(), "OAuth access token is expired")
		}
		return item, nil
	}

	update, refreshErr := refresh(ctx, item.Credential)
	if ctx.Err() != nil {
		return item, ctx.Err()
	}
	update.AccessToken = strings.TrimSpace(update.AccessToken)
	if refreshErr != nil || update.AccessToken == "" {
		if expired || strings.TrimSpace(item.AccessToken) == "" {
			return item, providerUnavailableError(a.providerName(), "OAuth refresh failed for an expired token")
		}
		return item, nil
	}
	update = mergeSubscriptionTokenUpdate(item.Credential, update)
	updated, updateErr := a.store.UpdateTokens(item.ID, update)
	if updateErr != nil {
		if expired || strings.TrimSpace(item.AccessToken) == "" {
			return item, providerUnavailableError(a.providerName(), "refreshed OAuth tokens could not be saved")
		}
		return item, nil
	}
	return updated, nil
}

func mergeSubscriptionTokenUpdate(current subscriptionauth.Credential, update subscriptionauth.TokenUpdate) subscriptionauth.TokenUpdate {
	if strings.TrimSpace(update.IDToken) == "" {
		update.IDToken = current.IDToken
	}
	if strings.TrimSpace(update.TokenType) == "" {
		update.TokenType = current.TokenType
	}
	if strings.TrimSpace(update.Email) == "" {
		update.Email = current.Email
	}
	if strings.TrimSpace(update.Subject) == "" {
		update.Subject = current.Subject
	}
	if strings.TrimSpace(update.Scope) == "" {
		update.Scope = current.Scope
	}
	if strings.TrimSpace(update.ProjectID) == "" {
		update.ProjectID = current.ProjectID
	}
	if strings.TrimSpace(update.DeviceID) == "" {
		update.DeviceID = current.DeviceID
	}
	if strings.TrimSpace(update.TokenEndpoint) == "" {
		update.TokenEndpoint = current.TokenEndpoint
	}
	return update
}

func (a *subscriptionProviderAccounts) credentialExpiry(credential subscriptionauth.Credential) (needsRefresh, expired bool, err error) {
	access := strings.TrimSpace(credential.AccessToken)
	raw := strings.TrimSpace(credential.ExpiresAt)
	if raw == "" {
		return access == "", access == "", nil
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return false, false, err
	}
	now := a.now()
	return access == "" || !now.Before(expiresAt.Add(-subscriptionAccessTokenRefreshAhead)), !now.Before(expiresAt), nil
}

// warmupTokens proactively refreshes every enabled credential that is expired
// or within the refresh-ahead window. Errors are silently ignored — if refresh
// fails here the normal per-request path retries; the cost is just the same
// cold-start latency the user would otherwise see on the first call.
func (a *subscriptionProviderAccounts) warmupTokens(ctx context.Context, refresh subscriptionRefreshFunc) {
	if a == nil || ctx == nil || refresh == nil {
		return
	}
	accounts, err := a.enabledAccounts()
	if err != nil || len(accounts) == 0 {
		return
	}
	for _, item := range accounts {
		if ctx.Err() != nil {
			return
		}
		needsRefresh, _, expiryErr := a.credentialExpiry(item.Credential)
		if expiryErr != nil || !needsRefresh {
			continue
		}
		// prepareCredential handles the gate and store update; ignore errors.
		_, _ = a.prepareCredential(ctx, item, refresh)
	}
}

func (a *subscriptionProviderAccounts) refreshGate(accountID string) chan struct{} {
	accountID = strings.TrimSpace(accountID)
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	gate := a.refreshGates[accountID]
	if gate == nil {
		gate = make(chan struct{}, 1)
		a.refreshGates[accountID] = gate
	}
	return gate
}

func (a *subscriptionProviderAccounts) recordAttempt(ctx context.Context, accountID string, success bool, status int, code string, err error) {
	if a == nil || ctx == nil || ctx.Err() != nil || strings.TrimSpace(accountID) == "" {
		return
	}
	a.telemetryMu.RLock()
	telemetry := a.telemetry
	a.telemetryMu.RUnlock()
	if telemetry == nil {
		return
	}
	if success {
		code = ""
	} else {
		if code == "" {
			code = subscriptionErrorCode(err, status)
		}
		code = safeSubscriptionTelemetryCode(code, status)
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = telemetry.RecordProviderAccountAttempt(recordCtx, ProviderAccountAttempt{
		Provider:    a.telemetryProviderName(),
		AccountID:   accountID,
		Success:     success,
		HTTPStatus:  status,
		StatusCode:  safeTelemetryCode(http.StatusText(status), ""),
		ErrorCode:   safeTelemetryCode(code, ""),
		AttemptedAt: a.now().UTC(),
	})
}

// providerName is the user-facing label used in error messages, so it prefers
// the configured name the user actually sees.
func (a *subscriptionProviderAccounts) providerName() string {
	if a == nil {
		return "subscription"
	}
	if name := strings.TrimSpace(a.cfg.Name); name != "" {
		return name
	}
	if a.expectedProvider != "" {
		return a.expectedProvider
	}
	return "subscription"
}

// telemetryProviderName is the key attempt and quota rows are stored under. It
// must be the subscription provider the credential store uses, NOT the config
// name: the two coincide for grok, but an Antigravity provider is conventionally
// named "gemini-oauth" while its accounts live under "gemini", so writing rows
// under the config name filed them where no reader ever looks — the account UI
// queries by subscription provider, which is why gemini accounts showed neither
// stats nor quota. The config name is user-editable too, so keying telemetry on
// it would orphan an account's history the moment the provider is renamed.
func (a *subscriptionProviderAccounts) telemetryProviderName() string {
	if a == nil {
		return "subscription"
	}
	if provider := strings.TrimSpace(a.expectedProvider); provider != "" {
		return provider
	}
	return a.providerName()
}

func (a *subscriptionProviderAccounts) now() time.Time {
	if a != nil && a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

type subscriptionAttemptError struct {
	provider string
	status   int
	code     string
	cause    error
}

func (e *subscriptionAttemptError) Error() string {
	if e == nil {
		return "subscription provider request failed"
	}
	if e.status > 0 {
		return providerHTTPFailedText(e.provider, e.status)
	}
	return providerRequestFailedText(e.provider)
}

func (e *subscriptionAttemptError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newSubscriptionHTTPError(provider string, status int, code string) error {
	return &subscriptionAttemptError{provider: provider, status: status, code: safeSubscriptionTelemetryCode(code, status)}
}

func newSubscriptionNetworkError(provider string, cause error) error {
	return &subscriptionAttemptError{provider: provider, code: subscriptionErrorCode(cause, 0), cause: cause}
}

func subscriptionErrorStatus(err error) int {
	var attempt *subscriptionAttemptError
	if errors.As(err, &attempt) {
		return attempt.status
	}
	return 0
}

func subscriptionErrorCode(err error, status int) string {
	var attempt *subscriptionAttemptError
	if errors.As(err, &attempt) && attempt.code != "" {
		return attempt.code
	}
	if status > 0 {
		return fmt.Sprintf("http_%d", status)
	}
	return telemetryErrorCode(err)
}

func safeSubscriptionTelemetryCode(code string, status int) string {
	code = strings.TrimSpace(code)
	switch code {
	case "credential_unavailable", "request_construction_failed", "models_request_failed", "models_response_invalid", "model_request_failed", "network_error", "device_id_missing", "invalid_stream_event", "stream_read_error", "stream_closed", "response_failed", "stream_error", "invalid_upstream_response", "context_canceled", "deadline_exceeded":
		return code
	}
	if status > 0 {
		return fmt.Sprintf("http_%d", status)
	}
	return "upstream_error"
}

func sanitizeSubscriptionError(ctx context.Context, provider string, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if isContextTermination(err) {
		return err
	}
	if status := subscriptionErrorStatus(err); status > 0 {
		return errors.New(providerHTTPFailedText(provider, status))
	}
	return errors.New(providerRequestFailedText(provider))
}

func shouldTryNextSubscriptionAccount(ctx context.Context, err error, emittedContent bool) bool {
	if err == nil || emittedContent || ctx != nil && ctx.Err() != nil || isContextTermination(err) {
		return false
	}
	if status := subscriptionErrorStatus(err); status > 0 {
		return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
	}
	var netErr net.Error
	var urlErr *url.Error
	return errors.As(err, &netErr) || errors.As(err, &urlErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func configuredSubscriptionModels(cfg config.ProviderConfig) []string {
	models := make([]string, 0, len(cfg.Models)+1)
	seen := make(map[string]struct{}, len(cfg.Models)+1)
	for _, configured := range cfg.Models {
		appendUniqueModel(&models, seen, configured.Name)
	}
	appendUniqueModel(&models, seen, cfg.Model)
	return models
}

func appendUniqueModel(models *[]string, seen map[string]struct{}, model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	if _, exists := seen[model]; exists {
		return
	}
	seen[model] = struct{}{}
	*models = append(*models, model)
}

func validateSubscriptionTestBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("subscription provider test Base URL is invalid")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("subscription provider test Base URL must be loopback")
	}
	return nil
}
