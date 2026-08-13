package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/db"
	"autoto/internal/gitlock"
)

type gitService struct {
	store             *db.Store
	hub               *agentpkg.Hub
	defaultProjectDir string
}

func (s *Server) git() gitService {
	defaultProjectDir := ""
	var store *db.Store
	var hub *agentpkg.Hub
	if s != nil {
		store = s.store
		hub = s.hub
		defaultProjectDir = strings.TrimSpace(s.configSnapshot().Paths.DefaultProjectDir)
	}
	return gitService{store: store, hub: hub, defaultProjectDir: defaultProjectDir}
}

func (g gitService) status(ctx context.Context, agentID string) (gitStatusResponse, error) {
	cwd, repoRoot, err := g.resolveAgentRepo(ctx, agentID)
	if err != nil {
		return gitStatusResponse{}, err
	}
	return g.statusForRepo(ctx, repoRoot, cwd)
}

func (g gitService) diff(ctx context.Context, agentID, scope, rawPath, contextParam string) (gitDiffResponse, error) {
	_, repoRoot, err := g.resolveAgentRepo(ctx, agentID)
	if err != nil {
		return gitDiffResponse{}, err
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "staged" && scope != "unstaged" {
		return gitDiffResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "invalid git diff scope"}
	}
	relPath, err := cleanGitPath(repoRoot, rawPath)
	if err != nil {
		return gitDiffResponse{}, err
	}
	contextLines := boundedInt(contextParam, 3, 0, 20)
	hasHead := true
	if scope == "all" {
		hasHead = gitRepoHasHead(ctx, repoRoot)
	}
	patchArgs := gitDiffArgs(scope, contextLines, false, relPath, hasHead)
	patch, truncated, err := runGitCommand(ctx, repoRoot, gitDiffMaxBytes, 5*time.Second, nil, patchArgs...)
	if err != nil {
		return gitDiffResponse{}, err
	}
	statArgs := gitDiffArgs(scope, contextLines, true, relPath, hasHead)
	statOut, statTruncated, _ := runGitCommand(ctx, repoRoot, gitLogMaxBytes, 5*time.Second, nil, statArgs...)
	return gitDiffResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		RepoRoot:    repoRoot,
		Scope:       scope,
		Path:        relPath,
		Patch:       safeUTF8(patch),
		Files:       parseGitNumstat(statOut),
		Truncated:   truncated || statTruncated,
	}, nil
}

func (g gitService) log(ctx context.Context, agentID, limitParam string) (gitLogResponse, error) {
	_, repoRoot, err := g.resolveAgentRepo(ctx, agentID)
	if err != nil {
		return gitLogResponse{}, err
	}
	limit := boundedInt(limitParam, 30, 1, 100)
	format := "%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x00%x1e"
	out, truncated, err := runGitCommand(ctx, repoRoot, gitLogMaxBytes, 3*time.Second, nil, "log", "--max-count="+strconv.Itoa(limit), "--date=iso-strict", "--pretty=format:"+format)
	if err != nil {
		return gitLogResponse{}, err
	}
	return gitLogResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), RepoRoot: repoRoot, Commits: parseGitLog(out), Truncated: truncated}, nil
}

func (g gitService) rollbackPreview(ctx context.Context, agentID, runID string) (gitRollbackPreviewResponse, error) {
	_, repoRoot, err := g.resolveAgentRepo(ctx, agentID)
	if err != nil {
		return gitRollbackPreviewResponse{}, err
	}
	run, err := g.store.GetRun(ctx, agentID, runID)
	if err != nil {
		return gitRollbackPreviewResponse{}, err
	}
	plan, err := g.buildRollbackPlan(ctx, repoRoot, run, db.RunCheckpointReady)
	if err != nil {
		plan = gitRollbackPlan{reason: err.Error()}
	}
	return gitRollbackPreview(repoRoot, runID, plan), nil
}

