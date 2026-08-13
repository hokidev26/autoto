package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"autoto/internal/gitlock"
)

type worklineWorkflowService struct {
	server *Server
}

func (s *Server) worklineWorkflow() worklineWorkflowService {
	return worklineWorkflowService{server: s}
}

type worklineResult struct {
	Status int
	Body   any
}

type worklineRepoStatusError struct {
	Code       string
	Path       string
	Candidates []gitRepositoryState
}

func (e worklineRepoStatusError) Error() string {
	switch e.Code {
	case "no_git_repo":
		return `"` + e.Path + `" is not a git repository`
	case "git_no_commits":
		return `"` + e.Path + `" is a git repository but has no commits yet`
	case "multiple_git_repos":
		return `multiple git repositories were found under "` + e.Path + `"; configure the project to point to the intended repository`
	default:
		return e.Code
	}
}

type forkPermissionResolver func(defaultMode, requested string) (mode string, ok bool, message string)

func (w worklineWorkflowService) fork(ctx context.Context, worklineID string, decodeReq func() (forkWorklineRequest, error), resolvePermission forkPermissionResolver) (forkWorklineResponse, error) {
	s := w.server
	parent, project, err := s.worklineAndProject(ctx, worklineID)
	if err != nil {
		return forkWorklineResponse{}, err
	}
	sourcePath := strings.TrimSpace(parent.WorktreePath)
	if sourcePath == "" {
		sourcePath = strings.TrimSpace(project.GitPath)
	}
	if sourcePath == "" {
		return forkWorklineResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "source workline worktree is not configured"}
	}
	if err := validateDir(sourcePath); err != nil {
		return forkWorklineResponse{}, err
	}
	repository, candidates, err := resolveGitRepository(ctx, sourcePath)
	if err != nil {
		return forkWorklineResponse{}, err
	}
	if len(candidates) > 1 {
		return forkWorklineResponse{}, worklineRepoStatusError{Code: "multiple_git_repos", Path: sourcePath, Candidates: candidates}
	}
	if repository.Root == "" {
		return forkWorklineResponse{}, worklineRepoStatusError{Code: "no_git_repo", Path: sourcePath}
	}
	if !repository.HasHead {
		return forkWorklineResponse{}, worklineRepoStatusError{Code: "git_no_commits", Path: repository.Root}
	}
	repoRoot := repository.Root
	if !s.projectAllowsRepoRoot(project, repoRoot) {
		return forkWorklineResponse{}, gitCommandError{Status: http.StatusForbidden, Msg: "git repository is outside the configured project boundary"}
	}
	baseRef, err := currentGitRef(ctx, repoRoot, parent)
	if err != nil {
		return forkWorklineResponse{}, err
	}
	forkPoint, _, err := runGitCommand(ctx, repoRoot, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return forkWorklineResponse{}, err
	}
	forkPoint = strings.TrimSpace(forkPoint)
	req, err := decodeReq()
	if err != nil {
		return forkWorklineResponse{}, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Fork of " + parent.Title
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = defaultWorklineBranch(title)
	}
	branch, err = validateGitBranchName(ctx, repoRoot, branch)
	if err != nil {
		return forkWorklineResponse{}, err
	}
	worktreePath, err := s.resolveForkWorktreePath(project, repoRoot, branch, req.WorktreePath)
	if err != nil {
		return forkWorklineResponse{}, err
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return forkWorklineResponse{}, err
	}
	// Serialized against agent tool writes and commits on the same repository:
	// `worktree add` mutates refs and the worktree list, and racing a concurrent
	// git mutation can corrupt either side.
	unlockGitMutation := gitlock.Default.Lock(repoRoot)
	if _, _, err := runGitCommand(ctx, repoRoot, worklineGitOutputMaxBytes, 15*time.Second, nil, "worktree", "add", "-b", branch, worktreePath, baseRef); err != nil {
		unlockGitMutation()
		return forkWorklineResponse{}, err
	}
	unlockGitMutation()
	cfg := s.configSnapshot()
	model := strings.TrimSpace(req.Model)
	if model == "" {
		// A fork continues the conversation it branched from, so it inherits that
		// conversation's model. Falling straight through to the global default
		// silently moved the fork onto a different model than the parent, and if
		// that default has no reasoning-effort support the composer reports the
		// level as unsupported on a fork of a conversation where it worked.
		model = s.parentWorklineModel(ctx, parent.ID)
	}
	if model == "" {
		model = cfg.Agent.DefaultModel
	}
	permissionMode := strings.TrimSpace(req.PermissionMode)
	if permissionMode == "" {
		permissionMode, _, _ = resolvePermission(cfg.Agent.DefaultPermissionMode, "")
	} else {
		var ok bool
		var message string
		permissionMode, ok, message = resolvePermission(cfg.Agent.DefaultPermissionMode, permissionMode)
		if !ok {
			_ = removeGitWorktree(context.Background(), repoRoot, worktreePath)
			return forkWorklineResponse{}, apiErr(http.StatusBadRequest, message)
		}
	}
	workline, agent, err := s.store.CreateWorklineFork(ctx, parent, title, branch, worktreePath, baseRef, forkPoint, model, permissionMode)
	if err != nil {
		_ = removeGitWorktree(context.Background(), repoRoot, worktreePath)
		return forkWorklineResponse{}, apiErr(http.StatusInternalServerError, err.Error())
	}
	if cfg.Agent.DefaultStartInPlanMode {
		agent, err = s.updatePersistedAgentPlanMode(ctx, agent.ID, true)
		if err != nil {
			return forkWorklineResponse{}, apiErr(http.StatusInternalServerError, "workline fork was created but its default plan mode could not be applied")
		}
	}
	return forkWorklineResponse{Workline: workline, Agent: agent, ForkPoint: forkPoint}, nil
}

