package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/geminiauth"
	"autoto/internal/grokauth"
	"autoto/internal/kimiauth"
	"autoto/internal/providers"
	"autoto/internal/subscriptionauth"
)

const subscriptionAccountRequestMaxBytes = 64 << 10

type subscriptionAccountPatchRequest struct {
	Alias    *string `json:"alias"`
	Priority *int    `json:"priority"`
	Disabled *bool   `json:"disabled"`
}

type subscriptionAccountPayload struct {
	subscriptionauth.AccountSummary
	Stats *db.ProviderAccountStats `json:"stats,omitempty"`
	// Quota is present only once the account has made a request that returned
	// rate-limit headers. It is absent, never zeroed, when nothing is known.
	Quota *providers.ProviderAccountQuotaSnapshot `json:"quota,omitempty"`
}

type subscriptionAccountsResponse struct {
	Accounts []subscriptionAccountPayload `json:"accounts"`
	Count    int                          `json:"count"`
}

type subscriptionAccountHandlerError struct {
	status  int
	message string
}

func (e *subscriptionAccountHandlerError) Error() string {
	if e == nil {
		return "订阅账号操作失败"
	}
	return e.message
}

func (s *Server) listSubscriptionAccounts(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	provider, err := subscriptionAccountProvider(chi.URLParam(r, "provider"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	store, err := s.nativeSubscriptionCredentialStore(provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items, err := store.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取订阅账号失败")
		return
	}
	statsByID := map[string]db.ProviderAccountStats{}
	if s.store != nil {
		if stats, statsErr := s.store.ListProviderAccountStats(r.Context(), provider); statsErr == nil {
			statsByID = stats
		}
	}
	accounts := make([]subscriptionAccountPayload, 0, len(items))
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.Provider), provider) {
			continue
		}
		payload := subscriptionAccountPayload{AccountSummary: subscriptionauth.Summary(item)}
		if stats, ok := statsByID[item.ID]; ok {
			statsCopy := stats
			payload.Stats = &statsCopy
			if len(stats.QuotaSnapshotJSON) > 0 {
				var quota providers.ProviderAccountQuotaSnapshot
				if json.Unmarshal(stats.QuotaSnapshotJSON, &quota) == nil {
					payload.Quota = &quota
				}
			}
		}
		accounts = append(accounts, payload)
	}
	writeJSON(w, http.StatusOK, subscriptionAccountsResponse{Accounts: accounts, Count: len(accounts)})
}

