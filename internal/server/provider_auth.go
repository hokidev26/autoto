package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/codexauth"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

type importAuthFileRequest struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

const (
	maxProviderAuthImportBytes = 8 << 20
	// An auth-file listing is metadata, so this is generous rather than tight;
	// the point is that a ceiling exists at all.
	maxProviderManagementResponseBytes = 16 << 20
	maxProviderAuthImportAccounts      = 200
)

type providerAuthImportFile struct {
	Filename string
	Content  []byte
}

type providerAuthImportPlan struct {
	Format  string
	Files   []providerAuthImportFile
	Skipped int
}

type providerAuthImportResponse struct {
	Status   string   `json:"status"`
	Format   string   `json:"format"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped,omitempty"`
	Files    []string `json:"files,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

type codexOAuthAccountResponse struct {
	codexauth.AccountSummary
	Stats *db.ProviderAccountStats `json:"stats,omitempty"`
	Quota *codexauth.QuotaSnapshot `json:"quota,omitempty"`
	Usage codexAccountUsage        `json:"usage"`
}

type codexAccountUsageWindow struct {
	RequestCount int64   `json:"requestCount"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	TotalTokens  int64   `json:"totalTokens"`
	CostUSD      float64 `json:"costUsd"`
}

type codexAccountUsage struct {
	Total      codexAccountUsageWindow `json:"total"`
	Last5Hours codexAccountUsageWindow `json:"last5Hours"`
	Last7Days  codexAccountUsageWindow `json:"last7Days"`
}

func normalizeCodexAccountUsage(usage db.ProviderAccountUsage) codexAccountUsage {
	window := func(value db.ProviderAccountUsageWindow) codexAccountUsageWindow {
		return codexAccountUsageWindow{
			RequestCount: value.RequestCount,
			InputTokens:  value.InputTokens,
			OutputTokens: value.OutputTokens,
			TotalTokens:  value.TotalTokens(),
			CostUSD:      value.CostUSD,
		}
	}
	return codexAccountUsage{
		Total:      window(usage.Total),
		Last5Hours: window(usage.Last5Hours),
		Last7Days:  window(usage.Last7Days),
	}
}

type codexOAuthAccountsResponse struct {
	Accounts []codexOAuthAccountResponse `json:"accounts"`
	Count    int                         `json:"count"`
}

type codexOAuthAccountPatchRequest struct {
	Alias    *string `json:"alias"`
	Priority *int    `json:"priority"`
	Disabled *bool   `json:"disabled"`
}

// listCodexOAuthAccounts reads Autoto's own credential store. It never calls a
// CLIProxyAPI management endpoint and never serializes token material.
func (s *Server) listCodexOAuthAccounts(w http.ResponseWriter, r *http.Request) {
	store, err := s.nativeCodexCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	accounts, err := store.ListAccounts()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	statsByID := map[string]db.ProviderAccountStats{}
	quotasByID := map[string]*codexauth.QuotaSnapshot{}
	usageStarts := map[string]db.ProviderAccountUsageWindowStarts{}
	usageByID := map[string]db.ProviderAccountUsage{}
	if s.store != nil {
		if stats, statsErr := s.store.ListProviderAccountStats(r.Context(), codexauth.DefaultProviderName); statsErr == nil {
			statsByID = stats
			for id, item := range stats {
				if len(item.QuotaSnapshotJSON) == 0 {
					continue
				}
				var quota codexauth.QuotaSnapshot
				if json.Unmarshal(item.QuotaSnapshotJSON, &quota) != nil {
					continue
				}
				quotaCopy := quota
				quotasByID[id] = &quotaCopy
				last5, last7 := providers.CodexLocalUsageWindowStarts(&quotaCopy, s.now())
				usageStarts[id] = db.ProviderAccountUsageWindowStarts{Last5Hours: last5, Last7Days: last7}
			}
		}
		accountIDs := make([]string, 0, len(accounts))
		for _, account := range accounts {
			accountIDs = append(accountIDs, account.ID)
		}
		var usageErr error
		usageByID, usageErr = s.store.ListProviderAccountUsage(r.Context(), codexauth.DefaultProviderName, accountIDs, s.now(), usageStarts)
		if usageErr != nil {
			writeError(w, http.StatusInternalServerError, "Codex 帳號用量統計失敗")
			return
		}
	}
	response := make([]codexOAuthAccountResponse, 0, len(accounts))
	for _, account := range accounts {
		item := codexOAuthAccountResponse{AccountSummary: account, Usage: normalizeCodexAccountUsage(usageByID[account.ID]), Quota: quotasByID[account.ID]}
		if stats, ok := statsByID[account.ID]; ok {
			statsCopy := stats
			item.Stats = &statsCopy
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, codexOAuthAccountsResponse{Accounts: response, Count: len(response)})
}

// exportCodexOAuthAccount is deliberately stricter than the normal sensitive
// provider routes: credential downloads are local-only, even when a full
// remote session exists. The response is an attachment and is never logged or
// included in an API envelope.
func (s *Server) exportCodexOAuthAccount(w http.ResponseWriter, r *http.Request) {
	if auth := s.remoteAccessAuthentication(r); auth.Remote {
		writeError(w, http.StatusForbidden, "Codex 憑證只能在本機匯出")
		return
	}
	if r.Header.Get("X-Autoto-Confirm") != "export-codex-account" {
		writeError(w, http.StatusBadRequest, "匯出 Codex 憑證需要明確確認")
		return
	}
	store, err := s.nativeCodexCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	document, err := store.ExportByID(chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Codex 帳號不存在")
		} else {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
		}
		return
	}
	filename := strings.TrimSpace(document.Filename)
	if filename == "" {
		filename = "codex-auth.json"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".json") {
		filename += ".json"
	}
	contentDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if contentDisposition == "" {
		contentDisposition = `attachment; filename="codex-auth.json"`
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition)
	w.Header().Set("Content-Length", strconv.Itoa(len(document.Content)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(document.Content)
}

func (s *Server) patchCodexOAuthAccount(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	store, err := s.nativeCodexCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	var request codexOAuthAccountPatchRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "帳號更新內容無效")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "帳號更新內容必須是單個 JSON 物件")
		return
	}
	if request.Alias == nil && request.Priority == nil && request.Disabled == nil {
		writeError(w, http.StatusBadRequest, "至少提供 alias、priority 或 disabled 之一")
		return
	}
	item, err := store.UpdateMetadata(chi.URLParam(r, "id"), codexauth.MetadataUpdate{Alias: request.Alias, Priority: request.Priority, Disabled: request.Disabled})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Codex 帳號不存在")
		} else {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, codexOAuthAccountResponse{AccountSummary: codexauth.Summary(item)})
}

