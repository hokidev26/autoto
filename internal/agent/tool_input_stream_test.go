package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func feedAll(t *testing.T, preview *toolInputStreamPreview, fragments ...string) string {
	t.Helper()
	var out strings.Builder
	for _, fragment := range fragments {
		out.WriteString(preview.Feed(fragment))
	}
	return out.String()
}

func TestToolInputPreviewSpecByTool(t *testing.T) {
	cases := []struct {
		tool       string
		field      string
		arrayField string
		itemField  string
		snapshot   bool
	}{
		{tool: "Write", field: "content"},
		{tool: "Edit", field: "new_string"},
		{tool: "Agent", field: "prompt"},
		{tool: "MultiEdit", arrayField: "edits", itemField: "new_string"},
		{tool: "Bash", field: "command", snapshot: true},
	}
	for _, tc := range cases {
		spec, ok := toolInputPreviewSpecFor(tc.tool)
		if !ok || spec.field != tc.field || spec.arrayField != tc.arrayField || spec.itemField != tc.itemField || spec.snapshot != tc.snapshot {
			t.Fatalf("%s spec = %+v, %v", tc.tool, spec, ok)
		}
	}
	if _, ok := toolInputPreviewSpecFor("Read"); ok {
		t.Fatal("Read must not stream an input preview")
	}
	if preview := newToolInputStreamPreview("Read"); preview != nil {
		t.Fatal("unsupported tool must return a nil preview")
	}
}

func TestToolInputStreamPreviewAgentPrompt(t *testing.T) {
	preview := newToolInputStreamPreview("Agent")
	got := feedAll(t, preview, `{"subagent_type": "explore", "prompt": "Find every`, ` API endpoint"}`)
	if got != "Find every API endpoint" {
		t.Fatalf("streamed prompt = %q", got)
	}
	if preview.Field() != "prompt" {
		t.Fatalf("field = %q", preview.Field())
	}
}

func TestToolInputStreamPreviewMultiEditHunks(t *testing.T) {
	preview := newToolInputStreamPreview("MultiEdit")
	got := feedAll(t, preview,
		`{"file_path": "main.go", "edits": [{"old_string": "a", "new_string": "first`, ` hunk"}, {"old_string": "b", "new_`, `string": "second hunk", "replace_all": true}]}`,
	)
	want := "first hunk" + multiEditPreviewSeparator(2) + "second hunk"
	if got != want {
		t.Fatalf("streamed hunks = %q, want %q", got, want)
	}
	if path, ok := preview.FilePath(); !ok || path != "main.go" {
		t.Fatalf("file path = %q, %v", path, ok)
	}
}

func TestToolInputStreamPreviewMultiEditFinalizeMatchesStream(t *testing.T) {
	input := json.RawMessage(`{"file_path": "main.go", "edits": [{"old_string": "a", "new_string": "one"}, {"old_string": "b", "new_string": "two"}]}`)
	streaming := newToolInputStreamPreview("MultiEdit")
	streamed := feedAll(t, streaming, string(input))
	if rest := streaming.Finalize(input); rest != "" {
		t.Fatalf("fully streamed preview must have no remainder, got %q", rest)
	}
	fresh := newToolInputStreamPreview("MultiEdit")
	whole := fresh.Finalize(input)
	if whole != streamed {
		t.Fatalf("finalize-only preview %q must match streamed preview %q", whole, streamed)
	}
}

func TestToolInputStreamPreviewMultiEditSkipsNonObjectAndNonStringElements(t *testing.T) {
	input := `{"edits": ["decoy", {"new_string": "one"}, {"new_string": 7}, {"new_string": "three"}]}`
	preview := newToolInputStreamPreview("MultiEdit")
	streamed := feedAll(t, preview, input)
	fresh := newToolInputStreamPreview("MultiEdit")
	whole := fresh.Finalize(json.RawMessage(input))
	if streamed != whole {
		t.Fatalf("streamed %q must match finalize %q", streamed, whole)
	}
	// Element ordinals count objects only: "one" is object #1 (no separator),
	// "three" is object #3.
	want := "one" + multiEditPreviewSeparator(3) + "three"
	if streamed != want {
		t.Fatalf("streamed = %q, want %q", streamed, want)
	}
}

func TestToolInputStreamPreviewBashSnapshotRedacts(t *testing.T) {
	preview := newToolInputStreamPreview("Bash")
	if !preview.SnapshotMode() {
		t.Fatal("Bash preview must run in snapshot mode")
	}
	base := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	preview.Feed(`{"command": "curl -H \"Authorization: Bearer secret_token_12345\" https://api.example.com`)
	snapshot, ok := preview.SnapshotText(base, false)
	if !ok {
		t.Fatal("first snapshot must be due immediately")
	}
	if strings.Contains(snapshot, "secret_token_12345") {
		t.Fatalf("snapshot leaked the token: %q", snapshot)
	}
	if !strings.Contains(snapshot, "curl") {
		t.Fatalf("snapshot lost the command: %q", snapshot)
	}
	// Within the throttle window nothing new is due, even though text grew.
	preview.Feed(` --verbose`)
	if _, ok := preview.SnapshotText(base.Add(100*time.Millisecond), false); ok {
		t.Fatal("snapshot must be throttled inside the interval")
	}
	after, ok := preview.SnapshotText(base.Add(time.Second), false)
	if !ok || !strings.Contains(after, "--verbose") {
		t.Fatalf("later snapshot = %q, %v", after, ok)
	}
}

