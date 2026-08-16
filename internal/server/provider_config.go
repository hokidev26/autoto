package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/codexauth"
	"autoto/internal/config"
	"autoto/internal/network"
	"autoto/internal/providers"
	"autoto/internal/secrets"
)

type providerRequestHeaderInput struct {
	Name         string `json:"name"`
	Value        string `json:"value"`
	KeepExisting bool   `json:"keepExisting,omitempty"`
}

type providerProxyAuthPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type providerConfigUpdateRequest struct {
	Name                  string                        `json:"name"`
	Type                  string                        `json:"type"`
	Profile               string                        `json:"profile"`
	BaseURL               string                        `json:"baseUrl"`
	APIKey                string                        `json:"apiKey"`
	ClearAPIKey           bool                          `json:"clearApiKey"`
	CreateOnly            bool                          `json:"createOnly,omitempty"`
	OriginalName          string                        `json:"originalName,omitempty"`
	Model                 string                        `json:"model"`
	Models                *[]config.ProviderModelConfig `json:"models,omitempty"`
	MaxTokens             int64                         `json:"maxTokens"`
	APIKeyOptional        bool                          `json:"apiKeyOptional"`
	GatewayEnabled        *bool                         `json:"gatewayEnabled"`
	ProxyURL              *string                       `json:"proxyUrl,omitempty"`
	ClearProxyAuth        bool                          `json:"clearProxyAuth,omitempty"`
	UserAgent             *string                       `json:"userAgent,omitempty"`
	ClientIdentity        *string                       `json:"clientIdentity,omitempty"`
	RequestHeaders        *[]providerRequestHeaderInput `json:"requestHeaders,omitempty"`
	InsecureSkipTLSVerify *bool                         `json:"insecureSkipTLSVerify,omitempty"`
	AllowPlaintextHTTP    *bool                         `json:"allowPlaintextHTTP,omitempty"`
}

type providerConfigUpdateResponse struct {
	Provider        settingsProviderResponse `json:"provider"`
	Persisted       bool                     `json:"persisted"`
	APIKeyPersisted bool                     `json:"apiKeyPersisted"`
	Message         string                   `json:"message"`
}

type providerConfigPatchRequest struct {
	Enabled        *bool   `json:"enabled"`
	Model          *string `json:"model"`
	GatewayEnabled *bool   `json:"gatewayEnabled"`
}

type providerDeleteResponse struct {
	Deleted   bool   `json:"deleted"`
	Name      string `json:"name"`
	Persisted bool   `json:"persisted"`
}

type providerTestResponse struct {
	Reachable  bool     `json:"reachable"`
	Configured bool     `json:"configured"`
	ModelCount int      `json:"modelCount"`
	Models     []string `json:"models,omitempty"`
	ErrorCode  string   `json:"errorCode,omitempty"`
	Message    string   `json:"message"`
}

type providerMessageTestRequest struct {
	Name                  string                        `json:"name"`
	Type                  string                        `json:"type"`
	Profile               string                        `json:"profile"`
	BaseURL               string                        `json:"baseUrl"`
	APIKey                string                        `json:"apiKey"`
	ClearAPIKey           bool                          `json:"clearApiKey"`
	CreateOnly            bool                          `json:"createOnly,omitempty"`
	OriginalName          string                        `json:"originalName,omitempty"`
	Model                 string                        `json:"model"`
	Models                *[]config.ProviderModelConfig `json:"models,omitempty"`
	MaxTokens             int64                         `json:"maxTokens"`
	APIKeyOptional        bool                          `json:"apiKeyOptional"`
	ProxyURL              *string                       `json:"proxyUrl,omitempty"`
	ClearProxyAuth        bool                          `json:"clearProxyAuth,omitempty"`
	UserAgent             *string                       `json:"userAgent,omitempty"`
	ClientIdentity        *string                       `json:"clientIdentity,omitempty"`
	RequestHeaders        *[]providerRequestHeaderInput `json:"requestHeaders,omitempty"`
	InsecureSkipTLSVerify *bool                         `json:"insecureSkipTLSVerify,omitempty"`
	AllowPlaintextHTTP    *bool                         `json:"allowPlaintextHTTP,omitempty"`
	Prompt                string                        `json:"prompt"`
}

