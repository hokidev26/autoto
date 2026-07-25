package providers

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/codexauth"
	"autoto/internal/config"
)

var (
	codexURLCredentialPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/@\s]+)@`)
	codexJWTPattern           = regexp.MustCompile(`[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}`)
	codexPrefixedTokenPattern = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(?:bearer\s+[A-Za-z0-9._~+/=-]{4,}|(?:sk|rt|pk)[_-][A-Za-z0-9._~+/=-]{8,})`)
	codexLongTokenPattern     = regexp.MustCompile(`[A-Za-z0-9_+/-]{32,}={0,2}`)
)

func redactCodexSecrets(message string, credential codexauth.Credential, cfg config.ProviderConfig) (string, bool) {
	original := message
	message = redactCodexPEMPrivateKeys(message)
	message = codexURLCredentialPattern.ReplaceAllString(message, `${1}[redacted]@`)
	secrets := []string{credential.AccessToken, credential.RefreshToken, credential.IDToken, cfg.ProxyUsername, cfg.ProxyPassword}
	for _, header := range cfg.RequestHeaders {
		secrets = append(secrets, header.Value)
	}
	if proxyURL, err := url.Parse(strings.TrimSpace(cfg.ProxyURL)); err == nil && proxyURL.User != nil {
		secrets = append(secrets, proxyURL.User.Username(), proxyURL.User.String())
		if password, ok := proxyURL.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = codexJWTPattern.ReplaceAllString(message, "[redacted]")
	message = codexPrefixedTokenPattern.ReplaceAllString(message, `${1}[redacted]`)
	message = codexLongTokenPattern.ReplaceAllString(message, "[redacted]")
	changed := message != original
	message = truncateCodexUTF8(message, codexMaxErrorDetailBytes)
	return message, changed
}

func redactCodexPEMPrivateKeys(message string) string {
	for searchFrom := 0; searchFrom < len(message); {
		beginRelative := strings.Index(message[searchFrom:], "-----BEGIN ")
		if beginRelative < 0 {
			return message
		}
		begin := searchFrom + beginRelative
		labelStart := begin + len("-----BEGIN ")
		headerEndRelative := strings.Index(message[labelStart:], "-----")
		if headerEndRelative < 0 {
			return message[:begin] + "[redacted]"
		}
		headerEnd := labelStart + headerEndRelative + len("-----")
		label := message[labelStart : labelStart+headerEndRelative]
		if !strings.Contains(label, "PRIVATE KEY") || len(label) > 64 {
			searchFrom = headerEnd
			continue
		}
		endMarker := "-----END " + label + "-----"
		endRelative := strings.Index(message[headerEnd:], endMarker)
		if endRelative < 0 {
			return message[:begin] + "[redacted]"
		}
		end := headerEnd + endRelative + len(endMarker)
		message = message[:begin] + "[redacted]" + message[end:]
		searchFrom = begin + len("[redacted]")
	}
	return message
}

func sanitizeCodexErrorCode(code string, credential codexauth.Credential, cfg config.ProviderConfig) string {
	redactedCode, redacted := redactCodexSecrets(strings.TrimSpace(code), credential, cfg)
	if redactedCode == "" {
		return ""
	}
	if redacted || strings.Contains(redactedCode, "[redacted]") || looksLikeCodexSecretToken(redactedCode) || looksLikeEmbeddedJWT(redactedCode) {
		return "redacted"
	}
	if len(redactedCode) > 64 {
		return "invalid_upstream_code"
	}
	sanitized := safeTelemetryCode(redactedCode, "invalid_upstream_code")
	if sanitized == "" {
		return "invalid_upstream_code"
	}
	return sanitized
}

func looksLikeCodexSecretToken(value string) bool {
	return codexPrefixedTokenPattern.MatchString(value) || codexLongTokenPattern.MatchString(value)
}

func looksLikeEmbeddedJWT(value string) bool {
	return codexJWTPattern.MatchString(value)
}

func boundedCodexError(value string) string {
	return truncateCodexUTF8(strings.TrimSpace(value), codexMaxErrorOutputBytes)
}

func truncateCodexUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	suffix := "…[truncated]"
	if len(suffix) >= limit {
		return suffix[:limit]
	}
	end := limit - len(suffix)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix
}

func applyCodexJWTMetadata(credential *codexauth.Credential) {
	if credential == nil {
		return
	}
	access := parseCodexJWTMetadata(credential.AccessToken)
	idToken := parseCodexJWTMetadata(credential.IDToken)
	if access.AccountID == "" {
		access.AccountID = idToken.AccountID
	}
	if access.Email == "" {
		access.Email = idToken.Email
	}
	if access.PlanType == "" {
		access.PlanType = idToken.PlanType
	}
	if access.ExpiresAt == 0 {
		access.ExpiresAt = idToken.ExpiresAt
	}
	if access.AccountID != "" {
		credential.AccountID = access.AccountID
	}
	if access.Email != "" {
		credential.Email = access.Email
	}
	if access.PlanType != "" {
		credential.PlanType = access.PlanType
	}
	if access.ExpiresAt > 0 {
		credential.Expired = time.Unix(access.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
}

type codexJWTMetadata struct {
	AccountID string
	Email     string
	PlanType  string
	ExpiresAt int64
}

func parseCodexJWTMetadata(token string) codexJWTMetadata {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return codexJWTMetadata{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return codexJWTMetadata{}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return codexJWTMetadata{}
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	return codexJWTMetadata{
		AccountID: firstCodexString(auth, "chatgpt_account_id", "account_id"),
		Email:     firstCodexString(profile, "email"),
		PlanType:  firstCodexString(auth, "chatgpt_plan_type", "plan_type"),
		ExpiresAt: codexInt64(claims["exp"]),
	}
}

func firstCodexString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func codexInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}
