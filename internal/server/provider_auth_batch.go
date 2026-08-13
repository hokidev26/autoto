package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/providers"
)

const (
	maxCodexOAuthBatchAccounts      = 100
	maxCodexOAuthBatchBytes         = 64 << 10
	maxCodexOAuthImportFiles        = 50
	maxCodexOAuthImportFileBytes    = 2 << 20
	maxCodexOAuthImportBatchBytes   = 8 << 20
	maxCodexOAuthImportRequestBytes = 16 << 20
	maxCodexOAuthPriority           = 1_000_000
	codexOAuthSyncTimeout           = 30 * time.Second
)

type codexOAuthAccountsBatchRequest struct {
	IDs       []string `json:"ids"`
	Operation string   `json:"operation"`
	Priority  *int     `json:"priority,omitempty"`
}

type codexOAuthAccountsBatchResult struct {
	ID        string `json:"id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	Warning   string `json:"warning,omitempty"`
	Retryable bool   `json:"retryable"`
}

type codexOAuthAccountsBatchResponse struct {
	Status  string                          `json:"status"`
	Total   int                             `json:"total"`
	Success int                             `json:"success"`
	Failed  int                             `json:"failed"`
	Results []codexOAuthAccountsBatchResult `json:"results"`
}

type codexOAuthImportBatchRequest struct {
	Files []importAuthFileRequest `json:"files"`
}

type codexOAuthImportBatchResult struct {
	Filename string   `json:"filename"`
	Status   string   `json:"status"`
	Format   string   `json:"format,omitempty"`
	Imported int      `json:"imported,omitempty"`
	Skipped  int      `json:"skipped,omitempty"`
	Files    []string `json:"files,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type codexOAuthImportBatchResponse struct {
	Status  string                        `json:"status"`
	Total   int                           `json:"total"`
	Success int                           `json:"success"`
	Skipped int                           `json:"skipped"`
	Failed  int                           `json:"failed"`
	Results []codexOAuthImportBatchResult `json:"results"`
}

type codexOAuthImportBatchPlan struct {
	Index int
	Plan  providerAuthImportPlan
}

type codexOAuthDeleteOutcome struct {
	ID                string
	CredentialDeleted bool
	StatsDeleted      bool
	GrantDeleted      bool
	AlreadyMissing    bool
	CleanupPending    bool
	Retryable         bool
	Warning           string
}

func (s *Server) batchImportCodexOAuthCredentials(w http.ResponseWriter, r *http.Request) {
	request, err := decodeCodexOAuthImportBatchRequest(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}

	results := make([]codexOAuthImportBatchResult, len(request.Files))
	plans := make([]codexOAuthImportBatchPlan, 0, len(request.Files))
	now := s.now()
	for index, file := range request.Files {
		filename := filepath.Base(strings.TrimSpace(file.Filename))
		results[index].Filename = filename
		switch {
		case filename == "" || filename == ".":
			results[index].Status = "failed"
			results[index].Error = "JSON 文件名不能为空"
			continue
		case !strings.HasSuffix(strings.ToLower(filename), ".json"):
			results[index].Status = "failed"
			results[index].Error = "仅支持 .json 文件"
			continue
		case strings.TrimSpace(file.Content) == "":
			results[index].Status = "failed"
			results[index].Error = "JSON 文件为空"
			continue
		case len([]byte(file.Content)) > maxCodexOAuthImportFileBytes:
			results[index].Status = "failed"
			results[index].Error = "单个 JSON 文件超过 2 MiB 限制"
			continue
		}
		var value any
		if err := json.Unmarshal([]byte(file.Content), &value); err != nil {
			results[index].Status = "failed"
			results[index].Error = "JSON 格式无效"
			continue
		}
		plan, err := buildProviderAuthJSONImportPlan(filename, value, now)
		if err != nil {
			results[index].Status = "failed"
			results[index].Error = safeCodexOAuthImportPlanError(err)
			continue
		}
		results[index].Format = plan.Format
		plans = append(plans, codexOAuthImportBatchPlan{Index: index, Plan: plan})
	}

	if len(plans) > 0 {
		store, storeErr := s.nativeCodexCredentialStore()
		if storeErr == nil {
			storeErr = s.ensureNativeCodexProvider()
		}
		if storeErr != nil {
			for _, planned := range plans {
				results[planned.Index].Status = "failed"
				results[planned.Index].Error = storeErr.Error()
			}
		} else {
			for _, planned := range plans {
				documents := make([]codexauth.ImportDocument, 0, len(planned.Plan.Files))
				for _, file := range planned.Plan.Files {
					documents = append(documents, codexauth.ImportDocument{Filename: file.Filename, Content: file.Content})
				}
				stored, importErr := store.Import(documents)
				result := &results[planned.Index]
				if importErr != nil {
					result.Status = "failed"
					result.Error = importErr.Error()
					continue
				}
				result.Imported = stored.Imported
				result.Skipped = planned.Plan.Skipped + stored.Skipped
				result.Files = stored.Files
				if result.Imported == 0 {
					result.Status = "skipped"
				} else {
					result.Status = "success"
				}
			}
		}
	}
	writeCodexOAuthImportBatchResponse(w, results)
}

