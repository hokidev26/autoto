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
	"autoto/internal/gitlock"
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
	resp, err := s.worklineWorkflow().fork(r.Context(), chi.URLParam(r, "id"), func() (forkWorklineRequest, error) {
		var req forkWorklineRequest
		if err := decodeJSON(r, &req); err != nil {
			return req, apiErr(http.StatusBadRequest, err.Error())
		}
		return req, nil
	}, func(defaultMode, requested string) (string, bool, string) {
		if requested == "" {
			return s.safeDefaultPermissionModeForRequest(r, defaultMode), true, ""
		}
		return s.permissionModeAllowedForRequest(r, requested)
	})
	if err != nil {
		writeWorklineServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
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
	resp, err := s.worklineWorkflow().mergeCheck(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("targetWorklineId"))
	if err != nil {
		writeWorklineServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
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
	var req worklineMergeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.worklineWorkflow().merge(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		writeWorklineServiceError(w, err)
		return
	}
	writeJSON(w, result.Status, result.Body)
}

type worklineUnmergeRequest struct {
	Confirm bool `json:"confirm"`
}

type worklineUnmergeResponse struct {
	GeneratedAt      string `json:"generatedAt"`
	SourceWorklineID string `json:"sourceWorklineId"`
	TargetWorklineID string `json:"targetWorklineId"`
	// Strategy is "reset" when the merge commit was still the target head and
	// history could simply be rewound, or "revert" when the target had moved on
	// and a counter-commit was created instead.
	Strategy      string      `json:"strategy"`
	MergeCommit   string      `json:"mergeCommit"`
	NewTargetHead string      `json:"newTargetHead"`
	Conflicts     []string    `json:"conflicts,omitempty"`
	Output        string      `json:"output,omitempty"`
	Workline      db.Workline `json:"workline"`
}

// worklineUnmerge undoes a workline merge on the target branch. The merge
// endpoint records preMergeTargetSha and mergeCommitSha for exactly this
// moment: if the target head is still the merge commit the target is hard-reset
// to its pre-merge position, otherwise the merge commit is reverted with a new
// commit so later target work survives. Either way the source workline returns
// to active so it can be fixed up and merged again.
func (s *Server) worklineUnmerge(w http.ResponseWriter, r *http.Request) {
	var req worklineUnmergeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.worklineWorkflow().unmerge(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		writeWorklineServiceError(w, err)
		return
	}
	writeJSON(w, result.Status, result.Body)
}

type worklineCleanupRequest struct {
	Confirm bool `json:"confirm"`
}

type worklineCleanupResponse struct {
	GeneratedAt     string      `json:"generatedAt"`
	WorklineID      string      `json:"worklineId"`
	Branch          string      `json:"branch,omitempty"`
	WorktreePath    string      `json:"worktreePath,omitempty"`
	RemovedWorktree bool        `json:"removedWorktree"`
	DeletedBranch   bool        `json:"deletedBranch"`
	Warnings        []string    `json:"warnings,omitempty"`
	Workline        db.Workline `json:"workline"`
}

// worklineCleanup removes the git worktree and branch of a merged fork
// workline. Merging alone only flips the database status, so without this the
// worktree directory and the autoto/* branch outlive every conversation that
// referenced them.
func (s *Server) worklineCleanup(w http.ResponseWriter, r *http.Request) {
	var req worklineCleanupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.worklineWorkflow().cleanup(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		writeWorklineServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// projectMainRepoRoot resolves the primary repository a project's fork
// worktrees hang off. Worktree and branch removal must run against the main
// repository, not the (possibly already deleted) linked worktree.
func (s *Server) projectMainRepoRoot(ctx context.Context, project db.Project) (string, error) {
	gitPath := strings.TrimSpace(project.GitPath)
	if gitPath == "" {
		return "", gitCommandError{Status: http.StatusBadRequest, Msg: "project has no git path configured"}
	}
	if err := validateDir(gitPath); err != nil {
		return "", err
	}
	repoRoot, _, err := runGitCommand(ctx, gitPath, 4096, 3*time.Second, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if !s.projectAllowsRepoRoot(project, repoRoot) {
		return "", gitCommandError{Status: http.StatusForbidden, Msg: "git repository is outside the configured project boundary"}
	}
	return repoRoot, nil
}

type worklineGitCleanupResult struct {
	removedWorktree bool
	deletedBranch   bool
	warnings        []string
}

// cleanupWorklineGitArtifacts best-effort removes a fork workline's linked
// worktree and branch from the main repository. Every failure is reported as a
// warning rather than aborting: a half-cleaned workline is still better than a
// fully leaked one, and the caller records the row as cleaned either way.
func cleanupWorklineGitArtifacts(ctx context.Context, repoRoot, worktreePath, branch string) worklineGitCleanupResult {
	result := worklineGitCleanupResult{}
	unlockGitMutation := gitlock.Default.Lock(repoRoot)
	defer unlockGitMutation()
	if worktreePath = strings.TrimSpace(worktreePath); worktreePath != "" {
		if _, statErr := os.Stat(worktreePath); statErr == nil {
			if err := removeGitWorktree(ctx, repoRoot, worktreePath); err != nil {
				result.warnings = append(result.warnings, "worktree removal failed: "+err.Error())
			} else {
				result.removedWorktree = true
			}
		}
	}
	// Prune clears stale administrative entries whether the directory was just
	// removed above or had already disappeared from disk.
	if _, _, err := runGitCommand(ctx, repoRoot, worklineGitOutputMaxBytes, 10*time.Second, nil, "worktree", "prune"); err != nil {
		result.warnings = append(result.warnings, "worktree prune failed: "+err.Error())
	}
	if branch = strings.TrimSpace(branch); branch != "" {
		exists, _, verifyErr := runGitCommand(ctx, repoRoot, 256, 3*time.Second, []int{1, 128}, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
		if verifyErr == nil && strings.TrimSpace(exists) != "" {
			if _, _, err := runGitCommand(ctx, repoRoot, worklineGitOutputMaxBytes, 10*time.Second, nil, "branch", "-D", branch); err != nil {
				result.warnings = append(result.warnings, "branch deletion failed: "+err.Error())
			} else {
				result.deletedBranch = true
			}
		}
	}
	return result
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

func abortGitRevert(ctx context.Context, repoRoot string) error {
	_, _, err := runGitCommand(ctx, repoRoot, worklineGitOutputMaxBytes, 10*time.Second, []int{128}, "revert", "--abort")
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
