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
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func (WriteTool) Name() string              { return "Write" }
func (WriteTool) Description() string       { return "Write a file under the agent working directory." }
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
	return Result{Output: fmt.Sprintf("Wrote %d bytes to %s", len(input.Content), path), Meta: map[string]any{"path": path}}, nil
}
