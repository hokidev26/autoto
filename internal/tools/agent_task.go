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
	Model              string   `json:"model,omitempty" desc:"Model override in provider:model form. Omit unless the user explicitly named a model; the child then inherits the preset or parent model. Never guess a model name."`
	Workdir            string   `json:"workdir,omitempty" desc:"Optional directory inside the parent agent workspace. Defaults to the parent agent working directory."`
	ReasoningEffort    string   `json:"reasoning_effort,omitempty" jsonschema:"enum=auto|low|medium|high|xhigh|max|ultra" desc:"Reasoning effort for the child agent."`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty" desc:"Completion checks for the child. These are checks only; they never grant the child extra permissions, tools, or scope."`
	RunInBackground    *bool    `json:"run_in_background,omitempty" desc:"Must be true. Child agents always run as background tasks; poll them with the Task tool."`
	// ResumeParent stays in the schema only so older conversations that still
	// send it keep decoding; its value is ignored. Models filled optional
	// booleans with false out of habit, which silently disabled the report the
	// user was waiting for, so reporting is no longer the model's decision.
	ResumeParent *bool `json:"resume_parent,omitempty" desc:"Deprecated and ignored. The parent run is resumed automatically when the child finishes, so its result is always reported back."`
}

type agentTaskPayload struct {
	Prompt             string   `json:"prompt"`
	Description        string   `json:"description,omitempty"`
	SubagentType       string   `json:"subagentType,omitempty"`
	Model              string   `json:"model,omitempty"`
	Workdir            string   `json:"workdir,omitempty"`
	ReasoningEffort    string   `json:"reasoningEffort,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
}

type agentTaskPublicSummary struct {
	Description           string `json:"description"`
	RequestedSubagentType string `json:"requestedSubagentType,omitempty"`
	SubagentType          string `json:"subagentType"`
	RequestedModel        string `json:"requestedModel,omitempty"`
	Model                 string `json:"model,omitempty"`
	Workdir               string `json:"workdir,omitempty"`
	AcceptanceCount       int    `json:"acceptanceCount,omitempty"`
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
	input.Workdir = strings.TrimSpace(input.Workdir)
	input.ReasoningEffort = strings.TrimSpace(input.ReasoningEffort)
	if input.Prompt == "" {
		return Result{Output: "prompt is required", IsError: true}, nil
	}
	if len([]byte(input.Prompt)) > 64*1024 {
		return Result{Output: "prompt exceeds size limit", IsError: true}, nil
	}
	if len([]byte(input.Description)) > 200 || len([]byte(input.Model)) > 256 || len([]byte(input.SubagentType)) > 64 || len([]byte(input.Workdir)) > 1024 {
		return agentToolError("subagent_payload_rejected", "agent task metadata exceeds size limit"), nil
	}
	requestedSubagentType := input.SubagentType
	if strings.EqualFold(input.SubagentType, "general-purpose") {
		input.SubagentType = "general"
	}
	if role, err := agentrole.Normalize(input.SubagentType); err == nil {
		input.SubagentType = string(role)
	} else {
		input.SubagentType = strings.ToLower(input.SubagentType)
		if !validAgentPresetKey(input.SubagentType) {
			return agentToolError("subagent_role_rejected", "invalid subagent_type"), nil
		}
	}
	if env.CWD != "" || input.Workdir != "" {
		workdir, err := ResolveWorkdirWithin(env.CWD, input.Workdir)
		if err != nil {
			return agentToolError("workdir_rejected", err.Error()), nil
		}
		input.Workdir = workdir
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
	case "", "auto", "low", "medium", "high", "xhigh", "max", "ultra":
	default:
		return Result{Output: "invalid reasoning_effort", IsError: true}, nil
	}
	if env.Background == nil {
		return Result{Output: "background task service is unavailable", IsError: true}, nil
	}
	// Reporting back is not negotiable per call: a dispatched child whose
	// outcome nobody relays is the failure mode, not the feature, and models
	// habitually sent resume_parent:false without meaning it. The runner marks
	// support only when the run can actually park and be woken, so this never
	// promises a resume the run cannot deliver.
	resumeParent := env.ResumeParentSupported
	if resumeParent && strings.TrimSpace(env.RunID) == "" {
		resumeParent = false
	}
	payload, err := json.Marshal(agentTaskPayload{
		Prompt: input.Prompt, Description: input.Description, SubagentType: input.SubagentType, Model: input.Model, Workdir: input.Workdir, ReasoningEffort: input.ReasoningEffort, AcceptanceCriteria: input.AcceptanceCriteria,
	})
	if err != nil {
		return Result{}, err
	}
	publicSummary, _ := json.Marshal(agentTaskPublicSummary{
		Description: input.Description, RequestedSubagentType: requestedSubagentType, SubagentType: input.SubagentType,
		RequestedModel: input.Model, Workdir: input.Workdir, AcceptanceCount: len(input.AcceptanceCriteria),
	})
	if preflight, ok := env.Background.(AgentTaskPreflightService); ok {
		if err := preflight.PreflightAgentTask(ctx, AgentTaskPreflightRequest{
			OwnerAgentID: env.AgentID, ParentRunID: env.RunID, SubagentType: input.SubagentType, ExplicitModel: input.Model,
		}); err != nil {
			code := "subagent_preflight_rejected"
			if coded, ok := err.(interface{ ErrorCode() string }); ok && coded.ErrorCode() != "" {
				code = coded.ErrorCode()
			}
			return agentToolError(code, err.Error()), nil
		}
	}
	task, err := env.Background.Submit(ctx, BackgroundTaskRequest{
		Kind:                         BackgroundTaskKindAgent,
		OwnerAgentID:                 env.AgentID,
		ParentRunID:                  env.RunID,
		ParentToolUseID:              call.ID,
		CWD:                          env.CWD,
		Payload:                      payload,
		PublicSummary:                publicSummary,
		ResumeParent:                 resumeParent,
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
		return agentToolError("background_task_rejected", "background agent task could not be created"), nil
	}
	encoded, _ := json.Marshal(task)
	return Result{Output: string(encoded), Meta: map[string]any{"backgroundTaskId": task.ID, "background": true}}, nil
}

func agentToolError(code, message string) Result {
	payload, _ := json.Marshal(map[string]string{"errorCode": code, "errorMessage": message})
	return Result{Output: string(payload), IsError: true, Meta: map[string]any{"errorCode": code, "errorMessage": message}}
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
