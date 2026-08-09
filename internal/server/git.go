package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/gitlock"
	"autoto/internal/gitsnapshot"
	"autoto/internal/process"
)

const (
	gitStatusMaxBytes          = 1 << 20
	gitDiffMaxBytes            = 2 << 20
	gitLogMaxBytes             = 512 << 10
	gitCommitOutputMaxBytes    = 512 << 10
	gitCommitMessageMaxBytes   = 10 << 10
	gitCommitMaxPaths          = 200
	gitRollbackPreviewMaxPaths = 20
)

type gitStatusResponse struct {
	GeneratedAt string          `json:"generatedAt"`
	CWD         string          `json:"cwd"`
	RepoRoot    string          `json:"repoRoot"`
	Head        string          `json:"head,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	Upstream    string          `json:"upstream,omitempty"`
	Ahead       int             `json:"ahead,omitempty"`
	Behind      int             `json:"behind,omitempty"`
	Clean       bool            `json:"clean"`
	Files       []gitStatusFile `json:"files"`
	Truncated   bool            `json:"truncated,omitempty"`
}

type gitStatusFile struct {
	Path      string `json:"path"`
	OrigPath  string `json:"origPath,omitempty"`
	Index     string `json:"index"`
	Worktree  string `json:"worktree"`
	Staged    bool   `json:"staged"`
	Unstaged  bool   `json:"unstaged"`
	Untracked bool   `json:"untracked"`
	Renamed   bool   `json:"renamed"`
}

type gitDiffResponse struct {
	GeneratedAt string        `json:"generatedAt"`
	RepoRoot    string        `json:"repoRoot"`
	Scope       string        `json:"scope"`
	Path        string        `json:"path,omitempty"`
	Patch       string        `json:"patch"`
	Files       []gitDiffFile `json:"files"`
	Truncated   bool          `json:"truncated,omitempty"`
}

type gitDiffFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary,omitempty"`
}

type gitLogResponse struct {
	GeneratedAt string      `json:"generatedAt"`
	RepoRoot    string      `json:"repoRoot"`
	Commits     []gitCommit `json:"commits"`
	Truncated   bool        `json:"truncated,omitempty"`
}

type gitCommitRequest struct {
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}

type gitRollbackRequest struct {
	Confirm bool `json:"confirm"`
}

type gitRollbackResponse struct {
	GeneratedAt string             `json:"generatedAt"`
	RepoRoot    string             `json:"repoRoot"`
	RunID       string             `json:"runId"`
	BaseHead    string             `json:"baseHead"`
	Status      *gitStatusResponse `json:"status,omitempty"`
	Warning     string             `json:"warning,omitempty"`
}

type gitRollbackPreviewResponse struct {
	GeneratedAt  string   `json:"generatedAt"`
	RepoRoot     string   `json:"repoRoot"`
	RunID        string   `json:"runId"`
	Available    bool     `json:"available"`
	Reason       string   `json:"reason,omitempty"`
	RestorePaths []string `json:"restorePaths"`
	DeletePaths  []string `json:"deletePaths"`
	RestoreCount int      `json:"restoreCount"`
	DeleteCount  int      `json:"deleteCount"`
	Truncated    bool     `json:"truncated,omitempty"`
}

type gitRollbackPlan struct {
	available    bool
	reason       string
	baseHead     string
	changes      []db.RunGitChange
	restorePaths []string
	deletePaths  []string
}

type gitCommitResponse struct {
	GeneratedAt    string          `json:"generatedAt"`
	RepoRoot       string          `json:"repoRoot"`
	Commit         gitCommit       `json:"commit"`
	Paths          []string        `json:"paths"`
	RemainingFiles []gitStatusFile `json:"remainingFiles"`
	Truncated      bool            `json:"truncated,omitempty"`
}

type gitCommit struct {
	Hash        string `json:"hash"`
	ShortHash   string `json:"shortHash"`
	AuthorName  string `json:"authorName"`
	AuthorEmail string `json:"authorEmail"`
	Date        string `json:"date"`
	Subject     string `json:"subject"`
}

type gitCommandError struct {
	Status int
	Msg    string
}

func (e gitCommandError) Error() string { return e.Msg }

