package agent

import (
	"encoding/json"
	"strings"
	"time"

	"autoto/internal/tools"
)

const toolEventVersion = 1

const (
	decisionSourceHardDangerBlock        = "hard_danger_block"
	decisionSourceCommandReview          = "command_review"
	decisionSourceDangerReflection       = "danger_reflection"
	decisionSourceReadOnlyCap            = "read_only_cap"
	decisionSourceRule                   = "rule"
	decisionSourceSessionApproval        = "session_approval"
	decisionSourceDefaultPolicy          = "default_policy"
	decisionSourcePolicyUnavailable      = "policy_unavailable"
	decisionSourceWorkflowUnavailable    = "workflow_unavailable"
	decisionSourcePlanMode               = "plan_mode"
	decisionSourceHumanApproval          = "human_approval"
	decisionSourceGenerationInvalidation = "generation_invalidation"
	decisionSourceSystem                 = "system"
)

// ToolEventMeta is the additive, versioned metadata shared by tool lifecycle
// events. InputJSON is the existing bounded/redacted activity projection, never
// the original tool arguments. Bash command facts are deliberately argument-free.
type ToolEventMeta struct {
	EventVersion         int                 `json:"eventVersion"`
	ToolUseID            string              `json:"toolUseId"`
	ToolName             string              `json:"toolName"`
	Risk                 tools.Risk          `json:"risk"`
	RunID                string              `json:"runId,omitempty"`
	InputJSON            json.RawMessage     `json:"inputJson"`
	InputTruncated       bool                `json:"inputTruncated,omitempty"`
	ExecutionDeviceID    string              `json:"executionDeviceId"`
	Decision             string              `json:"decision,omitempty"`
	DecisionSource       string              `json:"decisionSource,omitempty"`
	RuleID               string              `json:"ruleId,omitempty"`
	DecisionScope        string              `json:"decisionScope,omitempty"`
	CommandFacts         *tools.CommandFacts `json:"commandFacts,omitempty"`
	Status               string              `json:"status,omitempty"`
	DurationMS           *int64              `json:"durationMs,omitempty"`
	ResultPreview        string              `json:"resultPreview,omitempty"`
	ResultTruncated      bool                `json:"resultTruncated,omitempty"`
	Warning              string              `json:"warning,omitempty"`
	ExecutionMode        string              `json:"executionMode,omitempty"`
	Reason               string              `json:"reason,omitempty"`
	ExpiresAt            string              `json:"expiresAt,omitempty"`
	PermissionGeneration *int64              `json:"permissionGeneration,omitempty"`
	PolicyGeneration     *int64              `json:"policyGeneration,omitempty"`
}

// ToolEventMetaBuilder keeps all lifecycle variants on the same safe base
// projection while allowing each event to add its outcome or approval details.
type ToolEventMetaBuilder struct {
	meta ToolEventMeta
}

func NewToolEventMetaBuilder(call tools.Call, risk tools.Risk, executionDeviceID, runID string) ToolEventMetaBuilder {
	inputJSON, inputTruncated := toolEventInputProjection(call)
	meta := ToolEventMeta{
		EventVersion:      toolEventVersion,
		ToolUseID:         call.ID,
		ToolName:          call.Name,
		Risk:              risk,
		RunID:             runID,
		InputJSON:         inputJSON,
		InputTruncated:    inputTruncated,
		ExecutionDeviceID: normalizedExecutionDeviceID(executionDeviceID),
	}
	if call.Name == "Bash" {
		facts := tools.AnalyzeBashCommand(tools.BashCommand(call.Input))
		meta.CommandFacts = &facts
	}
	return ToolEventMetaBuilder{meta: meta}
}

func toolEventInputProjection(call tools.Call) (json.RawMessage, bool) {
	inputJSON, truncated := ProjectToolActivityInput(call.Name, call.Input, maxToolEventInputBytes)
	if call.Name != "Bash" {
		return inputJSON, truncated
	}
	var projected map[string]any
	if json.Unmarshal(inputJSON, &projected) != nil || projected == nil {
		return json.RawMessage(`{}`), true
	}
	if _, found := projected["command"]; found {
		delete(projected, "command")
		projected["commandPresent"] = true
		truncated = true
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return json.RawMessage(`{}`), true
	}
	return encoded, truncated
}

