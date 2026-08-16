package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"autoto/internal/config"
	"autoto/internal/providers"
)

const modelListTimeout = 5 * time.Second

// modelListConcurrency caps how many provider catalogs are fetched at once. The
// work is dominated by network latency; the database stays behind a single
// connection, so a wider fan-out would only queue on it.
const modelListConcurrency = 4

type modelsResponse struct {
	Providers []modelProviderResponse `json:"providers"`
}

type providerManagementResponse struct {
	URL       string `json:"url,omitempty"`
	AuthFiles bool   `json:"authFiles,omitempty"`
}

type modelProviderResponse struct {
	Name              string                                 `json:"name"`
	Type              string                                 `json:"type"`
	Profile           string                                 `json:"profile,omitempty"`
	BaseURL           string                                 `json:"baseUrl,omitempty"`
	DefaultModel      string                                 `json:"defaultModel"`
	MaxTokens         int64                                  `json:"maxTokens,omitempty"`
	Models            []string                               `json:"models"`
	ModelsSource      string                                 `json:"modelsSource"`
	Available         bool                                   `json:"available"`
	RuntimeAvailable  bool                                   `json:"runtimeAvailable"`
	Registered        bool                                   `json:"registered"`
	Discovered        bool                                   `json:"discovered"`
	Configured        bool                                   `json:"configured"`
	APIKeyConfigured  bool                                   `json:"apiKeyConfigured"`
	APIKeyPersisted   bool                                   `json:"apiKeyPersisted"`
	APIKeyLastFive    string                                 `json:"apiKeyLastFive,omitempty"`
	APIKeySource      string                                 `json:"apiKeySource"`
	APIKeyOptional    bool                                   `json:"apiKeyOptional,omitempty"`
	GatewayEnabled    bool                                   `json:"gatewayEnabled"`
	Enabled           bool                                   `json:"enabled"`
	Origin            string                                 `json:"origin"`
	Capabilities      providers.Capabilities                 `json:"capabilities"`
	ModelCapabilities map[string]providers.ModelCapabilities `json:"modelCapabilities,omitempty"`
	Management        *providerManagementResponse            `json:"management,omitempty"`
	ManagementURL     string                                 `json:"managementUrl,omitempty"`
	Error             string                                 `json:"error,omitempty"`
}

// providerSettingsMetadata is ready for the settings response to compose with
// config summaries. Route integration intentionally remains separate.
type providerSettingsMetadata struct {
	Profile      string                      `json:"profile,omitempty"`
	Capabilities providers.Capabilities      `json:"capabilities"`
	Management   *providerManagementResponse `json:"management,omitempty"`
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	summaries := s.configSnapshot().Providers.Summaries()
	out := modelsResponse{Providers: make([]modelProviderResponse, len(summaries))}
	// Each entry may perform a live ListModels round trip bounded by
	// modelListTimeout, so serial iteration made the whole response as slow as
	// the sum of every provider's timeout. Bounded concurrency keeps one slow or
	// unreachable endpoint from holding up the rest. Results are written by index
	// so the response order still follows configuration order.
	sem := make(chan struct{}, modelListConcurrency)
	var wg sync.WaitGroup
	for i, summary := range summaries {
		wg.Add(1)
		go func(idx int, ps config.ProviderSummary) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out.Providers[idx] = s.modelProviderResponse(r.Context(), ps)
		}(i, summary)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) modelProviderResponse(ctx context.Context, provider config.ProviderSummary) modelProviderResponse {
	providerConfig, ok := s.providerConfig(provider.Name)
	if !ok {
		providerConfig = config.NormalizeProviderConfig(config.ProviderConfig{Name: provider.Name, Type: provider.Type, Model: provider.Model})
	}
	metadata := s.providerSettingsMetadata(provider, providerConfig)
	keyStatus := s.providerAPIKeyStatus(ctx, providerConfig)
	response := modelProviderResponse{
		Name:             provider.Name,
		Type:             provider.Type,
		Profile:          metadata.Profile,
		BaseURL:          provider.BaseURL,
		DefaultModel:     provider.Model,
		MaxTokens:        provider.MaxTokens,
		Models:           configuredModelNames(providerConfig),
		ModelsSource:     "configured-default",
		Configured:       s.providerConfigured(provider),
		APIKeyConfigured: keyStatus.Configured,
		APIKeyPersisted:  keyStatus.Persisted,
		APIKeyLastFive:   keyStatus.LastFive,
		APIKeySource:     keyStatus.Source,
		APIKeyOptional:   provider.APIKeyOptional,
		GatewayEnabled:   provider.GatewayEnabled,
		Enabled:          provider.Enabled,
		Origin:           provider.Origin,
		Capabilities:     metadata.Capabilities,
		Management:       metadata.Management,
		ManagementURL:    providerManagementURL(provider),
	}
	// Disabled and unregistered providers remain visible for settings cards, but
	// explicit runtime markers prevent either state from becoming chat-selectable.
	if s.providers == nil {
		if provider.Enabled {
			response.ModelsSource = "fallback"
			response.Error = "模型註冊表尚未初始化。"
		}
		attachModelCapabilities(&response, nil, providerConfig)
		return response
	}
	registered, ok := s.providers.Get(provider.Name)
	response.Registered = ok
	if !provider.Enabled {
		attachModelCapabilities(&response, registered, providerConfig)
		return response
	}
	if !ok {
		response.ModelsSource = "fallback"
		response.Error = fmt.Sprintf("provider %s 尚未註冊。", provider.Name)
		attachModelCapabilities(&response, nil, providerConfig)
		return response
	}
	response.Capabilities = providers.CapabilitiesFor(registered)
	response.Configured = providers.ConfiguredFor(registered, provider.Configured)
	response.RuntimeAvailable = response.Configured
	// Unconfigured providers stay visible with a quiet "待設定" state. Calling
	// ListModels here would turn a missing API key into a red "無法取得模型清單"
	// alert, which reads as a failure rather than incomplete setup.
	if !response.Configured {
		response.ModelsSource = "configured-default"
		response.Discovered = false
		response.Available = false
		attachModelCapabilities(&response, registered, providerConfig)
		return response
	}
	listCtx, cancel := context.WithTimeout(ctx, modelListTimeout)
	defer cancel()
	models, err := registered.ListModels(listCtx)
	if err != nil {
		response.ModelsSource = "fallback"
		response.Error = friendlyModelListError(provider, err)
		attachModelCapabilities(&response, registered, providerConfig)
		return response
	}
	response.Models = mergeModelNames(models, providerConfig)
	attachModelCapabilities(&response, registered, providerConfig)
	response.ModelsSource = "remote"
	response.Discovered = len(response.Models) > 0
	response.Available = response.Configured && response.Discovered
	return response
}

