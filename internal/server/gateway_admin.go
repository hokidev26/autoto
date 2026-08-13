package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	gatewaypkg "autoto/internal/gateway"
	"autoto/internal/providers"
)

type gatewayKeyPolicyRequest struct {
	Name              *string   `json:"name"`
	Enabled           *bool     `json:"enabled"`
	AllowedModels     *[]string `json:"allowedModels"`
	RequestsPerMinute *int64    `json:"requestsPerMinute"`
	MonthlyTokenLimit *int64    `json:"monthlyTokenLimit"`
	MaxConcurrency    *int64    `json:"maxConcurrency"`
	ExpiresAt         *string   `json:"expiresAt"`
	ExpectedUpdatedAt *string   `json:"expectedUpdatedAt"`
}

type gatewayModelRequest struct {
	Alias             *string `json:"alias"`
	TargetModel       *string `json:"targetModel"`
	Enabled           *bool   `json:"enabled"`
	ExpectedUpdatedAt *string `json:"expectedUpdatedAt"`
}

type gatewayKeyUsage struct {
	MonthUTC      string  `json:"monthUtc"`
	Requests      int64   `json:"requests"`
	InputTokens   int64   `json:"inputTokens"`
	OutputTokens  int64   `json:"outputTokens"`
	MonthlyTokens int64   `json:"monthlyTokens"`
	Errors        int64   `json:"errors"`
	CostUSD       float64 `json:"costUsd"`
}

type gatewayKeyResponse struct {
	db.GatewayKey
	Usage gatewayKeyUsage `json:"usage"`
}

type gatewayUsageItem struct {
	db.GatewayMonthlyUsage
	Name          string `json:"name"`
	KeyPrefix     string `json:"keyPrefix"`
	Enabled       bool   `json:"enabled"`
	RevokedAt     string `json:"revokedAt,omitempty"`
	Requests      int64  `json:"requests"`
	Tokens        int64  `json:"tokens"`
	MonthlyTokens int64  `json:"monthlyTokens"`
	Errors        int64  `json:"errors"`
}

type gatewayUsageSummary struct {
	Requests   int64   `json:"requests"`
	Tokens     int64   `json:"tokens"`
	ActiveKeys int64   `json:"activeKeys"`
	Errors     int64   `json:"errors"`
	CostUSD    float64 `json:"costUsd"`
}

func (s *Server) listGatewayKeys(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	keys, err := s.store.ListGatewayKeys(r.Context())
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	now := s.gatewayAdminNow()
	items := make([]gatewayKeyResponse, 0, len(keys))
	for _, key := range keys {
		usage, err := s.gatewayUsageForKey(r.Context(), key, now)
		if err != nil {
			writeGatewayAdminError(w, err, "gateway usage")
			return
		}
		items = append(items, gatewayKeyResponse{GatewayKey: key, Usage: usage})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": items})
}

func (s *Server) createGatewayKey(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !s.gatewayStoreAvailable(w) {
		return
	}
	var req gatewayKeyPolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	policy := gatewayKeyPolicyFromCreate(req)
	if err := validateGatewayKeyPolicy(policy); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	generated, err := gatewaypkg.GenerateKey()
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	key, err := s.store.CreateGatewayKey(r.Context(), db.GatewayKey{
		Name:              policy.Name,
		KeyPrefix:         generated.Prefix,
		TokenHash:         generated.Hash,
		Enabled:           policy.Enabled,
		AllowedModels:     policy.AllowedModels,
		RequestsPerMinute: policy.RequestsPerMinute,
		MonthlyTokenLimit: policy.MonthlyTokenLimit,
		MaxConcurrency:    policy.MaxConcurrency,
		ExpiresAt:         policy.ExpiresAt,
	})
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "token": generated.Token})
}

