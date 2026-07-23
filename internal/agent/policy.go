package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// ExecutionMode is an immutable per-run capability boundary. A plan run can
// inspect approved read-only sources but can never execute its proposed plan.
type ExecutionMode string

const (
	ExecutionModeExecute ExecutionMode = "execute"
	ExecutionModePlan    ExecutionMode = "plan"
)

var planToolAllowlist = map[string]struct{}{
	"Read":             {},
	"Glob":             {},
	"Grep":             {},
	"WebFetch":         {},
	"WebSearch":        {},
	"ContextAsk":       {},
	"AskUserQuestion":  {},
	"StartPipeline":    {},
	"EndPipeline":      {},
}

var conversationResearchToolNames = []string{"WebFetch", "WebSearch", "AskUserQuestion"}

var conversationToolAllowlist = map[string]struct{}{
	"WebFetch":        {},
	"WebSearch":       {},
	"AskUserQuestion": {},
}

// PolicyContext is the single source of run capability decisions used by both
// looped and direct tool execution. It keeps a direct ExecuteTool caller from
// bypassing the mode that restricted the run itself.
type PolicyContext struct {
	AgentID           string
	RunID             string
	CWD               string
	PermissionMode    string
	ExecutionMode     ExecutionMode
	ExecutionDeviceID string
	Conversation      bool
}

func (p PolicyContext) IsPlan() bool {
	return p.ExecutionMode == ExecutionModePlan
}

func (p PolicyContext) IsConversation() bool {
	return p.Conversation
}

func (p PolicyContext) allowsToolOutputPipeline() bool {
	return p.IsPlan() || strings.TrimSpace(p.PermissionMode) == "readOnly"
}

func (p PolicyContext) permitsTool(name string, risk tools.Risk) (bool, string) {
	if p.IsConversation() {
		if risk != tools.RiskRead {
			return false, fmt.Sprintf("conversation context denies %s-risk tool %s", risk, name)
		}
		if _, ok := conversationToolAllowlist[name]; !ok {
			return false, fmt.Sprintf("conversation context only allows public WebFetch, WebSearch, and AskUserQuestion; %s is denied", name)
		}
		return true, ""
	}
	if tools.IsToolOutputPipelineControl(name) && !p.allowsToolOutputPipeline() {
		return false, fmt.Sprintf("tool output pipeline is only available in readOnly permission mode or plan execution mode; %s is denied", name)
	}
	if !p.IsPlan() {
		return true, ""
	}
	if risk != tools.RiskRead {
		return false, fmt.Sprintf("plan execution mode denies %s-risk tool %s", risk, name)
	}
	if _, ok := planToolAllowlist[name]; !ok {
		return false, fmt.Sprintf("plan execution mode only allows Read, Glob, Grep, WebFetch, WebSearch, ContextAsk, AskUserQuestion, StartPipeline, and EndPipeline; %s is denied", name)
	}
	return true, ""
}

func (p PolicyContext) filtersTool(name string) bool {
	if p.IsConversation() {
		_, allowed := conversationToolAllowlist[name]
		return !allowed
	}
	if p.IsPlan() {
		_, allowed := planToolAllowlist[name]
		return !allowed
	}
	return tools.IsToolOutputPipelineControl(name) && !p.allowsToolOutputPipeline()
}

func (r *Runner) policyContext(ctx context.Context, agentID, runID string) (db.Agent, PolicyContext, error) {
	if r == nil || r.store == nil {
		return db.Agent{}, PolicyContext{}, errors.New("agent runner is not initialized")
	}
	agent, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, PolicyContext{}, err
	}
	if deviceID := strings.TrimSpace(agent.ExecutionDeviceID); deviceID != "" && deviceID != "local" {
		return db.Agent{}, PolicyContext{}, fmt.Errorf("%w: agent %s targets device %s", ErrRemoteExecutionUnavailable, agent.ID, deviceID)
	}
	mode := executionModeForAgent(agent)
	conversation := false
	if strings.TrimSpace(runID) != "" {
		run, err := r.store.GetRun(ctx, agentID, runID)
		if err != nil {
			return db.Agent{}, PolicyContext{}, err
		}
		agent.PermissionMode = permissionModeWithCap(agent.PermissionMode, run.PermissionModeCap)
		mode = executionModeForRun(run)
		conversation = isConversationRun(run)
	}
	return agent, PolicyContext{
		AgentID:           agent.ID,
		RunID:             strings.TrimSpace(runID),
		CWD:               agent.CWD,
		PermissionMode:    agent.PermissionMode,
		ExecutionMode:     mode,
		ExecutionDeviceID: normalizedExecutionDeviceID(agent.ExecutionDeviceID),
		Conversation:      conversation,
	}, nil
}

