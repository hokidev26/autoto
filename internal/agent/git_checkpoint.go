package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/gitlock"
	"autoto/internal/gitsnapshot"
	"autoto/internal/tools"
)

const (
	gitCheckpointTimeout                  = 3 * time.Second
	gitCheckpointStatusMaxBytes           = 1 << 20
	gitCheckpointIndexMaxBytes            = 64 << 10
	gitCheckpointDiagnosticMaxBytes       = 64 << 10
	gitCheckpointMaxPaths                 = 500
	gitCheckpointFingerprintTimeout       = 3 * time.Second
	gitCheckpointMaxFileBytes       int64 = 4 << 20
	gitCheckpointMaxTotalBytes      int64 = 32 << 20
)

type runGitToolSnapshot struct {
	repoRoot string
	before   map[string]db.RunGitChange
	owned    map[string]db.RunGitChange
}

func (r *Runner) captureRunCheckpoint(ctx context.Context, agent db.Agent, runID string) {
	if r == nil || r.store == nil || strings.TrimSpace(runID) == "" {
		return
	}
	run, err := r.store.GetRunByID(ctx, runID)
	if err != nil {
		slog.Warn("load run before git checkpoint failed", "runId", runID, "error", err)
		return
	}
	if isConversationRun(run) {
		return
	}
	repoRoot, ok := gitRepoRoot(ctx, agent.CWD)
	if !ok {
		slog.Warn("run git checkpoint unavailable", "runId", runID, "reason", "repository root could not be resolved")
		return
	}
	clean, err := gitWorktreeClean(ctx, repoRoot)
	if err != nil {
		slog.Warn("run git checkpoint unavailable", "runId", runID, "repoRoot", repoRoot, "reason", "worktree status could not be read", "error", err)
		return
	}
	if !clean {
		slog.Info("run git checkpoint skipped because worktree is not clean", "runId", runID, "repoRoot", repoRoot)
		return
	}
	head, ok := gitHead(ctx, repoRoot)
	if !ok {
		slog.Warn("run git checkpoint unavailable", "runId", runID, "reason", "HEAD could not be resolved")
		return
	}
	if err := r.store.BeginRunGitCheckpoint(context.Background(), runID, head, repoRoot); err != nil {
		slog.Warn("initialize durable run git checkpoint failed", "runId", runID, "error", err)
		return
	}
	slog.Info("run git checkpoint tracking initialized", "runId", runID, "repoRoot", repoRoot, "baseHead", head)
}

func (r *Runner) captureRunEndHead(runID string) {
	if r == nil || r.store == nil || strings.TrimSpace(runID) == "" {
		return
	}
	run, err := r.store.GetRunByID(context.Background(), runID)
	if err != nil {
		slog.Warn("load run git checkpoint failed", "runId", runID, "error", err)
		return
	}
	switch run.CheckpointState {
	case db.RunCheckpointTracking:
		// Continue below.
	case db.RunCheckpointCapturing:
		r.failRunGitCheckpoint(runID, "run ended while tool checkpoint capture was in progress", nil)
		return
	default:
		return
	}
	if strings.TrimSpace(run.BaseHead) == "" || strings.TrimSpace(run.CheckpointRepoRoot) == "" {
		r.failRunGitCheckpoint(runID, "tracking checkpoint is missing repository metadata", nil)
		return
	}
	agent, err := r.store.GetAgent(context.Background(), run.AgentID)
	if err != nil {
		r.failRunGitCheckpoint(runID, "load agent failed", err)
		return
	}
	repoRoot, ok := gitRepoRoot(context.Background(), agent.CWD)
	if !ok || !sameCheckpointRepo(repoRoot, run.CheckpointRepoRoot) {
		r.failRunGitCheckpoint(runID, "repository root changed before snapshot completion", nil)
		return
	}
	head, ok := gitHead(context.Background(), repoRoot)
	if !ok {
		r.failRunGitCheckpoint(runID, "HEAD could not be resolved at run completion", nil)
		return
	}
	ready, err := r.store.FinalizeRunGitCheckpoint(context.Background(), runID, head)
	if err != nil {
		slog.Warn("finalize durable run git checkpoint failed", "runId", runID, "error", err)
		return
	}
	if !ready {
		slog.Warn("run git checkpoint was not tracking at finalization", "runId", runID)
		return
	}
	slog.Info("run git checkpoint ready", "runId", runID, "repoRoot", repoRoot, "endHead", head)
}