func modelCapabilitiesForModels(provider providers.Provider, cfg config.ProviderConfig, models []string) map[string]providers.ModelCapabilities {
	var out map[string]providers.ModelCapabilities
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		capabilities := providers.ModelCapabilitiesFor(provider, model)
		if !capabilities.ImageGenerationKnown {
			capabilities.ImageGeneration, capabilities.ImageGenerationKnown = cfg.ModelImageGeneration(model)
		}
		if capabilities.ContextTokenLimit <= 0 {
			capabilities.ContextTokenLimit = cfg.ModelContextTokenLimit(model)
		}
		if capabilities.ContextTokenLimit <= 0 && !capabilities.FastModeKnown && !capabilities.FastMode && !capabilities.ImageGenerationKnown && !capabilities.ImageGeneration && len(capabilities.ReasoningEfforts) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]providers.ModelCapabilities)
		}
		out[model] = capabilities
	}
	return out
}

func attachModelCapabilities(response *modelProviderResponse, provider providers.Provider, cfg config.ProviderConfig) {
	if response == nil {
		return
	}
	if capabilities := modelCapabilitiesForModels(provider, cfg, response.Models); len(capabilities) > 0 {
		response.ModelCapabilities = capabilities
	}
}

func (s *Server) providerConfigured(provider config.ProviderSummary) bool {
	if s.providers == nil {
		return provider.Configured
	}
	registered, ok := s.providers.Get(provider.Name)
	if !ok {
		return provider.Configured
	}
	return providers.ConfiguredFor(registered, provider.Configured)
}

// resolveExecutableModel is the server trust boundary for chat model changes.
// Configuration metadata alone is insufficient: the live registry must resolve
// the reference and the resolved provider must currently be enabled and ready.
func (s *Server) resolveExecutableModel(model string) (providers.Provider, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, "", errors.New("model is required")
	}
	if s == nil || s.providers == nil {
		return nil, "", errors.New("model runtime registry is unavailable")
	}
	provider, resolvedModel, err := s.providers.Resolve(model)
	if err != nil {
		return nil, "", fmt.Errorf("model is not registered: %w", err)
	}
	providerName, _ := providers.SplitModel(model)
	if providerName == "" {
		providerName = strings.TrimSpace(provider.Name())
	}
	if providerName == "aggregate" {
		listCtx, cancel := context.WithTimeout(context.Background(), modelListTimeout)
		defer cancel()
		members, listErr := provider.ListModels(listCtx)
		if listErr != nil {
			return nil, "", fmt.Errorf("aggregate model is unavailable: %w", listErr)
		}
		for _, member := range members {
			if _, _, memberErr := s.resolveExecutableModel(member); memberErr == nil {
				return provider, resolvedModel, nil
			}
		}
		return nil, "", fmt.Errorf("aggregate model %q has no configured runtime member", resolvedModel)
	}
	var summary config.ProviderSummary
	found := false
	for _, candidate := range s.configSnapshot().Providers.Summaries() {
		if candidate.Name == providerName {
			summary = candidate
			found = true
			break
		}
	}
	if !found || !summary.Enabled || !providers.ConfiguredFor(provider, summary.Configured) {
		return nil, "", fmt.Errorf("provider %q is not configured for runtime chat", providerName)
	}
	return provider, resolvedModel, nil
}

