package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type GrepTool struct{}

const (
	grepDefaultHeadLimit = 100
	grepMaxContextLines  = 20
	grepMaxLineBytes     = 1 << 20
)

type grepInput struct {
	Pattern         string `json:"pattern" desc:"Regular expression (Go RE2 syntax) to search for."`
	Path            string `json:"path,omitempty" desc:"Directory or file to search, relative to the working directory. Defaults to the working directory root."`
	Glob            string `json:"glob,omitempty" desc:"Only search files whose path matches this glob, such as *.go or **/*.test.ts."`
	CaseInsensitive bool   `json:"case_insensitive,omitempty" desc:"Match without regard to letter case."`
	OutputMode      string `json:"output_mode,omitempty" jsonschema:"enum=content|files_with_matches|count" desc:"content returns matching lines (default), files_with_matches returns only file paths, count returns per-file match counts."`
	Before          int    `json:"before,omitempty" jsonschema:"minimum=0,maximum=20" desc:"Lines of context to show before each match. Only used when output_mode is content."`
	After           int    `json:"after,omitempty" jsonschema:"minimum=0,maximum=20" desc:"Lines of context to show after each match. Only used when output_mode is content."`
	Context         int    `json:"context,omitempty" jsonschema:"minimum=0,maximum=20" desc:"Lines of context on both sides of each match. Overrides before and after when set."`
	HeadLimit       int    `json:"head_limit,omitempty" jsonschema:"minimum=1" desc:"Maximum number of results to return. Defaults to 100."`
}

func (GrepTool) Name() string { return "Grep" }
func (GrepTool) Description() string {
	return "Search text files under the agent working directory using a regular expression, with optional context lines, glob filtering, and count or file-list output."
}
func (GrepTool) Schema() any               { return grepInput{} }
func (GrepTool) Risk(json.RawMessage) Risk { return RiskRead }

type grepFileResult struct {
	rel     string
	count   int
	renders []string
}

func (GrepTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input grepInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(input.Pattern) == "" {
		return Result{Output: "pattern is required", IsError: true}, nil
	}
	pattern := input.Pattern
	if input.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	mode := strings.TrimSpace(input.OutputMode)
	switch mode {
	case "", "content":
		mode = "content"
	case "files_with_matches", "count":
	default:
		return Result{Output: "output_mode must be content, files_with_matches, or count", IsError: true}, nil
	}
	before, after := input.Before, input.After
	if input.Context > 0 {
		before, after = input.Context, input.Context
	}
	before = clampContext(before)
	after = clampContext(after)

	rootInput := input.Path
	if rootInput == "" {
		rootInput = "."
	}
	root, err := resolveInCWD(env.CWD, rootInput)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	var globSegments []string
	if strings.TrimSpace(input.Glob) != "" {
		globSegments = splitGlobPattern(input.Glob)
	}
	limit := input.HeadLimit
	if limit <= 0 {
		limit = grepDefaultHeadLimit
	}

	results := make([]grepFileResult, 0, 16)
	emitted := 0
	truncated := false

	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable subtree must not abort the whole search.
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if emitted >= limit {
			truncated = true
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path != root && (heavyToolDirectory(entry.Name()) || sensitiveToolPath(root, path)) {
				return filepath.SkipDir
			}
			return nil
		}
		resolved, err := resolveInCWD(env.CWD, path)
		if err != nil || sensitiveToolPath(root, resolved) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if len(globSegments) > 0 && !matchGlobSegments(globSegments, splitGlobPattern(relSlash)) {
			return nil
		}
		result, scanned := grepFile(resolved, relSlash, re, mode, before, after, limit-emitted)
		if !scanned || result.count == 0 {
			return nil
		}
		results = append(results, result)
		switch mode {
		case "count", "files_with_matches":
			emitted++
		default:
			emitted += len(result.renders)
		}
		return nil
	})
	if walkErr != nil && ctx.Err() != nil {
		return Result{Output: ctx.Err().Error(), IsError: true}, nil
	}
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return Result{Output: walkErr.Error(), IsError: true}, nil
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].rel < results[j].rel })

	total := 0
	for _, result := range results {
		total += result.count
	}
	out := renderGrepResults(results, mode)
	if out == "" {
		out = "No matches found"
	} else if truncated || emitted >= limit {
		out += "\n...[truncated]"
	}
	return Result{Output: out, Meta: map[string]any{
		"count":      total,
		"files":      len(results),
		"outputMode": mode,
		"truncated":  truncated || emitted >= limit,
	}}, nil
}

