package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
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
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.filterProjectsForRequest(r, projects))
}

type createProjectRequest struct {
	Name                 string `json:"name"`
	Description          string `json:"description"`
	GitPath              string `json:"gitPath"`
	Model                string `json:"model"`
	ForceNewConversation bool   `json:"forceNewConversation"`
	IdempotencyKey       string `json:"idempotencyKey"`
}

type createProjectConversationRequest struct {
	Title          string `json:"title"`
	Name           string `json:"name"`
	Model          string `json:"model"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type projectConversationResult struct {
	Project  db.Project
	Workline db.Workline
	Agent    db.Agent
}

type navigationStatePatchRequest struct {
	Pinned   *bool `json:"pinned"`
	Archived *bool `json:"archived"`
}

func validNavigationStatePatch(req navigationStatePatchRequest) bool {
	return req.Pinned != nil || req.Archived != nil
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	cfg := s.configSnapshot()
	gitPath := cleanProjectPath(strings.TrimSpace(req.GitPath))
	if gitPath == "" {
		gitPath = filepath.Join(cfg.Paths.DefaultProjectDir, slugify(req.Name))
	}
	resolvedGitPath, err := s.resolveCWDForRequest(r, gitPath)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	gitPath = resolvedGitPath
	if err := os.MkdirAll(gitPath, 0o755); err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = cfg.Agent.DefaultModel
	}
	permissionMode := s.safeDefaultPermissionModeForRequest(r, cfg.Agent.DefaultPermissionMode)
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	userID := ""
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		userID = user.ID
	}

	create := func() (projectConversationResult, error) {
		if req.ForceNewConversation {
			var projects []db.Project
			if hasUsers {
				projects, err = s.store.ListProjectsForUserWithOptions(r.Context(), userID, true)
			} else {
				projects, err = s.store.ListProjectsWithOptions(r.Context(), true)
			}
			if err != nil {
				return projectConversationResult{}, err
			}
			for _, existing := range projects {
				if existing.Status != "active" || existing.ArchivedAt != "" || existing.FlowMode == db.ProjectFlowModeConversation || !sameFilesystemProjectPath(existing.GitPath, gitPath) {
					continue
				}
				project, workline, agent, createErr := s.store.CreateProjectConversation(r.Context(), existing.ID, req.Name, model, permissionMode)
				return projectConversationResult{Project: project, Workline: workline, Agent: agent}, createErr
			}
		}
		var project db.Project
		var workline db.Workline
		var agent db.Agent
		if hasUsers {
			project, workline, agent, err = s.store.CreateProjectForUser(r.Context(), userID, req.Name, req.Description, gitPath, model, permissionMode)
		} else {
			project, workline, agent, err = s.store.CreateProject(r.Context(), req.Name, req.Description, gitPath, model, permissionMode)
		}
		return projectConversationResult{Project: project, Workline: workline, Agent: agent}, err
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(req.IdempotencyKey)
	}
	if len(key) > 200 {
		writeError(w, http.StatusBadRequest, "idempotency key is too long")
		return
	}
	cacheKey := ""
	if req.ForceNewConversation && key != "" {
		cacheKey = "directory" + "\x00" + userID + "\x00" + filesystemProjectPathKey(gitPath) + "\x00" + key
	}

	var result projectConversationResult
	if req.ForceNewConversation {
		s.projectConversationMu.Lock()
		defer s.projectConversationMu.Unlock()
		if s.projectConversationKeys == nil {
			s.projectConversationKeys = make(map[string]projectConversationResult)
		}
		if cacheKey != "" {
			if cached, ok := s.projectConversationKeys[cacheKey]; ok {
				writeJSON(w, http.StatusOK, map[string]any{"project": cached.Project, "workline": cached.Workline, "agent": cached.Agent})
				return
			}
		}
		result, err = create()
	} else {
		result, err = create()
	}
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if cfg.Agent.DefaultStartInPlanMode {
		result.Agent, err = s.updatePersistedAgentPlanMode(r.Context(), result.Agent.ID, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "project was created but its default plan mode could not be applied")
			return
		}
	}
	if req.ForceNewConversation && cacheKey != "" {
		s.projectConversationKeys[cacheKey] = result
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"project":   result.Project,
		"workline":  result.Workline,
		"agent":     result.Agent,
		"workspace": s.projectWorkspaceGitReport(r.Context(), gitPath),
	})
}

// projectWorkspaceGitReport describes the Git state of a newly configured
// project path. Creation deliberately still succeeds when the path is not a
// repository, because init-git exists for exactly that flow, but staying silent
// was the actual defect: a project pointed at a directory that merely *contains*
// a repository looks fine for days, then blocks auto-continuation with an
// internal message about a safety snapshot. Reporting the state here lets the UI
// say so while the user is still looking at the dialog.
//
// discoveredRoot carries the useful half. Git resolves a repository by walking
// upward, never downward, so a parent directory never inherits a child's
// repository; when the chosen path is not a repository but exactly one
// subdirectory is, that subdirectory is almost always what the user meant.
func (s *Server) projectWorkspaceGitReport(ctx context.Context, gitPath string) map[string]any {
	report := map[string]any{"path": gitPath, "isGitRepository": false}
	repository, candidates, err := resolveGitRepository(ctx, gitPath)
	if err != nil {
		// A probe failure must not fail project creation, and the raw Git error
		// is not something the dialog should show.
		report["gitStateUnavailable"] = true
		return report
	}
	if repository.Root != "" && sameFilesystemProjectPath(repository.Root, gitPath) {
		report["isGitRepository"] = true
		report["hasCommits"] = repository.HasHead
		return report
	}
	if paths := gitCandidatePaths(candidates); len(paths) > 0 {
		report["discoveredRoots"] = paths
		if len(paths) == 1 {
			report["discoveredRoot"] = paths[0]
		}
	} else if repository.Root != "" {
		// The path sits inside a repository whose root is somewhere above it.
		report["enclosingRoot"] = repository.Root
	}
	return report
}

func filesystemProjectPathKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = filepath.Clean(value)
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func sameFilesystemProjectPath(left, right string) bool {
	leftKey := filesystemProjectPathKey(left)
	rightKey := filesystemProjectPathKey(right)
	return leftKey != "" && leftKey == rightKey
}

func (s *Server) createProjectConversation(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	var req createProjectConversationRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = strings.TrimSpace(req.Name)
	}
	cfg := s.configSnapshot()
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = cfg.Agent.DefaultModel
	}
	permissionMode := s.safeDefaultPermissionModeForRequest(r, cfg.Agent.DefaultPermissionMode)
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	userID := ""
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		userID = user.ID
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(req.IdempotencyKey)
	}
	if len(key) > 200 {
		writeError(w, http.StatusBadRequest, "idempotency key is too long")
		return
	}
	cacheKey := ""
	if key != "" {
		cacheKey = userID + "\x00" + projectID + "\x00" + key
	}

	create := func() (projectConversationResult, error) {
		project, workline, agent, createErr := s.store.CreateProjectConversation(r.Context(), projectID, req.Title, model, permissionMode)
		if createErr != nil {
			return projectConversationResult{}, createErr
		}
		if cfg.Agent.DefaultStartInPlanMode {
			agent, createErr = s.updatePersistedAgentPlanMode(r.Context(), agent.ID, true)
			if createErr != nil {
				return projectConversationResult{}, createErr
			}
		}
		return projectConversationResult{Project: project, Workline: workline, Agent: agent}, nil
	}

	var result projectConversationResult
	if cacheKey != "" {
		s.projectConversationMu.Lock()
		defer s.projectConversationMu.Unlock()
		if s.projectConversationKeys == nil {
			s.projectConversationKeys = make(map[string]projectConversationResult)
		}
		if cached, ok := s.projectConversationKeys[cacheKey]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"project": cached.Project, "workline": cached.Workline, "agent": cached.Agent})
			return
		}
		result, err = create()
		if err == nil {
			s.projectConversationKeys[cacheKey] = result
		}
	} else {
		result, err = create()
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": result.Project, "workline": result.Workline, "agent": result.Agent})
}

func (s *Server) createWorklineConversation(w http.ResponseWriter, r *http.Request) {
	worklineID := strings.TrimSpace(chi.URLParam(r, "id"))
	var req createProjectConversationRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = strings.TrimSpace(req.Name)
	}
	cfg := s.configSnapshot()
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = s.parentWorklineModel(r.Context(), worklineID)
	}
	if model == "" {
		model = cfg.Agent.DefaultModel
	}
	permissionMode := s.safeDefaultPermissionModeForRequest(r, cfg.Agent.DefaultPermissionMode)
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	userID := ""
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		userID = user.ID
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(req.IdempotencyKey)
	}
	if len(key) > 200 {
		writeError(w, http.StatusBadRequest, "idempotency key is too long")
		return
	}
	cacheKey := ""
	if key != "" {
		cacheKey = userID + "\x00workline\x00" + worklineID + "\x00" + key
	}

	create := func() (projectConversationResult, error) {
		project, workline, agent, createErr := s.store.CreateWorklineConversation(r.Context(), worklineID, req.Title, model, permissionMode)
		if createErr != nil {
			return projectConversationResult{}, createErr
		}
		if cfg.Agent.DefaultStartInPlanMode {
			agent, createErr = s.updatePersistedAgentPlanMode(r.Context(), agent.ID, true)
			if createErr != nil {
				return projectConversationResult{}, createErr
			}
		}
		return projectConversationResult{Project: project, Workline: workline, Agent: agent}, nil
	}

	var result projectConversationResult
	if cacheKey != "" {
		s.projectConversationMu.Lock()
		defer s.projectConversationMu.Unlock()
		if s.projectConversationKeys == nil {
			s.projectConversationKeys = make(map[string]projectConversationResult)
		}
		if cached, ok := s.projectConversationKeys[cacheKey]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"project": cached.Project, "workline": cached.Workline, "agent": cached.Agent})
			return
		}
		result, err = create()
		if err == nil {
			s.projectConversationKeys[cacheKey] = result
		}
	} else {
		result, err = create()
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workline not found")
			return
		}
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": result.Project, "workline": result.Workline, "agent": result.Agent})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	// Keep the old route during the compatibility window, but make the removed
	// product boundary explicit. In particular, do not decode, validate, or
	// touch the store: stale clients must not be able to create a hidden project
	// container by accident.
	writeJSON(w, http.StatusGone, map[string]any{
		"error": "standalone conversations have been removed; create or choose a project instead",
		"code":  "standalone_conversation_removed",
	})
}

func (s *Server) patchProjectNavigationState(w http.ResponseWriter, r *http.Request) {
	var req navigationStatePatchRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if !validNavigationStatePatch(req) {
		writeError(w, http.StatusBadRequest, "navigation state patch must include pinned or archived")
		return
	}
	project, err := s.store.UpdateProjectNavigationState(r.Context(), chi.URLParam(r, "id"), req.Pinned, req.Archived)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) patchAgentNavigationState(w http.ResponseWriter, r *http.Request) {
	var req navigationStatePatchRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if !validNavigationStatePatch(req) {
		writeError(w, http.StatusBadRequest, "navigation state patch must include pinned or archived")
		return
	}
	agent, err := s.store.UpdateAgentNavigationState(r.Context(), chi.URLParam(r, "id"), req.Pinned, req.Archived)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// writeArchiveDeleteError maps the archive-deletion guards onto HTTP codes so
// the UI can tell "you must archive first" apart from "it is still running".
func (s *Server) writeArchiveDeleteError(w http.ResponseWriter, r *http.Request, kind string, err error) {
	switch {
	case db.IsNotFound(err):
		writeError(w, http.StatusNotFound, kind+" not found")
	case db.IsNotArchived(err):
		s.writeRequestError(w, r, http.StatusConflict, err)
	case db.HasActiveRun(err):
		s.writeRequestError(w, r, http.StatusConflict, err)
	default:
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
	}
}

func (s *Server) deleteArchivedProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Captured before the rows disappear: after DeleteArchivedProject succeeds
	// there is no record left of which worktrees and branches this project
	// created, and they would leak on disk forever.
	targets := s.collectProjectWorktreeCleanupTargets(r.Context(), id)
	if err := s.store.DeleteArchivedProject(r.Context(), id); err != nil {
		s.writeArchiveDeleteError(w, r, "project", err)
		return
	}
	for _, target := range targets {
		result := cleanupWorklineGitArtifacts(context.Background(), target.repoRoot, target.worktreePath, target.branch)
		for _, warning := range result.warnings {
			slog.Warn("project deletion git cleanup", "projectId", id, "worklineBranch", target.branch, "warning", warning)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

type worktreeCleanupTarget struct {
	repoRoot     string
	worktreePath string
	branch       string
}

// collectProjectWorktreeCleanupTargets lists the fork worktrees and branches a
// project owns so the deletion handler can remove them after the database rows
// are gone. Lookup failures return nothing: cleanup is best effort and must
// never block the deletion itself.
func (s *Server) collectProjectWorktreeCleanupTargets(ctx context.Context, projectID string) []worktreeCleanupTarget {
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		return nil
	}
	repoRoot, err := s.projectMainRepoRoot(ctx, project)
	if err != nil {
		return nil
	}
	worklines, err := s.store.ListWorklinesByProject(ctx, projectID)
	if err != nil {
		return nil
	}
	baseDir := s.worklineWorktreeBaseDir(project)
	targets := make([]worktreeCleanupTarget, 0, len(worklines))
	for _, workline := range worklines {
		if workline.IsRoot {
			continue
		}
		worktreePath := strings.TrimSpace(workline.WorktreePath)
		// Only autoto-managed directories are ever force-removed.
		if worktreePath != "" && !pathWithin(baseDir, worktreePath) {
			worktreePath = ""
		}
		branch := strings.TrimSpace(workline.Branch)
		if worktreePath == "" && branch == "" {
			continue
		}
		targets = append(targets, worktreeCleanupTarget{repoRoot: repoRoot, worktreePath: worktreePath, branch: branch})
	}
	return targets
}

func (s *Server) deleteArchivedAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// The durable runs check lives in the store; this catches a live in-memory
	// loop that has not yet written a run row.
	if s.runner != nil && s.runner.IsAgentRunning(id) {
		writeError(w, http.StatusConflict, "conversation is still running: interrupt it before deleting")
		return
	}
	// Resolved before deletion for the same reason as the project handler: once
	// the agent row cascades away, the workline it pointed at is unreachable.
	worklineID := ""
	if agent, err := s.store.GetAgent(r.Context(), id); err == nil {
		worklineID = strings.TrimSpace(agent.WorklineID)
	}
	if err := s.store.DeleteArchivedAgent(r.Context(), id); err != nil {
		s.writeArchiveDeleteError(w, r, "conversation", err)
		return
	}
	s.cleanupWorklineAfterAgentDeletion(worklineID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// cleanupWorklineAfterAgentDeletion removes the fork workline, its worktree
// and its branch once the deleted conversation was the last agent using them.
// Best effort: a failed cleanup only logs, the conversation deletion already
// succeeded.
func (s *Server) cleanupWorklineAfterAgentDeletion(worklineID string) {
	if worklineID == "" {
		return
	}
	ctx := context.Background()
	workline, err := s.store.GetWorkline(ctx, worklineID)
	if err != nil || workline.IsRoot {
		return
	}
	agents, err := s.store.ListAgentsByWorkline(ctx, worklineID)
	if err != nil || len(agents) > 0 {
		return
	}
	project, err := s.store.GetProject(ctx, workline.ProjectID)
	if err != nil {
		return
	}
	worktreePath := strings.TrimSpace(workline.WorktreePath)
	if worktreePath != "" && !pathWithin(s.worklineWorktreeBaseDir(project), worktreePath) {
		worktreePath = ""
	}
	branch := strings.TrimSpace(workline.Branch)
	if worktreePath != "" || branch != "" {
		repoRoot, err := s.projectMainRepoRoot(ctx, project)
		if err != nil {
			slog.Warn("workline git cleanup skipped: main repository unavailable", "worklineId", worklineID, "error", err)
		} else {
			result := cleanupWorklineGitArtifacts(ctx, repoRoot, worktreePath, branch)
			for _, warning := range result.warnings {
				slog.Warn("workline git cleanup", "worklineId", worklineID, "warning", warning)
			}
		}
	}
	if _, err := s.store.DeleteWorklineIfEmpty(ctx, worklineID); err != nil {
		slog.Warn("empty workline deletion failed", "worklineId", worklineID, "error", err)
	}
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

var slugCleanup = regexp.MustCompile(`[^a-z0-9_-]+`)

func cleanProjectPath(path string) string {
	if strings.HasPrefix(path, "Users"+string(filepath.Separator)) {
		return string(filepath.Separator) + path
	}
	return path
}

func slugify(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = slugCleanup.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "project"
	}
	return slug
}

func (s *Server) listProjectWorklines(w http.ResponseWriter, r *http.Request) {
	worklines, err := s.store.ListWorklinesByProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.filterWorklinesForRequest(r, worklines))
}

func (s *Server) getWorkline(w http.ResponseWriter, r *http.Request) {
	workline, err := s.store.GetWorkline(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "workline not found")
			return
		}
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, workline)
}

func (s *Server) listWorklineAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgentsByWorkline(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.filterAgentsForRequest(r, agents))
}