func (g gitService) rollback(ctx context.Context, agentID, runID string, confirm bool) (gitRollbackResponse, error) {
	cwd, repoRoot, err := g.resolveAgentRepo(ctx, agentID)
	if err != nil {
		return gitRollbackResponse{}, err
	}
	if !confirm {
		return gitRollbackResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "confirm must be true"}
	}

	unlockGitMutation := gitlock.Default.Lock(repoRoot)
	defer unlockGitMutation()
	run, err := g.store.GetRun(ctx, agentID, runID)
	if err != nil {
		return gitRollbackResponse{}, err
	}
	plan, err := g.buildRollbackPlan(ctx, repoRoot, run, db.RunCheckpointReady)
	if err != nil {
		return gitRollbackResponse{}, err
	}
	if !plan.available {
		return gitRollbackResponse{}, gitCommandError{Status: http.StatusConflict, Msg: plan.reason}
	}
	if err := g.store.ClaimRunGitRollback(ctx, runID); err != nil {
		return gitRollbackResponse{}, gitCommandError{Status: http.StatusConflict, Msg: "run rollback is no longer available: " + err.Error()}
	}

	run, err = g.store.GetRun(ctx, agentID, runID)
	if err != nil {
		return gitRollbackResponse{}, g.failRollbackAfterClaim(ctx, runID, "reload run after rollback claim failed: "+err.Error(), http.StatusInternalServerError)
	}
	plan, err = g.buildRollbackPlan(ctx, repoRoot, run, db.RunCheckpointRollingBack)
	if err != nil || !plan.available {
		reason := "rollback verification failed after claim"
		if err != nil {
			reason += ": " + err.Error()
		} else if plan.reason != "" {
			reason += ": " + plan.reason
		}
		return gitRollbackResponse{}, g.failRollbackAfterClaim(ctx, runID, reason, http.StatusConflict)
	}
	if err := restoreRunGitChanges(ctx, repoRoot, plan.baseHead, plan.changes); err != nil {
		return gitRollbackResponse{}, g.failRollbackAfterClaim(ctx, runID, "rollback file operations failed: "+err.Error(), http.StatusInternalServerError)
	}
	if err := g.store.MarkRunGitCheckpointRolledBack(ctx, runID); err != nil {
		return gitRollbackResponse{}, g.failRollbackAfterClaim(ctx, runID, "rollback file operations completed, but checkpoint state could not be marked rolled back: "+err.Error(), http.StatusInternalServerError)
	}
	response := gitRollbackResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), RepoRoot: repoRoot, RunID: runID, BaseHead: plan.baseHead}
	if err := g.recordRollbackNotice(ctx, agentID, runID, plan); err != nil {
		response.Warning = "rollback completed, but the conversation rollback notice could not be recorded: " + err.Error()
		slog.Warn("record rollback notice failed", "runId", runID, "agentId", agentID, "error", err)
	}
	status, err := g.statusForRepo(ctx, repoRoot, cwd)
	if err != nil {
		if response.Warning != "" {
			response.Warning += "; "
		}
		response.Warning += "rollback completed, but git status refresh failed: " + err.Error()
		slog.Warn("refresh git status after rollback failed", "runId", runID, "repoRoot", repoRoot, "error", err)
	} else {
		response.Status = &status
	}
	return response, nil
}

