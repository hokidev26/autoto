package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

var (
	modelAggregateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$`)
	subscriptionTierOrder     = []string{"free", "plus", "pro", "team", "enterprise", "education_k12"}
)

type strictString struct {
	set   bool
	value string
}

func (value *strictString) UnmarshalJSON(raw []byte) error {
	if strings.TrimSpace(string(raw)) == "null" {
		return errors.New("value must be a string")
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("value must be a string")
	}
	value.set = true
	value.value = decoded
	return nil
}

type strictInt64 struct {
	set   bool
	value int64
}

func (value *strictInt64) UnmarshalJSON(raw []byte) error {
	if strings.TrimSpace(string(raw)) == "null" {
		return errors.New("revision must be an integer")
	}
	var decoded int64
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("revision must be an integer")
	}
	value.set = true
	value.value = decoded
	return nil
}

type strictBool struct {
	set   bool
	value bool
}

func (value *strictBool) UnmarshalJSON(raw []byte) error {
	if strings.TrimSpace(string(raw)) == "null" {
		return errors.New("value must be a boolean")
	}
	var decoded bool
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("value must be a boolean")
	}
	value.set = true
	value.value = decoded
	return nil
}

type strictStrings struct {
	set    bool
	values []string
}

func (value *strictStrings) UnmarshalJSON(raw []byte) error {
	if strings.TrimSpace(string(raw)) == "null" {
		return errors.New("members must be an array of strings")
	}
	var decoded []string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("members must be an array of strings")
	}
	value.set = true
	value.values = decoded
	return nil
}

type modelAggregatePutRequest struct {
	Mode             strictString  `json:"mode"`
	Members          strictStrings `json:"members"`
	Revision         strictInt64   `json:"revision"`
	ExpectedRevision strictInt64   `json:"expectedRevision"`
}

type revisionCASRequest struct {
	Revision         strictInt64 `json:"revision"`
	ExpectedRevision strictInt64 `json:"expectedRevision"`
}

type runtimeModelSettingsRequest struct {
	DefaultReasoningEffort strictString `json:"defaultReasoningEffort"`
	SubscriptionTier       strictString `json:"subscriptionTier"`
	AccountEmail           strictString `json:"accountEmail"`
	Revision               strictInt64  `json:"revision"`
	ExpectedRevision       strictInt64  `json:"expectedRevision"`
}

type agentModelSettingsRequest struct {
	DefaultModel       strictString        `json:"defaultModel"`
	SummaryModel       strictString        `json:"summaryModel"`
	SafetyModel        strictString        `json:"safetyModel"`
	SubagentModels     map[string]string   `json:"subagentModels"`
	SubagentModelPools map[string][]string `json:"subagentModelPools"`
}

type agentReasoningRequest struct {
	ReasoningEffort  strictString `json:"reasoningEffort"`
	Model            strictString `json:"model"`
	EntityGeneration strictInt64  `json:"entityGeneration"`
}

type agentFastModeRequest struct {
	FastMode         strictBool   `json:"fastMode"`
	Model            strictString `json:"model"`
	EntityGeneration strictInt64  `json:"entityGeneration"`
}

type clientIdentityResponse struct {
	InstallationID             string `json:"installationId"`
	ClientVersion              string `json:"clientVersion"`
	Version                    string `json:"version"`
	Authentication             bool   `json:"authentication"`
	IsAuthenticationCredential bool   `json:"isAuthenticationCredential"`
	Purpose                    string `json:"purpose"`
	Revision                   int64  `json:"revision"`
	UpdatedAt                  string `json:"updatedAt,omitempty"`
}

func (s *Server) listModelAggregates(w http.ResponseWriter, r *http.Request) {
	aggregates, err := s.modelRuntime().listAggregates(r.Context())
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aggregates)
}

func (s *Server) getModelAggregate(w http.ResponseWriter, r *http.Request) {
	name, err := modelAggregateName(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	aggregate, err := s.modelRuntime().getAggregate(r.Context(), name)
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aggregate)
}

func (s *Server) putModelAggregate(w http.ResponseWriter, r *http.Request) {
	name, err := modelAggregateName(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var request modelAggregatePutRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	aggregate, err := s.modelRuntime().putAggregate(r.Context(), name, request)
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aggregate)
}

func (s *Server) deleteModelAggregate(w http.ResponseWriter, r *http.Request) {
	name, err := modelAggregateName(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var request revisionCASRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	revision, err := s.modelRuntime().deleteAggregate(r.Context(), name, request)
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "name": name, "revision": revision})
}

func (s *Server) updateAgentModelSettings(w http.ResponseWriter, r *http.Request) {
	var request agentModelSettingsRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !request.DefaultModel.set || !request.SummaryModel.set {
		writeError(w, http.StatusBadRequest, "defaultModel and summaryModel are required")
		return
	}
	defaultModel, err := validateAgentModelReference("defaultModel", request.DefaultModel.value, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	summaryModel, err := validateAgentModelReference("summaryModel", request.SummaryModel.value, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Optional, and blank is meaningful: it clears the override so the safety
	// gate follows the summary model again.
	safetyModel := ""
	if request.SafetyModel.set {
		safetyModel, err = validateAgentModelReference("safetyModel", request.SafetyModel.value, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		s.cfgMu.RLock()
		safetyModel = s.cfg.Agent.SafetyModel
		s.cfgMu.RUnlock()
	}
	subagentModels, subagentPools, err := normalizeAgentRoleModelSettings(request.SubagentModels, request.SubagentModelPools)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	s.cfgMu.RLock()
	updated := s.cfg
	configPath := s.configPath
	s.cfgMu.RUnlock()
	updated.Agent.DefaultModel = defaultModel
	updated.Agent.SummaryModel = summaryModel
	updated.Agent.SafetyModel = safetyModel
	updated.Agent.SubagentModels = subagentModels
	updated.Agent.SubagentModelPools = subagentPools
	path := effectiveConfigPath(updated, configPath)
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusInternalServerError, "agent model settings could not be persisted")
		return
	}
	if err := config.Save(path, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "agent model settings could not be persisted")
		return
	}
	s.refreshProviderDefault(updated)
	s.cfgMu.Lock()
	s.cfg = updated
	s.cfgMu.Unlock()
	if s.runner != nil {
		s.runner.SetAgentModelSettings(updated.Agent)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": updated.Agent, "persisted": true})
}

func (s *Server) updateRuntimeModelSettings(w http.ResponseWriter, r *http.Request) {
	var request runtimeModelSettingsRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := s.modelRuntime().updateRuntimeSettings(r.Context(), request)
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) updateAgentReasoningEffort(w http.ResponseWriter, r *http.Request) {
	var request agentReasoningRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if agentID == "" {
		agentID = strings.TrimSpace(chi.URLParam(r, "agentId"))
	}
	agent, err := s.modelRuntime().updateReasoningEffort(r.Context(), agentID, request)
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) updateAgentFastMode(w http.ResponseWriter, r *http.Request) {
	var request agentFastModeRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if agentID == "" {
		agentID = strings.TrimSpace(chi.URLParam(r, "agentId"))
	}
	agent, err := s.modelRuntime().updateFastMode(r.Context(), agentID, request)
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) clientIdentity(w http.ResponseWriter, r *http.Request) {
	settings, err := s.modelRuntime().clientIdentity(r.Context())
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identityResponse(settings))
}

func (s *Server) rotateClientIdentity(w http.ResponseWriter, r *http.Request) {
	var request revisionCASRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := s.modelRuntime().rotateClientIdentity(r.Context(), request)
	if err != nil {
		writeModelRuntimeError(w, err)
		return
	}
	s.refreshProviderRuntimeIdentity(settings.InstallationID)
	writeJSON(w, http.StatusOK, identityResponse(settings))
}

func (s *Server) runtimeSettingsForResponse(ctx context.Context) (db.RuntimeSettings, error) {
	if s.store == nil {
		return db.RuntimeSettings{DefaultReasoningEffort: "auto", SubscriptionTier: "free"}, nil
	}
	return s.store.GetRuntimeSettings(ctx)
}

func subscriptionTierOrderSnapshot() []string {
	return append([]string(nil), subscriptionTierOrder...)
}

func identityResponse(settings db.RuntimeSettings) clientIdentityResponse {
	return clientIdentityResponse{
		InstallationID: settings.InstallationID,
		// ClientVersion reports what providers actually receive, so it mirrors the
		// adapted value. Version stays the raw build stamp for display.
		ClientVersion:              providers.ClientVersionFromBuildStamp(config.Version),
		Version:                    config.Version,
		Authentication:             false,
		IsAuthenticationCredential: false,
		Purpose:                    "client-identification-only",
		Revision:                   settings.Revision,
		UpdatedAt:                  settings.UpdatedAt,
	}
}

func (s *Server) refreshProviderRuntimeIdentity(installationID string) {
	s.providerMutationMu.Lock()
	defer s.providerMutationMu.Unlock()
	if s.providers == nil {
		return
	}
	cfg := s.configSnapshot()
	if len(cfg.Providers.Instances) == 0 {
		return
	}
	for _, providerCfg := range cfg.Providers.Instances {
		if providerCfg.Disabled {
			s.providers.Unregister(providerCfg.Name)
			continue
		}
		// A non-semver build stamp must not reach ClientVersion: NewProvider below
		// fails closed and this loop skips silently, so one bad stamp empties the
		// whole registry and the UI only shows "no usable models".
		providerCfg.ClientVersion = providers.ClientVersionFromBuildStamp(config.Version)
		providerCfg.InstallationID = installationID
		providerCfg = providers.ApplyCredentialStorePath(providerCfg, cfg.Paths.HomeDir)
		provider, err := providers.NewProvider(providerCfg)
		if err != nil {
			continue
		}
		if accountProvider, ok := provider.(interface {
			SetAccountTelemetry(providers.AccountTelemetry)
			SetGatewayAccountPolicy(providers.GatewayAccountPolicy)
		}); ok && s.store != nil {
			accountProvider.SetAccountTelemetry(s.store)
			accountProvider.SetGatewayAccountPolicy(s.store)
		}
		s.providers.Register(provider)
	}
	s.providers.SetDefaultFromConfig(cfg.Agent.DefaultModel, cfg.Providers.Instances)
}

func modelAggregateName(r *http.Request) (string, error) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if !modelAggregateNamePattern.MatchString(name) {
		return "", errors.New("invalid model aggregate name")
	}
	return name, nil
}

func requestRevision(revision, expectedRevision strictInt64, allowZero bool) (int64, error) {
	if revision.set && expectedRevision.set {
		return 0, errors.New("provide only one of revision or expectedRevision")
	}
	if !revision.set && !expectedRevision.set {
		return 0, errors.New("revision is required")
	}
	value := revision.value
	if expectedRevision.set {
		value = expectedRevision.value
	}
	if value < 0 || (!allowZero && value == 0) {
		if allowZero {
			return 0, errors.New("revision must be zero or greater")
		}
		return 0, errors.New("revision must be greater than zero")
	}
	return value, nil
}

func validateModelAggregateMembers(members []string) ([]string, error) {
	if len(members) == 0 || len(members) > db.ModelAggregateMaxMembers {
		return nil, fmt.Errorf("members must contain 1 to %d items", db.ModelAggregateMaxMembers)
	}
	seen := make(map[string]struct{}, len(members))
	normalized := make([]string, 0, len(members))
	for _, member := range members {
		member = strings.TrimSpace(member)
		parts := strings.SplitN(member, ":", 2)
		if len(member) > 256 || len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.EqualFold(strings.TrimSpace(parts[0]), "aggregate") {
			return nil, errors.New("members must be non-aggregate provider:model references")
		}
		if _, duplicate := seen[member]; duplicate {
			return nil, errors.New("members must be unique")
		}
		seen[member] = struct{}{}
		normalized = append(normalized, member)
	}
	return normalized, nil
}

func validateAgentModelReference(field, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("%s is required", field)
		}
		return "", nil
	}
	if len(value) > 256 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("%s is invalid", field)
	}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", fmt.Errorf("%s must be a provider:model reference", field)
	}
	return strings.TrimSpace(parts[0]) + ":" + strings.TrimSpace(parts[1]), nil
}

func normalizeAgentRoleModelSettings(models map[string]string, pools map[string][]string) (map[string]string, map[string][]string, error) {
	allowedRoles := map[string]struct{}{"explore": {}, "plan": {}, "general": {}, "search": {}}
	normalizedModels := make(map[string]string)
	for rawRole, rawModel := range models {
		role := strings.ToLower(strings.TrimSpace(rawRole))
		if _, ok := allowedRoles[role]; !ok {
			return nil, nil, fmt.Errorf("unsupported subagent role %q", rawRole)
		}
		model, err := validateAgentModelReference("subagentModels."+role, rawModel, false)
		if err != nil {
			return nil, nil, err
		}
		if model != "" {
			normalizedModels[role] = model
		}
	}
	normalizedPools := make(map[string][]string)
	for rawRole, rawPool := range pools {
		role := strings.ToLower(strings.TrimSpace(rawRole))
		if _, ok := allowedRoles[role]; !ok {
			return nil, nil, fmt.Errorf("unsupported subagent role %q", rawRole)
		}
		if len(rawPool) > 64 {
			return nil, nil, fmt.Errorf("subagentModelPools.%s must contain at most 64 models", role)
		}
		seen := make(map[string]struct{}, len(rawPool))
		pool := make([]string, 0, len(rawPool))
		for _, rawModel := range rawPool {
			model, err := validateAgentModelReference("subagentModelPools."+role, rawModel, true)
			if err != nil {
				return nil, nil, err
			}
			if _, duplicate := seen[model]; duplicate {
				continue
			}
			seen[model] = struct{}{}
			pool = append(pool, model)
		}
		if preferred := normalizedModels[role]; preferred != "" && len(pool) > 0 {
			if _, ok := seen[preferred]; !ok {
				return nil, nil, fmt.Errorf("subagentModels.%s must be included in its model pool", role)
			}
		}
		if len(pool) > 0 {
			normalizedPools[role] = pool
		}
	}
	if len(normalizedModels) == 0 {
		normalizedModels = nil
	}
	if len(normalizedPools) == 0 {
		normalizedPools = nil
	}
	return normalizedModels, normalizedPools, nil
}

func validAgentReasoningEffort(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	switch value {
	case "auto", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

// validDefaultReasoningEffort accepts the same levels as a per-agent override,
// including "xhigh", which Codex serves. The runtime default is provider
// agnostic, and safeReasoningEffortForCapabilities already clamps an inherited
// level a given model cannot serve down to "auto", so accepting it here cannot
// push an unsupported effort at a provider.
func validDefaultReasoningEffort(value string) bool {
	switch value {
	case "auto", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func validSubscriptionTier(value string) bool {
	for _, tier := range subscriptionTierOrder {
		if value == tier {
			return true
		}
	}
	return false
}

func validateRuntimeAccountEmail(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 320 || !utf8.ValidString(value) || strings.Count(value, "@") != 1 || strings.ContainsAny(value, "\r\n\t ") {
		return errors.New("invalid accountEmail")
	}
	parts := strings.SplitN(value, "@", 2)
	if parts[0] == "" || parts[1] == "" || !strings.Contains(parts[1], ".") {
		return errors.New("invalid accountEmail")
	}
	return nil
}

func writeModelRuntimeError(w http.ResponseWriter, err error) {
	var api apiError
	if errors.As(err, &api) {
		writeError(w, api.status, api.msg)
		return
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, err.Error())
	case db.IsConflict(err):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
