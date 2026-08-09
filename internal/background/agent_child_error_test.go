package background

import (
	"encoding/json"
	"strings"
	"testing"
)

// A failed child used to surface as nothing but "did not complete", so the
// parent could not tell an unconfigured provider from a rejected model from a
// mid-run fault. The Run already records the reason; these tests keep it
// reaching the parent.
func TestAgentPublicResultCarriesTheChildFailureReason(t *testing.T) {
	const reason = "provider unavailable: anthropic provider is unavailable: Anthropic credentials are not configured"
	result, err := marshalAgentPublicResultWithDetails(
		"general", "general", "anthropic:claude-sonnet-4-5", "anthropic:claude-sonnet-4-5",
		`C:\work`, 4, "child-agent", "child-run", "error", reason,
	)
	if err != nil {
		t.Fatal(err)
	}
	var projected agentPublicResult
	if err := json.Unmarshal(result, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.ChildError != reason {
		t.Fatalf("childError = %q, want %q", projected.ChildError, reason)
	}
	if projected.Status != "error" {
		t.Fatalf("status = %q, want error", projected.Status)
	}
}

// A successful child has no failure to report, and an empty reason must not
// add a noise field to the result.
func TestAgentPublicResultOmitsChildErrorWhenAbsent(t *testing.T) {
	result, err := marshalAgentPublicResult("general", 0, "child-agent", "child-run", "completed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result), "childError") {
		t.Fatalf("result carried an empty childError field: %s", result)
	}
}

// The diagnostic is the only optional part of the result. An oversized reason
// must be truncated rather than rejected, and must never push the result past
// the budget the caller depends on.
func TestAgentPublicResultBoundsAnOversizedChildError(t *testing.T) {
	result, err := marshalAgentPublicResultWithDetails(
		"general", "general", "", "", "", 0, "child-agent", "child-run", "error",
		strings.Repeat("x", 8000),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) > maxAgentResultBytes {
		t.Fatalf("result is %d bytes, over the %d budget", len(result), maxAgentResultBytes)
	}
	var projected agentPublicResult
	if err := json.Unmarshal(result, &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected.ChildError) > maxAgentChildErrorBytes {
		t.Fatalf("childError is %d bytes, over the %d bound", len(projected.ChildError), maxAgentChildErrorBytes)
	}
	if projected.ChildError == "" {
		t.Fatal("childError was dropped entirely instead of truncated")
	}
}

// Truncation must not split a multi-byte rune, or the result stops being valid
// UTF-8 and the parent sees mojibake instead of a reason.
func TestAgentPublicResultTruncatesChildErrorOnRuneBoundaries(t *testing.T) {
	result, err := marshalAgentPublicResultWithDetails(
		"general", "general", "", "", "", 0, "child-agent", "child-run", "error",
		strings.Repeat("模型憑證未設定", 400),
	)
	if err != nil {
		t.Fatal(err)
	}
	var projected agentPublicResult
	if err := json.Unmarshal(result, &projected); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(result) {
		t.Fatal("result is not valid JSON after truncation")
	}
	if strings.ContainsRune(projected.ChildError, '\uFFFD') {
		t.Fatalf("truncation split a rune: %q", projected.ChildError)
	}
}