func (s *Server) gitStatus(w http.ResponseWriter, r *http.Request) {
	cwd, repoRoot, err := s.resolveAgentGitRepo(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeGitError(w, err)
		return
	}
	status, err := s.gitStatusForRepo(r.Context(), repoRoot, cwd)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) gitStatusForRepo(ctx context.Context, repoRoot, cwd string) (gitStatusResponse, error) {
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

func (s *Server) gitDiff(w http.ResponseWriter, r *http.Request) {
	_, repoRoot, err := s.resolveAgentGitRepo(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeGitError(w, err)
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "staged" && scope != "unstaged" {
		writeError(w, http.StatusBadRequest, "invalid git diff scope")
		return
	}
	relPath, err := cleanGitPath(repoRoot, r.URL.Query().Get("path"))
	if err != nil {
		writeGitError(w, err)
		return
	}
	contextLines := boundedInt(r.URL.Query().Get("context"), 3, 0, 20)
	hasHead := true
	if scope == "all" {
		hasHead = gitRepoHasHead(r.Context(), repoRoot)
	}
	patchArgs := gitDiffArgs(scope, contextLines, false, relPath, hasHead)
	patch, truncated, err := runGitCommand(r.Context(), repoRoot, gitDiffMaxBytes, 5*time.Second, nil, patchArgs...)
	if err != nil {
		writeGitError(w, err)
		return
	}
	statArgs := gitDiffArgs(scope, contextLines, true, relPath, hasHead)
	statOut, statTruncated, _ := runGitCommand(r.Context(), repoRoot, gitLogMaxBytes, 5*time.Second, nil, statArgs...)
	writeJSON(w, http.StatusOK, gitDiffResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		RepoRoot:    repoRoot,
		Scope:       scope,
		Path:        relPath,
		Patch:       safeUTF8(patch),
		Files:       parseGitNumstat(statOut),
		Truncated:   truncated || statTruncated,
	})
}

func (s *Server) gitLog(w http.ResponseWriter, r *http.Request) {
	_, repoRoot, err := s.resolveAgentGitRepo(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeGitError(w, err)
		return
	}
	limit := boundedInt(r.URL.Query().Get("limit"), 30, 1, 100)
	format := "%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x00%x1e"
	out, truncated, err := runGitCommand(r.Context(), repoRoot, gitLogMaxBytes, 3*time.Second, nil, "log", "--max-count="+strconv.Itoa(limit), "--date=iso-strict", "--pretty=format:"+format)
	if err != nil {
		writeGitError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gitLogResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), RepoRoot: repoRoot, Commits: parseGitLog(out), Truncated: truncated})
}

func (s *Server) rollbackRunPreview(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "runId")
	_, repoRoot, err := s.resolveAgentGitRepo(r.Context(), agentID)
	if err != nil {
		writeGitError(w, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), agentID, runID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	plan, err := s.buildRollbackPlan(r.Context(), repoRoot, run, db.RunCheckpointReady)
	if err != nil {
		plan = gitRollbackPlan{reason: err.Error()}
	}
	writeJSON(w, http.StatusOK, gitRollbackPreview(repoRoot, runID, plan))
}

