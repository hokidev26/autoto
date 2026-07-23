package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

type GlobTool struct{}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
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
	matches, err := filepath.Glob(filepath.Join(root, input.Pattern))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	filtered := matches[:0]
	for _, match := range matches {
		resolved, err := resolveInCWD(env.CWD, match)
		if err != nil || sensitiveToolPath(root, resolved) {
			continue
		}
		if rel, err := filepath.Rel(root, resolved); err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			filtered = append(filtered, rel)
		}
	}
	matches = filtered
	out := strings.Join(matches, "\n")
	if out == "" {
		out = "No matches found"
	}
	return Result{Output: out, Meta: map[string]any{"count": len(matches)}}, nil
}
