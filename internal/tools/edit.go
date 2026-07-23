package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type EditTool struct{}

type editInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func (EditTool) Name() string { return "Edit" }
func (EditTool) Description() string {
	return "Replace text in an existing file under the agent working directory."
}
func (EditTool) Schema() any               { return editInput{} }
func (EditTool) Risk(json.RawMessage) Risk { return RiskWrite }

func (EditTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input editInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if input.FilePath == "" || input.OldString == "" {
		return Result{Output: "file_path and old_string are required", IsError: true}, nil
	}
	if input.OldString == input.NewString {
		return Result{Output: "old_string and new_string must differ", IsError: true}, nil
	}
	path, err := resolveInCWD(env.CWD, input.FilePath)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	text := string(data)
	count := strings.Count(text, input.OldString)
	if count == 0 {
		return Result{Output: "old_string not found", IsError: true}, nil
	}
	if !input.ReplaceAll && count != 1 {
		return Result{Output: fmt.Sprintf("old_string is not unique; found %d occurrences", count), IsError: true}, nil
	}
	replacements := 1
	if input.ReplaceAll {
		replacements = -1
	}
	updated := strings.Replace(text, input.OldString, input.NewString, replacements)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	changed := 1
	if input.ReplaceAll {
		changed = count
	}
	diff, diffTruncated := buildEditDiff(text, updated, editDiffDisplayPath(env.CWD, input.FilePath, path))
	meta := map[string]any{"path": path, "replacements": changed, "diff": diff}
	if diffTruncated {
		meta["diffTruncated"] = true
	}
	return Result{Output: fmt.Sprintf("Edited %s (%d replacement(s))", path, changed), Meta: meta}, nil
}

const (
	editDiffContext    = 3
	editDiffMaxBytes   = 64 * 1024
	editDiffMaxCells   = 1_000_000
	editDiffTruncation = "\n...[diff truncated]\n"
)

type editDiffLine struct {
	text       string
	terminated bool
}

type editDiffOp struct {
	kind byte
	line editDiffLine
}

type editDiffHunk struct {
	start int
	end   int
}

type boundedEditDiff struct {
	builder   strings.Builder
	truncated bool
}

func buildEditDiff(oldText, newText, displayPath string) (string, bool) {
	displayPath = sanitizeEditDiffPath(displayPath)
	ops := makeEditDiffOps(splitEditDiffLines(oldText), splitEditDiffLines(newText))
	hunks := makeEditDiffHunks(ops, editDiffContext)
	var output boundedEditDiff
	output.append(fmt.Sprintf("--- a/%s\n", displayPath))
	output.append(fmt.Sprintf("+++ b/%s\n", displayPath))

	oldBefore := make([]int, len(ops)+1)
	newBefore := make([]int, len(ops)+1)
	for i, op := range ops {
		oldBefore[i+1] = oldBefore[i]
		newBefore[i+1] = newBefore[i]
		if op.kind != '+' {
			oldBefore[i+1]++
		}
		if op.kind != '-' {
			newBefore[i+1]++
		}
	}
	for _, hunk := range hunks {
		oldCount, newCount := 0, 0
		for _, op := range ops[hunk.start:hunk.end] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		oldStart := oldBefore[hunk.start] + 1
		if oldCount == 0 {
			oldStart = oldBefore[hunk.start]
		}
		newStart := newBefore[hunk.start] + 1
		if newCount == 0 {
			newStart = newBefore[hunk.start]
		}
		output.append(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount))
		for _, op := range ops[hunk.start:hunk.end] {
			output.append(string(op.kind) + op.line.text + "\n")
			if !op.line.terminated {
				output.append("\\ No newline at end of file\n")
			}
		}
	}
	return output.builder.String(), output.truncated
}

func (output *boundedEditDiff) append(text string) {
	if output.truncated {
		return
	}
	text = strings.ToValidUTF8(text, "�")
	remaining := editDiffMaxBytes - output.builder.Len()
	if len(text) <= remaining {
		output.builder.WriteString(text)
		return
	}
	output.truncated = true
	contentLimit := remaining - len(editDiffTruncation)
	if contentLimit > 0 {
		output.builder.WriteString(utf8Prefix(text, contentLimit))
	}
	if output.builder.Len()+len(editDiffTruncation) <= editDiffMaxBytes {
		output.builder.WriteString(editDiffTruncation)
	}
}

