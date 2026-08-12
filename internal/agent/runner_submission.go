package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/skills"
)

type SourceSubmission struct {
	AgentID           string
	Prompt            string
	Source            string
	SourceID          string
	PermissionModeCap string
	DispatchID        string
	TriggerType       string
}

var serverSkillCommandPattern = regexp.MustCompile(`^(/[A-Za-z0-9][A-Za-z0-9_-]{0,62})(?:$|[ \t\r\n]+([\s\S]*))`)

func (r *Runner) SubmitUserMessage(ctx context.Context, agentID, text, createdBy string, attachments ...db.Attachment) (db.Message, error) {
	agent, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Message{}, err
	}
	return r.submitUserMessageWithMode(ctx, agentID, text, createdBy, runExecutionModeForAgent(agent), attachments...)
}

// SubmitUserMessageWithMode freezes an explicit capability boundary directly on
// the new Run. It never mutates the Agent's persisted default, so concurrent
// submissions cannot observe a transient Plan/Execute selection.
func (r *Runner) SubmitUserMessageWithMode(ctx context.Context, agentID, text, createdBy string, mode ExecutionMode, attachments ...db.Attachment) (db.Message, error) {
	return r.SubmitUserMessageWithModeAndPermissionCap(ctx, agentID, text, createdBy, mode, "", attachments...)
}

// SubmitUserMessageWithModeAndPermissionCap freezes a request-scoped
// permission ceiling on the durable Run. It never rewrites the Agent's default
// permission mode, so a restricted remote submission cannot affect later local
// work or a concurrent full session.
func (r *Runner) SubmitUserMessageWithModeAndPermissionCap(ctx context.Context, agentID, text, createdBy string, mode ExecutionMode, permissionModeCap string, attachments ...db.Attachment) (db.Message, error) {
	return r.SubmitUserMessageWithModePermissionCapAndSource(ctx, agentID, text, createdBy, mode, permissionModeCap, db.RunSourceManual, attachments...)
}

// SubmitUserMessageWithModePermissionCapAndSource additionally freezes the Run
// source. Conversation runs are always execute-mode, read-only research runs;
// callers cannot widen them by supplying a project execution mode or cap.
func (r *Runner) SubmitUserMessageWithModePermissionCapAndSource(ctx context.Context, agentID, text, createdBy string, mode ExecutionMode, permissionModeCap, runSource string, attachments ...db.Attachment) (db.Message, error) {
	var durableMode string
	switch mode {
	case ExecutionModePlan:
		durableMode = db.RunExecutionModePlan
	case ExecutionModeExecute:
		durableMode = db.RunExecutionModeExecute
	default:
		return db.Message{}, fmt.Errorf("invalid execution mode %q", mode)
	}
	permissionModeCap = strings.TrimSpace(permissionModeCap)
	if permissionModeCap != "" && permissionModeCap != "readOnly" && permissionModeCap != "acceptEdits" {
		return db.Message{}, fmt.Errorf("invalid permission mode cap %q", permissionModeCap)
	}
	runSource = strings.TrimSpace(runSource)
	if runSource == "" {
		runSource = db.RunSourceManual
	}
	switch runSource {
	case db.RunSourceManual:
		// Keep the requested project capability boundary.
	case db.RunSourceConversation:
		durableMode = db.RunExecutionModeExecute
		permissionModeCap = "readOnly"
	default:
		return db.Message{}, fmt.Errorf("invalid user message run source %q", runSource)
	}
	return r.submitUserMessageWithModeAndPermissionCap(ctx, agentID, text, createdBy, durableMode, permissionModeCap, runSource, attachments...)
}

func (r *Runner) submitUserMessageWithMode(ctx context.Context, agentID, text, createdBy, mode string, attachments ...db.Attachment) (db.Message, error) {
	return r.submitUserMessageWithModeAndPermissionCap(ctx, agentID, text, createdBy, mode, "", db.RunSourceManual, attachments...)
}

