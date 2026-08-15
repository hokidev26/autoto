package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/tools"
)

// Background-task park, child-report, and wake/recovery helpers. Split out of
// continuation.go to keep that file inside the source size budget; the run loop
// still lives there, while this file owns the resumeParent boundary.

// subagentReportMaxBytes bounds the child answer embedded in a wake-up. Large
// enough for a real report, small enough that a rambling child cannot crowd
// the parent's context window.
const subagentReportMaxBytes = 6 * 1024

// waitingBackgroundTaskReport assembles what the parked run's child actually
// produced: terminal status, failure reason if any, and the child's last
// visible answer. Empty when there is nothing worth relaying, in which case
// the control message falls back to pointing at the Task tools.
func (r *Runner) waitingBackgroundTaskReport(ctx context.Context, run db.Run) string {
	taskID := strings.TrimSpace(run.WaitingBackgroundTaskID)
	if r == nil || taskID == "" {
		return ""
	}
	service := r.backgroundTaskService()
	if service == nil {
		return ""
	}
	task, err := service.Get(ctx, run.AgentID, taskID)
	if err != nil {
		return ""
	}
	answer := ""
	if child := strings.TrimSpace(task.ChildAgentID); child != "" && r.store != nil {
		answer = r.latestVisibleAssistantText(ctx, child, task.ChildRunID)
	}
	failure := strings.TrimSpace(task.ErrorMessage)
	if answer == "" && failure == "" {
		return ""
	}
	var report strings.Builder
	report.WriteString("status: " + strings.TrimSpace(task.Status))
	if failure != "" {
		report.WriteString("\nerror: " + failure)
	}
	if answer != "" {
		bounded, _ := truncateUTF8Bytes(answer, subagentReportMaxBytes)
		report.WriteString("\n" + bounded)
	}
	return report.String()
}

// latestVisibleAssistantText is the child's answer as the task panel shows it:
// the newest assistant message with non-empty text. When the task recorded the
// run it dispatched, only that run's messages qualify: the child can be an
// existing conversation reached through a message task, and a dispatched run
// that produced no visible text must report nothing rather than quote some
// concurrent exchange in the same conversation as the answer. Older tasks
// without a child run id keep the agent-wide newest text.
func (r *Runner) latestVisibleAssistantText(ctx context.Context, agentID string, runIDs ...string) string {
	messages, err := r.store.ListMessages(ctx, agentID)
	if err != nil {
		return ""
	}
	runID := ""
	if len(runIDs) > 0 {
		runID = strings.TrimSpace(runIDs[0])
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(message.ContentText)
		if text == "" {
			continue
		}
		if runID == "" || message.RunID == runID {
			return text
		}
	}
	return ""
}

// backgroundTaskIsTerminal reports whether a background task has already
// reached a state it will never leave.
func backgroundTaskIsTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "completed", "failed", "error", "cancelled", "canceled", "interrupted":
		return true
	default:
		return false
	}
}

// backgroundTaskContinuationBoundary decides whether a tool result parks this
// run until a background task finishes.
//
// Only a fresh dispatch advertises Meta["background"]. Task inspect/wait/output
// replay the same JSON shape (including resumeParent) without that flag, and
// parking on those reads stranded the parent: after the first child woke it,
// inspecting a still-running sibling took the boundary again, and Task.wait's
// timeout could never return control to the model. Ownership and liveness
// checks still run here for a real dispatch, at the moment the boundary is
// taken, not only on the wake path where the run is already parked.
func backgroundTaskContinuationBoundary(result tools.Result, runID string) (string, bool, error) {
	if result.IsError {
		return "", false, nil
	}
	background := false
	metaResume := false
	if result.Meta != nil {
		background, _ = result.Meta["background"].(bool)
		metaResume, _ = result.Meta["resumeParent"].(bool)
	}
	if !background {
		return "", false, nil
	}
	var task tools.BackgroundTask
	hasJSON := strings.TrimSpace(result.Output) != "" && json.Unmarshal([]byte(result.Output), &task) == nil
	if hasJSON && task.ResumeParent {
		if strings.TrimSpace(task.ID) == "" {
			return "", false, errors.New("resumeParent tool result is missing a background task id")
		}
		if parent := strings.TrimSpace(task.ParentRunID); parent != "" && parent != strings.TrimSpace(runID) {
			return "", false, nil
		}
		if backgroundTaskIsTerminal(task.Status) {
			return "", false, nil
		}
		return strings.TrimSpace(task.ID), true, nil
	}
	if !metaResume {
		return "", false, nil
	}
	for _, key := range []string{"backgroundTaskId", "taskId", "waitingBackgroundTaskId"} {
		if value, _ := result.Meta[key].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true, nil
		}
	}
	return "", false, errors.New("resumeParent tool result is missing a background task id")
}

