package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func scanProject(scan func(...any) error) (Project, error) {
	var project Project
	var pinned int
	err := scan(
		&project.ID,
		&project.Name,
		&project.Description,
		&project.Status,
		&project.FlowMode,
		&project.GitPath,
		&project.RemoteURL,
		&project.DefaultBranch,
		&pinned,
		&project.ArchivedAt,
		&project.CreatedAt,
		&project.UpdatedAt,
	)
	project.Pinned = pinned != 0
	return project, err
}

func (s *projectStore) ListProjects(ctx context.Context) ([]Project, error) {
	return s.ListProjectsWithOptions(ctx, false)
}

func (s *projectStore) ListProjectsWithOptions(ctx context.Context, includeArchived bool) ([]Project, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT id, name, COALESCE(description,''), status, flow_mode,
       COALESCE(git_path,''), COALESCE(remote_url,''), COALESCE(default_branch,''),
       COALESCE(pinned, 0), COALESCE(archived_at, ''), created_at, updated_at
FROM projects
WHERE (? = 1 OR archived_at IS NULL)
ORDER BY CASE WHEN archived_at IS NULL THEN 0 ELSE 1 END ASC,
         pinned DESC, updated_at DESC, id ASC`, boolInt(includeArchived))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		project, err := scanProject(rows.Scan)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s *projectStore) ListProjectsForUser(ctx context.Context, userID string) ([]Project, error) {
	return s.ListProjectsForUserWithOptions(ctx, userID, false)
}

func (s *projectStore) ListProjectsForUserWithOptions(ctx context.Context, userID string, includeArchived bool) ([]Project, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT p.id, p.name, COALESCE(p.description,''), p.status, p.flow_mode,
       COALESCE(p.git_path,''), COALESCE(p.remote_url,''), COALESCE(p.default_branch,''),
       COALESCE(p.pinned, 0), COALESCE(p.archived_at, ''), p.created_at, p.updated_at
FROM projects p
JOIN project_members pm ON pm.project_id = p.id
WHERE pm.user_id = ? AND (? = 1 OR p.archived_at IS NULL)
ORDER BY CASE WHEN p.archived_at IS NULL THEN 0 ELSE 1 END ASC,
         p.pinned DESC, p.updated_at DESC, p.id ASC`, strings.TrimSpace(userID), boolInt(includeArchived))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		project, err := scanProject(rows.Scan)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s *projectStore) ListNavigationConversations(ctx context.Context) ([]NavigationConversation, error) {
	return s.ListNavigationConversationsWithOptions(ctx, false)
}

