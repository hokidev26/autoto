package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/hooks"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type runToolSnapshot struct {
	tools map[string]tools.Tool
	specs []providers.ToolSpec
}

type pendingApproval struct {
	AgentID              string
	RunID                string
	ToolUseID            string
	ToolName             string
	Input                json.RawMessage
	Risk                 tools.Risk
	CWD                  string
	Command              string
	Reason               string
	Warning              string
	GrantKey             string
	AllowSession         bool
	PermissionGeneration int64
	PolicyGeneration     int64
	ExpiresAt            time.Time
	Decision             chan ToolApprovalDecision
}

type ToolApprovalDecision struct {
	Decision             string
	Reason               string
	DecidedBy            string
	PermissionGeneration int64
	PolicyGeneration     int64
	GrantKey             string
}

type sessionGrant struct {
	PermissionGeneration int64
	PolicyGeneration     int64
}

type sessionApprovalTool interface {
	SessionApprovalAllowed() bool
}

type contextualSessionApprovalTool interface {
	SessionApprovalAllowedForInput(json.RawMessage) bool
}

func toolAllowsSessionApproval(tool tools.Tool, input json.RawMessage) bool {
	if policy, ok := tool.(contextualSessionApprovalTool); ok {
		return policy.SessionApprovalAllowedForInput(input)
	}
	policy, ok := tool.(sessionApprovalTool)
	return !ok || policy.SessionApprovalAllowed()
}

func (r *Runner) ApproveToolCall(ctx context.Context, agentID, toolUseID string, decision ToolApprovalDecision) (bool, error) {
	generations, err := r.store.GetPermissionGenerations(ctx, agentID)
	if err != nil {
		return false, err
	}
	decision.Decision = strings.TrimSpace(decision.Decision)
	if decision.Decision != "allow_once" && decision.Decision != "allow_session" && decision.Decision != "deny" {
		return false, fmt.Errorf("invalid approval decision: %s", decision.Decision)
	}
	key := approvalKey(agentID, toolUseID)
	r.approvalMu.Lock()
	approval := r.approvals[key]
	r.approvalMu.Unlock()
	if approval == nil {
		return false, nil
	}
	if decision.Decision == "allow_session" && !approval.AllowSession {
		return false, errors.New("session approval is unavailable for this tool; use allow_once")
	}
	if decision.PermissionGeneration != 0 && decision.PermissionGeneration != approval.PermissionGeneration {
		return false, fmt.Errorf("%w: pending approval permission generation changed", db.ErrConflict)
	}
	if decision.PolicyGeneration != 0 && decision.PolicyGeneration != approval.PolicyGeneration {
		return false, fmt.Errorf("%w: pending approval policy generation changed", db.ErrConflict)
	}
	if generations.Permission != approval.PermissionGeneration || generations.Policy != approval.PolicyGeneration {
		invalidated := ToolApprovalDecision{Decision: "deny", Reason: "tool approval invalidated by permission or policy change", DecidedBy: "system", PermissionGeneration: approval.PermissionGeneration, PolicyGeneration: approval.PolicyGeneration, GrantKey: approval.GrantKey}
		select {
		case approval.Decision <- invalidated:
		default:
		}
		return false, fmt.Errorf("%w: pending approval was invalidated by permission or policy change", db.ErrConflict)
	}
	decision.PermissionGeneration = approval.PermissionGeneration
	decision.PolicyGeneration = approval.PolicyGeneration
	decision.GrantKey = approval.GrantKey
	select {
	case approval.Decision <- decision:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	default:
		return false, nil
	}
}

type ToolInfo struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Risk        tools.Risk `json:"risk"`
}

func (r *Runner) ListTools() []ToolInfo {
	if r.tools == nil {
		return []ToolInfo{}
	}
	registered := r.tools.List()
	out := make([]ToolInfo, 0, len(registered))
	for _, tool := range registered {
		out = append(out, ToolInfo{Name: tool.Name(), Description: tool.Description(), Risk: conservativeToolRisk(tool.Name())})
	}
	return out
}

// ListToolsForAgent returns the same core tools as ListTools plus the dynamic
// tools currently enabled for the requested agent.
func (r *Runner) ListToolsForAgent(ctx context.Context, agentID string) ([]ToolInfo, error) {
	agent, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	snapshot, err := r.snapshotTools(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD})
	if err != nil {
		return nil, err
	}
	registered := make([]tools.Tool, 0, len(snapshot.tools))
	for _, tool := range snapshot.tools {
		registered = append(registered, tool)
	}
	sort.Slice(registered, func(i, j int) bool { return registered[i].Name() < registered[j].Name() })
	out := make([]ToolInfo, 0, len(registered))
	for _, tool := range registered {
		out = append(out, ToolInfo{Name: tool.Name(), Description: tool.Description(), Risk: conservativeToolRisk(tool.Name())})
	}
	return out, nil
}