// runSupportsResumeParent reports whether the run can park on a resume_parent
// boundary and be woken by the child's terminal hook. The wait boundary is
// exempt from the safe-mode continuation gate (see scheduleContinuation), so
// any durable execute run qualifies -- including chat conversation runs, whose
// forced-off continuation mode only disables runaway auto-continuation. Plan
// draft runs stay excluded: validateContinuationBoundary refuses to resume
// them at all.
func (r *Runner) runSupportsResumeParent(run db.Run) bool {
	if strings.TrimSpace(run.ID) == "" {
		return false
	}
	return run.ExecutionMode != db.RunExecutionModePlan
}

// abandonWaitingBackgroundTask stops the subagent a cancelled continuation was
// waiting on. Cancelling the Run alone orphaned it: the task kept working
// against a boundary that can no longer be resumed, because
// WakeBackgroundContinuation refuses a Run that is not continuation_pending. The
// subagent therefore spent its whole budget and its result was discarded without
// anything being reported.
func (r *Runner) abandonWaitingBackgroundTask(ctx context.Context, run db.Run, depth int) {
	taskID := strings.TrimSpace(run.WaitingBackgroundTaskID)
	if r == nil || taskID == "" || depth >= maxInterruptSubagentDepth {
		return
	}
	// In a chat the user typing again is routine, not a change of plan: killing
	// the subagent they just dispatched because they said "ok" would discard
	// real work. The child keeps running; only the automatic report is lost,
	// and its result stays visible in the task panel. This covers every
	// attended run -- the UI submits chats as manual runs, not just as
	// conversation runs -- while unattended sources (schedules, internal
	// dispatch) still tear the child down with the boundary.
	if !isUnattendedRun(run) {
		return
	}
	service := r.backgroundTaskService()
	if service == nil {
		return
	}
	task, err := service.Get(ctx, run.AgentID, taskID)
	if err != nil {
		slog.Warn("loading the background task behind a cancelled continuation failed",
			"agentId", run.AgentID, "runId", run.ID, "taskId", taskID, "error", err)
		return
	}
	status := strings.ToLower(strings.TrimSpace(task.Status))
	if backgroundTaskStatusTerminal(status) {
		return
	}
	if status != db.BackgroundTaskStatusCancelRequested {
		if _, cancelErr := service.Cancel(ctx, run.AgentID, taskID); cancelErr != nil {
			slog.Warn("cancelling the background task behind a cancelled continuation failed",
				"agentId", run.AgentID, "runId", run.ID, "taskId", taskID, "error", cancelErr)
		}
	}
	if child := strings.TrimSpace(task.ChildAgentID); child != "" {
		if _, childErr := r.interruptAgentTree(ctx, child, depth+1); childErr != nil {
			slog.Warn("interrupting the child agent behind a cancelled continuation failed",
				"agentId", run.AgentID, "runId", run.ID, "childAgentId", child, "error", childErr)
		}
	}
}

// ResumeContinuationRun is the app/background integration point for waking a
// durable continuation_pending run without creating a new Run identity.
func (r *Runner) ResumeContinuationRun(ctx context.Context, runID string) (bool, error) {
	run, err := r.store.GetRunByID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return false, err
	}
	return r.schedulePendingContinuation(ctx, run)
}

// WakeBackgroundContinuation verifies the durable task boundary before
// resuming. Background completion wiring should call this method.
func (r *Runner) WakeBackgroundContinuation(ctx context.Context, runID, taskID string) (bool, error) {
	run, err := r.store.GetRunByID(ctx, strings.TrimSpace(runID))
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(run.WaitingBackgroundTaskID) == "" || run.WaitingBackgroundTaskID != strings.TrimSpace(taskID) {
		return false, fmt.Errorf("%w: background task does not own the continuation boundary", db.ErrConflict)
	}
	return r.schedulePendingContinuation(ctx, run)
}

