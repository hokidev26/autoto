package server

import (
	"net/http"
	"strings"

	"autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/tools"
)

type continuationSettingsRequest struct {
	Mode             string `json:"mode"`
	SegmentTurns     int64  `json:"segmentTurns"`
	MaxContinuations int64  `json:"maxContinuations"`
	MaxTotalTurns    int64  `json:"maxTotalTurns"`
	MaxRunDurationMs int64  `json:"maxRunDurationMs"`
	MaxRunTokens     int64  `json:"maxRunTokens"`
	// Pointer so an older client that omits the field keeps the stored value
	// instead of resetting it to zero, which would silently mean "never retry".
	MaxTransientRetries *int `json:"maxTransientRetries"`
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
	retries, retriesProvided, err := strictMaxTransientRetries(req.MaxTransientRetries)
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
	if retriesProvided {
		updated.Agent.MaxTransientRetries = retries
	}
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
	// Applied to the live runner too, so the new ceiling covers the next turn
	// rather than waiting for a restart to re-read the config.
	appliedRetries := s.runner.MaxTransientRetries()
	if retriesProvided {
		appliedRetries = s.runner.SetMaxTransientRetries(retries)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"continuation":        applied,
		"maxTransientRetries": appliedRetries,
		"persisted":           true,
	})
}

type backgroundRuntimeSettingsRequest struct {
	WorkerCount          int  `json:"workerCount"`
	PerAgentLimit        int  `json:"perAgentLimit"`
	AllowNestedSubagents bool `json:"allowNestedSubagents"`
	MaxSubagentDepth     int  `json:"maxSubagentDepth"`
}

func (s *Server) backgroundRuntimeSettingsEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.backgroundRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "background runtime controller is unavailable")
		return
	}
	var req backgroundRuntimeSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := strictBackgroundRuntimeSettings(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	previousRuntime := s.backgroundRuntime.BackgroundRuntimeSettings()
	applied, err := s.backgroundRuntime.UpdateBackgroundRuntimeSettings(settings)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "background runtime settings could not be applied")
		return
	}

	s.cfgMu.RLock()
	updated := s.cfg
	configPath := s.configPath
	s.cfgMu.RUnlock()
	updated.Background = config.BackgroundConfig{
		WorkerCount:          applied.WorkerCount,
		PerAgentLimit:        applied.PerAgentLimit,
		AllowNestedSubagents: applied.AllowNestedSubagents,
		MaxSubagentDepth:     applied.MaxSubagentDepth,
	}
	path := effectiveConfigPath(updated, configPath)
	if strings.TrimSpace(path) == "" {
		_, _ = s.backgroundRuntime.UpdateBackgroundRuntimeSettings(previousRuntime)
		writeError(w, http.StatusInternalServerError, "background runtime settings could not be persisted")
		return
	}
	if err := config.Save(path, updated); err != nil {
		_, _ = s.backgroundRuntime.UpdateBackgroundRuntimeSettings(previousRuntime)
		writeError(w, http.StatusInternalServerError, "background runtime settings could not be persisted")
		return
	}
	s.cfgMu.Lock()
	s.cfg = updated
	s.cfgMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"backgroundTasks": applied, "persisted": true})
}

func strictBackgroundRuntimeSettings(req backgroundRuntimeSettingsRequest) (tools.BackgroundRuntimeSettings, error) {
	settings := tools.BackgroundRuntimeSettings{
		WorkerCount:          req.WorkerCount,
		PerAgentLimit:        req.PerAgentLimit,
		AllowNestedSubagents: req.AllowNestedSubagents,
		MaxSubagentDepth:     req.MaxSubagentDepth,
	}
	if settings.WorkerCount < 1 || settings.WorkerCount > 16 {
		return tools.BackgroundRuntimeSettings{}, invalidContinuationSetting("workerCount must be between 1 and 16")
	}
	if settings.PerAgentLimit < 1 || settings.PerAgentLimit > 8 {
		return tools.BackgroundRuntimeSettings{}, invalidContinuationSetting("perAgentLimit must be between 1 and 8")
	}
	if settings.MaxSubagentDepth < 2 || settings.MaxSubagentDepth > 4 {
		return tools.BackgroundRuntimeSettings{}, invalidContinuationSetting("maxSubagentDepth must be between 2 and 4")
	}
	return settings, nil
}

// strictMaxTransientRetries validates the retry ceiling on the same terms as the
// budgets beside it: -1 removes the ceiling, and any other negative number is an
// error rather than a second spelling of unlimited. Reports whether the field was
// present at all, so omitting it leaves the stored value alone.
//
// Zero is accepted and is not unlimited: it means a transient provider failure is
// reported on the first attempt. The upper bound matches maxContinuations, since
// both count repeats of work the user is waiting on.
func strictMaxTransientRetries(value *int) (int, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	retries := *value
	if retries == int(unlimitedContinuationBudget) {
		return retries, true, nil
	}
	if retries < 0 || retries > 64 {
		return 0, false, invalidContinuationSetting("maxTransientRetries must be -1 (unlimited) or between 0 and 64")
	}
	return retries, true, nil
}

func strictContinuationSettings(req continuationSettingsRequest) (agent.ContinuationSettings, error) {
	settings := agent.ContinuationSettings{
		Mode: strings.ToLower(strings.TrimSpace(req.Mode)), SegmentTurns: req.SegmentTurns, MaxContinuations: req.MaxContinuations,
		MaxTotalTurns: req.MaxTotalTurns, MaxRunDurationMs: req.MaxRunDurationMs, MaxRunTokens: req.MaxRunTokens,
	}
	if settings.Mode != "off" && settings.Mode != "safe" {
		return agent.ContinuationSettings{}, invalidContinuationSetting("mode must be off or safe")
	}
	// -1 is the one accepted negative value: it means "no ceiling". Any other
	// negative number is rejected rather than treated as unlimited, so a client
	// sending -5 gets an error instead of silently removing a budget.
	//
	// segmentTurns accepts -1 for the same reason as the budgets below. Requiring
	// 1..1000 here made the endpoint unable to express its own shipped default
	// (config.Default sets -1, and continuationLimitsForConfig reads <=0 as "no
	// ceiling"), so any client that saved budgets had to carry a positive segment
	// cap forward forever with no way to clear it.
	if settings.SegmentTurns != unlimitedContinuationBudget && (settings.SegmentTurns < 1 || settings.SegmentTurns > 1000) {
		return agent.ContinuationSettings{}, invalidContinuationSetting("segmentTurns must be -1 (unlimited) or between 1 and 1000")
	}
	if settings.MaxContinuations != unlimitedContinuationBudget && (settings.MaxContinuations < 0 || settings.MaxContinuations > 64) {
		return agent.ContinuationSettings{}, invalidContinuationSetting("maxContinuations must be -1 (unlimited) or between 0 and 64")
	}
	// The segmentTurns floor only applies when both are real ceilings: an
	// unlimited segment cap cannot be a lower bound for the total.
	if settings.MaxTotalTurns != unlimitedContinuationBudget {
		floor := settings.SegmentTurns
		if floor == unlimitedContinuationBudget {
			floor = 1
		}
		if settings.MaxTotalTurns < floor || settings.MaxTotalTurns > 10000 {
			return agent.ContinuationSettings{}, invalidContinuationSetting("maxTotalTurns must be -1 (unlimited) or between segmentTurns and 10000")
		}
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