func (r *Runner) snapshotTools(ctx context.Context, scope tools.ResolutionContext) (runToolSnapshot, error) {
	byName := make(map[string]tools.Tool)
	if r.tools != nil {
		for _, tool := range r.tools.List() {
			if tool == nil || strings.TrimSpace(tool.Name()) == "" {
				return runToolSnapshot{}, errors.New("core tool has an empty name")
			}
			byName[tool.Name()] = tool
		}
	}
	source, _ := r.dynamicTools()
	if source != nil {
		dynamic, err := source.ListTools(ctx, scope)
		if err != nil {
			return runToolSnapshot{}, err
		}
		for _, tool := range dynamic {
			if tool == nil || strings.TrimSpace(tool.Name()) == "" {
				return runToolSnapshot{}, errors.New("dynamic tool has an empty name")
			}
			if _, exists := byName[tool.Name()]; exists {
				return runToolSnapshot{}, fmt.Errorf("duplicate tool name: %s", tool.Name())
			}
			byName[tool.Name()] = tool
		}
	}
	if r.store != nil {
		availabilityScope := agentRuntimeScope{}
		if strings.TrimSpace(scope.AgentID) != "" {
			agent, err := r.store.GetAgent(ctx, scope.AgentID)
			if err != nil {
				return runToolSnapshot{}, errors.New("tool availability scope is unavailable")
			}
			availabilityScope, err = r.agentRuntimeScope(ctx, agent)
			if err != nil {
				return runToolSnapshot{}, errors.New("tool availability scope is unavailable")
			}
		}
		target := toolAvailabilityTarget(availabilityScope)
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		decisions, err := r.store.ResolveToolAvailabilities(ctx, target, names)
		if err != nil {
			return runToolSnapshot{}, errors.New("tool availability policy is unavailable")
		}
		for name := range byName {
			decision := decisions[name]
			if !decision.Enabled {
				delete(byName, name)
			}
		}
	}
	registered := make([]tools.Tool, 0, len(byName))
	for _, tool := range byName {
		registered = append(registered, tool)
	}
	sort.Slice(registered, func(i, j int) bool { return registered[i].Name() < registered[j].Name() })
	specs := make([]providers.ToolSpec, 0, len(registered))
	for _, tool := range registered {
		schema, err := checkedToolInputSchema(tool.Schema())
		if err != nil {
			return runToolSnapshot{}, fmt.Errorf("invalid schema for tool %s: %w", tool.Name(), err)
		}
		specs = append(specs, providers.ToolSpec{Name: tool.Name(), Description: tool.Description(), Schema: schema})
	}
	return runToolSnapshot{tools: byName, specs: specs}, nil
}

func (r *Runner) resolveTool(ctx context.Context, scope tools.ResolutionContext, name string, snapshot map[string]tools.Tool) (tools.Tool, error) {
	name = strings.TrimSpace(name)
	if snapshot != nil {
		tool, ok := snapshot[name]
		if !ok {
			return nil, errors.New("tool not found: " + name)
		}
		return tool, nil
	}
	if r.tools != nil {
		if tool, ok := r.tools.Get(name); ok {
			return tool, nil
		}
	}
	_, resolver := r.dynamicTools()
	if resolver == nil {
		return nil, errors.New("tool not found: " + name)
	}
	tool, err := resolver.ResolveTool(ctx, scope, name)
	if err != nil {
		return nil, err
	}
	if tool == nil || tool.Name() != name {
		return nil, errors.New("tool not found: " + name)
	}
	return tool, nil
}

// ExecuteTool is retained for out-of-band callers. It derives policy from the
// agent's current mode; callers holding a durable run should use ExecuteToolForRun.
func (r *Runner) ExecuteTool(ctx context.Context, agentID string, call tools.Call) (tools.Result, error) {
	return r.executeTool(ctx, agentID, "", call, "")
}

// ExecuteToolForRun shares the same PolicyContext and final execution gateway
// as the model loop, including the persisted run execution mode.
func (r *Runner) ExecuteToolForRun(ctx context.Context, agentID, runID string, call tools.Call) (tools.Result, error) {
	return r.executeTool(ctx, agentID, runID, call, "")
}