func (r *Runner) captureRunToolGitBefore(ctx context.Context, agent db.Agent, runID string, risk tools.Risk) *runGitToolSnapshot {
	if r == nil || r.store == nil || runID == "" || (risk != tools.RiskWrite && risk != tools.RiskExec) {
		return nil
	}
	run, err := r.store.GetRunByID(ctx, runID)
	if err != nil {
		r.failRunGitCheckpoint(runID, "load run before tool snapshot failed", err)
		return nil
	}
	if run.CheckpointState != db.RunCheckpointTracking || strings.TrimSpace(run.BaseHead) == "" || strings.TrimSpace(run.CheckpointRepoRoot) == "" {
		return nil
	}
	repoRoot, ok := gitRepoRoot(ctx, agent.CWD)
	if !ok || !sameCheckpointRepo(repoRoot, run.CheckpointRepoRoot) {
		r.failRunGitCheckpoint(runID, "repository root changed before tool snapshot", nil)
		return nil
	}
	if err := r.store.MarkRunGitCheckpointCapturing(context.Background(), runID); err != nil {
		r.failRunGitCheckpoint(runID, "persist checkpoint capturing state before tool execution failed", err)
		return nil
	}
	before, err := gitRunChangeSnapshot(ctx, repoRoot)
	if err != nil {
		r.failRunGitCheckpoint(runID, "capture pre-tool git snapshot failed", err)
		return nil
	}
	changes, err := r.store.ListRunGitChanges(ctx, runID)
	if err != nil {
		r.failRunGitCheckpoint(runID, "load persisted owned changes before tool snapshot failed", err)
		return nil
	}
	return &runGitToolSnapshot{repoRoot: repoRoot, before: before, owned: runGitChangeMap(changes)}
}

func (r *Runner) captureRunToolGitAfter(ctx context.Context, runID string, before *runGitToolSnapshot) {
	if r == nil || r.store == nil || before == nil || runID == "" {
		return
	}
	after, err := gitRunChangeSnapshot(ctx, before.repoRoot)
	if err != nil {
		r.failRunGitCheckpoint(runID, "capture post-tool git snapshot failed", err)
		return
	}
	owned, err := mergeRunGitChangeWindow(before.owned, before.before, after)
	if err != nil {
		r.failRunGitCheckpoint(runID, "tool changed a path outside its owned clean window: "+err.Error(), nil)
		return
	}
	if err := r.store.ReplaceRunGitCheckpointChanges(context.Background(), runID, runGitChangeSlice(owned)); err != nil {
		r.failRunGitCheckpoint(runID, "persist owned changes after tool snapshot failed", err)
		return
	}
}

func mergeRunGitChangeWindow(owned, before, after map[string]db.RunGitChange) (map[string]db.RunGitChange, error) {
	next := make(map[string]db.RunGitChange, len(owned)+len(after))
	for path, change := range owned {
		next[path] = change
	}
	paths := make(map[string]struct{}, len(owned)+len(before)+len(after))
	for path := range owned {
		paths[path] = struct{}{}
	}
	for path := range before {
		paths[path] = struct{}{}
	}
	for path := range after {
		paths[path] = struct{}{}
	}
	for path := range paths {
		beforeChange, beforeOK := before[path]
		afterChange, afterOK := after[path]
		ownedChange, ownedBefore := owned[path]
		if ownedBefore {
			if !beforeOK || !sameRunGitChange(ownedChange, beforeChange) {
				return nil, fmt.Errorf("owned path changed outside a tool window before the next tool: %s", path)
			}
			if afterOK {
				next[path] = afterChange
			} else {
				delete(next, path)
			}
			continue
		}
		if beforeOK {
			if !afterOK || !sameRunGitChange(beforeChange, afterChange) {
				return nil, fmt.Errorf("tool changed a path that was already dirty before the tool window: %s", path)
			}
			continue
		}
		if afterOK {
			next[path] = afterChange
		}
	}
	if len(next) > gitCheckpointMaxPaths {
		return nil, fmt.Errorf("checkpoint path count exceeds %d", gitCheckpointMaxPaths)
	}
	return next, nil
}