func (w worklineWorkflowService) mergeCheck(ctx context.Context, sourceID, targetWorklineID string) (worklineMergeCheckResponse, error) {
	s := w.server
	source, project, err := s.worklineAndProject(ctx, sourceID)
	if err != nil {
		return worklineMergeCheckResponse{}, err
	}
	target, err := s.mergeTargetWorkline(ctx, project.ID, targetWorklineID)
	if err != nil {
		return worklineMergeCheckResponse{}, err
	}
	if source.ID == target.ID {
		return worklineMergeCheckResponse{}, apiErr(http.StatusBadRequest, "source and target worklines must differ")
	}
	sourceRepo, sourceHead, err := s.worklineRepoAndHead(ctx, project, source)
	if err != nil {
		return worklineMergeCheckResponse{}, err
	}
	targetRepo, targetHead, err := s.worklineRepoAndHead(ctx, project, target)
	if err != nil {
		return worklineMergeCheckResponse{}, err
	}
	tempDir, err := os.MkdirTemp("", "autoto-merge-check-*")
	if err != nil {
		return worklineMergeCheckResponse{}, apiErr(http.StatusInternalServerError, err.Error())
	}
	defer os.RemoveAll(tempDir)
	if _, _, err := runGitCommand(ctx, targetRepo, worklineGitOutputMaxBytes, 15*time.Second, nil, "worktree", "add", "--detach", tempDir, targetHead); err != nil {
		return worklineMergeCheckResponse{}, err
	}
	defer removeGitWorktree(context.Background(), targetRepo, tempDir)
	// Gathered before the trial merge: afterwards the temporary worktree holds a
	// merged tree, and diffing against it would report nothing.
	changed, changedCount, filesLimited := mergeCheckChangedFiles(ctx, tempDir, targetHead, sourceHead)
	ahead, behind := mergeCheckAheadBehind(ctx, tempDir, targetHead, sourceHead)
	sourceDirty, _ := gitRepoDirty(ctx, sourceRepo)
	targetDirty, _ := gitRepoDirty(ctx, targetRepo)
	mergeOut, _, mergeErr := runGitCommand(ctx, tempDir, worklineGitOutputMaxBytes, 20*time.Second, nil, "merge", "--no-commit", "--no-ff", sourceHead)
	conflicts := mergeCheckConflicts(ctx, tempDir)
	if mergeErr != nil && len(conflicts) == 0 {
		return worklineMergeCheckResponse{}, mergeErr
	}
	return worklineMergeCheckResponse{
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
	}, nil
}