func (r *Runner) resumeReadyBackgroundContinuation(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := r.store.GetRunByID(ctx, strings.TrimSpace(runID))
	if err != nil || run.Status != "continuation_pending" || strings.TrimSpace(run.WaitingBackgroundTaskID) == "" {
		return
	}
	ready, err := r.backgroundContinuationReady(ctx, run)
	if err != nil {
		r.publishContinuationBlocked(run, err.Error())
		return
	}
	if !ready {
		return
	}
	if _, err := r.schedulePendingContinuation(ctx, run); err != nil && !errors.Is(err, ErrAgentBusy) && !errors.Is(err, db.ErrConflict) {
		r.publishContinuationBlocked(run, err.Error())
	}
}

func (r *Runner) backgroundContinuationReady(ctx context.Context, run db.Run) (bool, error) {
	service := r.backgroundTaskService()
	if service == nil {
		return false, errors.New("background task service is unavailable for continuation recovery")
	}
	task, err := service.Get(ctx, run.AgentID, run.WaitingBackgroundTaskID)
	if err != nil {
		return false, err
	}
	if task.ParentRunID != run.ID || !task.ResumeParent {
		return false, errors.New("background task no longer owns this resumeParent boundary")
	}
	return backgroundTaskIsTerminal(task.Status), nil
}

func (r *Runner) schedulePendingContinuation(ctx context.Context, run db.Run) (bool, error) {
	if run.Status != "continuation_pending" {
		return false, nil
	}
	if strings.TrimSpace(run.WaitingBackgroundTaskID) != "" {
		ready, err := r.backgroundContinuationReady(ctx, run)
		if err != nil {
			return false, err
		}
		if !ready {
			return false, nil
		}
	}
	if err := r.validateContinuationBoundary(ctx, run, true); err != nil {
		r.publishContinuationBlocked(run, err.Error())
		if store, storeErr := r.continuationStore(); storeErr == nil {
			_, _ = store.CancelContinuationRun(context.WithoutCancel(ctx), run.ID, run.ContinuationCount, err.Error())
		}
		return false, err
	}
	r.runMu.Lock()
	if r.running == nil {
		r.running = make(map[string]*activeRun)
	}
	if !r.waitForBackgroundCompactionLocked(run.AgentID) {
		r.runMu.Unlock()
		return false, ErrAgentBusy
	}
	if r.running[run.AgentID] != nil {
		r.runMu.Unlock()
		return false, ErrAgentBusy
	}
	placeholder := &activeRun{runID: run.ID}
	r.running[run.AgentID] = placeholder
	r.runMu.Unlock()
	store, err := r.continuationStore()
	if err != nil {
		r.clearReservedRun(run.AgentID, placeholder)
		return false, err
	}
	resumed, err := store.ResumeContinuationRun(ctx, run.ID, run.ContinuationCount)
	if err != nil {
		r.clearReservedRun(run.AgentID, placeholder)
		return false, err
	}
	baseCtx := context.Background()
	if taskID := strings.TrimSpace(run.WaitingBackgroundTaskID); taskID != "" {
		baseCtx = context.WithValue(baseCtx, continuationBackgroundTaskContextKey, taskID)
	}
	runCtx, cancel := context.WithCancel(baseCtx)
	active := &activeRun{cancel: cancel, runID: resumed.ID, triggerMessageID: resumed.TriggerMessageID}
	r.runMu.Lock()
	if r.running[run.AgentID] != placeholder {
		r.runMu.Unlock()
		cancel()
		return false, ErrAgentBusy
	}
	active.pending = placeholder.pending
	active.pendingRunID = placeholder.pendingRunID
	active.pendingTriggerMessageID = placeholder.pendingTriggerMessageID
	active.interrupted = placeholder.interrupted
	r.running[run.AgentID] = active
	r.runMu.Unlock()
	if active.interrupted {
		r.unregisterRun(run.AgentID, active)
		r.captureRunEndHead(active.runID)
		_ = r.completeRun(context.Background(), active.runID, "interrupted", "")
		return false, nil
	}
	go r.executeRegisteredRun(runCtx, run.AgentID, active)
	return true, nil
}

