package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	agentpkg "autoto/internal/agent"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type agentService struct {
	store           *db.Store
	runner          *agentpkg.Runner
	hub             *agentpkg.Hub
	providers       *providers.Registry
	backgroundTasks tools.BackgroundTaskService
	lockMutation    func(string) func()
	reviewState     func(context.Context, string, *db.Run) (agentReviewState, error)
}

func (s *Server) agents() agentService {
	var store *db.Store
	var runner *agentpkg.Runner
	var hub *agentpkg.Hub
	var registry *providers.Registry
	var backgroundTasks tools.BackgroundTaskService
	var lockMutation func(string) func()
	var reviewState func(context.Context, string, *db.Run) (agentReviewState, error)
	if s != nil {
		store = s.store
		runner = s.runner
		hub = s.hub
		registry = s.providers
		backgroundTasks = s.backgroundTasks
		lockMutation = s.lockAgentMutation
		reviewState = s.agentReviewState
	}
	return agentService{
		store:           store,
		runner:          runner,
		hub:             hub,
		providers:       registry,
		backgroundTasks: backgroundTasks,
		lockMutation:    lockMutation,
		reviewState:     reviewState,
	}
}

func (a agentService) liveSnapshot(ctx context.Context, agentID, afterExecutionGeneration string, filterChildren func([]db.Agent) ([]db.Agent, error)) (agentLiveSnapshotResponse, error) {
	if a.hub == nil {
		return agentLiveSnapshotResponse{}, apiErr(http.StatusServiceUnavailable, "agent event hub is not initialized")
	}
	watermark := a.hub.Watermark(agentID)
	snapshot, err := a.store.ReadAgentLiveSnapshot(ctx, agentID)
	if err != nil {
		return agentLiveSnapshotResponse{}, err
	}
	var executions []db.Run
	var truncated bool
	if raw := strings.TrimSpace(afterExecutionGeneration); raw != "" {
		after, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || after < 0 {
			return agentLiveSnapshotResponse{}, apiErr(http.StatusBadRequest, "invalid afterExecutionGeneration")
		}
		executions, truncated, err = a.store.ListRunsAfterExecutionGeneration(ctx, agentID, after, 20)
		if err != nil {
			return agentLiveSnapshotResponse{}, err
		}
	}
	board, err := a.store.GetSpecBoard(ctx, agentID)
	if err != nil {
		return agentLiveSnapshotResponse{}, err
	}
	spec := &board
	listedChildren, err := a.store.ListChildAgents(ctx, agentID)
	if err != nil {
		return agentLiveSnapshotResponse{}, err
	}
	children, err := filterChildren(listedChildren)
	if err != nil {
		return agentLiveSnapshotResponse{}, err
	}
	reviewState, err := a.reviewState(ctx, agentID, snapshot.LatestRun)
	if err != nil {
		return agentLiveSnapshotResponse{}, err
	}
	var backgroundTasks []tools.BackgroundTask
	if a.backgroundTasks != nil {
		backgroundTasks, err = a.backgroundTasks.List(ctx, tools.BackgroundTaskListOptions{OwnerAgentID: agentID, Limit: 20})
		if err != nil {
			return agentLiveSnapshotResponse{}, apiErr(http.StatusInternalServerError, "background task snapshot is unavailable")
		}
	}
	continuation := continuationSnapshot(snapshot.LatestRun)
	var toolActivity []activityToolCall
	if snapshot.LatestRun != nil && !terminalAgentRunStatus(snapshot.LatestRun.Status) {
		calls, listErr := a.store.ListToolCallsByRunWindow(ctx, agentID, snapshot.LatestRun.ID, activityMaxLimit, 0)
		if listErr != nil {
			return agentLiveSnapshotResponse{}, listErr
		}
		outputSnapshots := a.hub.ToolOutputSnapshots(agentID)
		inputPreviews := a.hub.ToolInputPreviewSnapshots(agentID)
		toolActivity = make([]activityToolCall, 0, len(calls))
		for _, call := range calls {
			projected := projectActivityToolCall(call)
			inFlight := call.Status == "running" || call.Status == "pending_approval"
			if output, ok := outputSnapshots[call.ToolUseID]; ok && call.Status == "running" {
				text, outTruncated := truncateActivityString(agentpkg.RedactToolActivityText(output.Text), activityOutputTextBytes)
				encoded, _ := json.Marshal(activityToolResult{Output: text})
				projected.OutputJSON = encoded
				projected.OutputTruncated = projected.OutputTruncated || output.Truncated || outTruncated
			}
			if preview, ok := inputPreviews[call.ToolUseID]; ok && inFlight {
				text, previewTruncated := truncateActivityString(agentpkg.RedactToolActivityText(preview.Text), activityOutputTextBytes)
				projected.InputPreview = text
				projected.InputPreviewTruncated = preview.Truncated || previewTruncated
			}
			toolActivity = append(toolActivity, projected)
		}
	}
	workState := a.buildWorkState(ctx, snapshot.Agent, spec, children, reviewState, backgroundTasks)
	var pendingUserQuestions []map[string]any
	if a.runner != nil {
		pendingUserQuestions = a.runner.ListPendingUserQuestions(agentID)
	}
	return agentLiveSnapshotResponse{
		Protocol:              agentpkg.ProtocolVersion,
		Agent:                 publicLiveSnapshotAgent(snapshot.Agent),
		Messages:              snapshot.Messages,
		MessageHasMoreBefore:  snapshot.MessageHasMoreBefore,
		MessageNextBefore:     snapshot.MessageNextBefore,
		PendingApprovals:      snapshot.PendingApprovals,
		PendingUserQuestions:  pendingUserQuestions,
		ToolActivity:          toolActivity,
		LatestRun:             snapshot.LatestRun,
		Generations:           snapshot.Generations,
		ExecutionGeneration:   snapshot.Agent.ExecutionGeneration,
		ExecutionsSince:       executions,
		ExecutionsTruncated:   truncated,
		Spec:                  spec,
		ChildAgents:           children,
		ActivePlan:            reviewState.ActivePlan,
		PendingPlanApproval:   reviewState.PendingPlanApproval,
		Review:                reviewState.Review,
		BackgroundTasks:       backgroundTasks,
		RecentBackgroundTasks: recentBackgroundTasks(backgroundTasks, 8),
		Continuation:          continuation,
		Context:               a.contextStatus(ctx, snapshot.Agent),
		WorkState:             workState,
		Stream:                watermark,
	}, nil
}

