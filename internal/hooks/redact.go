package hooks

import (
	"encoding/json"
	"regexp"
	"strings"
)

var sensitiveTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)[^\s,;]+`),
	regexp.MustCompile(`(?i)((?:api[-_]?key|token|secret|password|cookie)\s*[:=]\s*)[^\s,;]+`),
	regexp.MustCompile(`(?i)(env|secret|vault|keychain):[A-Za-z0-9_./-]+`),
}

func RedactText(value string) string {
	for _, pattern := range sensitiveTextPatterns {
		value = pattern.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	return value
}

func RedactJSON(raw json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		encoded, _ := json.Marshal(RedactText(string(raw)))
		return encoded
	}
	redactValue(&value, "")
	encoded, _ := json.Marshal(value)
	return encoded
}

func redactValue(value *any, key string) {
	switch current := (*value).(type) {
	case map[string]any:
		for name, child := range current {
			if sensitiveKey(name) {
				current[name] = "[REDACTED]"
				continue
			}
			redactValue(&child, name)
			current[name] = child
		}
	case []any:
		for index, child := range current {
			redactValue(&child, key)
			current[index] = child
		}
	case string:
		*value = RedactText(current)
	}
}

func sensitiveKey(value string) bool {
	value = strings.ToLower(value)
	for _, fragment := range []string{"authorization", "cookie", "password", "token", "secret", "api_key", "apikey", "credential"} {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
