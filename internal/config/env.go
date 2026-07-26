package config

import (
	"os"
	"strconv"
	"strings"

	"autoto/internal/compat"
)

// Environment-variable readers for configuration defaults. Split out of
// defaults.go, which had grown past the per-file size budget; these are a
// self-contained concern with no other dependency on the rest of that file.

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func firstEnvFallback(report *compat.Report, canonical string, fallbackKeys ...string) string {
	if value := os.Getenv(canonical); value != "" {
		return value
	}
	for _, key := range fallbackKeys {
		if value := os.Getenv(key); value != "" {
			reportLegacyEnv(report, key, canonical)
			return value
		}
	}
	return ""
}

func envUsageKey(name string) string {
	return "env:" + name
}

func reportLegacyEnv(report *compat.Report, legacy, replacement string) {
	if report == nil || !strings.HasPrefix(legacy, "CODEHARBOR_") {
		return
	}
	report.Add(compat.Usage{
		Key:         envUsageKey(legacy),
		Legacy:      legacy,
		Replacement: replacement,
		Kind:        "environment-variable",
	})
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	return getenvIntFallback([]string{key}, fallback)
}

func getenvIntFallback(keys []string, fallback int) int {
	return getenvIntFallbackReported(nil, keys, fallback)
}

func getenvIntFallbackReported(report *compat.Report, keys []string, fallback int) int {
	for i, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return fallback
		}
		if i > 0 && len(keys) > 0 {
			reportLegacyEnv(report, key, keys[0])
		}
		return parsed
	}
	return fallback
}

func getenvBool(key string, fallback bool) bool {
	return getenvBoolFallback([]string{key}, fallback)
}

func getenvBoolFallback(keys []string, fallback bool) bool {
	return getenvBoolFallbackReported(nil, keys, fallback)
}

func getenvBoolFallbackReported(report *compat.Report, keys []string, fallback bool) bool {
	value, ok := lookupBoolEnvFallbackReported(report, keys...)
	if !ok {
		return fallback
	}
	return value
}

func lookupBoolEnvFallback(keys ...string) (bool, bool) {
	return lookupBoolEnvFallbackReported(nil, keys...)
}

func lookupBoolEnvFallbackReported(report *compat.Report, keys ...string) (bool, bool) {
	for i, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			continue
		}
		value, ok := lookupBoolEnv(key)
		if ok && i > 0 && len(keys) > 0 {
			reportLegacyEnv(report, key, keys[0])
		}
		return value, ok
	}
	return false, false
}

func lookupBoolEnv(key string) (bool, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, false
	}
	return parsed, true
}
