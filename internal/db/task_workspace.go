package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type TaskWorkspace struct {
	Projects []TaskWorkspaceProject `json:"projects"`
	Summary  TaskWorkspaceSummary   `json:"summary"`
}

type TaskWorkspaceProject struct {
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Status    string                  `json:"status"`
	FlowMode  string                  `json:"flowMode"`
	GitPath   string                  `json:"gitPath,omitempty"`
	UpdatedAt string                  `json:"updatedAt"`
	Worklines []TaskWorkspaceWorkline `json:"worklines"`
	Agents    []TaskWorkspaceAgent    `json:"agents"`
	Counts    SpecTaskStatusCounts    `json:"counts"`
}

type TaskWorkspaceWorkline struct {
	ID           string `json:"id"`
	ProjectID    string `json:"projectId"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Role         string `json:"role"`
	Branch       string `json:"branch,omitempty"`
	WorktreePath string `json:"worktreePath,omitempty"`
	IsRoot       bool   `json:"isRoot"`
	UpdatedAt    string `json:"updatedAt"`
}

type TaskWorkspaceAgent struct {
	ID             string               `json:"id"`
	ProjectID      string               `json:"projectId"`
	WorklineID     string               `json:"worklineId"`
	WorklineTitle  string               `json:"worklineTitle"`
	WorklineBranch string               `json:"worklineBranch,omitempty"`
	ParentAgentID  string               `json:"parentAgentId,omitempty"`
	Type           string               `json:"type"`
	SubagentType   string               `json:"subagentType,omitempty"`
	Title          string               `json:"title"`
	Model          string               `json:"model"`
	PermissionMode string               `json:"permissionMode"`
	Status         string               `json:"status"`
	CWD            string               `json:"cwd,omitempty"`
	MessageCount   int                  `json:"messageCount"`
	UpdatedAt      string               `json:"updatedAt"`
	SpecRevision   int64                `json:"specRevision"`
	SpecUpdatedAt  string               `json:"specUpdatedAt,omitempty"`
	Tasks          []TaskWorkspaceTask  `json:"tasks"`
	Counts         SpecTaskStatusCounts `json:"counts"`
}

type TaskWorkspaceTask struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	Text       string `json:"text"`
	Status     string `json:"status"`
	Protected  bool   `json:"protected"`
	Position   int    `json:"position"`
	Revision   int64  `json:"revision"`
	SourceType string `json:"sourceType"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

type SpecTaskStatusCounts struct {
	Todo    int `json:"todo"`
	Doing   int `json:"doing"`
	Blocked int `json:"blocked"`
	Done    int `json:"done"`
	Total   int `json:"total"`
}

type TaskWorkspaceSummary struct {
	SpecTaskStatusCounts
	ProjectCount int `json:"projectCount"`
	AgentCount   int `json:"agentCount"`
}

type SpecTaskAssignmentResult struct {
	Task        SpecTask  `json:"task"`
	SourceBoard SpecBoard `json:"sourceBoard"`
	TargetBoard SpecBoard `json:"targetBoard"`
}

type workspaceWorklineIndex struct {
	project  int
	workline int
}

type workspaceAgentIndex struct {
	project int
	agent   int
}

// ListTaskWorkspace returns an explicit safe projection for task-oriented UIs.
// It intentionally excludes Agent prompts, context summaries, errors, provider
// state, and other runtime internals.
func (s *specStore) ListTaskWorkspace(ctx context.Context) (TaskWorkspace, error) {
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskWorkspace{}, err
	}
	defer tx.Rollback()

	workspace := TaskWorkspace{Projects: make([]TaskWorkspaceProject, 0)}
	projectIndexes := make(map[string]int)
	worklineIndexes := make(map[string]workspaceWorklineIndex)
	agentIndexes := make(map[string]workspaceAgentIndex)

	projectRows, err := tx.QueryContext(ctx, `SELECT id, name, status, flow_mode, COALESCE(git_path,''), updated_at FROM projects WHERE flow_mode != ? ORDER BY updated_at DESC, id ASC`, ProjectFlowModeConversation)
	if err != nil {
		return TaskWorkspace{}, err
	}
	for projectRows.Next() {
		var project TaskWorkspaceProject
		if err := projectRows.Scan(&project.ID, &project.Name, &project.Status, &project.FlowMode, &project.GitPath, &project.UpdatedAt); err != nil {
			projectRows.Close()
			return TaskWorkspace{}, err
		}
		project.Worklines = make([]TaskWorkspaceWorkline, 0)
		project.Agents = make([]TaskWorkspaceAgent, 0)
		projectIndexes[project.ID] = len(workspace.Projects)
		workspace.Projects = append(workspace.Projects, project)
	}
	if err := projectRows.Close(); err != nil {
		return TaskWorkspace{}, err
	}
	if err := projectRows.Err(); err != nil {
		return TaskWorkspace{}, err
	}

	worklineRows, err := tx.QueryContext(ctx, `SELECT id, project_id, title, status, role, COALESCE(branch,''), COALESCE(worktree_path,''), is_root, updated_at FROM worklines ORDER BY project_id ASC, is_root DESC, created_at ASC, id ASC`)
	if err != nil {
		return TaskWorkspace{}, err
	}
	for worklineRows.Next() {
		var workline TaskWorkspaceWorkline
		var isRoot int
		if err := worklineRows.Scan(&workline.ID, &workline.ProjectID, &workline.Title, &workline.Status, &workline.Role, &workline.Branch, &workline.WorktreePath, &isRoot, &workline.UpdatedAt); err != nil {
			worklineRows.Close()
			return TaskWorkspace{}, err
		}
		projectIndex, ok := projectIndexes[workline.ProjectID]
		if !ok {
			continue
		}
		workline.IsRoot = isRoot != 0
		worklineIndex := len(workspace.Projects[projectIndex].Worklines)
		workspace.Projects[projectIndex].Worklines = append(workspace.Projects[projectIndex].Worklines, workline)
		worklineIndexes[workline.ID] = workspaceWorklineIndex{project: projectIndex, workline: worklineIndex}
	}
	if err := worklineRows.Close(); err != nil {
		return TaskWorkspace{}, err
	}
	if err := worklineRows.Err(); err != nil {
		return TaskWorkspace{}, err
	}

	agentRows, err := tx.QueryContext(ctx, `
SELECT a.id, a.workline_id, COALESCE(a.parent_agent_id,''), a.type, COALESCE(a.subagent_type,''),
       a.title, a.model, a.permission_mode, a.status, COALESCE(a.cwd,''), a.message_count, a.updated_at,
       COALESCE(sb.revision,0), COALESCE(sb.updated_at,'')
FROM agents a
LEFT JOIN spec_boards sb ON sb.agent_id = a.id
WHERE a.workline_id IS NOT NULL
ORDER BY a.workline_id ASC, a.type ASC, a.created_at ASC, a.id ASC`)
	if err != nil {
		return TaskWorkspace{}, err
	}
	for agentRows.Next() {
		var agent TaskWorkspaceAgent
		if err := agentRows.Scan(&agent.ID, &agent.WorklineID, &agent.ParentAgentID, &agent.Type, &agent.SubagentType, &agent.Title, &agent.Model, &agent.PermissionMode, &agent.Status, &agent.CWD, &agent.MessageCount, &agent.UpdatedAt, &agent.SpecRevision, &agent.SpecUpdatedAt); err != nil {
			agentRows.Close()
			return TaskWorkspace{}, err
		}
		worklineIndex, ok := worklineIndexes[agent.WorklineID]
		if !ok {
			continue
		}
		workline := workspace.Projects[worklineIndex.project].Worklines[worklineIndex.workline]
		agent.ProjectID = workline.ProjectID
		agent.WorklineTitle = workline.Title
		agent.WorklineBranch = workline.Branch
		agent.Tasks = make([]TaskWorkspaceTask, 0)
		agentIndex := len(workspace.Projects[worklineIndex.project].Agents)
		workspace.Projects[worklineIndex.project].Agents = append(workspace.Projects[worklineIndex.project].Agents, agent)
		agentIndexes[agent.ID] = workspaceAgentIndex{project: worklineIndex.project, agent: agentIndex}
	}
	if err := agentRows.Close(); err != nil {
		return TaskWorkspace{}, err
	}
	if err := agentRows.Err(); err != nil {
		return TaskWorkspace{}, err
	}

	taskRows, err := tx.QueryContext(ctx, `SELECT id, agent_id, text, status, protected, position, revision, source_type, created_at, updated_at FROM spec_tasks ORDER BY agent_id ASC, position ASC, id ASC`)
	if err != nil {
		return TaskWorkspace{}, err
	}
	for taskRows.Next() {
		var task TaskWorkspaceTask
		var protected int
		if err := taskRows.Scan(&task.ID, &task.AgentID, &task.Text, &task.Status, &protected, &task.Position, &task.Revision, &task.SourceType, &task.CreatedAt, &task.UpdatedAt); err != nil {
			taskRows.Close()
			return TaskWorkspace{}, err
		}
		agentIndex, ok := agentIndexes[task.AgentID]
		if !ok {
			continue
		}
		task.Protected = protected != 0
		project := &workspace.Projects[agentIndex.project]
		agent := &project.Agents[agentIndex.agent]
		agent.Tasks = append(agent.Tasks, task)
		incrementSpecTaskStatus(&agent.Counts, task.Status)
		incrementSpecTaskStatus(&project.Counts, task.Status)
		incrementSpecTaskStatus(&workspace.Summary.SpecTaskStatusCounts, task.Status)
	}
	if err := taskRows.Close(); err != nil {
		return TaskWorkspace{}, err
	}
	if err := taskRows.Err(); err != nil {
		return TaskWorkspace{}, err
	}
	workspace.Summary.ProjectCount = len(workspace.Projects)
	for _, project := range workspace.Projects {
		workspace.Summary.AgentCount += len(project.Agents)
	}
	if err := tx.Commit(); err != nil {
		return TaskWorkspace{}, err
	}
	return workspace, nil
}

