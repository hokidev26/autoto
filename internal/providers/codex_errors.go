package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"autoto/internal/codexauth"
	"autoto/internal/config"
)

// Codex error classification, upstream hints, and telemetry codes. Split out
// of codex.go to keep that file inside the source size budget.

func shouldTryNextCodexCredential(status int, code string) bool {
	if status == http.StatusUnauthorized || status == http.StatusTooManyRequests {
		return true
	}
	return status == http.StatusForbidden && isCodexSSEFailoverCode(code)
}

func isCodexSSEFailoverCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "authentication_error", "invalid_authentication", "invalid_token", "access_token_expired", "token_expired", "unauthorized", "forbidden", "permission_denied", "insufficient_permissions", "rate_limit_error", "rate_limit_exceeded", "too_many_requests", "insufficient_quota",
		// A subscription whose own budget ran out reports itself as an SSE
		// error with one of these codes rather than an HTTP 429. That is an
		// account-level condition exactly like a rate limit: the next account
		// in priority order may still have quota, so it must be tried instead
		// of surfacing "usage limit reached" while a fresh account sits idle.
		"usage_limit_reached", "usage_not_included", "quota_exceeded", "quota_exhausted", "plan_limit_reached":
		return true
	default:
		return false
	}
}

func codexHTTPErrorDetails(response *http.Response, credential codexauth.Credential, cfg config.ProviderConfig, prefix string) (error, string) {
	code, message, detail := codexJSONErrorFields(response.Body)
	safeCode := sanitizeCodexErrorCode(code, credential, cfg)
	if safeCode == "" {
		safeCode = fmt.Sprintf("http_%d", response.StatusCode)
	}
	message = codexUpstreamHint(safeCode, message, detail, credential, cfg)
	var text string
	switch {
	case safeCode != "" && message != "":
		text = fmt.Sprintf("%s：HTTP %d (%s)：%s", prefix, response.StatusCode, safeCode, message)
	case safeCode != "":
		text = fmt.Sprintf("%s：HTTP %d (%s)", prefix, response.StatusCode, safeCode)
	default:
		text = fmt.Sprintf("%s：HTTP %d", prefix, response.StatusCode)
	}
	return errors.New(boundedCodexError(text)), safeTelemetryCode(safeCode, fmt.Sprintf("http_%d", response.StatusCode))
}

// codexJSONErrorFields splits an upstream failure body into a code, a free-form
// upstream message, and the ChatGPT backend's `detail` field. detail is returned
// separately because the two message channels carry different trust: see
// codexUpstreamHint.
func codexJSONErrorFields(reader io.Reader) (string, string, string) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return "", "", ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return "", "", ""
	}
	code := codexRawJSONString(root["code"])
	message := codexRawJSONString(root["message"])
	if description := codexRawJSONString(root["error_description"]); message == "" {
		message = description
	}
	detail := codexRawJSONString(root["detail"])
	if rawError := root["error"]; len(rawError) > 0 {
		if text := codexRawJSONString(rawError); text != "" {
			if code == "" {
				code = text
			}
		} else {
			var nested map[string]json.RawMessage
			if json.Unmarshal(rawError, &nested) == nil {
				if nestedCode := codexRawJSONString(nested["code"]); nestedCode != "" {
					code = nestedCode
				}
				if nestedMessage := codexRawJSONString(nested["message"]); nestedMessage != "" {
					message = nestedMessage
				} else if nestedDescription := codexRawJSONString(nested["error_description"]); nestedDescription != "" {
					message = nestedDescription
				}
				// Request-validation rejections arrive as type
				// "invalid_request_error" with a null code, so the code alone
				// cannot classify them. The message names the offending input
				// ("No tool output found for function call X") and is the only
				// way to tell a malformed transcript from an outage.
				if codexRawJSONString(nested["type"]) == "invalid_request_error" && detail == "" {
					detail = codexRawJSONString(nested["message"])
				}
			}
		}
	}
	return strings.TrimSpace(code), strings.TrimSpace(message), strings.TrimSpace(detail)
}