func (s *Server) refreshCodexOAuthAccount(w http.ResponseWriter, r *http.Request) {
	provider, err := s.nativeCodexProvider()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), codexOAuthSyncTimeout)
	defer cancel()
	response, status, err := s.syncCodexOAuthAccount(ctx, provider, chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) resetCodexOAuthQuota(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Autoto-Confirm") != "reset-codex-quota" {
		writeError(w, http.StatusBadRequest, "重置 Codex 額度需要明確確認")
		return
	}
	provider, err := s.nativeCodexProvider()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), codexOAuthSyncTimeout)
	defer cancel()
	id := chi.URLParam(r, "id")
	if err := provider.ConsumeRateLimitResetCredit(ctx, id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "Codex 帳號不存在")
			return
		}
		s.writeRequestError(w, r, http.StatusBadGateway, err)
		return
	}
	response, status, err := s.syncCodexOAuthAccount(ctx, provider, id)
	if err != nil {
		s.writeRequestError(w, r, status, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) deleteCodexOAuthAccount(w http.ResponseWriter, r *http.Request) {
	store, err := s.nativeCodexCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	outcome, err := s.deleteCodexOAuthAccountCore(r.Context(), store, chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	response := map[string]any{
		"status":             "ok",
		"id":                 outcome.ID,
		"credential_deleted": outcome.CredentialDeleted,
		"stats_deleted":      outcome.StatsDeleted,
		"grant_deleted":      outcome.GrantDeleted,
		"already_missing":    outcome.AlreadyMissing,
		"cleanup_pending":    outcome.CleanupPending,
		"retryable":          outcome.Retryable,
	}
	if outcome.Warning != "" {
		response["status"] = "partial"
		response["warning"] = outcome.Warning
		writeJSON(w, http.StatusMultiStatus, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) nativeCodexProvider() (*providers.CodexProvider, error) {
	// Provider creation may persist a missing built-in provider. Keep the global
	// config transaction lock ahead of the runtime registry lock, matching every
	// other read-modify-save-publish path.
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	if provider, ok := s.codexProviderConfig(); ok && provider.Disabled {
		s.unregisterProvider(codexauth.DefaultProviderName)
		return nil, errors.New("Codex Provider 已停用")
	}
	if s.providers != nil {
		if provider, ok := s.providers.Get(codexauth.DefaultProviderName); ok {
			if codexProvider, ok := provider.(*providers.CodexProvider); ok {
				if s.store != nil {
					codexProvider.SetAccountTelemetry(s.store)
				}
				return codexProvider, nil
			}
		}
	}
	if err := s.ensureNativeCodexProviderLocked(); err != nil {
		return nil, err
	}
	if provider, ok := s.providers.Get(codexauth.DefaultProviderName); ok {
		if codexProvider, ok := provider.(*providers.CodexProvider); ok {
			if s.store != nil {
				codexProvider.SetAccountTelemetry(s.store)
			}
			return codexProvider, nil
		}
	}
	return nil, errors.New("Codex Provider 不可用")
}

func (s *Server) codexProviderConfig() (config.ProviderConfig, bool) {
	for _, provider := range s.configSnapshot().Providers.Instances {
		if provider.Name == codexauth.DefaultProviderName {
			return config.NormalizeProviderConfig(provider), true
		}
	}
	return config.ProviderConfig{}, false
}

// importCodexOAuthCredentials normalizes sub2api/Codex JSON and persists each
// account directly in Autoto's local credential store. The native codex
// provider observes the same store and can use imported credentials immediately.
func (s *Server) importCodexOAuthCredentials(w http.ResponseWriter, r *http.Request) {
	var req importAuthFileRequest
	if err := decodeImportAuthFileRequest(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "請貼上 JSON 或 token 內容")
		return
	}
	plan, err := buildProviderAuthImportPlan(req.Filename, content, s.now())
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	store, err := s.nativeCodexCredentialStore()
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	documents := make([]codexauth.ImportDocument, 0, len(plan.Files))
	for _, file := range plan.Files {
		documents = append(documents, codexauth.ImportDocument{Filename: file.Filename, Content: file.Content})
	}
	stored, err := store.Import(documents)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	if err := s.ensureNativeCodexProvider(); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, providerAuthImportResponse{
		Status:   "ok",
		Format:   plan.Format,
		Imported: stored.Imported,
		Skipped:  plan.Skipped + stored.Skipped,
		Files:    stored.Files,
	})
}

func (s *Server) nativeCodexCredentialStore() (*codexauth.Store, error) {
	path := codexauth.DefaultStoreDir(s.configSnapshot().Paths.HomeDir)
	s.codexCredentialsMu.Lock()
	defer s.codexCredentialsMu.Unlock()
	if s.codexCredentials == nil || strings.TrimSpace(s.codexCredentials.Dir()) == "" {
		if path == "" {
			return nil, errors.New("Autoto HomeDir 未設定，無法儲存 Codex 憑證")
		}
		s.codexCredentials = codexauth.NewStore(path)
	}
	return s.codexCredentials, nil
}

func (s *Server) ensureNativeCodexProvider() error {
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	return s.ensureNativeCodexProviderLocked()
}

// ensureNativeCodexProviderLocked makes the native Codex adapter available
// only when its persisted provider is enabled. Credential import must never
// silently reactivate a disabled provider.
func (s *Server) ensureNativeCodexProviderLocked() error {
	cfg := s.configSnapshot()
	provider := config.ProviderConfig{
		Name:    codexauth.DefaultProviderName,
		Type:    config.ProviderTypeCodex,
		BaseURL: codexauth.DefaultBaseURL,
		Model:   codexauth.DefaultModel,
	}
	found := false
	for _, existing := range cfg.Providers.Instances {
		if existing.Name != codexauth.DefaultProviderName {
			continue
		}
		if existing.Type != config.ProviderTypeCodex {
			return fmt.Errorf("provider %s 已被其他協定佔用", codexauth.DefaultProviderName)
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
		if persisted, err := s.persistProviderConfig(s.configPathSnapshot(), cfg); err != nil {
			return fmt.Errorf("儲存 Codex Provider 設定失敗：%w", err)
		} else if !persisted {
			// No config path is a supported in-memory mode; runtime registration
			// still proceeds without claiming a disk write happened.
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

// listProviderAuthFiles serves management auth files for the provider named in
// the route. Only profiles that declare auth-file management are supported.
func (s *Server) listProviderAuthFiles(w http.ResponseWriter, r *http.Request) {
	s.listProviderAuthFilesForName(w, r, strings.TrimSpace(chi.URLParam(r, "name")))
}

// listCLIProxyAPIAuthFiles preserves the legacy route while delegating to the
// profile-aware handler.
func (s *Server) listCLIProxyAPIAuthFiles(w http.ResponseWriter, r *http.Request) {
	s.listProviderAuthFilesForName(w, r, config.ProviderProfileCLIProxyAPI)
}

func (s *Server) listProviderAuthFilesForName(w http.ResponseWriter, r *http.Request, name string) {
	if name == codexauth.DefaultProviderName {
		if !s.requireSensitiveLocalToken(w, r) {
			return
		}
		s.listCodexOAuthAccounts(w, r)
		return
	}
	provider, err := s.authFileProvider(name)
	if err != nil {
		s.writeRequestError(w, r, http.StatusNotFound, err)
		return
	}
	body, err := s.providerManagementRequest(r.Context(), provider, http.MethodGet, "/auth-files", nil, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, friendlyProviderManagementError(provider, err))
		return
	}
	writeRawJSON(w, body)
}

// importProviderAuthFile accepts an auth-file upload for the route provider.
func (s *Server) importProviderAuthFile(w http.ResponseWriter, r *http.Request) {
	s.importProviderAuthFileForName(w, r, strings.TrimSpace(chi.URLParam(r, "name")))
}

// importCLIProxyAPIAuthFile preserves the legacy route while delegating to the
// profile-aware handler.
func (s *Server) importCLIProxyAPIAuthFile(w http.ResponseWriter, r *http.Request) {
	s.importProviderAuthFileForName(w, r, config.ProviderProfileCLIProxyAPI)
}

func (s *Server) importProviderAuthFileForName(w http.ResponseWriter, r *http.Request, name string) {
	if name == codexauth.DefaultProviderName {
		if !s.requireSensitiveLocalToken(w, r) {
			return
		}
		s.importCodexOAuthCredentials(w, r)
		return
	}
	provider, err := s.authFileProvider(name)
	if err != nil {
		s.writeRequestError(w, r, http.StatusNotFound, err)
		return
	}
	var req importAuthFileRequest
	if err := decodeImportAuthFileRequest(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "請貼上 JSON 或 token 內容")
		return
	}
	plan, err := buildProviderAuthImportPlan(req.Filename, content, time.Now())
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	result := providerAuthImportResponse{
		Status:  "ok",
		Format:  plan.Format,
		Skipped: plan.Skipped,
		Files:   make([]string, 0, len(plan.Files)),
	}
	for _, file := range plan.Files {
		if _, err := s.uploadProviderAuthFile(r.Context(), provider, file); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s：%s", file.Filename, friendlyProviderManagementError(provider, err)))
			continue
		}
		result.Imported++
		result.Files = append(result.Files, file.Filename)
	}
	if result.Imported == 0 && len(result.Errors) > 0 {
		writeError(w, http.StatusBadGateway, result.Errors[0])
		return
	}
	status := http.StatusOK
	if len(result.Errors) > 0 {
		result.Status = "partial"
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, result)
}

func decodeImportAuthFileRequest(r *http.Request, dst *importAuthFileRequest) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxProviderAuthImportBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxProviderAuthImportBytes {
		return fmt.Errorf("匯入內容超過 %d MiB 限制", maxProviderAuthImportBytes>>20)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
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

func (s *Server) uploadProviderAuthFile(ctx context.Context, provider config.ProviderSummary, file providerAuthImportFile) ([]byte, error) {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filepath.Base(file.Filename))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(file.Content); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return s.providerManagementRequest(ctx, provider, http.MethodPost, "/auth-files", &payload, writer.FormDataContentType())
}

func buildProviderAuthImportPlan(filename, content string, now time.Time) (providerAuthImportPlan, error) {
	var value any
	if err := json.Unmarshal([]byte(content), &value); err == nil {
		return buildProviderAuthJSONImportPlan(filename, value, now)
	} else if trimmed := strings.TrimSpace(content); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return providerAuthImportPlan{}, fmt.Errorf("JSON 格式無效：%w", err)
	}
	return buildProviderAuthTokenImportPlan(filename, content, now)
}

func buildProviderAuthJSONImportPlan(filename string, value any, now time.Time) (providerAuthImportPlan, error) {
	format := "codex"
	root := map[string]any{}
	accounts := make([]any, 0, 1)
	switch typed := value.(type) {
	case map[string]any:
		root = typed
		if rawAccounts, ok := typed["accounts"]; ok {
			items, ok := rawAccounts.([]any)
			if !ok {
				return providerAuthImportPlan{}, errors.New("accounts 必須是陣列")
			}
			accounts = append(accounts, items...)
			if source := authImportString(typed, "format"); source != "" {
				format = source
			} else {
				format = "accounts"
			}
		} else {
			accounts = append(accounts, typed)
		}
	case []any:
		format = "array"
		accounts = append(accounts, typed...)
	default:
		return providerAuthImportPlan{}, errors.New("JSON 頂層必須是物件或帳號陣列")
	}
	return buildProviderAuthAccountPlan(filename, format, root, accounts, now)
}

func buildProviderAuthTokenImportPlan(filename, content string, now time.Time) (providerAuthImportPlan, error) {
	accounts := make([]any, 0)
	for _, line := range strings.FieldsFunc(content, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		fields := []string{strings.TrimSpace(line)}
		if strings.Contains(line, "----") {
			fields = strings.Split(line, "----")
		}
		account := map[string]any{}
		for _, raw := range fields {
			field := strings.Trim(strings.TrimSpace(raw), "\"'")
			switch {
			case strings.HasPrefix(field, "rt_"):
				account["refresh_token"] = field
			case authImportLooksLikeJWT(field):
				account["access_token"] = field
			case authImportLooksLikeMSAToken(field):
				// Microsoft MSA refresh token (M.C... format used by OpenAI/Codex OAuth)
				account["refresh_token"] = field
			case authImportLooksLikeUUID(field):
				// UUID is the OpenAI account_id / chatgpt_account_id
				account["account_id"] = field
			case strings.Contains(field, "@") && !strings.ContainsAny(field, " \t"):
				account["email"] = field
			}
		}
		if authImportString(account, "access_token", "refresh_token") != "" {
			accounts = append(accounts, account)
		}
	}
	if len(accounts) == 0 {
		return providerAuthImportPlan{}, errors.New("未識別到可匯入的 Codex JSON、access_token 或 refresh_token")
	}
	return buildProviderAuthAccountPlan(filename, "token-list", map[string]any{}, accounts, now)
}

func buildProviderAuthAccountPlan(filename, format string, root map[string]any, accounts []any, now time.Time) (providerAuthImportPlan, error) {
	if len(accounts) == 0 {
		return providerAuthImportPlan{}, errors.New("匯入內容中沒有帳號")
	}
	if len(accounts) > maxProviderAuthImportAccounts {
		return providerAuthImportPlan{}, fmt.Errorf("單次最多匯入 %d 個帳號", maxProviderAuthImportAccounts)
	}
	plan := providerAuthImportPlan{Format: format, Files: make([]providerAuthImportFile, 0, len(accounts))}
	seen := map[string]struct{}{}
	for index, raw := range accounts {
		account, ok := raw.(map[string]any)
		if !ok {
			return providerAuthImportPlan{}, fmt.Errorf("第 %d 個帳號必須是 JSON 物件", index+1)
		}
		normalized, identity, slug, err := normalizeCodexAuthAccount(account, root, now)
		if err != nil {
			return providerAuthImportPlan{}, fmt.Errorf("第 %d 個帳號無效：%w", index+1, err)
		}
		if _, exists := seen[identity]; exists {
			plan.Skipped++
			continue
		}
		seen[identity] = struct{}{}
		data, err := json.MarshalIndent(normalized, "", "  ")
		if err != nil {
			return providerAuthImportPlan{}, fmt.Errorf("第 %d 個帳號無法序列化", index+1)
		}
		plan.Files = append(plan.Files, providerAuthImportFile{
			Filename: authImportFilename(filename, slug, index, now),
			Content:  append(data, '\n'),
		})
	}
	if len(plan.Files) == 0 {
		return providerAuthImportPlan{}, errors.New("沒有新的可匯入帳號")
	}
	return plan, nil
}

func normalizeCodexAuthAccount(account, root map[string]any, now time.Time) (map[string]any, string, string, error) {
	credentials := authImportObject(account["credentials"])
	if credentials == nil {
		credentials = account
	}
	tokens := authImportObject(account["tokens"])
	extra := authImportObject(account["extra"])
	maps := []map[string]any{credentials, tokens, account}
	accessToken := authImportStringFromMaps(maps, "access_token", "accessToken")
	refreshToken := authImportStringFromMaps(maps, "refresh_token", "refreshToken")
	idToken := authImportStringFromMaps(maps, "id_token", "idToken")
	if accessToken == "" && refreshToken == "" {
		return nil, "", "", errors.New("缺少 access_token 或 refresh_token")
	}
	platform := strings.ToLower(authImportString(account, "platform", "provider"))
	if platform != "" && platform != "openai" && platform != "codex" {
		return nil, "", "", fmt.Errorf("platform %q 不是 OpenAI/Codex", platform)
	}
	jwt := authImportJWTMetadata(accessToken)
	idJWT := authImportJWTMetadata(idToken)
	if jwt.AccountID == "" {
		jwt.AccountID = idJWT.AccountID
	}
	if jwt.Email == "" {
		jwt.Email = idJWT.Email
	}
	if jwt.PlanType == "" {
		jwt.PlanType = idJWT.PlanType
	}
	if jwt.ExpiresAt == 0 {
		jwt.ExpiresAt = idJWT.ExpiresAt
	}
	email := authImportStringFromMaps([]map[string]any{credentials, account, extra}, "email")
	if email == "" {
		email = jwt.Email
	}
	accountID := authImportStringFromMaps(maps, "account_id", "accountID", "chatgpt_account_id")
	if accountID == "" {
		accountID = jwt.AccountID
	}
	if accountID == "" {
		accountID = authImportString(extra, "source_target_id", "source_workspace_id")
	}
	if accountID == "" {
		accountID = authImportString(root, "workspace_id")
	}
	planType := authImportStringFromMaps(maps, "plan_type", "planType")
	if planType == "" {
		planType = jwt.PlanType
	}
	expired := authImportTimeFromMaps(maps, "expired")
	if expired == "" {
		expired = authImportTimeFromMaps(maps, "expires_at", "expiresAt")
	}
	if expired == "" && jwt.ExpiresAt > 0 {
		expired = time.Unix(jwt.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	lastRefresh := authImportTimeFromMaps([]map[string]any{extra, account, root}, "last_refresh", "lastRefresh", "exported_at")
	if lastRefresh == "" {
		lastRefresh = now.UTC().Format(time.RFC3339)
	}
	disabled, _ := authImportBoolFromMaps([]map[string]any{account, credentials}, "disabled")
	alias := authImportStringFromMaps([]map[string]any{account, credentials}, "alias", "name")
	priority := authImportIntFromMaps([]map[string]any{account, credentials}, "priority")
	websockets, websocketSet := authImportBoolFromMaps([]map[string]any{account, extra}, "websockets", "openai_oauth_responses_websockets_v2_enabled")
	if !websocketSet {
		websockets = false
	}
	standard := account["credentials"] == nil && account["tokens"] == nil && (authImportString(account, "access_token", "refresh_token") != "")
	normalized := map[string]any{}
	if standard {
		for key, value := range account {
			normalized[key] = value
		}
	}
	normalized["type"] = "codex"
	normalized["disabled"] = disabled
	if alias != "" {
		normalized["alias"] = alias
	}
	if priority > 0 {
		normalized["priority"] = priority
	}
	normalized["websockets"] = websockets
	authImportSetString(normalized, "access_token", accessToken)
	authImportSetString(normalized, "refresh_token", refreshToken)
	authImportSetString(normalized, "id_token", idToken)
	authImportSetString(normalized, "email", email)
	authImportSetString(normalized, "account_id", accountID)
	authImportSetString(normalized, "plan_type", planType)
	authImportSetString(normalized, "expired", expired)
	authImportSetString(normalized, "last_refresh", lastRefresh)
	hash := sha256.Sum256([]byte(accessToken + "\x00" + refreshToken))
	identity := "token:" + fmt.Sprintf("%x", hash[:12])
	if accountID != "" {
		identity = "account:" + strings.ToLower(accountID)
	} else if email != "" {
		identity = "email:" + strings.ToLower(email)
	}
	name := authImportString(account, "name", "alias")
	slug := authImportAccountSlug(name, accountID, email, fmt.Sprintf("%x", hash[:6]))
	return normalized, identity, slug, nil
}

type authImportJWTInfo struct {
	AccountID string
	Email     string
	PlanType  string
	ExpiresAt int64
}

func authImportJWTMetadata(token string) authImportJWTInfo {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return authImportJWTInfo{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return authImportJWTInfo{}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return authImportJWTInfo{}
	}
	auth := authImportObject(claims["https://api.openai.com/auth"])
	profile := authImportObject(claims["https://api.openai.com/profile"])
	return authImportJWTInfo{
		AccountID: authImportString(auth, "chatgpt_account_id", "account_id"),
		Email:     authImportString(profile, "email"),
		PlanType:  authImportString(auth, "chatgpt_plan_type", "plan_type"),
		ExpiresAt: authImportUnix(claims["exp"]),
	}
}

func authImportFilename(filename, slug string, index int, now time.Time) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(filename)), filepath.Ext(filename))
	base = authImportSlug(base)
	if base == "" {
		base = "autoto-codex"
	}
	if slug == "" {
		slug = fmt.Sprintf("%d-%02d", now.Unix(), index+1)
	}
	name := strings.Trim(base+"-"+slug, "-.")
	if len(name) > 110 {
		name = strings.Trim(name[:110], "-.")
	}
	return name + ".json"
}

func authImportAccountSlug(name, accountID, email, fallback string) string {
	if value := authImportSlug(name); value != "" {
		return value
	}
	if value := authImportSlug(accountID); value != "" {
		if len(value) > 24 {
			value = value[:24]
		}
		return value
	}
	if at := strings.Index(email, "@"); at > 0 {
		if value := authImportSlug(email[:at]); value != "" {
			return value + "-" + fallback[:8]
		}
	}
	return fallback
}

func authImportSlug(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func authImportObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func authImportString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			switch typed := value.(type) {
			case string:
				if text := strings.TrimSpace(typed); text != "" {
					return text
				}
			case json.Number:
				return typed.String()
			}
		}
	}
	return ""
}

