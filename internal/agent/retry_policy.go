package agent

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"autoto/internal/config"
)

// Whether a failed provider call is worth sending again. Split out of
// continuation.go and runner_model.go so the whole decision reads in one place:
// the built-in status rule, the user-maintained pattern list, and the segment
// gate that continuation.go consults all answer the same question.

// providerStatusPattern reads the numeric status out of the text built by
// providers.providerHTTPFailedText. Matching the whole phrase rather than a bare
// "404" keeps a token count or a model name from being read as a status.
var providerStatusPattern = regexp.MustCompile(`model request failed:\s*(\d{3})`)

// permanentProviderStatus reports whether a status will fail again no matter how
// many times it is sent. A wrong base URL answers 404 to every attempt, so
// retrying it four times only delays the error the user needs to read. 408, 409,
// 425 and 429 stay retryable, as does anything 5xx.
func permanentProviderStatus(err error) bool {
	if err == nil {
		return false
	}
	match := providerStatusPattern.FindStringSubmatch(strings.ToLower(err.Error()))
	if len(match) < 2 {
		return false
	}
	status, convErr := strconv.Atoi(match[1])
	if convErr != nil || status < 400 || status > 499 {
		return false
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	default:
		return true
	}
}

func isTransientProviderError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	if permanentProviderStatus(err) {
		return false
	}
	for _, marker := range []string{"401", "403", "unauthorized", "forbidden", "invalid_request", "invalid request", "invalid schema", "context canceled"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	for _, marker := range []string{"408", "409", "425", "429", "500", "502", "503", "504", "rate limit", "too many requests", "temporar", "timeout", "timed out", "deadline exceeded", "eof", "unexpected end of json input", "connection reset", "server error", "service unavailable", "bad gateway", "gateway timeout"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// SetNonRetryableErrorPatterns replaces the user-maintained permanent-error
// list. Returns the normalized list actually stored so the caller can report
// what took effect rather than what was sent.
func (r *Runner) SetNonRetryableErrorPatterns(patterns []string) []string {
	normalized := config.NormalizeNonRetryableErrorPatterns(patterns)
	if r == nil {
		return normalized
	}
	r.retryPolicyMu.Lock()
	r.nonRetryableErrorPatterns = normalized
	r.retryPolicyMu.Unlock()
	return normalized
}

// NonRetryableErrorPatterns returns a copy: the caller must not be able to
// mutate the live list that every provider failure reads.
func (r *Runner) NonRetryableErrorPatterns() []string {
	if r == nil {
		return nil
	}
	r.retryPolicyMu.RLock()
	defer r.retryPolicyMu.RUnlock()
	if len(r.nonRetryableErrorPatterns) == 0 {
		return nil
	}
	return append([]string(nil), r.nonRetryableErrorPatterns...)
}

// matchesNonRetryablePattern reports whether a configured pattern appears in the
// error text. Patterns are stored lowercased, so the comparison is a plain
// substring test.
func (r *Runner) matchesNonRetryablePattern(err error) bool {
	if r == nil || err == nil {
		return false
	}
	r.retryPolicyMu.RLock()
	patterns := r.nonRetryableErrorPatterns
	r.retryPolicyMu.RUnlock()
	if len(patterns) == 0 {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, pattern := range patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

// permanentProviderError combines the built-in status rule with the user's list.
// Both answer the same question: will sending this again produce the same
// failure?
func (r *Runner) permanentProviderError(err error) bool {
	return permanentProviderStatus(err) || r.matchesNonRetryablePattern(err)
}

func (r *Runner) isTransientProviderError(err error) bool {
	if r.matchesNonRetryablePattern(err) {
		return false
	}
	return isTransientProviderError(err)
}

// retryableProviderError reports whether a failed segment can be resumed. It
// requires the partial-assistant persist to have produced a resume point, which
// runSegment only does for provider faults, so this cannot silently retry an
// unrelated failure such as a store or settlement error.
func (r *Runner) retryableProviderError(state continuationRunState, outcome segmentOutcome, segmentErr error) bool {
	if segmentErr == nil || errors.Is(segmentErr, context.Canceled) || errors.Is(segmentErr, context.DeadlineExceeded) {
		return false
	}
	if state.limits.mode != continuationModeSafe {
		return false
	}
	if outcome.continuationReason != continuationReasonProviderError {
		return false
	}
	// A 4xx that is not a timeout, conflict or rate limit is a configuration
	// fault, not a transient one: it answers the same way every time. Retrying it
	// four times with backoff only delays the error the user has to act on. The
	// user's own patterns are consulted here too, for upstreams that report a
	// permanent fault with a 200 or a 500.
	if r.permanentProviderError(segmentErr) {
		return false
	}
	// A resume point is not required. When the failed call persisted nothing the
	// caller re-runs the same segment in place, which is safe precisely because
	// there is no partial output to duplicate. Requiring one here is what made
	// this whole path unreachable for a fault on the first turn.
	return true
}