func incrementSpecTaskStatus(counts *SpecTaskStatusCounts, status string) {
	if counts == nil {
		return
	}
	switch status {
	case "todo":
		counts.Todo++
	case "doing":
		counts.Doing++
	case "blocked":
		counts.Blocked++
	case "done":
		counts.Done++
	default:
		return
	}
	counts.Total++
}

// AssignSpecTask moves a task between Agents in the same project and appends it
// to the target board. Both board revisions change in the same transaction.
func (s *specStore) AssignSpecTask(ctx context.Context, sourceAgentID, taskID, targetAgentID string, expectedRevision int64, acknowledgeProtected bool, actor string) (SpecTaskAssignmentResult, error) {
	sourceAgentID = strings.TrimSpace(sourceAgentID)
	taskID = strings.TrimSpace(taskID)
	targetAgentID = strings.TrimSpace(targetAgentID)
	if sourceAgentID == "" || taskID == "" || targetAgentID == "" || expectedRevision < 1 {
		return SpecTaskAssignmentResult{}, errors.New("spec task assignment requires source agent, task, target agent, and expected revision")
	}
	if sourceAgentID == targetAgentID {
		return SpecTaskAssignmentResult{}, fmt.Errorf("%w: target agent must differ from source agent", ErrConflict)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	defer tx.Rollback()

	var sourceProjectID, targetProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT w.project_id FROM agents a JOIN worklines w ON w.id = a.workline_id WHERE a.id = ?`, sourceAgentID).Scan(&sourceProjectID); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT w.project_id FROM agents a JOIN worklines w ON w.id = a.workline_id WHERE a.id = ?`, targetAgentID).Scan(&targetProjectID); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	if sourceProjectID == "" || sourceProjectID != targetProjectID {
		return SpecTaskAssignmentResult{}, fmt.Errorf("%w: target agent must belong to the same project", ErrConflict)
	}

	var task SpecTask
	var protected int
	if err := tx.QueryRowContext(ctx, `SELECT id, agent_id, text, status, protected, position, revision, source_type, COALESCE(source_id,''), created_at, updated_at FROM spec_tasks WHERE agent_id = ? AND id = ?`, sourceAgentID, taskID).Scan(&task.ID, &task.AgentID, &task.Text, &task.Status, &protected, &task.Position, &task.Revision, &task.SourceType, &task.SourceID, &task.CreatedAt, &task.UpdatedAt); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	task.Protected = protected != 0
	if task.Revision != expectedRevision {
		return SpecTaskAssignmentResult{}, fmt.Errorf("%w: spec task changed", ErrConflict)
	}
	if task.Protected && !acknowledgeProtected {
		return SpecTaskAssignmentResult{}, fmt.Errorf("%w: protected task requires acknowledgement", ErrConflict)
	}

	now := Now()
	if err := ensureSpecBoardTx(ctx, tx, sourceAgentID, now); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	if err := ensureSpecBoardTx(ctx, tx, targetAgentID, now); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	var targetCount, targetPosition int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(position),-1)+1 FROM spec_tasks WHERE agent_id = ?`, targetAgentID).Scan(&targetCount, &targetPosition); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	if targetCount >= SpecTaskMaxCount {
		return SpecTaskAssignmentResult{}, errors.New("spec task limit reached")
	}

	result, err := tx.ExecContext(ctx, `UPDATE spec_tasks SET agent_id = ?, position = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND id = ? AND revision = ?`, targetAgentID, targetPosition, now, sourceAgentID, taskID, expectedRevision)
	if err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return SpecTaskAssignmentResult{}, err
	} else if affected != 1 {
		return SpecTaskAssignmentResult{}, fmt.Errorf("%w: spec task changed", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE goal_confirmations SET agent_id = ? WHERE task_id = ?`, targetAgentID, taskID); err != nil {
		return SpecTaskAssignmentResult{}, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id FROM spec_tasks WHERE agent_id = ? ORDER BY position ASC, id ASC`, sourceAgentID)
	if err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	var sourceTaskIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return SpecTaskAssignmentResult{}, err
		}
		sourceTaskIDs = append(sourceTaskIDs, id)
	}
	if err := rows.Close(); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	if err := rows.Err(); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	for position, id := range sourceTaskIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE spec_tasks SET position = ? WHERE agent_id = ? AND id = ?`, position, sourceAgentID, id); err != nil {
			return SpecTaskAssignmentResult{}, err
		}
	}

	for _, agentID := range []string{sourceAgentID, targetAgentID} {
		result, err := tx.ExecContext(ctx, `UPDATE spec_boards SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`, now, agentID)
		if err != nil {
			return SpecTaskAssignmentResult{}, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return SpecTaskAssignmentResult{}, err
		} else if affected != 1 {
			return SpecTaskAssignmentResult{}, fmt.Errorf("%w: spec board changed", ErrConflict)
		}
	}
	if task.Protected {
		if err := insertSpecAuditTx(ctx, tx, sourceAgentID, taskID, "task.assign_protected", actor); err != nil {
			return SpecTaskAssignmentResult{}, err
		}
	}

	sourceBoard, err := readSpecBoardTx(ctx, tx, sourceAgentID)
	if err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	targetBoard, err := readSpecBoardTx(ctx, tx, targetAgentID)
	if err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	var moved SpecTask
	for _, candidate := range targetBoard.Tasks {
		if candidate.ID == taskID {
			moved = candidate
			break
		}
	}
	if moved.ID == "" {
		return SpecTaskAssignmentResult{}, errors.New("assigned spec task not found")
	}
	if err := tx.Commit(); err != nil {
		return SpecTaskAssignmentResult{}, err
	}
	return SpecTaskAssignmentResult{Task: moved, SourceBoard: sourceBoard, TargetBoard: targetBoard}, nil
}
