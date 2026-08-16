package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type WriteTool struct{}

type writeInput struct {
	FilePath string `json:"file_path" desc:"Path to write, absolute or relative to the working directory. Creates parent directories as needed. Paths outside the working directory and sensitive files such as .env are rejected."`
	Content  string `json:"content" desc:"Full file contents. This replaces the whole file; use Edit to change part of an existing file."`
}

func (WriteTool) Name() string { return "Write" }
func (WriteTool) Description() string {
	return "Write a file under the agent working directory. The result starts with a file hash header so a later Edit can pass expected_hash."
}
func (WriteTool) Schema() any               { return writeInput{} }
func (WriteTool) Risk(json.RawMessage) Risk { return RiskWrite }

func (WriteTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input writeInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	path, err := resolveInCWD(env.CWD, input.FilePath)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := os.WriteFile(path, []byte(input.Content), 0o644); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	fp := hashBytes([]byte(input.Content))
	display := editDiffDisplayPath(env.CWD, input.FilePath, path)
	return withFileSnapshot(display, fp, fmt.Sprintf("Wrote %d bytes to %s", len(input.Content), path), map[string]any{"path": path}), nil
}
