package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"autoto/internal/agent"
	"autoto/internal/db"
)

const (
	// defaultTargetBusyWait bounds how long a message task waits for a busy
	// target conversation before giving up. The sender gets a clear
	// target_agent_busy error and can simply retry later; queueing behind an
	// arbitrarily long run would silently hold a worker slot instead.
	defaultTargetBusyWait = 30 * time.Second
	// maxMessageReplyBytes keeps the target's reply inside the task result
	// budget (maxAgentResultBytes). The full reply stays readable in the target
	// conversation and in the parent's wake-up report.
	maxMessageReplyBytes = 3 * 1024
)

type messagePublicResult struct {
	TargetAgentID  string `json:"targetAgentId"`
	TargetTitle    string `json:"targetTitle,omitempty"`
	RunID          string `json:"runId,omitempty"`
	Status         string `json:"status"`
	Reply          string `json:"reply,omitempty"`
	ReplyTruncated bool   `json:"replyTruncated,omitempty"`
	TargetError    string `json:"targetError,omitempty"`
}

// executeSendMessage delivers a message task to an existing primary
// conversation: it submits the prompt as an internal run on the target, waits
// for that run to finish, and reports the target's reply as the task result.
// The task's child linkage points at the target conversation, so cancellation,
// the task panel, and the parent's wake-up report all reuse the child-agent
// machinery unchanged.
func (e *AgentExecutor) executeSendMessage(ctx context.Context, task db.BackgroundTask, parent db.Agent, payload agentPayload, output OutputWriter) (Result, error) {
	if !isPrimaryConversation(parent.Type) {
		return Result{ErrorCode: "message_owner_rejected"}, errors.New("only primary conversations can send messages to other conversations")
	}
	target, err := e.Store.GetAgent(ctx, payload.TargetAgentID)
	if err != nil {
		return Result{ErrorCode: "message_target_unavailable"}, fmt.Errorf("load target conversation: %w", err)
	}
	if target.ID == parent.ID {
		return Result{ErrorCode: "message_target_rejected"}, errors.New("a conversation cannot send a message to itself")
	}
	if !isPrimaryConversation(target.Type) {
		return Result{ErrorCode: "message_target_rejected"}, errors.New("messages can only be sent to primary conversations")
	}
	if strings.TrimSpace(target.ArchivedAt) != "" {
		return Result{ErrorCode: "message_target_rejected"}, errors.New("target conversation is archived")
	}
	if code, hideErr := hiddenPeerConversationError(ctx, e.Store, target); hideErr != nil {
		return Result{ErrorCode: code}, hideErr
	}
	targetTitle := truncateUTF8(target.Title, maxAgentTaskMetaBytes)
	if err := e.rejectMessageCycle(ctx, parent.ID, target.ID); err != nil {
		// The original cross-conversation task is still active and its reply will
		// be delivered automatically. Treat a duplicate reverse send as an
		// informational completion rather than a failed task, so the UI does not
		// report a successful exchange as a red failure notification.
		result, marshalErr := marshalMessagePublicResult(target.ID, targetTitle, "", "already_in_progress", "", err.Error())
		if marshalErr != nil {
			return Result{ErrorCode: "invalid_result"}, marshalErr
		}
		return Result{JSON: result}, nil
	}
	permissionCap, err := narrowestPermissionCap(target.PermissionMode, task.PermissionModeCap)
	if err != nil {
		return Result{ErrorCode: "permission_rejected"}, err
	}
	run, err := e.submitTargetRun(ctx, task, target.ID, payload.Prompt, permissionCap)
	if err != nil {
		if errors.Is(err, agent.ErrAgentBusy) {
			return Result{ErrorCode: "target_agent_busy"}, fmt.Errorf("target conversation stayed busy for %s; retry later", e.targetBusyWait())
		}
		if ctx.Err() != nil {
			result, marshalErr := marshalMessagePublicResult(target.ID, targetTitle, "", "canceled", "", "")
			if marshalErr != nil {
				return Result{ErrorCode: "invalid_result"}, marshalErr
			}
			return Result{JSON: result, ErrorCode: "canceled"}, ctx.Err()
		}
		return Result{ErrorCode: "message_submit_failed"}, fmt.Errorf("submit message run: %w", err)
	}
	attached, err := e.Store.AttachBackgroundTaskChild(ctx, task.ID, task.Revision, target.ID, run.ID)
	if err != nil {
		e.interruptTargetRun(target.ID, run.ID)
		return Result{ErrorCode: "child_attach_conflict"}, fmt.Errorf("attach message run: %w", err)
	}
	started, err := marshalMessagePublicResult(target.ID, targetTitle, run.ID, "running", "", "")
	if err != nil {
		e.interruptTargetRun(target.ID, run.ID)
		return Result{ErrorCode: "output_failed"}, err
	}
	if err := output.Write("system", append(started, '\n')); err != nil {
		e.interruptTargetRun(target.ID, run.ID)
		return Result{ErrorCode: "output_failed"}, err
	}
	return e.waitTargetRun(ctx, attached, target, targetTitle)
}