func (r *Runner) executeToolForLoop(ctx context.Context, agentID, runID string, call tools.Call, messageID string, snapshots ...map[string]tools.Tool) (result tools.Result, executeErr error) {
	call = policyToolCall(call)
	agent, policy, err := r.policyContext(ctx, agentID, runID)
	if err != nil {
		return tools.Result{}, err
	}
	executionDeviceID := policy.ExecutionDeviceID
	var snapshot map[string]tools.Tool
	if len(snapshots) > 0 {
		snapshot = snapshots[0]
	}
	if snapshot == nil && strings.TrimSpace(runID) != "" {
		if runtimeSnapshot, ok := r.runRuntimeSnapshot(runID); ok {
			snapshot = runtimeSnapshot.tools.tools
		} else if runtimeSnapshot, _, snapshotErr := r.prepareRunRuntimeSnapshot(ctx, agentID, runID); snapshotErr != nil {
			return tools.Result{}, snapshotErr
		} else {
			snapshot = runtimeSnapshot.tools.tools
		}
	}
	if snapshot == nil {
		current, snapshotErr := r.snapshotToolsForPolicy(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD}, policy)
		if snapshotErr != nil {
			return tools.Result{}, snapshotErr
		}
		snapshot = current.tools
	}
	tool, err := r.resolveTool(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD}, call.Name, snapshot)
	if err != nil {
		return tools.Result{}, err
	}
	call, err = normalizeToolCallInput(call, tool)
	if err != nil {
		return r.rejectInvalidToolInput(ctx, agentID, runID, messageID, call, executionDeviceID, err), nil
	}
	risk := tool.Risk(call.Input)
	// A session grant is one human's answer to one interactive question, and it
	// outranks execRequiresHumanReview because a person looked at that exact
	// command. An unattended Run has no such person: it would be spending a
	// grant that was given hours earlier, in a different Run, for a command the
	// scheduler happens to reissue at 03:00. Withholding the grant here also
	// keeps waitForToolApproval from offering "allow for session" to a Run whose
	// approver is answering from Telegram.
	allowSessionApproval := toolAllowsSessionApproval(tool, call.Input) && !policy.Unattended
	if result, denied := planToolDeniedResult(policy, call, risk); denied {
		source, scope := decisionSourcePlanMode, "plan"
		if policy.IsConversation() {
			source, scope = decisionSourceReadOnlyCap, "run"
		}
		resolution := toolPermissionResolution{Decision: toolPermissionDeny, Reason: result.Output, Warning: result.Output, Source: source, Scope: scope}
		r.recordImmediateToolResult(ctx, agentID, runID, messageID, call, risk, result, "denied", result.Output)
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, map[string]any{"warning": result.Output, "executionMode": policy.ExecutionMode}, resolution)})
		return result, nil
	}
	if risk == tools.RiskDanger {
		warning := toolRiskWarning(call.Name, call.Input)
		result := tools.Result{Output: dangerBlockedMessage(warning), IsError: true}
		resolution := toolPermissionResolution{Decision: toolPermissionDeny, Reason: warning, Warning: warning, Source: decisionSourceHardDangerBlock}
		r.recordImmediateToolResult(ctx, agentID, runID, messageID, call, risk, result, "denied", warning)
		r.publish(Event{Type: "tool.approval_required", AgentID: agentID, Data: mergeEventData(approvalEventDataWithResolution(agent, call, risk, warning, "danger", time.Time{}, 0, 0, resolution), runID)})
		r.notify(NotificationEvent{Event: "approval_required", RunID: runID, AgentID: agentID, Status: "pending_approval", ToolUseID: call.ID, ToolName: call.Name})
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, map[string]any{"warning": warning}, resolution)})
		return result, nil
	}
	permission := r.resolveToolPermissionWithSession(ctx, policy.AgentID, policy.PermissionMode, call.Name, risk, call.Input, allowSessionApproval)
	// The model reflects on any action that static policy would let run with no
	// human involvement. It can only make the outcome stricter.
	permission = r.reflectBeforeExecution(ctx, agent, policy.PermissionMode, runID, call, risk, permission)
	if permission.Decision == toolPermissionAllow {
		return r.executeApprovedTool(ctx, agent, runID, call, tool, risk, messageID, false, permission)
	}
	if permission.Decision == toolPermissionDeny {
		message := strings.TrimSpace(permission.Reason)
		if message == "" {
			message = "tool call denied by permission policy"
		}
		result := tools.Result{Output: message, IsError: true}
		r.recordImmediateToolResult(ctx, agentID, runID, messageID, call, risk, result, "denied", message)
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, nil, permission)})
		return result, nil
	}
	decision, err := r.waitForToolApproval(ctx, agent, runID, call, risk, messageID, permission, allowSessionApproval)
	if err != nil {
		return tools.Result{}, err
	}
	if decision.Decision == "deny" {
		message := strings.TrimSpace(decision.Reason)
		if message == "" {
			message = "tool call denied by user"
		}
		result := tools.Result{Output: message, IsError: true}
		r.updatePendingToolResult(ctx, agentID, call.ID, result, "denied", 0)
		source, scope := decisionSourceHumanApproval, "once"
		if strings.EqualFold(strings.TrimSpace(decision.DecidedBy), "system") {
			source, scope = decisionSourceSystem, "tool_call"
		}
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, nil, toolPermissionResolution{Decision: toolPermissionDeny, Reason: message, Source: source, Scope: scope})})
		return result, nil
	}
	current, err := r.approvalGenerationsCurrent(ctx, agentID, decision.PermissionGeneration, decision.PolicyGeneration)
	if err != nil {
		return tools.Result{}, err
	}
	if !current {
		message := "tool approval invalidated by permission or policy change"
		result := tools.Result{Output: message, IsError: true}
		r.updatePendingToolResult(ctx, agentID, call.ID, result, "denied", 0)
		resolution := toolPermissionResolution{Decision: toolPermissionDeny, Reason: message, Source: decisionSourceGenerationInvalidation}
		r.publish(Event{Type: "tool.approval_invalidated", AgentID: agentID, Data: mergeEventData(NewToolEventMetaBuilder(call, risk, executionDeviceID, runID).Decision(resolution.Decision, resolution.Source, "", "").Finished(result, "denied", 0).Approval("", message, time.Time{}, decision.PermissionGeneration, decision.PolicyGeneration).ToEventData(), runID)})
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, nil, resolution)})
		return result, nil
	}
	if decision.Decision == "allow_session" {
		r.addSessionGrant(agentID, decision.GrantKey, decision.PermissionGeneration, decision.PolicyGeneration)
	}
	if err := r.store.UpdateToolCallApproval(ctx, agentID, call.ID, "approved", decision.DecidedBy, "", decision.Reason, ""); err != nil {
		slog.Warn("record tool approval failed", "agentId", agentID, "toolUseId", call.ID, "error", err)
	}
	humanResolution := toolPermissionResolution{Decision: toolPermissionAllow, Reason: decision.Reason, Source: decisionSourceHumanApproval}
	if decision.Decision == "allow_session" {
		humanResolution.Scope = "session"
	} else {
		humanResolution.Scope = "once"
	}
	return r.executeApprovedTool(ctx, agent, runID, call, tool, risk, messageID, true, humanResolution)
}

