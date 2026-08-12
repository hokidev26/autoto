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

// ReplayClass states whether a tool call that started but never reached a
// terminal result may be dispatched again after an interrupt. It is deliberately
// independent of Risk: Risk answers "may this run at all", ReplayClass answers
// "is running it a second time observably different from running it once".
//
// The two axes genuinely disagree. Bash running `ls` is effectively read-only
// yet carries RiskExec because it spawns a process, while a low-risk tool that
// opens a URL still has an external side effect. Deriving one from the other
// would be wrong in both directions.
type ReplayClass string

const (
	// ReplayNever forbids re-dispatch after an interrupt. Recovery must record a
	// terminal failure so the model can re-issue the call itself. This is the
	// default for every tool that does not opt in.
	ReplayNever ReplayClass = "never"
	// ReplaySafe permits one re-dispatch because the call has no observable
	// effect beyond its own result.
	ReplaySafe ReplayClass = "safe"
)

// ReplayClassifier is an optional extension. A tool that does not implement it
// is classified ReplayNever, so new tools and every dynamic plugin/MCP adapter
// fail closed without needing to know this interface exists.
//
// The input is supplied because one tool can be replay-safe for some arguments
// and not others; implementations must classify the specific call, not the tool
// in general.
type ReplayClassifier interface {
	ReplayClass(input json.RawMessage) ReplayClass
}

// ClassifyReplay derives the trusted replay class for one call. The tool's own
// answer is necessary but not sufficient: the call must also be RiskRead for
// this input. A dynamic tool from a plugin or MCP server can implement
// ReplayClassifier and claim ReplaySafe, and that assertion is not trustworthy
// on its own, so it is confirmed against the same risk signal the approval path
// already relies on. Anything unrecognised collapses to ReplayNever.
func ClassifyReplay(tool Tool, input json.RawMessage) ReplayClass {
	if tool == nil {
		return ReplayNever
	}
	classifier, ok := tool.(ReplayClassifier)
	if !ok {
		return ReplayNever
	}
	if classifier.ReplayClass(input) != ReplaySafe {
		return ReplayNever
	}
	if tool.Risk(input) != RiskRead {
		return ReplayNever
	}
	return ReplaySafe
}

// NormalizeReplayClass maps a persisted or otherwise untrusted value onto the
// known set, defaulting to ReplayNever. Recovery reads stored rows, and a row
// written by an older build, a hand-edited database, or a future value must not
// widen replay permission.
func NormalizeReplayClass(value string) ReplayClass {
	if ReplayClass(value) == ReplaySafe {
		return ReplaySafe
	}
	return ReplayNever
}

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
	PermissionModeCap string
	// ResumeParentSupported reports whether the current run can park on a
	// background task boundary and be woken when the task finishes. The Agent
	// tool defaults resume_parent from this, so a dispatch never promises a
	// wake-up that the run's continuation mode cannot deliver.
	ResumeParentSupported        bool
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
