package server

import (
	"strings"
	"testing"

	"autoto/internal/config"
)

func TestStrictNonRetryableErrorPatternsRejectsWhatItCannotStore(t *testing.T) {
	// Silently dropping these is the failure mode worth guarding: the pattern
	// disappears from the list after saving and the user cannot tell why.
	if _, err := strictNonRetryableErrorPatterns([]string{"ab"}); err == nil {
		t.Fatal("a pattern below the minimum length must be rejected, not dropped")
	}
	tooLong := strings.Repeat("x", config.MaxNonRetryableErrorPatternLen+1)
	if _, err := strictNonRetryableErrorPatterns([]string{tooLong}); err == nil {
		t.Fatal("a pattern above the maximum length must be rejected, not truncated")
	}
	tooMany := make([]string, config.MaxNonRetryableErrorPatterns+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("a", 4) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := strictNonRetryableErrorPatterns(tooMany); err == nil {
		t.Fatal("more patterns than the cap must be rejected, not silently cut")
	}
}

func TestStrictNonRetryableErrorPatternsNormalizesAccepted(t *testing.T) {
	patterns, err := strictNonRetryableErrorPatterns([]string{"  Insufficient   Balance  ", "insufficient balance", "MODEL NOT FOUND"})
	if err != nil {
		t.Fatalf("valid patterns rejected: %v", err)
	}
	// Whitespace collapses, case folds, and the duplicate is dropped, so the
	// stored form can be compared with a plain substring test.
	want := []string{"insufficient balance", "model not found"}
	if len(patterns) != len(want) {
		t.Fatalf("got %d patterns (%v), want %d", len(patterns), patterns, len(want))
	}
	for i, value := range want {
		if patterns[i] != value {
			t.Fatalf("pattern %d = %q, want %q", i, patterns[i], value)
		}
	}
}

func TestStrictNonRetryableErrorPatternsAcceptsEmptyList(t *testing.T) {
	// Clearing the list is how a user undoes a pattern that was stopping runs
	// too eagerly, so it must not be an error.
	patterns, err := strictNonRetryableErrorPatterns(nil)
	if err != nil {
		t.Fatalf("clearing the list must be allowed: %v", err)
	}
	if len(patterns) != 0 {
		t.Fatalf("expected no patterns, got %v", patterns)
	}
}
