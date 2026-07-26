package app

import (
	"log/slog"
	"testing"
)

// TestLogLevelFromEnv pins the AUTOTO_LOG_LEVEL contract. The important case is
// the default: an unset or misspelled value must stay at Info rather than
// silently dropping the operational logging the server relies on.
func TestLogLevelFromEnv(t *testing.T) {
	cases := []struct {
		value string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"  Debug  ", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
		{"trace", slog.LevelInfo},
	}
	for _, testCase := range cases {
		if got := logLevelFromEnv(testCase.value); got != testCase.want {
			t.Errorf("logLevelFromEnv(%q) = %v, want %v", testCase.value, got, testCase.want)
		}
	}
}
