package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"autoto/internal/db"
)

// Compaction replaces older turns with prose. Prose is lossy in a specific way
// that matters: which files the discarded turns read and which they changed.
// A later turn that cannot see "we already edited internal/db/schema.go" will
// re-read or re-edit it, or contradict work it cannot remember.
//
// The summary text is the only durable carrier available here, because
// compaction state lives in agent columns (ContextSummary,
// PruneBoundaryMessageID) rather than in an append-only entry that could hold
// structured details. So the file lists are rendered into a delimited block
// appended to the summary, and parsed back out of the previous summary on the
// next compaction. That parse-and-merge step is what makes provenance survive
// repeated compaction instead of only the most recent round.
const (
	fileProvenanceHeader   = "Files touched in compacted history:"
	fileProvenanceReadTag  = "  read: "
	fileProvenanceWriteTag = "  modified: "
	// maxProvenancePaths bounds each list. A long session can touch far more
	// files than are useful to restate, and the summary is size-capped anyway.
	maxProvenancePaths = 40
	// maxProvenancePathRunes bounds one rendered path.
	maxProvenancePathRunes = 200
)

// fileProvenance holds the paths a stretch of history read and modified.
type fileProvenance struct {
	Read     []string
	Modified []string
}

// toolPathFields lists the input field naming a specific file, per tool.
//
// Only tools that address one identified file appear here. LS, Glob, and Grep
// take a directory and are excluded: recording a directory as a file that was
// read would misreport what happened. Bash is excluded because its command is
// not parsed, so any path it touched would be a guess.
var toolPathFields = map[string][]string{
	"Read":      {"file_path"},
	"Write":     {"file_path"},
	"Edit":      {"file_path"},
	"MultiEdit": {"file_path"},
}

// toolModifiesFiles reports whether a tool's named paths were changed rather
// than only observed.
func toolModifiesFiles(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "Write", "Edit", "MultiEdit":
		return true
	default:
		return false
	}
}

// extractFileProvenance collects file paths from the tool calls in messages.
//
// It reads the tool call inputs rather than the results, because a call that
// failed still tells the next turn that the path was involved, and because
// results are truncated by the output pipeline.
func extractFileProvenance(messages []db.Message) fileProvenance {
	read := map[string]struct{}{}
	modified := map[string]struct{}{}
	for _, message := range messages {
		for _, block := range contentBlocksFromMessage(message) {
			if block.Type != "tool_use" && block.Type != "tool_call" {
				continue
			}
			fields, ok := toolPathFields[strings.TrimSpace(block.ToolName)]
			if !ok || len(block.Input) == 0 {
				continue
			}
			var input map[string]any
			if err := json.Unmarshal(block.Input, &input); err != nil {
				continue
			}
			for _, field := range fields {
				value, ok := input[field].(string)
				if !ok {
					continue
				}
				path := strings.TrimSpace(value)
				if path == "" {
					continue
				}
				if toolModifiesFiles(block.ToolName) {
					modified[path] = struct{}{}
				} else {
					read[path] = struct{}{}
				}
			}
		}
	}
	return fileProvenance{Read: sortedPaths(read), Modified: sortedPaths(modified)}
}

func sortedPaths(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for path := range set {
		out = append(out, truncateRunes(path, maxProvenancePathRunes))
	}
	sort.Strings(out)
	return out
}

// parseFileProvenance recovers the lists from a previous summary's block. An
// absent or malformed block yields an empty result rather than an error: the
// summary is derived text, and losing provenance must never fail compaction.
func parseFileProvenance(summary string) fileProvenance {
	index := strings.LastIndex(summary, fileProvenanceHeader)
	if index < 0 {
		return fileProvenance{}
	}
	var provenance fileProvenance
	for _, line := range strings.Split(summary[index:], "\n") {
		switch {
		case strings.HasPrefix(line, fileProvenanceReadTag):
			provenance.Read = splitProvenanceList(strings.TrimPrefix(line, fileProvenanceReadTag))
		case strings.HasPrefix(line, fileProvenanceWriteTag):
			provenance.Modified = splitProvenanceList(strings.TrimPrefix(line, fileProvenanceWriteTag))
		}
	}
	return provenance
}

func splitProvenanceList(value string) []string {
	parts := strings.Split(value, ", ")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		// The renderer appends an ellipsis marker when it truncates; it is not a path.
		if part == "" || strings.HasPrefix(part, "(+") {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeFileProvenance carries the previous lists forward and folds in the new
// ones. A path that was modified at any point stays modified: demoting it to
// "read" would understate what the discarded history did.
func mergeFileProvenance(previous, current fileProvenance) fileProvenance {
	modified := map[string]struct{}{}
	for _, path := range previous.Modified {
		modified[path] = struct{}{}
	}
	for _, path := range current.Modified {
		modified[path] = struct{}{}
	}
	read := map[string]struct{}{}
	for _, path := range append(append([]string{}, previous.Read...), current.Read...) {
		if _, isModified := modified[path]; isModified {
			continue
		}
		read[path] = struct{}{}
	}
	return fileProvenance{Read: sortedPaths(read), Modified: sortedPaths(modified)}
}

// renderFileProvenance formats the block appended to a summary. It returns an
// empty string when there is nothing to record, so summaries for conversations
// that touched no files are unchanged.
func renderFileProvenance(provenance fileProvenance) string {
	if len(provenance.Read) == 0 && len(provenance.Modified) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(fileProvenanceHeader)
	if len(provenance.Modified) > 0 {
		builder.WriteString("\n")
		builder.WriteString(fileProvenanceWriteTag)
		builder.WriteString(joinProvenanceList(provenance.Modified))
	}
	if len(provenance.Read) > 0 {
		builder.WriteString("\n")
		builder.WriteString(fileProvenanceReadTag)
		builder.WriteString(joinProvenanceList(provenance.Read))
	}
	return builder.String()
}

func joinProvenanceList(paths []string) string {
	if len(paths) <= maxProvenancePaths {
		return strings.Join(paths, ", ")
	}
	kept := paths[:maxProvenancePaths]
	return strings.Join(kept, ", ") + ", (+" + itoa(len(paths)-maxProvenancePaths) + " more)"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 12)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// withFileProvenance appends the merged provenance block to a freshly generated
// summary. It is applied after the summary is produced, so it works with both
// the model-generated and the deterministic fallback path: neither can be relied
// on to preserve the paths on its own.
func withFileProvenance(summary, previousSummary string, compacted []db.Message) string {
	merged := mergeFileProvenance(parseFileProvenance(previousSummary), extractFileProvenance(compacted))
	block := renderFileProvenance(merged)
	if block == "" {
		return summary
	}
	// Drop any block the generator echoed from the previous summary so the
	// rendered one is authoritative and not duplicated.
	summary = strings.TrimRight(stripFileProvenance(summary), "\n")
	if summary == "" {
		return block
	}
	return summary + "\n\n" + block
}

// stripFileProvenance removes a provenance block from text.
func stripFileProvenance(summary string) string {
	index := strings.LastIndex(summary, fileProvenanceHeader)
	if index < 0 {
		return summary
	}
	return summary[:index]
}