func (s *Server) rollbackRun(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "runId")
	cwd, repoRoot, err := s.resolveAgentGitRepo(r.Context(), agentID)
	if err != nil {
		writeGitError(w, err)
		return
	}
	var req gitRollbackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "confirm must be true")
		return
	}

	unlockGitMutation := gitlock.Default.Lock(repoRoot)
	defer unlockGitMutation()
	run, err := s.store.GetRun(r.Context(), agentID, runID)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	plan, err := s.buildRollbackPlan(r.Context(), repoRoot, run, db.RunCheckpointReady)
	if err != nil {
		writeGitError(w, err)
		return
	}
	if !plan.available {
		writeGitError(w, gitCommandError{Status: http.StatusConflict, Msg: plan.reason})
		return
	}
	if err := s.store.ClaimRunGitRollback(r.Context(), runID); err != nil {
		writeGitError(w, gitCommandError{Status: http.StatusConflict, Msg: "run rollback is no longer available: " + err.Error()})
		return
	}

	run, err = s.store.GetRun(r.Context(), agentID, runID)
	if err != nil {
		writeGitError(w, s.failRollbackAfterClaim(r.Context(), runID, "reload run after rollback claim failed: "+err.Error(), http.StatusInternalServerError))
		return
	}
	plan, err = s.buildRollbackPlan(r.Context(), repoRoot, run, db.RunCheckpointRollingBack)
	if err != nil || !plan.available {
		reason := "rollback verification failed after claim"
		if err != nil {
			reason += ": " + err.Error()
		} else if plan.reason != "" {
			reason += ": " + plan.reason
		}
		writeGitError(w, s.failRollbackAfterClaim(r.Context(), runID, reason, http.StatusConflict))
		return
	}
	if err := restoreRunGitChanges(r.Context(), repoRoot, plan.baseHead, plan.changes); err != nil {
		writeGitError(w, s.failRollbackAfterClaim(r.Context(), runID, "rollback file operations failed: "+err.Error(), http.StatusInternalServerError))
		return
	}
	if err := s.store.MarkRunGitCheckpointRolledBack(r.Context(), runID); err != nil {
		writeGitError(w, s.failRollbackAfterClaim(r.Context(), runID, "rollback file operations completed, but checkpoint state could not be marked rolled back: "+err.Error(), http.StatusInternalServerError))
		return
	}
	response := gitRollbackResponse{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), RepoRoot: repoRoot, RunID: runID, BaseHead: plan.baseHead}
	status, err := s.gitStatusForRepo(r.Context(), repoRoot, cwd)
	if err != nil {
		response.Warning = "rollback completed, but git status refresh failed: " + err.Error()
		slog.Warn("refresh git status after rollback failed", "runId", runID, "repoRoot", repoRoot, "error", err)
	} else {
		response.Status = &status
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) failRollbackAfterClaim(ctx context.Context, runID, reason string, status int) error {
	if err := s.store.FailRunGitRollback(ctx, runID, reason); err != nil {
		return gitCommandError{Status: http.StatusInternalServerError, Msg: reason + "; checkpoint remains rolling_back because failure state could not be persisted: " + err.Error()}
	}
	return gitCommandError{Status: status, Msg: reason}
}