func (w worklineWorkflowService) merge(ctx context.Context, sourceID string, req worklineMergeRequest) (worklineResult, error) {
	s := w.server
	source, project, err := s.worklineAndProject(ctx, sourceID)
	if err != nil {
		return worklineResult{}, err
	}
	target, err := s.mergeTargetWorkline(ctx, project.ID, req.TargetWorklineID)
	if err != nil {
		return worklineResult{}, err
	}
	if source.ID == target.ID {
		return worklineResult{}, apiErr(http.StatusBadRequest, "source and target worklines must differ")
	}
	sourceRepo, sourceHead, err := s.worklineRepoAndHead(ctx, project, source)
	if err != nil {
		return worklineResult{}, err
	}
	targetRepo, targetHead, err := s.worklineRepoAndHead(ctx, project, target)
	if err != nil {
		return worklineResult{}, err
	}
	// The merge writes into the target worktree, which an agent may be mutating
	// at the same time. Holding the target's git lock from the dirty check
	// through the merge keeps the check and the write atomic with respect to
	// every other locked git mutation on that repository.
	unlockGitMutation := gitlock.Default.Lock(targetRepo)
	defer unlockGitMutation()
	if dirty, err := gitRepoDirty(ctx, sourceRepo); err != nil {
		return worklineResult{}, err
	} else if dirty {
		return worklineResult{}, gitCommandError{Status: http.StatusConflict, Msg: "source workline worktree has uncommitted changes"}
	}
	if dirty, err := gitRepoDirty(ctx, targetRepo); err != nil {
		return worklineResult{}, err
	} else if dirty {
		return worklineResult{}, gitCommandError{Status: http.StatusConflict, Msg: "target workline worktree has uncommitted changes"}
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "Merge workline " + source.Title
	}
	mergeOut, _, mergeErr := runGitCommand(ctx, targetRepo, worklineGitOutputMaxBytes, 30*time.Second, nil, "merge", "--no-ff", sourceHead, "-m", message)
	if mergeErr != nil {
		conflicts := mergeCheckConflicts(ctx, targetRepo)
		_ = abortGitMerge(context.Background(), targetRepo)
		if len(conflicts) > 0 {
			return worklineResult{Status: http.StatusConflict, Body: worklineMergeResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceWorklineID: source.ID, TargetWorklineID: target.ID, SourceHead: sourceHead, PreMergeTarget: targetHead, Merged: false, Conflicts: conflicts, Output: strings.TrimSpace(mergeOut)}}, nil
		}
		return worklineResult{}, mergeErr
	}
	mergeCommit, _, err := runGitCommand(ctx, targetRepo, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return worklineResult{}, err
	}
	mergeCommit = strings.TrimSpace(mergeCommit)
	updated, err := s.store.MarkWorklineMerged(ctx, source.ID, target.ID, targetHead, mergeCommit, "no-ff")
	if err != nil {
		return worklineResult{}, apiErr(http.StatusInternalServerError, err.Error())
	}
	return worklineResult{Status: http.StatusOK, Body: worklineMergeResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceWorklineID: source.ID, TargetWorklineID: target.ID, SourceHead: sourceHead, PreMergeTarget: targetHead, MergeCommit: mergeCommit, Merged: true, Output: strings.TrimSpace(mergeOut), Workline: updated}}, nil
}

