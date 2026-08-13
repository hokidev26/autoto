package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"autoto/internal/db"
)

const (
	overviewRecentConversationLimit = 8
	overviewActiveTaskLimit         = 8
	overviewActiveRunLimit          = 6
	overviewUpcomingScheduleLimit   = 8
	overviewAgentIDChunkSize        = 400
)

type overviewResponse struct {
	CapturedAt          string                 `json:"capturedAt"`
	Summary             overviewSummary        `json:"summary"`
	RecentConversations []overviewConversation `json:"recentConversations"`
	ActiveTasks         []overviewTask         `json:"activeTasks"`
	ActiveRuns          []overviewRun          `json:"activeRuns"`
	UpcomingSchedules   []overviewSchedule     `json:"upcomingSchedules"`
}

type overviewSummary struct {
	Conversations    int                     `json:"conversations"`
	RunningAgents    int                     `json:"runningAgents"`
	Tasks            overviewTaskSummary     `json:"tasks"`
	ActiveRuns       int                     `json:"activeRuns"`
	PendingApprovals int64                   `json:"pendingApprovals"`
	Schedules        overviewScheduleSummary `json:"schedules"`
}

type overviewTaskSummary struct {
	Total int `json:"total"`
	Todo  int `json:"todo"`
	Doing int `json:"doing"`
	Done  int `json:"done"`
}

type overviewScheduleSummary struct {
	Total   int64 `json:"total"`
	Enabled int64 `json:"enabled"`
	Due     int64 `json:"due"`
	Failed  int64 `json:"failed"`
}

type overviewConversation struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	UpdatedAt   string `json:"updatedAt"`
}

type overviewTask struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	AgentID     string `json:"agentId"`
	AgentTitle  string `json:"agentTitle"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	UpdatedAt   string `json:"updatedAt"`
}

type overviewRun struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	AgentTitle string `json:"agentTitle"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
}

type overviewSchedule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AgentID     string `json:"agentId"`
	AgentTitle  string `json:"agentTitle"`
	NextRunAt   string `json:"nextRunAt"`
	Timezone    string `json:"timezone"`
	LastOutcome string `json:"lastOutcome"`
}

type overviewAgent struct {
	Title  string
	Status string
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "overview unavailable")
		return
	}

	capturedAt := s.now().UTC().Format(time.RFC3339Nano)
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview unavailable")
		return
	}

	var projects []db.Project
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		projects, err = s.store.ListProjectsForUser(r.Context(), user.ID)
	} else {
		projects, err = s.store.ListProjects(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview unavailable")
		return
	}

	// Match ordinary navigation membership before applying the request-specific
	// remote filesystem filter. Legacy conversation-flow containers remain in
	// storage for compatibility but never enter the overview projection.
	visibleProjects := make([]db.Project, 0, len(projects))
	allowedProjects := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		if project.FlowMode == db.ProjectFlowModeConversation {
			continue
		}
		visibleProjects = append(visibleProjects, project)
		allowedProjects[project.ID] = struct{}{}
	}
	conversations, err := s.store.ListNavigationConversations(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview unavailable")
		return
	}
	filteredConversations := make([]db.NavigationConversation, 0, len(conversations))
	for _, conversation := range conversations {
		if _, ok := allowedProjects[conversation.ProjectID]; ok {
			filteredConversations = append(filteredConversations, conversation)
		}
	}
	filteredConversations = s.filterNavigationConversationsForRequest(r, filteredConversations)

	workspace, err := s.store.ListTaskWorkspace(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview unavailable")
		return
	}
	workspace = s.filterTaskWorkspaceForRequest(r, workspace, s.filterProjectsForRequest(r, visibleProjects))

	// Navigation is the canonical visible-agent projection. In particular, it
	// excludes archived agents that ListTaskWorkspace intentionally still loads.
	agents := make(map[string]overviewAgent, len(filteredConversations))
	recent := make([]overviewConversation, 0, len(filteredConversations))
	for _, conversation := range filteredConversations {
		agents[conversation.AgentID] = overviewAgent{Title: conversation.AgentTitle, Status: conversation.AgentStatus}
		recent = append(recent, overviewConversation{
			ID: conversation.AgentID, Title: conversation.AgentTitle, Status: conversation.AgentStatus,
			ProjectID: conversation.ProjectID, ProjectName: conversation.ProjectName, UpdatedAt: conversation.LastActivityAt,
		})
	}
	sort.SliceStable(recent, func(i, j int) bool {
		return overviewTimeAfter(recent[i].UpdatedAt, recent[j].UpdatedAt, recent[i].ID, recent[j].ID)
	})
	if len(recent) > overviewRecentConversationLimit {
		recent = recent[:overviewRecentConversationLimit]
	}

	tasks := make([]overviewTask, 0, overviewActiveTaskLimit)
	var taskSummary overviewTaskSummary
	for _, project := range workspace.Projects {
		for _, workspaceAgent := range project.Agents {
			agent, visible := agents[workspaceAgent.ID]
			if !visible {
				continue
			}
			taskSummary.Total += workspaceAgent.Counts.Total
			taskSummary.Todo += workspaceAgent.Counts.Todo
			taskSummary.Doing += workspaceAgent.Counts.Doing
			taskSummary.Done += workspaceAgent.Counts.Done
			for _, task := range workspaceAgent.Tasks {
				if task.Status != "doing" && task.Status != "todo" {
					continue
				}
				tasks = append(tasks, overviewTask{
					ID: task.ID, Title: task.Text, Status: task.Status, Priority: "normal",
					AgentID: workspaceAgent.ID, AgentTitle: agent.Title, ProjectID: project.ID, ProjectName: project.Name, UpdatedAt: task.UpdatedAt,
				})
			}
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Status != tasks[j].Status {
			return tasks[i].Status == "doing"
		}
		return overviewTimeAfter(tasks[i].UpdatedAt, tasks[j].UpdatedAt, tasks[i].ID, tasks[j].ID)
	})
	if len(tasks) > overviewActiveTaskLimit {
		tasks = tasks[:overviewActiveTaskLimit]
	}

	activeRuns, activeRunCount, err := s.overviewActiveRuns(r.Context(), agents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview unavailable")
		return
	}
	pendingApprovals, err := s.overviewPendingApprovals(r.Context(), agents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview unavailable")
		return
	}
	scheduleSummary, upcomingSchedules, err := s.overviewSchedules(r.Context(), capturedAt, agents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview unavailable")
		return
	}

	writeJSON(w, http.StatusOK, overviewResponse{
		CapturedAt: capturedAt,
		Summary: overviewSummary{
			Conversations: len(filteredConversations), RunningAgents: activeRunCount,
			Tasks: taskSummary, ActiveRuns: activeRunCount, PendingApprovals: pendingApprovals, Schedules: scheduleSummary,
		},
		RecentConversations: recent,
		ActiveTasks:         tasks,
		ActiveRuns:          activeRuns,
		UpcomingSchedules:   upcomingSchedules,
	})
}