func (s *Server) buildRollbackPlan(ctx context.Context, repoRoot string, run db.Run, expectedState string) (gitRollbackPlan, error) {
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
	changes, err := s.store.ListRunGitChanges(ctx, run.ID)
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

func rollbackCheckpointStateReason(run db.Run) string {
	switch run.CheckpointState {
	case db.RunCheckpointRolledBack:
		return "run checkpoint was already rolled back"
	case db.RunCheckpointRollingBack:
		return "run rollback is already in progress"
	case db.RunCheckpointInvalid:
		return "run checkpoint is invalid: " + strings.TrimSpace(run.CheckpointError)
	case db.RunCheckpointCapturing:
		return "run checkpoint is still capturing tool changes"
	case db.RunCheckpointTracking:
		return "run checkpoint is still tracking and is not ready for rollback"
	case db.RunCheckpointNone:
		return "run has no completed scoped file checkpoint"
	default:
		return "run checkpoint is in an unknown state and cannot be rolled back"
	}
}

func gitRollbackPreview(repoRoot, runID string, plan gitRollbackPlan) gitRollbackPreviewResponse {
	restorePaths, restoreTruncated := truncateRollbackPaths(plan.restorePaths)
	deletePaths, deleteTruncated := truncateRollbackPaths(plan.deletePaths)
	return gitRollbackPreviewResponse{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		RepoRoot:     repoRoot,
		RunID:        runID,
		Available:    plan.available,
		Reason:       plan.reason,
		RestorePaths: restorePaths,
		DeletePaths:  deletePaths,
		RestoreCount: len(plan.restorePaths),
		DeleteCount:  len(plan.deletePaths),
		Truncated:    restoreTruncated || deleteTruncated,
	}
}

func truncateRollbackPaths(paths []string) ([]string, bool) {
	if len(paths) <= gitRollbackPreviewMaxPaths {
		return append([]string{}, paths...), false
	}
	return append([]string{}, paths[:gitRollbackPreviewMaxPaths]...), true
}

func verifyRunGitChanges(ctx context.Context, repoRoot string, changes []db.RunGitChange) error {
	statusOut, truncated, err := runGitCommand(ctx, repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return err
	}
	if truncated {
		return gitCommandError{Status: http.StatusConflict, Msg: "git status output exceeded rollback verification limit"}
	}
	entries, err := gitsnapshot.ParsePorcelainV1NoRenames(statusOut)
	if err != nil {
		return gitCommandError{Status: http.StatusConflict, Msg: "could not parse current git status for rollback"}
	}
	current := make(map[string]gitsnapshot.StatusEntry, len(entries))
	for _, entry := range entries {
		current[entry.Path] = entry
	}
	for _, change := range changes {
		path, err := cleanGitPath(repoRoot, change.Path)
		if err != nil || path == "" || change.OrigPath != "" {
			return gitCommandError{Status: http.StatusConflict, Msg: "run checkpoint contains an invalid or legacy rename path"}
		}
		entry, ok := current[path]
		if !ok || !runGitChangeMatchesStatus(change, entry) {
			return gitCommandError{Status: http.StatusConflict, Msg: "run rollback refused because path changed after the run completed: " + path}
		}
		indexFingerprint, err := gitRunIndexFingerprint(ctx, repoRoot, path)
		if err != nil {
			return err
		}
		if indexFingerprint != change.IndexFingerprint {
			return gitCommandError{Status: http.StatusConflict, Msg: "run rollback refused because the staged state changed after the run completed: " + path}
		}
		worktreeFingerprint, err := gitsnapshot.WorktreeFingerprint(repoRoot, path)
		if err != nil {
			return gitCommandError{Status: http.StatusConflict, Msg: "run rollback could not fingerprint checkpoint path: " + path}
		}
		if worktreeFingerprint != change.WorktreeFingerprint {
			return gitCommandError{Status: http.StatusConflict, Msg: "run rollback refused because file contents or mode changed after the run completed: " + path}
		}
	}
	return nil
}

func runGitChangeMatchesStatus(change db.RunGitChange, entry gitsnapshot.StatusEntry) bool {
	return change.Path == entry.Path && change.IndexStatus == entry.IndexStatus && change.WorktreeStatus == entry.WorktreeStatus && change.Untracked == entry.Untracked
}

func gitRunIndexFingerprint(ctx context.Context, repoRoot, path string) (string, error) {
	out, truncated, err := runGitCommand(ctx, repoRoot, 16<<10, 3*time.Second, nil, "ls-files", "-s", "-z", "--", path)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", gitCommandError{Status: http.StatusConflict, Msg: "git index output exceeded rollback verification limit"}
	}
	return gitsnapshot.IndexFingerprint(out), nil
}

func restoreRunGitChanges(ctx context.Context, repoRoot, baseHead string, changes []db.RunGitChange) error {
	trackedPaths := make([]string, 0, len(changes))
	untrackedPaths := make([]string, 0, len(changes))
	for _, change := range changes {
		path, err := cleanGitPath(repoRoot, change.Path)
		if err != nil || path == "" || change.OrigPath != "" {
			return gitCommandError{Status: http.StatusConflict, Msg: "run checkpoint contains an invalid or legacy rename path"}
		}
		if change.Untracked {
			untrackedPaths = append(untrackedPaths, path)
			continue
		}
		trackedPaths = append(trackedPaths, path)
	}
	sort.Strings(trackedPaths)
	sort.Strings(untrackedPaths)
	if len(trackedPaths) > 0 {
		args := append([]string{"restore", "--source", baseHead, "--staged", "--worktree", "--"}, trackedPaths...)
		if _, _, err := runGitCommand(ctx, repoRoot, gitCommitOutputMaxBytes, 10*time.Second, nil, args...); err != nil {
			return err
		}
	}
	for _, path := range untrackedPaths {
		if err := removeScopedRunFile(repoRoot, path); err != nil {
			return gitCommandError{Status: http.StatusInternalServerError, Msg: "tracked changes were restored, but a verified run-created untracked file could not be removed; no further files were removed: " + path + ": " + err.Error()}
		}
	}
	return nil
}

