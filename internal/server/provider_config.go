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
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req providerConfigUpdateRequest
	if err := decodeProviderConfigUpdateRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Keep the full config read-modify-save-publish transaction serialized with
	// security and continuation updates. The global config lock is always
	// acquired before the narrower provider runtime lock.
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	existing, existed := s.providerConfig(providerName)
	if existed {
		existing.Models = s.providerModels(providerName)
	}
	if req.CreateOnly && existed {
		writeError(w, http.StatusConflict, "Provider 名称已存在")
		return
	}
	updated, err := providerConfigFromUpdateRequest(providerName, existing, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	renamed := existed && existing.Name != updated.Name
	if renamed {
		if config.IsBuiltinProviderName(existing.Name) {
			writeError(w, http.StatusBadRequest, "内置 Provider 不支持重命名")
			return
		}
		if config.IsBuiltinProviderName(updated.Name) {
			writeError(w, http.StatusBadRequest, "新的 Provider 名称不能使用内置名称")
			return
		}
		if _, occupied := s.providerConfig(updated.Name); occupied {
			writeError(w, http.StatusConflict, "Provider 名称已存在")
			return
		}
		if existing.ProxyAuthSource == secrets.ProviderSecretSourceStoredUnavailable || existing.RequestHeadersSource == secrets.ProviderSecretSourceStoredUnavailable {
			writeError(w, http.StatusBadRequest, "无法读取原 Provider 网络凭据；请恢复凭据仓库或重新输入网络凭据后再重命名")
			return
		}
	}

	incomingAPIKey := strings.TrimSpace(req.APIKey)
	// A stored key is scoped to the endpoint it was entered for, not to the
	// protocol spoken there. Switching protocol keeps the same host and
	// transport, so re-encrypt the stored key under the new binding instead of
	// making the user paste it again. Anything that moves the endpoint — base
	// URL, proxy, TLS posture — still falls through to the clearing branch.
	if storedProviderSecretMigratable(existed, existing, updated) && incomingAPIKey == "" && !req.ClearAPIKey {
		if s.providerVault == nil {
			if renamed {
				writeError(w, http.StatusBadRequest, "重命名已保存凭据的 Provider 前请重新输入 API Key")
				return
			}
		} else if resolved, _, resolveErr := s.providerVault.Resolve(r.Context(), serverProviderSecretBinding(existing)); resolveErr != nil || strings.TrimSpace(resolved) == "" {
			// A rename with an unreadable key leaves nothing to migrate, so fail
			// loudly. A protocol switch degrades to "re-enter the key", which is
			// the behaviour that shipped before migration existed.
			if renamed {
				writeError(w, http.StatusBadRequest, "无法读取原 Provider 凭据；请重新输入 API Key 后再重命名")
				return
			}
		} else {
			incomingAPIKey = strings.TrimSpace(resolved)
		}
	}
	secretMutation := ""
	switch {
	case incomingAPIKey != "":
		updated.SecretRevision = nextProviderSecretRevision(existing.SecretRevision)
		updated.APIKey = incomingAPIKey
		if s.providerVault != nil {
			updated.APIKeySource = secrets.ProviderSecretSourceStored
		} else {
			updated.APIKeySource = secrets.ProviderSecretSourceRuntime
		}
		secretMutation = "set"
	case req.ClearAPIKey:
		updated.SecretRevision = nextProviderSecretRevision(existing.SecretRevision)
		updated.APIKey = ""
		updated.APIKeySource = secrets.ProviderSecretSourceNone
		secretMutation = "clear"
	case existed && providerSecretBindingChanged(existing, updated):
		// Any inherited key is scoped to the endpoint and transport where it was
		// entered. Never silently forward stored, runtime, or environment values
		// across a changed security boundary.
		updated.SecretRevision = nextProviderSecretRevision(existing.SecretRevision)
		updated.APIKey = ""
		updated.APIKeySource = secrets.ProviderSecretSourceNone
		secretMutation = "clear"
	}

	transportSecretMutation := providerTransportSecretMutationRequired(existing, updated)
	if transportSecretMutation {
		updated.TransportSecretRevision = nextProviderSecretRevision(existing.TransportSecretRevision)
		if providerProxyAuthConfigured(updated) {
			if s.providerVault != nil {
				updated.ProxyAuthSource = secrets.ProviderSecretSourceStored
			} else {
				updated.ProxyAuthSource = secrets.ProviderSecretSourceRuntime
			}
		} else {
			updated.ProxyAuthSource = secrets.ProviderSecretSourceNone
		}
		if providerHeadersConfigured(updated) {
			if s.providerVault != nil {
				updated.RequestHeadersSource = secrets.ProviderSecretSourceStored
			} else {
				updated.RequestHeadersSource = secrets.ProviderSecretSourceRuntime
			}
		} else {
			updated.RequestHeadersSource = secrets.ProviderSecretSourceNone
		}
	}

	adapter, err := s.newRuntimeProvider(updated)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.cfgMu.RLock()
	cfg := s.cfg
	if renamed {
		cfg.Providers.Instances = renameServerProvider(cfg.Providers.Instances, existing.Name, updated)
		renameProviderModelReferences(&cfg, existing.Name, updated.Name, existing.Model, updated.Model)
	} else {
		cfg.Providers.Instances = upsertServerProvider(cfg.Providers.Instances, updated)
	}
	configPath := s.configPath
	s.cfgMu.RUnlock()
	if err := s.ensureProviderDefaultAfterMutation(cfg, updated.Name); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	publishTargetConfig := func() {
		if renamed {
			s.unregisterProvider(existing.Name)
		}
		if updated.Disabled {
			s.unregisterProvider(updated.Name)
		} else {
			s.registerProviderAdapter(adapter)
		}
		s.refreshProviderDefault(cfg)
		s.cfgMu.Lock()
		s.cfg = cfg
		s.cfgMu.Unlock()
	}

	preparedSecretKinds := make([]string, 0, 3)
	if s.providerVault != nil && secretMutation != "" {
		switch secretMutation {
		case "set":
			_, err = s.providerVault.PrepareSet(r.Context(), serverProviderSecretBinding(updated), incomingAPIKey)
		case "clear":
			err = s.providerVault.PrepareClear(r.Context(), serverProviderSecretBinding(updated))
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法安全保存 Provider 凭据。")
			return
		}
		preparedSecretKinds = append(preparedSecretKinds, secrets.ProviderAPIKeyKind)
	}
	if s.providerVault != nil && transportSecretMutation {
		transportKinds, prepareErr := s.prepareProviderTransportSecrets(r.Context(), updated)
		preparedSecretKinds = append(preparedSecretKinds, transportKinds...)
		if prepareErr != nil {
			s.rollbackProviderSecretKinds(r.Context(), updated.Name, preparedSecretKinds)
			writeError(w, http.StatusInternalServerError, "无法安全保存 Provider 网络凭据。")
			return
		}
	}

	s.runProviderMutationHook()
	persisted, err := s.persistProviderConfig(configPath, cfg)
	if err != nil {
		s.rollbackProviderSecretKinds(r.Context(), updated.Name, preparedSecretKinds)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存配置失败：%v", err))
		return
	}
	if len(preparedSecretKinds) > 0 && !persisted {
		s.rollbackProviderSecretKinds(r.Context(), updated.Name, preparedSecretKinds)
		writeError(w, http.StatusInternalServerError, "配置路径不可用，Provider 凭据未保存。")
		return
	}
	if err := s.commitProviderSecretKinds(r.Context(), updated.Name, preparedSecretKinds); err != nil {
		// config.json already contains the target revisions. Publish that same
		// target in memory so a later unrelated save cannot overwrite the durable
		// transaction while startup recovery finishes pending secret commits.
		publishTargetConfig()
		writeError(w, http.StatusInternalServerError, "Provider 凭据提交未完成；重启后将自动恢复。")
		return
	}
	oldSecretCleanupFailed := false
	if renamed && s.providerVault != nil {
		for _, kind := range []string{secrets.ProviderAPIKeyKind, secrets.ProviderProxyAuthKind, secrets.ProviderRequestHeadersKind} {
			if s.providerVault.DeleteKind(r.Context(), existing.Name, kind) != nil {
				oldSecretCleanupFailed = true
			}
		}
	}
	publishTargetConfig()

	status := s.providerAPIKeyStatus(r.Context(), updated)
	message := "Provider 配置已持久化并在当前运行时生效。"
	if renamed {
		message = "Provider 配置已保存，名称已更新并在当前运行时生效。"
	}
	if s.providerVault == nil && (incomingAPIKey != "" || providerProxyAuthConfigured(updated) || providerHeadersConfigured(updated)) {
		message = "Provider 配置已在当前运行时生效；当前实例未启用持久凭据仓库，敏感网络设置不会跨重启保存。"
	}
	if oldSecretCleanupFailed {
		message += "旧凭据记录未能立即清理，将在后续恢复流程中处理。"
	}
	writeJSON(w, http.StatusOK, providerConfigUpdateResponse{
		Provider:        s.settingsProviderResponse(r.Context(), updated),
		Persisted:       persisted,
		APIKeyPersisted: status.Persisted,
		Message:         message,
	})
}

