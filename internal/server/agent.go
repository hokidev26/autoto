package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	agentpkg "autoto/internal/agent"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func terminalAgentRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "error", "failed", "interrupted", "superseded", "denied":
		return true
	default:
		return false
	}
}

type workStateGoal struct {
	Text       string `json:"text"`
	Source     string `json:"source"`
	Status     string `json:"status,omitempty"`
	QueueState string `json:"queueState,omitempty"`
}

type workStateTask struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	Protected bool   `json:"protected"`
}

type workStateTaskCounts struct {
	Total   int `json:"total"`
	Todo    int `json:"todo"`
	Doing   int `json:"doing"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
}

type workStateExecutionRole struct {
	Kind             string `json:"kind"`
	Role             string `json:"role"`
	Status           string `json:"status"`
	AgentID          string `json:"agentId,omitempty"`
	Title            string `json:"title,omitempty"`
	WorklineID       string `json:"worklineId,omitempty"`
	WorklineRole     string `json:"worklineRole,omitempty"`
	BackgroundTaskID string `json:"backgroundTaskId,omitempty"`
	BackgroundKind   string `json:"backgroundKind,omitempty"`
	ChildAgentID     string `json:"childAgentId,omitempty"`
}

type workStateVerification struct {
	Status         string                  `json:"status"`
	Summary        string                  `json:"summary,omitempty"`
	PlanID         string                  `json:"planId,omitempty"`
	PlanStatus     string                  `json:"planStatus,omitempty"`
	Tests          []reviewPlanTestSummary `json:"tests"`
	ReviewVerdict  string                  `json:"reviewVerdict,omitempty"`
	ReviewFindings []string                `json:"reviewFindings"`
}

type workStateSnapshot struct {
	SchemaVersion  int                      `json:"schemaVersion"`
	Goal           *workStateGoal           `json:"goal,omitempty"`
	Tasks          []workStateTask          `json:"tasks"`
	TaskCounts     workStateTaskCounts      `json:"taskCounts"`
	ExecutionRoles []workStateExecutionRole `json:"executionRoles"`
	Verification   workStateVerification    `json:"verification"`
}

type agentLiveSnapshotResponse struct {
	Protocol              int                      `json:"protocol"`
	Agent                 db.Agent                 `json:"agent"`
	Messages              []db.Message             `json:"messages"`
	MessageHasMoreBefore  bool                     `json:"messageHasMoreBefore"`
	MessageNextBefore     string                   `json:"messageNextBefore,omitempty"`
	PendingApprovals      []db.ToolCall            `json:"pendingApprovals"`
	PendingUserQuestions  []map[string]any         `json:"pendingUserQuestions,omitempty"`
	ToolActivity          []activityToolCall       `json:"toolActivity,omitempty"`
	LatestRun             *db.Run                  `json:"latestRun,omitempty"`
	Generations           db.PermissionGenerations `json:"generations"`
	ExecutionGeneration   int64                    `json:"executionGeneration"`
	ExecutionsSince       []db.Run                 `json:"executionsSince,omitempty"`
	ExecutionsTruncated   bool                     `json:"executionsTruncated,omitempty"`
	Spec                  *db.SpecBoard            `json:"spec,omitempty"`
	ChildAgents           []db.Agent               `json:"childAgents,omitempty"`
	ActivePlan            *reviewPlanSummary       `json:"activePlan,omitempty"`
	PendingPlanApproval   *reviewPlanSummary       `json:"pendingPlanApproval,omitempty"`
	Review                reviewStateSummary       `json:"review"`
	BackgroundTasks       []tools.BackgroundTask   `json:"backgroundTasks,omitempty"`
	RecentBackgroundTasks []tools.BackgroundTask   `json:"recentBackgroundTasks,omitempty"`
	Continuation          map[string]any           `json:"continuation,omitempty"`
	Context               agentContextStatus       `json:"context"`
	WorkState             *workStateSnapshot       `json:"workState,omitempty"`
	Stream                agentpkg.StreamWatermark `json:"stream"`
}

func publicLiveSnapshotAgent(agent db.Agent) db.Agent {
	agent.SystemPrompt = ""
	agent.ContextSummary = ""
	return agent
}

func publicRunErrorText(value string) string {
	value, _ = truncateActivityString(agentpkg.RedactToolActivityText(value), activityErrorMessageBytes)
	return value
}

func publicRunSummary(summary db.RunSummary) db.RunSummary {
	summary.Run.ErrorMessage = publicRunErrorText(summary.Run.ErrorMessage)
	summary.Run.CheckpointError = publicRunErrorText(summary.Run.CheckpointError)
	for index := range summary.ToolCalls {
		summary.ToolCalls[index].ErrorMessage = publicRunErrorText(summary.ToolCalls[index].ErrorMessage)
	}
	return summary
}

func publicActiveRunSummary(summary db.ActiveRunSummary) db.ActiveRunSummary {
	summary.Run.ErrorMessage = publicRunErrorText(summary.Run.ErrorMessage)
	summary.Run.CheckpointError = publicRunErrorText(summary.Run.CheckpointError)
	for index := range summary.ToolCalls {
		summary.ToolCalls[index].ErrorMessage = publicRunErrorText(summary.ToolCalls[index].ErrorMessage)
	}
	return summary
}

func (s *Server) liveSnapshotChildrenForRequest(r *http.Request, children []db.Agent) ([]db.Agent, error) {
	out := make([]db.Agent, 0, len(children))
	if s == nil || s.store == nil {
		for _, child := range children {
			out = append(out, publicLiveSnapshotAgent(child))
		}
		return out, nil
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		return nil, err
	}
	var userID string
	if hasUsers {
		user, ok, userErr := s.currentUser(r)
		if userErr != nil {
			return nil, userErr
		}
		if !ok {
			return nil, errors.New("current user is unavailable")
		}
		userID = user.ID
	}
	for _, child := range children {
		if hasUsers {
			allowed, accessErr := s.store.CanAccessAgent(r.Context(), userID, child.ID)
			if accessErr != nil {
				return nil, accessErr
			}
			if !allowed {
				continue
			}
		}
		if s.capabilitiesForRequest(r).FilesystemScope == "project" && !s.filesystemPathWithinProjectRoot(child.CWD) {
			continue
		}
		out = append(out, publicLiveSnapshotAgent(child))
	}
	return out, nil
}

func (s *Server) buildWorkState(ctx context.Context, agent db.Agent, spec *db.SpecBoard, children []db.Agent, reviewState agentReviewState, backgroundTasks []tools.BackgroundTask) *workStateSnapshot {
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
		if s != nil && s.store != nil && strings.TrimSpace(item.WorklineID) != "" {
			if workline, err := s.store.GetWorkline(ctx, item.WorklineID); err == nil {
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

func (s *Server) getAgentLiveSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "agent event hub is not initialized")
		return
	}
	if err := rejectUnknownQuery(r, "afterExecutionGeneration"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := chi.URLParam(r, "id")
	watermark := s.hub.Watermark(agentID)
	snapshot, err := s.store.ReadAgentLiveSnapshot(r.Context(), agentID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	var executions []db.Run
	var truncated bool
	if raw := strings.TrimSpace(r.URL.Query().Get("afterExecutionGeneration")); raw != "" {
		after, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || after < 0 {
			writeError(w, http.StatusBadRequest, "invalid afterExecutionGeneration")
			return
		}
		executions, truncated, err = s.store.ListRunsAfterExecutionGeneration(r.Context(), agentID, after, 20)
		if err != nil {
			writeError(w, statusFromError(err), err.Error())
			return
		}
	}
	board, err := s.store.GetSpecBoard(r.Context(), agentID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	spec := &board
	listedChildren, err := s.store.ListChildAgents(r.Context(), agentID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	children, err := s.liveSnapshotChildrenForRequest(r, listedChildren)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	reviewState, err := s.agentReviewState(r.Context(), agentID, snapshot.LatestRun)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	var backgroundTasks []tools.BackgroundTask
	if s.backgroundTasks != nil {
		backgroundTasks, err = s.backgroundTasks.List(r.Context(), tools.BackgroundTaskListOptions{OwnerAgentID: agentID, Limit: 20})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "background task snapshot is unavailable")
			return
		}
	}
	continuation := continuationSnapshot(snapshot.LatestRun)
	var toolActivity []activityToolCall
	if snapshot.LatestRun != nil && !terminalAgentRunStatus(snapshot.LatestRun.Status) {
		calls, listErr := s.store.ListToolCallsByRunWindow(r.Context(), agentID, snapshot.LatestRun.ID, activityMaxLimit, 0)
		if listErr != nil {
			writeError(w, statusFromError(listErr), listErr.Error())
			return
		}
		outputSnapshots := s.hub.ToolOutputSnapshots(agentID)
		toolActivity = make([]activityToolCall, 0, len(calls))
		for _, call := range calls {
			projected := projectActivityToolCall(call)
			if output, ok := outputSnapshots[call.ToolUseID]; ok && call.Status == "running" {
				text, truncated := truncateActivityString(agentpkg.RedactToolActivityText(output.Text), activityOutputTextBytes)
				encoded, _ := json.Marshal(activityToolResult{Output: text})
				projected.OutputJSON = encoded
				projected.OutputTruncated = projected.OutputTruncated || output.Truncated || truncated
			}
			toolActivity = append(toolActivity, projected)
		}
	}
	workState := s.buildWorkState(r.Context(), snapshot.Agent, spec, children, reviewState, backgroundTasks)
	var pendingUserQuestions []map[string]any
	if s.runner != nil {
		pendingUserQuestions = s.runner.ListPendingUserQuestions(agentID)
	}
	writeJSON(w, http.StatusOK, agentLiveSnapshotResponse{
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
		Context:               s.agentContextStatusForRequest(r.Context(), snapshot.Agent),
		WorkState:             workState,
		Stream:                watermark,
	})
}

func recentBackgroundTasks(tasks []tools.BackgroundTask, limit int) []tools.BackgroundTask {
	if len(tasks) == 0 || limit <= 0 {
		return nil
	}
	if len(tasks) < limit {
		limit = len(tasks)
	}
	return append([]tools.BackgroundTask(nil), tasks[:limit]...)
}

func continuationSnapshot(run *db.Run) map[string]any {
	if run == nil {
		return nil
	}
	result := map[string]any{
		"mode":                    run.AutoContinuationMode,
		"status":                  run.Status,
		"count":                   run.ContinuationCount,
		"continuationCount":       run.ContinuationCount,
		"segmentTurns":            run.ContinuationSegmentTurns,
		"turnsUsed":               run.TurnCount,
		"maxTotalTurns":           run.MaxTotalTurns,
		"tokensUsed":              run.ConsumedInputTokens + run.ConsumedOutputTokens,
		"tokenBudget":             run.MaxTotalTokens,
		"maxTotalTokens":          run.MaxTotalTokens,
		"waitingTaskId":           run.WaitingBackgroundTaskID,
		"waitingBackgroundTaskId": run.WaitingBackgroundTaskID,
		"lastStop":                run.LastStopReason,
		"lastStopReason":          run.LastStopReason,
		"reason":                  run.ContinuationReason,
	}
	startedAt, startedErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(run.StartedAt))
	deadline, deadlineErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(run.DeadlineAt))
	if startedErr == nil {
		elapsed := time.Since(startedAt).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		result["elapsedMs"] = elapsed
	}
	if startedErr == nil && deadlineErr == nil {
		budget := deadline.Sub(startedAt).Milliseconds()
		if budget > 0 {
			result["durationBudgetMs"] = budget
			result["maxDurationMs"] = budget
		}
	}
	return result
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	agent, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	agent.SystemPrompt = ""
	agent.ContextSummary = ""
	writeJSON(w, http.StatusOK, agent)
}

type agentContextStatus struct {
	HasSummary             bool                        `json:"hasSummary"`
	PrunedPercent          int                         `json:"prunedPercent"`
	MessageCount           int                         `json:"messageCount"`
	CanCompact             bool                        `json:"canCompact"`
	CanClear               bool                        `json:"canClear"`
	SummaryModelConfigured bool                        `json:"summaryModelConfigured"`
	EstimatedTokens        int                         `json:"estimatedTokens"`
	LimitTokens            int                         `json:"limitTokens"`
	UsagePercent           int                         `json:"usagePercent"`
	WindowClass            agentpkg.ContextWindowClass `json:"windowClass"`
	Thresholds             agentpkg.ContextThresholds  `json:"thresholds"`
	LatestMessageID        string                      `json:"latestMessageId,omitempty"`
	PruneEnabled           bool                        `json:"pruneEnabled"`
	Estimated              bool                        `json:"estimated"`
}

type compactAgentContextRequest struct {
	EntityGeneration        strictInt64 `json:"entityGeneration"`
	ExpectedLatestMessageID string      `json:"expectedLatestMessageId,omitempty"`
}

type agentContextPreferencesRequest struct {
	PruneEnabled     strictBool  `json:"pruneEnabled"`
	EntityGeneration strictInt64 `json:"entityGeneration"`
}

type clearAgentContextRequest struct {
	EntityGeneration        strictInt64  `json:"entityGeneration"`
	ExpectedLatestMessageID strictString `json:"expectedLatestMessageId"`
}

type compactAgentContextResponse struct {
	Context               agentContextStatus `json:"context"`
	Compacted             bool               `json:"compacted"`
	CompactedMessageCount int                `json:"compactedMessageCount"`
}

func (s *Server) agentContextStatusForRequest(ctx context.Context, agent db.Agent) agentContextStatus {
	summaryModelConfigured := s != nil && s.runner != nil && strings.TrimSpace(s.runner.SummaryModel()) != ""
	status := agentpkg.ContextTokenStatus{MessageCount: agent.MessageCount, PruneEnabled: agent.PruneEnabled, HasSummary: strings.TrimSpace(agent.ContextSummary) != ""}
	if s != nil && s.runner != nil {
		if estimated, _, err := s.runner.ContextStatus(ctx, agent.ID); err == nil {
			status = estimated
		}
	}
	return agentContextStatus{
		HasSummary: status.HasSummary, PrunedPercent: agent.PrunedPercent, MessageCount: status.MessageCount,
		CanCompact: status.CanCompact, CanClear: status.CanClear, SummaryModelConfigured: summaryModelConfigured,
		EstimatedTokens: status.EstimatedTokens, LimitTokens: status.LimitTokens, UsagePercent: status.UsagePercent,
		WindowClass: status.WindowClass, Thresholds: status.Thresholds, LatestMessageID: status.LatestMessageID,
		PruneEnabled: status.PruneEnabled, Estimated: status.Estimated,
	}
}

func (s *Server) getAgentContext(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "agent context store is unavailable")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"context": s.agentContextStatusForRequest(r.Context(), agent), "entityGeneration": agent.EntityGeneration})
}

func (s *Server) patchAgentContextPreferences(w http.ResponseWriter, r *http.Request) {
	var req agentContextPreferencesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.PruneEnabled.set {
		writeError(w, http.StatusBadRequest, "pruneEnabled is required")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	current, err := s.store.GetAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	if req.EntityGeneration.set && req.EntityGeneration.value != current.EntityGeneration {
		writeError(w, http.StatusConflict, "agent context preferences changed; refresh and try again")
		return
	}
	updated, err := s.store.UpdateAgentPruneEnabled(r.Context(), agentID, req.PruneEnabled.value, current.EntityGeneration)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	s.publishContextUpdated(r.Context(), agentID, updated)
	writeJSON(w, http.StatusOK, map[string]any{"context": s.agentContextStatusForRequest(r.Context(), updated), "agent": updated})
}

func (s *Server) clearAgentContext(w http.ResponseWriter, r *http.Request) {
	var req clearAgentContextRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.EntityGeneration.set || req.EntityGeneration.value <= 0 || !req.ExpectedLatestMessageID.set || strings.TrimSpace(req.ExpectedLatestMessageID.value) == "" {
		writeError(w, http.StatusBadRequest, "entityGeneration and expectedLatestMessageId are required")
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is unavailable")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	updated, err := s.runner.ClearAgentContext(r.Context(), agentID, req.EntityGeneration.value, req.ExpectedLatestMessageID.value)
	if err != nil {
		if errors.Is(err, agentpkg.ErrAgentBusy) || errors.Is(err, db.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, statusFromError(err), err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"context": s.agentContextStatusForRequest(r.Context(), updated), "cleared": true})
}

func (s *Server) publishContextUpdated(ctx context.Context, agentID string, agent db.Agent) {
	if s == nil || s.hub == nil {
		return
	}
	// The hub event is additive and intentionally carries only state metadata;
	// context summaries never enter the event payload.
	data := map[string]any{"entityGeneration": agent.EntityGeneration, "prunedPercent": agent.PrunedPercent, "pruneEnabled": agent.PruneEnabled, "messageCount": agent.MessageCount}
	if s.runner != nil && s.store != nil {
		if messages, err := s.store.ListMessages(ctx, agentID); err == nil {
			status := s.runner.ContextStatusForEvent(agent, messages)
			for key, value := range status {
				data[key] = value
			}
		}
	}
	s.hub.Publish(agentpkg.Event{Type: "context.updated", AgentID: agentID, Data: data})
}

func (s *Server) compactAgentContext(w http.ResponseWriter, r *http.Request) {
	var req compactAgentContextRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.EntityGeneration.set || req.EntityGeneration.value <= 0 {
		writeError(w, http.StatusBadRequest, "entityGeneration must be a positive integer")
		return
	}
	if s == nil || s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is unavailable")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	result, updated, err := s.runner.CompactAgentContext(r.Context(), agentID, req.EntityGeneration.value, req.ExpectedLatestMessageID)
	if err != nil {
		switch {
		case errors.Is(err, agentpkg.ErrAgentBusy), errors.Is(err, db.ErrConflict):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "agent not found")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, compactAgentContextResponse{
		Context:               s.agentContextStatusForRequest(r.Context(), updated),
		Compacted:             result.Compacted,
		CompactedMessageCount: result.CompactedMessageCount,
	})
}

type updateAgentTitleRequest struct {
	Title            strictString `json:"title"`
	EntityGeneration strictInt64  `json:"entityGeneration"`
}

func (s *Server) updateAgentTitle(w http.ResponseWriter, r *http.Request) {
	var req updateAgentTitleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Title.set {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	title := strings.TrimSpace(req.Title.value)
	if title == "" || len(title) > 200 || !utf8.ValidString(title) || strings.ContainsAny(title, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "invalid agent title")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "agent store is unavailable")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := validateAPIIdentifier("agent id", agentID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	unlock := s.lockAgentMutation(agentID)
	defer unlock()
	current, err := s.store.GetAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	if req.EntityGeneration.set && req.EntityGeneration.value != current.EntityGeneration {
		writeError(w, http.StatusConflict, "agent title changed; refresh and try again")
		return
	}
	if title == current.Title {
		writeJSON(w, http.StatusOK, current)
		return
	}
	agent, err := s.store.UpdateAgentTitle(r.Context(), agentID, title)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

type updateCWDRequest struct {
	CWD string `json:"cwd"`
}

func (s *Server) updateAgentCWD(w http.ResponseWriter, r *http.Request) {
	var req updateCWDRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CWD == "" {
		writeError(w, http.StatusBadRequest, "cwd is required")
		return
	}
	cwd, err := s.resolveCWDForRequest(r, req.CWD)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(cwd)
	if err != nil {
		writeError(w, statusFromFSError(err), err.Error())
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "cwd must be a directory")
		return
	}
	agentID := chi.URLParam(r, "id")
	agent, err := s.store.UpdateAgentCWD(r.Context(), agentID, cwd)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	if s.runner != nil {
		s.runner.InvalidateAgentApprovals(agentID, "tool approval invalidated because the agent workspace changed")
	}
	writeJSON(w, http.StatusOK, agent)
}

type updateModelRequest struct {
	Model string `json:"model"`
}

func (s *Server) updateAgentModel(w http.ResponseWriter, r *http.Request) {
	var req updateModelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "agent store is unavailable")
		return
	}
	if _, _, err := s.resolveExecutableModel(model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	unlock := s.lockAgentMutation(agentID)
	defer unlock()

	current, err := s.store.GetAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	// Persist the model and compatible runtime controls in one SQL UPDATE. A
	// model switch must not leave reasoning or Fast settings attached to a model
	// that cannot honor them.
	effort := s.safeReasoningEffortForCapabilities(r.Context(), current.ReasoningEffort, s.capabilitiesForAgentModel(model))
	fastMode := current.FastMode && s.modelCapabilitiesForAgentModel(model).FastMode
	agent, err := s.store.UpdateAgentModelRuntime(r.Context(), agentID, model, effort, fastMode)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) capabilitiesForAgentModel(model string) providers.Capabilities {
	providerName, _ := providers.SplitModel(model)
	if s == nil || s.providers == nil {
		// Preserve the legacy Gemini reasoning route for lightweight/test servers
		// that do not install a provider registry.
		if providerName == "gemini" {
			return providers.Capabilities{Reasoning: true}
		}
		return providers.Capabilities{}
	}
	provider, _, err := s.providers.Resolve(model)
	if err != nil {
		return providers.Capabilities{}
	}
	return providers.CapabilitiesFor(provider)
}

func (s *Server) modelCapabilitiesForAgentModel(model string) providers.ModelCapabilities {
	if s == nil || s.providers == nil {
		return providers.ModelCapabilities{}
	}
	provider, resolvedModel, err := s.providers.Resolve(model)
	if err != nil {
		return providers.ModelCapabilities{}
	}
	return providers.ModelCapabilitiesFor(provider, resolvedModel)
}

func (s *Server) safeReasoningEffortForCapabilities(ctx context.Context, effort string, capabilities providers.Capabilities) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	effectiveEffort := effort
	if effectiveEffort == "" {
		// An empty override inherits the runtime default. Preserve that inheritance
		// only when the target can support its actual value; otherwise persist an
		// explicit auto so a model switch cannot inherit an unsupported default.
		effectiveEffort = "auto"
		if s != nil && s.store != nil {
			if settings, err := s.store.GetRuntimeSettings(ctx); err == nil {
				effectiveEffort = strings.ToLower(strings.TrimSpace(settings.DefaultReasoningEffort))
			}
		}
	}
	if capabilities.SupportsReasoningEffort(effectiveEffort) {
		return effort
	}
	return "auto"
}

type updatePermissionModeRequest struct {
	PermissionMode string `json:"permissionMode"`
}

func (s *Server) updateAgentPermissionMode(w http.ResponseWriter, r *http.Request) {
	var req updatePermissionModeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	permissionMode, ok, message := s.permissionModeAllowedForRequest(r, req.PermissionMode)
	if !ok {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	agentID := chi.URLParam(r, "id")
	agent, err := s.store.UpdateAgentPermissionMode(r.Context(), agentID, permissionMode)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	if s.runner != nil {
		s.runner.InvalidateAgentApprovals(agentID, "tool approval invalidated because the permission mode changed")
	}
	writeJSON(w, http.StatusOK, agent)
}

func validPermissionMode(mode string) bool {
	switch mode {
	case "readOnly", "acceptEdits", "bypassPermissions", "default", "dontAsk":
		return true
	default:
		return false
	}
}

func (s *Server) interruptAgent(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	interrupted, err := s.runner.Interrupt(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interrupted": interrupted})
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	limit := db.DefaultMessagePageLimit
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > db.MaxMessagePageLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	page, err := s.store.ListMessagesPage(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("before"), limit)
	if err != nil {
		if errors.Is(err, db.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.store.ListRuns(r.Context(), chi.URLParam(r, "id"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getActiveRunSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.store.ActiveRunSummary(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "active run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, publicActiveRunSummary(summary))
}

func (s *Server) getRunSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.store.RunSummary(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "runId"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, publicRunSummary(summary))
}

const (
	activityDefaultLimit       = 40
	activityMaxLimit           = 50
	activityMaxOffset          = 100_000
	activityPageMaxBytes       = 1024 * 1024
	activityPageReserveBytes   = 4 * 1024
	activityInputMaxBytes      = 16 * 1024
	activityInputContentBytes  = 1024
	activityOutputTextBytes    = 12 * 1024
	activityOutputMaxBytes     = 80 * 1024
	activityEditDiffMaxBytes   = 64 * 1024
	activityErrorMessageBytes  = 4 * 1024
	activityIdentifierMaxBytes = 1024
)

type activityToolCall struct {
	AgentID                  string              `json:"agentId"`
	RunID                    string              `json:"runId"`
	MessageID                string              `json:"messageId"`
	ToolUseID                string              `json:"toolUseId"`
	ToolName                 string              `json:"toolName"`
	InputJSON                json.RawMessage     `json:"inputJson"`
	OutputJSON               json.RawMessage     `json:"outputJson"`
	Status                   string              `json:"status"`
	DurationMS               int64               `json:"durationMs"`
	ErrorMessage             string              `json:"errorMessage"`
	ExecutionDeviceID        string              `json:"executionDeviceId"`
	StartedAt                string              `json:"startedAt"`
	CompletedAt              string              `json:"completedAt"`
	CreatedAt                string              `json:"createdAt"`
	EventVersion             int                 `json:"eventVersion"`
	Decision                 string              `json:"decision,omitempty"`
	DecisionSource           string              `json:"decisionSource,omitempty"`
	PermissionDecidedBy      string              `json:"permissionDecidedBy,omitempty"`
	PermissionDecisionReason string              `json:"permissionDecisionReason,omitempty"`
	PermissionGeneration     int64               `json:"permissionGeneration"`
	PolicyGeneration         int64               `json:"policyGeneration"`
	CommandFacts             *tools.CommandFacts `json:"commandFacts,omitempty"`
	InputTruncated           bool                `json:"inputTruncated,omitempty"`
	OutputTruncated          bool                `json:"outputTruncated,omitempty"`
}

type activityToolResult struct {
	Output  string         `json:"output"`
	IsError bool           `json:"isError,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type activityToolCallPage struct {
	ToolCalls  []activityToolCall `json:"toolCalls"`
	HasMore    bool               `json:"hasMore"`
	NextOffset int                `json:"nextOffset,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
}

func (s *Server) listRunToolCalls(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "runId")
	if r.URL.Query().Get("view") != "activity" {
		calls, err := s.store.ListToolCallsByRun(r.Context(), agentID, runID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, calls)
		return
	}

	limit := activityDefaultLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed <= 0 || parsed > activityMaxLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}
		limit = parsed
	}
	offset := 0
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		parsed, parseErr := strconv.Atoi(rawOffset)
		if parseErr != nil || parsed < 0 || parsed > activityMaxOffset {
			writeError(w, http.StatusBadRequest, "offset must be between 0 and 100000")
			return
		}
		offset = parsed
	}
	calls, err := s.store.ListToolCallsByRunWindow(r.Context(), agentID, runID, limit+1, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
	writeJSON(w, http.StatusOK, page)
}

func projectActivityToolCall(call db.ToolCall) activityToolCall {
	input, inputTruncated := agentpkg.ProjectToolActivityInput(call.ToolName, call.InputJSON, activityInputMaxBytes)
	output, outputTruncated := boundedActivityOutput(call.OutputJSON)
	errorMessage, errorTruncated := truncateActivityString(agentpkg.RedactToolActivityText(call.ErrorMessage), activityErrorMessageBytes)
	decision, source := activityPermissionDecision(call)
	reason, reasonTruncated := truncateActivityString(agentpkg.RedactToolActivityText(call.PermissionDecisionReason), activityErrorMessageBytes)
	projected := activityToolCall{
		AgentID:                  boundedActivityIdentifier(call.AgentID),
		RunID:                    boundedActivityIdentifier(call.RunID),
		MessageID:                boundedActivityIdentifier(call.MessageID),
		ToolUseID:                boundedActivityIdentifier(call.ToolUseID),
		ToolName:                 boundedActivityIdentifier(call.ToolName),
		InputJSON:                input,
		OutputJSON:               output,
		Status:                   boundedActivityIdentifier(call.Status),
		DurationMS:               call.DurationMS,
		ErrorMessage:             errorMessage,
		ExecutionDeviceID:        boundedActivityIdentifier(call.ExecutionDeviceID),
		StartedAt:                boundedActivityIdentifier(call.StartedAt),
		CompletedAt:              boundedActivityIdentifier(call.CompletedAt),
		CreatedAt:                boundedActivityIdentifier(call.CreatedAt),
		EventVersion:             1,
		Decision:                 decision,
		DecisionSource:           source,
		PermissionDecidedBy:      boundedActivityIdentifier(call.PermissionDecidedBy),
		PermissionDecisionReason: reason,
		PermissionGeneration:     call.PermissionGeneration,
		PolicyGeneration:         call.PolicyGeneration,
		InputTruncated:           inputTruncated,
		OutputTruncated:          outputTruncated || errorTruncated || reasonTruncated,
	}
	if call.ToolName == "Bash" {
		facts := tools.AnalyzeBashCommand(tools.BashCommand(call.InputJSON))
		projected.CommandFacts = &facts
	}
	return projected
}

// activityPermissionDecision conservatively derives source for persisted
// records created before ToolEventMeta existed. It intentionally never extracts
// a rule ID from free-form historical reason text.
func activityPermissionDecision(call db.ToolCall) (decision, source string) {
	switch call.Status {
	case "pending_approval":
		decision = "ask"
	case "denied":
		decision = "deny"
	default:
		decision = "allow"
	}
	reason := strings.ToLower(call.PermissionDecisionReason + " " + call.PermissionDenyMessage + " " + call.ErrorMessage)
	if strings.TrimSpace(call.PermissionDecidedBy) != "" && call.PermissionDecidedBy != "policy" && call.PermissionDecidedBy != "system" {
		return decision, "human_approval"
	}
	switch {
	case strings.Contains(reason, "timed out") || strings.Contains(reason, "approval canceled"):
		return decision, "system"
	case strings.Contains(reason, "invalidated"):
		return decision, "generation_invalidation"
	case strings.Contains(reason, "plan execution mode"):
		return decision, "plan_mode"
	case strings.Contains(reason, "readonly") || strings.Contains(reason, "read only"):
		return decision, "read_only_cap"
	case strings.Contains(reason, "permission rule matched"):
		return decision, "rule"
	case strings.Contains(reason, "session approval"):
		return decision, "session_approval"
	case strings.Contains(reason, "policy unavailable"):
		return decision, "policy_unavailable"
	case strings.Contains(reason, "workflow preferences unavailable"):
		return decision, "workflow_unavailable"
	case strings.Contains(reason, "danger") || strings.Contains(reason, "删除命令") || strings.Contains(reason, "风险过高"):
		return decision, "hard_danger_block"
	default:
		return decision, "default_policy"
	}
}

func boundedActivityIdentifier(value string) string {
	value, _ = truncateActivityString(value, activityIdentifierMaxBytes)
	return value
}

func boundedActivityInput(raw json.RawMessage) (json.RawMessage, bool) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`), false
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil || input == nil {
		return json.RawMessage(`{}`), true
	}

	projected := make(map[string]any)
	truncated := false
	priority := []string{"command", "file_path", "path", "pattern", "pages", "offset", "limit", "old_string", "new_string", "replace_all", "url", "query", "content"}
	included := make(map[string]struct{}, len(priority))
	for _, key := range priority {
		value, ok := input[key]
		if !ok {
			continue
		}
		included[key] = struct{}{}
		bounded, valueTruncated := boundedActivityInputValue(value, activityInputFieldBytes(key), 0)
		if valueTruncated {
			truncated = true
		}
		if ok, fieldTruncated := addBoundedActivityInputField(projected, key, bounded); ok {
			truncated = truncated || fieldTruncated
		} else {
			truncated = true
		}
	}

	// Keep a small deterministic sample of non-priority fields for custom tools,
	// without letting arbitrary schemas turn this into a raw transport channel.
	otherKeys := make([]string, 0, len(input))
	for key := range input {
		if _, ok := included[key]; !ok {
			otherKeys = append(otherKeys, key)
		}
	}
	sort.Strings(otherKeys)
	for index, key := range otherKeys {
		if index >= 8 {
			truncated = true
			break
		}
		bounded, valueTruncated := boundedActivityInputValue(input[key], 512, 0)
		if valueTruncated {
			truncated = true
		}
		if ok, fieldTruncated := addBoundedActivityInputField(projected, key, bounded); ok {
			truncated = truncated || fieldTruncated
		} else {
			truncated = true
		}
	}
	encoded, err := json.Marshal(projected)
	if err != nil || len(encoded) > activityInputMaxBytes {
		return json.RawMessage(`{}`), true
	}
	return json.RawMessage(encoded), truncated
}