func removeScopedRunFile(repoRoot, path string) error {
	path, err := cleanGitPath(repoRoot, path)
	if err != nil || path == "" {
		return gitCommandError{Status: http.StatusConflict, Msg: "run checkpoint contains an invalid path"}
	}
	absolute, err := gitsnapshot.Path(repoRoot, path)
	if err != nil {
		return gitCommandError{Status: http.StatusConflict, Msg: "run checkpoint contains an invalid path"}
	}
	info, err := os.Lstat(absolute)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return gitCommandError{Status: http.StatusConflict, Msg: "refusing to delete non-file run checkpoint path: " + path}
	}
	if err := os.Remove(absolute); err != nil {
		return err
	}
	return nil
}

func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	_, repoRoot, err := s.resolveAgentGitRepo(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeGitError(w, err)
		return
	}
	var req gitCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "commit message is required")
		return
	}
	if len(message) > gitCommitMessageMaxBytes {
		writeError(w, http.StatusBadRequest, "commit message is too long")
		return
	}
	paths, err := cleanGitCommitPaths(repoRoot, req.Paths)
	if err != nil {
		writeGitError(w, err)
		return
	}
	for _, path := range paths {
		if isSensitiveGitPath(path) {
			writeError(w, http.StatusBadRequest, "refusing to commit sensitive-looking path: "+path)
			return
		}
	}
	unlockGitMutation := gitlock.Default.Lock(repoRoot)
	defer unlockGitMutation()
	statusOut, _, err := runGitCommand(r.Context(), repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		writeGitError(w, err)
		return
	}
	statusFiles := parseGitPorcelainStatus(statusOut)
	if err := validateGitCommitSelection(statusFiles, paths); err != nil {
		writeGitError(w, err)
		return
	}
	commitPaths := expandGitCommitPaths(statusFiles, paths)
	for _, path := range commitPaths {
		if isSensitiveGitPath(path) {
			writeError(w, http.StatusBadRequest, "refusing to commit sensitive-looking path: "+path)
			return
		}
	}
	addArgs := append([]string{"add", "--"}, commitPaths...)
	if _, _, err := runGitCommand(r.Context(), repoRoot, gitCommitOutputMaxBytes, 10*time.Second, nil, addArgs...); err != nil {
		writeGitError(w, err)
		return
	}
	diffArgs := append([]string{"diff", "--cached", "--name-only", "-z", "--"}, commitPaths...)
	stagedOut, _, err := runGitCommand(r.Context(), repoRoot, gitCommitOutputMaxBytes, 5*time.Second, nil, diffArgs...)
	if err != nil {
		writeGitError(w, err)
		return
	}
	stagedPaths := parseGitPathList(stagedOut)
	if len(stagedPaths) == 0 {
		writeGitError(w, gitCommandError{Status: http.StatusConflict, Msg: "no staged changes for selected paths"})
		return
	}
	commitArgs := append([]string{"commit", "-m", message, "--"}, commitPaths...)
	if _, _, err := runGitCommand(r.Context(), repoRoot, gitCommitOutputMaxBytes, 20*time.Second, nil, commitArgs...); err != nil {
		writeGitError(w, normalizeGitCommitError(err))
		return
	}
	format := "%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x00%x1e"
	logOut, logTruncated, err := runGitCommand(r.Context(), repoRoot, gitLogMaxBytes, 3*time.Second, nil, "log", "-1", "--date=iso-strict", "--pretty=format:"+format)
	if err != nil {
		writeGitError(w, err)
		return
	}
	commits := parseGitLog(logOut)
	if len(commits) == 0 {
		writeGitError(w, gitCommandError{Status: http.StatusInternalServerError, Msg: "commit succeeded but new commit could not be read"})
		return
	}
	remainingOut, remainingTruncated, _ := runGitCommand(r.Context(), repoRoot, gitStatusMaxBytes, 3*time.Second, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	writeJSON(w, http.StatusCreated, gitCommitResponse{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		RepoRoot:       repoRoot,
		Commit:         commits[0],
		Paths:          paths,
		RemainingFiles: parseGitPorcelainStatus(remainingOut),
		Truncated:      logTruncated || remainingTruncated,
	})
}