func (r *Runner) submitUserMessageWithModeAndPermissionCap(ctx context.Context, agentID, text, createdBy, mode, permissionModeCap, runSource string, attachments ...db.Attachment) (db.Message, error) {
	if err := r.EnsureLocalExecution(ctx, agentID); err != nil {
		return db.Message{}, err
	}
	if mode != db.RunExecutionModePlan && mode != db.RunExecutionModeExecute {
		return db.Message{}, errors.New("invalid durable run execution mode")
	}
	if _, err := r.cancelPendingContinuationsForAgent(ctx, agentID, ContinuationBlockReasonPreempted); err != nil && !errors.Is(err, errContinuationStoreUnavailable) {
		return db.Message{}, err
	}
	contentText, commandText, err := r.expandServerSkillCommand(ctx, agentID, text)
	if err != nil {
		return db.Message{}, err
	}
	msg, err := r.store.AddMessageWithAttachments(ctx, db.Message{AgentID: agentID, Role: "user", ContentText: contentText, CommandText: commandText, CreatedBy: createdBy}, attachments)
	if err != nil {
		return db.Message{}, err
	}
	runRequest, err := r.bindPlanRunSnapshot(ctx, db.Run{AgentID: agentID, TriggerMessageID: msg.ID, Status: "pending", Source: runSource, ExecutionMode: mode, PermissionModeCap: permissionModeCap})
	if err != nil {
		return db.Message{}, err
	}
	runRequest, err = r.prepareContinuationRun(ctx, runRequest)
	if err != nil {
		return db.Message{}, err
	}
	run, err := r.store.CreateRun(ctx, runRequest)
	if err != nil {
		return db.Message{}, err
	}
	if err := r.store.AssignMessageRun(ctx, agentID, msg.ID, run.ID); err != nil {
		return db.Message{}, err
	}
	msg.RunID = run.ID
	r.publish(Event{Type: "message.created", AgentID: agentID, MessageID: msg.ID, Text: text, Data: mergeEventData(map[string]any{"attachments": len(msg.Attachments), "executionMode": mode}, run.ID)})
	r.scheduleConversationTitle(agentID, contentText)
	go r.runWithRun(context.Background(), agentID, run.ID, msg.ID)
	return msg, nil
}

// SubmitCorrection creates an immutable follow-up to a user message and starts a
// new run. It intentionally does not alter the source message or reuse its run.
func (r *Runner) SubmitCorrection(ctx context.Context, agentID, sourceMessageID, text, createdBy string, keepAttachmentIDs []string, attachments ...db.Attachment) (db.Message, error) {
	return r.SubmitCorrectionWithSource(ctx, agentID, sourceMessageID, text, createdBy, keepAttachmentIDs, db.RunSourceManual, attachments...)
}

func (r *Runner) SubmitCorrectionWithSource(ctx context.Context, agentID, sourceMessageID, text, createdBy string, keepAttachmentIDs []string, runSource string, attachments ...db.Attachment) (db.Message, error) {
	if err := r.EnsureLocalExecution(ctx, agentID); err != nil {
		return db.Message{}, err
	}
	runSource = strings.TrimSpace(runSource)
	if runSource == "" {
		runSource = db.RunSourceManual
	}
	if runSource != db.RunSourceManual && runSource != db.RunSourceConversation {
		return db.Message{}, fmt.Errorf("invalid correction run source %q", runSource)
	}
	agent, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Message{}, err
	}
	// CreateCorrectionWithRun predates runs.execution_mode and always creates an
	// execute run. A project correction must not silently widen plan mode, while
	// an ordinary conversation correction is independently forced to read-only.
	if agent.PlanMode && runSource != db.RunSourceConversation {
		return db.Message{}, errors.New("plan-mode corrections require an execution-mode-aware Store API")
	}
	contentText, commandText, err := r.expandServerSkillCommand(ctx, agentID, text)
	if err != nil {
		return db.Message{}, err
	}
	msg, run, err := r.store.CreateCorrectionWithRun(ctx, agentID, sourceMessageID, contentText, commandText, createdBy, keepAttachmentIDs, attachments)
	if err != nil {
		return db.Message{}, err
	}
	run, err = r.store.BindPendingCorrectionRun(ctx, run.ID, runSource)
	if err != nil {
		return db.Message{}, err
	}
	r.publish(Event{Type: "message.created", AgentID: agentID, MessageID: msg.ID, Text: text, Data: mergeEventData(map[string]any{"attachments": len(msg.Attachments), "correctionOfMessageId": sourceMessageID}, run.ID)})
	go r.runWithRun(context.Background(), agentID, run.ID, msg.ID)
	return msg, nil
}

