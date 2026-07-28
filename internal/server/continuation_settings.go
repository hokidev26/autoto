package server

import (
	"net/http"
	"strings"

	"autoto/internal/agent"
	"autoto/internal/config"
)

type continuationSettingsRequest struct {
	Mode             string `json:"mode"`
	SegmentTurns     int64  `json:"segmentTurns"`
	MaxContinuations int64  `json:"maxContinuations"`
	MaxTotalTurns    int64  `json:"maxTotalTurns"`
	MaxRunDurationMs int64  `json:"maxRunDurationMs"`
	MaxRunTokens     int64  `json:"maxRunTokens"`
}

func (s *Server) continuationSettingsEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is unavailable")
		return
	}
	var req continuationSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := strictContinuationSettings(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Serialize this durable config transaction with provider and security
	// mutations. Do not hold cfgMu across disk I/O.
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.cfgMu.RLock()
	updated := s.cfg
	configPath := s.configPath
	s.cfgMu.RUnlock()
	updated.Agent.AutoContinuationMode = settings.Mode
	updated.Agent.ContinuationSegmentTurns = int(settings.SegmentTurns)
	updated.Agent.MaxContinuations = int(settings.MaxContinuations)
	updated.Agent.MaxTotalTurns = int(settings.MaxTotalTurns)
	updated.Agent.MaxRunDurationMs = settings.MaxRunDurationMs
	updated.Agent.MaxRunTokens = settings.MaxRunTokens
	path := effectiveConfigPath(updated, configPath)
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusInternalServerError, "continuation settings could not be persisted")
		return
	}
	if err := config.Save(path, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "continuation settings could not be persisted")
		return
	}
	// Settings are frozen into each new Run by Runner.prepareContinuationRun;
	// currently running runs retain their existing durable budgets.
	s.cfgMu.Lock()
	s.cfg = updated
	s.cfgMu.Unlock()
	applied := s.runner.SetContinuationSettings(settings)
	writeJSON(w, http.StatusOK, map[string]any{"continuation": applied, "persisted": true})
}

func strictContinuationSettings(req continuationSettingsRequest) (agent.ContinuationSettings, error) {
	settings := agent.ContinuationSettings{
		Mode: strings.ToLower(strings.TrimSpace(req.Mode)), SegmentTurns: req.SegmentTurns, MaxContinuations: req.MaxContinuations,
		MaxTotalTurns: req.MaxTotalTurns, MaxRunDurationMs: req.MaxRunDurationMs, MaxRunTokens: req.MaxRunTokens,
	}
	if settings.Mode != "off" && settings.Mode != "safe" {
		return agent.ContinuationSettings{}, invalidContinuationSetting("mode must be off or safe")
	}
	if settings.SegmentTurns < 1 || settings.SegmentTurns > 1000 {
		return agent.ContinuationSettings{}, invalidContinuationSetting("segmentTurns must be between 1 and 1000")
	}
	// -1 is the one accepted negative value: it means "no ceiling". Any other
	// negative number is rejected rather than treated as unlimited, so a client
	// sending -5 gets an error instead of silently removing a budget.
	if settings.MaxContinuations != unlimitedContinuationBudget && (settings.MaxContinuations < 0 || settings.MaxContinuations > 64) {
		return agent.ContinuationSettings{}, invalidContinuationSetting("maxContinuations must be -1 (unlimited) or between 0 and 64")
	}
	if settings.MaxTotalTurns != unlimitedContinuationBudget && (settings.MaxTotalTurns < settings.SegmentTurns || settings.MaxTotalTurns > 10000) {
		return agent.ContinuationSettings{}, invalidContinuationSetting("maxTotalTurns must be -1 (unlimited) or between segmentTurns and 10000")
	}
	if settings.MaxRunDurationMs != unlimitedContinuationBudget && (settings.MaxRunDurationMs < 1000 || settings.MaxRunDurationMs > 86400000) {
		return agent.ContinuationSettings{}, invalidContinuationSetting("maxRunDurationMs must be -1 (unlimited) or between 1000 and 86400000")
	}
	if settings.MaxRunTokens != unlimitedContinuationBudget && (settings.MaxRunTokens < 1000 || settings.MaxRunTokens > 10000000) {
		return agent.ContinuationSettings{}, invalidContinuationSetting("maxRunTokens must be -1 (unlimited) or between 1000 and 10000000")
	}
	return settings, nil
}

// unlimitedContinuationBudget mirrors continuationUnlimited in internal/agent.
const unlimitedContinuationBudget int64 = -1

type invalidContinuationSetting string

func (e invalidContinuationSetting) Error() string { return string(e) }
