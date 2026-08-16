package server

import (
	"context"
	"net/http"
	"strings"

	"autoto/internal/config"
	"autoto/internal/providers"
	"autoto/internal/secrets"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": config.Version})
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hasUsers": hasUsers, "registrationOpen": s.configSnapshot().Auth.RegistrationOpen})
}

type settingsProviderHeaderResponse struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

type settingsProviderResponse struct {
	Name                    string                                 `json:"name"`
	Type                    string                                 `json:"type"`
	Profile                 string                                 `json:"profile,omitempty"`
	BaseURL                 string                                 `json:"baseUrl,omitempty"`
	Model                   string                                 `json:"model"`
	Models                  []config.ProviderModelConfig           `json:"models"`
	MaxTokens               int64                                  `json:"maxTokens,omitempty"`
	Configured              bool                                   `json:"configured"`
	APIKeyConfigured        bool                                   `json:"apiKeyConfigured"`
	APIKeyPersisted         bool                                   `json:"apiKeyPersisted"`
	APIKeyLastFive          string                                 `json:"apiKeyLastFive,omitempty"`
	APIKeySource            string                                 `json:"apiKeySource"`
	APIKeyOptional          bool                                   `json:"apiKeyOptional,omitempty"`
	GatewayEnabled          bool                                   `json:"gatewayEnabled"`
	Enabled                 bool                                   `json:"enabled"`
	Origin                  string                                 `json:"origin"`
	ProxyURL                string                                 `json:"proxyUrl,omitempty"`
	ProxyAuthConfigured     bool                                   `json:"proxyAuthConfigured"`
	ProxyAuthPersisted      bool                                   `json:"proxyAuthPersisted"`
	ProxyAuthSource         string                                 `json:"proxyAuthSource"`
	UserAgent               string                                 `json:"userAgent,omitempty"`
	ClientIdentity          string                                 `json:"clientIdentity,omitempty"`
	RequestHeaders          []settingsProviderHeaderResponse       `json:"requestHeaders,omitempty"`
	RequestHeadersPersisted bool                                   `json:"requestHeadersPersisted"`
	RequestHeadersSource    string                                 `json:"requestHeadersSource"`
	InsecureSkipTLSVerify   bool                                   `json:"insecureSkipTLSVerify"`
	AllowPlaintextHTTP      bool                                   `json:"allowPlaintextHTTP"`
	Capabilities            providers.Capabilities                 `json:"capabilities"`
	ModelCapabilities       map[string]providers.ModelCapabilities `json:"modelCapabilities,omitempty"`
	Management              *providerManagementResponse            `json:"management,omitempty"`
}

func (s *Server) settingsProviderResponse(ctx context.Context, provider config.ProviderConfig) settingsProviderResponse {
	return s.settingsProviderResponseWithSnapshot(ctx, provider, nil)
}

func (s *Server) settingsProviderResponseWithSnapshot(ctx context.Context, provider config.ProviderConfig, snapshot *secrets.ProviderSecretMetadataSnapshot) settingsProviderResponse {
	safeProvider := config.NormalizeProviderConfig(provider)
	summary := safeProvider.Summary()
	metadata := s.providerSettingsMetadata(summary, safeProvider)
	keyStatus := s.providerAPIKeyStatusWithSnapshot(ctx, provider, snapshot)
	proxyStatus := s.providerProxyAuthStatusWithSnapshot(ctx, provider, snapshot)
	headerStatus := s.providerRequestHeadersStatusWithSnapshot(ctx, provider, snapshot)
	headers := make([]settingsProviderHeaderResponse, 0, len(provider.RequestHeaders))
	for _, header := range provider.RequestHeaders {
		name := strings.TrimSpace(header.Name)
		if name == "" {
			continue
		}
		headers = append(headers, settingsProviderHeaderResponse{Name: name, Configured: headerStatus.Configured && header.Value != ""})
	}
	var registered providers.Provider
	if s.providers != nil {
		registered, _ = s.providers.Get(safeProvider.Name)
	}
	return settingsProviderResponse{
		Name: summary.Name, Type: summary.Type, Profile: metadata.Profile, BaseURL: summary.BaseURL, Model: summary.Model,
		Models: safeProvider.Models, MaxTokens: summary.MaxTokens, Configured: s.providerConfigured(summary), APIKeyConfigured: keyStatus.Configured,
		APIKeyPersisted: keyStatus.Persisted, APIKeyLastFive: keyStatus.LastFive, APIKeySource: keyStatus.Source,
		APIKeyOptional: summary.APIKeyOptional, GatewayEnabled: summary.GatewayEnabled, Enabled: summary.Enabled,
		Origin: summary.Origin, ProxyURL: safeProvider.ProxyURL, ProxyAuthConfigured: proxyStatus.Configured,
		ProxyAuthPersisted: proxyStatus.Persisted, ProxyAuthSource: proxyStatus.Source, UserAgent: provider.UserAgent,
		ClientIdentity: provider.ClientIdentity,
		RequestHeaders: headers, RequestHeadersPersisted: headerStatus.Persisted, RequestHeadersSource: headerStatus.Source,
		InsecureSkipTLSVerify: provider.InsecureSkipTLSVerify, AllowPlaintextHTTP: provider.AllowPlaintextHTTP,
		Capabilities: metadata.Capabilities, ModelCapabilities: modelCapabilitiesForModels(registered, safeProvider, configuredModelNames(safeProvider)),
		Management: metadata.Management,
	}
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	snapshot := s.providerSecretMetadataSnapshot(r.Context())
	providerResponses := make([]settingsProviderResponse, 0, len(cfg.Providers.Instances))
	for _, provider := range cfg.Providers.Instances {
		providerResponses = append(providerResponses, s.settingsProviderResponseWithSnapshot(r.Context(), provider, snapshot))
	}
	runtimeSettings, err := s.runtimeSettingsForResponse(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server":                         cfg.Server,
		"gateway":                        cfg.Gateway,
		"background":                     cfg.Background,
		"paths":                          cfg.Paths,
		"agent":                          cfg.Agent,
		"agentModelSettingsEndpoint":     "/api/runtime/agent-model-settings",
		"continuationSettingsEndpoint":   "/api/runtime/continuation-settings",
		"backgroundTaskSettingsEndpoint": "/api/runtime/background-task-settings",
		"retryPolicySettingsEndpoint":    "/api/runtime/retry-policy-settings",
		"contextSettingsEndpoint":        "/api/runtime/context-settings",
		"contextManagement":              cfg.ContextManagement,
		"providers":                      providerResponses,
		"runtimeSettings":                runtimeSettings,
		"tierOrder":                      subscriptionTierOrderSnapshot(),
		"version":                        config.Version,
	})
}
