package tools

import (
	"context"
	"encoding/json"
	"strings"
)

const (
	StartPipelineToolName = "StartPipeline"
	EndPipelineToolName   = "EndPipeline"
)

type StartPipelineTool struct{}

type startPipelineInput struct {
	Label           string `json:"label,omitempty"`
	MaxPreviewChars int    `json:"max_preview_chars,omitempty"`
}

func (StartPipelineTool) Name() string { return StartPipelineToolName }
func (StartPipelineTool) Description() string {
	return "Start capturing subsequent tool outputs during a read-only or plan run. Use this only when several potentially large read results need to be filtered together; the model receives aliases and short previews until EndPipeline is called."
}
func (StartPipelineTool) Schema() any               { return startPipelineInput{} }
func (StartPipelineTool) Risk(json.RawMessage) Risk { return RiskRead }
func (StartPipelineTool) Execute(_ context.Context, call Call, env Env) (Result, error) {
	if env.ToolOutputPipeline == nil {
		return Result{Output: "pipeline_unavailable: tool output pipeline service is unavailable", IsError: true}, nil
	}
	var input startPipelineInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: "pipeline_rule_invalid: " + err.Error(), IsError: true}, nil
	}
	return env.ToolOutputPipeline.Start(
		ToolOutputPipelineScope{AgentID: env.AgentID, RunID: env.RunID},
		ToolOutputPipelineStartOptions{Label: strings.TrimSpace(input.Label), MaxPreviewChars: input.MaxPreviewChars},
	), nil
}

type EndPipelineTool struct{}

type endPipelineInput struct {
	Aliases  []string `json:"aliases,omitempty"`
	Rule     string   `json:"rule,omitempty"`
	Format   string   `json:"format,omitempty"`
	MaxChars int      `json:"max_chars,omitempty"`
	Discard  bool     `json:"discard,omitempty"`
}

func (EndPipelineTool) Name() string { return EndPipelineToolName }
func (EndPipelineTool) Description() string {
	return "Finish the active tool output pipeline, applying a restricted in-process rule to captured aliases. Supported operations are from, cat, grep, head, tail, sort, uniq, and cut; no shell command is executed."
}
func (EndPipelineTool) Schema() any               { return endPipelineInput{} }
func (EndPipelineTool) Risk(json.RawMessage) Risk { return RiskRead }
func (EndPipelineTool) Execute(_ context.Context, call Call, env Env) (Result, error) {
	if env.ToolOutputPipeline == nil {
		return Result{Output: "pipeline_unavailable: tool output pipeline service is unavailable", IsError: true}, nil
	}
	var input endPipelineInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: "pipeline_rule_invalid: " + err.Error(), IsError: true}, nil
	}
	return env.ToolOutputPipeline.End(
		ToolOutputPipelineScope{AgentID: env.AgentID, RunID: env.RunID},
		ToolOutputPipelineEndOptions{
			Aliases:  append([]string(nil), input.Aliases...),
			Rule:     strings.TrimSpace(input.Rule),
			Format:   strings.TrimSpace(input.Format),
			MaxChars: input.MaxChars,
			Discard:  input.Discard,
		},
	), nil
}

func IsToolOutputPipelineControl(name string) bool {
	switch strings.TrimSpace(name) {
	case StartPipelineToolName, EndPipelineToolName:
		return true
	default:
		return false
	}
}