func (r *Runner) executeTool(ctx context.Context, agentID, runID string, call tools.Call, messageID string) (result tools.Result, executeErr error) {
	call = policyToolCall(call)
	agent, policy, err := r.policyContext(ctx, agentID, runID)
	if err != nil {
		return tools.Result{}, err
	}
	executionDeviceID := policy.ExecutionDeviceID
	var toolSnapshot runToolSnapshot
	if strings.TrimSpace(runID) != "" {
		if runtimeSnapshot, ok := r.runRuntimeSnapshot(runID); ok {
			toolSnapshot = runtimeSnapshot.tools
		} else {
			runtimeSnapshot, _, snapshotErr := r.prepareRunRuntimeSnapshot(ctx, agentID, runID)
			if snapshotErr != nil {
				return tools.Result{}, snapshotErr
			}
			toolSnapshot = runtimeSnapshot.tools
		}
	} else {
		toolSnapshot, err = r.snapshotToolsForPolicy(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD}, policy)
		if err != nil {
			// Catalog snapshots must reject any invalid schema. For a direct call,
			// resolve only the requested tool to distinguish its own invalid schema
			// from an unrelated catalog failure, while still refusing execution.
			calledTool, resolveErr := r.resolveTool(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD}, call.Name, nil)
			if resolveErr == nil {
				if _, schemaErr := checkedToolInputSchema(calledTool.Schema()); schemaErr != nil {
					inputErr := fmt.Errorf("invalid schema for tool %s: %w", calledTool.Name(), schemaErr)
					return r.rejectInvalidToolInput(ctx, agentID, runID, messageID, call, executionDeviceID, inputErr), nil
				}
			}
			return tools.Result{}, err
		}
	}
	tool, err := r.resolveTool(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD}, call.Name, toolSnapshot.tools)
	if err != nil {
		return tools.Result{}, err
	}
	call, err = normalizeToolCallInput(call, tool)
	if err != nil {
		return r.rejectInvalidToolInput(ctx, agentID, runID, messageID, call, executionDeviceID, err), nil
	}
	risk := tool.Risk(call.Input)
	if tools.IsToolOutputPipelineControl(call.Name) {
		message := "tool output pipeline control tools are only available inside the model loop"
		result := tools.Result{Output: "pipeline_operation_not_allowed: " + message, IsError: true}
		resolution := toolPermissionResolution{Decision: toolPermissionDeny, Reason: message, Warning: message, Source: decisionSourceDefaultPolicy, Scope: "tool_call"}
		r.recordImmediateToolResult(ctx, agentID, runID, messageID, call, risk, result, "denied", message)
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, map[string]any{"warning": message}, resolution)})
		return result, nil
	}
	if result, denied := planToolDeniedResult(policy, call, risk); denied {
		source, scope := decisionSourcePlanMode, "plan"
		if policy.IsConversation() {
			source, scope = decisionSourceReadOnlyCap, "run"
		}
		resolution := toolPermissionResolution{Decision: toolPermissionDeny, Reason: result.Output, Warning: result.Output, Source: source, Scope: scope}
		r.recordImmediateToolResult(ctx, agentID, runID, messageID, call, risk, result, "denied", result.Output)
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, map[string]any{"warning": result.Output, "executionMode": policy.ExecutionMode}, resolution)})
		return result, nil
	}
	permission := r.resolveToolPermissionWithSession(ctx, policy.AgentID, policy.PermissionMode, call.Name, risk, call.Input, !policy.Unattended)
	permission = r.reflectBeforeExecution(ctx, agent, policy.PermissionMode, runID, call, risk, permission)
	if permission.Decision != toolPermissionAllow {
		message := strings.TrimSpace(permission.Reason)
		if permission.Decision == toolPermissionAsk {
			message = "tool call requires approval in an agent loop"
			// No human can answer inside a loop, so this text is the only thing
			// the model gets back. When danger reflection is what escalated the
			// call, pass its reason and safer alternative through so the model can
			// choose a different approach instead of retrying the same action.
			if permission.Source == decisionSourceDangerReflection {
				if detail := strings.TrimSpace(permission.Warning); detail != "" {
					message += ": " + detail
				}
			}
		}
		if message == "" {
			message = "tool call denied by permission policy"
		}
		result := tools.Result{Output: message, IsError: true}
		output, _ := json.Marshal(result)
		if _, err := r.store.AddToolCall(ctx, db.ToolCall{AgentID: agentID, RunID: runID, MessageID: messageID, ToolUseID: call.ID, ToolName: call.Name, InputJSON: call.Input, OutputJSON: output, Status: "denied", ErrorMessage: result.Output, PermissionDecidedBy: "policy", PermissionDecidedAt: db.Now(), PermissionDenyMessage: result.Output, PermissionDecisionReason: permission.Reason, PermissionSuggestions: permission.Warning}); err != nil {
			slog.Warn("record denied tool call failed", "agentId", agentID, "toolUseId", call.ID, "error", err)
		}
		r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "denied", 0, nil, permission)})
		return result, nil
	}
	unlockGitMutation := runGitMutationLock(ctx, agent.CWD, risk)
	defer unlockGitMutation()
	if _, err := r.store.AddToolCall(ctx, db.ToolCall{AgentID: agentID, RunID: runID, MessageID: messageID, ToolUseID: call.ID, ToolName: call.Name, InputJSON: call.Input, Status: "running", PermissionDecidedBy: "policy", PermissionDecidedAt: db.Now(), PermissionDecisionReason: autoApprovalReasonWithPolicy(call.Name, call.Input, permission.Reason)}); err != nil {
		return tools.Result{}, fmt.Errorf("persist running tool call: %w", err)
	}
	r.publish(Event{Type: "tool.started", AgentID: agentID, Data: toolStartedEventDataWithResolution(call, risk, executionDeviceID, runID, permission)})
	if hookErr := r.dispatchToolLifecycle(ctx, agentID, runID, hooks.EventToolBefore, call, nil, nil); hookErr != nil {
		return r.finishToolSetupFailure(ctx, agentID, runID, call, risk, executionDeviceID, permission, hookErr)
	}
	env, err := r.toolExecutionEnv(ctx, agent, runID, r.toolOutputPublisher(agentID, runID, call))
	if err != nil {
		return r.finishToolSetupFailure(ctx, agentID, runID, call, risk, executionDeviceID, permission, err)
	}
	started := time.Now()
	result, executeErr = tool.Execute(ctx, call, env)
	duration := time.Since(started).Milliseconds()
	output, _ := json.Marshal(result)
	status := "completed"
	errMsg := ""
	if result.IsError {
		status = "error"
		errMsg = result.Output
	}
	if executeErr != nil {
		status = "error"
		errMsg = executeErr.Error()
	}
	if recordErr := r.store.UpdateToolCallResult(ctx, agentID, call.ID, output, status, duration, errMsg); recordErr != nil {
		slog.Warn("record tool call result failed", "agentId", agentID, "toolUseId", call.ID, "error", recordErr)
	}
	r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, status, duration, nil, permission)})
	if hookErr := r.dispatchToolLifecycle(ctx, agentID, runID, hooks.EventToolAfter, call, &result, executeErr); hookErr != nil && executeErr == nil {
		executeErr = hookErr
	}
	return result, executeErr
}