func authImportStringFromMaps(objects []map[string]any, keys ...string) string {
	for _, object := range objects {
		if object == nil {
			continue
		}
		if value := authImportString(object, keys...); value != "" {
			return value
		}
	}
	return ""
}

func authImportIntFromMaps(objects []map[string]any, keys ...string) int {
	for _, object := range objects {
		if object == nil {
			continue
		}
		for _, key := range keys {
			switch value := object[key].(type) {
			case float64:
				return int(value)
			case json.Number:
				parsed, _ := strconv.Atoi(value.String())
				return parsed
			case string:
				parsed, _ := strconv.Atoi(strings.TrimSpace(value))
				return parsed
			}
		}
	}
	return 0
}

func authImportBoolFromMaps(objects []map[string]any, keys ...string) (bool, bool) {
	for _, object := range objects {
		if object == nil {
			continue
		}
		for _, key := range keys {
			if value, ok := object[key]; ok {
				switch typed := value.(type) {
				case bool:
					return typed, true
				case string:
					parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
					if err == nil {
						return parsed, true
					}
				}
			}
		}
	}
	return false, false
}

func authImportTimeFromMaps(objects []map[string]any, keys ...string) string {
	for _, object := range objects {
		if object == nil {
			continue
		}
		for _, key := range keys {
			if value, ok := object[key]; ok {
				if timestamp := authImportTime(value); timestamp != "" {
					return timestamp
				}
			}
		}
	}
	return ""
}

