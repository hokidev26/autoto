package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"autoto/internal/agentrole"
)

const (
	maxAcceptanceCriteriaItems      = 16
	maxAcceptanceCriterionBytes     = 1000
	maxAcceptanceCriteriaTotalBytes = maxAcceptanceCriteriaItems * maxAcceptanceCriterionBytes
)

type AgentTool struct{}

type agentTaskInput struct {
	Prompt             string   `json:"prompt" desc:"Self-contained instructions for the child agent. It does not see this conversation, so include every file path and detail it needs."`
	Description        string   `json:"description,omitempty" desc:"Short label for this task, shown in the task list."`
	SubagentType       string   `json:"subagent_type,omitempty" desc:"Configured agent preset to run as. Lower-case letters, digits, dot, underscore, and hyphen only."`
	Model              string   `json:"model,omitempty" desc:"Model override in provider:model form. Defaults to the preset or parent model."`
	ReasoningEffort    string   `json:"reasoning_effort,omitempty" jsonschema:"enum=auto|low|medium|high|xhigh" desc:"Reasoning effort for the child agent."`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty" desc:"Completion checks for the child. These are checks only; they never grant the child extra permissions, tools, or scope."`
	RunInBackground    *bool    `json:"run_in_background,omitempty" desc:"Must be true. Child agents always run as background tasks; poll them with the Task tool."`
	ResumeParent       bool     `json:"resume_parent,omitempty" desc:"Resume this run automatically once the child agent finishes."`
}

type agentTaskPayload struct {
	Prompt             string   `json:"prompt"`
	Description        string   `json:"description,omitempty"`
	SubagentType       string   `json:"subagentType,omitempty"`
	Model              string   `json:"model,omitempty"`
	ReasoningEffort    string   `json:"reasoningEffort,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
}

type agentTaskPublicSummary struct {
	Description     string `json:"description"`
	SubagentType    string `json:"subagentType"`
	Model           string `json:"model"`
	AcceptanceCount int    `json:"acceptanceCount,omitempty"`
}

func (AgentTool) Name() string { return "Agent" }
func (AgentTool) Description() string {
	return "Start a child agent as a durable background task and return its task handle immediately."
}
func (AgentTool) Schema() any               { return agentTaskInput{} }
func (AgentTool) Risk(json.RawMessage) Risk { return RiskExec }

func (AgentTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input agentTaskInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Description = strings.TrimSpace(input.Description)
	input.SubagentType = strings.TrimSpace(input.SubagentType)
	input.Model = strings.TrimSpace(input.Model)
	input.ReasoningEffort = strings.TrimSpace(input.ReasoningEffort)
	if input.Prompt == "" {
		return Result{Output: "prompt is required", IsError: true}, nil
	}
	if len([]byte(input.Prompt)) > 64*1024 {
		return Result{Output: "prompt exceeds size limit", IsError: true}, nil
	}
	if len([]byte(input.Description)) > 200 || len([]byte(input.Model)) > 256 || len([]byte(input.SubagentType)) > 64 {
		return Result{Output: "agent task metadata exceeds size limit", IsError: true}, nil
	}
	if role, err := agentrole.Normalize(input.SubagentType); err == nil {
		input.SubagentType = string(role)
	} else {
		input.SubagentType = strings.ToLower(input.SubagentType)
		if !validAgentPresetKey(input.SubagentType) {
			return Result{Output: "invalid subagent_type", IsError: true}, nil
		}
	}
	if len(input.AcceptanceCriteria) > maxAcceptanceCriteriaItems {
		return Result{Output: "acceptance_criteria exceeds item limit", IsError: true}, nil
	}
	acceptanceCriteriaBytes := 0
	for index, criterion := range input.AcceptanceCriteria {
		criterion = strings.TrimSpace(criterion)
		if criterion == "" {
			return Result{Output: "acceptance_criteria items must not be blank", IsError: true}, nil
		}
		criterionBytes := len([]byte(criterion))
		if criterionBytes > maxAcceptanceCriterionBytes {
			return Result{Output: "acceptance_criteria item exceeds size limit", IsError: true}, nil
		}
		acceptanceCriteriaBytes += criterionBytes
		if acceptanceCriteriaBytes > maxAcceptanceCriteriaTotalBytes {
			return Result{Output: "acceptance_criteria exceeds total size limit", IsError: true}, nil
		}
		input.AcceptanceCriteria[index] = criterion
	}
	if input.RunInBackground != nil && !*input.RunInBackground {
		return Result{Output: "foreground child agents are not supported; set run_in_background to true", IsError: true}, nil
	}
	switch input.ReasoningEffort {
	case "", "auto", "low", "medium", "high", "xhigh":
	default:
		return Result{Output: "invalid reasoning_effort", IsError: true}, nil
	}
	if env.Background == nil {
		return Result{Output: "background task service is unavailable", IsError: true}, nil
	}
	if input.ResumeParent && strings.TrimSpace(env.RunID) == "" {
		return Result{Output: "resume_parent requires a durable parent run", IsError: true}, nil
	}
	payload, err := json.Marshal(agentTaskPayload{
		Prompt: input.Prompt, Description: input.Description, SubagentType: input.SubagentType, Model: input.Model, ReasoningEffort: input.ReasoningEffort, AcceptanceCriteria: input.AcceptanceCriteria,
	})
	if err != nil {
		return Result{}, err
	}
	publicSummary, _ := json.Marshal(agentTaskPublicSummary{
		Description: input.Description, SubagentType: input.SubagentType, Model: input.Model, AcceptanceCount: len(input.AcceptanceCriteria),
	})
	task, err := env.Background.Submit(ctx, BackgroundTaskRequest{
		Kind:                         BackgroundTaskKindAgent,
		OwnerAgentID:                 env.AgentID,
		ParentRunID:                  env.RunID,
		ParentToolUseID:              call.ID,
		CWD:                          env.CWD,
		Payload:                      payload,
		PublicSummary:                publicSummary,
		ResumeParent:                 input.ResumeParent,
		PermissionModeCap:            env.PermissionModeCap,
		PermissionGenerationSnapshot: env.PermissionGenerationSnapshot,
		PolicyGenerationSnapshot:     env.PolicyGenerationSnapshot,
		AgentGenerationSnapshot:      env.AgentGenerationSnapshot,
		ToolCatalogDigest:            env.ToolCatalogDigest,
		WorkspaceFingerprint:         env.WorkspaceFingerprint,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return Result{}, err
		}
		return Result{Output: "background agent task could not be created", IsError: true}, nil
	}
	encoded, _ := json.Marshal(task)
	return Result{Output: string(encoded), Meta: map[string]any{"backgroundTaskId": task.ID, "background": true}}, nil
}

func validAgentPresetKey(value string) bool {
	if len(value) < 1 || len(value) > 64 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return false
	}
	return true
}