func (r *Runner) failRunGitCheckpoint(runID, reason string, cause error) {
	if r == nil || r.store == nil || runID == "" {
		return
	}
	if cause != nil {
		reason = reason + ": " + cause.Error()
	}
	if err := r.store.InvalidateRunGitCheckpoint(context.Background(), runID, reason); err != nil {
		slog.Warn("invalidate run git checkpoint failed", "runId", runID, "reason", reason, "error", err)
		return
	}
	slog.Warn("run git checkpoint invalidated", "runId", runID, "reason", reason)
}

func (r *Runner) RecoverInterruptedRuns(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	if err := r.RecoverInterruptedToolExecutionGroups(ctx); err != nil {
		return fmt.Errorf("recover interrupted tool execution groups: %w", err)
	}
	rollingBackRuns, err := r.store.ListRollingBackRuns(ctx)
	if err != nil {
		return fmt.Errorf("list rolling back runs for recovery: %w", err)
	}
	for _, run := range rollingBackRuns {
		if err := r.store.FailRunGitRollback(ctx, run.ID, "process restarted while rollback was in progress"); err != nil {
			return fmt.Errorf("invalidate stranded rollback for run %s: %w", run.ID, err)
		}
		updated, err := r.store.GetRunByID(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("load recovered rollback run %s: %w", run.ID, err)
		}
		slog.Info("recovered stranded rollback checkpoint", "runId", updated.ID, "agentId", updated.AgentID, "runStatus", updated.Status, "checkpointState", updated.CheckpointState, "checkpointRecovery", "invalidated_rolling_back")
	}

	runs, err := r.store.ListRecoverableRuns(ctx)
	if err != nil {
		return fmt.Errorf("list interrupted runs for recovery: %w", err)
	}
	for _, run := range runs {
		action, err := r.recoverRunGitCheckpoint(ctx, run)
		if err != nil {
			return fmt.Errorf("recover run %s checkpoint: %w", run.ID, err)
		}
		if err := r.store.RecoverInterruptedRun(ctx, run.ID); err != nil {
			return fmt.Errorf("mark recovered run %s interrupted: %w", run.ID, err)
		}
		updated, err := r.store.GetRunByID(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("load recovered run %s: %w", run.ID, err)
		}
		slog.Info("recovered interrupted run", "runId", updated.ID, "agentId", updated.AgentID, "runStatus", updated.Status, "checkpointState", updated.CheckpointState, "checkpointRecovery", action)
	}
	return nil
}

func (r *Runner) recoverRunGitCheckpoint(ctx context.Context, run db.Run) (string, error) {
	switch run.CheckpointState {
	case db.RunCheckpointCapturing:
		return "invalidated_capturing", r.store.InvalidateRunGitCheckpoint(ctx, run.ID, "process ended while tool checkpoint capture was in progress")
	case db.RunCheckpointTracking:
		return r.recoverTrackingRunGitCheckpoint(ctx, run)
	case db.RunCheckpointRollingBack:
		return "invalidated_rolling_back", r.store.FailRunGitRollback(ctx, run.ID, "process restarted while rollback was in progress")
	default:
		return "unchanged", nil
	}
}

func (r *Runner) recoverTrackingRunGitCheckpoint(ctx context.Context, run db.Run) (string, error) {
	invalidate := func(reason string, cause error) (string, error) {
		if cause != nil {
			reason += ": " + cause.Error()
		}
		if err := r.store.InvalidateRunGitCheckpoint(ctx, run.ID, reason); err != nil {
			return "", err
		}
		return "invalidated_tracking", nil
	}
	head, err := r.verifyTrackingRunGitCheckpoint(ctx, run)
	if err != nil {
		return invalidate("tracking checkpoint recovery verification failed", err)
	}
	ready, err := r.store.FinalizeRunGitCheckpoint(ctx, run.ID, head)
	if err != nil {
		return "", err
	}
	if !ready {
		return "", fmt.Errorf("tracking checkpoint was not available for finalization")
	}
	return "finalized_ready", nil
}