func (s *Server) resolveAgentGitRepo(ctx context.Context, agentID string) (string, string, error) {
	agent, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return "", "", err
	}
	if err := requireLocalExecutionAgent(agent); err != nil {
		return "", "", gitCommandError{Status: http.StatusConflict, Msg: "remote execution transport is disabled; local fallback is forbidden"}
	}
	cwd := strings.TrimSpace(agent.CWD)
	if cwd == "" && agent.WorklineID != "" {
		workline, err := s.store.GetWorkline(ctx, agent.WorklineID)
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
	if err := s.validateAgentGitRepoBoundary(ctx, agent, repoRoot); err != nil {
		return "", "", err
	}
	return cwd, repoRoot, nil
}

func (s *Server) validateAgentGitRepoBoundary(ctx context.Context, agent db.Agent, repoRoot string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return gitCommandError{Status: http.StatusConflict, Msg: "git repository root is not configured"}
	}
	allowedRoots := make([]string, 0, 2)
	if agent.WorklineID != "" {
		if workline, err := s.store.GetWorkline(ctx, agent.WorklineID); err == nil {
			if strings.TrimSpace(workline.WorktreePath) != "" {
				allowedRoots = append(allowedRoots, workline.WorktreePath)
			}
			if project, err := s.store.GetProject(ctx, workline.ProjectID); err == nil && strings.TrimSpace(project.GitPath) != "" {
				allowedRoots = append(allowedRoots, project.GitPath)
			}
		}
	}
	if defaultDir := strings.TrimSpace(s.configSnapshot().Paths.DefaultProjectDir); defaultDir != "" {
		allowedRoots = append(allowedRoots, defaultDir)
	}
	for _, root := range allowedRoots {
		if pathWithin(root, repoRoot) {
			return nil
		}
	}
	return gitCommandError{Status: http.StatusForbidden, Msg: "git repository is outside the configured project boundary"}
}

func pathWithin(root, path string) bool {
	root = strings.TrimSpace(root)
	path = strings.TrimSpace(path)
	if root == "" || path == "" {
		return false
	}
	root = canonicalPath(root)
	path = canonicalPath(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func cleanGitCommitPaths(repoRoot string, rawPaths []string) ([]string, error) {
	if len(rawPaths) == 0 {
		return nil, gitCommandError{Status: http.StatusBadRequest, Msg: "at least one git path is required"}
	}
	if len(rawPaths) > gitCommitMaxPaths {
		return nil, gitCommandError{Status: http.StatusBadRequest, Msg: "too many git paths selected"}
	}
	paths := make([]string, 0, len(rawPaths))
	seen := make(map[string]bool, len(rawPaths))
	for _, rawPath := range rawPaths {
		relPath, err := cleanGitPath(repoRoot, rawPath)
		if err != nil {
			return nil, err
		}
		if relPath == "" {
			return nil, gitCommandError{Status: http.StatusBadRequest, Msg: "git path is required"}
		}
		if seen[relPath] {
			continue
		}
		seen[relPath] = true
		paths = append(paths, relPath)
	}
	if len(paths) == 0 {
		return nil, gitCommandError{Status: http.StatusBadRequest, Msg: "at least one git path is required"}
	}
	return paths, nil
}

func validateGitCommitSelection(files []gitStatusFile, paths []string) error {
	matched := make(map[string]bool, len(paths))
	for _, file := range files {
		selected := false
		for _, path := range paths {
			if gitStatusFileMatchesPath(file, path) {
				matched[path] = true
				selected = true
			}
		}
		if file.Staged && !selected {
			return gitCommandError{Status: http.StatusConflict, Msg: "staged changes outside selected paths must be committed separately: " + file.Path}
		}
	}
	for _, path := range paths {
		if !matched[path] {
			return gitCommandError{Status: http.StatusConflict, Msg: "selected path has no worktree changes: " + path}
		}
	}
	return nil
}

func expandGitCommitPaths(files []gitStatusFile, paths []string) []string {
	expanded := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths)*2)
	appendPath := func(path string) {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		expanded = append(expanded, path)
	}
	for _, path := range paths {
		appendPath(path)
	}
	for _, file := range files {
		if file.OrigPath == "" || !gitStatusFileSelected(file, paths) {
			continue
		}
		appendPath(file.OrigPath)
		appendPath(file.Path)
	}
	return expanded
}

func gitStatusFileSelected(file gitStatusFile, paths []string) bool {
	for _, path := range paths {
		if gitStatusFileMatchesPath(file, path) {
			return true
		}
	}
	return false
}

