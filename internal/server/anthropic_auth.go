package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

const anthropicConfiguredAccountID = "configured"

type anthropicAccountCreateRequest struct {
	AuthType string `json:"authType"`
	Profile  string `json:"profile"`
	APIKey   string `json:"apiKey"`
	Alias    string `json:"alias"`
	Priority *int   `json:"priority"`
}

type anthropicAccountPatchRequest struct {
	Alias    *string `json:"alias"`
	Priority *int    `json:"priority"`
	Disabled *bool   `json:"disabled"`
}

type anthropicAccountsResponse struct {
	Accounts []map[string]any `json:"accounts"`
	Count    int              `json:"count"`
}

func (s *Server) listAnthropicAccounts(w http.ResponseWriter, r *http.Request) {
	store, err := s.nativeAnthropicCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	items, err := store.Load()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	statsByID := map[string]db.ProviderAccountStats{}
	if s.store != nil {
		if stats, statsErr := s.store.ListProviderAccountStats(r.Context(), anthropicauth.DefaultProviderName); statsErr == nil {
			statsByID = stats
		}
	}
	accounts := make([]map[string]any, 0, len(items)+1)
	for _, item := range items {
		accounts = append(accounts, anthropicAccountPayload(
			anthropicauth.Summary(item),
			item.Credential.ID,
			!item.Credential.Disabled,
			true,
			statsByID,
			nil,
			nil,
		))
	}
	if provider, ok := s.anthropicProviderConfig(); ok && strings.TrimSpace(provider.APIKey) != "" && !anthropicStoreContainsAPIKey(items, provider.APIKey) {
		legacy := map[string]any{
			"id":         anthropicConfiguredAccountID,
			"name":       anthropicConfiguredAccountID,
			"provider":   anthropicauth.DefaultProviderName,
			"auth_type":  anthropicauth.AuthTypeAPIKey,
			"source":     "configured",
			"priority":   1_000_000,
			"disabled":   provider.Disabled,
			"configured": !provider.Disabled,
			"managed":    false,
		}
		attachAnthropicStats(legacy, anthropicConfiguredAccountID, statsByID)
		accounts = append(accounts, legacy)
	}
	writeJSON(w, http.StatusOK, anthropicAccountsResponse{Accounts: accounts, Count: len(accounts)})
}

func (s *Server) createAnthropicAccount(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var request anthropicAccountCreateRequest
	if err := decodeAnthropicAccountJSON(r.Body, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	priority := anthropicauth.DefaultPriority
	if request.Priority != nil {
		priority = *request.Priority
	}
	store, err := s.nativeAnthropicCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	item, err := store.Create(anthropicauth.CreateRequest{
		AuthType: strings.TrimSpace(request.AuthType),
		Profile:  strings.TrimSpace(request.Profile),
		APIKey:   strings.TrimSpace(request.APIKey),
		Alias:    strings.TrimSpace(request.Alias),
		Priority: priority,
	})
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.ensureNativeAnthropicProvider(); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	payload := anthropicAccountPayload(anthropicauth.Summary(item), item.Credential.ID, !item.Credential.Disabled, true, nil, nil, nil)
	writeJSON(w, http.StatusCreated, payload)
}

func (s *Server) patchAnthropicAccount(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == anthropicConfiguredAccountID {
		writeError(w, http.StatusBadRequest, "既有設定帳號請透過 Anthropic 模型設定修改")
		return
	}
	var request anthropicAccountPatchRequest
	if err := decodeAnthropicAccountJSON(r.Body, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if request.Alias == nil && request.Priority == nil && request.Disabled == nil {
		writeError(w, http.StatusBadRequest, "至少提供 alias、priority 或 disabled 之一")
		return
	}
	store, err := s.nativeAnthropicCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	item, err := store.UpdateMetadata(id, anthropicauth.MetadataUpdate{Alias: request.Alias, Priority: request.Priority, Disabled: request.Disabled})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Anthropic 帳號不存在")
		} else {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, anthropicAccountPayload(anthropicauth.Summary(item), item.Credential.ID, !item.Credential.Disabled, true, nil, nil, nil))
}

func (s *Server) syncAnthropicAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == anthropicConfiguredAccountID {
		writeError(w, http.StatusBadRequest, "既有設定帳號透過模型重新整理進行檢測")
		return
	}
	provider, err := s.nativeAnthropicProvider()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	account, models, rateLimit, err := provider.SyncAccount(ctx, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Anthropic 帳號不存在")
		} else {
			s.writeRequestError(w, r, http.StatusBadGateway, err)
		}
		return
	}
	if s.store != nil {
		fetchedAt := rateLimit.FetchedAt
		if fetchedAt.IsZero() {
			fetchedAt = s.now()
		}
		if err := s.store.UpdateProviderAccountQuota(r.Context(), anthropicauth.DefaultProviderName, id, rateLimit, fetchedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "Anthropic 速率限制快照儲存失敗")
			return
		}
	}
	statsByID := map[string]db.ProviderAccountStats{}
	if s.store != nil {
		if stats, statsErr := s.store.GetProviderAccountStats(r.Context(), anthropicauth.DefaultProviderName, id); statsErr == nil {
			statsByID[id] = stats
		}
	}
	writeJSON(w, http.StatusOK, anthropicAccountPayload(account, id, true, true, statsByID, models, rateLimit))
}