func (g gitService) commit(ctx context.Context, agentID string, req gitCommitRequest) (gitCommitResponse, error) {
	_, repoRoot, err := g.resolveAgentRepo(ctx, agentID)
	if err != nil {
		return gitCommitResponse{}, err
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return gitCommitResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "commit message is required"}
	}
	if len(message) > gitCommitMessageMaxBytes {
		return gitCommitResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "commit message is too long"}
	}
	paths, err := cleanGitCommitPaths(repoRoot, req.Paths)
	if err != nil {
		return gitCommitResponse{}, err
	}
	for _, path := range paths {
		if isSensitiveGitPath(path) {
			return gitCommitResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "refusing to commit sensitive-looking path: " + path}
		}
	}
	unlockGitMutation := gitlock.Default.Lock(repoRoot)
	defer unlockGitMutation()
	statusOut, _, err := runGitCommand(ctx, repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return gitCommitResponse{}, err
	}
	statusFiles := parseGitPorcelainStatus(statusOut)
	if err := validateGitCommitSelection(statusFiles, paths); err != nil {
		return gitCommitResponse{}, err
	}
	commitPaths := expandGitCommitPaths(statusFiles, paths)
	for _, path := range commitPaths {
		if isSensitiveGitPath(path) {
			return gitCommitResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "refusing to commit sensitive-looking path: " + path}
		}
	}
	addArgs := append([]string{"add", "--"}, commitPaths...)
	if _, _, err := runGitCommand(ctx, repoRoot, gitCommitOutputMaxBytes, 10*time.Second, nil, addArgs...); err != nil {
		return gitCommitResponse{}, err
	}
	diffArgs := append([]string{"diff", "--cached", "--name-only", "-z", "--"}, commitPaths...)
	stagedOut, _, err := runGitCommand(ctx, repoRoot, gitCommitOutputMaxBytes, 5*time.Second, nil, diffArgs...)
	if err != nil {
		return gitCommitResponse{}, err
	}
	stagedPaths := parseGitPathList(stagedOut)
	if len(stagedPaths) == 0 {
		return gitCommitResponse{}, gitCommandError{Status: http.StatusConflict, Msg: "no staged changes for selected paths"}
	}
	commitArgs := append([]string{"commit", "-m", message, "--"}, commitPaths...)
	if _, _, err := runGitCommand(ctx, repoRoot, gitCommitOutputMaxBytes, 20*time.Second, nil, commitArgs...); err != nil {
		return gitCommitResponse{}, normalizeGitCommitError(err)
	}
	format := "%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x00%x1e"
	logOut, logTruncated, err := runGitCommand(ctx, repoRoot, gitLogMaxBytes, 3*time.Second, nil, "log", "-1", "--date=iso-strict", "--pretty=format:"+format)
	if err != nil {
		return gitCommitResponse{}, err
	}
	commits := parseGitLog(logOut)
	if len(commits) == 0 {
		return gitCommitResponse{}, gitCommandError{Status: http.StatusInternalServerError, Msg: "commit succeeded but new commit could not be read"}
	}
	remainingOut, remainingTruncated, _ := runGitCommand(ctx, repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	return gitCommitResponse{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		RepoRoot:       repoRoot,
		Commit:         commits[0],
		Paths:          paths,
		RemainingFiles: parseGitPorcelainStatus(remainingOut),
		Truncated:      logTruncated || remainingTruncated,
	}, nil
}

func (g gitService) discard(ctx context.Context, agentID string, req gitDiscardRequest) (gitDiscardResponse, error) {
	cwd, repoRoot, err := g.resolveAgentRepo(ctx, agentID)
	if err != nil {
		return gitDiscardResponse{}, err
	}
	if !req.Confirm {
		return gitDiscardResponse{}, gitCommandError{Status: http.StatusBadRequest, Msg: "confirm must be true"}
	}
	paths, err := cleanGitCommitPaths(repoRoot, req.Paths)
	if err != nil {
		return gitDiscardResponse{}, err
	}
	unlockGitMutation := gitlock.Default.Lock(repoRoot)
	defer unlockGitMutation()
	statusOut, _, err := runGitCommand(ctx, repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return gitDiscardResponse{}, err
	}
	plan, err := buildGitDiscardPlan(parseGitPorcelainStatus(statusOut), paths)
	if err != nil {
		return gitDiscardResponse{}, err
	}
	hasHead := gitRepoHasHead(ctx, repoRoot)
	if !hasHead && len(plan.restoreFromHead) > 0 {
		return gitDiscardResponse{}, gitCommandError{Status: http.StatusConflict, Msg: "repository has no commits to restore from"}
	}
	if len(plan.unstageOnly) > 0 {
		if hasHead {
			args := append([]string{"restore", "--staged", "--source", "HEAD", "--"}, plan.unstageOnly...)
			if _, _, err := runGitCommand(ctx, repoRoot, gitCommitOutputMaxBytes, 10*time.Second, nil, args...); err != nil {
				return gitDiscardResponse{}, err
			}
		} else {
			args := append([]string{"rm", "--cached", "--force", "-q", "--"}, plan.unstageOnly...)
			if _, _, err := runGitCommand(ctx, repoRoot, gitCommitOutputMaxBytes, 10*time.Second, nil, args...); err != nil {
				return gitDiscardResponse{}, err
			}
		}
	}
	for _, path := range plan.deleteFiles {
		if err := removeScopedRunFile(repoRoot, path); err != nil {
			return gitDiscardResponse{}, gitCommandError{Status: http.StatusInternalServerError, Msg: "discard stopped after a file could not be removed; remaining selections were left untouched: " + path + ": " + err.Error()}
		}
	}
	if len(plan.restoreFromHead) > 0 {
		args := append([]string{"restore", "--staged", "--worktree", "--source", "HEAD", "--"}, plan.restoreFromHead...)
		if _, _, err := runGitCommand(ctx, repoRoot, gitCommitOutputMaxBytes, 15*time.Second, nil, args...); err != nil {
			return gitDiscardResponse{}, err
		}
	}
	response := gitDiscardResponse{
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		RepoRoot:      repoRoot,
		RestoredPaths: plan.restoreFromHead,
		DeletedPaths:  plan.deleteFiles,
	}
	status, err := g.statusForRepo(ctx, repoRoot, cwd)
	if err != nil {
		response.Warning = "discard completed, but git status refresh failed: " + err.Error()
	} else {
		response.Status = &status
	}
	return response, nil
}