func (a agentService) buildWorkState(ctx context.Context, agent db.Agent, spec *db.SpecBoard, children []db.Agent, reviewState agentReviewState, backgroundTasks []tools.BackgroundTask) *workStateSnapshot {
	state := &workStateSnapshot{
		SchemaVersion:  1,
		Tasks:          []workStateTask{},
		ExecutionRoles: []workStateExecutionRole{},
		Verification: workStateVerification{
			Status:         "not_configured",
			Tests:          []reviewPlanTestSummary{},
			ReviewFindings: []string{},
		},
	}
	if spec != nil {
		tasksByID := make(map[string]db.SpecTask, len(spec.Tasks))
		for _, task := range spec.Tasks {
			tasksByID[task.ID] = task
			state.Tasks = append(state.Tasks, workStateTask{ID: task.ID, Text: task.Text, Status: task.Status, Protected: task.Protected})
			state.TaskCounts.Total++
			switch task.Status {
			case "todo":
				state.TaskCounts.Todo++
			case "doing":
				state.TaskCounts.Doing++
			case "blocked":
				state.TaskCounts.Blocked++
			case "done":
				state.TaskCounts.Done++
			}
		}
		for _, confirmation := range spec.Confirmations {
			if confirmation.Status != "confirmed" {
				continue
			}
			if task, ok := tasksByID[confirmation.TaskID]; ok {
				state.Goal = &workStateGoal{Text: task.Text, Source: "spec", Status: confirmation.Status, QueueState: confirmation.QueueState}
				break
			}
		}
	}

	plan := reviewState.ActivePlan
	if plan == nil {
		plan = reviewState.PendingPlanApproval
	}
	if plan != nil {
		state.Verification.PlanID = plan.ID
		state.Verification.PlanStatus = plan.Status
		state.Verification.Tests = append(state.Verification.Tests, plan.Tests...)
		state.Verification.ReviewVerdict = plan.ReviewVerdict
		state.Verification.ReviewFindings = append(state.Verification.ReviewFindings, plan.ReviewFindings...)
		switch {
		case plan.Status == db.PlanStatusStale:
			state.Verification.Status = "stale"
		case len(plan.Tests) > 0:
			state.Verification.Status = "declared"
		case strings.TrimSpace(plan.ReviewVerdict) != "":
			state.Verification.Status = "reviewed"
		default:
			state.Verification.Status = "pending"
		}
		if len(plan.ReviewFindings) > 0 {
			state.Verification.Summary = plan.ReviewFindings[0]
		} else {
			state.Verification.Summary = plan.Summary
		}
		if state.Goal == nil && strings.TrimSpace(plan.Goal) != "" {
			state.Goal = &workStateGoal{Text: plan.Goal, Source: "plan", Status: plan.Status}
		}
	}

	appendAgentRole := func(item db.Agent) {
		role := strings.TrimSpace(item.SubagentType)
		if role == "" {
			role = strings.TrimSpace(item.Type)
		}
		projected := workStateExecutionRole{Kind: "agent", Role: role, Status: item.Status, AgentID: item.ID, Title: item.Title, WorklineID: item.WorklineID}
		if a.store != nil && strings.TrimSpace(item.WorklineID) != "" {
			if workline, err := a.store.GetWorkline(ctx, item.WorklineID); err == nil {
				projected.WorklineRole = workline.Role
				if strings.TrimSpace(projected.Role) == "" {
					projected.Role = workline.Role
				}
			}
		}
		state.ExecutionRoles = append(state.ExecutionRoles, projected)
	}
	appendAgentRole(agent)
	for _, child := range children {
		appendAgentRole(child)
	}
	for _, task := range backgroundTasks {
		state.ExecutionRoles = append(state.ExecutionRoles, workStateExecutionRole{
			Kind: "backgroundTask", Role: task.Kind, Status: task.Status, BackgroundTaskID: task.ID,
			BackgroundKind: task.Kind, ChildAgentID: task.ChildAgentID,
		})
	}
	return state
}