func normalizeToolCallInput(call tools.Call, tool tools.Tool) (tools.Call, error) {
	if tool == nil {
		return call, errors.New("tool is unavailable")
	}
	schema, err := checkedToolInputSchema(tool.Schema())
	if err != nil {
		return call, fmt.Errorf("invalid schema for tool %s: %w", tool.Name(), err)
	}
	normalized, err := NormalizeToolInput(call.Input, schema)
	if err != nil {
		return call, err
	}
	call.Input = normalized
	return call, nil
}

func (r *Runner) rejectInvalidToolInput(ctx context.Context, agentID, runID, messageID string, call tools.Call, executionDeviceID string, inputErr error) tools.Result {
	message := "invalid tool input: " + strings.TrimSpace(inputErr.Error())
	result := tools.Result{Output: message, IsError: true}
	resolution := toolPermissionResolution{Decision: toolPermissionDeny, Reason: message, Warning: message, Source: decisionSourceDefaultPolicy, Scope: "tool_call"}
	r.recordImmediateToolResult(ctx, agentID, runID, messageID, call, tools.Risk(""), result, "error", message)
	r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, tools.Risk(""), executionDeviceID, runID, result, "error", 0, map[string]any{"warning": message}, resolution)})
	return result
}

func (r *Runner) finishToolSetupFailure(ctx context.Context, agentID, runID string, call tools.Call, risk tools.Risk, executionDeviceID string, permission toolPermissionResolution, setupErr error) (tools.Result, error) {
	message := strings.TrimSpace(setupErr.Error())
	if message == "" {
		message = "tool execution environment is unavailable"
	}
	result := tools.Result{Output: message, IsError: true}
	output, _ := json.Marshal(result)
	if recordErr := r.store.UpdateToolCallResult(ctx, agentID, call.ID, output, "error", 0, message); recordErr != nil {
		slog.Warn("finalize tool setup failure failed", "agentId", agentID, "toolUseId", call.ID, "error", recordErr)
	}
	r.publish(Event{Type: "tool.finished", AgentID: agentID, Data: toolFinishedEventDataWithResolution(call, risk, executionDeviceID, runID, result, "error", 0, nil, permission)})
	return result, setupErr
}