type providerMessageTestResponse struct {
	Success   bool             `json:"success"`
	Model     string           `json:"model,omitempty"`
	Output    string           `json:"output,omitempty"`
	Usage     *providers.Usage `json:"usage,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
	ErrorCode string           `json:"errorCode,omitempty"`
	Message   string           `json:"message"`
}

func (r providerMessageTestRequest) configUpdateRequest() providerConfigUpdateRequest {
	return providerConfigUpdateRequest{
		Name:                  r.Name,
		Type:                  r.Type,
		Profile:               r.Profile,
		BaseURL:               r.BaseURL,
		APIKey:                r.APIKey,
		ClearAPIKey:           r.ClearAPIKey,
		CreateOnly:            r.CreateOnly,
		OriginalName:          r.OriginalName,
		Model:                 r.Model,
		Models:                r.Models,
		MaxTokens:             r.MaxTokens,
		APIKeyOptional:        r.APIKeyOptional,
		ProxyURL:              r.ProxyURL,
		ClearProxyAuth:        r.ClearProxyAuth,
		UserAgent:             r.UserAgent,
		ClientIdentity:        r.ClientIdentity,
		RequestHeaders:        r.RequestHeaders,
		InsecureSkipTLSVerify: r.InsecureSkipTLSVerify,
		AllowPlaintextHTTP:    r.AllowPlaintextHTTP,
	}
}

const (
	providerTestTimeout                = 3 * time.Second
	providerMessageTestTimeout         = 30 * time.Second
	providerMessageTestMaxOutputBytes  = 64 << 10
	providerMessageTestMaxPromptBytes  = 8 << 10
	providerMessageTestMaxTokens       = 512
	maxProviderConfigRequestSize       = 128 << 10
	maxProviderBaseURLBytes            = 2048
	maxProviderAPIKeyBytes             = 16 << 10
	maxProviderModelBytes              = 512
	maxProviderModels                  = 256
	maxProviderProfileBytes            = 64
	maxProviderProxyURLBytes           = 2048
	maxProviderUserAgentBytes          = 512
	maxProviderRequestHeaders          = 32
	maxProviderRequestHeaderNameBytes  = 128
	maxProviderRequestHeaderValueBytes = 8 << 10
	maxProviderRequestHeaderTotalBytes = 64 << 10
)

func providerGatewaySharingForbidden(providerType, profile string) bool {
	// Codex OAuth may now be shared via the private gateway (user opt-in). Only
	// CLI-proxy OAuth profiles remain forbidden.
	_ = providerType
	return strings.EqualFold(strings.TrimSpace(profile), config.ProviderProfileCLIProxyAPI)
}

func (s *Server) updateProviderConfig(w http.ResponseWriter, r *http.Request) {
	providerName := strings.TrimSpace(chi.URLParam(r, "name"))
	if providerName == "" {
		writeError(w, http.StatusBadRequest, "provider name is required")
		return
	}
	if err := validateProviderName(providerName); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	var req providerConfigUpdateRequest
	if err := decodeProviderConfigUpdateRequest(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	// Keep the full config read-modify-save-publish transaction serialized with
	// security and continuation updates. The global config lock is always
	// acquired before the narrower provider runtime lock.
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	resp, err := s.providerConfigs().update(r.Context(), providerName, req)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) patchProviderConfig(w http.ResponseWriter, r *http.Request) {
	providerName := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := validateProviderName(providerName); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	var req providerConfigPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.Enabled == nil && req.Model == nil && req.GatewayEnabled == nil {
		writeError(w, http.StatusBadRequest, "enabled, model, or gatewayEnabled is required")
		return
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	resp, err := s.providerConfigs().patch(r.Context(), providerName, req)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) deleteProviderConfig(w http.ResponseWriter, r *http.Request) {
	providerName := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := validateProviderName(providerName); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	resp, err := s.providerConfigs().delete(r.Context(), providerName)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) testProviderConfig(w http.ResponseWriter, r *http.Request) {
	if err := rejectProviderTestBody(r); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	providerName := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := validateProviderName(providerName); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	provider, ok := s.providerConfig(providerName)
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeJSON(w, http.StatusOK, s.providerConfigs().testAdapter(r.Context(), provider))
}

// testProviderConfigDraft validates and tests a full configuration draft without
// writing it to disk or changing the runtime registry. Blank keys may reuse only
// the explicitly identified original provider and never cross endpoint bindings.
var errProviderDraftNameConflict = errors.New("Provider 名稱已存在")

func (s *Server) providerConfigForDraftTest(ctx context.Context, providerName string, req providerConfigUpdateRequest) (config.ProviderConfig, error) {
	return s.providerConfigs().configForDraftTest(ctx, providerName, req)
}

func (s *Server) testProviderConfigDraft(w http.ResponseWriter, r *http.Request) {
	var req providerConfigUpdateRequest
	if err := decodeProviderConfigUpdateRequest(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ClearAPIKey {
		writeError(w, http.StatusBadRequest, "provider test does not support clearing API keys")
		return
	}
	providerName := strings.TrimSpace(req.Name)
	if err := validateProviderName(providerName); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	s.providerMutationMu.Lock()
	provider, err := s.providerConfigForDraftTest(r.Context(), providerName, req)
	s.providerMutationMu.Unlock()
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errProviderDraftNameConflict) {
			status = http.StatusConflict
		}
		s.writeRequestError(w, r, status, err)
		return
	}
	if _, err := s.newRuntimeProvider(provider); err != nil {
		writeError(w, http.StatusBadRequest, describeProviderConfigError(provider, err))
		return
	}
	writeJSON(w, http.StatusOK, s.providerConfigs().testAdapter(r.Context(), provider))
}

// testProviderMessageDraft sends one tool-free prompt through a temporary
// provider adapter. It never persists the draft or changes the runtime registry.
func (s *Server) testProviderMessageDraft(w http.ResponseWriter, r *http.Request) {
	var req providerMessageTestRequest
	if err := decodeProviderJSONRequest(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := validateProviderConfigRequest(req.configUpdateRequest()); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.ClearAPIKey {
		writeError(w, http.StatusBadRequest, "provider message test does not support clearing API keys")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if err := validateAPIText("prompt", prompt, providerMessageTestMaxPromptBytes, true, true); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	providerName := strings.TrimSpace(req.Name)
	if err := validateProviderName(providerName); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	s.providerMutationMu.Lock()
	provider, err := s.providerConfigForDraftTest(r.Context(), providerName, req.configUpdateRequest())
	s.providerMutationMu.Unlock()
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errProviderDraftNameConflict) {
			status = http.StatusConflict
		}
		s.writeRequestError(w, r, status, err)
		return
	}
	adapter, early, done := s.providerConfigs().messageTestAdapter(provider)
	if done {
		writeJSON(w, http.StatusOK, early)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), providerMessageTestTimeout)
	defer cancel()
	events, err := adapter.Generate(ctx, providers.GenerateRequest{
		Model:           provider.Model,
		Messages:        []providers.Message{{Role: "user", Content: prompt}},
		MaxOutputTokens: providerMessageTestMaxTokens,
		Scenario:        providers.CallScenarioInternal,
	})
	if err != nil {
		errorCode, message := classifyProviderMessageTestError(err)
		writeJSON(w, http.StatusOK, providerMessageTestResponse{Model: provider.Model, ErrorCode: errorCode, Message: message})
		return
	}

	var output strings.Builder
	var usage *providers.Usage
	truncated := false
	for {
		select {
		case <-ctx.Done():
			errorCode, message := classifyProviderMessageTestError(ctx.Err())
			writeJSON(w, http.StatusOK, providerMessageTestResponse{Model: provider.Model, ErrorCode: errorCode, Message: message})
			return
		case event, ok := <-events:
			if !ok {
				text := strings.TrimSpace(output.String())
				if text == "" {
					writeJSON(w, http.StatusOK, providerMessageTestResponse{Model: provider.Model, Usage: usage, ErrorCode: "empty_response", Message: "模型沒有返回文字。"})
					return
				}
				writeJSON(w, http.StatusOK, providerMessageTestResponse{Success: true, Model: provider.Model, Output: text, Usage: usage, Truncated: truncated, Message: "測試訊息傳送成功。"})
				return
			}
			if event.Usage != nil {
				copy := *event.Usage
				usage = &copy
			}
			switch event.Type {
			case "error":
				errorCode, message := classifyProviderMessageTestError(errors.New(event.Text))
				writeJSON(w, http.StatusOK, providerMessageTestResponse{Model: provider.Model, Usage: usage, ErrorCode: errorCode, Message: message})
				return
			case "text":
				if appendProviderMessageTestOutput(&output, event.Text, providerMessageTestMaxOutputBytes) {
					truncated = true
				}
			}
		}
	}
}

func (s *Server) testProviderAdapter(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig) {
	writeJSON(w, http.StatusOK, s.providerConfigs().testAdapter(r.Context(), provider))
}

func rejectProviderTestBody(r *http.Request) error {
	if r.ContentLength > 0 {
		return errors.New("provider test does not accept a request body")
	}
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		return errors.New("provider test request body is invalid")
	}
	if len(body) != 0 {
		return errors.New("provider test does not accept a request body")
	}
	return nil
}

func (s *Server) providerConfig(name string) (config.ProviderConfig, bool) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	for _, provider := range s.cfg.Providers.Instances {
		if provider.Name == name {
			return config.NormalizeProviderConfig(provider), true
		}
	}
	return config.ProviderConfig{}, false
}

func (s *Server) providerModels(name string) []config.ProviderModelConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	for _, provider := range s.cfg.Providers.Instances {
		if provider.Name == name {
			return config.NormalizeProviderModels(provider.Models, "")
		}
	}
	return nil
}

func decodeProviderJSONRequest(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxProviderConfigRequestSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func decodeProviderConfigUpdateRequest(r *http.Request, dst *providerConfigUpdateRequest) error {
	if err := decodeProviderJSONRequest(r, dst); err != nil {
		return err
	}
	return validateProviderConfigRequest(*dst)
}

func validateProviderConfigRequest(req providerConfigUpdateRequest) error {
	if req.ClearAPIKey && strings.TrimSpace(req.APIKey) != "" {
		return errors.New("apiKey and clearApiKey cannot be used together")
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{name: "name", value: req.Name, limit: 64},
		{name: "type", value: req.Type, limit: 64},
		{name: "profile", value: req.Profile, limit: maxProviderProfileBytes},
		{name: "baseUrl", value: req.BaseURL, limit: maxProviderBaseURLBytes},
		{name: "apiKey", value: req.APIKey, limit: maxProviderAPIKeyBytes},
		{name: "model", value: req.Model, limit: maxProviderModelBytes},
	} {
		if len(field.value) > field.limit {
			return fmt.Errorf("%s exceeds size limit", field.name)
		}
		if strings.ContainsAny(field.value, "\x00\r\n") {
			return fmt.Errorf("%s contains invalid control characters", field.name)
		}
	}
	if req.Models != nil {
		if len(*req.Models) > maxProviderModels {
			return fmt.Errorf("models exceeds %d entries", maxProviderModels)
		}
		seen := make(map[string]struct{}, len(*req.Models))
		for _, model := range *req.Models {
			name := strings.TrimSpace(model.Name)
			if name == "" {
				return errors.New("model name is required")
			}
			if len(name) > maxProviderModelBytes {
				return errors.New("model name exceeds size limit")
			}
			for _, r := range name {
				if unicode.IsControl(r) {
					return errors.New("model name contains invalid control characters")
				}
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("model %q is duplicated", name)
			}
			seen[name] = struct{}{}
			if model.ContextTokenLimit < 0 || model.ContextTokenLimit > config.ProviderModelContextTokenLimitMax {
				return fmt.Errorf("model %q contextTokenLimit must be between 0 and %d", name, config.ProviderModelContextTokenLimitMax)
			}
		}
	}
	if req.ProxyURL != nil {
		if len(*req.ProxyURL) > maxProviderProxyURLBytes || strings.ContainsAny(*req.ProxyURL, "\x00\r\n") {
			return errors.New("proxyUrl is invalid")
		}
	}
	if req.UserAgent != nil {
		if len(*req.UserAgent) > maxProviderUserAgentBytes || strings.ContainsAny(*req.UserAgent, "\x00\r\n") {
			return errors.New("userAgent is invalid")
		}
	}
	if req.ClientIdentity != nil {
		if _, err := config.ParseClientIdentity(*req.ClientIdentity); err != nil {
			return err
		}
	}
	if req.RequestHeaders != nil {
		if len(*req.RequestHeaders) > maxProviderRequestHeaders {
			return fmt.Errorf("requestHeaders exceeds %d entries", maxProviderRequestHeaders)
		}
		seen := make(map[string]struct{}, len(*req.RequestHeaders))
		total := 0
		for _, header := range *req.RequestHeaders {
			name := strings.TrimSpace(header.Name)
			value := header.Value
			if len(name) == 0 || len(name) > maxProviderRequestHeaderNameBytes || !validProviderHeaderName(name) {
				return errors.New("request header name is invalid")
			}
			canonical := http.CanonicalHeaderKey(name)
			key := strings.ToLower(canonical)
			if providerHeaderForbidden(key) {
				return fmt.Errorf("request header %q is reserved", canonical)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("request header %q is duplicated", canonical)
			}
			seen[key] = struct{}{}
			if len(value) > maxProviderRequestHeaderValueBytes || strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("request header %q value is invalid", canonical)
			}
			if value == "" && !header.KeepExisting {
				return fmt.Errorf("request header %q value is required", canonical)
			}
			total += len(name) + len(value)
		}
		if total > maxProviderRequestHeaderTotalBytes {
			return errors.New("requestHeaders exceeds total size limit")
		}
	}
	if req.MaxTokens < 0 || req.MaxTokens > 10_000_000 {
		return errors.New("maxTokens must be between 0 and 10000000")
	}
	return nil
}

func validProviderHeaderName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func providerHeaderForbidden(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "host", "content-length", "transfer-encoding", "connection", "proxy-authorization", "user-agent", "te", "trailer", "upgrade", "keep-alive", "x-autoto-client", "x-autoto-installation-id":
		return true
	default:
		return false
	}
}

func (s *Server) runProviderMutationHook() {
	if s.providerMutationHook != nil {
		s.providerMutationHook()
	}
}

func providerTransportScopeChanged(current, next config.ProviderConfig) bool {
	return !strings.EqualFold(strings.TrimSpace(current.Type), strings.TrimSpace(next.Type)) ||
		!strings.EqualFold(strings.TrimSpace(current.Profile), strings.TrimSpace(next.Profile)) ||
		strings.TrimSpace(current.BaseURL) != strings.TrimSpace(next.BaseURL) ||
		strings.TrimSpace(current.ProxyURL) != strings.TrimSpace(next.ProxyURL) ||
		current.InsecureSkipTLSVerify != next.InsecureSkipTLSVerify ||
		current.AllowPlaintextHTTP != next.AllowPlaintextHTTP
}

// providerEndpointScopeChanged reports whether an edit moves the provider to a
// different network endpoint. It deliberately ignores Type and Profile: changing
// the protocol keeps talking to the same host over the same transport, so a key
// entered for that endpoint is still in scope. Anything that changes where bytes
// travel takes the key out of scope.
func providerEndpointScopeChanged(current, next config.ProviderConfig) bool {
	return strings.TrimSpace(current.BaseURL) != strings.TrimSpace(next.BaseURL) ||
		strings.TrimSpace(current.ProxyURL) != strings.TrimSpace(next.ProxyURL) ||
		current.InsecureSkipTLSVerify != next.InsecureSkipTLSVerify ||
		current.AllowPlaintextHTTP != next.AllowPlaintextHTTP
}

// storedProviderSecretMigratable reports whether a vault-stored API key may be
// re-encrypted under a changed binding. Only keys the user deliberately saved
// qualify: runtime and environment values stay scoped to where they came from
// and are never silently forwarded.
func storedProviderSecretMigratable(existed bool, existing, updated config.ProviderConfig) bool {
	return existed &&
		storedProviderSecretSource(existing.APIKeySource) &&
		!providerEndpointScopeChanged(existing, updated) &&
		providerSecretBindingChanged(existing, updated)
}

func providerProxySettings(existing config.ProviderConfig, req providerConfigUpdateRequest) (string, string, string, string, bool, error) {
	proxyURL := existing.ProxyURL
	username := existing.ProxyUsername
	password := existing.ProxyPassword
	source := existing.ProxyAuthSource
	authSupplied := false
	if req.ProxyURL != nil {
		raw := strings.TrimSpace(*req.ProxyURL)
		if raw == "" {
			return "", "", "", secrets.ProviderSecretSourceNone, false, nil
		}
		parsed, err := url.Parse(raw)
		if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
			return "", "", "", "", false, errors.New("代理位址無效")
		}
		if parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || (parsed.Path != "" && parsed.Path != "/") {
			return "", "", "", "", false, errors.New("代理位址不能包含路徑、查詢參數或片段")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return "", "", "", "", false, errors.New("代理協定僅支援 http、https、socks5 或 socks5h")
		}
		if parsed.User != nil {
			username = parsed.User.Username()
			password, _ = parsed.User.Password()
			authSupplied = username != "" || password != ""
			if authSupplied {
				source = secrets.ProviderSecretSourceRuntime
			}
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.User = nil
		parsed.Path = ""
		proxyURL = parsed.String()
		if !authSupplied && strings.TrimSpace(proxyURL) != strings.TrimSpace(existing.ProxyURL) {
			username = ""
			password = ""
			source = secrets.ProviderSecretSourceNone
		}
	}
	if req.ClearProxyAuth {
		username = ""
		password = ""
		source = secrets.ProviderSecretSourceNone
	}
	return proxyURL, username, password, source, authSupplied, nil
}

func providerHeadersFromRequest(existing config.ProviderConfig, inputs *[]providerRequestHeaderInput, allowKeepExisting bool) ([]config.ProviderRequestHeader, string, error) {
	if inputs == nil {
		return append([]config.ProviderRequestHeader(nil), existing.RequestHeaders...), existing.RequestHeadersSource, nil
	}
	existingValues := make(map[string]string, len(existing.RequestHeaders))
	for _, header := range existing.RequestHeaders {
		existingValues[strings.ToLower(http.CanonicalHeaderKey(strings.TrimSpace(header.Name)))] = header.Value
	}
	result := make([]config.ProviderRequestHeader, 0, len(*inputs))
	usedNewValue := false
	for _, input := range *inputs {
		name := http.CanonicalHeaderKey(strings.TrimSpace(input.Name))
		value := input.Value
		if value == "" && input.KeepExisting {
			if !allowKeepExisting {
				return nil, "", fmt.Errorf("安全邊界已變化，請重新輸入請求頭 %q 的值", name)
			}
			value = existingValues[strings.ToLower(name)]
			if value == "" {
				return nil, "", fmt.Errorf("無法保留請求頭 %q，請重新輸入值", name)
			}
		} else if value != "" {
			usedNewValue = true
		}
		result = append(result, config.ProviderRequestHeader{Name: name, Value: value})
	}
	if len(result) == 0 {
		return nil, secrets.ProviderSecretSourceNone, nil
	}
	if usedNewValue {
		return result, secrets.ProviderSecretSourceRuntime, nil
	}
	return result, existing.RequestHeadersSource, nil
}

func providerConfigFromUpdateRequest(providerName string, existing config.ProviderConfig, req providerConfigUpdateRequest) (config.ProviderConfig, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = providerName
	}
	if err := validateProviderName(name); err != nil {
		return config.ProviderConfig{}, err
	}
	providerType := strings.TrimSpace(req.Type)
	if providerType == "" && existing.Name != "" {
		providerType = existing.Type
	}
	if providerType == "" {
		providerType = "openai-compatible"
	}
	switch providerType {
	case "openai-compatible", "anthropic", "openai", config.ProviderTypeGeminiInteractions, config.ProviderTypeGemini, config.ProviderTypeGrok, config.ProviderTypeKimi, config.ProviderTypeCodex:
	default:
		return config.ProviderConfig{}, errors.New("API 協定目前僅支援 codex、openai-compatible、anthropic、openai、gemini-interactions、gemini、grok 或 kimi")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if providerType == "openai-compatible" && baseURL == "" {
		return config.ProviderConfig{}, errors.New("中轉站 Base URL 不能為空")
	}
	switch providerType {
	case config.ProviderTypeGeminiInteractions:
		if baseURL == "" {
			baseURL = "https://generativelanguage.googleapis.com/v1beta/interactions"
		}
	case config.ProviderTypeGemini:
		if baseURL == "" {
			baseURL = "https://cloudcode-pa.googleapis.com"
		}
	case config.ProviderTypeGrok:
		if baseURL == "" {
			baseURL = "https://cli-chat-proxy.grok.com/v1"
		}
	case config.ProviderTypeKimi:
		if baseURL == "" {
			baseURL = "https://api.kimi.com/coding"
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" && existing.Name != "" {
		model = existing.Model
	}
	if model == "" {
		switch providerType {
		case config.ProviderTypeCodex:
			model = codexauth.DefaultModel
		case "anthropic":
			model = "claude-sonnet-4-5"
		case config.ProviderTypeGeminiInteractions:
			model = "gemini-2.5-pro"
		case config.ProviderTypeGemini:
			model = "gemini-3-flash"
		case config.ProviderTypeGrok:
			model = "grok-4.5"
		case config.ProviderTypeKimi:
			model = "kimi-k2.7-code"
		default:
			model = "gpt-4.1-mini"
		}
	}
	models := append([]config.ProviderModelConfig(nil), existing.Models...)
	// A present but empty list is treated as "unchanged" rather than "delete
	// every model". Per-model context limits only exist because the user typed
	// them, and the console always sends this field: any client path that
	// reaches save before its model list is populated would otherwise erase
	// them silently. There is no legitimate empty state to express either,
	// since NormalizeProviderModels always re-adds the default model.
	if req.Models != nil && len(*req.Models) > 0 {
		models = append([]config.ProviderModelConfig(nil), (*req.Models)...)
	}
	models = config.NormalizeProviderModels(models, model)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 && existing.Name != "" {
		maxTokens = existing.MaxTokens
	}
	if providerType == "anthropic" && maxTokens <= 0 {
		maxTokens = 4096
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && existing.Name != "" {
		apiKey = existing.APIKey
	}
	if providerType == config.ProviderTypeGemini || providerType == config.ProviderTypeGrok || providerType == config.ProviderTypeKimi {
		if strings.TrimSpace(req.APIKey) != "" {
			return config.ProviderConfig{}, errors.New("原生 OAuth Provider 不接受 API Key")
		}
		apiKey = ""
	}
	profile := strings.TrimSpace(req.Profile)
	if profile == "" && existing.Name != "" {
		profile = existing.Profile
	}
	if err := validateProviderProfile(profile); err != nil {
		return config.ProviderConfig{}, err
	}
	apiKeyOptional := req.APIKeyOptional
	if profile == config.ProviderProfileCLIProxyAPI {
		apiKeyOptional = true
	}
	if providerType == config.ProviderTypeGemini || providerType == config.ProviderTypeGrok || providerType == config.ProviderTypeKimi {
		apiKeyOptional = false
	}
	gatewayEnabled := existing.GatewayEnabled
	if req.GatewayEnabled != nil {
		gatewayEnabled = *req.GatewayEnabled
	}
	if gatewayEnabled && providerGatewaySharingForbidden(providerType, profile) {
		return config.ProviderConfig{}, errors.New("OAuth-backed providers cannot be enabled for the shared API gateway")
	}
	proxyURL, proxyUsername, proxyPassword, proxyAuthSource, proxyAuthSupplied, err := providerProxySettings(existing, req)
	if err != nil {
		return config.ProviderConfig{}, err
	}
	userAgent := existing.UserAgent
	if req.UserAgent != nil {
		userAgent = strings.TrimSpace(*req.UserAgent)
	}
	clientIdentity := existing.ClientIdentity
	if req.ClientIdentity != nil {
		parsed, err := config.ParseClientIdentity(*req.ClientIdentity)
		if err != nil {
			return config.ProviderConfig{}, err
		}
		clientIdentity = parsed
	}
	insecureSkipTLSVerify := existing.InsecureSkipTLSVerify
	if req.InsecureSkipTLSVerify != nil {
		insecureSkipTLSVerify = *req.InsecureSkipTLSVerify
	}
	allowPlaintextHTTP := existing.AllowPlaintextHTTP
	if req.AllowPlaintextHTTP != nil {
		allowPlaintextHTTP = *req.AllowPlaintextHTTP
	}
	updated := config.ProviderConfig{
		Name:                    name,
		Type:                    providerType,
		Profile:                 profile,
		BaseURL:                 baseURL,
		APIKey:                  apiKey,
		Model:                   model,
		Models:                  models,
		MaxTokens:               maxTokens,
		APIKeyOptional:          apiKeyOptional,
		ImageInput:              existing.ImageInput,
		GatewayEnabled:          gatewayEnabled,
		ProxyURL:                proxyURL,
		ProxyUsername:           proxyUsername,
		ProxyPassword:           proxyPassword,
		ProxyAuthSource:         proxyAuthSource,
		UserAgent:               userAgent,
		ClientIdentity:          clientIdentity,
		InsecureSkipTLSVerify:   insecureSkipTLSVerify,
		AllowPlaintextHTTP:      allowPlaintextHTTP,
		Disabled:                existing.Disabled,
		SecretRevision:          existing.SecretRevision,
		TransportSecretRevision: existing.TransportSecretRevision,
		APIKeySource:            existing.APIKeySource,
	}
	scopeChanged := existing.Name != "" && providerTransportScopeChanged(existing, updated)
	if scopeChanged && !proxyAuthSupplied {
		updated.ProxyUsername = ""
		updated.ProxyPassword = ""
		updated.ProxyAuthSource = secrets.ProviderSecretSourceNone
	}
	requestHeaders, requestHeadersSource, err := providerHeadersFromRequest(existing, req.RequestHeaders, !scopeChanged)
	if err != nil {
		return config.ProviderConfig{}, err
	}
	if scopeChanged && req.RequestHeaders == nil {
		requestHeaders = nil
		requestHeadersSource = secrets.ProviderSecretSourceNone
	}
	updated.RequestHeaders = requestHeaders
	updated.RequestHeadersSource = requestHeadersSource
	return updated, nil
}

func providerProxyAuthConfigured(provider config.ProviderConfig) bool {
	return strings.TrimSpace(provider.ProxyUsername) != "" || provider.ProxyPassword != ""
}

func providerHeadersConfigured(provider config.ProviderConfig) bool {
	if len(provider.RequestHeaders) == 0 {
		return false
	}
	for _, header := range provider.RequestHeaders {
		if strings.TrimSpace(header.Name) == "" || header.Value == "" {
			return false
		}
	}
	return true
}

func providerRequestHeadersEqual(left, right []config.ProviderRequestHeader) bool {
	if len(left) != len(right) {
		return false
	}
	leftValues := make(map[string]string, len(left))
	for _, header := range left {
		leftValues[strings.ToLower(http.CanonicalHeaderKey(strings.TrimSpace(header.Name)))] = header.Value
	}
	for _, header := range right {
		if leftValues[strings.ToLower(http.CanonicalHeaderKey(strings.TrimSpace(header.Name)))] != header.Value {
			return false
		}
	}
	return true
}

func providerTransportSecretMutationRequired(current, next config.ProviderConfig) bool {
	currentProxy := providerProxyAuthConfigured(current)
	nextProxy := providerProxyAuthConfigured(next)
	currentHeaders := providerHeadersConfigured(current) || len(current.RequestHeaders) > 0
	nextHeaders := providerHeadersConfigured(next) || len(next.RequestHeaders) > 0
	bindingChanged := providerSecretBindingChanged(current, next)
	if bindingChanged && (currentProxy || nextProxy || currentHeaders || nextHeaders || storedProviderSecretSource(current.ProxyAuthSource) || storedProviderSecretSource(current.RequestHeadersSource)) {
		return true
	}
	return current.ProxyUsername != next.ProxyUsername ||
		current.ProxyPassword != next.ProxyPassword ||
		currentProxy != nextProxy ||
		!providerRequestHeadersEqual(current.RequestHeaders, next.RequestHeaders)
}

func providerProxyAuthSecret(provider config.ProviderConfig) (string, bool, error) {
	if !providerProxyAuthConfigured(provider) {
		return "", false, nil
	}
	encoded, err := json.Marshal(providerProxyAuthPayload{Username: provider.ProxyUsername, Password: provider.ProxyPassword})
	if err != nil {
		return "", false, err
	}
	return string(encoded), true, nil
}

func providerRequestHeadersSecret(provider config.ProviderConfig) (string, bool, error) {
	if len(provider.RequestHeaders) == 0 {
		return "", false, nil
	}
	values := make(map[string]string, len(provider.RequestHeaders))
	for _, header := range provider.RequestHeaders {
		name := http.CanonicalHeaderKey(strings.TrimSpace(header.Name))
		if name == "" || header.Value == "" {
			return "", false, fmt.Errorf("request header %q is unavailable", name)
		}
		values[name] = header.Value
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", false, err
	}
	return string(encoded), true, nil
}

func (s *Server) prepareProviderTransportSecrets(ctx context.Context, provider config.ProviderConfig) ([]string, error) {
	if s.providerVault == nil {
		return nil, nil
	}
	binding := serverProviderSecretBinding(provider)
	prepared := make([]string, 0, 2)
	prepare := func(kind, value string, configured bool) error {
		var err error
		if configured {
			_, err = s.providerVault.PrepareSetKind(ctx, binding, kind, value, "")
		} else {
			err = s.providerVault.PrepareClearKind(ctx, binding, kind)
		}
		if err == nil {
			prepared = append(prepared, kind)
		}
		return err
	}
	proxySecret, proxyConfigured, err := providerProxyAuthSecret(provider)
	if err != nil {
		return nil, err
	}
	if err := prepare(secrets.ProviderProxyAuthKind, proxySecret, proxyConfigured); err != nil {
		return prepared, err
	}
	headerSecret, headersConfigured, err := providerRequestHeadersSecret(provider)
	if err != nil {
		return prepared, err
	}
	if err := prepare(secrets.ProviderRequestHeadersKind, headerSecret, headersConfigured); err != nil {
		return prepared, err
	}
	return prepared, nil
}

func (s *Server) rollbackProviderSecretKinds(ctx context.Context, providerName string, kinds []string) {
	if s.providerVault == nil {
		return
	}
	for _, kind := range kinds {
		_ = s.providerVault.RollbackPendingKind(ctx, providerName, kind)
	}
}

func (s *Server) commitProviderSecretKinds(ctx context.Context, providerName string, kinds []string) error {
	if s.providerVault == nil {
		return nil
	}
	for _, kind := range kinds {
		if err := s.providerVault.CommitPendingKind(ctx, providerName, kind); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("provider name is required")
	}
	if len(name) > 64 {
		return errors.New("Provider 名稱最多 64 個字元")
	}
	for i, r := range name {
		if i == 0 && !isProviderNameAlphaNumeric(r) {
			return errors.New("Provider 名稱必須以英文字母或數字開頭，且只能包含英文字母、數字、點、底線和連字號")
		}
		if !isProviderNameChar(r) {
			return errors.New("Provider 名稱只能包含英文字母、數字、點、底線和連字號")
		}
	}
	return nil
}

func validateProviderProfile(profile string) error {
	switch strings.TrimSpace(profile) {
	case "", config.ProviderProfileCLIProxyAPI:
		return nil
	default:
		return errors.New("unsupported provider profile")
	}
}

func isProviderNameAlphaNumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func isProviderNameChar(r rune) bool {
	return isProviderNameAlphaNumeric(r) || r == '.' || r == '_' || r == '-'
}

func (s *Server) newRuntimeProvider(provider config.ProviderConfig) (providers.Provider, error) {
	provider.ClientVersion = providers.ClientVersionFromBuildStamp(config.Version)
	// ApplyCredentialStorePath is the single source of truth for where each
	// provider's account store lives. The three separate assignments that used
	// to be here (Codex, Anthropic, Gemini/Grok/Kimi) drifted from that
	// function and were missing Kiro, which made Kiro accounts invisible after
	// every restart until the provider was re-saved.
	provider = providers.ApplyCredentialStorePath(provider, s.configSnapshot().Paths.HomeDir)
	if s.store != nil {
		if settings, err := s.store.GetRuntimeSettings(context.Background()); err == nil {
			provider.InstallationID = settings.InstallationID
		}
	}
	adapter, err := providers.NewProvider(provider)
	if err != nil {
		return nil, err
	}
	if accountProvider, ok := adapter.(interface {
		SetAccountTelemetry(providers.AccountTelemetry)
		SetGatewayAccountPolicy(providers.GatewayAccountPolicy)
	}); ok && s.store != nil {
		accountProvider.SetAccountTelemetry(s.store)
		accountProvider.SetGatewayAccountPolicy(s.store)
	}
	return adapter, nil
}

func (s *Server) registerProvider(provider config.ProviderConfig) error {
	if provider.Disabled {
		s.unregisterProvider(provider.Name)
		return nil
	}
	adapter, err := s.newRuntimeProvider(provider)
	if err != nil {
		return err
	}
	s.registerProviderAdapter(adapter)
	return nil
}

func (s *Server) registerProviderAdapter(adapter providers.Provider) {
	if adapter == nil {
		return
	}
	if s.providers == nil {
		s.providers = providers.NewRegistry()
	}
	s.providers.Register(adapter)
}

func (s *Server) unregisterProvider(name string) {
	if s.providers != nil {
		s.providers.Unregister(name)
	}
}

func (s *Server) refreshProviderDefault(cfg config.Config) {
	if s.providers == nil {
		return
	}
	// A server constructed before all configured adapters were registered can
	// still safely switch away from a disabled/deleted default. Register only
	// enabled, validated configs; disabled entries remain unresolvable.
	for _, provider := range cfg.Providers.Instances {
		if provider.Disabled {
			s.providers.Unregister(provider.Name)
			continue
		}
		if _, ok := s.providers.Get(provider.Name); ok {
			continue
		}
		if adapter, err := s.newRuntimeProvider(provider); err == nil {
			s.providers.Register(adapter)
		}
	}
	s.providers.SetDefaultFromConfig(cfg.Agent.DefaultModel, cfg.Providers.Instances)
}

func (s *Server) ensureProviderDefaultAfterMutation(next config.Config, affectedName string) error {
	currentDefault := ""
	if s.providers != nil {
		if provider, err := s.providers.Default(); err == nil {
			currentDefault = provider.Name()
		}
	}
	configuredDefault, _ := providers.SplitModel(next.Agent.DefaultModel)
	if currentDefault != affectedName && configuredDefault != affectedName {
		return nil
	}
	for _, provider := range next.Providers.Instances {
		if provider.Disabled {
			continue
		}
		adapter, err := s.newRuntimeProvider(provider)
		if err != nil {
			continue
		}
		if providers.ConfiguredFor(adapter, provider.IsConfigured()) {
			return nil
		}
	}
	return errors.New("不能停用或刪除目前預設 Provider：沒有可用且已設定的回退 Provider")
}

func (s *Server) persistProviderConfig(configPath string, cfg config.Config) (bool, error) {
	if strings.TrimSpace(configPath) == "" {
		return false, nil
	}
	if err := config.Save(configPath, cfg); err != nil {
		return false, err
	}
	return true, nil
}

func distinctModelCount(models []string) int {
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" {
			seen[model] = struct{}{}
		}
	}
	return len(seen)
}

// describeProviderConfigError explains why building a runtime adapter failed.
// The three call sites used to collapse every cause into "Provider 設定無效。",
// which gave no way to tell a denied base URL apart from a malformed header, so
// a plain-HTTP relay looked like an unexplained rejection.
//
// The underlying error is logged rather than returned: the network package keeps
// its errors free of URL, hostname and resolver detail on purpose, and that
// property must survive being surfaced in the UI.
func describeProviderConfigError(provider config.ProviderConfig, err error) string {
	if err == nil {
		return "Provider 設定無效。"
	}
	slog.Debug("provider runtime configuration rejected", "provider", provider.Name, "error", err.Error())
	if errors.Is(err, network.ErrDestinationDenied) && isPlaintextHTTPBaseURL(provider.BaseURL) && !provider.AllowPlaintextHTTP {
		return "明文 HTTP 只允許連線本機。請改用 https://，或為該 Provider 單獨開啟「允許明文 HTTP」（開啟後 API Key 與請求內容會明文傳輸）。"
	}
	if errors.Is(err, network.ErrDestinationDenied) {
		return "該位址被網路策略拒絕。"
	}
	if errors.Is(err, network.ErrInvalidURL) {
		return "Base URL 格式無效。"
	}
	if errors.Is(err, network.ErrNameResolution) {
		return "無法解析該位址的主機名稱。"
	}
	if errors.Is(err, network.ErrProxyConfiguration) {
		return "代理設定無效。"
	}
	return "Provider 設定無效。"
}

// isPlaintextHTTPBaseURL reports whether a base URL uses the http scheme, so the
// guidance above is only shown when it actually applies.
func isPlaintextHTTPBaseURL(raw string) bool {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.EqualFold(target.Scheme, "http")
}

func classifyProviderTestError(err error) (errorCode, message string, reachable bool) {
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded") {
		return "timeout", "連線 Provider 逾時。", false
	}
	messageText := strings.ToLower(err.Error())
	switch {
	case strings.Contains(messageText, "401"), strings.Contains(messageText, "403"), strings.Contains(messageText, "unauthorized"), strings.Contains(messageText, "forbidden"):
		return "authentication_failed", "Provider 拒絕了憑證。", true
	case strings.Contains(messageText, "not configured"), strings.Contains(messageText, "没有可用"), strings.Contains(messageText, "credential"), strings.Contains(messageText, "api key is required"):
		return "not_configured", "Provider 憑證尚未設定。", false
	case strings.Contains(messageText, "connection refused"), strings.Contains(messageText, "no such host"), strings.Contains(messageText, "network is unreachable"), strings.Contains(messageText, "connect:"):
		return "unreachable", "無法連線 Provider。", false
	// A models catalog is optional. The Messages API does not require one, and
	// third-party Anthropic-compatible endpoints commonly serve /v1/messages
	// without /v1/models -- DeepSeek returns 404 there while generating
	// perfectly well. Reporting that as a failed connection told the operator
	// the provider was unreachable when only its catalog was, and the draft
	// could not be saved even though the manual model entry below it exists for
	// exactly this case.
	case strings.Contains(messageText, "404"), strings.Contains(messageText, "not found"):
		return "catalog_unavailable", "Provider 可連線，但沒有模型目錄，請手動填寫模型名稱。", true
	default:
		return "request_failed", "Provider 測試失敗。", false
	}
}

// classifyProviderMessageTestError maps an upstream failure onto a stable code
// and a user-facing sentence. Both deliberately drop the underlying error so a
// provider cannot leak endpoint or credential detail into the UI, which left
// "模型測試失敗。" with no way to find out why; the original text is logged at
// debug level instead so operators can diagnose it locally.
func classifyProviderMessageTestError(err error) (errorCode, message string) {
	errorCode, _, _ = classifyProviderTestError(err)
	if err != nil {
		slog.Debug("provider message test failed", "errorCode", errorCode, "error", err.Error())
	}
	switch errorCode {
	case "timeout":
		return errorCode, "模型回應逾時。"
	case "authentication_failed":
		return errorCode, "Provider 拒絕了憑證，測試訊息未傳送。"
	case "not_configured":
		return errorCode, "Provider 憑證尚未設定，測試訊息未傳送。"
	case "unreachable":
		return errorCode, "無法連線 Provider，測試訊息未傳送。"
	default:
		return "request_failed", "模型測試失敗。"
	}
}

func appendProviderMessageTestOutput(output *strings.Builder, text string, maxBytes int) bool {
	if output == nil || text == "" {
		return false
	}
	remaining := maxBytes - output.Len()
	if remaining <= 0 {
		return true
	}
	if len(text) <= remaining {
		output.WriteString(text)
		return false
	}
	text = text[:remaining]
	for text != "" && !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	output.WriteString(text)
	return true
}

func upsertServerProvider(items []config.ProviderConfig, provider config.ProviderConfig) []config.ProviderConfig {
	out := append([]config.ProviderConfig(nil), items...)
	for i, existing := range out {
		if existing.Name == provider.Name {
			out[i] = provider
			return out
		}
	}
	return append(out, provider)
}

func renameServerProvider(items []config.ProviderConfig, oldName string, provider config.ProviderConfig) []config.ProviderConfig {
	out := make([]config.ProviderConfig, 0, len(items))
	replaced := false
	for _, existing := range items {
		if existing.Name == oldName {
			if !replaced {
				out = append(out, provider)
				replaced = true
			}
			continue
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, provider)
	}
	return out
}

func renameProviderModelReferences(cfg *config.Config, oldName, newName, oldModel, newModel string) {
	if cfg == nil || strings.TrimSpace(oldName) == "" || strings.TrimSpace(newName) == "" || oldName == newName {
		return
	}
	cfg.Agent.DefaultModel = renameProviderModelReference(cfg.Agent.DefaultModel, oldName, newName, oldModel, newModel)
	cfg.Agent.SummaryModel = renameProviderModelReference(cfg.Agent.SummaryModel, oldName, newName, oldModel, newModel)
	cfg.Agent.ReviewModel = renameProviderModelReference(cfg.Agent.ReviewModel, oldName, newName, oldModel, newModel)
	if cfg.Agent.SubagentModels != nil {
		models := make(map[string]string, len(cfg.Agent.SubagentModels))
		for role, model := range cfg.Agent.SubagentModels {
			models[role] = renameProviderModelReference(model, oldName, newName, oldModel, newModel)
		}
		cfg.Agent.SubagentModels = models
	}
	if cfg.Agent.SubagentModelPools != nil {
		pools := make(map[string][]string, len(cfg.Agent.SubagentModelPools))
		for role, models := range cfg.Agent.SubagentModelPools {
			updated := make([]string, len(models))
			for i, model := range models {
				updated[i] = renameProviderModelReference(model, oldName, newName, oldModel, newModel)
			}
			pools[role] = updated
		}
		cfg.Agent.SubagentModelPools = pools
	}
}

func renameProviderModelReference(value, oldName, newName, oldModel, newModel string) string {
	providerName, modelName := providers.SplitModel(strings.TrimSpace(value))
	if providerName != oldName || modelName == "" {
		return value
	}
	if strings.TrimSpace(oldModel) != "" && modelName == oldModel && strings.TrimSpace(newModel) != "" {
		modelName = newModel
	}
	return newName + ":" + modelName
}

func removeServerProvider(items []config.ProviderConfig, name string) ([]config.ProviderConfig, bool) {
	out := make([]config.ProviderConfig, 0, len(items))
	removed := false
	for _, provider := range items {
		if provider.Name == name {
			removed = true
			continue
		}
		out = append(out, provider)
	}
	return out, removed
}