func (a agentService) get(ctx context.Context, agentID string) (db.Agent, error) {
	agent, err := a.store.GetAgent(ctx, agentID)
	if err != nil {
		if err == sql.ErrNoRows {
			return db.Agent{}, apiErr(http.StatusNotFound, "agent not found")
		}
		return db.Agent{}, apiErr(http.StatusInternalServerError, err.Error())
	}
	agent.SystemPrompt = ""
	agent.ContextSummary = ""
	return agent, nil
}

func (a agentService) contextStatus(ctx context.Context, agent db.Agent) agentContextStatus {
	summaryModelConfigured := a.runner != nil && strings.TrimSpace(a.runner.SummaryModel()) != ""
	status := agentpkg.ContextTokenStatus{MessageCount: agent.MessageCount, PruneEnabled: agent.PruneEnabled, HasSummary: strings.TrimSpace(agent.ContextSummary) != ""}
	if a.runner != nil {
		if estimated, _, err := a.runner.ContextStatus(ctx, agent.ID); err == nil {
			status = estimated
		}
	}
	return agentContextStatus{
		HasSummary: status.HasSummary, PrunedPercent: agent.PrunedPercent, MessageCount: status.MessageCount,
		CanCompact: status.CanCompact, CanClear: status.CanClear, SummaryModelConfigured: summaryModelConfigured,
		EstimatedTokens: status.EstimatedTokens, LimitTokens: status.LimitTokens, UsagePercent: status.UsagePercent,
		WindowClass: status.WindowClass, Thresholds: status.Thresholds, LatestMessageID: status.LatestMessageID,
		PruneEnabled: status.PruneEnabled, Estimated: status.Estimated,
		EstimateBasis: status.EstimateBasis, LastActualInputTokens: status.LastActualInputTokens,
	}
}

type agentContextView struct {
	Context          agentContextStatus
	EntityGeneration int64
}

func (a agentService) getContext(ctx context.Context, agentID string) (agentContextView, error) {
	if a.store == nil {
		return agentContextView{}, apiErr(http.StatusServiceUnavailable, "agent context store is unavailable")
	}
	agent, err := a.store.GetAgent(ctx, agentID)
	if err != nil {
		return agentContextView{}, err
	}
	status := a.contextStatus(ctx, agent)
	status.SummaryText = strings.TrimSpace(agent.ContextSummary)
	return agentContextView{Context: status, EntityGeneration: agent.EntityGeneration}, nil
}