func (s *projectStore) ListNavigationConversationsWithOptions(ctx context.Context, includeArchived bool) ([]NavigationConversation, error) {
	rows, err := s.reader().QueryContext(ctx, `
SELECT
  p.id,
  p.name,
  p.flow_mode,
  COALESCE(p.git_path, ''),
  p.updated_at,
  COALESCE(p.pinned, 0),
  COALESCE(p.archived_at, ''),
  w.id,
  w.title,
  w.role,
  COALESCE(w.parent_workline_id, ''),
  COALESCE(w.branch, ''),
  w.updated_at,
  a.id,
  a.title,
  a.type,
  a.status,
  COALESCE(a.pinned, 0),
  COALESCE(a.archived_at, ''),
  a.model,
  a.permission_mode,
  COALESCE(a.cwd, ''),
  a.message_count,
  COALESCE(NULLIF(a.last_message_at, ''), a.updated_at) AS last_activity_at
FROM projects p
JOIN worklines w ON w.project_id = p.id
JOIN agents a ON a.workline_id = w.id
WHERE p.status = 'active'
  AND (? = 1 OR (p.archived_at IS NULL AND a.archived_at IS NULL))
ORDER BY CASE WHEN p.archived_at IS NULL THEN 0 ELSE 1 END ASC,
         p.pinned DESC,
         CASE WHEN a.archived_at IS NULL THEN 0 ELSE 1 END ASC,
         a.pinned DESC,
         last_activity_at DESC, p.id ASC, w.id ASC, a.id ASC`, boolInt(includeArchived))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	conversations := make([]NavigationConversation, 0)
	for rows.Next() {
		var conversation NavigationConversation
		var projectFlowMode string
		var projectPinned, agentPinned int
		if err := rows.Scan(
			&conversation.ProjectID,
			&conversation.ProjectName,
			&projectFlowMode,
			&conversation.ProjectPath,
			&conversation.ProjectUpdatedAt,
			&projectPinned,
			&conversation.ProjectArchivedAt,
			&conversation.WorklineID,
			&conversation.WorklineTitle,
			&conversation.WorklineRole,
			&conversation.WorklineParentID,
			&conversation.WorklineBranch,
			&conversation.WorklineUpdatedAt,
			&conversation.AgentID,
			&conversation.AgentTitle,
			&conversation.AgentType,
			&conversation.AgentStatus,
			&agentPinned,
			&conversation.AgentArchivedAt,
			&conversation.Model,
			&conversation.PermissionMode,
			&conversation.CWD,
			&conversation.MessageCount,
			&conversation.LastActivityAt,
		); err != nil {
			return nil, err
		}
		if projectFlowMode == ProjectFlowModeConversation {
			conversation.Context = ProjectFlowModeConversation
		} else {
			conversation.Context = "project"
		}
		conversation.ProjectPinned = projectPinned != 0
		conversation.AgentPinned = agentPinned != 0
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

func (s *projectStore) CreateProject(ctx context.Context, name, description, gitPath string, defaultModel, permissionMode string) (Project, Workline, Agent, error) {
	return s.createProject(ctx, "", name, description, gitPath, defaultModel, permissionMode)
}

// CreateProjectConversation adds another project-scoped root conversation to
// an existing filesystem project without creating a duplicate project row.
func (s *projectStore) CreateProjectConversation(ctx context.Context, projectID, title, model, permissionMode string) (Project, Workline, Agent, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Project{}, Workline{}, Agent{}, errors.New("project id is required")
	}
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if project.Status != "active" || project.FlowMode == ProjectFlowModeConversation {
		return Project{}, Workline{}, Agent{}, errors.New("project is not available for a project conversation")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return Project{}, Workline{}, Agent{}, errors.New("model is required")
	}
	permissionMode = strings.TrimSpace(permissionMode)
	if permissionMode == "" {
		permissionMode = "acceptEdits"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	defer tx.Rollback()
	var rootCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM worklines WHERE project_id = ? AND is_root = 1`, projectID).Scan(&rootCount); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = project.Name
	}
	// A repeated directory choice must remain distinguishable in navigation.
	// Preserve an explicit title when it is unique, then add a stable numeric
	// suffix rather than relying on client-only state or random labels.
	candidate := title
	var titleCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM worklines WHERE project_id = ? AND is_root = 1 AND title = ? COLLATE NOCASE`, projectID, candidate).Scan(&titleCount); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	var agentTitleCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents a JOIN worklines w ON w.id = a.workline_id WHERE w.project_id = ? AND w.is_root = 1 AND a.title = ? COLLATE NOCASE`, projectID, candidate).Scan(&agentTitleCount); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	titleCount += agentTitleCount
	if titleCount > 0 {
		for suffix := rootCount + 1; ; suffix++ {
			candidate = fmt.Sprintf("%s (%d)", title, suffix)
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM worklines WHERE project_id = ? AND is_root = 1 AND title = ? COLLATE NOCASE`, projectID, candidate).Scan(&titleCount); err != nil {
				return Project{}, Workline{}, Agent{}, err
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents a JOIN worklines w ON w.id = a.workline_id WHERE w.project_id = ? AND w.is_root = 1 AND a.title = ? COLLATE NOCASE`, projectID, candidate).Scan(&agentTitleCount); err != nil {
				return Project{}, Workline{}, Agent{}, err
			}
			if titleCount+agentTitleCount == 0 {
				break
			}
		}
	}
	title = candidate
	now := Now()
	workline := Workline{ID: NewID(), ProjectID: projectID, Title: title, Status: "active", Role: "root", WorktreePath: project.GitPath, IsRoot: true, CreatedAt: now, UpdatedAt: now}
	agent := Agent{ID: NewID(), WorklineID: workline.ID, Type: "primary", Title: title, Model: model, PermissionMode: permissionMode, ExecutionDeviceID: "local", Status: "idle", PlanReflection: true, CWD: project.GitPath, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO worklines (id, project_id, title, status, role, worktree_path, is_root, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, workline.ID, workline.ProjectID, workline.Title, workline.Status, workline.Role, workline.WorktreePath, boolInt(workline.IsRoot), workline.CreatedAt, workline.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, workline_id, type, title, model, permission_mode, reasoning_effort, execution_device_id, status, cwd, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?,''), ?, ?, ?, ?, ?)`, agent.ID, agent.WorklineID, agent.Type, agent.Title, agent.Model, agent.PermissionMode, agent.ReasoningEffort, agent.ExecutionDeviceID, agent.Status, agent.CWD, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, now, projectID); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	project.UpdatedAt = now
	return project, workline, agent, nil
}

