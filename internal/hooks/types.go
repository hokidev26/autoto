package hooks

import (
	"encoding/json"
	"time"
)

type EventName string

const (
	EventRunBefore  EventName = "run.before"
	EventRunAfter   EventName = "run.after"
	EventToolBefore EventName = "tool.before"
	EventToolAfter  EventName = "tool.after"
)

type ScopeKind string

const (
	ScopeGlobal  ScopeKind = "global"
	ScopeProject ScopeKind = "project"
	ScopeAgent   ScopeKind = "agent"
)

type DispatchMode string

const (
	ModeSync  DispatchMode = "sync"
	ModeAsync DispatchMode = "async"
)

type FailurePolicy string

const (
	FailureContinue    FailurePolicy = "continue"
	FailureFailRun     FailurePolicy = "fail_run"
	FailureRetry       FailurePolicy = "retry"
	FailureDisableHook FailurePolicy = "disable_hook"
)

type ActionKind string

const (
	ActionShell ActionKind = "shell"
	ActionHTTP  ActionKind = "http"
	ActionLLM   ActionKind = "llm"
)

type Scope struct {
	Kind ScopeKind `json:"kind"`
	ID   string    `json:"id,omitempty"`
}

type Filter struct {
	ProjectIDs []string            `json:"projectIds,omitempty"`
	AgentIDs   []string            `json:"agentIds,omitempty"`
	ToolNames  []string            `json:"toolNames,omitempty"`
	RunKinds   []string            `json:"runKinds,omitempty"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

type ShellAction struct {
	Executable       string            `json:"executable"`
	Args             []string          `json:"args,omitempty"`
	CWD              string            `json:"cwd,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	SecretRefs       map[string]string `json:"secretRefs,omitempty"`
	TimeoutSeconds   int               `json:"timeoutSeconds,omitempty"`
	Detached         bool              `json:"detached,omitempty"`
	CanonicalStdinV1 bool              `json:"canonicalStdinV1"`
}

type HTTPAction struct {
	URL            string            `json:"url"`
	Method         string            `json:"method,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	SecretRefs     map[string]string `json:"secretRefs,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

type LLMAction struct {
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
	TimeoutSeconds  int    `json:"timeoutSeconds,omitempty"`
}

type Action struct {
	Kind  ActionKind   `json:"kind"`
	Shell *ShellAction `json:"shell,omitempty"`
	HTTP  *HTTPAction  `json:"http,omitempty"`
	LLM   *LLMAction   `json:"llm,omitempty"`
}

type Hook struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description,omitempty"`
	Enabled       bool          `json:"enabled"`
	Event         EventName     `json:"event"`
	Scope         Scope         `json:"scope"`
	Priority      int           `json:"priority"`
	Filter        Filter        `json:"filter,omitempty"`
	Mode          DispatchMode  `json:"mode"`
	FailurePolicy FailurePolicy `json:"failurePolicy"`
	Action        Action        `json:"action"`
	Revision      int64         `json:"revision"`
	CreatedAt     string        `json:"createdAt,omitempty"`
	UpdatedAt     string        `json:"updatedAt,omitempty"`
}

type Event struct {
	ID         string                     `json:"id,omitempty"`
	Name       EventName                  `json:"name"`
	RunID      string                     `json:"runId"`
	ProjectID  string                     `json:"projectId,omitempty"`
	AgentID    string                     `json:"agentId,omitempty"`
	RunKind    string                     `json:"runKind,omitempty"`
	ToolName   string                     `json:"toolName,omitempty"`
	OccurredAt time.Time                  `json:"occurredAt"`
	Attributes map[string]string          `json:"attributes,omitempty"`
	Payload    map[string]json.RawMessage `json:"payload,omitempty"`
}

type CanonicalEventStdin struct {
	SchemaVersion int                        `json:"schemaVersion"`
	EventID       string                     `json:"eventId,omitempty"`
	Event         EventName                  `json:"event"`
	RunID         string                     `json:"runId"`
	ProjectID     string                     `json:"projectId,omitempty"`
	AgentID       string                     `json:"agentId,omitempty"`
	RunKind       string                     `json:"runKind,omitempty"`
	ToolName      string                     `json:"toolName,omitempty"`
	OccurredAt    string                     `json:"occurredAt"`
	Attributes    map[string]string          `json:"attributes,omitempty"`
	Payload       map[string]json.RawMessage `json:"payload,omitempty"`
}

type Snapshot struct {
	Version   int    `json:"version"`
	Digest    string `json:"digest"`
	CreatedAt string `json:"createdAt"`
	Hooks     []Hook `json:"hooks"`
}

type RunBindingStatus string

const (
	BindingActive RunBindingStatus = "active"
	BindingClosed RunBindingStatus = "closed"
)

type EventStatus string

const (
	EventPending   EventStatus = "pending"
	EventRunning   EventStatus = "running"
	EventCompleted EventStatus = "completed"
	EventFailed    EventStatus = "failed"
	EventCancelled EventStatus = "cancelled"
)

type ExecutionStatus string

const (
	ExecutionPending   ExecutionStatus = "pending"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionSucceeded ExecutionStatus = "succeeded"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCancelled ExecutionStatus = "cancelled"
)

type AttemptStatus string

const (
	AttemptRunning   AttemptStatus = "running"
	AttemptSucceeded AttemptStatus = "succeeded"
	AttemptFailed    AttemptStatus = "failed"
	AttemptCancelled AttemptStatus = "cancelled"
)

type GateDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}
