package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	gitLogPrettyFormat         = "%H%x00%h%x00%P%x00%an%x00%ae%x00%aI%x00%d%x00%s%x00%x1e"
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
	Hash        string         `json:"hash"`
	ShortHash   string         `json:"shortHash"`
	Parents     []string       `json:"parents,omitempty"`
	Refs        []gitCommitRef `json:"refs,omitempty"`
	AuthorName  string         `json:"authorName"`
	AuthorEmail string         `json:"authorEmail"`
	Date        string         `json:"date"`
	Subject     string         `json:"subject"`
}

type gitCommitRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type gitCommandError struct {
	Status int
	Msg    string
}

func (e gitCommandError) Error() string { return e.Msg }

func (s *Server) gitStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.git().status(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) gitDiff(w http.ResponseWriter, r *http.Request) {
	diff, err := s.git().diff(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("scope"), r.URL.Query().Get("path"), r.URL.Query().Get("context"))
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) gitLog(w http.ResponseWriter, r *http.Request) {
	log, err := s.git().log(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("limit"))
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, log)
}

func (s *Server) rollbackRunPreview(w http.ResponseWriter, r *http.Request) {
	preview, err := s.git().rollbackPreview(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "runId"))
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) rollbackRun(w http.ResponseWriter, r *http.Request) {
	var req gitRollbackRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	response, err := s.git().rollback(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "runId"), req.Confirm)
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

const rollbackNoticeMaxPaths = 20

func rollbackNoticeText(baseHead string, restorePaths, deletePaths []string) string {
	var builder strings.Builder
	builder.WriteString("[系统] 用户已将本次运行的文件改动回滚到运行前检查点 ")
	head := strings.TrimSpace(baseHead)
	if len(head) > 8 {
		head = head[:8]
	}
	builder.WriteString(head)
	builder.WriteString("。")
	if len(restorePaths) > 0 {
		builder.WriteString("已还原文件：")
		builder.WriteString(joinBoundedPaths(restorePaths, rollbackNoticeMaxPaths))
		builder.WriteString("。")
	}
	if len(deletePaths) > 0 {
		builder.WriteString("已删除运行期间新建的文件：")
		builder.WriteString(joinBoundedPaths(deletePaths, rollbackNoticeMaxPaths))
		builder.WriteString("。")
	}
	builder.WriteString("此前对话中描述的这些改动已不存在于工作区，请以当前文件内容为准。")
	return builder.String()
}