// CreateWorklineConversation adds another primary conversation on an existing
// workline. Git branches stay where they are; this only opens a second chat in
// the same working directory.
func (s *projectStore) CreateWorklineConversation(ctx context.Context, worklineID, title, model, permissionMode string) (Project, Workline, Agent, error) {
	worklineID = strings.TrimSpace(worklineID)
	if worklineID == "" {
		return Project{}, Workline{}, Agent{}, errors.New("workline id is required")
	}
	workline, err := s.GetWorkline(ctx, worklineID)
	if err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if workline.Status != "active" {
		return Project{}, Workline{}, Agent{}, errors.New("workline is not available for a conversation")
	}
	project, err := s.GetProject(ctx, workline.ProjectID)
	if err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if project.Status != "active" || project.FlowMode == ProjectFlowModeConversation {
		return Project{}, Workline{}, Agent{}, errors.New("project is not available for a workline conversation")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return Project{}, Workline{}, Agent{}, errors.New("model is required")
	}
	permissionMode = strings.TrimSpace(permissionMode)
	if permissionMode == "" {
		permissionMode = "acceptEdits"
	}
	cwd := strings.TrimSpace(workline.WorktreePath)
	if cwd == "" {
		if workline.IsRoot {
			cwd = strings.TrimSpace(project.GitPath)
		}
	}
	if cwd == "" {
		return Project{}, Workline{}, Agent{}, errors.New("workline has no working directory")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	defer tx.Rollback()
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.TrimSpace(workline.Title)
	}
	if title == "" {
		title = "New conversation"
	}
	candidate := title
	var titleCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE workline_id = ? AND type = 'primary' AND title = ? COLLATE NOCASE`, worklineID, candidate).Scan(&titleCount); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if titleCount > 0 {
		for suffix := 2; ; suffix++ {
			candidate = fmt.Sprintf("%s (%d)", title, suffix)
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE workline_id = ? AND type = 'primary' AND title = ? COLLATE NOCASE`, worklineID, candidate).Scan(&titleCount); err != nil {
				return Project{}, Workline{}, Agent{}, err
			}
			if titleCount == 0 {
				break
			}
		}
	}
	title = candidate
	now := Now()
	agent := Agent{ID: NewID(), WorklineID: workline.ID, Type: "primary", Title: title, Model: model, PermissionMode: permissionMode, ExecutionDeviceID: "local", Status: "idle", PlanReflection: true, CWD: cwd, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, workline_id, type, title, model, permission_mode, reasoning_effort, execution_device_id, status, cwd, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?,''), ?, ?, ?, ?, ?)`, agent.ID, agent.WorklineID, agent.Type, agent.Title, agent.Model, agent.PermissionMode, agent.ReasoningEffort, agent.ExecutionDeviceID, agent.Status, agent.CWD, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worklines SET updated_at = ? WHERE id = ?`, now, workline.ID); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, now, project.ID); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	workline.UpdatedAt = now
	project.UpdatedAt = now
	return project, workline, agent, nil
}

// CreateProjectForUser atomically creates the project hierarchy and makes the
// creating user its owner.
func (s *projectStore) CreateProjectForUser(ctx context.Context, userID, name, description, gitPath string, defaultModel, permissionMode string) (Project, Workline, Agent, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Project{}, Workline{}, Agent{}, errors.New("user is required")
	}
	return s.createProject(ctx, userID, name, description, gitPath, defaultModel, permissionMode)
}

// CreateStandaloneConversation atomically creates the hidden project container,
// root workline, and read-only primary Agent used by a filesystem-free chat.
func (s *projectStore) CreateStandaloneConversation(ctx context.Context, title, model string) (Project, Workline, Agent, error) {
	return s.createStandaloneConversation(ctx, "", title, model)
}

// CreateStandaloneConversationForUser additionally makes the creating user the
// owner in the same transaction as the conversation hierarchy.
func (s *projectStore) CreateStandaloneConversationForUser(ctx context.Context, userID, title, model string) (Project, Workline, Agent, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Project{}, Workline{}, Agent{}, errors.New("user is required")
	}
	return s.createStandaloneConversation(ctx, userID, title, model)
}

