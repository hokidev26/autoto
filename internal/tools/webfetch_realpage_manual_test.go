package tools

import (
	"os"
	"strings"
	"testing"
)

// TestHTMLToTextAgainstRealPage runs the extractor over a saved real-world
// documentation page rather than a hand-written fixture, because fixtures only
// prove the rules fire on markup shaped the way the author imagined.
//
// Opt-in: it needs a file, so it skips unless AUTOTO_REAL_PAGE points at one.
// It is a diagnostic, not a gate, and asserts only invariants that must hold for
// any input rather than anything specific to one site.
func TestHTMLToTextAgainstRealPage(t *testing.T) {
	path := os.Getenv("AUTOTO_REAL_PAGE")
	if path == "" {
		t.Skip("set AUTOTO_REAL_PAGE to a saved HTML file to run this")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := htmlToText(string(raw))

	if strings.Contains(text, "\x00") {
		t.Fatalf("placeholder NUL leaked into output")
	}
	for _, leak := range []string{"<script", "<style", "</div>", "</span>"} {
		if strings.Contains(text, leak) {
			t.Fatalf("raw markup %q survived extraction", leak)
		}
	}
	if strings.Contains(text, "\n\n\n") {
		t.Fatalf("more than one blank line survived collapsing")
	}
	reduction := 100 - (len(text) * 100 / len(raw))
	t.Logf("input %d bytes -> output %d bytes (%d%% smaller)", len(raw), len(text), reduction)
	t.Logf("first 600 bytes:\n%s", text[:min(len(text), 600)])
}
