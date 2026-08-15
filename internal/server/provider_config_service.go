package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"autoto/internal/config"
	"autoto/internal/providers"
	"autoto/internal/secrets"
)

type providerConfigService struct {
	server *Server
}

func (s *Server) providerConfigs() providerConfigService {
	return providerConfigService{server: s}
}

func (p providerConfigService) update(ctx context.Context, providerName string, req providerConfigUpdateRequest) (providerConfigUpdateResponse, error) {
	s := p.server
	existing, existed := s.providerConfig(providerName)
	if existed {
		existing.Models = s.providerModels(providerName)
	}
	if req.CreateOnly && existed {
		return providerConfigUpdateResponse{}, apiErr(http.StatusConflict, "Provider 名稱已存在")
	}
	updated, err := providerConfigFromUpdateRequest(providerName, existing, req)
	if err != nil {
		return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, err.Error())
	}
	renamed := existed && existing.Name != updated.Name
	if renamed {
		if config.IsBuiltinProviderName(existing.Name) {
			return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "內建 Provider 不支援重新命名")
		}
		if config.IsBuiltinProviderName(updated.Name) {
			return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "新的 Provider 名稱不能使用內建名稱")
		}
		if _, occupied := s.providerConfig(updated.Name); occupied {
			return providerConfigUpdateResponse{}, apiErr(http.StatusConflict, "Provider 名稱已存在")
		}
		if existing.ProxyAuthSource == secrets.ProviderSecretSourceStoredUnavailable || existing.RequestHeadersSource == secrets.ProviderSecretSourceStoredUnavailable {
			return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "無法讀取原 Provider 網路憑證；請恢復憑證倉庫或重新輸入網路憑證後再重新命名")
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
				return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "重新命名已儲存憑證的 Provider 前請重新輸入 API Key")
			}
		} else if resolved, _, resolveErr := s.providerVault.Resolve(ctx, serverProviderSecretBinding(existing)); resolveErr != nil || strings.TrimSpace(resolved) == "" {
			// A rename with an unreadable key leaves nothing to migrate, so fail
			// loudly. A protocol switch degrades to "re-enter the key", which is
			// the behaviour that shipped before migration existed.
			if renamed {
				return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "無法讀取原 Provider 憑證；請重新輸入 API Key 後再重新命名")
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
		return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, err.Error())
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
		return providerConfigUpdateResponse{}, apiErr(http.StatusConflict, err.Error())
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
			_, err = s.providerVault.PrepareSet(ctx, serverProviderSecretBinding(updated), incomingAPIKey)
		case "clear":
			err = s.providerVault.PrepareClear(ctx, serverProviderSecretBinding(updated))
		}
		if err != nil {
			slog.Error("prepare provider api-key secret", "provider", updated.Name, "mutation", secretMutation, "error", err)
			return providerConfigUpdateResponse{}, apiErr(http.StatusInternalServerError, "無法安全儲存 Provider 憑證。")
		}
		preparedSecretKinds = append(preparedSecretKinds, secrets.ProviderAPIKeyKind)
	}
	if s.providerVault != nil && transportSecretMutation {
		transportKinds, prepareErr := s.prepareProviderTransportSecrets(ctx, updated)
		preparedSecretKinds = append(preparedSecretKinds, transportKinds...)
		if prepareErr != nil {
			slog.Error("prepare provider transport secrets", "provider", updated.Name, "error", prepareErr)
			s.rollbackProviderSecretKinds(ctx, updated.Name, preparedSecretKinds)
			return providerConfigUpdateResponse{}, apiErr(http.StatusInternalServerError, "無法安全儲存 Provider 網路憑證。")
		}
	}

	s.runProviderMutationHook()
	persisted, err := s.persistProviderConfig(configPath, cfg)
	if err != nil {
		s.rollbackProviderSecretKinds(ctx, updated.Name, preparedSecretKinds)
		return providerConfigUpdateResponse{}, apiErr(http.StatusInternalServerError, fmt.Sprintf("儲存設定失敗：%v", err))
	}
	if len(preparedSecretKinds) > 0 && !persisted {
		s.rollbackProviderSecretKinds(ctx, updated.Name, preparedSecretKinds)
		return providerConfigUpdateResponse{}, apiErr(http.StatusInternalServerError, "設定路徑不可用，Provider 憑證未儲存。")
	}
	if err := s.commitProviderSecretKinds(ctx, updated.Name, preparedSecretKinds); err != nil {
		// config.json already contains the target revisions. Publish that same
		// target in memory so a later unrelated save cannot overwrite the durable
		// transaction while startup recovery finishes pending secret commits.
		publishTargetConfig()
		return providerConfigUpdateResponse{}, apiErr(http.StatusInternalServerError, "Provider 憑證提交未完成；重啟後將自動恢復。")
	}
	oldSecretCleanupFailed := false
	if renamed && s.providerVault != nil {
		for _, kind := range []string{secrets.ProviderAPIKeyKind, secrets.ProviderProxyAuthKind, secrets.ProviderRequestHeadersKind} {
			if s.providerVault.DeleteKind(ctx, existing.Name, kind) != nil {
				oldSecretCleanupFailed = true
			}
		}
	}
	publishTargetConfig()

	status := s.providerAPIKeyStatus(ctx, updated)
	message := "Provider 設定已持久化並在目前執行階段生效。"
	if renamed {
		message = "Provider 設定已儲存，名稱已更新並在目前執行階段生效。"
	}
	if s.providerVault == nil && (incomingAPIKey != "" || providerProxyAuthConfigured(updated) || providerHeadersConfigured(updated)) {
		message = "Provider 設定已在目前執行階段生效；目前執行個體未啟用持久憑證倉庫，敏感網路設定不會跨重啟儲存。"
	}
	if oldSecretCleanupFailed {
		message += "舊憑證記錄未能立即清理，將在後續恢復流程中處理。"
	}
	return providerConfigUpdateResponse{
		Provider:        s.settingsProviderResponse(ctx, updated),
		Persisted:       persisted,
		APIKeyPersisted: status.Persisted,
		Message:         message,
	}, nil
}