func authImportTime(value any) string {
	if seconds := authImportUnix(value); seconds > 0 {
		return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	return text
}

func authImportUnix(value any) int64 {
	var seconds int64
	switch typed := value.(type) {
	case float64:
		seconds = int64(typed)
	case float32:
		seconds = int64(typed)
	case int:
		seconds = int64(typed)
	case int64:
		seconds = typed
	case json.Number:
		seconds, _ = typed.Int64()
	case string:
		seconds, _ = strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
	}
	if seconds > 1_000_000_000_000 {
		seconds /= 1000
	}
	return seconds
}

func authImportSetString(object map[string]any, key, value string) {
	if value != "" {
		object[key] = value
	}
}

func authImportLooksLikeJWT(value string) bool {
	if !strings.HasPrefix(value, "eyJ") {
		return false
	}
	return len(strings.Split(value, ".")) == 3
}

// authImportLooksLikeMSAToken reports whether value looks like a Microsoft MSA
// refresh token (M.C... format produced by the OpenAI/Codex OAuth flow).
// These tokens are long (>100 chars), start with "M.C", and contain only
// URL-safe base64 characters plus dots, dashes, underscores, and exclamation
// marks (used as padding in the MSA encoding).
func authImportLooksLikeMSAToken(value string) bool {
	if len(value) < 100 || !strings.HasPrefix(value, "M.C") {
		return false
	}
	for _, r := range value {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '-' || r == '_' || r == '!' || r == '*' || r == '$') {
			return false
		}
	}
	return true
}

