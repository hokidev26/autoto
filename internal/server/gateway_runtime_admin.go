package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/anthropicauth"
	"autoto/internal/codexauth"
	"autoto/internal/config"
	gatewaypkg "autoto/internal/gateway"
	"autoto/internal/subscriptionauth"
)

// GatewayRuntimeController is the minimal live-runtime surface used by the
// management server. Persistence and publication remain owned by Server.
type GatewayRuntimeController interface {
	Reconfigure(context.Context, config.GatewayConfig) error
	Status() gatewaypkg.ManagerStatus
}

type gatewayConfigPatchRequest struct {
	Enabled              *bool   `json:"enabled"`
	Host                 *string `json:"host"`
	Port                 *int    `json:"port"`
	AllowRemote          *bool   `json:"allowRemote"`
	MaxGlobalConcurrency *int    `json:"maxGlobalConcurrency"`
	MaxRequestBytes      *int64  `json:"maxRequestBytes"`
	ConfirmRemoteRisk    bool    `json:"confirmRemoteRisk"`
}

type gatewayAccountPatchRequest struct {
	Shared *bool `json:"shared"`
}

type gatewayAccountSummary struct {
	Provider  string `json:"provider"`
	AccountID string `json:"accountId"`
	Label     string `json:"label"`
	AuthType  string `json:"authType"`
	Source    string `json:"source"`
	Priority  int    `json:"priority"`
	Disabled  bool   `json:"disabled"`
	Eligible  bool   `json:"eligible"`
	Shared    bool   `json:"shared"`
	Effective bool   `json:"effective"`
	Reason    string `json:"reason"`
}

func (s *Server) gatewayRuntimeStatus(w http.ResponseWriter, _ *http.Request) {
	setNoStore(w)
	controller := s.gatewayRuntimeController()
	if controller == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway runtime is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, controller.Status())
}

func (s *Server) listGatewayRequests(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway store is unavailable")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 200")
			return
		}
		limit = parsed
	}
	requests, err := s.store.ListRecentGatewayRequests(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list gateway requests failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
}

func (s *Server) patchGatewayRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	var request gatewayConfigPatchRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid gateway configuration patch")
		return
	}
	if request.Enabled == nil && request.Host == nil && request.Port == nil && request.AllowRemote == nil && request.MaxGlobalConcurrency == nil && request.MaxRequestBytes == nil {
		writeError(w, http.StatusBadRequest, "gateway configuration patch must include at least one field")
		return
	}

	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	controller := s.gatewayRuntimeController()
	if controller == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway runtime is unavailable")
		return
	}

	s.cfgMu.RLock()
	current := s.cfg
	configPath := s.configPath
	s.cfgMu.RUnlock()
	next := current
	applyGatewayConfigPatch(&next.Gateway, request)
	if err := validateGatewayRuntimeConfig(&next.Gateway); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if gatewayPatchIncreasesRemoteRisk(current.Gateway, next.Gateway, request) && !request.ConfirmRemoteRisk {
		writeError(w, http.StatusBadRequest, "confirmRemoteRisk=true is required for remote Gateway exposure")
		return
	}
	path := effectiveConfigPath(next, configPath)
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusServiceUnavailable, "gateway configuration persistence is unavailable")
		return
	}

	if err := controller.Reconfigure(r.Context(), next.Gateway); err != nil {
		if rollbackErr := rollbackGatewayRuntime(r.Context(), controller, current.Gateway); rollbackErr != nil {
			writeError(w, http.StatusServiceUnavailable, "gateway runtime reconfiguration and rollback failed")
			return
		}
		if gatewaypkg.IsBindError(err) {
			writeError(w, http.StatusConflict, "gateway address is unavailable")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "gateway runtime reconfiguration failed")
		return
	}
	if err := config.Save(path, next); err != nil {
		if rollbackErr := rollbackGatewayRuntime(r.Context(), controller, current.Gateway); rollbackErr != nil {
			writeError(w, http.StatusServiceUnavailable, "gateway configuration save and runtime rollback failed")
			return
		}
		writeError(w, http.StatusInternalServerError, "gateway configuration could not be saved")
		return
	}

	s.cfgMu.Lock()
	s.cfg = next
	s.cfgMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"gateway": next.Gateway,
		"status":  controller.Status(),
	})
}