func (r *Runner) verifyTrackingRunGitCheckpoint(ctx context.Context, run db.Run) (string, error) {
	if strings.TrimSpace(run.BaseHead) == "" || strings.TrimSpace(run.CheckpointRepoRoot) == "" {
		return "", errors.New("tracking checkpoint is missing repository metadata")
	}
	agent, err := r.store.GetAgent(ctx, run.AgentID)
	if err != nil {
		return "", fmt.Errorf("load agent during checkpoint verification: %w", err)
	}
	repoRoot, ok := gitRepoRoot(ctx, agent.CWD)
	if !ok || !sameCheckpointRepo(repoRoot, run.CheckpointRepoRoot) {
		return "", errors.New("repository root changed during checkpoint verification")
	}
	head, ok := gitHead(ctx, repoRoot)
	if !ok || head != strings.TrimSpace(run.BaseHead) {
		return "", errors.New("HEAD changed during checkpoint verification")
	}
	persisted, err := r.store.ListRunGitChanges(ctx, run.ID)
	if err != nil {
		return "", err
	}
	current, err := gitRunChangeSnapshot(ctx, repoRoot)
	if err != nil {
		return "", fmt.Errorf("capture current git snapshot during checkpoint verification: %w", err)
	}
	if !sameRunGitChangeMaps(runGitChangeMap(persisted), current) {
		return "", errors.New("current git changes do not exactly match persisted checkpoint ownership")
	}
	return head, nil
}

func (r *Runner) validateContinuationRunGitCheckpoint(ctx context.Context, run db.Run) error {
	switch run.CheckpointState {
	case db.RunCheckpointNone:
		return nil
	case db.RunCheckpointTracking:
		if _, err := r.verifyTrackingRunGitCheckpoint(ctx, run); err != nil {
			reason := "continuation checkpoint verification failed: " + err.Error()
			if invalidateErr := r.store.InvalidateRunGitCheckpoint(ctx, run.ID, reason); invalidateErr != nil {
				return invalidateErr
			}
			return errors.New(reason)
		}
		return nil
	case db.RunCheckpointCapturing:
		reason := "process restarted while a continuation tool checkpoint capture was in progress"
		if err := r.store.InvalidateRunGitCheckpoint(ctx, run.ID, reason); err != nil {
			return err
		}
		return errors.New(reason)
	case db.RunCheckpointRollingBack:
		reason := "process restarted while continuation rollback was in progress"
		if err := r.store.FailRunGitRollback(ctx, run.ID, reason); err != nil {
			return err
		}
		return errors.New(reason)
	case db.RunCheckpointReady:
		return errors.New("continuation checkpoint was finalized before the run completed")
	case db.RunCheckpointInvalid:
		return errors.New("continuation checkpoint is invalid")
	case db.RunCheckpointRolledBack:
		return errors.New("continuation checkpoint was already rolled back")
	default:
		return fmt.Errorf("unknown continuation checkpoint state %q", run.CheckpointState)
	}
}

func gitRepoRoot(ctx context.Context, cwd string) (string, bool) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", false
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", false
	}
	out, err := runCheckpointGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	repoRoot := strings.TrimSpace(out)
	return repoRoot, repoRoot != ""
}

func gitHead(ctx context.Context, repoRoot string) (string, bool) {
	out, err := runCheckpointGit(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", false
	}
	head := strings.TrimSpace(out)
	return head, head != ""
}

