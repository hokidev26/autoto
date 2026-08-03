package tools

import (
	"context"
	"encoding/json"
)

const (
	BackgroundTaskKindShell = "shell"
	BackgroundTaskKindAgent = "agent"
)

type BackgroundRuntimeSettings struct {
	WorkerCount          int  `json:"workerCount"`
	PerAgentLimit        int  `json:"perAgentLimit"`
	AllowNestedSubagents bool `json:"allowNestedSubagents"`
	MaxSubagentDepth     int  `json:"maxSubagentDepth"`
}

type BackgroundRuntimeController interface {
	BackgroundRuntimeSettings() BackgroundRuntimeSettings
	UpdateBackgroundRuntimeSettings(BackgroundRuntimeSettings) (BackgroundRuntimeSettings, error)
}

// BackgroundTaskRequest is the sanitized control-plane request produced only
// after the normal tool risk and permission gateway has allowed the call.
// Implementations must still validate the frozen generations before starting.
type AgentTaskPreflightRequest struct {
	OwnerAgentID  string
	ParentRunID   string
	SubagentType  string
	ExplicitModel string
}

// AgentTaskPreflightService lets the concrete background service reject role or
// explicit-model requests before they become durable queued tasks.
type AgentTaskPreflightService interface {
	PreflightAgentTask(context.Context, AgentTaskPreflightRequest) error
}

type PreflightError struct {
	Code    string
	Message string
}

func (e *PreflightError) Error() string {
	if e == nil {
		return "agent task preflight failed"
	}
	return e.Message
}

func (e *PreflightError) ErrorCode() string {
	if e == nil || e.Code == "" {
		return "subagent_preflight_rejected"
	}
	return e.Code
}

type BackgroundTaskRequest struct {
	Kind                         string          `json:"kind"`
	OwnerAgentID                 string          `json:"ownerAgentId"`
	ParentRunID                  string          `json:"parentRunId,omitempty"`
	ParentToolUseID              string          `json:"parentToolUseId,omitempty"`
	CWD                          string          `json:"cwd,omitempty"`
	Payload                      json.RawMessage `json:"-"`
	PublicSummary                json.RawMessage `json:"publicSummary,omitempty"`
	ResumeParent                 bool            `json:"resumeParent"`
	PermissionModeCap            string          `json:"permissionModeCap,omitempty"`
	PermissionGenerationSnapshot int64           `json:"permissionGenerationSnapshot"`
	PolicyGenerationSnapshot     int64           `json:"policyGenerationSnapshot"`
	AgentGenerationSnapshot      int64           `json:"agentGenerationSnapshot"`
	ToolCatalogDigest            string          `json:"toolCatalogDigest,omitempty"`
	WorkspaceFingerprint         string          `json:"workspaceFingerprint,omitempty"`
}

type BackgroundTask struct {
	ID              string          `json:"id"`
	OwnerAgentID    string          `json:"ownerAgentId"`
	ParentRunID     string          `json:"parentRunId,omitempty"`
	ParentToolUseID string          `json:"parentToolUseId,omitempty"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	Revision        int64           `json:"revision"`
	ResumeParent    bool            `json:"resumeParent"`
	ChildAgentID    string          `json:"childAgentId,omitempty"`
	ChildRunID      string          `json:"childRunId,omitempty"`
	PublicSummary   json.RawMessage `json:"publicSummary,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	ErrorCode       string          `json:"errorCode,omitempty"`
	ErrorMessage    string          `json:"errorMessage,omitempty"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	OutputBytes     int64           `json:"outputBytes"`
	OutputTruncated bool            `json:"outputTruncated"`
	CreatedAt       string          `json:"createdAt"`
	StartedAt       string          `json:"startedAt,omitempty"`
	CompletedAt     string          `json:"completedAt,omitempty"`
	UpdatedAt       string          `json:"updatedAt"`
}

type BackgroundTaskListOptions struct {
	OwnerAgentID string
	Status       string
	Kind         string
	Limit        int
}

type BackgroundTaskOutputChunk struct {
	Sequence  int64  `json:"sequence"`
	Stream    string `json:"stream"`
	Text      string `json:"text"`
	ByteCount int    `json:"byteCount"`
	CreatedAt string `json:"createdAt"`
}

type BackgroundTaskOutputPage struct {
	TaskID       string                      `json:"taskId"`
	Chunks       []BackgroundTaskOutputChunk `json:"chunks"`
	NextSequence int64                       `json:"nextSequence"`
	HasMore      bool                        `json:"hasMore"`
	Truncated    bool                        `json:"truncated"`
}

// BackgroundTaskService is intentionally owned by the tools package so tools
// do not import the concrete background manager and create an agent/tools
// dependency cycle.
type BackgroundTaskService interface {
	Submit(context.Context, BackgroundTaskRequest) (BackgroundTask, error)
	List(context.Context, BackgroundTaskListOptions) ([]BackgroundTask, error)
	Get(context.Context, string, string) (BackgroundTask, error)
	Output(context.Context, string, string, int64, int) (BackgroundTaskOutputPage, error)
	Wait(context.Context, string, string, int64) (BackgroundTask, error)
	Cancel(context.Context, string, string) (BackgroundTask, error)
}