func activityInputFieldBytes(key string) int {
	switch key {
	case "content":
		return activityInputContentBytes
	case "command":
		return 4 * 1024
	case "old_string", "new_string":
		return 3 * 1024
	case "file_path", "path", "pattern", "url", "query":
		return 2 * 1024
	default:
		return 1024
	}
}

func boundedActivityInputValue(value any, stringLimit, depth int) (any, bool) {
	if depth >= 3 {
		return nil, true
	}
	switch typed := value.(type) {
	case string:
		return truncateActivityString(typed, stringLimit)
	case bool, float64, nil:
		return typed, false
	case []any:
		result := make([]any, 0, min(len(typed), 8))
		truncated := len(typed) > 8
		for index, item := range typed {
			if index >= 8 {
				break
			}
			bounded, itemTruncated := boundedActivityInputValue(item, min(stringLimit, 512), depth+1)
			result = append(result, bounded)
			truncated = truncated || itemTruncated
		}
		return result, truncated
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make(map[string]any, min(len(keys), 8))
		truncated := len(keys) > 8
		for index, key := range keys {
			if index >= 8 {
				break
			}
			bounded, itemTruncated := boundedActivityInputValue(typed[key], min(stringLimit, 512), depth+1)
			result[key] = bounded
			truncated = truncated || itemTruncated
		}
		return result, truncated
	default:
		return nil, true
	}
}