type agentContextPreferencesResult struct {
	Context agentContextStatus
	Agent   db.Agent
}

func (a agentService) patchContextPreferences(ctx context.Context, agentID string, req agentContextPreferencesRequest) (agentContextPreferencesResult, error) {
	if !req.PruneEnabled.set {
		return agentContextPreferencesResult{}, apiErr(http.StatusBadRequest, "pruneEnabled is required")
	}
	current, err := a.store.GetAgent(ctx, agentID)
	if err != nil {
		return agentContextPreferencesResult{}, err
	}
	if req.EntityGeneration.set && req.EntityGeneration.value != current.EntityGeneration {
		return agentContextPreferencesResult{}, apiErr(http.StatusConflict, "agent context preferences changed; refresh and try again")
	}
	updated, err := a.store.UpdateAgentPruneEnabled(ctx, agentID, req.PruneEnabled.value, current.EntityGeneration)
	if err != nil {
		return agentContextPreferencesResult{}, err
	}
	a.publishContextUpdated(ctx, agentID, updated)
	return agentContextPreferencesResult{Context: a.contextStatus(ctx, updated), Agent: updated}, nil
}

type agentContextClearResult struct {
	Context agentContextStatus
	Cleared bool
}

func (a agentService) clearContext(ctx context.Context, agentID string, req clearAgentContextRequest) (agentContextClearResult, error) {
	if !req.EntityGeneration.set || req.EntityGeneration.value <= 0 || !req.ExpectedLatestMessageID.set || strings.TrimSpace(req.ExpectedLatestMessageID.value) == "" {
		return agentContextClearResult{}, apiErr(http.StatusBadRequest, "entityGeneration and expectedLatestMessageId are required")
	}
	if a.runner == nil {
		return agentContextClearResult{}, apiErr(http.StatusServiceUnavailable, "agent runner is unavailable")
	}
	updated, err := a.runner.ClearAgentContext(ctx, agentID, req.EntityGeneration.value, req.ExpectedLatestMessageID.value)
	if err != nil {
		if errors.Is(err, agentpkg.ErrAgentBusy) || errors.Is(err, db.ErrConflict) {
			return agentContextClearResult{}, apiErr(http.StatusConflict, err.Error())
		}
		return agentContextClearResult{}, err
	}
	return agentContextClearResult{Context: a.contextStatus(ctx, updated), Cleared: true}, nil
}

func (a agentService) compactContext(ctx context.Context, agentID string, req compactAgentContextRequest) (compactAgentContextResponse, error) {
	if !req.EntityGeneration.set || req.EntityGeneration.value <= 0 {
		return compactAgentContextResponse{}, apiErr(http.StatusBadRequest, "entityGeneration must be a positive integer")
	}
	if a.runner == nil {
		return compactAgentContextResponse{}, apiErr(http.StatusServiceUnavailable, "agent runner is unavailable")
	}
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		return compactAgentContextResponse{}, apiErr(http.StatusBadRequest, err.Error())
	}
	var result agentpkg.ContextCompactionResult
	var updated db.Agent
	var err error
	if strings.TrimSpace(req.ThroughMessageID) != "" {
		result, updated, err = a.runner.CompactAgentContextThroughMessage(ctx, agentID, req.EntityGeneration.value, req.ThroughMessageID, req.ExpectedLatestMessageID)
	} else {
		result, updated, err = a.runner.CompactAgentContext(ctx, agentID, req.EntityGeneration.value, req.ExpectedLatestMessageID)
	}
	if err != nil {
		switch {
		case errors.Is(err, agentpkg.ErrAgentBusy), errors.Is(err, db.ErrConflict):
			return compactAgentContextResponse{}, apiErr(http.StatusConflict, err.Error())
		case errors.Is(err, sql.ErrNoRows):
			return compactAgentContextResponse{}, apiErr(http.StatusNotFound, "agent not found")
		default:
			return compactAgentContextResponse{}, apiErr(http.StatusInternalServerError, err.Error())
		}
	}
	return compactAgentContextResponse{
		Context:               a.contextStatus(ctx, updated),
		Compacted:             result.Compacted,
		CompactedMessageCount: result.CompactedMessageCount,
	}, nil
}