// authImportLooksLikeUUID reports whether value is a canonical UUID
// (8-4-4-4-12 hex digits separated by hyphens).
func authImportLooksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

func (s *Server) providerManagementRequest(ctx context.Context, provider config.ProviderSummary, method, path string, body io.Reader, contentType string) ([]byte, error) {
	base, err := providerManagementBaseURL(provider)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	var payload []byte
	if body != nil {
		payload, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}
	key, _ := cliProxyAPIManagementKeyWithSource()
	data, _, err := cliProxyAPIManagementRequestWithKey(ctx, method, endpoint, payload, contentType, key)
	return data, err
}

func cliProxyAPIManagementRequestWithKey(ctx context.Context, method, endpoint string, payload []byte, contentType, key string) ([]byte, int, error) {
	if _, err := parseCLIProxyAPIManagementURL(endpoint); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, errors.New("無法構造 CLIProxyAPI 管理請求")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Management-Key", key)
	}
	client := newCLIProxyAPIManagementHTTPClient()
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("CLIProxyAPI 管理請求失敗")
	}
	defer res.Body.Close()
	// Bounded like every other read of an outside body. This one is reachable
	// from GET /api/providers/{name}/auth-files, and the management endpoint is
	// whatever host the user configured, so an unbounded read lets a misbehaving
	// or hostile upstream exhaust memory.
	data, err := io.ReadAll(io.LimitReader(res.Body, maxProviderManagementResponseBytes+1))
	if err == nil && len(data) > maxProviderManagementResponseBytes {
		return nil, res.StatusCode, fmt.Errorf("CLIProxyAPI management response exceeded %d MiB", maxProviderManagementResponseBytes>>20)
	}
	if err != nil {
		return nil, res.StatusCode, errors.New("無法讀取 CLIProxyAPI 管理回應")
	}
	if res.StatusCode >= http.StatusMultipleChoices && res.StatusCode < http.StatusBadRequest {
		return nil, res.StatusCode, fmt.Errorf("CLIProxyAPI 管理請求拒絕重新導向：HTTP %d", res.StatusCode)
	}
	if res.StatusCode >= http.StatusMultipleChoices {
		return nil, res.StatusCode, fmt.Errorf("CLIProxyAPI management request failed: %s", res.Status)
	}
	return data, res.StatusCode, nil
}

func newCLIProxyAPIManagementHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialCLIProxyAPIManagement
	return &http.Client{
		Timeout:       30 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func dialCLIProxyAPIManagement(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("CLIProxyAPI 管理位址無效")
	}
	ips, err := cliProxyAPIManagementLoopbackIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{}
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
	}
	return nil, errors.New("無法連線 CLIProxyAPI 管理介面")
}

func cliProxyAPIManagementLoopbackIPs(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return nil, errors.New("CLIProxyAPI 管理位址必須是 loopback 主機")
		}
		return []net.IP{ip}, nil
	}
	if host != "localhost" {
		return nil, errors.New("CLIProxyAPI 管理位址必須是 loopback 主機")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("無法安全解析 CLIProxyAPI loopback 主機")
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return nil, errors.New("CLIProxyAPI 管理位址必須解析為 loopback 主機")
		}
	}
	return ips, nil
}

func parseCLIProxyAPIManagementURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("CLIProxyAPI Base URL 無效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("CLIProxyAPI Base URL 僅允許 loopback HTTP(S)")
	}
	lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := cliProxyAPIManagementLoopbackIPs(lookupCtx, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func providerManagementBaseURL(provider config.ProviderSummary) (string, error) {
	if provider.Profile != config.ProviderProfileCLIProxyAPI {
		return "", fmt.Errorf("provider %s does not support management auth files", provider.Name)
	}
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8317/v0/management"
	}
	parsed, err := parseCLIProxyAPIManagementURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = "/v0/management"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func (s *Server) authFileProvider(name string) (config.ProviderSummary, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.ProviderSummary{}, errors.New("provider name is required")
	}
	for _, provider := range s.configSnapshot().Providers.Summaries() {
		if provider.Name != name {
			continue
		}
		if provider.Profile != config.ProviderProfileCLIProxyAPI {
			return config.ProviderSummary{}, fmt.Errorf("provider %s does not support auth files", name)
		}
		return provider, nil
	}
	return config.ProviderSummary{}, fmt.Errorf("provider %s is not configured", name)
}

