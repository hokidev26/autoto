package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	globMaxMatches = 1000
	globMaxEntries = 200000
)

// globMatches walks the tree beneath root and returns workspace-relative paths
// matching pattern. It replaces filepath.Glob, which cannot express `**` and so
// could only ever match a single directory level. Results are newest-first,
// which is what a coding agent almost always wants.
func globMatches(ctx context.Context, cwd, root, pattern string) ([]string, error) {
	segments := splitGlobPattern(pattern)
	if len(segments) == 0 {
		return nil, nil
	}
	type match struct {
		rel     string
		modTime int64
	}
	matches := make([]match, 0, 32)
	entries := 0

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable subtree must not abort the whole search.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entries++; entries > globMaxEntries {
			return fs.SkipAll
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if entry.IsDir() {
			// Skip build and VCS directories that would dominate the walk, and
			// never descend into paths the workspace policy hides.
			if heavyToolDirectory(entry.Name()) || sensitiveToolPath(root, path) {
				return fs.SkipDir
			}
			return nil
		}
		if sensitiveToolPath(root, path) {
			return nil
		}
		if !matchGlobSegments(segments, splitGlobPattern(filepath.ToSlash(rel))) {
			return nil
		}
		// Re-resolve through the workspace boundary so a symlinked file cannot
		// surface a path outside the working directory.
		resolved, resolveErr := resolveInCWD(cwd, path)
		if resolveErr != nil {
			return nil
		}
		safeRel, relErr := filepath.Rel(root, resolved)
		if relErr != nil || filepath.IsAbs(safeRel) || safeRel == ".." || strings.HasPrefix(safeRel, ".."+string(filepath.Separator)) {
			return nil
		}
		modTime := int64(0)
		if info, infoErr := entry.Info(); infoErr == nil {
			modTime = info.ModTime().UnixNano()
		}
		matches = append(matches, match{rel: safeRel, modTime: modTime})
		if len(matches) >= globMaxMatches {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].modTime > matches[j].modTime })
	out := make([]string, 0, len(matches))
	for _, item := range matches {
		out = append(out, item.rel)
	}
	return out, nil
}

func splitGlobPattern(pattern string) []string {
	pattern = strings.Trim(strings.ReplaceAll(filepath.ToSlash(pattern), `\`, "/"), "/")
	if pattern == "" {
		return nil
	}
	parts := strings.Split(pattern, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

// matchGlobSegments matches path segments against pattern segments, where `**`
// matches zero or more whole segments. Every other segment is matched with
// filepath.Match, so `*`, `?`, and character classes keep their usual meaning.
func matchGlobSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		// Zero segments consumed, or one segment consumed and retry.
		if matchGlobSegments(pattern[1:], name) {
			return true
		}
		if len(name) == 0 {
			return false
		}
		return matchGlobSegments(pattern, name[1:])
	}
	if len(name) == 0 {
		return false
	}
	matched, err := filepath.Match(pattern[0], name[0])
	if err != nil || !matched {
		return false
	}
	return matchGlobSegments(pattern[1:], name[1:])
}

type GlobTool struct{}

type globInput struct {
	Pattern string `json:"pattern" desc:"Glob pattern such as *.go or **/*.test.ts. Use ** to match across directory levels; a pattern without ** matches one level only. Results are newest-first."`
	Path    string `json:"path,omitempty" desc:"Directory to search under, relative to the working directory. Defaults to the working directory root."`
}

func (GlobTool) Name() string { return "Glob" }
func (GlobTool) Description() string {
	return "Find files by glob pattern under the agent working directory."
}
func (GlobTool) Schema() any               { return globInput{} }
func (GlobTool) Risk(json.RawMessage) Risk { return RiskRead }

func (GlobTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input globInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if input.Pattern == "" {
		return Result{Output: "pattern is required", IsError: true}, nil
	}
	rootInput := input.Path
	if rootInput == "" {
		rootInput = "."
	}
	root, err := resolveInCWD(env.CWD, rootInput)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	matches, err := globMatches(ctx, env.CWD, root, input.Pattern)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	out := strings.Join(matches, "\n")
	if out == "" {
		out = "No matches found"
	}
	return Result{Output: out, Meta: map[string]any{"count": len(matches)}}, nil
}
