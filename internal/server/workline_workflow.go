package server

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

const worklineGitOutputMaxBytes = 512 << 10

type forkWorklineRequest struct {
	Title          string `json:"title"`
	Branch         string `json:"branch"`
	WorktreePath   string `json:"worktreePath"`
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
}

type forkWorklineResponse struct {
	Workline  db.Workline `json:"workline"`
	Agent     db.Agent    `json:"agent"`
	ForkPoint string      `json:"forkPoint"`
}

type worklineMergeCheckResponse struct {
	GeneratedAt      string   `json:"generatedAt"`
	SourceWorklineID string   `json:"sourceWorklineId"`
	TargetWorklineID string   `json:"targetWorklineId"`
	SourceBranch     string   `json:"sourceBranch,omitempty"`
	TargetBranch     string   `json:"targetBranch,omitempty"`
	SourceHead       string   `json:"sourceHead,omitempty"`
	TargetHead       string   `json:"targetHead,omitempty"`
	CanMerge         bool     `json:"canMerge"`
	Conflicts        []string `json:"conflicts,omitempty"`
	Output           string   `json:"output,omitempty"`
	// Review signals. A merge is a decision, so the caller gets the size and
	// direction of the change alongside the verdict instead of a bare boolean.
	ChangedFiles  []string `json:"changedFiles,omitempty"`
	ChangedCount  int      `json:"changedCount"`
	Ahead         int      `json:"ahead"`
	Behind        int      `json:"behind"`
	SourceDirty   bool     `json:"sourceDirty"`
	TargetDirty   bool     `json:"targetDirty"`
	FilesLimited  bool     `json:"filesLimited,omitempty"`
	AlreadyMerged bool     `json:"alreadyMerged,omitempty"`
}

// mergeCheckFileLimit bounds the file list the API returns. The count stays
// exact; only the enumerated names are capped so a large branch cannot turn a
// review request into a multi-megabyte response.
const mergeCheckFileLimit = 200

type worklineMergeRequest struct {
	TargetWorklineID string `json:"targetWorklineId"`
	Message          string `json:"message"`
}

type worklineMergeResponse struct {
	GeneratedAt      string      `json:"generatedAt"`
	SourceWorklineID string      `json:"sourceWorklineId"`
	TargetWorklineID string      `json:"targetWorklineId"`
	SourceHead       string      `json:"sourceHead,omitempty"`
	PreMergeTarget   string      `json:"preMergeTarget,omitempty"`
	MergeCommit      string      `json:"mergeCommit,omitempty"`
	Merged           bool        `json:"merged"`
	Conflicts        []string    `json:"conflicts,omitempty"`
	Output           string      `json:"output,omitempty"`
	Workline         db.Workline `json:"workline,omitempty"`
}