// codexUpstreamHint decides how much of an upstream failure body may reach the
// operator, and is the single place that decision is made.
//
// `detail` is the ChatGPT Codex backend's request-rejection channel. It names
// the parameter, model or account condition the request got wrong — "Unsupported
// parameter: max_output_tokens", "System messages are not allowed", "requires a
// newer version of Codex" — and describes the request rather than the account,
// so it is always forwarded. Without it a rejected request surfaced only as
// "HTTP 400 (http_400)", which named no cause at all.
//
// `message` is the arbitrary upstream error channel and may carry unbounded
// internal diagnostics, so it stays restricted to codes that have been vetted as
// actionable. Both paths run through redactCodexSecrets, which scrubs tokens,
// JWTs and proxy credentials and bounds the length.
func codexUpstreamHint(code, message, detail string, credential codexauth.Credential, cfg config.ProviderConfig) string {
	hint := detail
	if hint == "" && codexSafeUpstreamHintCode(code) {
		hint = message
	}
	if hint == "" {
		return ""
	}
	redacted, _ := redactCodexSecrets(strings.TrimSpace(hint), credential, cfg)
	return redacted
}

func codexSafeUpstreamHintCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "image_generation_not_enabled":
		return true
	default:
		return false
	}
}

func codexRawJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func codexRefreshError(status int, reader io.Reader) error {
	code, description, detail := codexJSONErrorFields(reader)
	if description == "" {
		description = detail
	}
	canonical := classifyCodexRefreshFailure(code, description, status)
	message := fmt.Sprintf("Codex OAuth 刷新失败（HTTP %d）", status)
	retryable := false
	switch canonical {
	case "refresh_token_expired":
		message = "Codex refresh_token 已过期，请重新导入凭据"
		retryable = true
	case "refresh_token_reused":
		message = "Codex refresh_token 已被使用，请重新导入最新凭据"
		retryable = true
	case "refresh_token_invalidated":
		message = "Codex refresh_token 已被撤销，请重新登录后导入"
		retryable = true
	case "invalid_grant", "invalid_refresh_token":
		message = "Codex refresh_token 无效，请重新登录后导入"
		retryable = true
	case "oauth_unauthorized", "oauth_rate_limited":
		retryable = true
	}
	return newCodexCredentialFailure(canonical, providerUnavailableError(codexauth.DefaultProviderName, message).Error(), retryable)
}

func classifyCodexRefreshFailure(code, description string, status int) string {
	normalizedCode := strings.ToLower(strings.TrimSpace(code))
	detail := normalizedCode + " " + strings.ToLower(strings.TrimSpace(description))
	switch {
	case strings.Contains(detail, "reus") || strings.Contains(detail, "already used"):
		return "refresh_token_reused"
	case strings.Contains(detail, "expir"):
		return "refresh_token_expired"
	case strings.Contains(detail, "revok") || strings.Contains(detail, "invalidat"):
		return "refresh_token_invalidated"
	}
	switch normalizedCode {
	case "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
		return normalizedCode
	case "invalid_grant":
		return "invalid_grant"
	case "invalid_refresh_token", "refresh_token_invalid":
		return "invalid_refresh_token"
	}
	if status == http.StatusUnauthorized {
		return "oauth_unauthorized"
	}
	if status == http.StatusTooManyRequests {
		return "oauth_rate_limited"
	}
	return fmt.Sprintf("oauth_http_%d", status)
}

func safeCodexEventError(code, message, fallback string, credential codexauth.Credential, cfg config.ProviderConfig) string {
	safeCode := sanitizeCodexErrorCode(code, credential, cfg)
	// SSE failure events carry only the free-form upstream message channel, so
	// the vetted-code restriction applies; see codexUpstreamHint.
	message = codexUpstreamHint(safeCode, message, "", credential, cfg)
	var text string
	switch {
	case safeCode != "" && message != "":
		text = fmt.Sprintf("%s (%s)：%s", fallback, safeCode, message)
	case safeCode != "":
		text = fmt.Sprintf("%s (%s)", fallback, safeCode)
	case message != "":
		text = fallback + "：" + message
	default:
		text = fallback
	}
	return boundedCodexError(text)
}