func decodeCodexOAuthImportBatchRequest(r *http.Request) (codexOAuthImportBatchRequest, error) {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCodexOAuthImportRequestBytes+1))
	if err != nil || len(data) > maxCodexOAuthImportRequestBytes {
		return codexOAuthImportBatchRequest{}, errors.New("批量 JSON 导入请求过大")
	}
	var request codexOAuthImportBatchRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return codexOAuthImportBatchRequest{}, errors.New("批量 JSON 导入请求无效")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return codexOAuthImportBatchRequest{}, errors.New("批量 JSON 导入请求必须是单个 JSON 对象")
	}
	if len(request.Files) == 0 {
		return codexOAuthImportBatchRequest{}, errors.New("files 至少包含一个 JSON 文件")
	}
	if len(request.Files) > maxCodexOAuthImportFiles {
		return codexOAuthImportBatchRequest{}, errors.New("单次最多导入 50 个 JSON 文件")
	}
	totalContentBytes := 0
	for _, file := range request.Files {
		totalContentBytes += len([]byte(file.Content))
		if totalContentBytes > maxCodexOAuthImportBatchBytes {
			return codexOAuthImportBatchRequest{}, errors.New("批量 JSON 文件内容超过 8 MiB 限制")
		}
	}
	return request, nil
}

func writeCodexOAuthImportBatchResponse(w http.ResponseWriter, results []codexOAuthImportBatchResult) {
	response := codexOAuthImportBatchResponse{Status: "ok", Total: len(results), Results: results}
	for _, result := range results {
		switch result.Status {
		case "success":
			response.Success++
		case "skipped":
			response.Skipped++
		default:
			response.Failed++
		}
	}
	status := http.StatusOK
	if response.Failed > 0 {
		response.Status = "partial"
		status = http.StatusMultiStatus
		if response.Failed == response.Total {
			response.Status = "failed"
		}
	}
	setNoStore(w)
	writeJSON(w, status, response)
}

func (s *Server) batchCodexOAuthAccounts(w http.ResponseWriter, r *http.Request) {
	request, err := decodeCodexOAuthAccountsBatchRequest(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	ids, err := validateCodexOAuthAccountsBatchRequest(request)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}

	var results []codexOAuthAccountsBatchResult
	switch request.Operation {
	case "sync":
		results = s.batchSyncCodexOAuthAccounts(r.Context(), ids)
	case "enable", "disable", "set_priority":
		results = s.batchUpdateCodexOAuthAccounts(ids, request.Operation, request.Priority)
	case "delete":
		results = s.batchDeleteCodexOAuthAccounts(r.Context(), ids)
	}
	writeCodexOAuthAccountsBatchResponse(w, results)
}

func decodeCodexOAuthAccountsBatchRequest(r *http.Request) (codexOAuthAccountsBatchRequest, error) {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCodexOAuthBatchBytes+1))
	if err != nil || len(data) > maxCodexOAuthBatchBytes {
		return codexOAuthAccountsBatchRequest{}, errors.New("批量账号操作请求过大")
	}
	var request codexOAuthAccountsBatchRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return codexOAuthAccountsBatchRequest{}, errors.New("批量账号操作请求无效")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return codexOAuthAccountsBatchRequest{}, errors.New("批量账号操作请求必须是单个 JSON 对象")
	}
	return request, nil
}

