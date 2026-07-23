package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type GrepTool struct{}

type grepInput struct {
	Pattern   string `json:"pattern"`
	Path      string `json:"path,omitempty"`
	HeadLimit int    `json:"head_limit,omitempty"`
}

func (GrepTool) Name() string { return "Grep" }
func (GrepTool) Description() string {
	return "Search text files under the agent working directory using a regular expression."
}
func (GrepTool) Schema() any               { return grepInput{} }
func (GrepTool) Risk(json.RawMessage) Risk { return RiskRead }

func (GrepTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input grepInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if input.Pattern == "" {
		return Result{Output: "pattern is required", IsError: true}, nil
	}
	re, err := regexp.Compile(input.Pattern)
	if err != nil {
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
	limit := input.HeadLimit
	if limit <= 0 {
		limit = 100
	}
	var lines []string
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if len(lines) >= limit {
			return nil
		}
		if d.IsDir() {
			if path != root && (heavyToolDirectory(d.Name()) || sensitiveToolPath(root, path)) {
				return filepath.SkipDir
			}
			return nil
		}
		resolved, err := resolveInCWD(env.CWD, path)
		if err != nil || sensitiveToolPath(root, resolved) {
			return nil
		}
		file, err := os.Open(resolved)
		if err != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			text := scanner.Text()
			if re.MatchString(text) {
				rel, _ := filepath.Rel(root, path)
				lines = append(lines, fmt.Sprintf("%s:%d:%s", rel, lineNo, text))
				if len(lines) >= limit {
					break
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return Result{Output: walkErr.Error(), IsError: true}, nil
	}
	out := strings.Join(lines, "\n")
	if out == "" {
		out = "No matches found"
	}
	return Result{Output: out, Meta: map[string]any{"count": len(lines)}}, nil
}