func (b ToolEventMetaBuilder) Decision(decision, source, ruleID, scope string) ToolEventMetaBuilder {
	b.meta.Decision = strings.TrimSpace(decision)
	b.meta.DecisionSource = strings.TrimSpace(source)
	b.meta.RuleID = strings.TrimSpace(ruleID)
	b.meta.DecisionScope = strings.TrimSpace(scope)
	return b
}

func (b ToolEventMetaBuilder) DecisionReason(reason string) ToolEventMetaBuilder {
	b.meta.Reason = boundedToolEventMetaText(reason)
	return b
}

func (b ToolEventMetaBuilder) Finished(result tools.Result, status string, durationMS int64) ToolEventMetaBuilder {
	b.meta.Status = strings.TrimSpace(status)
	durationMS = maxInt64(durationMS, 0)
	b.meta.DurationMS = &durationMS
	b.meta.ResultPreview, b.meta.ResultTruncated = boundedToolResultPreview(RedactToolActivityText(result.Output))
	return b
}

func (b ToolEventMetaBuilder) Approval(warning, reason string, expiresAt time.Time, permissionGeneration, policyGeneration int64) ToolEventMetaBuilder {
	b.meta.Warning = boundedToolEventMetaText(warning)
	b.meta.Reason = boundedToolEventMetaText(reason)
	if !expiresAt.IsZero() {
		b.meta.ExpiresAt = expiresAt.Format(time.RFC3339Nano)
	}
	if permissionGeneration > 0 {
		b.meta.PermissionGeneration = &permissionGeneration
	}
	if policyGeneration > 0 {
		b.meta.PolicyGeneration = &policyGeneration
	}
	return b
}

func (b ToolEventMetaBuilder) Extra(extra map[string]any) ToolEventMetaBuilder {
	if extra == nil {
		return b
	}
	if warning, ok := extra["warning"].(string); ok {
		b.meta.Warning = boundedToolEventMetaText(warning)
	}
	if mode, ok := extra["executionMode"].(ExecutionMode); ok {
		b.meta.ExecutionMode = string(mode)
	} else if mode, ok := extra["executionMode"].(string); ok {
		b.meta.ExecutionMode = mode
	}
	return b
}

func boundedToolEventMetaText(value string) string {
	bounded, _ := boundedToolEventString(RedactToolActivityText(value), maxToolEventInputStringBytes)
	return bounded
}

func (b ToolEventMetaBuilder) ToEventData() map[string]any {
	meta := b.meta
	data := map[string]any{
		"eventVersion":      meta.EventVersion,
		"toolUseId":         meta.ToolUseID,
		"toolName":          meta.ToolName,
		"risk":              meta.Risk,
		"runId":             meta.RunID,
		"inputJson":         meta.InputJSON,
		"executionDeviceId": meta.ExecutionDeviceID,
	}
	if meta.InputTruncated {
		data["inputTruncated"] = true
	}
	if meta.Decision != "" {
		data["decision"] = meta.Decision
	}
	if meta.DecisionSource != "" {
		data["decisionSource"] = meta.DecisionSource
	}
	if meta.RuleID != "" {
		data["ruleId"] = meta.RuleID
	}
	if meta.DecisionScope != "" {
		data["decisionScope"] = meta.DecisionScope
	}
	if meta.CommandFacts != nil {
		data["commandFacts"] = *meta.CommandFacts
	}
	if meta.Status != "" {
		data["status"] = meta.Status
	}
	if meta.DurationMS != nil {
		data["durationMs"] = *meta.DurationMS
	}
	if meta.ResultPreview != "" || meta.Status != "" {
		data["resultPreview"] = meta.ResultPreview
	}
	if meta.ResultTruncated {
		data["resultTruncated"] = true
	}
	if meta.Warning != "" {
		data["warning"] = meta.Warning
	}
	if meta.ExecutionMode != "" {
		data["executionMode"] = meta.ExecutionMode
	}
	if meta.Reason != "" {
		data["reason"] = meta.Reason
	}
	if meta.ExpiresAt != "" {
		data["expiresAt"] = meta.ExpiresAt
	}
	if meta.PermissionGeneration != nil {
		data["permissionGeneration"] = *meta.PermissionGeneration
	}
	if meta.PolicyGeneration != nil {
		data["policyGeneration"] = *meta.PolicyGeneration
	}
	return data
}