func validateCodexOAuthAccountsBatchRequest(request codexOAuthAccountsBatchRequest) ([]string, error) {
	if len(request.IDs) == 0 {
		return nil, errors.New("ids 至少包含一个 Codex 账号 ID")
	}
	if len(request.IDs) > maxCodexOAuthBatchAccounts {
		return nil, errors.New("单次最多操作 100 个 Codex 账号")
	}
	switch request.Operation {
	case "sync", "enable", "disable", "delete":
		if request.Priority != nil {
			return nil, errors.New("仅 set_priority 操作可以提供 priority")
		}
	case "set_priority":
		if request.Priority == nil || *request.Priority < 1 || *request.Priority > maxCodexOAuthPriority {
			return nil, errors.New("set_priority 必须提供 1 到 1000000 之间的 priority")
		}
	default:
		return nil, errors.New("operation 必须是 sync、enable、disable、set_priority 或 delete")
	}

	ids := make([]string, 0, len(request.IDs))
	seen := make(map[string]struct{}, len(request.IDs))
	for _, rawID := range request.IDs {
		id := strings.TrimSpace(rawID)
		if !codexauth.ValidCredentialID(id) {
			return nil, errors.New("ids 包含无效的 Codex 账号 ID")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Server) batchUpdateCodexOAuthAccounts(ids []string, operation string, priority *int) []codexOAuthAccountsBatchResult {
	store, err := s.nativeCodexCredentialStore()
	if err != nil {
		return failedCodexOAuthBatchResults(ids, err.Error(), true)
	}
	update := codexauth.MetadataUpdate{}
	switch operation {
	case "enable":
		disabled := false
		update.Disabled = &disabled
	case "disable":
		disabled := true
		update.Disabled = &disabled
	case "set_priority":
		update.Priority = priority
	}
	mutations, err := store.BatchUpdateMetadata(ids, update)
	if err != nil {
		return failedCodexOAuthBatchResults(ids, err.Error(), true)
	}
	results := make([]codexOAuthAccountsBatchResult, 0, len(mutations))
	for _, mutation := range mutations {
		result := codexOAuthAccountsBatchResult{ID: mutation.ID}
		if mutation.Err == nil {
			result.Success = true
		} else if errors.Is(mutation.Err, os.ErrNotExist) {
			result.Error = "Codex 账号不存在"
		} else {
			result.Error = mutation.Err.Error()
			result.Retryable = true
		}
		results = append(results, result)
	}
	return results
}

func (s *Server) batchSyncCodexOAuthAccounts(ctx context.Context, ids []string) []codexOAuthAccountsBatchResult {
	provider, err := s.nativeCodexProvider()
	if err != nil {
		return failedCodexOAuthBatchResults(ids, err.Error(), true)
	}
	results := make([]codexOAuthAccountsBatchResult, 0, len(ids))
	// Process sequentially to bound upstream pressure. Each account retains its
	// own deadline so one stalled request cannot run without a limit.
	for _, id := range ids {
		accountCtx, cancel := context.WithTimeout(ctx, codexOAuthSyncTimeout)
		_, status, syncErr := s.syncCodexOAuthAccount(accountCtx, provider, id)
		cancel()
		result := codexOAuthAccountsBatchResult{ID: id}
		if syncErr == nil {
			result.Success = true
		} else {
			result.Error = safeCodexOAuthBatchSyncError(status, syncErr)
			result.Retryable = status >= http.StatusInternalServerError
		}
		results = append(results, result)
	}
	return results
}

func (s *Server) batchDeleteCodexOAuthAccounts(ctx context.Context, ids []string) []codexOAuthAccountsBatchResult {
	store, err := s.nativeCodexCredentialStore()
	if err != nil {
		return failedCodexOAuthBatchResults(ids, err.Error(), true)
	}
	mutations, err := store.BatchDelete(ids)
	if err != nil {
		return failedCodexOAuthBatchResults(ids, err.Error(), true)
	}
	results := make([]codexOAuthAccountsBatchResult, 0, len(mutations))
	for _, mutation := range mutations {
		result := codexOAuthAccountsBatchResult{ID: mutation.ID}
		if mutation.Err != nil && !errors.Is(mutation.Err, os.ErrNotExist) && !mutation.Deleted {
			result.Error = mutation.Err.Error()
			result.Retryable = true
			results = append(results, result)
			continue
		}
		outcome := s.finishCodexOAuthAccountDelete(ctx, mutation.ID, mutation.Deleted)
		if mutation.Err != nil && mutation.Deleted {
			outcome.Warning = appendCodexOAuthWarning(outcome.Warning, "凭据已删除，但本地凭据目录同步失败；可安全重试删除操作")
			outcome.Retryable = true
		}
		result.Success = outcome.Warning == ""
		result.Warning = outcome.Warning
		result.Retryable = outcome.Retryable
		results = append(results, result)
	}
	return results
}

func (s *Server) syncCodexOAuthAccount(ctx context.Context, provider *providers.CodexProvider, id string) (codexOAuthAccountResponse, int, error) {
	account, quota, err := provider.SyncAccount(ctx, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return codexOAuthAccountResponse{}, http.StatusNotFound, errors.New("Codex 账号不存在")
		}
		return codexOAuthAccountResponse{}, http.StatusBadGateway, err
	}
	if s.store != nil {
		fetchedAt := s.now()
		if parsed, parseErr := time.Parse(time.RFC3339Nano, quota.FetchedAt); parseErr == nil {
			fetchedAt = parsed
		}
		if err := s.store.UpdateProviderAccountQuota(ctx, codexauth.DefaultProviderName, account.ID, quota, fetchedAt); err != nil {
			return codexOAuthAccountResponse{}, http.StatusInternalServerError, errors.New("Codex 额度快照保存失败")
		}
	}
	response := codexOAuthAccountResponse{AccountSummary: account, Quota: &quota}
	if s.store != nil {
		if stats, statsErr := s.store.GetProviderAccountStats(ctx, codexauth.DefaultProviderName, account.ID); statsErr == nil {
			response.Stats = &stats
		}
		usageByID, usageErr := s.store.ListProviderAccountUsage(ctx, codexauth.DefaultProviderName, []string{account.ID}, s.now())
		if usageErr != nil {
			return codexOAuthAccountResponse{}, http.StatusInternalServerError, errors.New("Codex 账号用量统计失败")
		}
		response.Usage = normalizeCodexAccountUsage(usageByID[account.ID])
	}
	return response, http.StatusOK, nil
}

func (s *Server) deleteCodexOAuthAccountCore(ctx context.Context, store *codexauth.Store, id string) (codexOAuthDeleteOutcome, error) {
	mutations, err := store.BatchDelete([]string{id})
	if err != nil {
		return codexOAuthDeleteOutcome{}, err
	}
	if len(mutations) != 1 {
		return codexOAuthDeleteOutcome{}, os.ErrNotExist
	}
	mutation := mutations[0]
	if mutation.Err != nil && !errors.Is(mutation.Err, os.ErrNotExist) {
		return codexOAuthDeleteOutcome{}, mutation.Err
	}
	return s.finishCodexOAuthAccountDelete(ctx, mutation.ID, mutation.Deleted), nil
}

func (s *Server) finishCodexOAuthAccountDelete(ctx context.Context, id string, credentialDeleted bool) codexOAuthDeleteOutcome {
	statsDeleted := true
	grantDeleted := true
	if s.store != nil {
		if err := s.store.DeleteProviderAccountStats(ctx, codexauth.DefaultProviderName, id); err != nil {
			statsDeleted = false
		}
		if err := s.store.DeleteGatewayAccountGrant(ctx, codexauth.DefaultProviderName, id); err != nil {
			grantDeleted = false
		}
	}
	cleanupPending := !statsDeleted || !grantDeleted
	outcome := codexOAuthDeleteOutcome{
		ID:                id,
		CredentialDeleted: credentialDeleted,
		StatsDeleted:      statsDeleted,
		GrantDeleted:      grantDeleted,
		AlreadyMissing:    !credentialDeleted,
		CleanupPending:    cleanupPending,
		Retryable:         cleanupPending,
	}
	if cleanupPending {
		if credentialDeleted {
			outcome.Warning = "凭据已删除，但账号统计或 Gateway 授权清理失败；可安全重试 DELETE 完成清理"
		} else {
			outcome.Warning = "凭据已不存在，但账号统计或 Gateway 授权清理仍失败；可安全重试 DELETE 完成清理"
		}
	}
	return outcome
}

func writeCodexOAuthAccountsBatchResponse(w http.ResponseWriter, results []codexOAuthAccountsBatchResult) {
	success := 0
	for _, result := range results {
		if result.Success {
			success++
		}
	}
	response := codexOAuthAccountsBatchResponse{
		Status:  "ok",
		Total:   len(results),
		Success: success,
		Failed:  len(results) - success,
		Results: results,
	}
	status := http.StatusOK
	if response.Failed > 0 {
		response.Status = "partial"
		status = http.StatusMultiStatus
	}
	setNoStore(w)
	writeJSON(w, status, response)
}

func safeCodexOAuthBatchSyncError(status int, err error) string {
	if status == http.StatusNotFound || status == http.StatusInternalServerError {
		return err.Error()
	}
	return "Codex 账号同步失败"
}

func failedCodexOAuthBatchResults(ids []string, message string, retryable bool) []codexOAuthAccountsBatchResult {
	results := make([]codexOAuthAccountsBatchResult, 0, len(ids))
	for _, id := range ids {
		results = append(results, codexOAuthAccountsBatchResult{ID: id, Error: message, Retryable: retryable})
	}
	return results
}

func safeCodexOAuthImportPlanError(err error) string {
	message := strings.TrimSpace(err.Error())
	if strings.Contains(message, "platform ") {
		return "账号 platform 不是 OpenAI/Codex"
	}
	return message
}

func appendCodexOAuthWarning(existing, warning string) string {
	if existing == "" {
		return warning
	}
	return existing + "；" + warning
}