func (s *projectStore) createStandaloneConversation(ctx context.Context, ownerID, title, model string) (Project, Workline, Agent, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New conversation"
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return Project{}, Workline{}, Agent{}, errors.New("model is required")
	}
	now := Now()
	project := Project{ID: NewID(), Name: title, Status: "active", FlowMode: ProjectFlowModeConversation, CreatedAt: now, UpdatedAt: now}
	workline := Workline{ID: NewID(), ProjectID: project.ID, Title: "conversation", Status: "active", Role: "root", IsRoot: true, CreatedAt: now, UpdatedAt: now}
	agent := Agent{ID: NewID(), WorklineID: workline.ID, Type: "primary", Title: title, Model: model, PermissionMode: "readOnly", ExecutionDeviceID: "local", Status: "idle", PlanReflection: true, CreatedAt: now, UpdatedAt: now}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	defer tx.Rollback()
	if ownerID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, ownerID).Scan(&count); err != nil {
			return Project{}, Workline{}, Agent{}, err
		}
		if count != 1 {
			return Project{}, Workline{}, Agent{}, sql.ErrNoRows
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, name, description, status, flow_mode, git_path, created_at, updated_at) VALUES (?, ?, '', ?, ?, '', ?, ?)`, project.ID, project.Name, project.Status, project.FlowMode, project.CreatedAt, project.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO worklines (id, project_id, title, status, role, worktree_path, is_root, created_at, updated_at) VALUES (?, ?, ?, ?, ?, '', 1, ?, ?)`, workline.ID, workline.ProjectID, workline.Title, workline.Status, workline.Role, workline.CreatedAt, workline.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, workline_id, type, title, model, permission_mode, execution_device_id, status, cwd, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'readOnly', 'local', 'idle', '', ?, ?)`, agent.ID, agent.WorklineID, agent.Type, agent.Title, agent.Model, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if ownerID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, 'owner', ?)`, project.ID, ownerID, now); err != nil {
			return Project{}, Workline{}, Agent{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	return project, workline, agent, nil
}

func (s *projectStore) createProject(ctx context.Context, ownerID, name, description, gitPath string, defaultModel, permissionMode string) (Project, Workline, Agent, error) {
	if name == "" {
		return Project{}, Workline{}, Agent{}, errors.New("name is required")
	}
	now := Now()
	project := Project{ID: NewID(), Name: name, Description: description, Status: "active", FlowMode: ProjectFlowModeWorkspace, GitPath: gitPath, CreatedAt: now, UpdatedAt: now}
	workline := Workline{ID: NewID(), ProjectID: project.ID, Title: "main", Status: "active", Role: "root", WorktreePath: gitPath, IsRoot: true, CreatedAt: now, UpdatedAt: now}
	agent := Agent{ID: NewID(), WorklineID: workline.ID, Type: "primary", Title: name, Model: defaultModel, PermissionMode: permissionMode, ExecutionDeviceID: "local", Status: "idle", PlanReflection: true, CWD: gitPath, CreatedAt: now, UpdatedAt: now}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	defer tx.Rollback()
	if ownerID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, ownerID).Scan(&count); err != nil {
			return Project{}, Workline{}, Agent{}, err
		}
		if count != 1 {
			return Project{}, Workline{}, Agent{}, sql.ErrNoRows
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, name, description, status, flow_mode, git_path, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, project.ID, project.Name, project.Description, project.Status, project.FlowMode, project.GitPath, project.CreatedAt, project.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO worklines (id, project_id, title, status, role, worktree_path, is_root, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, workline.ID, workline.ProjectID, workline.Title, workline.Status, workline.Role, workline.WorktreePath, boolInt(workline.IsRoot), workline.CreatedAt, workline.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, workline_id, type, title, model, permission_mode, reasoning_effort, execution_device_id, status, cwd, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?,''), ?, ?, ?, ?, ?)`, agent.ID, agent.WorklineID, agent.Type, agent.Title, agent.Model, agent.PermissionMode, agent.ReasoningEffort, agent.ExecutionDeviceID, agent.Status, agent.CWD, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	if ownerID != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, 'owner', ?)`, project.ID, ownerID, now); err != nil {
			return Project{}, Workline{}, Agent{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, Workline{}, Agent{}, err
	}
	return project, workline, agent, nil
}

func (s *projectStore) GetProject(ctx context.Context, id string) (Project, error) {
	return scanProject(func(dest ...any) error {
		return s.reader().QueryRowContext(ctx, `SELECT id, name, COALESCE(description,''), status, flow_mode, COALESCE(git_path,''), COALESCE(remote_url,''), COALESCE(default_branch,''), COALESCE(pinned, 0), COALESCE(archived_at, ''), created_at, updated_at FROM projects WHERE id = ?`, id).Scan(dest...)
	})
}

func updateNavigationState(ctx context.Context, db *sql.DB, table, id string, pinned, archived *bool) error {
	if table != "projects" && table != "agents" {
		return errors.New("unsupported navigation state table")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("navigation state id is required")
	}
	if pinned == nil && archived == nil {
		return errors.New("navigation state patch is empty")
	}
	now := Now()
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if pinned != nil {
		sets = append(sets, "pinned = ?")
		args = append(args, boolInt(*pinned))
	}
	if archived != nil {
		if *archived {
			sets = append(sets, "archived_at = COALESCE(NULLIF(archived_at, ''), ?)")
			args = append(args, now)
		} else {
			sets = append(sets, "archived_at = NULL")
		}
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now, id)
	result, err := db.ExecContext(ctx, "UPDATE "+table+" SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *projectStore) UpdateProjectNavigationState(ctx context.Context, id string, pinned, archived *bool) (Project, error) {
	if err := updateNavigationState(ctx, s.db, "projects", id, pinned, archived); err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, id)
}