func (s *Server) forkWorkline(w http.ResponseWriter, r *http.Request) {
	parent, project, err := s.worklineAndProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeWorklineWorkflowError(w, err)
		return
	}
	sourcePath := strings.TrimSpace(parent.WorktreePath)
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(project.GitPath)
	}
	if sourcePath == "" {
		writeGitError(w, gitCommandError{Status: http.StatusBadRequest, Msg: "source workline worktree is not configured"})
		return
	}
	if err := validateDir(sourcePath); err != nil {
		writeWorklineWorkflowError(w, err)
		return
	}
	repository, candidates, err := resolveGitRepository(r.Context(), sourcePath)
	if err != nil {
		writeGitError(w, err)
		return
	}
	if len(candidates) > 1 {
		writeMultipleGitReposError(w, sourcePath, candidates)
		return
	}
	if repository.Root == "" {
		writeNoGitRepoError(w, sourcePath)
		return
	}
	if !repository.HasHead {
		writeNoGitCommitsError(w, repository.Root)
		return
	}
	repoRoot := repository.Root
	if !s.projectAllowsRepoRoot(project, repoRoot) {
		writeGitError(w, gitCommandError{Status: http.StatusForbidden, Msg: "git repository is outside the configured project boundary"})
		return
	}
	baseRef, err := currentGitRef(r.Context(), repoRoot, parent)
	if err != nil {
		writeGitError(w, err)
		return
	}
	forkPoint, _, err := runGitCommand(r.Context(), repoRoot, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		writeGitError(w, err)
		return
	}
	forkPoint = strings.TrimSpace(forkPoint)
	var req forkWorklineRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Fork of " + parent.Title
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = defaultWorklineBranch(title)
	}
	branch, err = validateGitBranchName(r.Context(), repoRoot, branch)
	if err != nil {
		writeGitError(w, err)
		return
	}
	worktreePath, err := s.resolveForkWorktreePath(project, repoRoot, branch, req.WorktreePath)
	if err != nil {
		writeGitError(w, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		writeGitError(w, err)
		return
	}
	if _, _, err := runGitCommand(r.Context(), repoRoot, worklineGitOutputMaxBytes, 15*time.Second, nil, "worktree", "add", "-b", branch, worktreePath, baseRef); err != nil {
		writeGitError(w, err)
		return
	}
	cfg := s.configSnapshot()
	model := strings.TrimSpace(req.Model)
	if model == "" {
		// A fork continues the conversation it branched from, so it inherits that
		// conversation's model. Falling straight through to the global default
		// silently moved the fork onto a different model than the parent, and if
		// that default has no reasoning-effort support the composer reports the
		// level as unsupported on a fork of a conversation where it worked.
		model = s.parentWorklineModel(r.Context(), parent.ID)
	}
	if model == "" {
		model = cfg.Agent.DefaultModel
	}
	permissionMode := strings.TrimSpace(req.PermissionMode)
	if permissionMode == "" {
		permissionMode = s.safeDefaultPermissionModeForRequest(r, cfg.Agent.DefaultPermissionMode)
	} else {
		var ok bool
		var message string
		permissionMode, ok, message = s.permissionModeAllowedForRequest(r, permissionMode)
		if !ok {
			_ = removeGitWorktree(context.Background(), repoRoot, worktreePath)
			writeError(w, http.StatusBadRequest, message)
			return
		}
	}
	workline, agent, err := s.store.CreateWorklineFork(r.Context(), parent, title, branch, worktreePath, baseRef, forkPoint, model, permissionMode)
	if err != nil {
		_ = removeGitWorktree(context.Background(), repoRoot, worktreePath)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg.Agent.DefaultStartInPlanMode {
		agent, err = s.updatePersistedAgentPlanMode(r.Context(), agent.ID, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "workline fork was created but its default plan mode could not be applied")
			return
		}
	}
	writeJSON(w, http.StatusCreated, forkWorklineResponse{Workline: workline, Agent: agent, ForkPoint: forkPoint})
}

// parentWorklineModel reports the model the forked conversation was running, so
// a fork can continue on it. Returns "" when the parent has no agent or none of
// them recorded a model, leaving the caller to fall back to the global default.
// A lookup failure is deliberately not fatal: a fork that lands on the default
// model is a worse outcome than the parent's, but refusing to fork at all is
// worse still.
func (s *Server) parentWorklineModel(ctx context.Context, worklineID string) string {
	if strings.TrimSpace(worklineID) == "" {
		return ""
	}
	agents, err := s.store.ListAgentsByWorkline(ctx, worklineID)
	if err != nil {
		return ""
	}
	// Prefer the primary agent: subagents can be assigned their own models, and
	// inheriting one of those would put the fork on a model the reader never
	// chose for this conversation.
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Type), "primary") {
			if model := strings.TrimSpace(agent.Model); model != "" {
				return model
			}
		}
	}
	for _, agent := range agents {
		if model := strings.TrimSpace(agent.Model); model != "" {
			return model
		}
	}
	return ""
}