func (s *Server) unusedCliProxyAPIProviderSummaryMarker() (config.ProviderSummary, bool) {
	for _, provider := range s.configSnapshot().Providers.Summaries() {
		if provider.Profile == config.ProviderProfileCLIProxyAPI {
			return provider, true
		}
	}
	return config.ProviderSummary{}, false
}

const defaultCLIProxyAPIManagementKey = "autoto-local"

func cliProxyAPIManagementKey() string {
	key, _ := cliProxyAPIManagementKeyWithSource()
	return key
}

func cliProxyAPIManagementKeyWithSource() (string, bool) {
	if key := strings.TrimSpace(os.Getenv("CLIPROXYAPI_MANAGEMENT_KEY")); key != "" {
		return key, true
	}
	return defaultCLIProxyAPIManagementKey, false
}

func friendlyProviderManagementError(provider config.ProviderSummary, err error) string {
	if provider.Profile != config.ProviderProfileCLIProxyAPI {
		return "Provider 管理請求失敗：" + err.Error()
	}
	return friendlyCLIProxyAPIManagementError(err)
}

func friendlyCLIProxyAPIManagementError(err error) string {
	message := err.Error()
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "connect:"):
		return "無法連線 CLIProxyAPI 管理介面。請確認 CLIProxyAPI 已啟動並監聽 127.0.0.1:8317。"
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized"):
		return "CLIProxyAPI 管理介面認證失敗。請確認 CLIPROXYAPI_MANAGEMENT_KEY 或本機管理密碼。"
	case strings.Contains(lower, "404"):
		return "CLIProxyAPI 管理介面未啟用。請確認 config.yaml 中 remote-management.secret-key 已設定。"
	default:
		return "CLIProxyAPI 管理請求失敗：" + message
	}
}

func writeRawJSON(w http.ResponseWriter, body []byte) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"raw": string(body)})
		return
	}
	writeJSON(w, http.StatusOK, value)
}