func (s *projectStore) UpdateAgentNavigationState(ctx context.Context, id string, pinned, archived *bool) (Agent, error) {
	if err := updateNavigationState(ctx, s.db, "agents", id, pinned, archived); err != nil {
		return Agent{}, err
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) ListWorklinesByProject(ctx context.Context, projectID string) ([]Workline, error) {
	rows, err := s.reader().QueryContext(ctx, `SELECT id, project_id, title, COALESCE(description,''), status, role, COALESCE(branch,''), COALESCE(worktree_path,''), COALESCE(base_branch,''), COALESCE(parent_workline_id,''), COALESCE(fork_point,''), COALESCE(merged_into_workline_id,''), COALESCE(merge_commit_sha,''), COALESCE(merge_strategy,''), COALESCE(pre_merge_target_sha,''), COALESCE(head_commit_sha,''), COALESCE(start_commit_sha,''), is_root, created_at, updated_at FROM worklines WHERE project_id = ? ORDER BY is_root DESC, created_at ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	worklines := make([]Workline, 0)
	for rows.Next() {
		var c Workline
		var isRoot int
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Title, &c.Description, &c.Status, &c.Role, &c.Branch, &c.WorktreePath, &c.BaseBranch, &c.ParentWorklineID, &c.ForkPoint, &c.MergedIntoWorklineID, &c.MergeCommitSHA, &c.MergeStrategy, &c.PreMergeTargetSHA, &c.HeadCommitSHA, &c.StartCommitSHA, &isRoot, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.IsRoot = isRoot != 0
		worklines = append(worklines, c)
	}
	return worklines, rows.Err()
}

func (s *projectStore) GetWorkline(ctx context.Context, id string) (Workline, error) {
	var c Workline
	var isRoot int
	err := s.reader().QueryRowContext(ctx, `SELECT id, project_id, title, COALESCE(description,''), status, role, COALESCE(branch,''), COALESCE(worktree_path,''), COALESCE(base_branch,''), COALESCE(parent_workline_id,''), COALESCE(fork_point,''), COALESCE(merged_into_workline_id,''), COALESCE(merge_commit_sha,''), COALESCE(merge_strategy,''), COALESCE(pre_merge_target_sha,''), COALESCE(head_commit_sha,''), COALESCE(start_commit_sha,''), is_root, created_at, updated_at FROM worklines WHERE id = ?`, id).Scan(&c.ID, &c.ProjectID, &c.Title, &c.Description, &c.Status, &c.Role, &c.Branch, &c.WorktreePath, &c.BaseBranch, &c.ParentWorklineID, &c.ForkPoint, &c.MergedIntoWorklineID, &c.MergeCommitSHA, &c.MergeStrategy, &c.PreMergeTargetSHA, &c.HeadCommitSHA, &c.StartCommitSHA, &isRoot, &c.CreatedAt, &c.UpdatedAt)
	c.IsRoot = isRoot != 0
	return c, err
}

func (s *projectStore) CreateWorklineFork(ctx context.Context, parent Workline, title, branch, worktreePath, baseBranch, forkPoint, model, permissionMode string) (Workline, Agent, error) {
	if parent.ID == "" || parent.ProjectID == "" {
		return Workline{}, Agent{}, errors.New("parent workline is required")
	}
	if title == "" {
		title = branch
	}
	if title == "" {
		return Workline{}, Agent{}, errors.New("workline title is required")
	}
	if branch == "" {
		return Workline{}, Agent{}, errors.New("branch is required")
	}
	if worktreePath == "" {
		return Workline{}, Agent{}, errors.New("worktree path is required")
	}
	now := Now()
	workline := Workline{ID: NewID(), ProjectID: parent.ProjectID, Title: title, Status: "active", Role: "worktree", Branch: branch, WorktreePath: worktreePath, BaseBranch: baseBranch, ParentWorklineID: parent.ID, ForkPoint: forkPoint, HeadCommitSHA: forkPoint, StartCommitSHA: forkPoint, IsRoot: false, CreatedAt: now, UpdatedAt: now}
	agent := Agent{ID: NewID(), WorklineID: workline.ID, Type: "primary", Title: title, Model: model, PermissionMode: permissionMode, ExecutionDeviceID: "local", Status: "idle", PlanReflection: true, CWD: worktreePath, CreatedAt: now, UpdatedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workline{}, Agent{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO worklines (id, project_id, title, status, role, branch, worktree_path, base_branch, parent_workline_id, fork_point, head_commit_sha, start_commit_sha, is_root, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`, workline.ID, workline.ProjectID, workline.Title, workline.Status, workline.Role, workline.Branch, workline.WorktreePath, workline.BaseBranch, workline.ParentWorklineID, workline.ForkPoint, workline.HeadCommitSHA, workline.StartCommitSHA, boolInt(workline.IsRoot), workline.CreatedAt, workline.UpdatedAt); err != nil {
		return Workline{}, Agent{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, workline_id, type, title, model, permission_mode, reasoning_effort, execution_device_id, status, cwd, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?,''), ?, ?, ?, ?, ?)`, agent.ID, agent.WorklineID, agent.Type, agent.Title, agent.Model, agent.PermissionMode, agent.ReasoningEffort, agent.ExecutionDeviceID, agent.Status, agent.CWD, agent.CreatedAt, agent.UpdatedAt); err != nil {
		return Workline{}, Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workline{}, Agent{}, err
	}
	return workline, agent, nil
}

// WorklineHasActiveRuns reports whether any agent in the workline still has a
// durable run in flight. Cleanup paths use it to refuse deleting a worktree an
// agent loop may still be writing into.
func (s *projectStore) WorklineHasActiveRuns(ctx context.Context, worklineID string) (bool, error) {
	var busy int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs r JOIN agents a ON a.id = r.agent_id WHERE a.workline_id = ? AND r.status IN `+activeRunStatusList, strings.TrimSpace(worklineID)).Scan(&busy)
	if err != nil {
		return false, err
	}
	return busy > 0, nil
}