// SubmitRerun re-runs an existing user message without writing anything to the
// conversation. See Store.CreateRerunForMessage for why a retry is not a
// correction.
func (r *Runner) SubmitRerun(ctx context.Context, agentID, messageID, runSource string) (db.Run, error) {
	if err := r.EnsureLocalExecution(ctx, agentID); err != nil {
		return db.Run{}, err
	}
	runSource = strings.TrimSpace(runSource)
	if runSource == "" {
		runSource = db.RunSourceManual
	}
	if runSource != db.RunSourceManual && runSource != db.RunSourceConversation {
		return db.Run{}, fmt.Errorf("invalid rerun source %q", runSource)
	}
	agent, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Run{}, err
	}
	// Same reasoning as SubmitCorrectionWithSource: the run this creates is an
	// execute run, so it must not become a way around plan mode.
	if agent.PlanMode && runSource != db.RunSourceConversation {
		return db.Run{}, errors.New("plan-mode reruns require an execution-mode-aware Store API")
	}
	run, err := r.store.CreateRerunForMessage(ctx, agentID, messageID)
	if err != nil {
		return db.Run{}, err
	}
	run, err = r.store.BindPendingCorrectionRun(ctx, run.ID, runSource)
	if err != nil {
		return db.Run{}, err
	}
	r.publish(Event{Type: "message.rerun", AgentID: agentID, MessageID: messageID, Data: mergeEventData(map[string]any{"messageId": messageID}, run.ID)})
	go r.runWithRun(context.Background(), agentID, run.ID, messageID)
	return run, nil
}

func (r *Runner) SubmitSchedule(ctx context.Context, schedule db.Schedule) (db.Run, error) {
	return r.SubmitScheduleDispatch(ctx, schedule, db.NewID())
}

func (r *Runner) SubmitScheduleDispatch(ctx context.Context, schedule db.Schedule, dispatchID string) (db.Run, error) {
	return r.SubmitSource(ctx, SourceSubmission{
		AgentID:           schedule.AgentID,
		Prompt:            schedule.Prompt,
		Source:            "schedule",
		SourceID:          schedule.ID,
		PermissionModeCap: schedule.PermissionMode,
		DispatchID:        dispatchID,
		TriggerType:       "scheduled",
	})
}

func (r *Runner) SubmitScheduleRun(ctx context.Context, schedule db.Schedule) (db.Run, error) {
	return r.SubmitSchedule(ctx, schedule)
}

func (r *Runner) SubmitInternal(ctx context.Context, agentID, sourceID, prompt, permissionModeCap string) (db.Run, error) {
	return r.SubmitSource(ctx, SourceSubmission{
		AgentID:           agentID,
		Prompt:            prompt,
		Source:            "internal",
		SourceID:          sourceID,
		PermissionModeCap: permissionModeCap,
		TriggerType:       "internal",
	})
}