func (s *Server) overviewActiveRuns(ctx context.Context, agents map[string]overviewAgent) ([]overviewRun, int, error) {
	items := make([]overviewRun, 0, overviewActiveRunLimit)
	if s == nil || s.store == nil || s.runner == nil {
		return items, 0, nil
	}
	runtimeCount := s.runner.ActiveRunCount()
	if runtimeCount <= 0 {
		return items, 0, nil
	}

	agentIDs := overviewAgentIDs(agents, true)
	if len(agentIDs) == 0 {
		return items, 0, nil
	}

	type candidate struct {
		item   overviewRun
		sortAt string
	}
	candidates := make([]candidate, 0, overviewActiveRunLimit)
	candidateCount := 0
	for _, chunk := range overviewAgentIDChunks(agentIDs) {
		placeholders := overviewSQLPlaceholders(len(chunk))
		args := overviewSQLArgs(chunk)
		var count int
		if err := s.store.ReadDB().QueryRowContext(ctx, `SELECT COUNT(DISTINCT agent_id) FROM runs WHERE status IN ('pending','running','continuation_pending') AND agent_id IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
			return nil, 0, err
		}
		candidateCount += count

		args = append(args, overviewActiveRunLimit)
		rows, err := s.store.ReadDB().QueryContext(ctx, `
WITH ranked AS (
  SELECT id, agent_id, status, COALESCE(started_at,'') AS started_at, created_at,
         COALESCE(NULLIF(started_at,''), created_at) AS sort_at,
         ROW_NUMBER() OVER (
           PARTITION BY agent_id
           ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'continuation_pending' THEN 1 ELSE 2 END,
                    COALESCE(NULLIF(started_at,''), created_at) DESC, id DESC
         ) AS position
  FROM runs
  WHERE status IN ('pending','running','continuation_pending') AND agent_id IN (`+placeholders+`)
)
SELECT id, agent_id, status, started_at, sort_at
FROM ranked
WHERE position = 1
ORDER BY sort_at DESC, id ASC
LIMIT ?`, args...)
		if err != nil {
			return nil, 0, err
		}
		for rows.Next() {
			var item overviewRun
			var sortAt string
			if err := rows.Scan(&item.ID, &item.AgentID, &item.Status, &item.StartedAt, &sortAt); err != nil {
				rows.Close()
				return nil, 0, err
			}
			agent, ok := agents[item.AgentID]
			if !ok {
				continue
			}
			item.AgentTitle = agent.Title
			candidates = append(candidates, candidate{item: item, sortAt: sortAt})
		}
		if err := rows.Close(); err != nil {
			return nil, 0, err
		}
		if err := rows.Err(); err != nil {
			return nil, 0, err
		}
	}

	// The runner is the source of truth for realtime activity. Durable states are
	// used only to project safe metadata and can never inflate the realtime count.
	visibleCount := candidateCount
	if visibleCount > runtimeCount {
		visibleCount = runtimeCount
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return overviewTimeAfter(candidates[i].sortAt, candidates[j].sortAt, candidates[i].item.ID, candidates[j].item.ID)
	})
	if len(candidates) > visibleCount {
		candidates = candidates[:visibleCount]
	}
	if len(candidates) > overviewActiveRunLimit {
		candidates = candidates[:overviewActiveRunLimit]
	}
	for _, candidate := range candidates {
		items = append(items, candidate.item)
	}
	return items, visibleCount, nil
}

func (s *Server) overviewPendingApprovals(ctx context.Context, agents map[string]overviewAgent) (int64, error) {
	agentIDs := overviewAgentIDs(agents, false)
	var total int64
	for _, chunk := range overviewAgentIDChunks(agentIDs) {
		query := `SELECT COUNT(*) FROM agent_tool_calls WHERE status = 'pending_approval' AND agent_id IN (` + overviewSQLPlaceholders(len(chunk)) + `)`
		var count int64
		if err := s.store.ReadDB().QueryRowContext(ctx, query, overviewSQLArgs(chunk)...).Scan(&count); err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (s *Server) overviewSchedules(ctx context.Context, capturedAt string, agents map[string]overviewAgent) (overviewScheduleSummary, []overviewSchedule, error) {
	agentIDs := overviewAgentIDs(agents, false)
	items := make([]overviewSchedule, 0, overviewUpcomingScheduleLimit)
	var summary overviewScheduleSummary
	for _, chunk := range overviewAgentIDChunks(agentIDs) {
		placeholders := overviewSQLPlaceholders(len(chunk))
		args := overviewSQLArgs(chunk)
		statsArgs := []any{capturedAt, capturedAt}
		statsArgs = append(statsArgs, args...)
		var item overviewScheduleSummary
		if err := s.store.ReadDB().QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ? AND (lease_until IS NULL OR lease_until <= ?) THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN last_outcome IN ('failure','error') THEN 1 ELSE 0 END),0)
FROM schedules
WHERE agent_id IN (`+placeholders+`)`, statsArgs...).Scan(&item.Total, &item.Enabled, &item.Due, &item.Failed); err != nil {
			return overviewScheduleSummary{}, nil, err
		}
		summary.Total += item.Total
		summary.Enabled += item.Enabled
		summary.Due += item.Due
		summary.Failed += item.Failed

		listArgs := append(append([]any(nil), args...), capturedAt, overviewUpcomingScheduleLimit)
		rows, err := s.store.ReadDB().QueryContext(ctx, `
SELECT id, name, agent_id, next_run_at, timezone, COALESCE(last_outcome,'')
FROM schedules
WHERE agent_id IN (`+placeholders+`)
  AND enabled = 1
  AND next_run_at IS NOT NULL
  AND next_run_at > ?
ORDER BY next_run_at ASC, id ASC
LIMIT ?`, listArgs...)
		if err != nil {
			return overviewScheduleSummary{}, nil, err
		}
		for rows.Next() {
			var schedule overviewSchedule
			if err := rows.Scan(&schedule.ID, &schedule.Name, &schedule.AgentID, &schedule.NextRunAt, &schedule.Timezone, &schedule.LastOutcome); err != nil {
				rows.Close()
				return overviewScheduleSummary{}, nil, err
			}
			agent, ok := agents[schedule.AgentID]
			if !ok {
				continue
			}
			schedule.AgentTitle = agent.Title
			items = append(items, schedule)
		}
		if err := rows.Close(); err != nil {
			return overviewScheduleSummary{}, nil, err
		}
		if err := rows.Err(); err != nil {
			return overviewScheduleSummary{}, nil, err
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return overviewTimeBefore(items[i].NextRunAt, items[j].NextRunAt, items[i].ID, items[j].ID)
	})
	if len(items) > overviewUpcomingScheduleLimit {
		items = items[:overviewUpcomingScheduleLimit]
	}
	return summary, items, nil
}