func (p providerConfigService) patch(ctx context.Context, providerName string, req providerConfigPatchRequest) (providerConfigUpdateResponse, error) {
	s := p.server
	existing, ok := s.providerConfig(providerName)
	if !ok {
		return providerConfigUpdateResponse{}, apiErr(http.StatusNotFound, "provider not found")
	}
	existing.Models = s.providerModels(providerName)

	updated := existing
	if req.Enabled != nil {
		updated.Disabled = !*req.Enabled
	}
	if req.GatewayEnabled != nil {
		if *req.GatewayEnabled && providerGatewaySharingForbidden(updated.Type, updated.Profile) {
			return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "OAuth-backed providers cannot be enabled for the shared API gateway")
		}
		updated.GatewayEnabled = *req.GatewayEnabled
	}
	if req.Model != nil {
		updated.Model = strings.TrimSpace(*req.Model)
		if updated.Model == "" {
			return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "model must not be empty")
		}
		if len(updated.Model) > maxProviderModelBytes || strings.ContainsAny(updated.Model, "\x00\r\n") {
			return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, "model is invalid")
		}
		updated.Models = config.NormalizeProviderModels(updated.Models, updated.Model)
	}

	adapter, err := s.newRuntimeProvider(updated)
	if err != nil {
		return providerConfigUpdateResponse{}, apiErr(http.StatusBadRequest, err.Error())
	}

	s.cfgMu.RLock()
	cfg := s.cfg
	cfg.Providers.Instances = upsertServerProvider(cfg.Providers.Instances, updated)
	configPath := s.configPath
	s.cfgMu.RUnlock()
	if err := s.ensureProviderDefaultAfterMutation(cfg, providerName); err != nil {
		return providerConfigUpdateResponse{}, apiErr(http.StatusConflict, err.Error())
	}

	s.runProviderMutationHook()
	persisted, err := s.persistProviderConfig(configPath, cfg)
	if err != nil {
		return providerConfigUpdateResponse{}, apiErr(http.StatusInternalServerError, fmt.Sprintf("儲存設定失敗：%v", err))
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

	status := s.providerAPIKeyStatus(ctx, updated)
	return providerConfigUpdateResponse{
		Provider:        s.settingsProviderResponse(ctx, updated),
		Persisted:       persisted,
		APIKeyPersisted: status.Persisted,
		Message:         "Provider 生命週期更新已在目前執行階段生效。",
	}, nil
}