func (r *Runner) toolOutputPublisher(agentID, runID string, call tools.Call) func(tools.OutputChunk) {
	return func(chunk tools.OutputChunk) {
		if chunk.Text == "" {
			return
		}
		stream := strings.TrimSpace(chunk.Stream)
		if stream == "" {
			stream = "combined"
		}
		data := map[string]any{"toolUseId": call.ID, "toolName": call.Name, "stream": stream}
		if chunk.Truncated {
			data["truncated"] = true
		}
		r.publish(Event{Type: "tool.output", AgentID: agentID, Text: chunk.Text, Data: mergeEventData(data, runID)})
	}
}

func (r *Runner) executeApprovedTool(ctx context.Context, agent db.Agent, runID string, call tools.Call, tool tools.Tool, risk tools.Risk, messageID string, updateExisting bool, permission toolPermissionResolution) (tools.Result, error) {
	unlockGitMutation := runGitMutationLock(ctx, agent.CWD, risk)
	defer unlockGitMutation()
	if updateExisting {
		if err := r.store.MarkToolCallRunning(ctx, agent.ID, call.ID); err != nil {
			return tools.Result{}, fmt.Errorf("persist approved tool call as running: %w", err)
		}
	} else if _, err := r.store.AddToolCall(ctx, db.ToolCall{AgentID: agent.ID, RunID: runID, MessageID: messageID, ToolUseID: call.ID, ToolName: call.Name, InputJSON: call.Input, Status: "running", PermissionDecidedBy: "policy", PermissionDecidedAt: db.Now(), PermissionDecisionReason: autoApprovalReasonWithPolicy(call.Name, call.Input, permission.Reason)}); err != nil {
		return tools.Result{}, fmt.Errorf("persist running tool call: %w", err)
	}
	r.publish(Event{Type: "tool.started", AgentID: agent.ID, Data: toolStartedEventDataWithResolution(call, risk, normalizedExecutionDeviceID(agent.ExecutionDeviceID), runID, permission)})
	if hookErr := r.dispatchToolLifecycle(ctx, agent.ID, runID, hooks.EventToolBefore, call, nil, nil); hookErr != nil {
		return r.finishToolSetupFailure(ctx, agent.ID, runID, call, risk, normalizedExecutionDeviceID(agent.ExecutionDeviceID), permission, hookErr)
	}
	gitBefore := r.captureRunToolGitBefore(ctx, agent, runID, risk)
	env, err := r.toolExecutionEnv(ctx, agent, runID, r.toolOutputPublisher(agent.ID, runID, call))
	if err != nil {
		return r.finishToolSetupFailure(ctx, agent.ID, runID, call, risk, normalizedExecutionDeviceID(agent.ExecutionDeviceID), permission, err)
	}
	started := time.Now()
	result, err := tool.Execute(ctx, call, env)
	r.captureRunToolGitAfter(context.Background(), runID, gitBefore)
	duration := time.Since(started).Milliseconds()
	status := "completed"
	errMsg := ""
	if result.IsError {
		status = "error"
		errMsg = result.Output
	}
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	output, _ := json.Marshal(result)
	if recordErr := r.store.UpdateToolCallResult(ctx, agent.ID, call.ID, output, status, duration, errMsg); recordErr != nil {
		slog.Warn("update tool call result failed", "agentId", agent.ID, "toolUseId", call.ID, "error", recordErr)
	}
	r.publish(Event{Type: "tool.finished", AgentID: agent.ID, Data: toolFinishedEventDataWithResolution(call, risk, normalizedExecutionDeviceID(agent.ExecutionDeviceID), runID, result, status, duration, nil, permission)})
	if hookErr := r.dispatchToolLifecycle(ctx, agent.ID, runID, hooks.EventToolAfter, call, &result, err); hookErr != nil && err == nil {
		err = hookErr
	}
	return result, err
}