func (a agentService) retainContextSummary(ctx context.Context, agentID string, req retainAgentContextRequest) (retainAgentContextResponse, error) {
	if a.store == nil {
		return retainAgentContextResponse{}, apiErr(http.StatusServiceUnavailable, "agent context store is unavailable")
	}
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		return retainAgentContextResponse{}, apiErr(http.StatusBadRequest, err.Error())
	}
	agent, err := a.store.GetAgent(ctx, agentID)
	if err != nil {
		return retainAgentContextResponse{}, err
	}
	summary := strings.TrimSpace(agent.ContextSummary)
	if summary == "" {
		return retainAgentContextResponse{}, apiErr(http.StatusConflict, "agent has no context summary to retain")
	}
	if len(summary) > db.MemoryContentMaxBytes {
		summary = truncateMemoryContentBytes(summary, db.MemoryContentMaxBytes)
	}
	created, err := a.store.CreateMemory(ctx, db.Memory{
		AgentID:  agentID,
		Content:  summary,
		Keywords: req.Keywords,
		Pinned:   req.Pinned,
	})
	if err != nil {
		return retainAgentContextResponse{}, apiErr(statusFromMemoryError(err), err.Error())
	}
	return retainAgentContextResponse{Memory: created, Context: a.contextStatus(ctx, agent)}, nil
}

func (a agentService) publishContextUpdated(ctx context.Context, agentID string, agent db.Agent) {
	if a.hub == nil {
		return
	}
	// The hub event is additive and intentionally carries only state metadata;
	// context summaries never enter the event payload.
	data := map[string]any{"entityGeneration": agent.EntityGeneration, "prunedPercent": agent.PrunedPercent, "pruneEnabled": agent.PruneEnabled, "messageCount": agent.MessageCount}
	if a.runner != nil && a.store != nil {
		if messages, err := a.store.ListMessages(ctx, agentID); err == nil {
			status := a.runner.ContextStatusForEvent(agent, messages)
			for key, value := range status {
				data[key] = value
			}
		}
	}
	a.hub.Publish(agentpkg.Event{Type: "context.updated", AgentID: agentID, Data: data})
}

func (a agentService) updateTitle(ctx context.Context, agentID string, req updateAgentTitleRequest) (db.Agent, error) {
	if !req.Title.set {
		return db.Agent{}, apiErr(http.StatusBadRequest, "title is required")
	}
	title := strings.TrimSpace(req.Title.value)
	if title == "" || len(title) > 200 || !utf8.ValidString(title) || strings.ContainsAny(title, "\x00\r\n") {
		return db.Agent{}, apiErr(http.StatusBadRequest, "invalid agent title")
	}
	if a.store == nil {
		return db.Agent{}, apiErr(http.StatusInternalServerError, "agent store is unavailable")
	}
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		return db.Agent{}, apiErr(http.StatusBadRequest, err.Error())
	}
	if a.lockMutation != nil {
		unlock := a.lockMutation(agentID)
		defer unlock()
	}
	current, err := a.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if req.EntityGeneration.set && req.EntityGeneration.value != current.EntityGeneration {
		return db.Agent{}, apiErr(http.StatusConflict, "agent title changed; refresh and try again")
	}
	if title == current.Title {
		return current, nil
	}
	agent, err := a.store.UpdateAgentTitle(ctx, agentID, title)
	if err != nil {
		return db.Agent{}, err
	}
	return agent, nil
}

func (a agentService) updateCWD(ctx context.Context, agentID, cwd string) (db.Agent, error) {
	if cwd == "" {
		return db.Agent{}, apiErr(http.StatusBadRequest, "cwd is required")
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return db.Agent{}, apiErr(statusFromFSError(err), err.Error())
	}
	if !info.IsDir() {
		return db.Agent{}, apiErr(http.StatusBadRequest, "cwd must be a directory")
	}
	agent, err := a.store.UpdateAgentCWD(ctx, agentID, cwd)
	if err != nil {
		return db.Agent{}, err
	}
	if a.runner != nil {
		a.runner.InvalidateAgentApprovals(agentID, "tool approval invalidated because the agent workspace changed")
	}
	return agent, nil
}