func (w worklineWorkflowService) unmerge(ctx context.Context, sourceID string, req worklineUnmergeRequest) (worklineResult, error) {
	s := w.server
	source, project, err := s.worklineAndProject(ctx, sourceID)
	if err != nil {
		return worklineResult{}, err
	}
	if !req.Confirm {
		return worklineResult{}, apiErr(http.StatusBadRequest, "confirm must be true")
	}
	if source.Status != "merged" || strings.TrimSpace(source.MergeCommitSHA) == "" || strings.TrimSpace(source.MergedIntoWorklineID) == "" {
		return worklineResult{}, apiErr(http.StatusConflict, "workline has no recorded merge to undo")
	}
	// After cleanup the fork's branch and worktree are gone, so the workline
	// cannot return to active; on the reset path the merged commits would also
	// lose their last named ref. Undoing then is a manual git operation.
	if strings.TrimSpace(source.WorktreePath) == "" {
		return worklineResult{}, apiErr(http.StatusConflict, "workline branch was already cleaned up; undo the merge manually with git revert")
	}
	target, err := s.store.GetWorkline(ctx, source.MergedIntoWorklineID)
	if err != nil {
		return worklineResult{}, err
	}
	if target.ProjectID != project.ID {
		return worklineResult{}, apiErr(http.StatusConflict, "merge target belongs to a different project")
	}
	_, sourceHead, err := s.worklineRepoAndHead(ctx, project, source)
	if err != nil {
		return worklineResult{}, err
	}
	targetRepo, _, err := s.worklineRepoAndHead(ctx, project, target)
	if err != nil {
		return worklineResult{}, err
	}
	mergeCommit := strings.TrimSpace(source.MergeCommitSHA)
	// The unmerge rewrites the target worktree; holding its git lock keeps the
	// head/dirty inspection and the reset-or-revert decision atomic against
	// agent commits landing on the same repository.
	unlockGitMutation := gitlock.Default.Lock(targetRepo)
	defer unlockGitMutation()
	if dirty, err := gitRepoDirty(ctx, targetRepo); err != nil {
		return worklineResult{}, err
	} else if dirty {
		return worklineResult{}, gitCommandError{Status: http.StatusConflict, Msg: "target workline worktree has uncommitted changes"}
	}
	targetHead, _, err := runGitCommand(ctx, targetRepo, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return worklineResult{}, err
	}
	targetHead = strings.TrimSpace(targetHead)
	// The merge commit must still be part of the target history: if someone
	// already rewound or reverted it by hand there is nothing left to undo.
	if _, _, err := runGitCommand(ctx, targetRepo, 256, 3*time.Second, nil, "merge-base", "--is-ancestor", mergeCommit, targetHead); err != nil {
		return worklineResult{}, gitCommandError{Status: http.StatusConflict, Msg: "merge commit is no longer part of the target branch history"}
	}
	strategy := "revert"
	output := ""
	if targetHead == mergeCommit && strings.TrimSpace(source.PreMergeTargetSHA) != "" {
		strategy = "reset"
		out, _, err := runGitCommand(ctx, targetRepo, worklineGitOutputMaxBytes, 30*time.Second, nil, "reset", "--hard", strings.TrimSpace(source.PreMergeTargetSHA))
		if err != nil {
			return worklineResult{}, err
		}
		output = strings.TrimSpace(out)
	} else {
		out, _, revertErr := runGitCommand(ctx, targetRepo, worklineGitOutputMaxBytes, 30*time.Second, nil, "revert", "-m", "1", "--no-edit", mergeCommit)
		if revertErr != nil {
			conflicts := mergeCheckConflicts(ctx, targetRepo)
			_ = abortGitRevert(context.Background(), targetRepo)
			if len(conflicts) > 0 {
				return worklineResult{Status: http.StatusConflict, Body: worklineUnmergeResponse{
					GeneratedAt:      time.Now().UTC().Format(time.RFC3339Nano),
					SourceWorklineID: source.ID,
					TargetWorklineID: target.ID,
					Strategy:         strategy,
					MergeCommit:      mergeCommit,
					Conflicts:        conflicts,
					Output:           strings.TrimSpace(out),
					Workline:         source,
				}}, nil
			}
			return worklineResult{}, revertErr
		}
		output = strings.TrimSpace(out)
	}
	newTargetHead, _, err := runGitCommand(ctx, targetRepo, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return worklineResult{}, err
	}
	newTargetHead = strings.TrimSpace(newTargetHead)
	updated, err := s.store.MarkWorklineUnmerged(ctx, source.ID, sourceHead, target.ID, newTargetHead)
	if err != nil {
		return worklineResult{}, apiErr(http.StatusInternalServerError, "merge was undone in git but the workline record could not be updated: "+err.Error())
	}
	return worklineResult{Status: http.StatusOK, Body: worklineUnmergeResponse{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		SourceWorklineID: source.ID,
		TargetWorklineID: target.ID,
		Strategy:         strategy,
		MergeCommit:      mergeCommit,
		NewTargetHead:    newTargetHead,
		Output:           output,
		Workline:         updated,
	}}, nil
}