func overviewAgentIDs(agents map[string]overviewAgent, runningOnly bool) []string {
	ids := make([]string, 0, len(agents))
	for id, agent := range agents {
		if runningOnly && agent.Status != "running" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func overviewAgentIDChunks(ids []string) [][]string {
	if len(ids) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(ids)+overviewAgentIDChunkSize-1)/overviewAgentIDChunkSize)
	for start := 0; start < len(ids); start += overviewAgentIDChunkSize {
		end := start + overviewAgentIDChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

func overviewSQLPlaceholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	placeholders := make([]string, count)
	for index := range placeholders {
		placeholders[index] = "?"
	}
	return strings.Join(placeholders, ",")
}

func overviewSQLArgs(ids []string) []any {
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	return args
}

func overviewTimeAfter(left, right, leftID, rightID string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(left))
	rightTime, rightErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(right))
	if leftErr == nil && rightErr == nil && !leftTime.Equal(rightTime) {
		return leftTime.After(rightTime)
	}
	if left != right {
		return left > right
	}
	return leftID < rightID
}

func overviewTimeBefore(left, right, leftID, rightID string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(left))
	rightTime, rightErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(right))
	if leftErr == nil && rightErr == nil && !leftTime.Equal(rightTime) {
		return leftTime.Before(rightTime)
	}
	if left != right {
		return left < right
	}
	return leftID < rightID
}