func applyGatewayConfigPatch(target *config.GatewayConfig, request gatewayConfigPatchRequest) {
	if request.Enabled != nil {
		target.Enabled = *request.Enabled
	}
	if request.Host != nil {
		target.Host = strings.TrimSpace(*request.Host)
	}
	if request.Port != nil {
		target.Port = *request.Port
	}
	if request.AllowRemote != nil {
		target.AllowRemote = *request.AllowRemote
	}
	if request.MaxGlobalConcurrency != nil {
		target.MaxGlobalConcurrency = *request.MaxGlobalConcurrency
	}
	if request.MaxRequestBytes != nil {
		target.MaxRequestBytes = *request.MaxRequestBytes
	}
}

func validateGatewayRuntimeConfig(value *config.GatewayConfig) error {
	if value == nil {
		return errors.New("gateway configuration is required")
	}
	host, loopback, err := normalizeGatewayRuntimeHost(value.Host, value.AllowRemote)
	if err != nil {
		return err
	}
	if !value.AllowRemote && !loopback {
		return errors.New("allowRemote must be true for a non-loopback Gateway host")
	}
	value.Host = host
	if value.Port < 1 || value.Port > 65535 {
		return errors.New("gateway port must be between 1 and 65535")
	}
	if value.MaxGlobalConcurrency < 1 || value.MaxGlobalConcurrency > 1024 {
		return errors.New("maxGlobalConcurrency must be between 1 and 1024")
	}
	if value.MaxRequestBytes < 1<<10 || value.MaxRequestBytes > 64<<20 {
		return errors.New("maxRequestBytes must be between 1024 and 67108864")
	}
	return nil
}

func normalizeGatewayRuntimeHost(raw string, allowRemote bool) (string, bool, error) {
	if !utf8.ValidString(raw) {
		return "", false, errors.New("gateway host must be valid UTF-8")
	}
	for _, char := range raw {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return "", false, errors.New("gateway host contains invalid characters")
		}
	}
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", false, errors.New("gateway host is required")
	}
	if strings.EqualFold(host, "localhost") {
		return "localhost", true, nil
	}
	if host == "*" {
		if !allowRemote {
			return "", false, errors.New("allowRemote must be true for wildcard Gateway binding")
		}
		return "0.0.0.0", false, nil
	}
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if len(host) < 3 || host[0] != '[' || host[len(host)-1] != ']' {
			return "", false, errors.New("gateway host is invalid")
		}
		host = host[1 : len(host)-1]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", false, errors.New("gateway host must be localhost or an IP address")
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String(), ipv4.IsLoopback(), nil
	}
	return ip.String(), ip.IsLoopback(), nil
}

func gatewayPatchIncreasesRemoteRisk(current, next config.GatewayConfig, request gatewayConfigPatchRequest) bool {
	_, nextLoopback, err := normalizeGatewayRuntimeHost(next.Host, true)
	if err != nil {
		return false
	}
	if !current.AllowRemote && next.AllowRemote {
		return true
	}
	if request.Host != nil && !nextLoopback {
		return true
	}
	return !current.Enabled && next.Enabled && (next.AllowRemote || !nextLoopback)
}

