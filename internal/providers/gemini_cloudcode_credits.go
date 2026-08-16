package providers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const geminiGoogleOneAICreditType = "GOOGLE_ONE_AI"

// A short RetryInfo window is a rate limit, not model-quota exhaustion.
// CLIProxyAPI uses the same 5-minute cutoff before treating RATE_LIMIT_EXCEEDED
// as a full quota event that may debit Google One credits.
const geminiCloudCodeQuotaRetryDelay = 5 * time.Minute

func injectGeminiGoogleOneCredits(payload map[string]any) {
	if payload == nil {
		return
	}
	payload["enabledCreditTypes"] = []string{geminiGoogleOneAICreditType}
}

// geminiCloudCodeShouldUseGoogleOneCredits reports whether a failed generate
// should be retried once on the same account with enabledCreditTypes set to
// GOOGLE_ONE_AI. Empty 429 bodies stay on the account-failover path: those are
// rate limits or mock/test refusals, not Antigravity quota exhaustion.
func geminiCloudCodeShouldUseGoogleOneCredits(status int, body []byte) bool {
	if status != http.StatusTooManyRequests || len(strings.TrimSpace(string(body))) == 0 {
		return false
	}
	rpc := parseGeminiCloudCodeRPCError(body)
	lower := strings.ToLower(string(body))
	retryAfter, hasRetry := geminiCloudCodeRetryDelay(rpc)

	for _, reason := range rpc.reasons {
		switch strings.ToUpper(strings.TrimSpace(reason)) {
		case "QUOTA_EXHAUSTED":
			return true
		case "RATE_LIMIT_EXCEEDED":
			return hasRetry && retryAfter >= geminiCloudCodeQuotaRetryDelay
		}
	}
	if strings.Contains(lower, "quota_exhausted") || strings.Contains(lower, "quota exhausted") {
		return true
	}
	// RESOURCE_EXHAUSTED with only RetryInfo (the official "capacity will reset
	// after 3s" shape) is still a quota/capacity refusal, not RATE_LIMIT_EXCEEDED.
	// Skipping credits here left generate failing while fetchAvailableModels
	// still showed 100% remaining on other models.
	if strings.EqualFold(rpc.status, "RESOURCE_EXHAUSTED") || strings.Contains(lower, "resource_exhausted") {
		return true
	}
	return false
}

type geminiCloudCodeRPCError struct {
	status  string
	message string
	reasons []string
	delays  []string
}

func parseGeminiCloudCodeRPCError(raw []byte) geminiCloudCodeRPCError {
	var wrap struct {
		Error    geminiJSONRPCError `json:"error"`
		Response struct {
			Error geminiJSONRPCError `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(raw, &wrap) != nil {
		return geminiCloudCodeRPCError{}
	}
	chosen := wrap.Error
	if strings.TrimSpace(chosen.Status) == "" && strings.TrimSpace(chosen.Message) == "" {
		chosen = wrap.Response.Error
	}
	parsed := geminiCloudCodeRPCError{
		status:  strings.TrimSpace(chosen.Status),
		message: strings.TrimSpace(chosen.Message),
	}
	for _, detail := range chosen.Details {
		if reason := strings.TrimSpace(detail.Reason); reason != "" {
			parsed.reasons = append(parsed.reasons, reason)
		}
		if delay := strings.TrimSpace(detail.RetryDelay); delay != "" {
			parsed.delays = append(parsed.delays, delay)
		}
	}
	return parsed
}

type geminiJSONRPCError struct {
	Message string `json:"message"`
	Status  string `json:"status"`
	Details []struct {
		Type       string `json:"@type"`
		Reason     string `json:"reason"`
		RetryDelay string `json:"retryDelay"`
	} `json:"details"`
}

func geminiCloudCodeRetryDelay(rpc geminiCloudCodeRPCError) (time.Duration, bool) {
	for _, raw := range rpc.delays {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed, true
		}
	}
	return 0, false
}
