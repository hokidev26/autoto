package agent

import (
	"strings"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/review"
)

// Continuation settings and per-run budget limits. Split out of
// continuation.go to keep that file inside the source size budget; this is
// the configuration layer, while continuation.go drives the run loop.

type ContinuationSettings struct {
	Mode             string `json:"mode"`
	SegmentTurns     int64  `json:"segmentTurns"`
	MaxContinuations int64  `json:"maxContinuations"`
	MaxTotalTurns    int64  `json:"maxTotalTurns"`
	MaxRunDurationMs int64  `json:"maxRunDurationMs"`
	MaxRunTokens     int64  `json:"maxRunTokens"`
}

// continuationUnlimited marks a budget the user explicitly opted out of by
// configuring a negative value. Long /goal runs can legitimately outlast any
// fixed ceiling, so the ceiling has to be removable — but only deliberately:
// zero still selects the default so existing configs keep their guard rails.
const (
	continuationUnlimited              int64 = -1
	continuationUnlimitedContinuations int64 = 1 << 40
)

// durableBudget converts an in-memory budget into the value stored on the run
// row. The runs table constrains these columns to non-negative values, and 0 is
// not a meaningful ceiling, so 0 is the durable spelling of "unlimited".
func durableBudget(limit int64) int64 {
	if limit == continuationUnlimited || limit == continuationUnlimitedContinuations {
		return 0
	}
	return limit
}

type continuationLimits struct {
	mode             string
	segmentTurns     int64
	maxContinuations int64
	maxTotalTurns    int64
	maxDuration      time.Duration
	maxTokens        int64
}

type continuationRunState struct {
	run      db.Run
	limits   continuationLimits
	deadline time.Time
}

type segmentDisposition int

const (
	segmentComplete segmentDisposition = iota
	segmentContinue
	segmentWait
	segmentBudgetExhausted
)

type segmentOutcome struct {
	disposition           segmentDisposition
	stopReason            string
	continuationReason    string
	segmentStartMessageID string
	resumeAfterID         string
	waitingTaskID         string
	turns                 int64
	inputTokens           int64
	outputTokens          int64
	planReview            review.Result
}

func (r *Runner) UpdateContinuationConfig(cfg config.AgentConfig) ContinuationSettings {
	return r.SetContinuationSettings(ContinuationSettings{
		Mode:             cfg.AutoContinuationMode,
		SegmentTurns:     int64(cfg.ContinuationSegmentTurns),
		MaxContinuations: int64(cfg.MaxContinuations),
		MaxTotalTurns:    int64(cfg.MaxTotalTurns),
		MaxRunDurationMs: cfg.MaxRunDurationMs,
		MaxRunTokens:     cfg.MaxRunTokens,
	})
}

func (r *Runner) SetContinuationSettings(settings ContinuationSettings) ContinuationSettings {
	cfg := config.AgentConfig{
		AutoContinuationMode:     settings.Mode,
		ContinuationSegmentTurns: int(settings.SegmentTurns),
		MaxContinuations:         int(settings.MaxContinuations),
		MaxTotalTurns:            int(settings.MaxTotalTurns),
		MaxRunDurationMs:         settings.MaxRunDurationMs,
		MaxRunTokens:             settings.MaxRunTokens,
	}
	limits := continuationLimitsForConfig(cfg)
	normalized := continuationSettingsFromLimits(limits)
	if r == nil {
		return normalized
	}
	r.continuationMu.Lock()
	r.continuationConfig.AutoContinuationMode = normalized.Mode
	r.continuationConfig.ContinuationSegmentTurns = int(normalized.SegmentTurns)
	r.continuationConfig.MaxContinuations = int(normalized.MaxContinuations)
	r.continuationConfig.MaxTotalTurns = int(normalized.MaxTotalTurns)
	r.continuationConfig.MaxRunDurationMs = normalized.MaxRunDurationMs
	r.continuationConfig.MaxRunTokens = normalized.MaxRunTokens
	r.continuationMu.Unlock()
	return normalized
}

func (r *Runner) GetContinuationSettings() ContinuationSettings {
	return continuationSettingsFromLimits(r.currentContinuationLimits())
}

func continuationSettingsFromLimits(limits continuationLimits) ContinuationSettings {
	return ContinuationSettings{
		Mode:             limits.mode,
		SegmentTurns:     limits.segmentTurns,
		MaxContinuations: limits.maxContinuations,
		MaxTotalTurns:    limits.maxTotalTurns,
		MaxRunDurationMs: limits.maxDuration.Milliseconds(),
		MaxRunTokens:     limits.maxTokens,
	}
}