func (g gitService) statusForRepo(ctx context.Context, repoRoot, cwd string) (gitStatusResponse, error) {
	statusOut, truncated, err := runGitCommand(ctx, repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return gitStatusResponse{}, err
	}
	files := parseGitPorcelainStatus(statusOut)
	head, _, _ := runGitCommand(ctx, repoRoot, 256, 2*time.Second, nil, "rev-parse", "--short", "HEAD")
	branch, _, _ := runGitCommand(ctx, repoRoot, 512, 2*time.Second, nil, "branch", "--show-current")
	upstream, _, _ := runGitCommand(ctx, repoRoot, 512, 2*time.Second, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	ahead, behind := 0, 0
	if strings.TrimSpace(upstream) != "" {
		counts, _, err := runGitCommand(ctx, repoRoot, 128, 2*time.Second, nil, "rev-list", "--left-right", "--count", "HEAD...@{u}")
		if err == nil {
			ahead, behind = parseAheadBehind(counts)
		}
	}
	return gitStatusResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), CWD: cwd, RepoRoot: repoRoot, Head: strings.TrimSpace(head), Branch: strings.TrimSpace(branch), Upstream: strings.TrimSpace(upstream), Ahead: ahead, Behind: behind, Clean: len(files) == 0, Files: files, Truncated: truncated}, nil
}

func (g gitService) recordRollbackNotice(ctx context.Context, agentID, runID string, plan gitRollbackPlan) error {
	text := rollbackNoticeText(plan.baseHead, plan.restorePaths, plan.deletePaths)
	// CreatedBy stays empty: the column references users(id) and this notice is
	// server-generated, not attributable to any account.
	message, err := g.store.AddMessage(ctx, db.Message{
		AgentID:     agentID,
		RunID:       runID,
		Role:        "system",
		ContentText: text,
	})
	if err != nil {
		return err
	}
	if g.hub != nil {
		g.hub.Publish(agentpkg.Event{Type: "message.created", AgentID: agentID, MessageID: message.ID, Text: text, Data: map[string]any{"runId": runID, "rollback": true}})
	}
	return nil
}

func (g gitService) failRollbackAfterClaim(ctx context.Context, runID, reason string, status int) error {
	if err := g.store.FailRunGitRollback(ctx, runID, reason); err != nil {
		return gitCommandError{Status: http.StatusInternalServerError, Msg: reason + "; checkpoint remains rolling_back because failure state could not be persisted: " + err.Error()}
	}
	return gitCommandError{Status: status, Msg: reason}
}

