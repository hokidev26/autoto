package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// AgentSendMessageTool sends one instruction or question to another existing
// conversation on this Autoto instance. It reuses the durable background-task
// channel that the Agent tool uses for spawned children, so the target's reply
// is reported back to this run through the same resumeParent wake-up instead of
// requiring the model to poll.
type AgentSendMessageTool struct{}

type agentSendMessageInput struct {
	AgentID     string `json:"agent_id" desc:"Target conversation id from AgentSnapshot. Must be another primary conversation on this instance, never this conversation or a subagent."`
	Message     string `json:"message" desc:"The instruction or question for the target conversation. It does not see this conversation, so include every detail it needs."`
	Description string `json:"description,omitempty" desc:"Short label for this exchange, shown in the task list."`
}

// agentMessagePayload is the durable payload for a send-message task. It rides
// the same background task kind as spawned children; targetAgentId is what
// switches the executor into the message branch.
type agentMessagePayload struct {
	Prompt        string `json:"prompt"`
	Description   string `json:"description,omitempty"`
	TargetAgentID string `json:"targetAgentId"`
}

type agentMessagePublicSummary struct {
	Description   string `json:"description,omitempty"`
	TargetAgentID string `json:"targetAgentId"`
	TargetTitle   string `json:"targetTitle,omitempty"`
}

func (AgentSendMessageTool) Name() string { return "AgentSendMessage" }

func (AgentSendMessageTool) Description() string {
	return "Send a message to another existing conversation on this local Autoto instance and return a durable task handle. The target conversation runs the message as its own turn under its own permissions, and its reply is reported back to this run when it finishes. Use AgentSnapshot first to find the target conversation id. To spawn a fresh worker instead of talking to an existing conversation, use the Agent tool."
}

func (AgentSendMessageTool) Schema() any               { return agentSendMessageInput{} }
func (AgentSendMessageTool) Risk(json.RawMessage) Risk { return RiskExec }

func (AgentSendMessageTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input agentSendMessageInput
	if err := decodePeerToolInput(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.Message = strings.TrimSpace(input.Message)
	input.Description = strings.TrimSpace(input.Description)
	if input.AgentID == "" {
		return Result{Output: "agent_id is required; call AgentSnapshot to list conversations", IsError: true}, nil
	}
	if input.Message == "" {
		return Result{Output: "message is required", IsError: true}, nil
	}
	if len([]byte(input.Message)) > 64*1024 {
		return Result{Output: "message exceeds size limit", IsError: true}, nil
	}
	if len([]byte(input.Description)) > 200 || len([]byte(input.AgentID)) > 128 {
		return agentToolError("message_payload_rejected", "message task metadata exceeds size limit"), nil
	}
	if input.AgentID == env.AgentID {
		return agentToolError("message_target_rejected", "a conversation cannot send a message to itself"), nil
	}
	if env.Background == nil {
		return Result{Output: "background task service is unavailable", IsError: true}, nil
	}
	targetTitle := ""
	// Fast, non-authoritative feedback only: the background executor re-checks
	// the target under the durable task, because the conversation can change
	// between this call and the worker picking the task up.
	if env.Store != nil {
		target, err := env.Store.GetAgent(ctx, input.AgentID)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return agentToolError("message_target_unavailable", "target conversation was not found; call AgentSnapshot to list conversations"), nil
		}
		if !isPrimaryAgentType(target.Type) {
			return agentToolError("message_target_rejected", "target is a subagent; messages can only be sent to primary conversations"), nil
		}
		if strings.TrimSpace(target.ArchivedAt) != "" {
			return agentToolError("message_target_rejected", "target conversation is archived"), nil
		}
		targetTitle = target.Title
	}
	resumeParent := env.ResumeParentSupported
	if resumeParent && strings.TrimSpace(env.RunID) == "" {
		resumeParent = false
	}
	payload, err := json.Marshal(agentMessagePayload{Prompt: input.Message, Description: input.Description, TargetAgentID: input.AgentID})
	if err != nil {
		return Result{}, err
	}
	// The task panel titles agent tasks from the public description; leaving it
	// empty rendered an unlabeled card, so name the exchange after its target.
	summaryDescription := input.Description
	if summaryDescription == "" && targetTitle != "" {
		summaryDescription = "Message to " + targetTitle
		if len([]byte(summaryDescription)) > 200 {
			summaryDescription = summaryDescription[:200]
			for len(summaryDescription) > 0 && !utf8.ValidString(summaryDescription) {
				summaryDescription = summaryDescription[:len(summaryDescription)-1]
			}
		}
	}
	publicSummary, _ := json.Marshal(agentMessagePublicSummary{Description: summaryDescription, TargetAgentID: input.AgentID, TargetTitle: targetTitle})
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
		return agentToolError("background_task_rejected", "message task could not be created"), nil
	}
	encoded, _ := json.Marshal(task)
	return Result{Output: string(encoded), Meta: map[string]any{"backgroundTaskId": task.ID, "background": true}}, nil
}