func (s *Server) worklineMergeCheck(w http.ResponseWriter, r *http.Request) {
	source, project, err := s.worklineAndProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeWorklineWorkflowError(w, err)
		return
	}
	target, err := s.mergeTargetWorkline(r.Context(), project.ID, r.URL.Query().Get("targetWorklineId"))
	if err != nil {
		writeWorklineWorkflowError(w, err)
		return
	}
	if source.ID == target.ID {
		writeError(w, http.StatusBadRequest, "source and target worklines must differ")
		return
	}
	sourceRepo, sourceHead, err := s.worklineRepoAndHead(r.Context(), project, source)
	if err != nil {
		writeGitError(w, err)
		return
	}
	targetRepo, targetHead, err := s.worklineRepoAndHead(r.Context(), project, target)
	if err != nil {
		writeGitError(w, err)
		return
	}
	tempDir, err := os.MkdirTemp("", "autoto-merge-check-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(tempDir)
	if _, _, err := runGitCommand(r.Context(), targetRepo, worklineGitOutputMaxBytes, 15*time.Second, nil, "worktree", "add", "--detach", tempDir, targetHead); err != nil {
		writeGitError(w, err)
		return
	}
	defer removeGitWorktree(context.Background(), targetRepo, tempDir)
	// Gathered before the trial merge: afterwards the temporary worktree holds a
	// merged tree, and diffing against it would report nothing.
	changed, changedCount, filesLimited := mergeCheckChangedFiles(r.Context(), tempDir, targetHead, sourceHead)
	ahead, behind := mergeCheckAheadBehind(r.Context(), tempDir, targetHead, sourceHead)
	sourceDirty, _ := gitRepoDirty(r.Context(), sourceRepo)
	targetDirty, _ := gitRepoDirty(r.Context(), targetRepo)
	mergeOut, _, mergeErr := runGitCommand(r.Context(), tempDir, worklineGitOutputMaxBytes, 20*time.Second, nil, "merge", "--no-commit", "--no-ff", sourceHead)
	conflicts := mergeCheckConflicts(r.Context(), tempDir)
	if mergeErr != nil && len(conflicts) == 0 {
		writeGitError(w, mergeErr)
		return
	}
	writeJSON(w, http.StatusOK, worklineMergeCheckResponse{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		SourceWorklineID: source.ID,
		TargetWorklineID: target.ID,
		SourceBranch:     source.Branch,
		TargetBranch:     target.Branch,
		SourceHead:       sourceHead,
		TargetHead:       targetHead,
		CanMerge:         mergeErr == nil && len(conflicts) == 0,
		Conflicts:        conflicts,
		Output:           strings.TrimSpace(mergeOut),
		ChangedFiles:     changed,
		ChangedCount:     changedCount,
		Ahead:            ahead,
		Behind:           behind,
		SourceDirty:      sourceDirty,
		TargetDirty:      targetDirty,
		FilesLimited:     filesLimited,
		// Nothing to bring over: the source is already contained in the target.
		AlreadyMerged: ahead == 0 && changedCount == 0,
	})
}

// mergeCheckChangedFiles lists what the source would bring into the target,
// comparing against the merge base rather than the target head so unrelated
// target-side commits are not counted as the source's work.
func mergeCheckChangedFiles(ctx context.Context, dir, targetHead, sourceHead string) ([]string, int, bool) {
	out, _, err := runGitCommand(ctx, dir, gitStatusMaxBytes, 10*time.Second, nil, "diff", "--name-only", "-z", targetHead+"..."+sourceHead)
	if err != nil {
		return nil, 0, false
	}
	names := make([]string, 0)
	for _, name := range strings.Split(out, "\x00") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) > mergeCheckFileLimit {
		return names[:mergeCheckFileLimit], len(names), true
	}
	return names, len(names), false
}

// mergeCheckAheadBehind counts commits unique to each side. Behind matters for
// review: a source branch far behind its target is likely to need a rebase even
// when the trial merge succeeds.
func mergeCheckAheadBehind(ctx context.Context, dir, targetHead, sourceHead string) (int, int) {
	out, _, err := runGitCommand(ctx, dir, 512, 5*time.Second, nil, "rev-list", "--left-right", "--count", sourceHead+"..."+targetHead)
	if err != nil {
		return 0, 0
	}
	return parseAheadBehind(strings.TrimSpace(out))
}