func rollbackGatewayRuntime(ctx context.Context, controller GatewayRuntimeController, cfg config.GatewayConfig) error {
	if controller == nil {
		return errors.New("gateway runtime is unavailable")
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return controller.Reconfigure(rollbackCtx, cfg)
}

func (s *Server) listGatewayAccounts(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !s.gatewayStoreAvailable(w) {
		return
	}
	accounts, err := s.gatewayAccountSummaries(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gateway accounts could not be listed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Server) patchGatewayAccount(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !s.gatewayStoreAvailable(w) {
		return
	}
	var request gatewayAccountPatchRequest
	if err := decodeJSON(r, &request); err != nil || request.Shared == nil {
		writeError(w, http.StatusBadRequest, "gateway account patch must contain only shared")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))
	accountID := strings.TrimSpace(chi.URLParam(r, "accountId"))
	_, subscriptionProvider := subscriptionOAuthProvider(provider)
	if provider != codexauth.DefaultProviderName && provider != anthropicauth.DefaultProviderName && !subscriptionProvider {
		writeError(w, http.StatusBadRequest, "unsupported gateway account provider")
		return
	}

	account, exists, err := s.gatewayAccountSummary(r.Context(), provider, accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gateway account could not be loaded")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "gateway account not found")
		return
	}
	if !account.Eligible {
		writeError(w, http.StatusBadRequest, "gateway sharing is not supported for this account")
		return
	}
	if *request.Shared && account.Disabled && !account.Shared {
		writeError(w, http.StatusConflict, "disabled accounts cannot receive a new Gateway grant")
		return
	}
	if err := s.store.SetGatewayAccountGrant(r.Context(), provider, accountID, *request.Shared); err != nil {
		writeError(w, http.StatusInternalServerError, "gateway account grant could not be saved")
		return
	}
	account, _, err = s.gatewayAccountSummary(r.Context(), provider, accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gateway account grant was saved but could not be reloaded")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) gatewayAccountSummaries(ctx context.Context) ([]gatewayAccountSummary, error) {
	cfg := s.configSnapshot()
	sharedByProvider := make(map[string]map[string]bool, 5)
	for _, provider := range []string{
		codexauth.DefaultProviderName,
		anthropicauth.DefaultProviderName,
		subscriptionauth.ProviderGemini,
		subscriptionauth.ProviderGrok,
		subscriptionauth.ProviderKimi,
		subscriptionauth.ProviderKiro,
	} {
		shared, err := s.gatewaySharedAccountSet(ctx, provider)
		if err != nil {
			return nil, err
		}
		sharedByProvider[provider] = shared
	}
	accounts := make([]gatewayAccountSummary, 0)

	codexStore := codexauth.NewStore(codexauth.DefaultStoreDir(cfg.Paths.HomeDir))
	codexAccounts, err := codexStore.ListAccounts()
	if err != nil {
		return nil, err
	}
	for _, item := range codexAccounts {
		label := strings.TrimSpace(item.Alias)
		if label == "" {
			label = strings.TrimSpace(item.Email)
		}
		if label == "" {
			label = "Codex account"
		}
		accounts = append(accounts, s.finalizeGatewayAccountSummary(cfg, gatewayAccountSummary{
			Provider: codexauth.DefaultProviderName, AccountID: item.ID, Label: label,
			AuthType: "oauth", Source: "managed", Priority: item.Priority, Disabled: item.Disabled, Eligible: true,
			Shared: sharedByProvider[codexauth.DefaultProviderName][item.ID],
		}))
	}

	anthropicStore := anthropicauth.NewStore(anthropicauth.DefaultStoreDir(cfg.Paths.HomeDir))
	anthropicItems, err := anthropicStore.Load()
	if err != nil {
		return nil, err
	}
	for _, item := range anthropicItems {
		summary := anthropicauth.Summary(item)
		label := strings.TrimSpace(summary.Alias)
		if label == "" {
			label = gatewayAnthropicAccountLabel(summary.AuthType)
		}
		eligible := summary.AuthType == anthropicauth.AuthTypeOAuth || summary.AuthType == anthropicauth.AuthTypeAPIKey
		accounts = append(accounts, s.finalizeGatewayAccountSummary(cfg, gatewayAccountSummary{
			Provider: anthropicauth.DefaultProviderName, AccountID: summary.ID, Label: label,
			AuthType: summary.AuthType, Source: "managed", Priority: summary.Priority, Disabled: summary.Disabled,
			Eligible: eligible, Shared: sharedByProvider[anthropicauth.DefaultProviderName][summary.ID],
		}))
	}
	if provider, ok := gatewayProviderConfig(cfg, anthropicauth.DefaultProviderName); ok && strings.TrimSpace(provider.APIKey) != "" && !anthropicStoreContainsAPIKey(anthropicItems, provider.APIKey) {
		accounts = append(accounts, s.finalizeGatewayAccountSummary(cfg, gatewayAccountSummary{
			Provider: anthropicauth.DefaultProviderName, AccountID: anthropicConfiguredAccountID,
			Label: "Configured API key", AuthType: anthropicauth.AuthTypeAPIKey, Source: "configured",
			Priority: 1_000_000, Disabled: provider.Disabled, Eligible: true,
			Shared: sharedByProvider[anthropicauth.DefaultProviderName][anthropicConfiguredAccountID],
		}))
	}

	for _, provider := range []string{
		subscriptionauth.ProviderGemini,
		subscriptionauth.ProviderGrok,
		subscriptionauth.ProviderKimi,
		subscriptionauth.ProviderKiro,
	} {
		dir := subscriptionauth.DefaultStoreDir(cfg.Paths.HomeDir, provider)
		if dir == "" {
			continue
		}
		store := subscriptionauth.NewStore(dir)
		items, err := store.ListAccounts()
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			accounts = append(accounts, s.finalizeGatewayAccountSummary(cfg, gatewayAccountSummary{
				Provider: provider, AccountID: item.ID, Label: gatewaySubscriptionAccountLabel(provider, item),
				AuthType: "oauth", Source: "managed", Priority: item.Priority, Disabled: item.Disabled,
				Eligible: true, Shared: sharedByProvider[provider][item.ID],
			}))
		}
	}
	return accounts, nil
}

func (s *Server) gatewayAccountSummary(ctx context.Context, provider, accountID string) (gatewayAccountSummary, bool, error) {
	accounts, err := s.gatewayAccountSummaries(ctx)
	if err != nil {
		return gatewayAccountSummary{}, false, err
	}
	for _, account := range accounts {
		if account.Provider == provider && account.AccountID == accountID {
			return account, true, nil
		}
	}
	return gatewayAccountSummary{}, false, nil
}

func (s *Server) gatewaySharedAccountSet(ctx context.Context, provider string) (map[string]bool, error) {
	ids, err := s.store.ListSharedGatewayAccountIDs(ctx, provider)
	if err != nil {
		return nil, err
	}
	shared := make(map[string]bool, len(ids))
	for _, id := range ids {
		shared[id] = true
	}
	return shared, nil
}

func (s *Server) finalizeGatewayAccountSummary(cfg config.Config, account gatewayAccountSummary) gatewayAccountSummary {
	provider, found := gatewayProviderConfig(cfg, account.Provider)
	switch {
	case !account.Eligible:
		account.Reason = "unsupported_auth_type"
	case account.Disabled:
		account.Reason = "account_disabled"
	case !found:
		account.Reason = "provider_not_configured"
	case provider.Disabled:
		account.Reason = "provider_disabled"
	case !provider.GatewayEnabled || providerGatewaySharingForbidden(provider.Type, provider.Profile):
		account.Reason = "provider_gateway_disabled"
	case !account.Shared:
		account.Reason = "not_shared"
	default:
		account.Effective = true
		account.Reason = "eligible"
	}
	return account
}

func gatewayProviderConfig(cfg config.Config, name string) (config.ProviderConfig, bool) {
	name = strings.TrimSpace(name)
	isNativeSubscription := name == subscriptionauth.ProviderGemini || name == subscriptionauth.ProviderGrok || name == subscriptionauth.ProviderKimi
	var nameMatch *config.ProviderConfig
	for _, provider := range cfg.Providers.Instances {
		provider = config.NormalizeProviderConfig(provider)
		if isNativeSubscription && strings.EqualFold(strings.TrimSpace(provider.Type), name) {
			return provider, true
		}
		if nameMatch == nil && strings.EqualFold(strings.TrimSpace(provider.Name), name) {
			matched := provider
			nameMatch = &matched
		}
	}
	if nameMatch != nil {
		return *nameMatch, true
	}
	return config.ProviderConfig{}, false
}

func gatewayAnthropicAccountLabel(authType string) string {
	switch authType {
	case anthropicauth.AuthTypeOAuth:
		return "Anthropic OAuth account"
	case anthropicauth.AuthTypeAPIKey:
		return "Anthropic API key"
	case anthropicauth.AuthTypeProfile:
		return "Anthropic profile"
	default:
		return "Anthropic account"
	}
}

func gatewaySubscriptionAccountLabel(provider string, account subscriptionauth.AccountSummary) string {
	if label := strings.TrimSpace(account.Alias); label != "" {
		return label
	}
	if label := strings.TrimSpace(account.Email); label != "" {
		return label
	}
	switch provider {
	case subscriptionauth.ProviderGemini:
		return "Gemini account"
	case subscriptionauth.ProviderGrok:
		return "Grok account"
	case subscriptionauth.ProviderKimi:
		return "Kimi account"
	default:
		return "Subscription account"
	}
}