func (r *Runner) waitForToolApproval(ctx context.Context, agent db.Agent, runID string, call tools.Call, risk tools.Risk, messageID string, resolution toolPermissionResolution, allowSession bool) (ToolApprovalDecision, error) {
	command := toolCommand(call.Name, call.Input)
	reason := resolution.Reason
	warning := resolution.Warning
	if strings.TrimSpace(reason) == "" {
		reason = defaultApprovalReason(risk)
	}
	if strings.TrimSpace(warning) == "" {
		warning = defaultApprovalWarning(call.Name, risk, call.Input)
	}
	resolution.Decision = toolPermissionAsk
	if resolution.Source == "" {
		resolution.Source = decisionSourceDefaultPolicy
	}
	generations, err := r.store.GetPermissionGenerations(ctx, agent.ID)
	if err != nil {
		return ToolApprovalDecision{}, err
	}
	approval := &pendingApproval{
		AgentID:              agent.ID,
		RunID:                runID,
		ToolUseID:            call.ID,
		ToolName:             call.Name,
		Input:                call.Input,
		Risk:                 risk,
		CWD:                  agent.CWD,
		Command:              command,
		Reason:               reason,
		Warning:              warning,
		GrantKey:             sessionGrantKey(call.Name, call.Input),
		AllowSession:         allowSession,
		PermissionGeneration: generations.Permission,
		PolicyGeneration:     generations.Policy,
		ExpiresAt:            time.Now().Add(toolApprovalTimeout),
		Decision:             make(chan ToolApprovalDecision, 1),
	}
	if _, err := r.store.AddToolCall(ctx, db.ToolCall{AgentID: agent.ID, RunID: runID, MessageID: messageID, ToolUseID: call.ID, ToolName: call.Name, InputJSON: call.Input, Status: "pending_approval", PermissionDecisionReason: approval.Reason, PermissionSuggestions: approval.Warning, PermissionGeneration: approval.PermissionGeneration, PolicyGeneration: approval.PolicyGeneration}); err != nil {
		return ToolApprovalDecision{}, err
	}
	r.addPendingApproval(approval)
	defer r.removePendingApproval(agent.ID, call.ID)
	approvalData := approvalEventDataWithResolution(agent, call, risk, approval.Warning, approval.Reason, approval.ExpiresAt, approval.PermissionGeneration, approval.PolicyGeneration, resolution)
	r.publish(Event{Type: "tool.approval_required", AgentID: agent.ID, Data: mergeEventData(approvalData, runID)})
	r.notify(NotificationEvent{Event: "approval_required", RunID: runID, AgentID: agent.ID, Status: "pending_approval", ToolUseID: call.ID, ToolName: call.Name})

	timer := time.NewTimer(toolApprovalTimeout)
	defer timer.Stop()
	select {
	case decision := <-approval.Decision:
		if decision.DecidedBy == "" {
			decision.DecidedBy = "user"
		}
		decision.PermissionGeneration = approval.PermissionGeneration
		decision.PolicyGeneration = approval.PolicyGeneration
		decision.GrantKey = approval.GrantKey
		if decision.Decision == "deny" {
			_ = r.store.UpdateToolCallApproval(context.Background(), agent.ID, call.ID, "denied", decision.DecidedBy, decision.Reason, decision.Reason, approval.Warning)
		}
		return decision, nil
	case <-timer.C:
		decision := ToolApprovalDecision{Decision: "deny", Reason: "tool approval timed out", DecidedBy: "system"}
		_ = r.store.UpdateToolCallApproval(context.Background(), agent.ID, call.ID, "denied", decision.DecidedBy, decision.Reason, decision.Reason, approval.Warning)
		return decision, nil
	case <-ctx.Done():
		_ = r.store.UpdateToolCallApproval(context.Background(), agent.ID, call.ID, "denied", "system", "tool approval canceled", "tool approval canceled", approval.Warning)
		return ToolApprovalDecision{}, ctx.Err()
	}
}

func (r *Runner) addPendingApproval(approval *pendingApproval) {
	r.approvalMu.Lock()
	if r.approvals == nil {
		r.approvals = make(map[string]*pendingApproval)
	}
	r.approvals[approvalKey(approval.AgentID, approval.ToolUseID)] = approval
	r.approvalMu.Unlock()
}

func (r *Runner) removePendingApproval(agentID, toolUseID string) {
	r.approvalMu.Lock()
	delete(r.approvals, approvalKey(agentID, toolUseID))
	r.approvalMu.Unlock()
}

func (r *Runner) addSessionGrant(agentID, grantKey string, permissionGeneration, policyGeneration int64) {
	if grantKey == "" || permissionGeneration < 1 || policyGeneration < 1 {
		return
	}
	r.approvalMu.Lock()
	defer r.approvalMu.Unlock()
	r.addSessionGrantLocked(agentID, grantKey, permissionGeneration, policyGeneration)
}

func (r *Runner) addSessionGrantLocked(agentID, grantKey string, generations ...int64) {
	permissionGeneration := int64(1)
	policyGeneration := int64(1)
	if len(generations) >= 2 {
		permissionGeneration = generations[0]
		policyGeneration = generations[1]
	}
	if grantKey == "" || permissionGeneration < 1 || policyGeneration < 1 {
		return
	}
	if r.sessionGrants == nil {
		r.sessionGrants = make(map[string]map[string]sessionGrant)
	}
	if r.sessionGrants[agentID] == nil {
		r.sessionGrants[agentID] = make(map[string]sessionGrant)
	}
	r.sessionGrants[agentID][grantKey] = sessionGrant{PermissionGeneration: permissionGeneration, PolicyGeneration: policyGeneration}
}