func (s *Server) providerSettingsMetadata(provider config.ProviderSummary, providerConfig config.ProviderConfig) providerSettingsMetadata {
	metadata := providerSettingsMetadata{Profile: provider.Profile}
	if provider.Type == config.ProviderTypeCodex {
		metadata.Management = &providerManagementResponse{AuthFiles: true}
	} else if provider.Profile == config.ProviderProfileCLIProxyAPI {
		metadata.Management = &providerManagementResponse{
			URL:       providerManagementURL(provider),
			AuthFiles: true,
		}
	}
	if s.providers != nil {
		if registered, ok := s.providers.Get(provider.Name); ok {
			metadata.Capabilities = providers.CapabilitiesFor(registered)
			return metadata
		}
	}
	// Providers that are configured but not registered yet (or whose registry is
	// still initializing) must still advertise their protocol capabilities, or
	// the thinking-effort picker collapses to "auto" in the model catalog.
	metadata.Capabilities = providers.CapabilitiesForConfig(providerConfig)
	return metadata
}

func fallbackModels(defaultModel string) []string {
	if strings.TrimSpace(defaultModel) == "" {
		return []string{}
	}
	return []string{strings.TrimSpace(defaultModel)}
}

func configuredModelNames(provider config.ProviderConfig) []string {
	provider = config.NormalizeProviderConfig(provider)
	models := make([]string, 0, len(provider.Models))
	for _, model := range provider.Models {
		models = append(models, model.Name)
	}
	return normalizeModelNames(models, provider.Model)
}

func mergeModelNames(remote []string, provider config.ProviderConfig) []string {
	models := append([]string(nil), remote...)
	models = append(models, configuredModelNames(provider)...)
	return normalizeModelNames(models, provider.Model)
}

func normalizeModelNames(models []string, defaultModel string) []string {
	seen := make(map[string]bool, len(models)+1)
	out := make([]string, 0, len(models)+1)
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	if len(out) == 0 {
		out = fallbackModels(defaultModel)
	}
	sort.Strings(out)
	return out
}

func friendlyModelListError(provider config.ProviderSummary, err error) string {
	message := err.Error()
	lower := strings.ToLower(message)
	if provider.Type == config.ProviderTypeCodex {
		switch {
		case strings.Contains(lower, "尚未匯入"), strings.Contains(lower, "尚未导入"), strings.Contains(lower, "沒有可用"), strings.Contains(lower, "没有可用"), strings.Contains(lower, "憑證庫"), strings.Contains(lower, "凭据库"):
			return "尚未匯入可用的 Codex OAuth 憑證。"
		case strings.Contains(lower, "401"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "refresh_token"):
			return "Codex OAuth 憑證已失效，請重新匯入最新憑證。"
		case strings.Contains(lower, "context deadline exceeded"):
			return "連線 OpenAI Codex 逾時，請稍後重試。"
		}
		return "無法直接從 OpenAI Codex 取得模型清單：" + message
	}
	if provider.Profile == config.ProviderProfileCLIProxyAPI {
		switch {
		case strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"), strings.Contains(lower, "connect:"):
			return "無法連線本機 CLIProxyAPI。請先啟動 CLIProxyAPI，然後點擊重新整理模型。"
		case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized"):
			return "CLIProxyAPI 返回 401。請確認 CLIProxyAPI 的 api-keys 設定；如啟用了用戶端鑑權，請設定 CLIPROXYAPI_API_KEY 後重啟 Autoto。"
		case strings.Contains(lower, "403"):
			return "CLIProxyAPI 拒絕了模型清單請求。請檢查帳號登入狀態、權限或 API key 設定。"
		case strings.Contains(lower, "context deadline exceeded"):
			return "連線 CLIProxyAPI 逾時。請確認它正在執行並可存取。"
		}
		return "無法從 CLIProxyAPI 取得模型清單：" + message
	}
	return "無法取得模型清單：" + message
}

func providerManagementURL(provider config.ProviderSummary) string {
	if provider.Profile != config.ProviderProfileCLIProxyAPI {
		return ""
	}
	baseURL := provider.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "http://127.0.0.1:8317/v1"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "http://127.0.0.1:8317/management.html"
	}
	parsed.Path = "/management.html"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
