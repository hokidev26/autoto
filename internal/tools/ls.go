package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	lsMaxEntries     = 1000
	lsMaxScanned     = 200000
	lsMaxDepth       = 10
	lsMaxOutputBytes = 100000
)

type lsEntry struct {
	rel   string
	isDir bool
	size  int64
}

// listDirectory collects entries beneath root down to depth levels, where depth 1
// is the immediate children. Heavy build/VCS directories are omitted entirely
// rather than merely not descended into, because listing thousands of vendored
// paths is never what the caller wanted.
func listDirectory(ctx context.Context, cwd, root string, depth int) ([]lsEntry, bool, error) {
	entries := make([]lsEntry, 0, 64)
	scanned := 0
	capped := false

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable subtree must not abort the whole listing.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if path == root {
			return nil
		}
		if scanned++; scanned > lsMaxScanned {
			capped = true
			return fs.SkipAll
		}
		if heavyToolDirectory(entry.Name()) || sensitiveToolPath(root, path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// Re-resolve through the workspace boundary so a symlink cannot surface a
		// path that lives outside the working directory.
		if _, resolveErr := resolveInCWD(cwd, path); resolveErr != nil {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if len(entries) >= lsMaxEntries {
			capped = true
			return fs.SkipAll
		}
		record := lsEntry{rel: rel, isDir: entry.IsDir()}
		if !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				record.size = info.Size()
			}
		}
		entries = append(entries, record)
		if entry.IsDir() && strings.Count(rel, "/")+1 >= depth {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil, capped, ctx.Err()
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, capped, err
	}
	// Plain lexicographic order on slash paths is deterministic and keeps every
	// child directly under its parent, because "a" sorts before "a/b" and "/"
	// sorts before any name character.
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	return entries, capped, nil
}

type LSTool struct{}

type lsInput struct {
	Path  string `json:"path,omitempty" desc:"Directory to list, absolute or relative to the working directory. Defaults to the working directory root. Paths outside the working directory and sensitive paths such as .env or .git are rejected."`
	Depth int    `json:"depth,omitempty" jsonschema:"minimum=1,maximum=10" desc:"How many directory levels to descend. 1 lists only the immediate children. Defaults to 1."`
}

func (LSTool) Name() string { return "LS" }
func (LSTool) Description() string {
	return "List directory entries under the agent working directory. Each line is 'dir <path>/' or 'file <path> <bytes>'. Build and VCS directories such as node_modules and .git are omitted."
}
func (LSTool) Schema() any               { return lsInput{} }
func (LSTool) Risk(json.RawMessage) Risk { return RiskRead }

func (LSTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input lsInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	rootInput := input.Path
	if rootInput == "" {
		rootInput = "."
	}
	root, err := resolveInCWD(env.CWD, rootInput)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if !info.IsDir() {
		return Result{Output: fmt.Sprintf("not a directory: %s", rootInput), IsError: true}, nil
	}
	depth := input.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > lsMaxDepth {
		depth = lsMaxDepth
	}
	entries, capped, err := listDirectory(ctx, env.CWD, root, depth)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if len(entries) == 0 {
		return Result{Output: "No entries found", Meta: map[string]any{"path": root, "count": 0, "truncated": false}}, nil
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.isDir {
			lines = append(lines, "dir  "+entry.rel+"/")
			continue
		}
		lines = append(lines, fmt.Sprintf("file %s %d", entry.rel, entry.size))
	}
	out, cut := truncate(strings.Join(lines, "\n"), lsMaxOutputBytes)
	if capped && !cut {
		out += "\n...[truncated]"
	}
	return Result{Output: out, Meta: map[string]any{
		"path":      root,
		"count":     len(entries),
		"truncated": capped || cut,
	}}, nil
}