// rejectMessageCycle refuses a direct ping-pong: the target already has an
// active message task pointed back at the sender. Longer rings stay possible in
// principle, but every hop is an exec-risk tool call under its own conversation
// permissions, and the per-agent task limits bound the blast radius.
func (e *AgentExecutor) rejectMessageCycle(ctx context.Context, ownerID, targetID string) error {
	tasks, err := e.Store.ListBackgroundTasks(ctx, db.BackgroundTaskListOptions{
		OwnerAgentID: targetID,
		Statuses: []string{
			db.BackgroundTaskStatusQueued, db.BackgroundTaskStatusWaitingApproval,
			db.BackgroundTaskStatusRunning, db.BackgroundTaskStatusCancelRequested,
		},
		Limit: db.BackgroundTaskDefaultListLimit,
	})
	if err != nil {
		return fmt.Errorf("inspect target conversation tasks: %w", err)
	}
	for _, candidate := range tasks {
		if candidate.Kind != db.BackgroundTaskKindAgent {
			continue
		}
		// The list projection deliberately strips payloads; the execution
		// projection is the only reader of targetAgentId.
		full, err := e.Store.GetBackgroundTaskForExecution(ctx, candidate.ID)
		if err != nil {
			continue
		}
		var candidatePayload struct {
			TargetAgentID string `json:"targetAgentId"`
		}
		if json.Unmarshal(full.PayloadJSON, &candidatePayload) != nil {
			continue
		}
		if strings.TrimSpace(candidatePayload.TargetAgentID) == ownerID {
			return errors.New("target conversation already has an active message task addressed to this conversation; the original message is already in progress and its reply will return automatically")
		}
	}
	return nil
}

func (e *AgentExecutor) targetBusyWait() time.Duration {
	if e != nil && e.TargetBusyWait > 0 {
		return e.TargetBusyWait
	}
	return defaultTargetBusyWait
}

// submitTargetRun retries a busy target for a bounded window. Submission is
// idempotent to retry because ErrAgentBusy is returned before anything durable
// is created for this task.
func (e *AgentExecutor) submitTargetRun(ctx context.Context, task db.BackgroundTask, targetID, prompt, permissionCap string) (db.Run, error) {
	deadline := time.Now().Add(e.targetBusyWait())
	interval := 500 * time.Millisecond
	for {
		run, err := e.Runner.SubmitInternal(ctx, targetID, task.ID, prompt, permissionCap)
		if err == nil || !errors.Is(err, agent.ErrAgentBusy) {
			return run, err
		}
		if time.Now().After(deadline) {
			return db.Run{}, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			_ = timer.Stop()
			return db.Run{}, ctx.Err()
		case <-timer.C:
		}
		if interval < 2*time.Second {
			interval *= 2
		}
	}
}