func executionModeForAgent(agent db.Agent) ExecutionMode {
	if agent.PlanMode {
		return ExecutionModePlan
	}
	return ExecutionModeExecute
}

func isConversationRun(run db.Run) bool {
	return strings.TrimSpace(run.Source) == db.RunSourceConversation
}

// executionModeForRun reads the durable runs.execution_mode capability. A
// missing or invalid value is denied by treating it as plan mode.
func executionModeForRun(run db.Run) ExecutionMode {
	switch strings.TrimSpace(run.ExecutionMode) {
	case db.RunExecutionModeExecute:
		return ExecutionModeExecute
	case db.RunExecutionModePlan:
		return ExecutionModePlan
	default:
		return ExecutionModePlan
	}
}

func runExecutionModeForAgent(agent db.Agent) string {
	if executionModeForAgent(agent) == ExecutionModePlan {
		return db.RunExecutionModePlan
	}
	return db.RunExecutionModeExecute
}

func (r *Runner) snapshotToolsForPolicy(ctx context.Context, scope tools.ResolutionContext, policy PolicyContext) (runToolSnapshot, error) {
	var snapshot runToolSnapshot
	var err error
	if policy.IsConversation() {
		snapshot, err = r.snapshotConversationTools()
	} else {
		snapshot, err = r.snapshotTools(ctx, scope)
	}
	if err != nil {
		return snapshot, err
	}
	specs := make([]providers.ToolSpec, 0, len(snapshot.specs))
	for _, spec := range snapshot.specs {
		if policy.filtersTool(spec.Name) {
			continue
		}
		specs = append(specs, spec)
	}
	// Keep the immutable snapshot for the final execution gateway. Conversation
	// runs receive only the two built-in public-web tools and never enumerate
	// project-scoped dynamic tools.
	return runToolSnapshot{tools: snapshot.tools, specs: specs}, nil
}

func (r *Runner) snapshotConversationTools() (runToolSnapshot, error) {
	byName := make(map[string]tools.Tool, len(conversationResearchToolNames))
	specs := make([]providers.ToolSpec, 0, len(conversationResearchToolNames))
	if r == nil || r.tools == nil {
		return runToolSnapshot{tools: byName, specs: specs}, nil
	}
	for _, name := range conversationResearchToolNames {
		tool, ok := r.tools.Get(name)
		if !ok || tool == nil {
			continue
		}
		if strings.TrimSpace(tool.Name()) != name {
			return runToolSnapshot{}, fmt.Errorf("conversation research tool name mismatch: %s", name)
		}
		byName[name] = tool
		specs = append(specs, providers.ToolSpec{Name: name, Description: tool.Description(), Schema: toolInputSchema(tool.Schema())})
	}
	return runToolSnapshot{tools: byName, specs: specs}, nil
}

func planToolDeniedResult(policy PolicyContext, call tools.Call, risk tools.Risk) (tools.Result, bool) {
	if allowed, reason := policy.permitsTool(call.Name, risk); !allowed {
		return tools.Result{Output: reason, IsError: true}, true
	}
	return tools.Result{}, false
}

// policyToolCall normalizes a direct or looped call before policy evaluation.
func policyToolCall(call tools.Call) tools.Call {
	call = normalizeToolCall(call)
	if !json.Valid(call.Input) {
		call.Input = json.RawMessage(`{}`)
	}
	return call
}