func TestToolInputStreamPreviewBashFinalizeRedacts(t *testing.T) {
	preview := newToolInputStreamPreview("Bash")
	final := preview.Finalize(json.RawMessage(`{"command": "export API_KEY=super_secret_value && ./run.sh"}`))
	if final == "" {
		t.Fatal("finalize must produce a snapshot for an unstreamed command")
	}
	if strings.Contains(final, "super_secret_value") {
		t.Fatalf("finalize leaked the secret: %q", final)
	}
	if again := preview.Finalize(json.RawMessage(`{"command": "export API_KEY=super_secret_value && ./run.sh"}`)); again != "" {
		t.Fatalf("unchanged finalize must be empty, got %q", again)
	}
}

func TestToolInputStreamPreviewExtractsContentAcrossFragments(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	got := feedAll(t, preview,
		`{"file_`, `path": "notes`, `.md", "cont`, `ent": "hello`, ` world\n`, `second line"`, `}`,
	)
	want := "hello world\nsecond line"
	if got != want {
		t.Fatalf("streamed content = %q, want %q", got, want)
	}
	path, ok := preview.FilePath()
	if !ok || path != "notes.md" {
		t.Fatalf("file path = %q, %v", path, ok)
	}
	if _, again := preview.FilePath(); again {
		t.Fatal("file path must only be reported once")
	}
}

func TestToolInputStreamPreviewContentBeforePath(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	got := feedAll(t, preview, `{"content": "first", "file_path": "a.txt"}`)
	if got != "first" {
		t.Fatalf("streamed content = %q", got)
	}
	if path, ok := preview.FilePath(); !ok || path != "a.txt" {
		t.Fatalf("file path = %q, %v", path, ok)
	}
}

func TestToolInputStreamPreviewDecodesEscapes(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	// The \uXXXX escape and the surrogate pair are both split across fragments.
	got := feedAll(t, preview,
		`{"content": "tab\t quote\" back\\ uni\u00e9`, ` emoji\ud83d`, `\ude00 done"}`,
	)
	want := "tab\t quote\" back\\ unié emoji😀 done"
	if got != want {
		t.Fatalf("decoded content = %q, want %q", got, want)
	}
}

func TestToolInputStreamPreviewIgnoresDecoys(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	// "content" appears as another field's value, as a nested key, and inside
	// an array; only the real top-level content field may stream.
	got := feedAll(t, preview,
		`{"file_path": "content", "meta": {"content": "nested"}, "tags": ["content"], "content": "real"}`,
	)
	if got != "real" {
		t.Fatalf("streamed content = %q, want %q", got, "real")
	}
	if path, ok := preview.FilePath(); !ok || path != "content" {
		t.Fatalf("file path = %q, %v", path, ok)
	}
}

func TestToolInputStreamPreviewEditStreamsNewString(t *testing.T) {
	preview := newToolInputStreamPreview("Edit")
	got := feedAll(t, preview,
		`{"file_path": "main.go", "old_string": "before", "new_string": "aft`, `er"}`,
	)
	if got != "after" {
		t.Fatalf("streamed new_string = %q", got)
	}
}

func TestToolInputStreamPreviewFinalizeReturnsRemainder(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	input := json.RawMessage(`{"file_path": "a.txt", "content": "hello world"}`)
	streamed := feedAll(t, preview, `{"file_path": "a.txt", "content": "hello`)
	if streamed != "hello" {
		t.Fatalf("streamed prefix = %q", streamed)
	}
	rest := preview.Finalize(input)
	if rest != " world" {
		t.Fatalf("finalize remainder = %q", rest)
	}
	if again := preview.Finalize(input); again != "" {
		t.Fatalf("second finalize must be empty, got %q", again)
	}
}

func TestToolInputStreamPreviewFinalizeWithoutStreaming(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	rest := preview.Finalize(json.RawMessage(`{"file_path": "a.txt", "content": "whole thing"}`))
	if rest != "whole thing" {
		t.Fatalf("finalize = %q", rest)
	}
}

func TestToolInputStreamPreviewFinalizeMismatchStaysSilent(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	feedAll(t, preview, `{"content": "streamed prefix`)
	if rest := preview.Finalize(json.RawMessage(`{"content": "different text"}`)); rest != "" {
		t.Fatalf("mismatched finalize must be empty, got %q", rest)
	}
}

func TestToolInputStreamPreviewSuppresssBeyondBudget(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	preview.Feed(`{"content": "`)
	total := 0
	chunk := strings.Repeat("x", 8192)
	for i := 0; i < (maxToolInputPreviewBytes/len(chunk))+4; i++ {
		total += len(preview.Feed(chunk))
	}
	if total > maxToolInputPreviewBytes+len(chunk) {
		t.Fatalf("emitted %d bytes, budget is %d", total, maxToolInputPreviewBytes)
	}
	if !preview.suppressed {
		t.Fatal("preview must be suppressed after exhausting the budget")
	}
	if rest := preview.Finalize(json.RawMessage(`{"content": "irrelevant"}`)); rest != "" {
		t.Fatalf("suppressed preview must not finalize, got %q", rest)
	}
}

func TestToolInputStreamPreviewNonStringTarget(t *testing.T) {
	preview := newToolInputStreamPreview("Write")
	got := feedAll(t, preview, `{"content": {"unexpected": "object"}, "file_path": "a.txt"}`)
	if got != "" {
		t.Fatalf("non-string target must stream nothing, got %q", got)
	}
	if path, ok := preview.FilePath(); !ok || path != "a.txt" {
		t.Fatalf("file path = %q, %v", path, ok)
	}
}

func TestToolInputStreamPreviewNilSafety(t *testing.T) {
	var preview *toolInputStreamPreview
	if preview.Feed("{}") != "" || preview.Field() != "" {
		t.Fatal("nil preview must be inert")
	}
	if _, ok := preview.FilePath(); ok {
		t.Fatal("nil preview has no path")
	}
	if preview.Finalize(json.RawMessage(`{}`)) != "" {
		t.Fatal("nil preview must not finalize")
	}
}