// interruptTargetRun stops the run this task triggered without touching later
// work. One active run per agent makes the guard precise: while our run is
// non-terminal it IS the target's active run, and once it is terminal there is
// nothing of ours left to interrupt.
func (e *AgentExecutor) interruptTargetRun(targetAgentID, runID string) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, err := e.Store.GetRunByID(interruptCtx, runID)
	if err != nil || terminalRun(run.Status) {
		return
	}
	if _, err := e.Runner.Interrupt(interruptCtx, targetAgentID); err != nil {
		slog.Warn("interrupting a message task target run failed",
			"targetAgentId", targetAgentID, "runId", runID, "error", err)
	}
}

func (e *AgentExecutor) waitTargetRun(ctx context.Context, task db.BackgroundTask, target db.Agent, targetTitle string) (Result, error) {
	interval := e.PollInterval
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	idleTimeout := e.ChildIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultChildIdleTimeout
	}
	startedAt := time.Now()
	lastProgressAt := startedAt
	progress := ""
	for {
		run, err := e.Store.GetRunByID(ctx, task.ChildRunID)
		if err != nil {
			return Result{ErrorCode: "child_run_unavailable"}, fmt.Errorf("load message run: %w", err)
		}
		status := strings.ToLower(strings.TrimSpace(run.Status))
		if terminalRun(status) {
			return e.messageRunResult(ctx, task, target, targetTitle, run, status)
		}
		if current := childProgressFingerprint(run); current != progress {
			progress = current
			lastProgressAt = time.Now()
		}
		if reason := childWaitAbandonReason(startedAt, lastProgressAt, idleTimeout, e.ChildMaxDuration); reason != "" {
			slog.Warn("message task target run abandoned as stuck",
				"taskId", task.ID, "targetAgentId", target.ID, "runId", task.ChildRunID, "reason", reason)
			e.interruptTargetRun(target.ID, task.ChildRunID)
			result, marshalErr := marshalMessagePublicResult(target.ID, targetTitle, task.ChildRunID, "timed_out", "", reason)
			if marshalErr != nil {
				return Result{ErrorCode: "invalid_result"}, marshalErr
			}
			return Result{JSON: result, ErrorCode: "message_timed_out"}, fmt.Errorf("target conversation did not answer: %s", reason)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			_ = timer.Stop()
			e.interruptTargetRun(target.ID, task.ChildRunID)
			result, marshalErr := marshalMessagePublicResult(target.ID, targetTitle, task.ChildRunID, "canceled", "", "")
			if marshalErr != nil {
				return Result{ErrorCode: "invalid_result"}, marshalErr
			}
			return Result{JSON: result, ErrorCode: "canceled"}, ctx.Err()
		case <-timer.C:
		}
		if interval < childWaitMaxInterval {
			interval *= 2
			if interval > childWaitMaxInterval {
				interval = childWaitMaxInterval
			}
		}
	}
}

func (e *AgentExecutor) messageRunResult(ctx context.Context, task db.BackgroundTask, target db.Agent, targetTitle string, run db.Run, status string) (Result, error) {
	reply, replyTruncated := "", false
	targetError := ""
	if status == "completed" {
		reply, replyTruncated = e.latestRunAssistantText(ctx, target.ID, run.ID)
	} else {
		targetError = publicError(run.ErrorMessage)
	}
	result, err := marshalMessagePublicResultWithReply(target.ID, targetTitle, run.ID, status, reply, replyTruncated, targetError)
	if err != nil {
		return Result{ErrorCode: "invalid_result"}, err
	}
	if status == "completed" {
		return Result{JSON: result}, nil
	}
	if targetError != "" {
		return Result{JSON: result, ErrorCode: "message_" + status}, fmt.Errorf("target conversation run did not complete: %s", targetError)
	}
	return Result{JSON: result, ErrorCode: "message_" + status}, errors.New("target conversation run did not complete")
}

// latestRunAssistantText returns the newest assistant text the triggered run
// produced. Scoping to the run id keeps a concurrent user exchange in the
// target conversation from being misreported as the reply.
func (e *AgentExecutor) latestRunAssistantText(ctx context.Context, agentID, runID string) (string, bool) {
	messages, err := e.Store.ListMessages(ctx, agentID)
	if err != nil {
		return "", false
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "assistant" || message.RunID != runID {
			continue
		}
		if text := strings.TrimSpace(message.ContentText); text != "" {
			bounded := truncateUTF8(text, maxMessageReplyBytes)
			return bounded, bounded != text
		}
	}
	return "", false
}

