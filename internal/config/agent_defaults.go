package config

import (
	"errors"
	"fmt"
)

// Agent and context-management types and defaults. Split out of defaults.go,
// which had grown past the per-file size budget; these are a self-contained
// concern with no other dependency on the rest of that file.

type ContextManagementWindowConfig struct {
	PruneStart   int `json:"pruneStart"`
	CompactStart int `json:"compactStart"`
}

type ContextManagementConfig struct {
	CompactKeepTurns int                           `json:"compactKeepTurns"`
	MaxPrunePercent  int                           `json:"maxPrunePercent"`
	MinPrunePercent  int                           `json:"minPrunePercent"`
	Standard         ContextManagementWindowConfig `json:"standard"`
	Large            ContextManagementWindowConfig `json:"large"`
}

// Token counts are chars/4 estimates with a real error margin of ±10-20%, and
// the model's own reply must also fit inside the window. Triggering compaction
// at 99% therefore often meant the request had already overflowed before the
// safety action could run. The defaults leave enough headroom for both the
// estimation error and the response; large windows get proportionally more
// absolute slack, so their percentages sit slightly higher.
var (
	defaultStandardContextWindow = ContextManagementWindowConfig{PruneStart: 80, CompactStart: 90}
	defaultLargeContextWindow    = ContextManagementWindowConfig{PruneStart: 85, CompactStart: 92}

	// legacyContextWindowDefaults is the shipped default before config schema
	// version 4. Configs still carrying it verbatim are migrated to the new
	// defaults; anything else is a deliberate user choice and is kept.
	legacyContextWindowDefaults = ContextManagementWindowConfig{PruneStart: 95, CompactStart: 99}
)

type AgentConfig struct {
	DefaultModel string `json:"defaultModel"`
	SummaryModel string `json:"summaryModel"`
	ReviewModel  string `json:"reviewModel"`
	// SafetyModel judges whether a risky action may proceed. It falls back to
	// SummaryModel when unset, but is separate because the summary model is
	// routinely pointed at a small cheap model for titles and compaction, and
	// that silently downgrades the safety gate to the same tier.
	SafetyModel            string              `json:"safetyModel,omitempty"`
	SubagentModels         map[string]string   `json:"subagentModels,omitempty"`
	SubagentModelPools     map[string][]string `json:"subagentModelPools,omitempty"`
	DefaultPermissionMode  string              `json:"defaultPermissionMode"`
	DefaultStartInPlanMode bool                `json:"defaultStartInPlanMode"`
	MaxTurns               int                 `json:"maxTurns"`
	ContextTokenLimit      int                 `json:"contextTokenLimit"`
	FirstTokenTimeoutMs    int                 `json:"firstTokenTimeoutMs"`
	// StreamIdleTimeoutMs bounds the gap between provider stream events once the
	// stream is under way. FirstTokenTimeoutMs only covers the wait for the first
	// event, so a stream that opened and then went silent left the turn parked on
	// a channel receive forever. Zero disables the guard.
	StreamIdleTimeoutMs      int    `json:"streamIdleTimeoutMs"`
	MaxTransientRetries      int    `json:"maxTransientRetries"`
	AutoContinuationMode     string `json:"autoContinuationMode"`
	ContinuationSegmentTurns int    `json:"continuationSegmentTurns"`
	MaxContinuations         int    `json:"maxContinuations"`
	MaxTotalTurns            int    `json:"maxTotalTurns"`
	MaxRunDurationMs         int64  `json:"maxRunDurationMs"`
	MaxRunTokens             int64  `json:"maxRunTokens"`
	// NonRetryableErrorPatterns are case-insensitive substrings of a provider
	// error that mark it permanent, so the run reports it instead of spending the
	// retry budget on a fault that answers the same way every time. The built-in
	// status rule already covers plain 4xx; this is for upstreams that return 200
	// or 500 with the real reason only in the body.
	NonRetryableErrorPatterns []string `json:"nonRetryableErrorPatterns,omitempty"`
	// ToolOutputSpillBytes caps how much of a tool result reaches the model in
	// UTF-8 bytes. A larger result is written to disk under the Autoto home and
	// replaced with a head/tail preview plus the file path, which the model reads
	// back with Read or Grep. Zero disables spilling.
	ToolOutputSpillBytes int `json:"toolOutputSpillBytes"`
	// RepeatToolCallThresholds are the consecutive-identical-call counts that
	// earn an escalating reminder. The first entry is the gentle tier and the
	// rest quote the arguments. An empty list disables the detector.
	RepeatToolCallThresholds []int `json:"repeatToolCallThresholds"`
}

func normalizeContextManagementConfig(value ContextManagementConfig) ContextManagementConfig {
	if value.CompactKeepTurns <= 0 {
		value.CompactKeepTurns = 2
	}
	if value.CompactKeepTurns > 100 {
		value.CompactKeepTurns = 100
	}
	if value.MaxPrunePercent <= 0 {
		value.MaxPrunePercent = 80
	}
	if value.MaxPrunePercent > 100 {
		value.MaxPrunePercent = 100
	}
	if value.MinPrunePercent <= 0 {
		value.MinPrunePercent = 30
	}
	if value.MinPrunePercent > value.MaxPrunePercent {
		value.MinPrunePercent = value.MaxPrunePercent
	}
	normalizeWindow := func(window, fallback ContextManagementWindowConfig) ContextManagementWindowConfig {
		if window.PruneStart <= 0 {
			window.PruneStart = fallback.PruneStart
		}
		if window.PruneStart > 100 {
			window.PruneStart = 100
		}
		if window.CompactStart <= 0 {
			window.CompactStart = fallback.CompactStart
		}
		if window.CompactStart > 100 {
			window.CompactStart = 100
		}
		return window
	}
	value.Standard = normalizeWindow(value.Standard, defaultStandardContextWindow)
	value.Large = normalizeWindow(value.Large, defaultLargeContextWindow)
	return value
}

func (c ContextManagementConfig) Normalized() ContextManagementConfig {
	return normalizeContextManagementConfig(c)
}

func (c ContextManagementConfig) Validate() error {
	if c.CompactKeepTurns < 1 || c.CompactKeepTurns > 100 {
		return errors.New("compactKeepTurns must be between 1 and 100")
	}
	if c.MinPrunePercent < 1 || c.MinPrunePercent > 100 {
		return errors.New("minPrunePercent must be between 1 and 100")
	}
	if c.MaxPrunePercent < 1 || c.MaxPrunePercent > 100 {
		return errors.New("maxPrunePercent must be between 1 and 100")
	}
	if c.MinPrunePercent > c.MaxPrunePercent {
		return errors.New("minPrunePercent must not exceed maxPrunePercent")
	}
	validateWindow := func(name string, window ContextManagementWindowConfig) error {
		if window.PruneStart < 1 || window.PruneStart > 100 {
			return fmt.Errorf("%s.pruneStart must be between 1 and 100", name)
		}
		if window.CompactStart < 1 || window.CompactStart > 100 {
			return fmt.Errorf("%s.compactStart must be between 1 and 100", name)
		}
		return nil
	}
	if err := validateWindow("standard", c.Standard); err != nil {
		return err
	}
	return validateWindow("large", c.Large)
}

func (c ContextManagementConfig) WindowForLimit(limit int) ContextManagementWindowConfig {
	c = normalizeContextManagementConfig(c)
	if limit > 600000 {
		return c.Large
	}
	return c.Standard
}