func (g gitService) buildRollbackPlan(ctx context.Context, repoRoot string, run db.Run, expectedState string) (gitRollbackPlan, error) {
	plan := gitRollbackPlan{}
	if run.CheckpointState != expectedState {
		plan.reason = rollbackCheckpointStateReason(run)
		return plan, nil
	}
	plan.baseHead = strings.TrimSpace(run.BaseHead)
	if plan.baseHead == "" {
		plan.reason = "run has no clean-start checkpoint"
		return plan, nil
	}
	if strings.TrimSpace(run.CheckpointRepoRoot) == "" || strings.TrimSpace(run.GitSnapshotAt) == "" {
		plan.reason = "run has no completed scoped file checkpoint"
		return plan, nil
	}
	if canonicalPath(run.CheckpointRepoRoot) != canonicalPath(repoRoot) {
		plan.reason = "run checkpoint belongs to a different git repository"
		return plan, nil
	}
	if endHead := strings.TrimSpace(run.EndHead); endHead != "" && endHead != plan.baseHead {
		plan.reason = "run changed HEAD; refusing rollback across commits"
		return plan, nil
	}
	currentHead, truncated, err := runGitCommand(ctx, repoRoot, 256, 3*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return plan, err
	}
	if truncated || strings.TrimSpace(currentHead) != plan.baseHead {
		plan.reason = "current HEAD differs from run checkpoint"
		return plan, nil
	}
	changes, err := g.store.ListRunGitChanges(ctx, run.ID)
	if err != nil {
		return plan, err
	}
	if len(changes) == 0 {
		plan.reason = "run checkpoint has no owned git changes to roll back"
		return plan, nil
	}
	if err := verifyRunGitChanges(ctx, repoRoot, changes); err != nil {
		return plan, err
	}
	plan.changes = append([]db.RunGitChange{}, changes...)
	for _, change := range changes {
		path, err := cleanGitPath(repoRoot, change.Path)
		if err != nil || path == "" || change.OrigPath != "" {
			return plan, gitCommandError{Status: http.StatusConflict, Msg: "run checkpoint contains an invalid or legacy rename path"}
		}
		if change.Untracked {
			plan.deletePaths = append(plan.deletePaths, path)
		} else {
			plan.restorePaths = append(plan.restorePaths, path)
		}
	}
	sort.Strings(plan.restorePaths)
	sort.Strings(plan.deletePaths)
	plan.available = true
	plan.reason = "verified run-owned changes are ready to roll back"
	return plan, nil
}

func (g gitService) resolveAgentRepo(ctx context.Context, agentID string) (string, string, error) {
	agent, err := g.store.GetAgent(ctx, agentID)
	if err != nil {
		return "", "", err
	}
	if err := requireLocalExecutionAgent(agent); err != nil {
		return "", "", gitCommandError{Status: http.StatusConflict, Msg: "remote execution transport is disabled; local fallback is forbidden"}
	}
	cwd := strings.TrimSpace(agent.CWD)
	if cwd == "" && agent.WorklineID != "" {
		workline, err := g.store.GetWorkline(ctx, agent.WorklineID)
		if err == nil {
			cwd = strings.TrimSpace(workline.WorktreePath)
		}
	}
	if cwd == "" {
		return "", "", gitCommandError{Status: http.StatusBadRequest, Msg: "agent cwd is not configured"}
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", gitCommandError{Status: http.StatusBadRequest, Msg: "agent cwd must be a directory"}
	}
	repoRoot, _, err := runGitCommand(ctx, cwd, 4096, 3*time.Second, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if err := g.validateRepoBoundary(ctx, agent, repoRoot); err != nil {
		return "", "", err
	}
	return cwd, repoRoot, nil
}

func (g gitService) validateRepoBoundary(ctx context.Context, agent db.Agent, repoRoot string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return gitCommandError{Status: http.StatusConflict, Msg: "git repository root is not configured"}
	}
	allowedRoots := make([]string, 0, 2)
	if agent.WorklineID != "" {
		if workline, err := g.store.GetWorkline(ctx, agent.WorklineID); err == nil {
			if strings.TrimSpace(workline.WorktreePath) != "" {
				allowedRoots = append(allowedRoots, workline.WorktreePath)
			}
			if project, err := g.store.GetProject(ctx, workline.ProjectID); err == nil && strings.TrimSpace(project.GitPath) != "" {
				allowedRoots = append(allowedRoots, project.GitPath)
			}
		}
	}
	if g.defaultProjectDir != "" {
		allowedRoots = append(allowedRoots, g.defaultProjectDir)
	}
	for _, root := range allowedRoots {
		if pathWithin(root, repoRoot) {
			return nil
		}
	}
	return gitCommandError{Status: http.StatusForbidden, Msg: "git repository is outside the configured project boundary"}
}