func (s *Server) worklineMerge(w http.ResponseWriter, r *http.Request) {
	source, project, err := s.worklineAndProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeWorklineWorkflowError(w, err)
		return
	}
	var req worklineMergeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := s.mergeTargetWorkline(r.Context(), project.ID, req.TargetWorklineID)
	if err != nil {
		writeWorklineWorkflowError(w, err)
		return
	}
	if source.ID == target.ID {
		writeError(w, http.StatusBadRequest, "source and target worklines must differ")
		return
	}
	sourceRepo, sourceHead, err := s.worklineRepoAndHead(r.Context(), project, source)
	if err != nil {
		writeGitError(w, err)
		return
	}
	targetRepo, targetHead, err := s.worklineRepoAndHead(r.Context(), project, target)
	if err != nil {
		writeGitError(w, err)
		return
	}
	if dirty, err := gitRepoDirty(r.Context(), sourceRepo); err != nil {
		writeGitError(w, err)
		return
	} else if dirty {
		writeGitError(w, gitCommandError{Status: http.StatusConflict, Msg: "source workline worktree has uncommitted changes"})
		return
	}
	if dirty, err := gitRepoDirty(r.Context(), targetRepo); err != nil {
		writeGitError(w, err)
		return
	} else if dirty {
		writeGitError(w, gitCommandError{Status: http.StatusConflict, Msg: "target workline worktree has uncommitted changes"})
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "Merge workline " + source.Title
	}
	mergeOut, _, mergeErr := runGitCommand(r.Context(), targetRepo, worklineGitOutputMaxBytes, 30*time.Second, nil, "merge", "--no-ff", sourceHead, "-m", message)
	if mergeErr != nil {
		conflicts := mergeCheckConflicts(r.Context(), targetRepo)
		_ = abortGitMerge(context.Background(), targetRepo)
		if len(conflicts) > 0 {
			writeJSON(w, http.StatusConflict, worklineMergeResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceWorklineID: source.ID, TargetWorklineID: target.ID, SourceHead: sourceHead, PreMergeTarget: targetHead, Merged: false, Conflicts: conflicts, Output: strings.TrimSpace(mergeOut)})
			return
		}
		writeGitError(w, mergeErr)
		return
	}
	mergeCommit, _, err := runGitCommand(r.Context(), targetRepo, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		writeGitError(w, err)
		return
	}
	mergeCommit = strings.TrimSpace(mergeCommit)
	updated, err := s.store.MarkWorklineMerged(r.Context(), source.ID, target.ID, targetHead, mergeCommit, "no-ff")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, worklineMergeResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceWorklineID: source.ID, TargetWorklineID: target.ID, SourceHead: sourceHead, PreMergeTarget: targetHead, MergeCommit: mergeCommit, Merged: true, Output: strings.TrimSpace(mergeOut), Workline: updated})
}

func (s *Server) worklineAndProject(ctx context.Context, worklineID string) (db.Workline, db.Project, error) {
	workline, err := s.store.GetWorkline(ctx, worklineID)
	if err != nil {
		return db.Workline{}, db.Project{}, err
	}
	project, err := s.store.GetProject(ctx, workline.ProjectID)
	if err != nil {
		return db.Workline{}, db.Project{}, err
	}
	return workline, project, nil
}

func (s *Server) mergeTargetWorkline(ctx context.Context, projectID, targetWorklineID string) (db.Workline, error) {
	targetWorklineID = strings.TrimSpace(targetWorklineID)
	if targetWorklineID != "" {
		target, err := s.store.GetWorkline(ctx, targetWorklineID)
		if err != nil {
			return db.Workline{}, err
		}
		if target.ProjectID != projectID {
			return db.Workline{}, sql.ErrNoRows
		}
		return target, nil
	}
	worklines, err := s.store.ListWorklinesByProject(ctx, projectID)
	if err != nil {
		return db.Workline{}, err
	}
	for _, workline := range worklines {
		if workline.IsRoot {
			return workline, nil
		}
	}
	return db.Workline{}, sql.ErrNoRows
}

func (s *Server) worklineRepoAndHead(ctx context.Context, project db.Project, workline db.Workline) (string, string, error) {
	path := strings.TrimSpace(workline.WorktreePath)
	if path == "" {
		path = strings.TrimSpace(project.GitPath)
	}
	if err := validateDir(path); err != nil {
		return "", "", err
	}
	repoRoot, _, err := runGitCommand(ctx, path, 4096, 3*time.Second, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if !s.projectAllowsRepoRoot(project, repoRoot) && !pathWithin(workline.WorktreePath, repoRoot) {
		return "", "", gitCommandError{Status: http.StatusForbidden, Msg: "git repository is outside the configured project boundary"}
	}
	head, _, err := runGitCommand(ctx, repoRoot, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return repoRoot, strings.TrimSpace(head), nil
}

func (s *Server) projectAllowsRepoRoot(project db.Project, repoRoot string) bool {
	if strings.TrimSpace(project.GitPath) != "" && pathWithin(project.GitPath, repoRoot) {
		return true
	}
	if defaultDir := strings.TrimSpace(s.configSnapshot().Paths.DefaultProjectDir); defaultDir != "" && pathWithin(defaultDir, repoRoot) {
		return true
	}
	return false
}

func validateDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return gitCommandError{Status: http.StatusBadRequest, Msg: "path must be a directory"}
	}
	return nil
}