func (s *Server) updateGatewayKey(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	var req gatewayKeyPolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedUpdatedAt == nil || strings.TrimSpace(*req.ExpectedUpdatedAt) == "" {
		writeError(w, http.StatusBadRequest, "expectedUpdatedAt is required")
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	current, err := s.store.GetGatewayKey(r.Context(), id)
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	policy := gatewayKeyPolicyFromPatch(current, req)
	if err := validateGatewayKeyPolicy(policy); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	key, err := s.store.UpdateGatewayKeyPolicyCAS(r.Context(), id, policy, *req.ExpectedUpdatedAt)
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

func (s *Server) rotateGatewayKey(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !s.gatewayStoreAvailable(w) {
		return
	}
	generated, err := gatewaypkg.GenerateKey()
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	key, err := s.store.RotateGatewayKey(r.Context(), chi.URLParam(r, "id"), generated.Prefix, generated.Hash)
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "token": generated.Token})
}

func (s *Server) revokeGatewayKey(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	key, err := s.store.RevokeGatewayKey(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

func (s *Server) deleteGatewayKey(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "key id is required")
		return
	}
	if err := s.store.DeleteGatewayKey(r.Context(), id); err != nil {
		writeGatewayAdminError(w, err, "gateway key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listGatewayModels(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	models, err := s.store.ListGatewayModels(r.Context())
	if err != nil {
		writeGatewayAdminError(w, err, "gateway model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) createGatewayModel(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	var req gatewayModelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	model := gatewayModelFromCreate(req)
	if err := validateGatewayModel(model); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	created, err := s.store.CreateGatewayModel(r.Context(), model)
	if err != nil {
		writeGatewayAdminError(w, err, "gateway model")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"model": created})
}

func (s *Server) updateGatewayModel(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	var req gatewayModelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedUpdatedAt == nil || strings.TrimSpace(*req.ExpectedUpdatedAt) == "" {
		writeError(w, http.StatusBadRequest, "expectedUpdatedAt is required")
		return
	}
	oldAlias := gatewayModelAlias(r)
	if oldAlias == "" {
		writeError(w, http.StatusBadRequest, "gateway model alias is required")
		return
	}
	current, err := s.store.GetGatewayModel(r.Context(), oldAlias)
	if err != nil {
		writeGatewayAdminError(w, err, "gateway model")
		return
	}
	updated := gatewayModelFromPatch(current, req)
	if err := validateGatewayModel(updated); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	model, err := s.store.UpdateGatewayModelCAS(r.Context(), oldAlias, updated, *req.ExpectedUpdatedAt)
	if err != nil {
		writeGatewayAdminError(w, err, "gateway model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": model})
}

func (s *Server) deleteGatewayModel(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	alias := gatewayModelAlias(r)
	if alias == "" {
		writeError(w, http.StatusBadRequest, "gateway model alias is required")
		return
	}
	if err := s.store.DeleteGatewayModel(r.Context(), alias); err != nil {
		writeGatewayAdminError(w, err, "gateway model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alias": alias})
}

func gatewayModelAlias(r *http.Request) string {
	if alias := strings.TrimSpace(chi.URLParam(r, "alias")); alias != "" {
		return alias
	}
	return strings.TrimSpace(r.URL.Query().Get("alias"))
}

func (s *Server) gatewayUsage(w http.ResponseWriter, r *http.Request) {
	if !s.gatewayStoreAvailable(w) {
		return
	}
	keys, err := s.store.ListGatewayKeys(r.Context())
	if err != nil {
		writeGatewayAdminError(w, err, "gateway usage")
		return
	}
	now := s.gatewayAdminNow()
	items := make([]gatewayUsageItem, 0, len(keys))
	summary := gatewayUsageSummary{}
	for _, key := range keys {
		usage, err := s.store.GetGatewayKeyMonthlyUsage(r.Context(), key.ID, now)
		if err != nil {
			writeGatewayAdminError(w, err, "gateway usage")
			return
		}
		errorsCount, err := s.gatewayKeyMonthlyErrors(r.Context(), key.ID, now)
		if err != nil {
			writeGatewayAdminError(w, err, "gateway usage")
			return
		}
		items = append(items, gatewayUsageItem{
			GatewayMonthlyUsage: usage,
			Name:                key.Name,
			KeyPrefix:           key.KeyPrefix,
			Enabled:             key.Enabled,
			RevokedAt:           key.RevokedAt,
			Requests:            usage.RequestCount,
			Tokens:              usage.TotalTokens,
			MonthlyTokens:       usage.TotalTokens,
			Errors:              errorsCount,
		})
		summary.Requests += usage.RequestCount
		summary.Tokens += usage.TotalTokens
		summary.Errors += errorsCount
		summary.CostUSD += usage.CostUSD
		if gatewayKeyIsActive(key, now) {
			summary.ActiveKeys++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "summary": summary})
}

// GatewayProviderAllowed reads the current configuration snapshot on every call
// so provider lifecycle patches take effect without restarting the Gateway.
func (s *Server) GatewayProviderAllowed(_ context.Context, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, provider := range s.configSnapshot().Providers.Summaries() {
		if !strings.EqualFold(strings.TrimSpace(provider.Name), name) {
			continue
		}
		return provider.Enabled && provider.GatewayEnabled && !providerGatewaySharingForbidden(provider.Type, provider.Profile)
	}
	return false
}

func (s *Server) gatewayUsageForKey(ctx context.Context, key db.GatewayKey, month time.Time) (gatewayKeyUsage, error) {
	usage, err := s.store.GetGatewayKeyMonthlyUsage(ctx, key.ID, month)
	if err != nil {
		return gatewayKeyUsage{}, err
	}
	errorsCount, err := s.gatewayKeyMonthlyErrors(ctx, key.ID, month)
	if err != nil {
		return gatewayKeyUsage{}, err
	}
	return gatewayKeyUsage{
		MonthUTC:      usage.MonthUTC,
		Requests:      usage.RequestCount,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		MonthlyTokens: usage.TotalTokens,
		Errors:        errorsCount,
		CostUSD:       usage.CostUSD,
	}, nil
}

func (s *Server) gatewayKeyMonthlyErrors(ctx context.Context, keyID string, month time.Time) (int64, error) {
	month = month.UTC()
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	var count int64
	err := s.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE gateway_key_id = ? AND COALESCE(error_message, '') <> '' AND julianday(created_at) >= julianday(?) AND julianday(created_at) < julianday(?)`, keyID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)).Scan(&count)
	return count, err
}

func (s *Server) gatewayAdminNow() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock().UTC()
}

func (s *Server) gatewayStoreAvailable(w http.ResponseWriter) bool {
	if s.store != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "database store is not initialized")
	return false
}

func writeGatewayAdminError(w http.ResponseWriter, err error, resource string) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, resource+" not found")
	case errors.Is(err, db.ErrGatewayKeyRevoked), db.IsConflict(err):
		writeError(w, http.StatusConflict, resource+" conflict")
	default:
		writeError(w, http.StatusInternalServerError, "gateway database operation failed")
	}
}

func gatewayKeyPolicyFromCreate(req gatewayKeyPolicyRequest) db.GatewayKeyPolicy {
	policy := db.GatewayKeyPolicy{Enabled: true}
	applyGatewayKeyPolicyRequest(&policy, req)
	return policy
}

func gatewayKeyPolicyFromPatch(current db.GatewayKey, req gatewayKeyPolicyRequest) db.GatewayKeyPolicy {
	policy := db.GatewayKeyPolicy{
		Name:              current.Name,
		Enabled:           current.Enabled,
		AllowedModels:     append([]string(nil), current.AllowedModels...),
		RequestsPerMinute: current.RequestsPerMinute,
		MonthlyTokenLimit: current.MonthlyTokenLimit,
		MaxConcurrency:    current.MaxConcurrency,
		ExpiresAt:         current.ExpiresAt,
	}
	applyGatewayKeyPolicyRequest(&policy, req)
	return policy
}

func applyGatewayKeyPolicyRequest(policy *db.GatewayKeyPolicy, req gatewayKeyPolicyRequest) {
	if req.Name != nil {
		policy.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		policy.Enabled = *req.Enabled
	}
	if req.AllowedModels != nil {
		policy.AllowedModels = append([]string(nil), (*req.AllowedModels)...)
	}
	if req.RequestsPerMinute != nil {
		policy.RequestsPerMinute = *req.RequestsPerMinute
	}
	if req.MonthlyTokenLimit != nil {
		policy.MonthlyTokenLimit = *req.MonthlyTokenLimit
	}
	if req.MaxConcurrency != nil {
		policy.MaxConcurrency = *req.MaxConcurrency
	}
	if req.ExpiresAt != nil {
		policy.ExpiresAt = strings.TrimSpace(*req.ExpiresAt)
	}
}

func gatewayModelFromCreate(req gatewayModelRequest) db.GatewayModel {
	model := db.GatewayModel{Enabled: true}
	applyGatewayModelRequest(&model, req)
	return model
}

func gatewayModelFromPatch(current db.GatewayModel, req gatewayModelRequest) db.GatewayModel {
	model := current
	applyGatewayModelRequest(&model, req)
	return model
}

func applyGatewayModelRequest(model *db.GatewayModel, req gatewayModelRequest) {
	if req.Alias != nil {
		model.Alias = strings.TrimSpace(*req.Alias)
	}
	if req.TargetModel != nil {
		model.TargetModel = strings.TrimSpace(*req.TargetModel)
	}
	if req.Enabled != nil {
		model.Enabled = *req.Enabled
	}
}

func validateGatewayKeyPolicy(policy db.GatewayKeyPolicy) error {
	if err := validateGatewayAdminText(policy.Name, 120, "name"); err != nil {
		return err
	}
	if policy.RequestsPerMinute < 0 || policy.MonthlyTokenLimit < 0 || policy.MaxConcurrency < 0 {
		return errors.New("gateway key limits must not be negative")
	}
	seenModels := make(map[string]struct{}, len(policy.AllowedModels))
	normalizedModels := make([]string, 0, len(policy.AllowedModels))
	for _, model := range policy.AllowedModels {
		model = strings.TrimSpace(model)
		if !validGatewayAdminModelRef(model, 256, false) {
			return errors.New("invalid gateway allowed model")
		}
		if _, exists := seenModels[model]; !exists {
			seenModels[model] = struct{}{}
			normalizedModels = append(normalizedModels, model)
		}
	}
	encodedModels, err := json.Marshal(normalizedModels)
	if err != nil || len(encodedModels) > 32768 {
		return errors.New("gateway allowed models are too large")
	}
	if policy.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, policy.ExpiresAt); err != nil {
			return errors.New("invalid gateway key expiration time")
		}
	}
	return nil
}

func validateGatewayModel(model db.GatewayModel) error {
	if !validGatewayAdminModelRef(model.Alias, 128, true) {
		return errors.New("invalid gateway model alias")
	}
	if !validGatewayAdminModelRef(model.TargetModel, 256, false) {
		return errors.New("invalid gateway target model")
	}
	providerName, targetModel := providers.SplitModel(model.TargetModel)
	if providerName == "" || targetModel == "" {
		return errors.New("gateway target model must be a complete provider:model reference")
	}
	return nil
}

func validateGatewayAdminText(value string, maxBytes int, name string) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("invalid gateway key %s", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("invalid gateway key %s", name)
		}
	}
	return nil
}

func validGatewayAdminModelRef(value string, maxBytes int, alias bool) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for index, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			return false
		}
		if alias && (r > unicode.MaxASCII || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || index > 0 && (r == '.' || r == '_' || r == '-' || r == ':' || r == '/'))) {
			return false
		}
	}
	return true
}

func gatewayKeyIsActive(key db.GatewayKey, now time.Time) bool {
	if !key.Enabled || key.RevokedAt != "" {
		return false
	}
	if key.ExpiresAt == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, key.ExpiresAt)
	return err == nil && expiresAt.After(now)
}