func gitWorktreeClean(ctx context.Context, repoRoot string) (bool, error) {
	out, err := runCheckpointGit(ctx, repoRoot, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

func gitRunChangeSnapshot(ctx context.Context, repoRoot string) (map[string]db.RunGitChange, error) {
	fingerprintCtx, cancel := context.WithTimeout(ctx, gitCheckpointFingerprintTimeout)
	defer cancel()
	out, err := runCheckpointGit(fingerprintCtx, repoRoot, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	entries, err := checkpointStatusEntries(out)
	if err != nil {
		return nil, err
	}
	changes := make(map[string]db.RunGitChange, len(entries))
	budget := &gitsnapshot.FingerprintBudget{MaxFileBytes: gitCheckpointMaxFileBytes, MaxTotalBytes: gitCheckpointMaxTotalBytes}
	for _, entry := range entries {
		if err := fingerprintCtx.Err(); err != nil {
			return nil, fmt.Errorf("checkpoint fingerprint time budget exceeded: %w", err)
		}
		if _, exists := changes[entry.Path]; exists {
			return nil, fmt.Errorf("git status reported duplicate checkpoint path: %s", entry.Path)
		}
		indexFingerprint, err := gitIndexFingerprint(fingerprintCtx, repoRoot, entry.Path)
		if err != nil {
			return nil, err
		}
		worktreeFingerprint, err := gitsnapshot.WorktreeFingerprintWithBudget(fingerprintCtx, repoRoot, entry.Path, budget)
		if err != nil {
			return nil, err
		}
		changes[entry.Path] = db.RunGitChange{Path: entry.Path, IndexStatus: entry.IndexStatus, WorktreeStatus: entry.WorktreeStatus, Untracked: entry.Untracked, IndexFingerprint: indexFingerprint, WorktreeFingerprint: worktreeFingerprint}
	}
	return changes, nil
}

func checkpointStatusEntries(out string) ([]gitsnapshot.StatusEntry, error) {
	entries, err := gitsnapshot.ParsePorcelainV1NoRenames(out)
	if err != nil {
		return nil, err
	}
	if len(entries) > gitCheckpointMaxPaths {
		return nil, fmt.Errorf("checkpoint path count exceeds %d", gitCheckpointMaxPaths)
	}
	return entries, nil
}

func gitIndexFingerprint(ctx context.Context, repoRoot, relativePath string) (string, error) {
	out, err := runCheckpointGit(ctx, repoRoot, "ls-files", "-s", "-z", "--", relativePath)
	if err != nil {
		return "", err
	}
	return gitsnapshot.IndexFingerprint(out), nil
}

func runGitChangeMap(changes []db.RunGitChange) map[string]db.RunGitChange {
	out := make(map[string]db.RunGitChange, len(changes))
	for _, change := range changes {
		out[change.Path] = change
	}
	return out
}

func runGitChangeSlice(changes map[string]db.RunGitChange) []db.RunGitChange {
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]db.RunGitChange, 0, len(paths))
	for _, path := range paths {
		out = append(out, changes[path])
	}
	return out
}

func sameRunGitChangeMaps(left, right map[string]db.RunGitChange) bool {
	if len(left) != len(right) {
		return false
	}
	for path, leftChange := range left {
		rightChange, ok := right[path]
		if !ok || !sameRunGitChange(leftChange, rightChange) {
			return false
		}
	}
	return true
}

func sameRunGitChange(left, right db.RunGitChange) bool {
	return left.Path == right.Path && left.OrigPath == right.OrigPath && left.IndexStatus == right.IndexStatus && left.WorktreeStatus == right.WorktreeStatus && left.Untracked == right.Untracked && left.IndexFingerprint == right.IndexFingerprint && left.WorktreeFingerprint == right.WorktreeFingerprint
}

func runGitMutationLock(ctx context.Context, cwd string, risk tools.Risk) func() {
	if risk != tools.RiskWrite && risk != tools.RiskExec {
		return func() {}
	}
	repoRoot, ok := gitRepoRoot(ctx, cwd)
	if !ok {
		return func() {}
	}
	return gitlock.Default.Lock(repoRoot)
}

func sameCheckpointRepo(left, right string) bool {
	return canonicalCheckpointPath(left) == canonicalCheckpointPath(right)
}

func canonicalCheckpointPath(path string) string {
	path = strings.TrimSpace(path)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

type checkpointLimitedBuffer struct {
	builder   strings.Builder
	max       int
	truncated bool
}

func (b *checkpointLimitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		b.truncated = len(p) > 0
		return len(p), nil
	}
	remaining := b.max - b.builder.Len()
	if remaining > 0 {
		if len(p) <= remaining {
			_, _ = b.builder.Write(p)
		} else {
			_, _ = b.builder.Write(p[:remaining])
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *checkpointLimitedBuffer) String() string { return b.builder.String() }

func runCheckpointGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, gitCheckpointTimeout)
	defer cancel()
	limit := gitCheckpointStatusMaxBytes
	if len(args) > 0 && args[0] == "ls-files" {
		limit = gitCheckpointIndexMaxBytes
	}
	cmd := exec.CommandContext(cmdCtx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	stdout := &checkpointLimitedBuffer{max: limit}
	stderr := &checkpointLimitedBuffer{max: gitCheckpointDiagnosticMaxBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.truncated || stderr.truncated {
		return stdout.String(), fmt.Errorf("git checkpoint output exceeded configured %d byte limit", limit)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}