func marshalMessagePublicResult(targetAgentID, targetTitle, runID, status, reply, targetError string) (json.RawMessage, error) {
	return marshalMessagePublicResultWithReply(targetAgentID, targetTitle, runID, status, reply, false, targetError)
}

func marshalMessagePublicResultWithReply(targetAgentID, targetTitle, runID, status string, reply string, replyTruncated bool, targetError string) (json.RawMessage, error) {
	payload := messagePublicResult{
		TargetAgentID:  strings.TrimSpace(targetAgentID),
		TargetTitle:    strings.TrimSpace(targetTitle),
		RunID:          strings.TrimSpace(runID),
		Status:         strings.ToLower(strings.TrimSpace(status)),
		Reply:          reply,
		ReplyTruncated: replyTruncated,
		TargetError:    truncateUTF8(targetError, maxAgentChildErrorBytes),
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("message task result could not be encoded")
	}
	if len(result) > maxAgentResultBytes {
		// The reply is the only unbounded-ish part; drop it rather than fail.
		// It stays readable in the target conversation and the wake-up report.
		payload.Reply = ""
		payload.ReplyTruncated = true
		result, err = json.Marshal(payload)
		if err != nil || len(result) > maxAgentResultBytes {
			return nil, errors.New("message task result exceeds size limit")
		}
	}
	return result, nil
}

func isPrimaryConversation(agentType string) bool {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "primary", "root":
		return true
	default:
		return false
	}
}

// hiddenPeerConversationError fails closed for conversations the live sidebar
// does not show: archived parent projects and retired standalone chats.
func hiddenPeerConversationError(ctx context.Context, store *db.Store, target db.Agent) (string, error) {
	if store == nil {
		return "message_target_unavailable", errors.New("target conversation was not found")
	}
	worklineID := strings.TrimSpace(target.WorklineID)
	if worklineID == "" {
		return "message_target_unavailable", errors.New("target conversation was not found")
	}
	workline, err := store.GetWorkline(ctx, worklineID)
	if err != nil {
		return "message_target_unavailable", fmt.Errorf("load target conversation: %w", err)
	}
	project, err := store.GetProject(ctx, workline.ProjectID)
	if err != nil {
		return "message_target_unavailable", fmt.Errorf("load target conversation: %w", err)
	}
	if strings.TrimSpace(project.ArchivedAt) != "" {
		return "message_target_rejected", errors.New("target conversation is archived")
	}
	if project.Status != "active" || project.FlowMode == db.ProjectFlowModeConversation {
		return "message_target_unavailable", errors.New("target conversation was not found")
	}
	return "", nil
}

// narrowestPermissionCap resolves the permission ceiling for the target's
// triggered run: the narrower of the target conversation's own mode and the
// sender's frozen cap. Unlike childPermissionCap it clamps instead of erroring,
// because the sender does not choose the target's configuration — it must
// simply never widen it.
func narrowestPermissionCap(targetMode, senderCap string) (string, error) {
	rank := func(mode string) int {
		switch strings.TrimSpace(mode) {
		case "readOnly":
			return 1
		case "acceptEdits", "default", "dontAsk":
			return 2
		case "bypassPermissions":
			return 3
		default:
			return 0
		}
	}
	targetRank := rank(targetMode)
	if targetRank == 0 {
		return "", errors.New("target conversation permission mode is invalid")
	}
	effective := targetRank
	if senderRank := rank(senderCap); strings.TrimSpace(senderCap) != "" {
		if senderRank == 0 {
			return "", errors.New("message task permission cap is invalid")
		}
		if senderRank < effective {
			effective = senderRank
		}
	}
	switch effective {
	case 1:
		return "readOnly", nil
	case 2:
		return "acceptEdits", nil
	default:
		return "bypassPermissions", nil
	}
}