func gitStatusFileMatchesPath(file gitStatusFile, path string) bool {
	return gitPathMatchesSelection(file.Path, path) || (file.OrigPath != "" && gitPathMatchesSelection(file.OrigPath, path))
}

func gitPathMatchesSelection(filePath, selectedPath string) bool {
	filePath = filepath.ToSlash(strings.TrimSpace(filePath))
	selectedPath = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(selectedPath)), "/")
	return filePath == selectedPath || strings.HasPrefix(filePath, selectedPath+"/")
}

func parseGitPathList(out string) []string {
	parts := strings.Split(out, "\x00")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		paths = append(paths, filepath.ToSlash(part))
	}
	return paths
}

func isSensitiveGitPath(path string) bool {
	base := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
	if base == "" || base == "." {
		return false
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return !hasAnySuffix(base, ".example", ".sample", ".template", ".dist")
	}
	sensitiveNames := map[string]bool{
		".netrc":                 true,
		".npmrc":                 true,
		".pypirc":                true,
		"credentials.json":       true,
		"client_secret.json":     true,
		"secret.json":            true,
		"secrets.json":           true,
		"id_rsa":                 true,
		"id_dsa":                 true,
		"id_ecdsa":               true,
		"id_ed25519":             true,
		"known_hosts.old":        true,
		"service-account.json":   true,
		"service_account.json":   true,
		"firebase-adminsdk.json": true,
	}
	if sensitiveNames[base] {
		return true
	}
	if strings.HasPrefix(base, "service-account") && strings.HasSuffix(base, ".json") {
		return true
	}
	if strings.HasPrefix(base, "service_account") && strings.HasSuffix(base, ".json") {
		return true
	}
	if strings.HasSuffix(base, "-credentials.json") || strings.HasSuffix(base, "_credentials.json") {
		return true
	}
	switch filepath.Ext(base) {
	case ".key", ".pem", ".p12", ".pfx":
		return true
	}
	return false
}

func hasAnySuffix(value string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func normalizeGitCommitError(err error) error {
	var gitErr gitCommandError
	if !errors.As(err, &gitErr) {
		return err
	}
	lower := strings.ToLower(gitErr.Msg)
	if strings.Contains(lower, "nothing to commit") || strings.Contains(lower, "no changes added to commit") || strings.Contains(lower, "no changes added") {
		gitErr.Status = http.StatusConflict
	}
	return gitErr
}

func gitDiffArgs(scope string, contextLines int, numstat bool, relPath string, hasHead bool) []string {
	args := []string{"diff", "--no-ext-diff", "--find-renames"}
	if numstat {
		args = append(args, "--numstat", "-z")
	} else {
		args = append(args, "--unified="+strconv.Itoa(contextLines))
	}
	switch scope {
	case "all":
		if hasHead {
			args = append(args, "HEAD")
		}
	case "staged":
		args = append(args, "--cached")
	}
	args = append(args, "--")
	if relPath != "" {
		args = append(args, relPath)
	}
	return args
}

func gitRepoHasHead(ctx context.Context, repoRoot string) bool {
	out, _, err := runGitCommand(ctx, repoRoot, 128, 2*time.Second, []int{1, 128}, "rev-parse", "--verify", "HEAD")
	return err == nil && strings.TrimSpace(out) != ""
}

func cleanGitPath(repoRoot, input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", nil
	}
	var abs string
	if filepath.IsAbs(trimmed) {
		abs = filepath.Clean(trimmed)
	} else {
		cleaned := filepath.Clean(trimmed)
		if cleaned == "." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
			return "", gitCommandError{Status: http.StatusBadRequest, Msg: "git path escapes repository"}
		}
		abs = filepath.Join(repoRoot, cleaned)
	}
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", gitCommandError{Status: http.StatusBadRequest, Msg: err.Error()}
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", gitCommandError{Status: http.StatusBadRequest, Msg: "git path escapes repository"}
	}
	return filepath.ToSlash(rel), nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if w.max <= 0 {
		return len(p), nil
	}
	remaining := w.max - w.buf.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = w.buf.Write(p)
		} else {
			_, _ = w.buf.Write(p[:remaining])
			w.truncated = true
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func runGitCommand(parent context.Context, dir string, maxBytes int, timeout time.Duration, allowedExitCodes []int, args ...string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	// Git status and diff refresh often while a task runs; each one would flash a
	// console window on Windows without this.
	process.HideWindow(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_EXTERNAL_DIFF=")
	stdout := &limitedBuffer{max: maxBytes}
	stderr := &limitedBuffer{max: 64 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.buf.String(), stdout.truncated, gitCommandError{Status: http.StatusGatewayTimeout, Msg: "git command timed out"}
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", false, gitCommandError{Status: http.StatusServiceUnavailable, Msg: "git executable not found"}
		}
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
			for _, allowed := range allowedExitCodes {
				if exitCode == allowed {
					return stdout.buf.String(), stdout.truncated, nil
				}
			}
		}
		msg := strings.TrimSpace(stderr.buf.String())
		if msg == "" {
			msg = err.Error()
		}
		status := http.StatusInternalServerError
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "not a git repository") || strings.Contains(lower, "not a git repo") {
			status = http.StatusConflict
		}
		if strings.Contains(lower, "unknown revision") || strings.Contains(lower, "ambiguous argument") || strings.Contains(lower, "bad revision") {
			status = http.StatusBadRequest
		}
		return stdout.buf.String(), stdout.truncated, gitCommandError{Status: status, Msg: msg}
	}
	return stdout.buf.String(), stdout.truncated, nil
}

