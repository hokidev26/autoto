package server

import (
	"fmt"
	"net/http"
	"strings"

	"autoto/internal/config"
)

// The user-maintained list of provider errors that must never be retried. The
// built-in status rule already refuses plain 4xx; this endpoint exists for
// upstreams that report a permanent fault with a 200 or a 500 and put the real
// reason only in the body, where no status check can see it.

type retryPolicySettingsRequest struct {
	NonRetryableErrorPatterns []string `json:"nonRetryableErrorPatterns"`
}

func (s *Server) retryPolicySettingsEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is unavailable")
		return
	}
	var req retryPolicySettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	patterns, err := strictNonRetryableErrorPatterns(req.NonRetryableErrorPatterns)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Serialize this durable config transaction with provider and security
	// mutations, matching continuationSettingsEndpoint. Do not hold cfgMu across
	// disk I/O.
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.cfgMu.RLock()
	updated := s.cfg
	configPath := s.configPath
	s.cfgMu.RUnlock()
	updated.Agent.NonRetryableErrorPatterns = patterns
	path := effectiveConfigPath(updated, configPath)
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusInternalServerError, "retry policy could not be persisted")
		return
	}
	if err := config.Save(path, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "retry policy could not be persisted")
		return
	}
	s.cfgMu.Lock()
	s.cfg = updated
	s.cfgMu.Unlock()
	// Applied to the live runner rather than frozen per run: a user adding a
	// pattern is reacting to a failure happening now, and the next attempt of an
	// in-flight run should already honour it.
	applied := s.runner.SetNonRetryableErrorPatterns(patterns)
	writeJSON(w, http.StatusOK, map[string]any{"nonRetryableErrorPatterns": applied, "persisted": true})
}

// strictNonRetryableErrorPatterns rejects what it cannot store rather than
// silently dropping it. Normalization would discard an over-long or too-short
// pattern, and a user who typed one deserves to hear why instead of watching it
// vanish from the list after saving.
func strictNonRetryableErrorPatterns(patterns []string) ([]string, error) {
	if len(patterns) > config.MaxNonRetryableErrorPatterns {
		return nil, invalidContinuationSetting(fmt.Sprintf("at most %d patterns are allowed", config.MaxNonRetryableErrorPatterns))
	}
	for _, pattern := range patterns {
		candidate := strings.Join(strings.Fields(pattern), " ")
		if len(candidate) < config.MinNonRetryableErrorPatternLen {
			return nil, invalidContinuationSetting(fmt.Sprintf("each pattern must be at least %d characters", config.MinNonRetryableErrorPatternLen))
		}
		if len(candidate) > config.MaxNonRetryableErrorPatternLen {
			return nil, invalidContinuationSetting(fmt.Sprintf("each pattern must be at most %d characters", config.MaxNonRetryableErrorPatternLen))
		}
	}
	return config.NormalizeNonRetryableErrorPatterns(patterns), nil
}