func utf8Prefix(text string, maxBytes int) string {
	if maxBytes <= 0 || text == "" {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func splitEditDiffLines(text string) []editDiffLine {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	terminated := strings.HasSuffix(text, "\n")
	if terminated {
		parts = parts[:len(parts)-1]
	}
	lines := make([]editDiffLine, len(parts))
	for i, part := range parts {
		lines[i] = editDiffLine{text: part, terminated: terminated || i < len(parts)-1}
	}
	return lines
}

func makeEditDiffOps(oldLines, newLines []editDiffLine) []editDiffOp {
	if len(oldLines) == 0 || len(newLines) == 0 || len(oldLines) > editDiffMaxCells/len(newLines) {
		return makeSimpleEditDiffOps(oldLines, newLines)
	}

	width := len(newLines)
	directions := make([]byte, len(oldLines)*width)
	previous := make([]int, width+1)
	current := make([]int, width+1)
	for i := 1; i <= len(oldLines); i++ {
		for j := 1; j <= width; j++ {
			index := (i-1)*width + j - 1
			if oldLines[i-1] == newLines[j-1] {
				current[j] = previous[j-1] + 1
				directions[index] = ' '
			} else if previous[j] > current[j-1] {
				current[j] = previous[j]
				directions[index] = '-'
			} else {
				current[j] = current[j-1]
				directions[index] = '+'
			}
		}
		previous, current = current, previous
		clear(current)
	}

	reversed := make([]editDiffOp, 0, len(oldLines)+len(newLines))
	for i, j := len(oldLines), len(newLines); i > 0 || j > 0; {
		if i > 0 && j > 0 && oldLines[i-1] == newLines[j-1] {
			reversed = append(reversed, editDiffOp{kind: ' ', line: oldLines[i-1]})
			i--
			j--
			continue
		}
		if j > 0 && (i == 0 || directions[(i-1)*width+j-1] == '+') {
			reversed = append(reversed, editDiffOp{kind: '+', line: newLines[j-1]})
			j--
			continue
		}
		reversed = append(reversed, editDiffOp{kind: '-', line: oldLines[i-1]})
		i--
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func makeSimpleEditDiffOps(oldLines, newLines []editDiffLine) []editDiffOp {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix && oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	ops := make([]editDiffOp, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines[:prefix] {
		ops = append(ops, editDiffOp{kind: ' ', line: line})
	}
	for _, line := range oldLines[prefix : len(oldLines)-suffix] {
		ops = append(ops, editDiffOp{kind: '-', line: line})
	}
	for _, line := range newLines[prefix : len(newLines)-suffix] {
		ops = append(ops, editDiffOp{kind: '+', line: line})
	}
	for _, line := range oldLines[len(oldLines)-suffix:] {
		ops = append(ops, editDiffOp{kind: ' ', line: line})
	}
	return ops
}

func makeEditDiffHunks(ops []editDiffOp, context int) []editDiffHunk {
	var hunks []editDiffHunk
	for index := 0; index < len(ops); {
		if ops[index].kind == ' ' {
			index++
			continue
		}
		changedEnd := index + 1
		for changedEnd < len(ops) && ops[changedEnd].kind != ' ' {
			changedEnd++
		}
		start := max(0, index-context)
		end := min(len(ops), changedEnd+context)
		if len(hunks) > 0 && start <= hunks[len(hunks)-1].end {
			if end > hunks[len(hunks)-1].end {
				hunks[len(hunks)-1].end = end
			}
		} else {
			hunks = append(hunks, editDiffHunk{start: start, end: end})
		}
		index = changedEnd
	}
	return hunks
}

func sanitizeEditDiffPath(path string) string {
	path = strings.ToValidUTF8(path, "�")
	path = strings.NewReplacer("\r", "_", "\n", "_").Replace(path)
	path = strings.TrimSpace(path)
	if path == "" {
		return "file"
	}
	return path
}

func editDiffDisplayPath(cwd, inputPath, resolvedPath string) string {
	if !filepath.IsAbs(inputPath) {
		return filepath.ToSlash(filepath.Clean(inputPath))
	}
	if cwd == "" {
		cwd = "."
	}
	if base, err := filepath.Abs(cwd); err == nil {
		if relative, err := filepath.Rel(base, resolvedPath); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return filepath.ToSlash(relative)
		}
	}
	return filepath.Base(resolvedPath)
}