func (s *Server) deleteAnthropicAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == anthropicConfiguredAccountID {
		writeError(w, http.StatusBadRequest, "既有設定帳號不能從帳號清單刪除")
		return
	}
	store, err := s.nativeAnthropicCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	credentialDeleted := true
	if err := store.Delete(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			credentialDeleted = false
		} else {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	statsDeleted := true
	grantDeleted := true
	if s.store != nil {
		if err := s.store.DeleteProviderAccountStats(r.Context(), anthropicauth.DefaultProviderName, id); err != nil {
			statsDeleted = false
		}
		if err := s.store.DeleteGatewayAccountGrant(r.Context(), anthropicauth.DefaultProviderName, id); err != nil {
			grantDeleted = false
		}
	}
	cleanupPending := !statsDeleted || !grantDeleted
	response := map[string]any{
		"status":             "ok",
		"id":                 id,
		"credential_deleted": credentialDeleted,
		"stats_deleted":      statsDeleted,
		"grant_deleted":      grantDeleted,
		"already_missing":    !credentialDeleted,
		"cleanup_pending":    cleanupPending,
		"retryable":          cleanupPending,
	}
	if cleanupPending {
		response["status"] = "partial"
		response["warning"] = "Anthropic 憑證已刪除，但帳號統計或 Gateway 授權清理失敗；可安全重試 DELETE 完成清理"
		writeJSON(w, http.StatusMultiStatus, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeAnthropicAccountJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("Anthropic 帳號內容無效")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("Anthropic 帳號內容必須是單一 JSON 物件")
	}
	return nil
}

func anthropicAccountPayload(summary anthropicauth.AccountSummary, id string, configured, managed bool, statsByID map[string]db.ProviderAccountStats, models []string, rateLimit any) map[string]any {
	payload := map[string]any{}
	if encoded, err := json.Marshal(summary); err == nil {
		_ = json.Unmarshal(encoded, &payload)
	}
	payload["id"] = id
	payload["configured"] = configured
	payload["managed"] = managed
	if len(models) > 0 {
		payload["models"] = models
	}
	if rateLimit != nil {
		payload["rate_limit"] = rateLimit
	}
	attachAnthropicStats(payload, id, statsByID)
	return payload
}

func attachAnthropicStats(payload map[string]any, id string, statsByID map[string]db.ProviderAccountStats) {
	stats, ok := statsByID[id]
	if !ok {
		return
	}
	statsCopy := stats
	payload["stats"] = &statsCopy
	if len(stats.QuotaSnapshotJSON) == 0 {
		return
	}
	var rateLimit map[string]any
	if json.Unmarshal(stats.QuotaSnapshotJSON, &rateLimit) == nil {
		payload["rate_limit"] = rateLimit
		if _, exists := payload["models"]; !exists {
			if models := anthropicSnapshotModels(rateLimit["models"]); len(models) > 0 {
				payload["models"] = models
			}
		}
	}
}

func anthropicSnapshotModels(value any) []string {
	var result []string
	switch typed := value.(type) {
	case []string:
		for _, model := range typed {
			if model = strings.TrimSpace(model); model != "" {
				result = append(result, model)
			}
		}
	case []any:
		for _, raw := range typed {
			if model, ok := raw.(string); ok {
				if model = strings.TrimSpace(model); model != "" {
					result = append(result, model)
				}
			}
		}
	}
	return result
}

func anthropicStoreContainsAPIKey(items []anthropicauth.StoredCredential, apiKey string) bool {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return false
	}
	for _, item := range items {
		candidate := strings.TrimSpace(item.Credential.APIKey)
		if len(candidate) == len(apiKey) && subtle.ConstantTimeCompare([]byte(candidate), []byte(apiKey)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) nativeAnthropicCredentialStore() (*anthropicauth.Store, error) {
	path := anthropicauth.DefaultStoreDir(s.configSnapshot().Paths.HomeDir)
	s.anthropicCredentialsMu.Lock()
	defer s.anthropicCredentialsMu.Unlock()
	if s.anthropicCredentials == nil || strings.TrimSpace(s.anthropicCredentials.Dir()) == "" {
		if path == "" {
			return nil, errors.New("Autoto HomeDir 未設定，無法儲存 Anthropic 憑證")
		}
		s.anthropicCredentials = anthropicauth.NewStore(path)
	}
	return s.anthropicCredentials, nil
}

func (s *Server) nativeAnthropicProvider() (*providers.AnthropicProvider, error) {
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	if provider, ok := s.anthropicProviderConfig(); ok && provider.Disabled {
		s.unregisterProvider(anthropicauth.DefaultProviderName)
		return nil, errors.New("Anthropic Provider 已停用")
	}
	if s.providers != nil {
		if provider, ok := s.providers.Get(anthropicauth.DefaultProviderName); ok {
			if anthropicProvider, ok := provider.(*providers.AnthropicProvider); ok {
				return anthropicProvider, nil
			}
		}
	}
	if err := s.ensureNativeAnthropicProviderLocked(); err != nil {
		return nil, err
	}
	if provider, ok := s.providers.Get(anthropicauth.DefaultProviderName); ok {
		if anthropicProvider, ok := provider.(*providers.AnthropicProvider); ok {
			return anthropicProvider, nil
		}
	}
	return nil, errors.New("Anthropic Provider 不可用")
}

func (s *Server) anthropicProviderConfig() (config.ProviderConfig, bool) {
	for _, provider := range s.configSnapshot().Providers.Instances {
		if provider.Name == anthropicauth.DefaultProviderName {
			return config.NormalizeProviderConfig(provider), true
		}
	}
	return config.ProviderConfig{}, false
}

func (s *Server) ensureNativeAnthropicProvider() error {
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	return s.ensureNativeAnthropicProviderLocked()
}

func (s *Server) ensureNativeAnthropicProviderLocked() error {
	cfg := s.configSnapshot()
	provider := config.ProviderConfig{
		Name:      anthropicauth.DefaultProviderName,
		Type:      "anthropic",
		Model:     "claude-sonnet-4-5",
		MaxTokens: 4096,
	}
	found := false
	for _, existing := range cfg.Providers.Instances {
		if existing.Name != anthropicauth.DefaultProviderName {
			continue
		}
		if existing.Type != "anthropic" {
			return fmt.Errorf("provider %s 已被其他協定佔用", anthropicauth.DefaultProviderName)
		}
		provider = config.NormalizeProviderConfig(existing)
		found = true
		break
	}
	if provider.Disabled {
		s.unregisterProvider(provider.Name)
		if s.providers != nil {
			s.providers.SetDefaultFromConfig(cfg.Agent.DefaultModel, cfg.Providers.Instances)
		}
		return nil
	}
	if !found {
		cfg.Providers.Instances = upsertServerProvider(cfg.Providers.Instances, provider)
		if _, err := s.persistProviderConfig(s.configPathSnapshot(), cfg); err != nil {
			return fmt.Errorf("儲存 Anthropic Provider 設定失敗：%w", err)
		}
	}
	if err := s.registerProvider(provider); err != nil {
		return err
	}
	if s.providers != nil {
		s.providers.SetDefaultFromConfig(cfg.Agent.DefaultModel, cfg.Providers.Instances)
	}
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
	return nil
}
