package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"autoto/internal/db"
	"autoto/internal/tools"
)

// utf16LEBytes is what a Windows child process that writes UTF-16 hands back. It
// is the real shape behind the reported failure rather than a hand-placed NUL.
func utf16LEBytes(text string) string {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return string(out)
}

// assertStorableText mirrors the trusted store's own predicate for a persisted
// preview: valid UTF-8, no NUL, within the byte ceiling. Duplicating the rule
// here is deliberate, since the store's validator is unexported and this test
// exists to prove the producing side satisfies it before the write is attempted.
func assertStorableText(t *testing.T, label, value string, maxBytes int) {
	t.Helper()
	if !utf8.ValidString(value) {
		t.Fatalf("%s is not valid UTF-8: %q", label, value)
	}
	if strings.ContainsRune(value, 0) {
		t.Fatalf("%s still contains NUL: %q", label, value)
	}
	if len(value) > maxBytes {
		t.Fatalf("%s is %d bytes, over the %d ceiling", label, len(value), maxBytes)
	}
}

// A preview carrying NUL made the store reject the tool ledger row, which killed
// the whole run rather than merely losing a readable preview. Repairing UTF-8 is
// not enough on its own: U+0000 is a legal code point and survives that repair.
func TestToolResultPreviewSurvivesUTF16Output(t *testing.T) {
	output := utf16LEBytes("WSL 版本: 2.6.1")
	if !strings.ContainsRune(output, 0) {
		t.Fatal("the fixture must contain NUL, or it does not reproduce the failure")
	}

	preview, truncated := boundedToolResultPreview(output)
	assertStorableText(t, "preview", preview, maxToolResultPreviewBytes)
	if truncated {
		t.Fatal("a short output must not report truncation")
	}
	if !strings.Contains(preview, "2.6.1") {
		t.Fatalf("stripping NUL must leave the readable text: %q", preview)
	}
}

// The summary the runner persists carries the preview, so it has to satisfy the
// same rule. This is the value whose rejection surfaced as
// "persist terminal tool execution item ...: invalid tool execution output preview".
func TestToolExecutionOutputSummaryFromUTF16OutputIsStorable(t *testing.T) {
	output := utf16LEBytes("Wsl/0x80070422")
	summaryJSON, err := toolExecutionOutputSummaryJSON(tools.Result{Output: output})
	if err != nil {
		t.Fatalf("building the summary failed: %v", err)
	}
	if len(summaryJSON) > 8192 {
		t.Fatalf("summary JSON is %d bytes, over the store's 8192 ceiling", len(summaryJSON))
	}

	var summary db.ToolExecutionOutputSummary
	if err := json.Unmarshal(summaryJSON, &summary); err != nil {
		t.Fatalf("decoding the summary failed: %v", err)
	}
	assertStorableText(t, "summary preview", summary.Preview, 4096)

	// The digest and byte count describe the untouched output, so sanitizing the
	// preview must not quietly rewrite what the audit row claims was executed.
	if summary.ByteCount != len(output) {
		t.Fatalf("byteCount = %d, want the raw output length %d", summary.ByteCount, len(output))
	}
	if len(summary.SHA256) != 64 {
		t.Fatalf("sha256 = %q, want a 64-char digest of the raw output", summary.SHA256)
	}
}

// Event strings travel the same sanitizer, so a UTF-16 command line or argument
// cannot poison the activity feed either.
func TestBoundedToolEventStringDropsNUL(t *testing.T) {
	value, _ := boundedToolEventString(utf16LEBytes("git status"), 4096)
	assertStorableText(t, "event string", value, 4096)
	if !strings.Contains(value, "git status") {
		t.Fatalf("readable text was lost: %q", value)
	}
}

// Ordinary output must pass through untouched, so the NUL strip cannot become a
// silent mutation of every preview.
func TestSanitizedToolTextLeavesCleanOutputAlone(t *testing.T) {
	for _, value := range []string{"", "plain ascii", "中文與 emoji 🚀", "tabs\tand\nnewlines"} {
		if got := sanitizedToolText(value); got != value {
			t.Fatalf("sanitizedToolText(%q) = %q, want it unchanged", value, got)
		}
	}
	if got := sanitizedToolText("a\x00b"); got != "ab" {
		t.Fatalf("sanitizedToolText dropped the wrong bytes: %q", got)
	}
	if got := sanitizedToolText("bad\xffbyte"); !utf8.ValidString(got) || strings.Contains(got, "\xff") {
		t.Fatalf("invalid UTF-8 was not repaired: %q", got)
	}
}