func addBoundedActivityInputField(projected map[string]any, key string, value any) (bool, bool) {
	projected[key] = value
	encoded, err := json.Marshal(projected)
	if err == nil && len(encoded) <= activityInputMaxBytes {
		return true, false
	}
	text, ok := value.(string)
	if !ok {
		delete(projected, key)
		return false, true
	}
	best := ""
	low, high := 0, len(text)
	for low <= high {
		middle := low + (high-low)/2
		candidate, _ := truncateActivityString(text, middle)
		projected[key] = candidate
		encoded, err = json.Marshal(projected)
		if err == nil && len(encoded) <= activityInputMaxBytes {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best == "" {
		projected[key] = ""
		encoded, err = json.Marshal(projected)
		if err != nil || len(encoded) > activityInputMaxBytes {
			delete(projected, key)
			return false, true
		}
	}
	projected[key] = best
	return true, true
}

func boundedActivityOutput(raw json.RawMessage) (json.RawMessage, bool) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`), false
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil || source == nil {
		return json.RawMessage(`{}`), true
	}
	result := activityToolResult{}
	truncated := false
	if encodedOutput, ok := source["output"]; ok {
		if err := json.Unmarshal(encodedOutput, &result.Output); err != nil {
			return json.RawMessage(`{}`), true
		}
		result.Output, truncated = truncateActivityString(agentpkg.RedactToolActivityText(result.Output), activityOutputTextBytes)
	}
	if encodedError, ok := source["isError"]; ok {
		if err := json.Unmarshal(encodedError, &result.IsError); err != nil {
			truncated = true
		}
	}
	if encodedMeta, ok := source["meta"]; ok {
		var sourceMeta map[string]json.RawMessage
		if err := json.Unmarshal(encodedMeta, &sourceMeta); err != nil {
			truncated = true
		} else {
			result.Meta, truncated = boundedActivityMeta(sourceMeta, truncated)
		}
	}
	encoded, fitTruncated := marshalBoundedActivityOutput(&result)
	return encoded, truncated || fitTruncated
}

func boundedActivityMeta(source map[string]json.RawMessage, truncated bool) (map[string]any, bool) {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	meta := make(map[string]any)
	for _, key := range keys {
		if !allowedActivityMetaKey(key) {
			truncated = true
			continue
		}
		var value any
		if err := json.Unmarshal(source[key], &value); err != nil {
			truncated = true
			continue
		}
		switch typed := value.(type) {
		case string:
			limit := 512
			if key == "diff" {
				limit = activityEditDiffMaxBytes
			} else if key == "path" || key == "url" || key == "query" {
				limit = 2 * 1024
			}
			bounded, valueTruncated := truncateActivityString(agentpkg.RedactToolActivityText(typed), limit)
			meta[key] = bounded
			truncated = truncated || valueTruncated
			if key == "diff" && valueTruncated {
				meta["diffTruncated"] = true
			}
		case bool, float64, nil:
			meta[key] = typed
		default:
			truncated = true
		}
	}
	if len(meta) == 0 {
		return nil, truncated
	}
	return meta, truncated
}

func allowedActivityMetaKey(key string) bool {
	switch key {
	case "diff", "diffTruncated", "path", "replacements", "truncated", "count", "query", "url", "status", "contentType", "source", "results", "toolName":
		return true
	default:
		return false
	}
}

func marshalBoundedActivityOutput(result *activityToolResult) (json.RawMessage, bool) {
	encoded, _ := json.Marshal(result)
	if len(encoded) <= activityOutputMaxBytes {
		return json.RawMessage(encoded), false
	}
	truncated := false
	if diff, ok := result.Meta["diff"].(string); ok {
		result.Meta["diffTruncated"] = true
		result.Meta["diff"] = activityStringThatFits(diff, func(value string) { result.Meta["diff"] = value }, result)
		truncated = true
		encoded, _ = json.Marshal(result)
	}
	if len(encoded) > activityOutputMaxBytes {
		result.Output = activityStringThatFits(result.Output, func(value string) { result.Output = value }, result)
		truncated = true
		encoded, _ = json.Marshal(result)
	}
	if len(encoded) > activityOutputMaxBytes {
		// Safe metadata is already narrowly bounded; this final fallback keeps a
		// malformed or unexpectedly encoded record from ever causing a huge response.
		result.Meta = nil
		result.Output, _ = truncateActivityString(result.Output, 1024)
		encoded, _ = json.Marshal(result)
		truncated = true
	}
	return json.RawMessage(encoded), truncated
}

func activityStringThatFits(value string, set func(string), result *activityToolResult) string {
	best := ""
	low, high := 0, len(value)
	for low <= high {
		middle := low + (high-low)/2
		candidate, _ := truncateActivityString(value, middle)
		set(candidate)
		encoded, _ := json.Marshal(result)
		if len(encoded) <= activityOutputMaxBytes {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	set(best)
	return best
}

func truncateActivityString(value string, maximum int) (string, bool) {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	if len(value) <= maximum {
		return value, false
	}
	value = value[:maximum]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func (s *Server) listPendingToolCalls(w http.ResponseWriter, r *http.Request) {
	calls, err := s.store.ListPendingToolCalls(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, calls)
}

type postMessageRequest struct {
	Text      string `json:"text"`
	CreatedBy string `json:"createdBy"`
	Mode      string `json:"mode,omitempty"`
	Context   string `json:"context,omitempty"`
}

func messageContextPermissionModeCap(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "project":
		return "", nil
	case "conversation":
		return "readOnly", nil
	default:
		return "", errors.New("context must be conversation or project")
	}
}

func messageContextRunSource(value string) string {
	if strings.TrimSpace(value) == "conversation" {
		return db.RunSourceConversation
	}
	return db.RunSourceManual
}

func (s *Server) messageRunBoundary(ctx context.Context, agentID, clientContext string) (string, string, error) {
	if s == nil || s.store == nil {
		return "", "", errors.New("agent store is unavailable")
	}
	flowMode, err := s.store.GetAgentProjectFlowMode(ctx, agentID)
	if err != nil {
		return "", "", err
	}
	if flowMode == db.ProjectFlowModeConversation {
		return "readOnly", db.RunSourceConversation, nil
	}
	contextCap, err := messageContextPermissionModeCap(clientContext)
	if err != nil {
		return "", "", err
	}
	return contextCap, messageContextRunSource(clientContext), nil
}

func statusFromMessageBoundaryError(err error) int {
	if db.IsNotFound(err) {
		return http.StatusNotFound
	}
	if strings.Contains(err.Error(), "context must be") {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func narrowPermissionModeCaps(values ...string) string {
	result := ""
	for _, value := range values {
		switch strings.TrimSpace(value) {
		case "readOnly":
			return "readOnly"
		case "acceptEdits":
			result = "acceptEdits"
		}
	}
	return result
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		s.postMultipartMessage(w, r)
		return
	}
	var req postMessageRequest
	if err := decodeLimitedJSON(w, r, &req, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAPIText("text", req.Text, 512<<10, true, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAPIText("createdBy", req.CreatedBy, 200, false, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := chi.URLParam(r, "id")
	contextCap, runSource, err := s.messageRunBoundary(r.Context(), agentID, req.Context)
	if err != nil {
		writeError(w, statusFromMessageBoundaryError(err), err.Error())
		return
	}
	if goal, ok := parseGoalCommand(req.Text); ok {
		if runSource == db.RunSourceConversation {
			writeError(w, http.StatusForbidden, "project context is required for goal commands")
			return
		}
		s.createGoalResponse(w, r, agentID, goal)
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	if user, ok, err := s.currentUser(r); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if ok {
		req.CreatedBy = user.ID
	} else {
		req.CreatedBy = ""
	}
	mode := db.RunExecutionModeExecute
	if runSource != db.RunSourceConversation {
		mode, err = s.reviewModeForMessage(r.Context(), agentID, req.Mode)
		if err != nil {
			writeReviewServiceError(w, err)
			return
		}
	}
	permissionModeCap := narrowPermissionModeCaps(contextCap, s.remotePermissionModeCapForRequest(r))
	msg, err := s.submitReviewRunWithSource(r.Context(), agentID, req.Text, req.CreatedBy, mode, permissionModeCap, runSource, nil)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	w.Header().Set("X-Autoto-Run-Mode", mode)
	writeJSON(w, http.StatusAccepted, msg)
}

func (s *Server) postMultipartMessage(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	text, createdBy, attachments, err := parseMultipartAttachments(w, r)
	if err != nil {
		var uploadErr attachmentUploadError
		if errors.As(err, &uploadErr) {
			writeError(w, uploadErr.Status, uploadErr.Message)
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := chi.URLParam(r, "id")
	contextCap, runSource, err := s.messageRunBoundary(r.Context(), agentID, r.FormValue("context"))
	if err != nil {
		writeError(w, statusFromMessageBoundaryError(err), err.Error())
		return
	}
	if user, ok, err := s.currentUser(r); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if ok {
		createdBy = user.ID
	} else {
		createdBy = ""
	}
	mode := db.RunExecutionModeExecute
	if runSource != db.RunSourceConversation {
		mode, err = s.reviewModeForMessage(r.Context(), agentID, r.FormValue("mode"))
		if err != nil {
			writeReviewServiceError(w, err)
			return
		}
	}
	permissionModeCap := narrowPermissionModeCaps(contextCap, s.remotePermissionModeCapForRequest(r))
	msg, err := s.submitReviewRunWithSource(r.Context(), agentID, text, createdBy, mode, permissionModeCap, runSource, attachments)
	if err != nil {
		writeReviewServiceError(w, err)
		return
	}
	w.Header().Set("X-Autoto-Run-Mode", mode)
	writeJSON(w, http.StatusAccepted, msg)
}

func (s *Server) getMessageAttachment(w http.ResponseWriter, r *http.Request) {
	attachment, err := s.store.GetAttachment(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "messageId"), chi.URLParam(r, "attachmentId"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "attachment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	contentType := attachment.MIMEType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	disposition := "attachment"
	if attachment.Kind == "image" && isSafeInlineImage(strings.ToLower(contentType), attachment.Data) {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(attachment.Data)), 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.Filename}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(attachment.Data)
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	items, err := s.runner.ListToolsForAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type executeToolRequest struct {
	ToolUseID string          `json:"toolUseId"`
	ToolName  string          `json:"toolName"`
	Input     json.RawMessage `json:"input"`
}

func (s *Server) executeTool(w http.ResponseWriter, r *http.Request) {
	var req executeToolRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ToolName == "" {
		writeError(w, http.StatusBadRequest, "toolName is required")
		return
	}
	if req.ToolUseID == "" {
		req.ToolUseID = db.NewID()
	}
	if len(req.Input) == 0 {
		req.Input = json.RawMessage(`{}`)
	}
	if err := s.enforceRemotePermissionCap(r, chi.URLParam(r, "id")); err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	result, err := s.runner.ExecuteTool(r.Context(), chi.URLParam(r, "id"), tools.Call{ID: req.ToolUseID, Name: req.ToolName, Input: req.Input})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"toolUseId": req.ToolUseID, "result": result})
}

type approveToolCallRequest struct {
	Decision             string `json:"decision"`
	Reason               string `json:"reason"`
	PermissionGeneration int64  `json:"permissionGeneration"`
	PolicyGeneration     int64  `json:"policyGeneration"`
}

func (s *Server) approveToolCall(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	var req approveToolCallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	accepted, err := s.runner.ApproveToolCall(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "toolUseId"), agentpkg.ToolApprovalDecision{Decision: req.Decision, Reason: req.Reason, DecidedBy: "user", PermissionGeneration: req.PermissionGeneration, PolicyGeneration: req.PolicyGeneration})
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	if !accepted {
		writeError(w, http.StatusNotFound, "pending tool approval not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"toolUseId": chi.URLParam(r, "toolUseId"), "decision": req.Decision, "accepted": true})
}

func (s *Server) answerUserQuestion(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	var req agentpkg.AnswerUserQuestionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	accepted, err := s.runner.AnswerUserQuestion(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "toolUseId"), req)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	if !accepted {
		writeError(w, http.StatusNotFound, "pending user question not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"toolUseId": chi.URLParam(r, "toolUseId"), "accepted": true, "skipped": req.Skipped})
}

func (s *Server) getToolCall(w http.ResponseWriter, r *http.Request) {
	call, err := s.store.GetToolCallByUseID(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "toolUseId"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "tool call not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, call)
}