func (s *Server) patchSubscriptionAccount(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	provider, err := subscriptionAccountProvider(chi.URLParam(r, "provider"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "订阅账号 ID 不能为空")
		return
	}
	var request subscriptionAccountPatchRequest
	if err := decodeSubscriptionAccountJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Alias == nil && request.Priority == nil && request.Disabled == nil {
		writeError(w, http.StatusBadRequest, "至少提供 alias、priority 或 disabled 之一")
		return
	}
	store, err := s.nativeSubscriptionCredentialStore(provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	current, err := store.GetByID(id)
	if err != nil || !strings.EqualFold(strings.TrimSpace(current.Provider), provider) {
		if errors.Is(err, os.ErrNotExist) || err == nil {
			writeError(w, http.StatusNotFound, subscriptionProviderLabel(provider)+" 账号不存在")
		} else {
			writeError(w, http.StatusInternalServerError, "读取订阅账号失败")
		}
		return
	}
	item, err := store.UpdateMetadata(id, subscriptionauth.MetadataUpdate{
		Alias: request.Alias, Priority: request.Priority, Disabled: request.Disabled,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, subscriptionProviderLabel(provider)+" 账号不存在")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, subscriptionAccountPayload{AccountSummary: subscriptionauth.Summary(item)})
}

func (s *Server) syncSubscriptionAccount(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	provider, err := subscriptionAccountProvider(chi.URLParam(r, "provider"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "订阅账号 ID 不能为空")
		return
	}
	store, err := s.nativeSubscriptionCredentialStore(provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	item, err := store.GetByID(id)
	if err != nil || !strings.EqualFold(strings.TrimSpace(item.Provider), provider) {
		if errors.Is(err, os.ErrNotExist) || err == nil {
			writeError(w, http.StatusNotFound, subscriptionProviderLabel(provider)+" 账号不存在")
		} else {
			writeError(w, http.StatusInternalServerError, "读取订阅账号失败")
		}
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	updated, err := s.syncSubscriptionCredential(ctx, store, item)
	if err != nil {
		var handlerErr *subscriptionAccountHandlerError
		if errors.As(err, &handlerErr) {
			writeError(w, handlerErr.status, handlerErr.message)
			return
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) && ctx.Err() != nil {
			writeError(w, http.StatusGatewayTimeout, "订阅账号同步超时")
			return
		}
		writeError(w, http.StatusBadGateway, subscriptionProviderLabel(provider)+" 账号同步失败")
		return
	}
	writeJSON(w, http.StatusOK, subscriptionAccountPayload{AccountSummary: subscriptionauth.Summary(updated)})
}

func (s *Server) syncSubscriptionCredential(ctx context.Context, store *subscriptionauth.Store, item subscriptionauth.StoredCredential) (subscriptionauth.StoredCredential, error) {
	if ctx == nil || store == nil {
		return subscriptionauth.StoredCredential{}, errors.New("订阅账号同步上下文无效")
	}
	update := subscriptionTokenUpdateFromCredential(item.Credential)
	switch strings.ToLower(strings.TrimSpace(item.Provider)) {
	case subscriptionauth.ProviderGemini:
		needsRefresh, err := subscriptionCredentialNeedsRefresh(item.Credential, geminiauth.RefreshLead())
		if err != nil {
			return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusBadRequest, message: "Gemini access token 到期时间无效"}
		}
		if needsRefresh {
			if strings.TrimSpace(item.RefreshToken) == "" {
				return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusBadRequest, message: "Gemini 账号缺少 refresh token，无法刷新"}
			}
			tokens, err := runSubscriptionSyncCall(ctx, func() (*geminiauth.TokenData, error) {
				return geminiauth.New(nil).RefreshTokens(ctx, item.RefreshToken)
			})
			if err != nil || tokens == nil {
				return subscriptionauth.StoredCredential{}, subscriptionSyncUpstreamError(ctx, subscriptionauth.ProviderGemini)
			}
			update.AccessToken = strings.TrimSpace(tokens.AccessToken)
			if refresh := strings.TrimSpace(tokens.RefreshToken); refresh != "" {
				update.RefreshToken = refresh
			}
			if idToken := strings.TrimSpace(tokens.IDToken); idToken != "" {
				update.IDToken = idToken
			}
			if tokenType := strings.TrimSpace(tokens.TokenType); tokenType != "" {
				update.TokenType = tokenType
			}
			update.ExpiresAt = strings.TrimSpace(tokens.ExpiresAt)
		}
		if strings.TrimSpace(update.AccessToken) == "" {
			return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusBadRequest, message: "Gemini 账号缺少可用的 access token"}
		}
		client := geminiauth.New(nil)
		userinfo, err := client.FetchUserInfo(ctx, update.AccessToken)
		if err != nil {
			return subscriptionauth.StoredCredential{}, subscriptionSyncUpstreamError(ctx, subscriptionauth.ProviderGemini)
		}
		projectID, err := client.FetchProjectID(ctx, update.AccessToken)
		if err != nil || strings.TrimSpace(projectID) == "" {
			return subscriptionauth.StoredCredential{}, subscriptionSyncUpstreamError(ctx, subscriptionauth.ProviderGemini)
		}
		update.Email = strings.TrimSpace(userinfo.Email)
		if subject := strings.TrimSpace(userinfo.Subject); subject != "" {
			update.Subject = subject
		}
		update.ProjectID = strings.TrimSpace(projectID)

	case subscriptionauth.ProviderGrok:
		if strings.TrimSpace(item.RefreshToken) == "" {
			return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusBadRequest, message: "Grok 账号缺少 refresh token，无法刷新"}
		}
		tokens, err := runSubscriptionSyncCall(ctx, func() (*grokauth.TokenData, error) {
			return grokauth.New(nil).RefreshTokens(ctx, item.RefreshToken, item.TokenEndpoint)
		})
		if err != nil || tokens == nil {
			return subscriptionauth.StoredCredential{}, subscriptionSyncUpstreamError(ctx, subscriptionauth.ProviderGrok)
		}
		update.AccessToken = strings.TrimSpace(tokens.AccessToken)
		if refresh := strings.TrimSpace(tokens.RefreshToken); refresh != "" {
			update.RefreshToken = refresh
		}
		if idToken := strings.TrimSpace(tokens.IDToken); idToken != "" {
			update.IDToken = idToken
		}
		if tokenType := strings.TrimSpace(tokens.TokenType); tokenType != "" {
			update.TokenType = tokenType
		}
		update.ExpiresAt = strings.TrimSpace(tokens.ExpiresAt)
		if email := strings.TrimSpace(tokens.Email); email != "" {
			update.Email = email
		}
		if subject := strings.TrimSpace(tokens.Subject); subject != "" {
			update.Subject = subject
		}

	case subscriptionauth.ProviderKimi:
		if strings.TrimSpace(item.RefreshToken) == "" {
			return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusBadRequest, message: "Kimi 账号缺少 refresh token，无法刷新"}
		}
		client := kimiauth.New(nil, config.Version, item.DeviceID)
		tokens, err := runSubscriptionSyncCall(ctx, func() (*kimiauth.TokenData, error) {
			return client.RefreshToken(ctx, item.RefreshToken)
		})
		if err != nil || tokens == nil {
			return subscriptionauth.StoredCredential{}, subscriptionSyncUpstreamError(ctx, subscriptionauth.ProviderKimi)
		}
		update.AccessToken = strings.TrimSpace(tokens.AccessToken)
		if refresh := strings.TrimSpace(tokens.RefreshToken); refresh != "" {
			update.RefreshToken = refresh
		}
		if tokenType := strings.TrimSpace(tokens.TokenType); tokenType != "" {
			update.TokenType = tokenType
		}
		update.ExpiresAt = strings.TrimSpace(tokens.ExpiresAt)
		if scope := strings.TrimSpace(tokens.Scope); scope != "" {
			update.Scope = scope
		}
		if deviceID := strings.TrimSpace(tokens.DeviceID); deviceID != "" {
			update.DeviceID = deviceID
		} else {
			update.DeviceID = client.DeviceID()
		}

	default:
		return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusBadRequest, message: "不支持的订阅账号 Provider"}
	}
	if strings.TrimSpace(update.AccessToken) == "" {
		return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusBadRequest, message: subscriptionProviderLabel(item.Provider) + " 账号缺少可用的 access token"}
	}
	updated, err := store.UpdateTokens(item.ID, update)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusNotFound, message: subscriptionProviderLabel(item.Provider) + " 账号不存在"}
		}
		return subscriptionauth.StoredCredential{}, &subscriptionAccountHandlerError{status: http.StatusInternalServerError, message: "保存订阅账号刷新结果失败"}
	}
	return updated, nil
}