func (r *Runner) SubmitSource(ctx context.Context, submission SourceSubmission) (db.Run, error) {
	if r == nil || r.store == nil {
		return db.Run{}, errors.New("agent runner is not initialized")
	}
	submission.AgentID = strings.TrimSpace(submission.AgentID)
	submission.Prompt = strings.TrimSpace(submission.Prompt)
	submission.Source = strings.TrimSpace(submission.Source)
	submission.SourceID = strings.TrimSpace(submission.SourceID)
	submission.PermissionModeCap = strings.TrimSpace(submission.PermissionModeCap)
	submission.DispatchID = strings.TrimSpace(submission.DispatchID)
	submission.TriggerType = strings.TrimSpace(submission.TriggerType)
	if submission.AgentID == "" || submission.Prompt == "" || submission.SourceID == "" {
		return db.Run{}, errors.New("agent, prompt, and source id are required")
	}
	switch submission.Source {
	case "schedule":
		if submission.DispatchID == "" {
			return db.Run{}, errors.New("scheduled source requires a dispatch id")
		}
		if submission.TriggerType != "scheduled" {
			return db.Run{}, errors.New("scheduled source requires scheduled trigger type")
		}
	case "internal":
		if submission.TriggerType != "internal" {
			return db.Run{}, errors.New("internal source requires internal trigger type")
		}
	default:
		return db.Run{}, errors.New("source must be schedule or internal")
	}
	if len(submission.DispatchID) > 256 {
		return db.Run{}, errors.New("dispatch id exceeds size limit")
	}
	// A subagent dispatch may carry bypassPermissions, because its cap is
	// derived from the parent's own mode by childPermissionCap, which refuses to
	// widen: only a parent already running under bypassPermissions can produce
	// it. Schedules keep the narrower contract -- their permission mode is
	// independently restricted to readOnly/acceptEdits before dispatch, and an
	// unattended trigger nobody chose per-run must not be able to claim bypass.
	switch submission.PermissionModeCap {
	case "readOnly", "acceptEdits":
	case "bypassPermissions":
		if submission.Source != "internal" {
			return db.Run{}, errors.New("permission mode cap must be readOnly or acceptEdits")
		}
	default:
		return db.Run{}, errors.New("permission mode cap must be readOnly or acceptEdits")
	}
	if err := ctx.Err(); err != nil {
		return db.Run{}, err
	}
	if err := r.EnsureLocalExecution(ctx, submission.AgentID); err != nil {
		return db.Run{}, err
	}
	agent, err := r.store.GetAgent(ctx, submission.AgentID)
	if err != nil {
		return db.Run{}, err
	}
	if submission.DispatchID != "" {
		existing, found, err := r.runForDispatch(ctx, submission.DispatchID)
		if err != nil {
			return db.Run{}, err
		}
		if found {
			if existing.AgentID != submission.AgentID || existing.Source != submission.Source || existing.SourceID != submission.SourceID || existing.TriggerType != submission.TriggerType {
				return db.Run{}, fmt.Errorf("%w: dispatch id belongs to another submission", db.ErrConflict)
			}
			return existing, nil
		}
	}

	runRequest, err := r.bindPlanRunSnapshot(ctx, db.Run{
		AgentID:           submission.AgentID,
		Status:            "pending",
		Source:            submission.Source,
		SourceID:          submission.SourceID,
		PermissionModeCap: submission.PermissionModeCap,
		DispatchID:        submission.DispatchID,
		TriggerType:       submission.TriggerType,
		ExecutionMode:     runExecutionModeForAgent(agent),
	})
	if err != nil {
		return db.Run{}, err
	}
	runRequest, err = r.prepareContinuationRun(ctx, runRequest)
	if err != nil {
		return db.Run{}, err
	}

	r.runMu.Lock()
	if r.running == nil {
		r.running = make(map[string]*activeRun)
	}
	if r.running[submission.AgentID] != nil {
		r.runMu.Unlock()
		return db.Run{}, ErrAgentBusy
	}
	if _, compacting := r.compacting[submission.AgentID]; compacting {
		r.runMu.Unlock()
		return db.Run{}, ErrAgentBusy
	}
	var durableBusy int
	if err := r.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE agent_id = ? AND status IN ('pending','running')`, submission.AgentID).Scan(&durableBusy); err != nil {
		r.runMu.Unlock()
		return db.Run{}, err
	}
	if durableBusy > 0 {
		r.runMu.Unlock()
		return db.Run{}, ErrAgentBusy
	}
	msg, err := r.store.AddMessage(ctx, db.Message{AgentID: submission.AgentID, Role: "user", ContentText: submission.Prompt})
	if err != nil {
		r.runMu.Unlock()
		return db.Run{}, err
	}
	runRequest.TriggerMessageID = msg.ID
	run, err := r.store.CreateRun(ctx, runRequest)
	if err != nil {
		r.runMu.Unlock()
		return db.Run{}, err
	}
	if err := r.store.AssignMessageRun(ctx, submission.AgentID, msg.ID, run.ID); err != nil {
		r.runMu.Unlock()
		return db.Run{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	active := &activeRun{cancel: cancel, runID: run.ID, triggerMessageID: msg.ID}
	r.running[submission.AgentID] = active
	r.runMu.Unlock()

	msg.RunID = run.ID
	r.publish(Event{Type: "message.created", AgentID: submission.AgentID, MessageID: msg.ID, Text: submission.Prompt, Data: mergeEventData(map[string]any{"source": submission.Source, "sourceId": submission.SourceID}, run.ID)})
	go r.executeRegisteredRun(runCtx, submission.AgentID, active)
	return run, nil
}

func (r *Runner) runForDispatch(ctx context.Context, dispatchID string) (db.Run, bool, error) {
	var runID, agentID string
	err := r.store.DB().QueryRowContext(ctx, `SELECT id, agent_id FROM runs WHERE dispatch_id = ?`, dispatchID).Scan(&runID, &agentID)
	if db.IsNotFound(err) {
		return db.Run{}, false, nil
	}
	if err != nil {
		return db.Run{}, false, err
	}
	run, err := r.store.GetRun(ctx, agentID, runID)
	if err != nil {
		return db.Run{}, false, err
	}
	return run, true, nil
}

func (r *Runner) expandServerSkillCommand(ctx context.Context, values ...string) (contentText, commandText string, err error) {
	if len(values) == 0 || len(values) > 2 {
		return "", "", errors.New("skill command expansion requires text")
	}
	agentID, text := "", values[0]
	if len(values) == 2 {
		agentID, text = values[0], values[1]
	}
	match := serverSkillCommandPattern.FindStringSubmatch(text)
	if match == nil {
		return text, "", nil
	}
	var skill db.Skill
	if agentID == "" {
		skill, err = r.store.GetSkillByCommand(ctx, match[1])
	} else {
		skill, err = r.store.ResolveSkillByAgentAndCommand(ctx, agentID, match[1])
	}
	if db.IsNotFound(err) {
		return text, "", nil
	}
	if err != nil {
		return "", "", err
	}
	if err := validateServerSkillInvocation(skill); err != nil {
		return "", "", err
	}
	contentText = skill.Prompt
	if args := strings.TrimSpace(match[2]); args != "" {
		contentText += "\n\nUser arguments:\n" + args
	}
	return contentText, text, nil
}

func validateServerSkillInvocation(skill db.Skill) error {
	unavailable := func(reason string) error {
		return fmt.Errorf("%w: server skill %s %s", db.ErrConflict, skill.Command, reason)
	}
	if !skill.Enabled {
		return unavailable("is disabled")
	}
	if skill.ScannerVersion != skills.ScannerVersion {
		return unavailable("requires scanner revalidation")
	}
	normalized, err := skills.Normalize(skills.Skill{Name: skill.Name, Command: skill.Command, Description: skill.Description, Prompt: skill.Prompt})
	if err != nil || normalized.Name != skill.Name || normalized.Command != skill.Command || normalized.Description != skill.Description || normalized.Prompt != skill.Prompt {
		return unavailable("has invalid stored content")
	}
	result := skills.Scan(normalized)
	if result.Hash != skill.ContentHash {
		return unavailable("has stale content metadata")
	}
	if result.Verdict != skill.ScanVerdict {
		return unavailable("has stale scan metadata")
	}
	switch skill.ScanVerdict {
	case skills.VerdictSafe:
		return nil
	case skills.VerdictReview:
		acknowledgedAt := strings.TrimSpace(skill.RiskAcknowledgedAt)
		acknowledgedBy := strings.TrimSpace(skill.RiskAcknowledgedBy)
		if acknowledgedAt == "" || acknowledgedBy == "" || len(acknowledgedBy) > 200 || strings.TrimSpace(skill.RiskAcknowledgedHash) != skill.ContentHash {
			return unavailable("requires acknowledgement for the current content")
		}
		if _, err := time.Parse(time.RFC3339Nano, acknowledgedAt); err != nil {
			return unavailable("has an invalid risk acknowledgement")
		}
		return nil
	case skills.VerdictBlocked:
		return unavailable("is blocked")
	default:
		return unavailable("has an invalid scan verdict")
	}
}