func (s *Server) patchProviderConfig(w http.ResponseWriter, r *http.Request) {
	providerName := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := validateProviderName(providerName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req providerConfigPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	existing, ok := s.providerConfig(providerName)
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	existing.Models = s.providerModels(providerName)

	updated := existing
	if req.Enabled != nil {
		updated.Disabled = !*req.Enabled
	}
	if req.GatewayEnabled != nil {
		if *req.GatewayEnabled && providerGatewaySharingForbidden(updated.Type, updated.Profile) {
			writeError(w, http.StatusBadRequest, "OAuth-backed providers cannot be enabled for the shared API gateway")
			return
		}
		updated.GatewayEnabled = *req.GatewayEnabled
	}
	if req.Model != nil {
		updated.Model = strings.TrimSpace(*req.Model)
		if updated.Model == "" {
			writeError(w, http.StatusBadRequest, "model must not be empty")
			return
		}
		if len(updated.Model) > maxProviderModelBytes || strings.ContainsAny(updated.Model, "\x00\r\n") {
			writeError(w, http.StatusBadRequest, "model is invalid")
			return
		}
		updated.Models = config.NormalizeProviderModels(updated.Models, updated.Model)
	}

	adapter, err := s.newRuntimeProvider(updated)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.cfgMu.RLock()
	cfg := s.cfg
	cfg.Providers.Instances = upsertServerProvider(cfg.Providers.Instances, updated)
	configPath := s.configPath
	s.cfgMu.RUnlock()
	if err := s.ensureProviderDefaultAfterMutation(cfg, providerName); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	s.runProviderMutationHook()
	persisted, err := s.persistProviderConfig(configPath, cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存配置失败：%v", err))
		return
	}
	if updated.Disabled {
		s.unregisterProvider(updated.Name)
	} else {
		s.registerProviderAdapter(adapter)
	}
	s.refreshProviderDefault(cfg)
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()

	status := s.providerAPIKeyStatus(r.Context(), updated)
	writeJSON(w, http.StatusOK, providerConfigUpdateResponse{
		Provider:        s.settingsProviderResponse(r.Context(), updated),
		Persisted:       persisted,
		APIKeyPersisted: status.Persisted,
		Message:         "Provider 生命周期更新已在当前运行时生效。",
	})
}

func (s *Server) deleteProviderConfig(w http.ResponseWriter, r *http.Request) {
	providerName := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := validateProviderName(providerName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	existing, ok := s.providerConfig(providerName)
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if config.IsBuiltinProviderName(existing.Name) {
		writeError(w, http.StatusConflict, "built-in providers cannot be deleted")
		return
	}

	s.cfgMu.RLock()
	cfg := s.cfg
	var removed bool
	cfg.Providers.Instances, removed = removeServerProvider(cfg.Providers.Instances, providerName)
	configPath := s.configPath
	s.cfgMu.RUnlock()
	if !removed {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if err := s.ensureProviderDefaultAfterMutation(cfg, providerName); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	preparedSecretKinds := make([]string, 0, 3)
	if s.providerVault != nil {
		for _, kind := range []string{secrets.ProviderAPIKeyKind, secrets.ProviderProxyAuthKind, secrets.ProviderRequestHeadersKind} {
			if err := s.providerVault.PrepareDeleteKind(r.Context(), providerName, kind); err != nil {
				s.rollbackProviderSecretKinds(r.Context(), providerName, preparedSecretKinds)
				writeError(w, http.StatusInternalServerError, "无法安全删除 Provider 凭据。")
				return
			}
			preparedSecretKinds = append(preparedSecretKinds, kind)
		}
	}
	s.runProviderMutationHook()
	persisted, err := s.persistProviderConfig(configPath, cfg)
	if err != nil {
		s.rollbackProviderSecretKinds(r.Context(), providerName, preparedSecretKinds)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("保存配置失败：%v", err))
		return
	}
	if len(preparedSecretKinds) > 0 && !persisted {
		s.rollbackProviderSecretKinds(r.Context(), providerName, preparedSecretKinds)
		writeError(w, http.StatusInternalServerError, "配置路径不可用，Provider 未删除。")
		return
	}
	if err := s.commitProviderSecretKinds(r.Context(), providerName, preparedSecretKinds); err != nil {
		// The config no longer references this Provider. Remove it from the
		// current registry as well; startup recovery will finish DB cleanup.
		s.unregisterProvider(providerName)
		s.refreshProviderDefault(cfg)
		s.cfgMu.Lock()
		s.cfg = cfg
		s.cfgMu.Unlock()
		writeError(w, http.StatusInternalServerError, "Provider 已移除，凭据清理将在重启后自动完成。")
		return
	}
	s.unregisterProvider(providerName)
	s.refreshProviderDefault(cfg)
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
	writeJSON(w, http.StatusOK, providerDeleteResponse{Deleted: true, Name: providerName, Persisted: persisted})
}

func (s *Server) testProviderConfig(w http.ResponseWriter, r *http.Request) {
	if err := rejectProviderTestBody(r); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	providerName := strings.TrimSpace(chi.URLParam(r, "name"))
	if err := validateProviderName(providerName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, ok := s.providerConfig(providerName)
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	s.testProviderAdapter(w, r, provider)
}

// testProviderConfigDraft validates and tests a full configuration draft without
// writing it to disk or changing the runtime registry. Blank keys may reuse only
// the explicitly identified original provider and never cross endpoint bindings.
var errProviderDraftNameConflict = errors.New("Provider 名称已存在")

func (s *Server) providerConfigForDraftTest(ctx context.Context, providerName string, req providerConfigUpdateRequest) (config.ProviderConfig, error) {
	providerName = strings.TrimSpace(providerName)
	if req.CreateOnly {
		if _, occupied := s.providerConfig(providerName); occupied {
			return config.ProviderConfig{}, errProviderDraftNameConflict
		}
		provider, err := providerConfigFromUpdateRequest(providerName, config.ProviderConfig{}, req)
		return provider, err
	}
	originalName := strings.TrimSpace(req.OriginalName)
	if originalName == "" {
		originalName = providerName
	}
	if err := validateProviderName(originalName); err != nil {
		return config.ProviderConfig{}, err
	}
	if originalName != providerName {
		if _, occupied := s.providerConfig(providerName); occupied {
			return config.ProviderConfig{}, errProviderDraftNameConflict
		}
	}
	existing, _ := s.providerConfig(originalName)
	existing.Models = s.providerModels(originalName)
	provider, err := providerConfigFromUpdateRequest(providerName, existing, req)
	if err != nil {
		return config.ProviderConfig{}, err
	}
	if strings.TrimSpace(req.APIKey) == "" && strings.TrimSpace(existing.Name) != "" && providerSecretBindingChanged(existing, provider) {
		// Mirror the save path: a stored key survives a protocol switch that
		// keeps the same endpoint, so the draft test can reach upstream and the
		// model list refreshes without retyping the key.
		migrated := ""
		if s.providerVault != nil && storedProviderSecretMigratable(true, existing, provider) {
			if resolved, _, resolveErr := s.providerVault.Resolve(ctx, serverProviderSecretBinding(existing)); resolveErr == nil {
				migrated = strings.TrimSpace(resolved)
			}
		}
		if migrated != "" {
			provider.APIKey = migrated
			provider.APIKeySource = secrets.ProviderSecretSourceStored
		} else {
			provider.APIKey = ""
			provider.APIKeySource = secrets.ProviderSecretSourceNone
		}
	}
	return provider, nil
}

func (s *Server) testProviderConfigDraft(w http.ResponseWriter, r *http.Request) {
	var req providerConfigUpdateRequest
	if err := decodeProviderConfigUpdateRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ClearAPIKey {
		writeError(w, http.StatusBadRequest, "provider test does not support clearing API keys")
		return
	}
	providerName := strings.TrimSpace(req.Name)
	if err := validateProviderName(providerName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
		writeError(w, status, err.Error())
		return
	}
	if _, err := s.newRuntimeProvider(provider); err != nil {
		writeError(w, http.StatusBadRequest, describeProviderConfigError(provider, err))
		return
	}
	s.testProviderAdapter(w, r, provider)
}

// testProviderMessageDraft sends one tool-free prompt through a temporary
// provider adapter. It never persists the draft or changes the runtime registry.
func (s *Server) testProviderMessageDraft(w http.ResponseWriter, r *http.Request) {
	var req providerMessageTestRequest
	if err := decodeProviderJSONRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateProviderConfigRequest(req.configUpdateRequest()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ClearAPIKey {
		writeError(w, http.StatusBadRequest, "provider message test does not support clearing API keys")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if err := validateAPIText("prompt", prompt, providerMessageTestMaxPromptBytes, true, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	providerName := strings.TrimSpace(req.Name)
	if err := validateProviderName(providerName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
		writeError(w, status, err.Error())
		return
	}
	adapter, err := s.newRuntimeProvider(provider)
	if err != nil {
		writeJSON(w, http.StatusOK, providerMessageTestResponse{ErrorCode: "invalid_configuration", Message: describeProviderConfigError(provider, err)})
		return
	}
	configured := providers.ConfiguredForScenario(adapter, provider.IsConfigured(), providers.CallScenarioInternal)
	if !configured {
		writeJSON(w, http.StatusOK, providerMessageTestResponse{Model: provider.Model, ErrorCode: "not_configured", Message: "需要 API Key，尚未发送测试。"})
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
					writeJSON(w, http.StatusOK, providerMessageTestResponse{Model: provider.Model, Usage: usage, ErrorCode: "empty_response", Message: "模型没有返回文本。"})
					return
				}
				writeJSON(w, http.StatusOK, providerMessageTestResponse{Success: true, Model: provider.Model, Output: text, Usage: usage, Truncated: truncated, Message: "测试消息发送成功。"})
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
	adapter, err := s.newRuntimeProvider(provider)
	if err != nil {
		writeJSON(w, http.StatusOK, providerTestResponse{
			Configured: provider.IsConfigured(), ErrorCode: "invalid_configuration", Message: describeProviderConfigError(provider, err),
		})
		return
	}
	configured := providers.ConfiguredFor(adapter, provider.IsConfigured())
	if !configured {
		writeJSON(w, http.StatusOK, providerTestResponse{
			Configured: false,
			ErrorCode:  "not_configured",
			Message:    "需要 API Key，尚未执行连接预检。",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), providerTestTimeout)
	defer cancel()
	models, err := adapter.ListModels(ctx)
	if err != nil {
		errorCode, message, reachable := classifyProviderTestError(err)
		writeJSON(w, http.StatusOK, providerTestResponse{
			Reachable: reachable, Configured: configured, ErrorCode: errorCode, Message: message,
		})
		return
	}
	models = normalizeModelNames(models, provider.Model)
	writeJSON(w, http.StatusOK, providerTestResponse{
		Reachable: true, Configured: configured, ModelCount: len(models), Models: models, Message: "Provider 可访问。",
	})
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
			return "", "", "", "", false, errors.New("代理地址无效")
		}
		if parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawQuery != "" || parsed.ForceQuery || (parsed.Path != "" && parsed.Path != "/") {
			return "", "", "", "", false, errors.New("代理地址不能包含路径、查询参数或片段")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return "", "", "", "", false, errors.New("代理协议仅支持 http、https、socks5 或 socks5h")
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
				return nil, "", fmt.Errorf("安全边界已变化，请重新输入请求头 %q 的值", name)
			}
			value = existingValues[strings.ToLower(name)]
			if value == "" {
				return nil, "", fmt.Errorf("无法保留请求头 %q，请重新输入值", name)
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
		return config.ProviderConfig{}, errors.New("API 协议当前仅支持 codex、openai-compatible、anthropic、openai、gemini-interactions、gemini、grok 或 kimi")
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if providerType == "openai-compatible" && baseURL == "" {
		return config.ProviderConfig{}, errors.New("中转站 Base URL 不能为空")
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
		return errors.New("Provider 名称最多 64 个字符")
	}
	for i, r := range name {
		if i == 0 && !isProviderNameAlphaNumeric(r) {
			return errors.New("Provider 名称必须以英文字母或数字开头，且只能包含英文字母、数字、点、下划线和中横线")
		}
		if !isProviderNameChar(r) {
			return errors.New("Provider 名称只能包含英文字母、数字、点、下划线和中横线")
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
	return errors.New("不能禁用或删除当前默认 Provider：没有可用且已配置的回退 Provider")
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
// The three call sites used to collapse every cause into "Provider 配置无效。",
// which gave no way to tell a denied base URL apart from a malformed header, so
// a plain-HTTP relay looked like an unexplained rejection.
//
// The underlying error is logged rather than returned: the network package keeps
// its errors free of URL, hostname and resolver detail on purpose, and that
// property must survive being surfaced in the UI.
func describeProviderConfigError(provider config.ProviderConfig, err error) string {
	if err == nil {
		return "Provider 配置无效。"
	}
	slog.Debug("provider runtime configuration rejected", "provider", provider.Name, "error", err.Error())
	if errors.Is(err, network.ErrDestinationDenied) && isPlaintextHTTPBaseURL(provider.BaseURL) && !provider.AllowPlaintextHTTP {
		return "明文 HTTP 只允许连接本机。请改用 https://，或为该 Provider 单独开启「允许明文 HTTP」（开启后 API Key 与请求内容会明文传输）。"
	}
	if errors.Is(err, network.ErrDestinationDenied) {
		return "该地址被网络策略拒绝。"
	}
	if errors.Is(err, network.ErrInvalidURL) {
		return "Base URL 格式无效。"
	}
	if errors.Is(err, network.ErrNameResolution) {
		return "无法解析该地址的主机名。"
	}
	if errors.Is(err, network.ErrProxyConfiguration) {
		return "代理配置无效。"
	}
	return "Provider 配置无效。"
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
		return "timeout", "连接 Provider 超时。", false
	}
	messageText := strings.ToLower(err.Error())
	switch {
	case strings.Contains(messageText, "401"), strings.Contains(messageText, "403"), strings.Contains(messageText, "unauthorized"), strings.Contains(messageText, "forbidden"):
		return "authentication_failed", "Provider 拒绝了凭据。", true
	case strings.Contains(messageText, "not configured"), strings.Contains(messageText, "没有可用"), strings.Contains(messageText, "credential"), strings.Contains(messageText, "api key is required"):
		return "not_configured", "Provider 凭据尚未配置。", false
	case strings.Contains(messageText, "connection refused"), strings.Contains(messageText, "no such host"), strings.Contains(messageText, "network is unreachable"), strings.Contains(messageText, "connect:"):
		return "unreachable", "无法连接 Provider。", false
	// A models catalog is optional. The Messages API does not require one, and
	// third-party Anthropic-compatible endpoints commonly serve /v1/messages
	// without /v1/models -- DeepSeek returns 404 there while generating
	// perfectly well. Reporting that as a failed connection told the operator
	// the provider was unreachable when only its catalog was, and the draft
	// could not be saved even though the manual model entry below it exists for
	// exactly this case.
	case strings.Contains(messageText, "404"), strings.Contains(messageText, "not found"):
		return "catalog_unavailable", "Provider 可访问，但没有模型目录，请手动填写模型名称。", true
	default:
		return "request_failed", "Provider 测试失败。", false
	}
}

// classifyProviderMessageTestError maps an upstream failure onto a stable code
// and a user-facing sentence. Both deliberately drop the underlying error so a
// provider cannot leak endpoint or credential detail into the UI, which left
// "模型测试失败。" with no way to find out why; the original text is logged at
// debug level instead so operators can diagnose it locally.
func classifyProviderMessageTestError(err error) (errorCode, message string) {
	errorCode, _, _ = classifyProviderTestError(err)
	if err != nil {
		slog.Debug("provider message test failed", "errorCode", errorCode, "error", err.Error())
	}
	switch errorCode {
	case "timeout":
		return errorCode, "模型响应超时。"
	case "authentication_failed":
		return errorCode, "Provider 拒绝了凭据，测试消息未发送。"
	case "not_configured":
		return errorCode, "Provider 凭据尚未配置，测试消息未发送。"
	case "unreachable":
		return errorCode, "无法连接 Provider，测试消息未发送。"
	default:
		return "request_failed", "模型测试失败。"
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