func (w worklineWorkflowService) cleanup(ctx context.Context, worklineID string, req worklineCleanupRequest) (worklineCleanupResponse, error) {
	s := w.server
	workline, project, err := s.worklineAndProject(ctx, worklineID)
	if err != nil {
		return worklineCleanupResponse{}, err
	}
	if !req.Confirm {
		return worklineCleanupResponse{}, apiErr(http.StatusBadRequest, "confirm must be true")
	}
	if workline.IsRoot {
		return worklineCleanupResponse{}, apiErr(http.StatusBadRequest, "mainline workline has no fork worktree to clean up")
	}
	if workline.Status != "merged" {
		return worklineCleanupResponse{}, apiErr(http.StatusConflict, "only merged worklines can be cleaned up")
	}
	worktreePath := strings.TrimSpace(workline.WorktreePath)
	branch := strings.TrimSpace(workline.Branch)
	// The branch name is kept on the row as history; a cleared worktree path is
	// what marks the cleanup as already done.
	if worktreePath == "" {
		return worklineCleanupResponse{}, apiErr(http.StatusConflict, "workline was already cleaned up")
	}
	if busy, err := s.store.WorklineHasActiveRuns(ctx, workline.ID); err != nil {
		return worklineCleanupResponse{}, apiErr(http.StatusInternalServerError, err.Error())
	} else if busy {
		return worklineCleanupResponse{}, apiErr(http.StatusConflict, "workline conversation is still running: interrupt it before cleaning up")
	}
	if s.runner != nil {
		agents, err := s.store.ListAgentsByWorkline(ctx, workline.ID)
		if err != nil {
			return worklineCleanupResponse{}, apiErr(http.StatusInternalServerError, err.Error())
		}
		for _, agent := range agents {
			if s.runner.IsAgentRunning(agent.ID) {
				return worklineCleanupResponse{}, apiErr(http.StatusConflict, "workline conversation is still running: interrupt it before cleaning up")
			}
		}
	}
	// Only paths autoto created for this project may be force-removed; anything
	// else recorded in the row is treated as user territory.
	if worktreePath != "" && !pathWithin(s.worklineWorktreeBaseDir(project), worktreePath) {
		return worklineCleanupResponse{}, apiErr(http.StatusBadRequest, "workline worktree is outside the managed worktree directory")
	}
	repoRoot, err := s.projectMainRepoRoot(ctx, project)
	if err != nil {
		return worklineCleanupResponse{}, err
	}
	result := cleanupWorklineGitArtifacts(ctx, repoRoot, worktreePath, branch)
	updated, err := s.store.ClearWorklineWorktree(ctx, workline.ID)
	if err != nil {
		return worklineCleanupResponse{}, apiErr(http.StatusInternalServerError, "git cleanup finished but the workline record could not be updated: "+err.Error())
	}
	return worklineCleanupResponse{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		WorklineID:      workline.ID,
		Branch:          branch,
		WorktreePath:    worktreePath,
		RemovedWorktree: result.removedWorktree,
		DeletedBranch:   result.deletedBranch,
		Warnings:        result.warnings,
		Workline:        updated,
	}, nil
}

func (s *Server) writeWorklineServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	var repoErr worklineRepoStatusError
	if errors.As(err, &repoErr) {
		switch repoErr.Code {
		case "no_git_repo":
			writeNoGitRepoError(w, repoErr.Path)
		case "git_no_commits":
			writeNoGitCommitsError(w, repoErr.Path)
		case "multiple_git_repos":
			writeMultipleGitReposError(w, repoErr.Path, repoErr.Candidates)
		default:
			s.writeRequestError(w, r, http.StatusBadRequest, repoErr)
		}
		return
	}
	var api apiError
	if errors.As(err, &api) {
		writeError(w, api.status, api.msg)
		return
	}
	s.writeWorklineWorkflowError(w, r, err)
}
