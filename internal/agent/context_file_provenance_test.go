package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"autoto/internal/db"
	"autoto/internal/providers"
)

func toolUseMessage(toolName, input string) db.Message {
	blocks := []providers.ContentBlock{{
		Type:      "tool_use",
		ToolUseID: toolName + "-1",
		ToolName:  toolName,
		Input:     json.RawMessage(input),
	}}
	raw, err := json.Marshal(blocks)
	if err != nil {
		panic(err)
	}
	return db.Message{Role: "assistant", ContentJSON: raw}
}

// TestExtractFileProvenanceSeparatesReadsFromWrites is the core mapping: a file
// that was written must not be reported as merely read.
func TestExtractFileProvenanceSeparatesReadsFromWrites(t *testing.T) {
	messages := []db.Message{
		toolUseMessage("Read", `{"file_path":"internal/db/schema.go"}`),
		toolUseMessage("Write", `{"file_path":"internal/db/migrations.go","content":"x"}`),
		toolUseMessage("Edit", `{"file_path":"internal/tools/tool.go"}`),
		toolUseMessage("MultiEdit", `{"file_path":"internal/agent/loop.go"}`),
	}
	provenance := extractFileProvenance(messages)
	if len(provenance.Read) != 1 || provenance.Read[0] != "internal/db/schema.go" {
		t.Fatalf("expected exactly the read file, got %v", provenance.Read)
	}
	if len(provenance.Modified) != 3 {
		t.Fatalf("expected three modified files, got %v", provenance.Modified)
	}
}

// TestExtractFileProvenanceIgnoresDirectoryAndOpaqueTools guards the deliberate
// exclusions. Recording a directory as a file, or guessing at a Bash command's
// paths, would put wrong information into the summary.
func TestExtractFileProvenanceIgnoresDirectoryAndOpaqueTools(t *testing.T) {
	messages := []db.Message{
		toolUseMessage("LS", `{"path":"internal"}`),
		toolUseMessage("Glob", `{"path":"internal","pattern":"*.go"}`),
		toolUseMessage("Grep", `{"path":"internal","pattern":"func"}`),
		toolUseMessage("Bash", `{"command":"rm internal/db/schema.go"}`),
	}
	provenance := extractFileProvenance(messages)
	if len(provenance.Read) != 0 || len(provenance.Modified) != 0 {
		t.Fatalf("expected no file provenance, got read=%v modified=%v", provenance.Read, provenance.Modified)
	}
}

// TestExtractFileProvenanceSurvivesMalformedInput keeps compaction working when
// a tool input is not the shape this code expects. Losing provenance is
// acceptable; failing compaction is not.
func TestExtractFileProvenanceSurvivesMalformedInput(t *testing.T) {
	// A stored row can legitimately contain a tool input this code cannot
	// interpret, so the malformed cases are written as raw persisted JSON rather
	// than through the marshalling helper.
	malformed := db.Message{Role: "assistant", ContentJSON: json.RawMessage(
		`[{"type":"tool_use","toolUseId":"bad-1","toolName":"Read","input":"a string, not an object"}]`,
	)}
	messages := []db.Message{
		malformed,
		toolUseMessage("Read", `{"file_path":123}`),
		toolUseMessage("Read", `{"file_path":"  "}`),
		toolUseMessage("Read", `{}`),
		toolUseMessage("Read", `{"file_path":"good.go"}`),
	}
	provenance := extractFileProvenance(messages)
	if len(provenance.Read) != 1 || provenance.Read[0] != "good.go" {
		t.Fatalf("expected only the well-formed path, got %v", provenance.Read)
	}
}

// TestFileProvenanceRoundTripsThroughSummary is the property that makes this
// work across repeated compaction: what was rendered must parse back out.
func TestFileProvenanceRoundTripsThroughSummary(t *testing.T) {
	original := fileProvenance{
		Read:     []string{"a/read.go", "b/read.go"},
		Modified: []string{"c/write.go"},
	}
	summary := "Earlier work happened.\n\n" + renderFileProvenance(original)
	parsed := parseFileProvenance(summary)
	if strings.Join(parsed.Read, ",") != "a/read.go,b/read.go" {
		t.Fatalf("read list did not round-trip, got %v", parsed.Read)
	}
	if strings.Join(parsed.Modified, ",") != "c/write.go" {
		t.Fatalf("modified list did not round-trip, got %v", parsed.Modified)
	}
}

// TestFileProvenanceCarriesForwardAcrossCompactions is item 3's actual point.
// The second compaction must still know about the file the first one recorded.
func TestFileProvenanceCarriesForwardAcrossCompactions(t *testing.T) {
	first := withFileProvenance(
		"First summary.",
		"",
		[]db.Message{toolUseMessage("Write", `{"file_path":"early.go","content":"x"}`)},
	)
	if !strings.Contains(first, "early.go") {
		t.Fatalf("first compaction did not record its file: %q", first)
	}

	second := withFileProvenance(
		"Second summary.",
		first,
		[]db.Message{toolUseMessage("Read", `{"file_path":"late.go"}`)},
	)
	if !strings.Contains(second, "early.go") {
		t.Fatalf("second compaction lost the earlier file: %q", second)
	}
	if !strings.Contains(second, "late.go") {
		t.Fatalf("second compaction did not record its own file: %q", second)
	}
	// The block must not accumulate duplicates across rounds.
	if strings.Count(second, fileProvenanceHeader) != 1 {
		t.Fatalf("expected exactly one provenance block, got %q", second)
	}
}

// TestFileProvenanceKeepsModifiedClassificationOnLaterRead is the merge rule
// that matters: reading a file after editing it must not downgrade the record to
// "only read", or the summary would understate what the lost history did.
func TestFileProvenanceKeepsModifiedClassificationOnLaterRead(t *testing.T) {
	first := withFileProvenance("Edited it.", "", []db.Message{
		toolUseMessage("Edit", `{"file_path":"shared.go"}`),
	})
	second := withFileProvenance("Then read it.", first, []db.Message{
		toolUseMessage("Read", `{"file_path":"shared.go"}`),
	})
	parsed := parseFileProvenance(second)
	for _, path := range parsed.Read {
		if path == "shared.go" {
			t.Fatalf("a modified file was downgraded to read-only: %q", second)
		}
	}
	found := false
	for _, path := range parsed.Modified {
		if path == "shared.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected shared.go to stay modified, got %q", second)
	}
}

// TestWithFileProvenanceLeavesFilelessSummariesAlone keeps the change invisible
// for conversations that touched no files.
func TestWithFileProvenanceLeavesFilelessSummariesAlone(t *testing.T) {
	summary := withFileProvenance("Just talking.", "", []db.Message{
		toolUseMessage("Bash", `{"command":"echo hi"}`),
	})
	if summary != "Just talking." {
		t.Fatalf("expected the summary unchanged, got %q", summary)
	}
}

// TestJoinProvenanceListBoundsOutput keeps a long session from turning the
// summary into a file listing.
func TestJoinProvenanceListBoundsOutput(t *testing.T) {
	paths := make([]string, maxProvenancePaths+7)
	for index := range paths {
		paths[index] = "file" + itoa(index) + ".go"
	}
	rendered := joinProvenanceList(paths)
	if !strings.Contains(rendered, "(+7 more)") {
		t.Fatalf("expected an overflow marker, got %q", rendered)
	}
	// The marker must not be parsed back as a path.
	if parsed := splitProvenanceList(rendered); len(parsed) != maxProvenancePaths {
		t.Fatalf("expected %d parsed paths, got %d", maxProvenancePaths, len(parsed))
	}
}
