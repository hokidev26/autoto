package tools

import (
	"context"
	"encoding/json"

	"autoto/internal/db"
)

type Risk string

const (
	RiskRead   Risk = "read"
	RiskWrite  Risk = "write"
	RiskExec   Risk = "exec"
	RiskDanger Risk = "danger"
)

type Call struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type Result struct {
	Output  string         `json:"output"`
	IsError bool           `json:"isError,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type OutputChunk struct {
	Text      string
	Stream    string
	Truncated bool
}

type ToolOutputPipelineScope struct {
	AgentID string
	RunID   string
}

type ToolOutputPipelineStartOptions struct {
	Label           string
	MaxPreviewChars int
}

type ToolOutputPipelineEndOptions struct {
	Aliases  []string
	Rule     string
	Format   string
	MaxChars int
	Discard  bool
}

type ToolOutputPipelineService interface {
	Start(ToolOutputPipelineScope, ToolOutputPipelineStartOptions) Result
	End(ToolOutputPipelineScope, ToolOutputPipelineEndOptions) Result
	ProcessResult(ToolOutputPipelineScope, Call, Result) Result
	IsActive(ToolOutputPipelineScope) bool
	CloseRun(ToolOutputPipelineScope)
	CloseAgent(string)
}

type Env struct {
	AgentID                      string
	RunID                        string
	CWD                          string
	Store                        *db.Store
	Output                       func(OutputChunk)
	Background                   BackgroundTaskService
	ContextAsk                   ContextAskService
	UserQuestion                 UserQuestionService
	ToolOutputPipeline           ToolOutputPipelineService
	PermissionModeCap            string
	PermissionGenerationSnapshot int64
	PolicyGenerationSnapshot     int64
	AgentGenerationSnapshot      int64
	ToolCatalogDigest            string
	WorkspaceFingerprint         string
}

// ResolutionContext scopes dynamic tools to the agent and working directory
// requesting them. Core registry tools remain process-wide and unscoped.
type ResolutionContext struct {
	AgentID string
	CWD     string
}

// ToolSource returns a point-in-time list of dynamic tools. Callers may retain
// the returned adapters for one agent run; adapters must validate mutable
// backing state again when executed.
type ToolSource interface {
	ListTools(context.Context, ResolutionContext) ([]Tool, error)
}

// Resolver resolves a dynamic tool for an out-of-band execution request.
// Implementations must fail closed when the backing plugin is disabled,
// removed, or no longer matches the adapter revision.
type Resolver interface {
	ResolveTool(context.Context, ResolutionContext, string) (Tool, error)
}

// CatalogMetadata describes optional presentation and provenance information
// for a tool. It intentionally excludes execution configuration and secrets.
type CatalogMetadata struct {
	Domain      string `json:"domain,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Source      string `json:"source,omitempty"`
	SourceID    string `json:"sourceId,omitempty"`
}

// CatalogMetadataProvider is an optional extension implemented by tools that
// can provide richer catalog grouping. Tool remains backwards compatible.
type CatalogMetadataProvider interface {
	CatalogMetadata() CatalogMetadata
}

// ToolAvailabilityResolver is the optional runner-facing policy boundary.
// TODO(optional-tools-integration): runner_tools should resolve the current
// project/workspace target and filter tools before advertising or executing
// them, without changing the Tool interface.
type ToolAvailabilityResolver interface {
	ResolveToolAvailability(context.Context, db.ToolAvailabilityTarget, string) (db.ToolAvailabilityDecision, error)
}

type Tool interface {
	Name() string
	Description() string
	Schema() any
	Risk(input json.RawMessage) Risk
	Execute(ctx context.Context, call Call, env Env) (Result, error)
}