// ClearWorklineWorktree records that the workline's git worktree was removed
// from disk: the path is cleared on the workline and on every agent whose cwd
// pointed into it, so nothing keeps resolving git operations against a
// directory that no longer exists.
func (s *projectStore) ClearWorklineWorktree(ctx context.Context, worklineID string) (Workline, error) {
	worklineID = strings.TrimSpace(worklineID)
	if worklineID == "" {
		return Workline{}, errors.New("workline id is required")
	}
	now := Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workline{}, err
	}
	defer tx.Rollback()
	var worktreePath sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(worktree_path,'') FROM worklines WHERE id = ?`, worklineID).Scan(&worktreePath); err != nil {
		return Workline{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worklines SET worktree_path = '', updated_at = ? WHERE id = ?`, now, worklineID); err != nil {
		return Workline{}, err
	}
	if strings.TrimSpace(worktreePath.String) != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET cwd = '', updated_at = ? WHERE workline_id = ? AND cwd = ?`, now, worklineID, worktreePath.String); err != nil {
			return Workline{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Workline{}, err
	}
	return s.GetWorkline(ctx, worklineID)
}

// DeleteWorklineIfEmpty removes a non-root workline row once its last agent is
// gone. Worklines only cascade with their project, so deleting a fork
// conversation used to leave the workline row behind forever.
func (s *projectStore) DeleteWorklineIfEmpty(ctx context.Context, worklineID string) (bool, error) {
	worklineID = strings.TrimSpace(worklineID)
	if worklineID == "" {
		return false, errors.New("workline id is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM worklines WHERE id = ? AND is_root = 0 AND NOT EXISTS (SELECT 1 FROM agents WHERE workline_id = ?)`, worklineID, worklineID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *projectStore) MarkWorklineMerged(ctx context.Context, sourceWorklineID, targetWorklineID, preMergeTargetSHA, mergeCommitSHA, strategy string) (Workline, error) {
	if sourceWorklineID == "" || targetWorklineID == "" || mergeCommitSHA == "" {
		return Workline{}, errors.New("source workline, target workline, and merge commit are required")
	}
	now := Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workline{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE worklines SET status = 'merged', merged_into_workline_id = ?, merge_commit_sha = ?, merge_strategy = NULLIF(?, ''), pre_merge_target_sha = NULLIF(?, ''), head_commit_sha = ?, updated_at = ? WHERE id = ?`, targetWorklineID, mergeCommitSHA, strategy, preMergeTargetSHA, mergeCommitSHA, now, sourceWorklineID); err != nil {
		return Workline{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worklines SET head_commit_sha = ?, updated_at = ? WHERE id = ?`, mergeCommitSHA, now, targetWorklineID); err != nil {
		return Workline{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workline{}, err
	}
	return s.GetWorkline(ctx, sourceWorklineID)
}

// MarkWorklineUnmerged is the inverse of MarkWorklineMerged: the merge was
// undone on the target branch (via reset or revert), so the source workline
// returns to active and its merge bookkeeping is cleared. Both heads are
// re-recorded because the unmerge moved the target and the source's head no
// longer equals the merge commit.
func (s *projectStore) MarkWorklineUnmerged(ctx context.Context, sourceWorklineID, sourceHeadSHA, targetWorklineID, targetHeadSHA string) (Workline, error) {
	if sourceWorklineID == "" || targetWorklineID == "" {
		return Workline{}, errors.New("source and target worklines are required")
	}
	now := Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workline{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE worklines SET status = 'active', merged_into_workline_id = NULL, merge_commit_sha = NULL, merge_strategy = NULL, pre_merge_target_sha = NULL, head_commit_sha = NULLIF(?, ''), updated_at = ? WHERE id = ?`, sourceHeadSHA, now, sourceWorklineID); err != nil {
		return Workline{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worklines SET head_commit_sha = NULLIF(?, ''), updated_at = ? WHERE id = ?`, targetHeadSHA, now, targetWorklineID); err != nil {
		return Workline{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workline{}, err
	}
	return s.GetWorkline(ctx, sourceWorklineID)
}

const agentSelectSQL = `SELECT id, COALESCE(workline_id,''), COALESCE(parent_agent_id,''), COALESCE(fork_message_id,''), COALESCE(inherit_mode,''), type, COALESCE(subagent_type,''), title, model, COALESCE(summary_model,''), COALESCE(system_prompt,''), permission_mode, COALESCE(entity_generation,1), COALESCE(permission_generation,1), COALESCE(execution_generation,0), COALESCE(reasoning_effort,''), COALESCE(fast_mode,0), COALESCE(execution_device_id,'local'), status, plan_mode, COALESCE(plan_reflection,1), COALESCE(pinned,0), COALESCE(archived_at,''), COALESCE(cwd,''), message_count, COALESCE(context_summary,''), COALESCE(prune_boundary_message_id,''), COALESCE(pruned_percent,0), COALESCE(prune_enabled,0), created_at, updated_at FROM agents`

type agentScanner func(dest ...any) error

func scanAgent(scan agentScanner) (Agent, error) {
	var agent Agent
	var fastMode, planMode, planReflection, pinned, pruneEnabled int
	err := scan(&agent.ID, &agent.WorklineID, &agent.ParentAgentID, &agent.ForkMessageID, &agent.InheritMode, &agent.Type, &agent.SubagentType, &agent.Title, &agent.Model, &agent.SummaryModel, &agent.SystemPrompt, &agent.PermissionMode, &agent.EntityGeneration, &agent.PermissionGeneration, &agent.ExecutionGeneration, &agent.ReasoningEffort, &fastMode, &agent.ExecutionDeviceID, &agent.Status, &planMode, &planReflection, &pinned, &agent.ArchivedAt, &agent.CWD, &agent.MessageCount, &agent.ContextSummary, &agent.PruneBoundaryMessageID, &agent.PrunedPercent, &pruneEnabled, &agent.CreatedAt, &agent.UpdatedAt)
	agent.FastMode = fastMode != 0
	agent.PlanMode = planMode != 0
	agent.PlanReflection = planReflection != 0
	agent.Pinned = pinned != 0
	agent.PruneEnabled = pruneEnabled != 0
	return agent, err
}

func (s *projectStore) GetAgent(ctx context.Context, id string) (Agent, error) {
	return scanAgent(func(dest ...any) error {
		return s.reader().QueryRowContext(ctx, agentSelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *projectStore) GetAgentProjectFlowMode(ctx context.Context, agentID string) (string, error) {
	var flowMode string
	err := s.reader().QueryRowContext(ctx, `SELECT p.flow_mode FROM agents a JOIN worklines w ON w.id = a.workline_id JOIN projects p ON p.id = w.project_id WHERE a.id = ?`, strings.TrimSpace(agentID)).Scan(&flowMode)
	return flowMode, err
}

func (s *projectStore) UpdateAgentTitle(ctx context.Context, id, title string) (Agent, error) {
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	if err := validateP2P3Text("agent id", id, 128, true, false); err != nil {
		return Agent{}, err
	}
	if err := validateP2P3Text("agent title", title, 200, true, false); err != nil || strings.ContainsAny(title, "\r\n") {
		return Agent{}, errors.New("invalid agent title")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET title = ?, entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, title, Now(), id)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

// UpdateAgentTitleCosmeticCAS updates a derived display title without changing
// the execution entity generation. The expected generation prevents an
// asynchronous auto-title from overwriting a concurrent user edit.
func (s *projectStore) UpdateAgentTitleCosmeticCAS(ctx context.Context, id, title string, expectedEntityGeneration int64) (Agent, error) {
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	if err := validateP2P3Text("agent id", id, 128, true, false); err != nil {
		return Agent{}, err
	}
	if err := validateP2P3Text("agent title", title, 200, true, false); err != nil || strings.ContainsAny(title, "\r\n") {
		return Agent{}, errors.New("invalid agent title")
	}
	if expectedEntityGeneration <= 0 {
		return Agent{}, errors.New("expected entity generation must be positive")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET title = ?, updated_at = ? WHERE id = ? AND entity_generation = ?`, title, Now(), id, expectedEntityGeneration)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return Agent{}, sql.ErrNoRows
		} else if err != nil {
			return Agent{}, err
		}
		return Agent{}, fmt.Errorf("%w: agent title changed", ErrConflict)
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentCWD(ctx context.Context, id, cwd string) (Agent, error) {
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET cwd = ?, entity_generation = entity_generation + 1, permission_generation = permission_generation + 1, updated_at = ? WHERE id = ?`, cwd, now, id)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentModel(ctx context.Context, id, model string, reasoningEffort ...string) (Agent, error) {
	id = strings.TrimSpace(id)
	model = strings.TrimSpace(model)
	if err := validateP2P3Text("agent id", id, 128, true, false); err != nil {
		return Agent{}, err
	}
	if err := validateP2P3Text("agent model", model, 256, true, false); err != nil {
		return Agent{}, err
	}
	if len(reasoningEffort) > 1 {
		return Agent{}, errors.New("update agent model accepts at most one reasoning effort")
	}
	now := Now()
	var result sql.Result
	var err error
	if len(reasoningEffort) == 1 {
		effort := strings.TrimSpace(reasoningEffort[0])
		if !validAgentReasoningEffort(effort, true) {
			return Agent{}, errors.New("invalid agent reasoning effort")
		}
		result, err = s.db.ExecContext(ctx, `UPDATE agents SET model = ?, reasoning_effort = NULLIF(?,''), entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, model, effort, now, id)
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE agents SET model = ?, entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, model, now, id)
	}
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentModelRuntime(ctx context.Context, id, model, reasoningEffort string, fastMode bool) (Agent, error) {
	id = strings.TrimSpace(id)
	model = strings.TrimSpace(model)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if err := validateP2P3Text("agent id", id, 128, true, false); err != nil {
		return Agent{}, err
	}
	if err := validateP2P3Text("agent model", model, 256, true, false); err != nil {
		return Agent{}, err
	}
	if !validAgentReasoningEffort(reasoningEffort, true) {
		return Agent{}, errors.New("invalid agent reasoning effort")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET model = ?, reasoning_effort = NULLIF(?,''), fast_mode = ?, entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, model, reasoningEffort, boolInt(fastMode), Now(), id)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentSummaryModel(ctx context.Context, id, summaryModel string) (Agent, error) {
	id = strings.TrimSpace(id)
	summaryModel = strings.TrimSpace(summaryModel)
	if err := validateP2P3Text("agent id", id, 128, true, false); err != nil {
		return Agent{}, err
	}
	if summaryModel != "" {
		if err := validateP2P3Text("agent summary model", summaryModel, 256, true, false); err != nil {
			return Agent{}, err
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET summary_model = NULLIF(?,''), entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, summaryModel, Now(), id)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentReasoningEffort(ctx context.Context, id, reasoningEffort string) (Agent, error) {
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if !validAgentReasoningEffort(reasoningEffort, true) {
		return Agent{}, errors.New("invalid agent reasoning effort")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET reasoning_effort = NULLIF(?,''), entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, reasoningEffort, Now(), strings.TrimSpace(id))
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentFastMode(ctx context.Context, id string, fastMode bool) (Agent, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET fast_mode = ?, entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, boolInt(fastMode), Now(), strings.TrimSpace(id))
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentPlanReflection(ctx context.Context, id string, enabled bool) (Agent, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET plan_reflection = ?, entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`, boolInt(enabled), Now(), strings.TrimSpace(id))
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func validAgentReasoningEffort(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	switch value {
	case "auto", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func (s *projectStore) UpdateAgentContextSummary(ctx context.Context, id, summary, boundaryMessageID string, prunedPercent int) error {
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET context_summary = NULLIF(?, ''), prune_boundary_message_id = NULLIF(?, ''), pruned_percent = ?, updated_at = ? WHERE id = ?`, summary, boundaryMessageID, prunedPercent, now, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *projectStore) UpdateAgentPruneEnabled(ctx context.Context, id string, enabled bool, expectedEntityGeneration ...int64) (Agent, error) {
	id = strings.TrimSpace(id)
	if len(expectedEntityGeneration) > 1 {
		return Agent{}, errors.New("expected entity generation accepts at most one value")
	}
	query := `UPDATE agents SET prune_enabled = ?, entity_generation = entity_generation + 1, updated_at = ? WHERE id = ?`
	args := []any{boolInt(enabled), Now(), id}
	if len(expectedEntityGeneration) == 1 {
		if expectedEntityGeneration[0] <= 0 {
			return Agent{}, errors.New("expected entity generation must be positive")
		}
		query += ` AND entity_generation = ?`
		args = append(args, expectedEntityGeneration[0])
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return Agent{}, sql.ErrNoRows
		} else if err != nil {
			return Agent{}, err
		}
		return Agent{}, fmt.Errorf("%w: agent settings changed", ErrConflict)
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) ClearAgentContext(ctx context.Context, id string, expectedEntityGeneration int64, expectedLatestMessageID string) (Agent, error) {
	id = strings.TrimSpace(id)
	expectedLatestMessageID = strings.TrimSpace(expectedLatestMessageID)
	if id == "" || expectedEntityGeneration <= 0 || expectedLatestMessageID == "" {
		return Agent{}, errors.New("agent id, entity generation, and expected latest message id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	var latest string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_messages WHERE agent_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, id).Scan(&latest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Agent{}, fmt.Errorf("%w: agent has no messages", ErrConflict)
		}
		return Agent{}, err
	}
	if latest != expectedLatestMessageID {
		return Agent{}, fmt.Errorf("%w: latest message changed", ErrConflict)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agents SET context_summary = NULL, prune_boundary_message_id = ?, pruned_percent = 100, entity_generation = entity_generation + 1, updated_at = ? WHERE id = ? AND entity_generation = ?`, latest, Now(), id, expectedEntityGeneration)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, fmt.Errorf("%w: agent settings changed", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, err
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) UpdateAgentPermissionMode(ctx context.Context, id, mode string) (Agent, error) {
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET permission_mode = ?, entity_generation = entity_generation + 1, permission_generation = permission_generation + 1, updated_at = ? WHERE id = ?`, mode, now, id)
	if err != nil {
		return Agent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Agent{}, err
	} else if affected != 1 {
		return Agent{}, sql.ErrNoRows
	}
	return s.GetAgent(ctx, id)
}

func (s *projectStore) ListAgentsByWorkline(ctx context.Context, worklineID string) ([]Agent, error) {
	rows, err := s.reader().QueryContext(ctx, agentSelectSQL+` WHERE workline_id = ? ORDER BY type ASC, created_at ASC`, worklineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := make([]Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows.Scan)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func (s *projectStore) SetAgentStatus(ctx context.Context, agentID, status, errorMessage string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status = ?, error_message = NULLIF(?, ''), updated_at = ? WHERE id = ?`, status, errorMessage, Now(), agentID)
	return err
}