func parseGitPorcelainStatus(out string) []gitStatusFile {
	parts := strings.Split(out, "\x00")
	files := make([]gitStatusFile, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		entry := parts[i]
		if len(entry) < 4 {
			continue
		}
		index := string(entry[0])
		worktree := string(entry[1])
		path := entry[3:]
		file := gitStatusFile{Path: filepath.ToSlash(path), Index: index, Worktree: worktree}
		file.Untracked = index == "?" && worktree == "?"
		file.Staged = index != " " && index != "?"
		file.Unstaged = worktree != " " && worktree != "?"
		file.Renamed = index == "R" || worktree == "R" || index == "C" || worktree == "C"
		if file.Renamed && i+1 < len(parts) && parts[i+1] != "" {
			file.OrigPath = filepath.ToSlash(parts[i+1])
			i++
		}
		files = append(files, file)
	}
	return files
}

func parseAheadBehind(out string) (int, int) {
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return 0, 0
	}
	ahead, _ := strconv.Atoi(fields[0])
	behind, _ := strconv.Atoi(fields[1])
	return ahead, behind
}

func parseGitNumstat(out string) []gitDiffFile {
	records := strings.Split(out, "\x00")
	files := make([]gitDiffFile, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record) == "" {
			continue
		}
		fields := strings.Split(record, "\t")
		if len(fields) < 3 {
			continue
		}
		added, binaryA := parseNumstatCount(fields[0])
		deleted, binaryD := parseNumstatCount(fields[1])
		files = append(files, gitDiffFile{Path: filepath.ToSlash(fields[2]), Added: added, Deleted: deleted, Binary: binaryA || binaryD})
	}
	return files
}

func parseNumstatCount(value string) (int, bool) {
	if value == "-" {
		return 0, true
	}
	parsed, _ := strconv.Atoi(value)
	return parsed, false
}

func parseGitLog(out string) []gitCommit {
	records := strings.Split(out, "\x1e")
	commits := make([]gitCommit, 0, len(records))
	for _, record := range records {
		record = strings.Trim(record, "\x00\n\r ")
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x00")
		if len(fields) < 6 {
			continue
		}
		commits = append(commits, gitCommit{Hash: fields[0], ShortHash: fields[1], AuthorName: fields[2], AuthorEmail: fields[3], Date: fields[4], Subject: fields[5]})
	}
	return commits
}

func boundedInt(raw string, fallback, minValue, maxValue int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		value = fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func safeUTF8(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	return strings.ToValidUTF8(text, "�")
}

func writeGitError(w http.ResponseWriter, err error) {
	var gitErr gitCommandError
	if errors.As(err, &gitErr) {
		writeError(w, gitErr.Status, gitErr.Msg)
		return
	}
	writeError(w, statusFromError(err), err.Error())
}