func currentGitRef(ctx context.Context, repoRoot string, workline db.Workline) (string, error) {
	if strings.TrimSpace(workline.Branch) != "" {
		return strings.TrimSpace(workline.Branch), nil
	}
	branch, _, _ := runGitCommand(ctx, repoRoot, 512, 2*time.Second, nil, "branch", "--show-current")
	if strings.TrimSpace(branch) != "" {
		return strings.TrimSpace(branch), nil
	}
	return "HEAD", nil
}

func validateGitBranchName(ctx context.Context, repoRoot, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", gitCommandError{Status: http.StatusBadRequest, Msg: "branch is required"}
	}
	if strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") || filepath.IsAbs(branch) {
		return "", gitCommandError{Status: http.StatusBadRequest, Msg: "invalid branch name"}
	}
	out, _, err := runGitCommand(ctx, repoRoot, 512, 3*time.Second, nil, "check-ref-format", "--branch", branch)
	if err != nil {
		return "", err
	}
	if normalized := strings.TrimSpace(out); normalized != "" {
		branch = normalized
	}
	return branch, nil
}

var branchUnsafeRE = regexp.MustCompile(`[^a-zA-Z0-9._/-]+`)

func defaultWorklineBranch(title string) string {
	base := strings.ToLower(strings.TrimSpace(title))
	base = branchUnsafeRE.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-./")
	if base == "" {
		base = "workline"
	}
	return "autoto/" + base + "-" + db.NewID()[:8]
}

func (s *Server) resolveForkWorktreePath(project db.Project, repoRoot, branch, requested string) (string, error) {
	base := s.worklineWorktreeBaseDir(project)
	var path string
	if strings.TrimSpace(requested) == "" {
		path = filepath.Join(base, slugify(branch))
	} else {
		abs, err := filepath.Abs(cleanProjectPath(strings.TrimSpace(requested)))
		if err != nil {
			return "", gitCommandError{Status: http.StatusBadRequest, Msg: err.Error()}
		}
		path = abs
	}
	if !pathWithin(base, path) {
		return "", gitCommandError{Status: http.StatusBadRequest, Msg: "worktree path must stay within the project worktree directory"}
	}
	if pathWithin(repoRoot, path) {
		return "", gitCommandError{Status: http.StatusBadRequest, Msg: "worktree path must not be inside the source repository"}
	}
	if _, err := os.Stat(path); err == nil {
		return "", gitCommandError{Status: http.StatusConflict, Msg: "worktree path already exists"}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func (s *Server) worklineWorktreeBaseDir(project db.Project) string {
	projectPath := strings.TrimSpace(project.GitPath)
	defaultDir := strings.TrimSpace(s.configSnapshot().Paths.DefaultProjectDir)
	if defaultDir != "" && (projectPath == "" || pathWithin(defaultDir, projectPath)) {
		return filepath.Join(defaultDir, ".autoto-worktrees", slugify(project.Name))
	}
	if projectPath != "" {
		return filepath.Join(filepath.Dir(projectPath), ".autoto-worktrees", slugify(project.Name))
	}
	return filepath.Join(os.TempDir(), ".autoto-worktrees", slugify(project.Name))
}

func removeGitWorktree(ctx context.Context, repoRoot, worktreePath string) error {
	_, _, err := runGitCommand(ctx, repoRoot, worklineGitOutputMaxBytes, 10*time.Second, []int{128}, "worktree", "remove", "--force", worktreePath)
	return err
}

func gitRepoDirty(ctx context.Context, repoRoot string) (bool, error) {
	statusOut, _, err := runGitCommand(ctx, repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z")
	if err != nil {
		return false, err
	}
	return strings.Trim(statusOut, "\x00\n\r\t ") != "", nil
}

func abortGitMerge(ctx context.Context, repoRoot string) error {
	_, _, err := runGitCommand(ctx, repoRoot, worklineGitOutputMaxBytes, 10*time.Second, []int{128}, "merge", "--abort")
	return err
}

func mergeCheckConflicts(ctx context.Context, tempDir string) []string {
	statusOut, _, err := runGitCommand(ctx, tempDir, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil
	}
	files := parseGitPorcelainStatus(statusOut)
	conflicts := make([]string, 0)
	for _, file := range files {
		if isUnmergedStatus(file.Index, file.Worktree) {
			conflicts = append(conflicts, file.Path)
		}
	}
	return conflicts
}

func isUnmergedStatus(index, worktree string) bool {
	pair := index + worktree
	switch pair {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}

func writeWorklineWorkflowError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "workline not found")
		return
	}
	writeGitError(w, err)
}