// RecoverInterruptedToolExecutionGroups reconciles durable ledgers at startup.
// A fully terminal group can be settled from its persisted items; any group with
// a non-terminal item is explicitly aborted so it can never be mistaken for a
// completed model-turn boundary after process restart.
func (r *Runner) RecoverInterruptedToolExecutionGroups(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	groups, err := r.store.ListUnsettledToolExecutionGroups(ctx, 10000)
	if err != nil {
		return fmt.Errorf("list unsettled tool execution groups: %w", err)
	}
	for _, group := range groups {
		if _, settleErr := r.store.SettleToolExecutionGroup(ctx, group.ID); settleErr == nil {
			continue
		} else if !errors.Is(settleErr, db.ErrConflict) {
			return fmt.Errorf("settle recovered tool execution group %s: %w", group.ID, settleErr)
		}
		// Cross-check the ledger against the assistant message that produced it
		// before drawing any conclusion from the ledger's contents. The two are
		// separate durable records of one event, and a disagreement between them is
		// not ordinary interruption: it means one of them is wrong. Reporting the
		// named reason is more useful than aborting as though the process had simply
		// stopped, which would silently hide the inconsistency.
		if assistant, msgErr := r.store.GetToolExecutionGroupAssistantMessage(ctx, group.AssistantMessageID); msgErr == nil {
			if validateErr := ValidateToolExecutionLedger(group, assistant); validateErr != nil {
				var corruption *LedgerCorruption
				if errors.As(validateErr, &corruption) {
					slog.Error("tool execution ledger disagrees with its assistant message",
						"groupId", group.ID, "runId", group.RunID,
						"reason", string(corruption.Reason), "detail", corruption.Message)
				}
			}
		}
		// The group cannot be settled, so it must be aborted: leaving it open would
		// permanently fail the run's settlement barrier. Classify the stranded items
		// first so the durable reason distinguishes work that was safe to redo from
		// work that was not, instead of collapsing both into one opaque message.
		summary := summarizeInterruptedGroup(group)
		if summary.Replayable > 0 {
			slog.Info("interrupted tool execution group had replay-safe calls",
				"groupId", group.ID, "runId", group.RunID,
				"pending", summary.Pending, "replaySafe", summary.Replayable, "notReplayable", summary.Unreplayable)
		}
		if _, abortErr := r.store.AbortToolExecutionGroup(ctx, group.ID, interruptedAbortReason(summary)); abortErr != nil {
			if errors.Is(abortErr, db.ErrConflict) {
				refreshed, getErr := r.store.GetToolExecutionGroup(ctx, group.ID)
				if getErr == nil && (refreshed.Status == db.ToolExecutionGroupStatusSettled || refreshed.Status == db.ToolExecutionGroupStatusAborted) {
					continue
				}
			}
			return fmt.Errorf("abort interrupted tool execution group %s: %w", group.ID, abortErr)
		}
	}
	return nil
}

// RecoverContinuationPendingRuns schedules only runs whose persisted boundary
// still passes all continuation safety checks. It is intentionally exposed so
// app startup can call it even if RecoverInterruptedRuns is not wired there.
func (r *Runner) RecoverContinuationPendingRuns(ctx context.Context) error {
	store, err := r.continuationStore()
	if err != nil {
		return err
	}
	runs, err := store.ListContinuationPendingRuns(ctx, 1000)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if checkpointErr := r.validateContinuationRunGitCheckpoint(ctx, run); checkpointErr != nil {
			r.publishContinuationBlocked(run, checkpointErr.Error())
			if _, cancelErr := store.CancelContinuationRun(ctx, run.ID, run.ContinuationCount, checkpointErr.Error()); cancelErr != nil {
				return cancelErr
			}
			continue
		}
		refreshed, refreshErr := r.store.GetRunByID(ctx, run.ID)
		if refreshErr != nil {
			return refreshErr
		}
		run = refreshed
		if strings.TrimSpace(run.WaitingBackgroundTaskID) != "" {
			ready, waitErr := r.backgroundContinuationReady(ctx, run)
			if waitErr != nil {
				r.publishContinuationBlocked(run, waitErr.Error())
				continue
			}
			if !ready {
				continue
			}
		}
		if err := r.validateContinuationBoundary(ctx, run, true); err != nil {
			r.publishContinuationBlocked(run, err.Error())
			if _, cancelErr := store.CancelContinuationRun(ctx, run.ID, run.ContinuationCount, err.Error()); cancelErr != nil {
				return cancelErr
			}
			continue
		}
		if _, err := r.schedulePendingContinuation(ctx, run); err != nil {
			return err
		}
	}
	return nil
}