func (r *Runner) approvalGenerationsCurrent(ctx context.Context, agentID string, permissionGeneration, policyGeneration int64) (bool, error) {
	generations, err := r.store.GetPermissionGenerations(ctx, agentID)
	if err != nil {
		return false, err
	}
	return generations.Permission == permissionGeneration && generations.Policy == policyGeneration, nil
}

func (r *Runner) hasSessionGrant(ctx context.Context, agentID, grantKey string) bool {
	if grantKey == "" {
		return false
	}
	r.approvalMu.Lock()
	grant, ok := r.sessionGrants[agentID][grantKey]
	r.approvalMu.Unlock()
	if !ok {
		return false
	}
	current, err := r.approvalGenerationsCurrent(ctx, agentID, grant.PermissionGeneration, grant.PolicyGeneration)
	if err != nil || !current {
		r.approvalMu.Lock()
		delete(r.sessionGrants[agentID], grantKey)
		if len(r.sessionGrants[agentID]) == 0 {
			delete(r.sessionGrants, agentID)
		}
		r.approvalMu.Unlock()
		return false
	}
	return true
}

func (r *Runner) InvalidateAgentApprovals(agentID, reason string) int {
	return r.invalidateApprovals(agentID, reason)
}

func (r *Runner) InvalidatePolicyApprovals(reason string) int {
	return r.invalidateApprovals("", reason)
}

func (r *Runner) invalidateApprovals(agentID, reason string) int {
	if strings.TrimSpace(reason) == "" {
		reason = "tool approval invalidated by permission or policy change"
	}
	r.approvalMu.Lock()
	if agentID == "" {
		r.sessionGrants = make(map[string]map[string]sessionGrant)
	} else {
		delete(r.sessionGrants, agentID)
	}
	// Remembered reflection verdicts were decided under the old policy, so they
	// must not survive a change that invalidates approvals.
	r.clearReflectionCache(agentID)
	approvals := make([]*pendingApproval, 0)
	for _, approval := range r.approvals {
		if agentID == "" || approval.AgentID == agentID {
			approvals = append(approvals, approval)
		}
	}
	r.approvalMu.Unlock()
	for _, approval := range approvals {
		decision := ToolApprovalDecision{Decision: "deny", Reason: reason, DecidedBy: "system", PermissionGeneration: approval.PermissionGeneration, PolicyGeneration: approval.PolicyGeneration, GrantKey: approval.GrantKey}
		select {
		case approval.Decision <- decision:
		default:
		}
		call := tools.Call{ID: approval.ToolUseID, Name: approval.ToolName, Input: approval.Input}
		result := tools.Result{Output: reason, IsError: true}
		resolution := toolPermissionResolution{Decision: toolPermissionDeny, Reason: reason, Source: decisionSourceGenerationInvalidation}
		data := NewToolEventMetaBuilder(call, approval.Risk, "local", approval.RunID).
			Decision(resolution.Decision, resolution.Source, "", "").
			Finished(result, "denied", 0).
			Approval("", reason, time.Time{}, approval.PermissionGeneration, approval.PolicyGeneration).
			ToEventData()
		r.publish(Event{Type: "tool.approval_invalidated", AgentID: approval.AgentID, Data: mergeEventData(data, approval.RunID)})
	}
	canceledQuestions := r.cancelPendingUserQuestions(agentID, reason)
	return len(approvals) + canceledQuestions
}

func approvalKey(agentID, toolUseID string) string {
	return agentID + ":" + toolUseID
}

func (r *Runner) recordImmediateToolResult(ctx context.Context, agentID, runID, messageID string, call tools.Call, risk tools.Risk, result tools.Result, status, reason string) {
	output, _ := json.Marshal(result)
	if _, err := r.store.AddToolCall(ctx, db.ToolCall{AgentID: agentID, RunID: runID, MessageID: messageID, ToolUseID: call.ID, ToolName: call.Name, InputJSON: call.Input, OutputJSON: output, Status: status, ErrorMessage: result.Output, PermissionDecidedBy: "policy", PermissionDecidedAt: db.Now(), PermissionDenyMessage: result.Output, PermissionDecisionReason: reason, PermissionSuggestions: reason}); err != nil {
		slog.Warn("record immediate tool result failed", "agentId", agentID, "toolUseId", call.ID, "error", err)
	}
}

func (r *Runner) updatePendingToolResult(ctx context.Context, agentID, toolUseID string, result tools.Result, status string, durationMS int64) {
	output, _ := json.Marshal(result)
	errMsg := ""
	if result.IsError {
		errMsg = result.Output
	}
	if err := r.store.UpdateToolCallResult(ctx, agentID, toolUseID, output, status, durationMS, errMsg); err != nil {
		slog.Warn("update pending tool result failed", "agentId", agentID, "toolUseId", toolUseID, "error", err)
	}
}