func joinBoundedPaths(paths []string, maximum int) string {
	if len(paths) <= maximum {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:maximum], ", ") + fmt.Sprintf(" 等 %d 个文件", len(paths))
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
	var req gitCommitRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	result, err := s.git().commit(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

type gitDiscardRequest struct {
	Paths   []string `json:"paths"`
	Confirm bool     `json:"confirm"`
}

type gitDiscardResponse struct {
	GeneratedAt   string             `json:"generatedAt"`
	RepoRoot      string             `json:"repoRoot"`
	RestoredPaths []string           `json:"restoredPaths"`
	DeletedPaths  []string           `json:"deletedPaths"`
	Status        *gitStatusResponse `json:"status,omitempty"`
	Warning       string             `json:"warning,omitempty"`
}

// gitDiscard reverts the selected working-tree paths to HEAD. It is the
// general-purpose escape hatch for when a run checkpoint is unavailable (dirty
// start, later commits, hand edits): tracked files are restored from HEAD in
// both index and worktree, and files HEAD does not know about are deleted.
// Scope is strictly the caller's explicit selection; nothing resembling
// `reset --hard` or `clean` ever runs.
func (s *Server) gitDiscard(w http.ResponseWriter, r *http.Request) {
	var req gitDiscardRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	response, err := s.git().discard(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type gitDiscardPlan struct {
	// restoreFromHead exists in HEAD: index and worktree are restored from it.
	restoreFromHead []string
	// unstageOnly is in the index but not in HEAD (adds, rename targets); the
	// index entry is dropped and the file is then deleted from disk.
	unstageOnly []string
	// deleteFiles are removed from the worktree: untracked files plus everything
	// in unstageOnly.
	deleteFiles []string
}

// buildGitDiscardPlan classifies the selected status entries. Every selected
// path must match at least one change so a stale client selection fails loudly
// instead of silently discarding nothing.
func buildGitDiscardPlan(files []gitStatusFile, paths []string) (gitDiscardPlan, error) {
	plan := gitDiscardPlan{}
	matched := make(map[string]bool, len(paths))
	restoreSet := make(map[string]bool)
	unstageSet := make(map[string]bool)
	deleteSet := make(map[string]bool)
	for _, file := range files {
		selected := false
		for _, path := range paths {
			if gitStatusFileMatchesPath(file, path) {
				matched[path] = true
				selected = true
			}
		}
		if !selected {
			continue
		}
		switch {
		case file.Untracked:
			deleteSet[file.Path] = true
		case file.Renamed && file.OrigPath != "":
			// The new name never existed in HEAD: unstage and delete it, then
			// bring the original name back.
			unstageSet[file.Path] = true
			deleteSet[file.Path] = true
			restoreSet[file.OrigPath] = true
		case file.Index == "A":
			unstageSet[file.Path] = true
			deleteSet[file.Path] = true
		default:
			restoreSet[file.Path] = true
		}
	}
	for _, path := range paths {
		if !matched[path] {
			return plan, gitCommandError{Status: http.StatusConflict, Msg: "selected path has no worktree changes: " + path}
		}
	}
	plan.restoreFromHead = sortedPathSet(restoreSet)
	plan.unstageOnly = sortedPathSet(unstageSet)
	plan.deleteFiles = sortedPathSet(deleteSet)
	return plan, nil
}

func sortedPathSet(set map[string]bool) []string {
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
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
	// -c flags must precede the git subcommand (they are global options),
	// so prepend safe.directory=* so any path the user points at this
	// desktop binary at counts as trusted even when the OS reports it as
	// owned by a different user, which otherwise surfaces as a misleading
	// "not a git repository" error.
	argsWithSafeDir := []string{"-c", "safe.directory=*"}
	argsWithSafeDir = append(argsWithSafeDir, args...)
	cmd := exec.CommandContext(ctx, "git", argsWithSafeDir...)
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
		// Surface the working dir and full argv on failure so a
		// misconfigured path or misleading "not a git repository" error
		// has enough context to debug without re-running the binary.
		argsQuoted := make([]string, len(args))
		for i, a := range args {
			if a == "" {
				argsQuoted[i] = `""`
			} else if strings.ContainsAny(a, " \t\"") {
				argsQuoted[i] = strconv.Quote(a)
			} else {
				argsQuoted[i] = a
			}
		}
		msg = fmt.Sprintf(`"git" %s failed in %s (exit %d): %s`, strings.Join(argsQuoted, " "), strconv.Quote(dir), exitCode, msg)
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
		record = strings.Trim(record, "\n\r ")
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x00")
		if len(fields) > 0 && fields[len(fields)-1] == "" {
			fields = fields[:len(fields)-1]
		}
		if len(fields) >= 8 {
			commits = append(commits, gitCommit{
				Hash:        fields[0],
				ShortHash:   fields[1],
				Parents:     parseGitParents(fields[2]),
				AuthorName:  fields[3],
				AuthorEmail: fields[4],
				Date:        fields[5],
				Refs:        parseGitRefs(fields[6]),
				Subject:     fields[7],
			})
			continue
		}
		if len(fields) < 6 {
			continue
		}
		commits = append(commits, gitCommit{Hash: fields[0], ShortHash: fields[1], AuthorName: fields[2], AuthorEmail: fields[3], Date: fields[4], Subject: fields[5]})
	}
	return commits
}

func parseGitParents(raw string) []string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return nil
	}
	parents := make([]string, 0, len(fields))
	for _, field := range fields {
		if !isGitObjectID(field) {
			continue
		}
		parents = append(parents, field)
	}
	if len(parents) == 0 {
		return nil
	}
	return parents
}

func isGitObjectID(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' {
			continue
		}
		return false
	}
	return true
}

func parseGitRefs(raw string) []gitCommitRef {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.TrimSuffix(raw, ")")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	refs := make([]gitCommitRef, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "HEAD -> ") {
			refs = append(refs, gitCommitRef{Kind: "head", Name: "HEAD"})
			refs = append(refs, parseGitRefName(strings.TrimSpace(strings.TrimPrefix(part, "HEAD -> ")))...)
			continue
		}
		if part == "HEAD" {
			refs = append(refs, gitCommitRef{Kind: "head", Name: "HEAD"})
			continue
		}
		refs = append(refs, parseGitRefName(part)...)
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

func parseGitRefName(name string) []gitCommitRef {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(name, "refs/heads/"):
		return []gitCommitRef{{Kind: "branch", Name: strings.TrimPrefix(name, "refs/heads/")}}
	case strings.HasPrefix(name, "refs/remotes/"):
		return []gitCommitRef{{Kind: "remote", Name: strings.TrimPrefix(name, "refs/remotes/")}}
	case strings.HasPrefix(name, "refs/tags/"):
		return []gitCommitRef{{Kind: "tag", Name: strings.TrimPrefix(name, "refs/tags/")}}
	case strings.HasPrefix(name, "tag: "):
		return []gitCommitRef{{Kind: "tag", Name: strings.TrimSpace(strings.TrimPrefix(name, "tag: "))}}
	default:
		return []gitCommitRef{{Kind: "branch", Name: name}}
	}
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

func (s *Server) writeGitError(w http.ResponseWriter, r *http.Request, err error) {
	var gitErr gitCommandError
	if errors.As(err, &gitErr) {
		// runGitCommand embeds the working directory and argv so a local
		// operator can tell a path misconfiguration from a missing repo.
		// Remote sessions still get the generic gated text.
		s.writeRequestError(w, r, gitErr.Status, gitErr)
		return
	}
	s.writeRequestError(w, r, statusFromError(err), err)
}