func (p providerConfigService) delete(ctx context.Context, providerName string) (providerDeleteResponse, error) {
	s := p.server
	existing, ok := s.providerConfig(providerName)
	if !ok {
		return providerDeleteResponse{}, apiErr(http.StatusNotFound, "provider not found")
	}
	if config.IsBuiltinProviderName(existing.Name) {
		return providerDeleteResponse{}, apiErr(http.StatusConflict, "built-in providers cannot be deleted")
	}

	s.cfgMu.RLock()
	cfg := s.cfg
	var removed bool
	cfg.Providers.Instances, removed = removeServerProvider(cfg.Providers.Instances, providerName)
	configPath := s.configPath
	s.cfgMu.RUnlock()
	if !removed {
		return providerDeleteResponse{}, apiErr(http.StatusNotFound, "provider not found")
	}
	if err := s.ensureProviderDefaultAfterMutation(cfg, providerName); err != nil {
		return providerDeleteResponse{}, apiErr(http.StatusConflict, err.Error())
	}

	preparedSecretKinds := make([]string, 0, 3)
	if s.providerVault != nil {
		for _, kind := range []string{secrets.ProviderAPIKeyKind, secrets.ProviderProxyAuthKind, secrets.ProviderRequestHeadersKind} {
			if err := s.providerVault.PrepareDeleteKind(ctx, providerName, kind); err != nil {
				s.rollbackProviderSecretKinds(ctx, providerName, preparedSecretKinds)
				return providerDeleteResponse{}, apiErr(http.StatusInternalServerError, "無法安全刪除 Provider 憑證。")
			}
			preparedSecretKinds = append(preparedSecretKinds, kind)
		}
	}
	s.runProviderMutationHook()
	persisted, err := s.persistProviderConfig(configPath, cfg)
	if err != nil {
		s.rollbackProviderSecretKinds(ctx, providerName, preparedSecretKinds)
		return providerDeleteResponse{}, apiErr(http.StatusInternalServerError, fmt.Sprintf("儲存設定失敗：%v", err))
	}
	if len(preparedSecretKinds) > 0 && !persisted {
		s.rollbackProviderSecretKinds(ctx, providerName, preparedSecretKinds)
		return providerDeleteResponse{}, apiErr(http.StatusInternalServerError, "設定路徑不可用，Provider 未刪除。")
	}
	if err := s.commitProviderSecretKinds(ctx, providerName, preparedSecretKinds); err != nil {
		// The config no longer references this Provider. Remove it from the
		// current registry as well; startup recovery will finish DB cleanup.
		s.unregisterProvider(providerName)
		s.refreshProviderDefault(cfg)
		s.cfgMu.Lock()
		s.cfg = cfg
		s.cfgMu.Unlock()
		return providerDeleteResponse{}, apiErr(http.StatusInternalServerError, "Provider 已移除，憑證清理將在重啟後自動完成。")
	}
	s.unregisterProvider(providerName)
	s.refreshProviderDefault(cfg)
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
	return providerDeleteResponse{Deleted: true, Name: providerName, Persisted: persisted}, nil
}

func (p providerConfigService) configForDraftTest(ctx context.Context, providerName string, req providerConfigUpdateRequest) (config.ProviderConfig, error) {
	s := p.server
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

func (p providerConfigService) testAdapter(ctx context.Context, provider config.ProviderConfig) providerTestResponse {
	s := p.server
	adapter, err := s.newRuntimeProvider(provider)
	if err != nil {
		return providerTestResponse{
			Configured: provider.IsConfigured(), ErrorCode: "invalid_configuration", Message: describeProviderConfigError(provider, err),
		}
	}
	configured := providers.ConfiguredFor(adapter, provider.IsConfigured())
	if !configured {
		return providerTestResponse{
			Configured: false,
			ErrorCode:  "not_configured",
			Message:    "需要 API Key，尚未執行連線預檢。",
		}
	}
	ctx, cancel := context.WithTimeout(ctx, providerTestTimeout)
	defer cancel()
	models, err := adapter.ListModels(ctx)
	if err != nil {
		errorCode, message, reachable := classifyProviderTestError(err)
		return providerTestResponse{
			Reachable: reachable, Configured: configured, ErrorCode: errorCode, Message: message,
		}
	}
	models = normalizeModelNames(models, provider.Model)
	return providerTestResponse{
		Reachable: true, Configured: configured, ModelCount: len(models), Models: models, Message: "Provider 可連線。",
	}
}

func (p providerConfigService) messageTestAdapter(provider config.ProviderConfig) (providers.Provider, providerMessageTestResponse, bool) {
	s := p.server
	adapter, err := s.newRuntimeProvider(provider)
	if err != nil {
		return nil, providerMessageTestResponse{ErrorCode: "invalid_configuration", Message: describeProviderConfigError(provider, err)}, true
	}
	configured := providers.ConfiguredForScenario(adapter, provider.IsConfigured(), providers.CallScenarioInternal)
	if !configured {
		return nil, providerMessageTestResponse{Model: provider.Model, ErrorCode: "not_configured", Message: "需要 API Key，尚未傳送測試。"}, true
	}
	return adapter, providerMessageTestResponse{}, false
}