func (a agentService) updateModel(ctx context.Context, agentID, model string) (db.Agent, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return db.Agent{}, apiErr(http.StatusBadRequest, "model is required")
	}
	if a.store == nil {
		return db.Agent{}, apiErr(http.StatusInternalServerError, "agent store is unavailable")
	}
	if a.lockMutation != nil {
		unlock := a.lockMutation(agentID)
		defer unlock()
	}
	current, err := a.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, err
	}
	effort := a.safeReasoningEffort(ctx, current.ReasoningEffort, a.capabilitiesForModel(model))
	fastMode := current.FastMode && a.modelCapabilities(model).FastMode
	agent, err := a.store.UpdateAgentModelRuntime(ctx, agentID, model, effort, fastMode)
	if err != nil {
		return db.Agent{}, err
	}
	return agent, nil
}

func (a agentService) capabilitiesForModel(model string) providers.Capabilities {
	providerName, _ := providers.SplitModel(model)
	if a.providers == nil {
		// Preserve the legacy Gemini reasoning route for lightweight/test servers
		// that do not install a provider registry.
		if providerName == "gemini" {
			return providers.Capabilities{Reasoning: true}
		}
		return providers.Capabilities{}
	}
	provider, resolvedModel, err := a.providers.Resolve(model)
	if err != nil {
		return providers.Capabilities{}
	}
	// Folded with the model's own levels, because Codex serves "max"/"ultra" on
	// some models only: judging by the provider alone would refuse a level the
	// selected model accepts.
	return providers.CapabilitiesForModel(providers.CapabilitiesFor(provider), providers.ModelCapabilitiesFor(provider, resolvedModel))
}

func (a agentService) modelCapabilities(model string) providers.ModelCapabilities {
	if a.providers == nil {
		return providers.ModelCapabilities{}
	}
	provider, resolvedModel, err := a.providers.Resolve(model)
	if err != nil {
		return providers.ModelCapabilities{}
	}
	return providers.ModelCapabilitiesFor(provider, resolvedModel)
}

func (a agentService) safeReasoningEffort(ctx context.Context, effort string, capabilities providers.Capabilities) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	effectiveEffort := effort
	if effectiveEffort == "" {
		// An empty override inherits the runtime default. Preserve that inheritance
		// only when the target can support its actual value; otherwise persist an
		// explicit auto so a model switch cannot inherit an unsupported default.
		effectiveEffort = "auto"
		if a.store != nil {
			if settings, err := a.store.GetRuntimeSettings(ctx); err == nil {
				effectiveEffort = strings.ToLower(strings.TrimSpace(settings.DefaultReasoningEffort))
			}
		}
	}
	if capabilities.SupportsReasoningEffort(effectiveEffort) {
		return effort
	}
	return "auto"
}

func (a agentService) updatePermissionMode(ctx context.Context, agentID, permissionMode string) (db.Agent, error) {
	agent, err := a.store.UpdateAgentPermissionMode(ctx, agentID, permissionMode)
	if err != nil {
		return db.Agent{}, err
	}
	if a.runner != nil {
		a.runner.InvalidateAgentApprovals(agentID, "tool approval invalidated because the permission mode changed")
	}
	return agent, nil
}

func (a agentService) activityToolCalls(ctx context.Context, agentID, runID string, limit, offset int) (activityToolCallPage, error) {
	calls, err := a.store.ListToolCallsByRunWindow(ctx, agentID, runID, limit+1, offset)
	if err != nil {
		return activityToolCallPage{}, apiErr(http.StatusInternalServerError, err.Error())
	}
	hasMore := len(calls) > limit
	if hasMore {
		calls = calls[1:]
	}
	activity := make([]activityToolCall, 0, len(calls))
	pageBytes := 0
	consumed := 0
	pageTruncated := false
	for index := len(calls) - 1; index >= 0; index-- {
		projected := projectActivityToolCall(calls[index])
		encoded, _ := json.Marshal(projected)
		if len(activity) > 0 && pageBytes+len(encoded) > activityPageMaxBytes-activityPageReserveBytes {
			hasMore = true
			pageTruncated = true
			break
		}
		activity = append(activity, projected)
		pageBytes += len(encoded)
		consumed++
	}
	for left, right := 0, len(activity)-1; left < right; left, right = left+1, right-1 {
		activity[left], activity[right] = activity[right], activity[left]
	}
	page := activityToolCallPage{ToolCalls: activity, HasMore: hasMore, Truncated: pageTruncated}
	if hasMore {
		page.NextOffset = offset + consumed
	}
	return page, nil
}