func clampContext(value int) int {
	if value < 0 {
		return 0
	}
	if value > grepMaxContextLines {
		return grepMaxContextLines
	}
	return value
}

// grepFile scans one file. For content mode it renders each match with its
// surrounding context; for the summary modes it only counts, so a large file
// does not build output that will be discarded.
func grepFile(path, rel string, re *regexp.Regexp, mode string, before, after, remaining int) (grepFileResult, bool) {
	file, err := os.Open(path)
	if err != nil {
		return grepFileResult{}, false
	}
	defer file.Close()

	// Reject binary files: null bytes are absent in valid UTF-8/ASCII text but
	// appear in compiled bytecode (.pyc), object files, and other binary formats.
	// Reading 512 bytes matches the heuristic used by git and ripgrep.
	var sniff [512]byte
	n, _ := file.Read(sniff[:])
	for _, b := range sniff[:n] {
		if b == 0 {
			return grepFileResult{}, false
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return grepFileResult{}, false
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), grepMaxLineBytes)
	result := grepFileResult{rel: rel}

	// A ring of preceding lines large enough to satisfy the before-context.
	ring := make([]string, 0, before)
	pendingAfter := 0
	lastRendered := 0
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		text := scanner.Text()
		matched := re.MatchString(text)
		if matched {
			result.count++
		}
		if mode != "content" {
			if matched && before == 0 && after == 0 {
				// Nothing to render; keep counting.
			}
			continue
		}
		switch {
		case matched:
			if len(result.renders) >= remaining {
				return result, true
			}
			start := lineNumber - len(ring)
			for index, contextLine := range ring {
				number := start + index
				if number > lastRendered {
					result.renders = append(result.renders, formatGrepLine(rel, number, contextLine, false))
					lastRendered = number
				}
			}
			result.renders = append(result.renders, formatGrepLine(rel, lineNumber, text, true))
			lastRendered = lineNumber
			pendingAfter = after
		case pendingAfter > 0:
			result.renders = append(result.renders, formatGrepLine(rel, lineNumber, text, false))
			lastRendered = lineNumber
			pendingAfter--
		}
		if before > 0 {
			ring = append(ring, text)
			if len(ring) > before {
				ring = ring[1:]
			}
		}
	}
	if scanner.Err() != nil {
		// A binary or over-long line ends this file's scan; keep what matched.
		return result, result.count > 0
	}
	return result, true
}

// formatGrepLine uses ':' for a matching line and '-' for context, matching the
// convention grep uses so the two are distinguishable at a glance.
func formatGrepLine(rel string, lineNumber int, text string, match bool) string {
	separator := "-"
	if match {
		separator = ":"
	}
	return fmt.Sprintf("%s%s%d%s%s", rel, separator, lineNumber, separator, text)
}

func renderGrepResults(results []grepFileResult, mode string) string {
	switch mode {
	case "files_with_matches":
		paths := make([]string, 0, len(results))
		for _, result := range results {
			paths = append(paths, result.rel)
		}
		return strings.Join(paths, "\n")
	case "count":
		counts := make([]string, 0, len(results))
		for _, result := range results {
			counts = append(counts, fmt.Sprintf("%s:%d", result.rel, result.count))
		}
		return strings.Join(counts, "\n")
	default:
		blocks := make([]string, 0, len(results))
		for _, result := range results {
			if len(result.renders) == 0 {
				continue
			}
			blocks = append(blocks, strings.Join(result.renders, "\n"))
		}
		return strings.Join(blocks, "\n")
	}
}