func (s *Server) deleteSubscriptionAccount(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	provider, err := subscriptionAccountProvider(chi.URLParam(r, "provider"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "订阅账号 ID 不能为空")
		return
	}
	store, err := s.nativeSubscriptionCredentialStore(provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	credentialDeleted := false
	item, getErr := store.GetByID(id)
	switch {
	case getErr == nil && strings.EqualFold(strings.TrimSpace(item.Provider), provider):
		if err := store.Delete(id); err == nil {
			credentialDeleted = true
		} else if !errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "删除订阅凭据失败")
			return
		}
	case errors.Is(getErr, os.ErrNotExist), getErr == nil:
	case getErr != nil:
		writeError(w, http.StatusInternalServerError, "读取订阅账号失败")
		return
	}

	statsDeleted := true
	grantDeleted := true
	if s.store != nil {
		if err := s.store.DeleteProviderAccountStats(r.Context(), provider, id); err != nil {
			statsDeleted = false
		}
		if err := s.store.DeleteGatewayAccountGrant(r.Context(), provider, id); err != nil {
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
		response["warning"] = "订阅凭据已删除，但账号统计或 Gateway 授权清理失败；可安全重试 DELETE 完成清理"
		writeJSON(w, http.StatusMultiStatus, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) nativeSubscriptionCredentialStore(provider string) (*subscriptionauth.Store, error) {
	provider, err := subscriptionAccountProvider(provider)
	if err != nil {
		return nil, err
	}
	path := subscriptionauth.DefaultStoreDir(s.configSnapshot().Paths.HomeDir, provider)
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("Autoto HomeDir 未配置，无法保存订阅凭据")
	}
	return subscriptionauth.NewStore(path), nil
}

func (s *Server) ensureNativeSubscriptionProvider(provider string) error {
	provider, err := subscriptionAccountProvider(provider)
	if err != nil {
		return err
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	return s.ensureNativeSubscriptionProviderLocked(provider)
}

func (s *Server) ensureNativeSubscriptionProviderLocked(provider string) error {
	provider, err := subscriptionAccountProvider(provider)
	if err != nil {
		return err
	}
	cfg := s.configSnapshot()
	native := config.NormalizeProviderConfig(config.ProviderConfig{Name: provider, Type: provider})
	found := false
	primaryNameOccupied := false
	for _, existing := range cfg.Providers.Instances {
		if strings.EqualFold(strings.TrimSpace(existing.Type), provider) {
			native = config.NormalizeProviderConfig(existing)
			found = true
			break
		}
		if strings.EqualFold(strings.TrimSpace(existing.Name), provider) {
			primaryNameOccupied = true
		}
	}
	if !found && primaryNameOccupied {
		base := provider + "-oauth"
		for suffix := 1; ; suffix++ {
			candidate := base
			if suffix > 1 {
				candidate = fmt.Sprintf("%s-%d", base, suffix)
			}
			occupied := false
			for _, existing := range cfg.Providers.Instances {
				if strings.EqualFold(strings.TrimSpace(existing.Name), candidate) {
					occupied = true
					break
				}
			}
			if !occupied {
				native.Name = candidate
				break
			}
		}
	}
	if native.Disabled {
		s.unregisterProvider(native.Name)
		if s.providers != nil {
			s.providers.SetDefaultFromConfig(cfg.Agent.DefaultModel, cfg.Providers.Instances)
		}
		return nil
	}
	if !found {
		cfg.Providers.Instances = upsertServerProvider(cfg.Providers.Instances, native)
		if _, err := s.persistProviderConfig(s.configPathSnapshot(), cfg); err != nil {
			return fmt.Errorf("保存 %s Provider 配置失败", subscriptionProviderLabel(provider))
		}
	}
	if err := s.registerProvider(native); err != nil {
		return fmt.Errorf("注册 %s Provider 失败", subscriptionProviderLabel(provider))
	}
	if s.providers != nil {
		s.providers.SetDefaultFromConfig(cfg.Agent.DefaultModel, cfg.Providers.Instances)
	}
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
	return nil
}

func decodeSubscriptionAccountJSON(r *http.Request, target any) error {
	if r == nil || r.Body == nil {
		return errors.New("订阅账号内容无效")
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, subscriptionAccountRequestMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > subscriptionAccountRequestMaxBytes {
		return errors.New("订阅账号内容无效或超过大小限制")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("订阅账号内容无效")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("订阅账号内容必须是单个 JSON 对象")
	}
	return nil
}

func subscriptionAccountProvider(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	switch provider {
	case config.ProviderTypeGemini, config.ProviderTypeGrok, config.ProviderTypeKimi:
		return provider, nil
	default:
		return "", errors.New("订阅账号 Provider 仅支持 gemini、grok 或 kimi")
	}
}

func subscriptionProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case subscriptionauth.ProviderGemini:
		// User-facing platform name; the provider id stays "gemini".
		return "Antigravity"
	case subscriptionauth.ProviderGrok:
		return "Grok"
	case subscriptionauth.ProviderKimi:
		return "Kimi"
	default:
		return "订阅"
	}
}

func subscriptionTokenUpdateFromCredential(credential subscriptionauth.Credential) subscriptionauth.TokenUpdate {
	return subscriptionauth.TokenUpdate{
		AccessToken:   credential.AccessToken,
		RefreshToken:  credential.RefreshToken,
		IDToken:       credential.IDToken,
		TokenType:     credential.TokenType,
		ExpiresAt:     credential.ExpiresAt,
		Email:         credential.Email,
		Subject:       credential.Subject,
		Scope:         credential.Scope,
		ProjectID:     credential.ProjectID,
		DeviceID:      credential.DeviceID,
		TokenEndpoint: credential.TokenEndpoint,
	}
}

func subscriptionCredentialNeedsRefresh(credential subscriptionauth.Credential, lead time.Duration) (bool, error) {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return true, nil
	}
	raw := strings.TrimSpace(credential.ExpiresAt)
	if raw == "" {
		return false, nil
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return false, err
	}
	return !time.Now().Before(expiresAt.Add(-lead)), nil
}

func subscriptionSyncUpstreamError(ctx context.Context, provider string) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return &subscriptionAccountHandlerError{status: http.StatusBadGateway, message: subscriptionProviderLabel(provider) + " 账号刷新或验证失败"}
}

type subscriptionSyncCallResult[T any] struct {
	value T
	err   error
}

func runSubscriptionSyncCall[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var zero T
	if ctx == nil || call == nil {
		return zero, errors.New("订阅账号同步调用无效")
	}
	result := make(chan subscriptionSyncCallResult[T], 1)
	go func() {
		value, err := call()
		result <- subscriptionSyncCallResult[T]{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case completed := <-result:
		return completed.value, completed.err
	}
}