func (r *Runner) currentContinuationLimits() continuationLimits {
	if r == nil {
		return continuationLimitsForConfig(config.AgentConfig{})
	}
	r.continuationMu.RLock()
	cfg := r.continuationConfig
	r.continuationMu.RUnlock()
	if cfg.AutoContinuationMode == "" && cfg.ContinuationSegmentTurns == 0 && cfg.MaxContinuations == 0 && cfg.MaxTotalTurns == 0 && cfg.MaxRunDurationMs == 0 && cfg.MaxRunTokens == 0 {
		cfg = r.cfg
	}
	return continuationLimitsForConfig(cfg)
}

func (r *Runner) freezeContinuationLimits(runID string, limits continuationLimits) {
	if r == nil || strings.TrimSpace(runID) == "" {
		return
	}
	r.continuationMu.Lock()
	if r.continuationRunLimits == nil {
		r.continuationRunLimits = make(map[string]continuationLimits)
	}
	if _, exists := r.continuationRunLimits[runID]; !exists {
		r.continuationRunLimits[runID] = limits
	}
	r.continuationMu.Unlock()
}

func (r *Runner) frozenContinuationLimits(runID string) (continuationLimits, bool) {
	if r == nil || strings.TrimSpace(runID) == "" {
		return continuationLimits{}, false
	}
	r.continuationMu.RLock()
	limits, ok := r.continuationRunLimits[runID]
	r.continuationMu.RUnlock()
	return limits, ok
}

func continuationLimitsForConfig(cfg config.AgentConfig) continuationLimits {
	mode := strings.ToLower(strings.TrimSpace(cfg.AutoContinuationMode))
	if mode != continuationModeOff && mode != continuationModeSafe {
		mode = continuationModeSafe
	}
	segmentTurns := int64(cfg.ContinuationSegmentTurns)
	if segmentTurns == 0 {
		// 0 means unset; fall back to the per-run MaxTurns when provided.
		segmentTurns = int64(cfg.MaxTurns)
	}
	// segmentTurns <= 0 (including -1) means no ceiling — the loop will run
	// until a stop reason, error, or cross-segment budget fires. Users who
	// want a hard cap per segment can set it via Settings → Execution Budget.
	if segmentTurns > 1000 {
		segmentTurns = 1000
	}
	// Every cross-segment budget below reads the same way: a negative value (or
	// an unset one) means "no ceiling", and a positive value is honoured within
	// its documented bounds. Long /goal runs legitimately outlast any fixed
	// number, so the ceiling has to be opt-in rather than assumed.
	maxContinuations := cfg.MaxContinuations
	switch {
	case maxContinuations <= 0:
		maxContinuations = int(continuationUnlimitedContinuations)
	case maxContinuations > 64:
		maxContinuations = 64
	}
	maxTotalTurns := int64(cfg.MaxTotalTurns)
	switch {
	case maxTotalTurns < 0:
		maxTotalTurns = continuationUnlimited
	case maxTotalTurns == 0:
		// A config that only ever set the legacy MaxTurns keeps meaning what it
		// said; an unset total falls through to unlimited like the others.
		maxTotalTurns = int64(cfg.MaxTurns)
		if maxTotalTurns <= 0 {
			maxTotalTurns = continuationUnlimited
		}
	case maxTotalTurns > 10000:
		maxTotalTurns = 10000
	}
	if maxTotalTurns != continuationUnlimited && segmentTurns > maxTotalTurns {
		segmentTurns = maxTotalTurns
	}
	maxDurationMS := cfg.MaxRunDurationMs
	switch {
	case maxDurationMS <= 0:
		maxDurationMS = continuationUnlimited
	case maxDurationMS < 1000:
		maxDurationMS = 1000
	}
	maxTokens := cfg.MaxRunTokens
	switch {
	case maxTokens <= 0:
		maxTokens = continuationUnlimited
	case maxTokens < 1000:
		maxTokens = 1000
	}
	return continuationLimits{
		mode:             mode,
		segmentTurns:     segmentTurns,
		maxContinuations: int64(maxContinuations),
		maxTotalTurns:    maxTotalTurns,
		maxDuration:      time.Duration(maxDurationMS) * time.Millisecond,
		maxTokens:        maxTokens,
	}
}
