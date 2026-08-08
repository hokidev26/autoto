package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"autoto/internal/db"
	"autoto/internal/hooks"
	"autoto/internal/providers"
	"autoto/internal/review"
	"autoto/internal/tools"
)

const (
	continuationModeOff  = "off"
	continuationModeSafe = "safe"

	continuationReasonMaxOutputTokens = "max_output_tokens"
	continuationReasonSegmentTurns    = "segment_turn_limit"
	continuationReasonBackgroundTask  = "background_task_wait"
	continuationReasonProviderError   = "provider_error"

	// Consecutive upstream failures to absorb before giving the run back to the
	// user. Small on purpose: a provider that fails four times in a row with
	// backoff is not a blip, and the user is better served by the error than by a
	// run that keeps retrying invisibly.
	maxProviderErrorRetries = 4

	providerErrorRetryBaseDelay = 2 * time.Second
	providerErrorRetryMaxDelay  = 30 * time.Second

	// How long a segment may go without finishing before the run says it is still
	// working. Long enough that a normal build or test run does not trip it.
	stallWarningInterval = 3 * time.Minute

	conversationSystemBoundary = "General conversation context is active. Respond as a conversational and research assistant, not as a project workspace agent. Do not inspect, search, read, modify, execute, or otherwise interact with local project or workspace resources. Only the explicitly provided conversation, attachments, and public-web research tools may be used."
)

var errContinuationStoreUnavailable = errors.New("continuation store APIs are unavailable")

type continuationContextKey uint8

const continuationBackgroundTaskContextKey continuationContextKey = iota

type continuationStore interface {
	RecordRunSegmentUsage(context.Context, string, int64, int64, int64, int64, int64) (db.Run, error)
	MarkRunContinuationPending(context.Context, string, db.RunContinuationPendingInput) (db.Run, error)
	ResumeContinuationRun(context.Context, string, int64) (db.Run, error)
	ListContinuationPendingRuns(context.Context, int) ([]db.Run, error)
	CancelContinuationRun(context.Context, string, int64, string) (db.Run, error)
}

func (r *Runner) continuationStore() (continuationStore, error) {
	if r == nil || r.store == nil {
		return nil, errContinuationStoreUnavailable
	}
	store, ok := any(r.store).(continuationStore)
	if !ok {
		return nil, errContinuationStoreUnavailable
	}
	return store, nil
}

func (r *Runner) prepareContinuationRun(ctx context.Context, run db.Run) (db.Run, error) {
	limits := r.currentContinuationLimits()
	if strings.TrimSpace(run.ID) == "" {
		run.ID = db.NewID()
	}
	r.freezeContinuationLimits(run.ID, limits)
	if run.ExecutionMode == db.RunExecutionModePlan || isConversationRun(run) {
		run.AutoContinuationMode = continuationModeOff
	} else if strings.TrimSpace(run.AutoContinuationMode) == "" {
		run.AutoContinuationMode = limits.mode
	}
	// durableBudget for the same reason as the three budgets below: segmentTurns
	// is -1 when no per-segment ceiling is configured, which is the shipped
	// default, and the runs table rejects negatives. Assigning the raw limit
	// here made CreateRun fail with "run continuation counters must not be
	// negative" on the first message of a stock install.
	if run.ContinuationSegmentTurns <= 0 {
		run.ContinuationSegmentTurns = durableBudget(limits.segmentTurns)
	}
	// Budgets persist as 0 for "no ceiling" rather than -1. The runs table has
	// CHECK (max_* >= 0) constraints and 43 columns with 9 indexes, so widening
	// them would mean a full table rebuild for no behavioural gain: 0 is already
	// meaningless as an actual ceiling, and loadContinuationRunState reads it
	// back as unlimited. -1 stays the in-memory encoding only.
	if run.MaxContinuations <= 0 {
		run.MaxContinuations = durableBudget(limits.maxContinuations)
	}
	if run.MaxTotalTurns <= 0 {
		run.MaxTotalTurns = durableBudget(limits.maxTotalTurns)
	}
	if run.MaxTotalTokens <= 0 {
		run.MaxTotalTokens = durableBudget(limits.maxTokens)
	}
	// An unlimited duration leaves DeadlineAt empty rather than storing a past
	// timestamp; every deadline check already treats the zero value as "no
	// deadline", so this is what disables the clock.
	if strings.TrimSpace(run.DeadlineAt) == "" && limits.maxDuration > 0 {
		run.DeadlineAt = time.Now().Add(limits.maxDuration).UTC().Format(time.RFC3339Nano)
	}
	if isConversationRun(run) {
		return run, nil
	}
	if snapshot, configured, err := r.currentPlanSnapshot(ctx, run.AgentID); err != nil {
		// A regular execute run may still run in a non-Git workspace. It must
		// fail closed if it later needs a continuation, but a snapshot provider
		// failure must not reject the initial model turn. Plan runs remain strict.
		if run.ExecutionMode == db.RunExecutionModePlan {
			return db.Run{}, fmt.Errorf("capture continuation safety snapshot: %w", err)
		}
	} else if configured {
		if run.PolicyGenerationSnapshot == 0 {
			run.PolicyGenerationSnapshot = snapshot.PolicyGenerationSnapshot
		}
		if run.AgentGenerationSnapshot == 0 {
			run.AgentGenerationSnapshot = snapshot.AgentGenerationSnapshot
		}
		if run.ToolCatalogDigest == "" {
			run.ToolCatalogDigest = snapshot.ToolCatalogDigest
		}
		if run.WorkspaceFingerprint == "" {
			run.WorkspaceFingerprint = snapshot.WorkspaceFingerprint
		}
	}
	return run, nil
}

func (r *Runner) run(ctx context.Context, agentID, runID string) error {
	return r.runContinuous(ctx, agentID, runID)
}

func (r *Runner) runContinuous(ctx context.Context, agentID, runID string) (runErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.store.SetAgentStatus(ctx, agentID, "running", ""); err != nil {
		return err
	}
	r.publish(Event{Type: "agent.started", AgentID: agentID, Data: runEventData(runID)})

	state, err := r.loadContinuationState(ctx, agentID, runID)
	if err != nil {
		return err
	}
	if taskID, _ := ctx.Value(continuationBackgroundTaskContextKey).(string); strings.TrimSpace(taskID) != "" {
		state.run.WaitingBackgroundTaskID = strings.TrimSpace(taskID)
	}
	if runID == "" {
		state.limits.mode = continuationModeOff
	}
	if state.run.ExecutionMode == db.RunExecutionModePlan || isConversationRun(state.run) {
		state.limits.mode = continuationModeOff
	}
	var lifecycleRun *lifecycleRunContext
	lifecycleAfterDispatched := false
	if strings.TrimSpace(runID) != "" {
		if _, _, err := r.prepareRunRuntimeSnapshot(ctx, agentID, runID); err != nil {
			return err
		}
		prepared, err := r.ensureLifecycleRun(ctx, agentID, runID)
		if err != nil {
			return err
		}
		lifecycleRun = &prepared
		defer func() {
			if lifecycleRun == nil || lifecycleAfterDispatched || runErr == nil {
				return
			}
			status := "error"
			if errors.Is(runErr, context.Canceled) {
				status = "interrupted"
			}
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			_ = r.dispatchRunLifecycle(cleanupContext, *lifecycleRun, hooks.EventRunAfter, status, runErr.Error())
			_ = r.closeLifecycleRun(cleanupContext, runID)
		}()
		if prepared.IsNew {
			if err := r.dispatchRunLifecycle(ctx, prepared, hooks.EventRunBefore, "running", ""); err != nil {
				return err
			}
		}
	}
	if !isConversationRun(state.run) && state.run.CheckpointState == db.RunCheckpointNone && state.run.ContinuationCount == 0 {
		agent, policy, policyErr := r.policyContext(ctx, agentID, runID)
		if policyErr != nil {
			return policyErr
		}
		if !policy.IsPlan() {
			r.captureRunCheckpoint(ctx, agent, runID)
			if runID != "" {
				state.run, _ = r.store.GetRunByID(ctx, runID)
			}
		}
	}

	continuationIndex := state.run.ContinuationCount
	// Consecutive provider failures only. A transient upstream fault (502, rate
	// limit, a relay running out of credit for a moment) used to end the run and
	// wait for the user to say "continue"; retrying it a bounded number of times
	// keeps long tasks moving. The counter resets on any successful segment so a
	// long run is not capped by failures it already recovered from, and a provider
	// that is genuinely down still surfaces its error after the last attempt.
	providerErrorRetries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.validateContinuationBoundary(ctx, state.run, continuationIndex > 0 || state.run.Status == "continuation_pending"); err != nil {
			r.publishContinuationBlocked(state.run, err.Error())
			return err
		}
		if continuationIndex > 0 {
			r.publishContinuationLifecycle("continuation_started", "agent.continuation_started", agentID, mergeEventData(map[string]any{
				"continuationCount": continuationIndex,
				"reason":            state.run.ContinuationReason,
			}, runID))
		}

		// Bind the run deadline to the segment context. Without this the deadline
		// is only consulted between segments, so a model stream or tool that hangs
		// mid-segment wedges the run indefinitely and MaxRunDurationMs never
		// applies. Cancellation propagates into provider calls and tool execution,
		// which both already honor ctx.
		outcome, segmentErr := r.runSegmentWithDeadline(ctx, state, continuationIndex)
		updatedRun, usageErr := r.recordSegmentUsage(ctx, state.run, outcome)
		if usageErr != nil {
			return usageErr
		}
		state.run = updatedRun
		if segmentErr != nil {
			// A provider fault is the one error worth retrying by itself: nothing
			// about the conversation is wrong, the upstream call just failed.
			if r.retryableProviderError(state, outcome, segmentErr) && providerErrorRetries < maxProviderErrorRetries {
				providerErrorRetries++
				resumeAfterID := strings.TrimSpace(outcome.resumeAfterID)
				r.publishContinuationLifecycle("provider_error_retry", "agent.provider_error_retry", agentID, mergeEventData(map[string]any{
					"attempt":     providerErrorRetries,
					"maxAttempts": maxProviderErrorRetries,
					"reason":      continuationReasonProviderError,
					"error":       boundedProviderErrorText(segmentErr),
					"inPlace":     resumeAfterID == "",
				}, runID))
				if err := r.waitProviderErrorBackoff(ctx, providerErrorRetries); err != nil {
					return err
				}
				// The failing call persisted nothing, so there is no durable resume
				// point and nothing to continue *from* -- but also nothing to skip or
				// duplicate. Re-run the same segment against the same messages
				// instead of ending the run. This is the first-segment 502 case,
				// where scheduleContinuation would refuse for want of a resume
				// message and the transient fault would surface as a dead run.
				if resumeAfterID == "" {
					continue
				}
				outcome.disposition = segmentContinue
				outcome.continuationReason = continuationReasonProviderError
				updated, scheduleErr := r.scheduleContinuation(ctx, state, outcome)
				if scheduleErr != nil {
					// Could not schedule a retry: report the original provider fault
					// rather than the scheduling detail, which is what the user needs.
					return segmentErr
				}
				resumed, resumeErr := r.resumeContinuationCAS(ctx, updated)
				if resumeErr != nil {
					return segmentErr
				}
				state.run = resumed
				continuationIndex = resumed.ContinuationCount
				continue
			}
			return segmentErr
		}
		providerErrorRetries = 0
		outcome.turns = 0
		outcome.inputTokens = 0
		outcome.outputTokens = 0
		switch outcome.disposition {
		case segmentComplete:
			if lifecycleRun != nil {
				lifecycleAfterDispatched = true
				if err := r.dispatchRunLifecycle(ctx, *lifecycleRun, hooks.EventRunAfter, "completed", ""); err != nil {
					_ = r.closeLifecycleRun(context.WithoutCancel(ctx), runID)
					return err
				}
				if err := r.closeLifecycleRun(ctx, runID); err != nil {
					return err
				}
			}
			return r.completeContinuousRun(ctx, agentID, runID, outcome)
		case segmentBudgetExhausted:
			r.publishContinuationLifecycle("budget_exhausted", "agent.budget_exhausted", agentID, mergeEventData(map[string]any{
				"reason":         outcome.continuationReason,
				"turnCount":      state.run.TurnCount,
				"consumedTokens": state.run.ConsumedInputTokens + state.run.ConsumedOutputTokens,
			}, runID))
			return fmt.Errorf("continuation budget exhausted: %s", outcome.continuationReason)
		case segmentContinue, segmentWait:
			updated, err := r.scheduleContinuation(ctx, state, outcome)
			if err != nil {
				return err
			}
			if outcome.disposition == segmentWait {
				_ = r.store.SetAgentStatus(ctx, agentID, "idle", "")
				return nil
			}
			resumed, err := r.resumeContinuationCAS(ctx, updated)
			if err != nil {
				return err
			}
			state.run = resumed
			continuationIndex = resumed.ContinuationCount
		}
	}
}

func (r *Runner) loadContinuationState(ctx context.Context, agentID, runID string) (continuationRunState, error) {
	limits, frozen := r.frozenContinuationLimits(runID)
	if !frozen {
		limits = r.currentContinuationLimits()
		r.freezeContinuationLimits(runID, limits)
	}
	state := continuationRunState{limits: limits}
	if strings.TrimSpace(runID) == "" {
		state.run = db.Run{AgentID: agentID, Status: "running", ExecutionMode: db.RunExecutionModeExecute, AutoContinuationMode: continuationModeOff, ContinuationSegmentTurns: limits.segmentTurns, MaxContinuations: limits.maxContinuations, MaxTotalTurns: limits.maxTotalTurns, MaxTotalTokens: limits.maxTokens}
		if limits.maxDuration > 0 {
			state.deadline = time.Now().Add(limits.maxDuration)
		}
		return state, nil
	}
	run, err := r.store.GetRun(ctx, agentID, runID)
	if err != nil {
		return continuationRunState{}, err
	}
	if run.Status == "pending" {
		if err := r.store.UpdateRunStatus(ctx, run.ID, "running", ""); err != nil {
			return continuationRunState{}, err
		}
		run, err = r.store.GetRun(ctx, agentID, runID)
		if err != nil {
			return continuationRunState{}, err
		}
	}
	legacyUnfrozen := run.MaxContinuations == 0 && run.MaxTotalTurns == 0 && run.MaxTotalTokens == 0 && strings.TrimSpace(run.DeadlineAt) == ""
	mode := strings.ToLower(strings.TrimSpace(run.AutoContinuationMode))
	if !legacyUnfrozen && (mode == continuationModeOff || mode == continuationModeSafe) {
		state.limits.mode = mode
	}
	if run.ContinuationSegmentTurns > 0 {
		state.limits.segmentTurns = run.ContinuationSegmentTurns
	}
	// A frozen run keeps the budgets it started with, including the unlimited
	// ones. 0 is how durableBudget spells "no ceiling", so it must resolve to
	// unlimited here rather than falling through to the current config — the
	// whole point of freezing is that a settings change mid-run cannot suddenly
	// impose a ceiling the run was not started under. legacyUnfrozen rows
	// predate the frozen budgets and do fall through.
	if !legacyUnfrozen {
		state.limits.maxContinuations = continuationUnlimitedContinuations
		if run.MaxContinuations > 0 {
			state.limits.maxContinuations = run.MaxContinuations
		}
		state.limits.maxTotalTurns = continuationUnlimited
		if run.MaxTotalTurns > 0 {
			state.limits.maxTotalTurns = run.MaxTotalTurns
		}
		state.limits.maxTokens = continuationUnlimited
		if run.MaxTotalTokens > 0 {
			state.limits.maxTokens = run.MaxTotalTokens
		}
	}
	var deadline time.Time
	if parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(run.DeadlineAt)); parseErr == nil {
		deadline = parsed
	} else if state.limits.maxDuration > 0 {
		deadline = time.Now().Add(state.limits.maxDuration)
	}
	state.run = run
	state.deadline = deadline
	return state, nil
}

// runSegmentWithDeadline enforces the run's wall-clock budget as a real context
// deadline for the duration of one segment. A zero or already-passed deadline
// leaves the context untouched so the existing between-segment budget check
// reports the stop reason instead of surfacing a bare cancellation.
func (r *Runner) runSegmentWithDeadline(ctx context.Context, state continuationRunState, continuationIndex int64) (segmentOutcome, error) {
	// Report-only watchdog. A segment that produces nothing for a long stretch is
	// indistinguishable from a finished run to anyone watching the UI, so say it
	// is still working. It deliberately does not cancel: a slow build or a large
	// download is not a hang, and killing it would lose real work. MaxRunDuration
	// remains the only thing that ends a run on time.
	stop := r.startSegmentStallWatchdog(ctx, state.run, continuationIndex)
	defer stop()
	if state.deadline.IsZero() || !time.Now().Before(state.deadline) {
		return r.runContinuationSegment(ctx, state, continuationIndex)
	}
	segmentCtx, cancel := context.WithDeadline(ctx, state.deadline)
	defer cancel()
	return r.runContinuationSegment(segmentCtx, state, continuationIndex)
}

// startSegmentStallWatchdog publishes agent.stalled every stallWarningInterval
// while a segment is still running, and returns a stop function. Repeating
// rather than firing once means a run that is wedged for an hour keeps saying so
// instead of going quiet after the first warning.
func (r *Runner) startSegmentStallWatchdog(ctx context.Context, run db.Run, continuationIndex int64) func() {
	if r == nil || strings.TrimSpace(run.AgentID) == "" {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(stallWarningInterval)
		defer ticker.Stop()
		started := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.publishContinuationLifecycle("stalled", "agent.stalled", run.AgentID, mergeEventData(map[string]any{
					"continuationCount": continuationIndex,
					"elapsedSeconds":    int64(time.Since(started).Seconds()),
				}, run.ID))
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func (r *Runner) runContinuationSegment(ctx context.Context, state continuationRunState, continuationIndex int64) (segmentOutcome, error) {
	run := state.run
	agentID, runID := run.AgentID, run.ID
	agent, policy, err := r.policyContext(ctx, agentID, runID)
	if err != nil {
		return segmentOutcome{}, err
	}
	runtimeSnapshot, _, err := r.prepareRunRuntimeSnapshot(ctx, agentID, runID)
	if err != nil {
		return segmentOutcome{}, err
	}
	agent.SystemPrompt = runtimeSnapshot.prompt.systemPrompt()
	promptMessages := runtimeSnapshot.prompt.userMessages()
	messages, err := r.store.ListMessagesWithAttachmentData(ctx, agentID)
	if err != nil {
		return segmentOutcome{}, err
	}
	provider, model, err := r.providers.Resolve(agent.Model)
	if err != nil {
		return segmentOutcome{}, err
	}
	toolSnapshot := runtimeSnapshot.tools
	toolSpecs := toolSnapshot.specs
	if !providers.CapabilitiesFor(provider).Tools {
		toolSpecs = nil
	}

	outcome := segmentOutcome{}
	if len(messages) > 0 {
		outcome.segmentStartMessageID = messages[len(messages)-1].ID
	}
	// segmentTurns <= 0 means no per-segment ceiling; the loop exits via an
	// internal stop reason, error, or a cross-segment budget (total turns /
	// continuations / wall clock).
	for turn := int64(0); state.limits.segmentTurns <= 0 || turn < state.limits.segmentTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return segmentOutcome{}, err
		}
		if err := r.store.RequireRunToolExecutionGroupsSettled(ctx, runID); err != nil {
			return outcome, fmt.Errorf("tool execution settlement barrier: %w", err)
		}
		if reason := continuationBudgetReason(state, outcome); reason != "" {
			outcome.disposition = segmentBudgetExhausted
			outcome.continuationReason = reason
			return outcome, nil
		}
		controls := r.buildTurnSystemControls(ctx, agent, run, messages, continuationIndex)
		controls.pipeline = r.toolOutputPipelineControl(agentID, runID)
		providerMessages, updatedAgent, err := r.managedContextForTurn(ctx, agent, messages, toolSpecs, controls, promptMessages)
		if err != nil {
			return segmentOutcome{}, err
		}
		agent = updatedAgent
		result, turnErr := r.runModelTurn(ctx, agentID, runID, provider, model, agent.SystemPrompt, providerMessages, toolSpecs, r.reasoningEffort(agent.ReasoningEffort), agent.FastMode)
		outcome.turns++
		outcome.inputTokens += maxInt64(result.Usage.InputTokens, 0)
		outcome.outputTokens += maxInt64(result.Usage.OutputTokens, 0)
		if turnErr != nil {
			// Mark the fault as a provider error so the retry path in
			// runContinuous can recognise it. Without this the reason stays empty,
			// retryableProviderError never matches, and a transient 502 ends the
			// run even though retrying was safe.
			outcome.continuationReason = continuationReasonProviderError
			if strings.TrimSpace(result.Text) != "" || len(result.ResponseBlocks) > 0 || len(result.ToolCalls) > 0 || len(result.GeneratedImages) > 0 {
				messageID, persistErr := r.persistPartialAssistant(ctx, agentID, runID, result, continuationReasonProviderError)
				if persistErr != nil {
					// A store failure is not a provider fault; retrying it would just
					// fail the same way. Drop the reason so the retry path declines.
					outcome.continuationReason = ""
					return outcome, persistErr
				}
				outcome.resumeAfterID = messageID
			}
			return outcome, turnErr
		}
		if err := ctx.Err(); err != nil {
			r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
			return outcome, err
		}

		if len(result.ToolCalls) == 0 {
			if isContinuationStopReason(result.StopReason) {
				messageID, persistErr := r.persistPartialAssistant(ctx, agentID, runID, result, normalizedContinuationStopReason(result.StopReason))
				if persistErr != nil {
					return outcome, persistErr
				}
				r.recordCompletedModelTurn(agentID, runID, messageID, provider.Name(), model, result)
				if policy.IsPlan() {
					return outcome, errors.New("plan draft output was truncated; automatic continuation is forbidden")
				}
				outcome.disposition = segmentContinue
				outcome.stopReason = result.StopReason
				outcome.continuationReason = normalizedContinuationStopReason(result.StopReason)
				outcome.resumeAfterID = messageID
				return outcome, nil
			}
			if !isTerminalStopReason(result.StopReason) {
				messageID, persistErr := r.persistPartialAssistant(ctx, agentID, runID, result, strings.TrimSpace(result.StopReason))
				if persistErr != nil {
					return outcome, persistErr
				}
				r.recordCompletedModelTurn(agentID, runID, messageID, provider.Name(), model, result)
				return outcome, fmt.Errorf("provider returned unknown stop reason %q", result.StopReason)
			}
			if r.toolOutputPipelineActive(agentID, runID) && len(result.GeneratedImages) == 0 {
				r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
				continue
			}
			if result.ImageGenerationStarted && len(result.GeneratedImages) == 0 && strings.TrimSpace(result.Text) == "" {
				r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
				outcome.disposition = segmentComplete
				outcome.stopReason = result.StopReason
				if len(messages) > 0 {
					outcome.resumeAfterID = messages[len(messages)-1].ID
				}
				return outcome, nil
			}
			assistantText := result.Text
			var planReview review.Result
			if policy.IsPlan() {
				assistantText, planReview, err = r.persistAndReviewPlan(ctx, policy, assistantText)
				if err != nil {
					r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
					return outcome, err
				}
			} else if assistantText == "" && len(result.GeneratedImages) == 0 {
				assistantText = "Done."
			}
			assistantMsg, persisted, err := r.persistAssistantResult(ctx, agentID, runID, result, assistantText, "completed", result.StopReason, "")
			if err != nil {
				r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
				return outcome, err
			}
			if !persisted {
				r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
				return outcome, errors.New("assistant result had no persistable content")
			}
			r.recordCompletedModelTurn(agentID, runID, assistantMsg.ID, provider.Name(), model, result)
			r.publish(Event{Type: "message.created", AgentID: agentID, MessageID: assistantMsg.ID, Text: assistantText, Data: runEventData(runID)})
			outcome.disposition = segmentComplete
			outcome.stopReason = result.StopReason
			outcome.resumeAfterID = assistantMsg.ID
			outcome.planReview = planReview
			return outcome, nil
		}

		if !isToolStopReason(result.StopReason) {
			messageID, persistErr := r.persistPartialAssistant(ctx, agentID, runID, result, strings.TrimSpace(result.StopReason))
			if persistErr != nil {
				return outcome, persistErr
			}
			r.recordCompletedModelTurn(agentID, runID, messageID, provider.Name(), model, result)
			return outcome, fmt.Errorf("provider returned unsafe tool stop reason %q", result.StopReason)
		}
		normalizedToolCalls := make([]providers.ToolCall, len(result.ToolCalls))
		ledgerItems := make([]db.ToolExecutionItemInput, len(result.ToolCalls))
		for index, call := range result.ToolCalls {
			normalizedToolCalls[index] = normalizeProviderToolCall(call)
			ledgerItems[index] = db.ToolExecutionItemInput{ToolUseID: normalizedToolCalls[index].ID, ToolName: normalizedToolCalls[index].Name}
		}
		result.ToolCalls = normalizedToolCalls
		assistantText := assistantToolUseText(result.Text, result.ToolCalls)
		assistantMsg, persisted, err := r.persistAssistantResult(ctx, agentID, runID, result, result.Text, "completed", result.StopReason, "")
		if err != nil {
			r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
			return outcome, err
		}
		if !persisted {
			r.recordCompletedModelTurn(agentID, runID, "", provider.Name(), model, result)
			return outcome, errors.New("tool-call assistant result had no persistable content")
		}
		toolGroup := db.ToolExecutionGroup{}
		if strings.TrimSpace(runID) != "" {
			toolGroup, err = r.store.CreateToolExecutionGroup(ctx, db.ToolExecutionGroupCreateInput{
				RunID:              runID,
				AssistantMessageID: assistantMsg.ID,
				ExpectedCount:      len(ledgerItems),
				Items:              ledgerItems,
			})
			if err != nil {
				r.recordCompletedModelTurn(agentID, runID, assistantMsg.ID, provider.Name(), model, result)
				return outcome, fmt.Errorf("persist tool execution group: %w", err)
			}
		}
		r.recordCompletedModelTurn(agentID, runID, assistantMsg.ID, provider.Name(), model, result)
		r.publish(Event{Type: "message.created", AgentID: agentID, MessageID: assistantMsg.ID, Text: assistantText, Data: mergeEventData(map[string]any{"toolCalls": len(result.ToolCalls), "generatedImages": len(result.GeneratedImages), "toolExecutionGroupId": toolGroup.ID}, runID)})
		messages = append(messages, assistantMsg)

		waitingTaskID := ""
		prefetched := r.prefetchToolCallResults(ctx, agentID, runID, result.ToolCalls, assistantMsg.ID, toolSnapshot.tools)
		for index, toolCall := range result.ToolCalls {
			if err := ctx.Err(); err != nil {
				return outcome, err
			}
			var rawToolResult tools.Result
			var executeErr error
			if prefetched != nil && prefetched[index].ran {
				rawToolResult, executeErr = prefetched[index].result, prefetched[index].executeErr
			} else {
				rawToolResult, executeErr = r.executeToolForLoop(ctx, agentID, runID, tools.Call{ID: toolCall.ID, Name: toolCall.Name, Input: toolCall.Input}, assistantMsg.ID, toolSnapshot.tools)
			}
			if executeErr != nil {
				rawToolResult = tools.Result{Output: executeErr.Error(), IsError: true}
			}
			modelToolResult := r.processToolResultForModel(agentID, runID, tools.Call{ID: toolCall.ID, Name: toolCall.Name, Input: toolCall.Input}, rawToolResult)
			toolResultBlock := providers.ContentBlock{Type: "tool_result", ToolUseID: toolCall.ID, ToolName: toolCall.Name, Output: modelToolResult.Output, IsError: modelToolResult.IsError}
			toolResultJSON, _ := json.Marshal([]providers.ContentBlock{toolResultBlock})
			toolResultText := toolResultMessageText(toolCall, modelToolResult)
			toolResultMsg := db.Message{AgentID: agentID, RunID: runID, Role: "user", ParentToolID: toolCall.ID, ContentText: toolResultText, ContentJSON: toolResultJSON}
			var toolMsg db.Message
			var err error
			if attachment, ok := toolResultImageAttachment(agentID, toolCall, rawToolResult); ok {
				toolMsg, err = r.store.AddMessageWithAttachments(ctx, toolResultMsg, []db.Attachment{attachment})
				// AddMessageWithAttachments strips Data/ModelData from the
				// returned attachment metadata (they are meant to be re-read from
				// the store, not held in memory). The rest of this segment keeps
				// building on the in-memory messages slice rather than
				// re-querying the store every turn, so without this the image
				// would only reach the model starting next segment. Restoring
				// the bytes we already normalized lets it reach the model on the
				// very next turn instead.
				if err == nil && len(toolMsg.Attachments) == 1 {
					toolMsg.Attachments[0].Data = attachment.Data
					toolMsg.Attachments[0].ModelData = attachment.ModelData
				}
			} else {
				toolMsg, err = r.store.AddMessage(ctx, toolResultMsg)
			}
			if err != nil {
				return outcome, err
			}
			terminalStatus, err := r.toolExecutionTerminalStatus(ctx, agentID, runID, assistantMsg.ID, toolCall.ID, modelToolResult)
			if err != nil {
				return outcome, err
			}
			summaryJSON, err := toolExecutionOutputSummaryJSON(modelToolResult)
			if err != nil {
				return outcome, err
			}
			if toolGroup.ID != "" {
				if _, err := r.store.RecordToolExecutionItemTerminal(ctx, toolGroup.ID, db.ToolExecutionItemTerminalInput{ToolUseID: toolCall.ID, Status: terminalStatus, ResultMessageID: toolMsg.ID, OutputSummaryJSON: summaryJSON}); err != nil {
					return outcome, fmt.Errorf("persist terminal tool execution item %s: %w", toolCall.ID, err)
				}
			}
			r.publish(Event{Type: "message.created", AgentID: agentID, MessageID: toolMsg.ID, Text: toolResultText, Data: mergeEventData(map[string]any{"parentToolUseId": toolCall.ID, "toolName": toolCall.Name, "isError": modelToolResult.IsError, "toolExecutionGroupId": toolGroup.ID}, runID)})
			messages = append(messages, toolMsg)
			outcome.resumeAfterID = toolMsg.ID
			if taskID, waits, boundaryErr := backgroundTaskContinuationBoundary(rawToolResult); boundaryErr != nil {
				return outcome, boundaryErr
			} else if waits {
				if waitingTaskID != "" && waitingTaskID != taskID {
					return outcome, errors.New("one model turn requested multiple resumeParent background task boundaries")
				}
				waitingTaskID = taskID
			}
		}
		if toolGroup.ID != "" {
			settledGroup, err := r.store.SettleToolExecutionGroup(ctx, toolGroup.ID)
			if err != nil {
				return outcome, fmt.Errorf("settle tool execution group %s: %w", toolGroup.ID, err)
			}
			if settledGroup.Status != db.ToolExecutionGroupStatusSettled || len(settledGroup.Items) != settledGroup.ExpectedCount {
				return outcome, errors.New("tool execution group settlement did not produce a complete durable boundary")
			}
		}
		if waitingTaskID != "" {
			outcome.disposition = segmentWait
			outcome.stopReason = result.StopReason
			outcome.continuationReason = continuationReasonBackgroundTask
			outcome.waitingTaskID = waitingTaskID
			return outcome, nil
		}
	}
	outcome.disposition = segmentContinue
	outcome.continuationReason = continuationReasonSegmentTurns
	outcome.stopReason = continuationReasonSegmentTurns
	return outcome, nil
}

func continuationBudgetReason(state continuationRunState, outcome segmentOutcome) string {
	if !state.deadline.IsZero() && !time.Now().Before(state.deadline) {
		return "deadline"
	}
	if state.limits.maxTotalTurns != continuationUnlimited && state.run.TurnCount+outcome.turns >= state.limits.maxTotalTurns {
		return "max_total_turns"
	}
	if state.limits.maxTokens != continuationUnlimited {
		consumed := state.run.ConsumedInputTokens + state.run.ConsumedOutputTokens + outcome.inputTokens + outcome.outputTokens
		if consumed >= state.limits.maxTokens {
			return "max_total_tokens"
		}
	}
	return ""
}

// continuationBudgetDetail turns a bare budget reason into something a user can
// act on. The bare reason ("max_total_tokens") gave no indication of which
// limit was hit, how close the run was, or where the limit is configured, so an
// interrupted long task looked like an unexplained failure.
func continuationBudgetDetail(state continuationRunState, outcome segmentOutcome, reason string) string {
	switch reason {
	case "max_total_tokens":
		consumed := state.run.ConsumedInputTokens + state.run.ConsumedOutputTokens + outcome.inputTokens + outcome.outputTokens
		input := state.run.ConsumedInputTokens + outcome.inputTokens
		output := state.run.ConsumedOutputTokens + outcome.outputTokens
		return fmt.Sprintf(
			"max_total_tokens (used %d of %d; input %d, output %d, over %d turns). Every turn resends the conversation, so input dominates this total. Raise agent.maxRunTokens or lower agent.contextTokenLimit in settings.",
			consumed, state.limits.maxTokens, input, output, state.run.TurnCount+outcome.turns,
		)
	case "max_total_turns":
		return fmt.Sprintf("max_total_turns (%d of %d). Raise agent.maxTotalTurns in settings, or set it to -1 for no limit.",
			state.run.TurnCount+outcome.turns, state.limits.maxTotalTurns)
	case "max_continuations":
		return fmt.Sprintf("max_continuations (%d of %d segment restarts). Raise agent.maxContinuations in settings, or set it to -1 for no limit.",
			state.run.ContinuationCount, state.limits.maxContinuations)
	case "deadline":
		return "deadline (run exceeded agent.maxRunDurationMs). Raise that limit in settings, or set it to -1 for no limit."
	default:
		return reason
	}
}

func (r *Runner) recordSegmentUsage(ctx context.Context, run db.Run, outcome segmentOutcome) (db.Run, error) {
	if strings.TrimSpace(run.ID) == "" {
		run.TurnCount += outcome.turns
		run.ConsumedInputTokens += outcome.inputTokens
		run.ConsumedOutputTokens += outcome.outputTokens
		return run, nil
	}
	if outcome.turns == 0 && outcome.inputTokens == 0 && outcome.outputTokens == 0 {
		return run, nil
	}
	store, err := r.continuationStore()
	if err != nil {
		return db.Run{}, err
	}
	return store.RecordRunSegmentUsage(ctx, run.ID, run.ContinuationCount, run.TurnCount, outcome.turns, outcome.inputTokens, outcome.outputTokens)
}

func (r *Runner) scheduleContinuation(ctx context.Context, state continuationRunState, outcome segmentOutcome) (db.Run, error) {
	run := state.run
	if state.limits.mode != continuationModeSafe {
		if outcome.continuationReason == continuationReasonSegmentTurns {
			return db.Run{}, fmt.Errorf("agent reached max turns (%d) while model kept requesting tools", state.limits.segmentTurns)
		}
		return db.Run{}, fmt.Errorf("automatic continuation is disabled at %s", outcome.continuationReason)
	}
	if !safeContinuationReason(outcome.continuationReason) {
		return db.Run{}, fmt.Errorf("unsafe continuation reason %q", outcome.continuationReason)
	}
	if run.ContinuationCount >= state.limits.maxContinuations {
		r.publishContinuationLifecycle("budget_exhausted", "agent.budget_exhausted", run.AgentID, mergeEventData(map[string]any{"reason": "max_continuations"}, run.ID))
		return db.Run{}, fmt.Errorf("continuation budget exhausted: %s", continuationBudgetDetail(state, outcome, "max_continuations"))
	}
	if reason := continuationBudgetReason(state, outcome); reason != "" {
		r.publishContinuationLifecycle("budget_exhausted", "agent.budget_exhausted", run.AgentID, mergeEventData(map[string]any{"reason": reason}, run.ID))
		return db.Run{}, fmt.Errorf("continuation budget exhausted: %s", continuationBudgetDetail(state, outcome, reason))
	}
	if strings.TrimSpace(outcome.resumeAfterID) == "" {
		return db.Run{}, errors.New("continuation boundary has no durable resume message")
	}
	if err := r.validateNoMessagePreemption(ctx, run, outcome.segmentStartMessageID); err != nil {
		r.publishContinuationBlocked(run, err.Error())
		return db.Run{}, err
	}
	if err := r.validateContinuationBoundary(ctx, run, false); err != nil {
		r.publishContinuationBlocked(run, err.Error())
		return db.Run{}, err
	}
	store, err := r.continuationStore()
	if err != nil {
		return db.Run{}, err
	}
	updated, err := store.MarkRunContinuationPending(ctx, run.ID, db.RunContinuationPendingInput{
		ExpectedContinuationCount: run.ContinuationCount,
		TurnCount:                 run.TurnCount + outcome.turns,
		ConsumedInputTokens:       run.ConsumedInputTokens + outcome.inputTokens,
		ConsumedOutputTokens:      run.ConsumedOutputTokens + outcome.outputTokens,
		ResumeAfterMessageID:      outcome.resumeAfterID,
		LastStopReason:            outcome.stopReason,
		ContinuationReason:        outcome.continuationReason,
		WaitingBackgroundTaskID:   outcome.waitingTaskID,
	})
	if err != nil {
		return db.Run{}, err
	}
	r.publishContinuationLifecycle("continuation_scheduled", "agent.continuation_scheduled", run.AgentID, mergeEventData(map[string]any{
		"continuationCount":       updated.ContinuationCount,
		"reason":                  updated.ContinuationReason,
		"resumeAfterMessageId":    updated.ResumeAfterMessageID,
		"waitingBackgroundTaskId": updated.WaitingBackgroundTaskID,
	}, run.ID))
	return updated, nil
}

func (r *Runner) resumeContinuationCAS(ctx context.Context, run db.Run) (db.Run, error) {
	if err := r.validateContinuationBoundary(ctx, run, true); err != nil {
		r.publishContinuationBlocked(run, err.Error())
		if store, storeErr := r.continuationStore(); storeErr == nil {
			_, _ = store.CancelContinuationRun(context.WithoutCancel(ctx), run.ID, run.ContinuationCount, err.Error())
		}
		return db.Run{}, err
	}
	store, err := r.continuationStore()
	if err != nil {
		return db.Run{}, err
	}
	updated, err := store.ResumeContinuationRun(ctx, run.ID, run.ContinuationCount)
	if err != nil {
		return db.Run{}, err
	}
	return updated, nil
}

func (r *Runner) validateNoMessagePreemption(ctx context.Context, run db.Run, segmentStartMessageID string) error {
	messages, err := r.store.ListMessages(ctx, run.AgentID)
	if err != nil {
		return err
	}
	start := -1
	if strings.TrimSpace(segmentStartMessageID) == "" {
		start = 0
	} else {
		for index, message := range messages {
			if message.ID == segmentStartMessageID {
				start = index + 1
				break
			}
		}
	}
	if start < 0 {
		return errors.New("segment start message disappeared")
	}
	for _, message := range messages[start:] {
		if message.RunID != run.ID {
			return errors.New("a new message preempted the continuation segment")
		}
	}
	return nil
}

func (r *Runner) validateContinuationBoundary(ctx context.Context, run db.Run, continuation bool) error {
	if strings.TrimSpace(run.ID) == "" {
		return nil
	}
	expectedStatus := "running"
	if run.Status == "continuation_pending" {
		expectedStatus = "continuation_pending"
	}
	if run.Status != expectedStatus {
		return fmt.Errorf("run status changed to %q", run.Status)
	}
	agent, err := r.store.GetAgent(ctx, run.AgentID)
	if err != nil {
		return fmt.Errorf("load agent snapshot: %w", err)
	}
	generations, err := r.store.GetPermissionGenerations(ctx, run.AgentID)
	if err != nil {
		return fmt.Errorf("load permission generations: %w", err)
	}
	if generations.Execution != run.ExecutionGeneration {
		return errors.New("run was preempted by a newer execution generation")
	}
	if generations.Entity != run.AgentGenerationSnapshot || generations.Policy != run.PolicyGenerationSnapshot {
		return errors.New("agent or policy generation changed")
	}
	if normalizedExecutionDeviceID(agent.ExecutionDeviceID) != normalizedExecutionDeviceID(run.ExecutionDeviceID) {
		return errors.New("execution device changed")
	}
	if isConversationRun(run) {
		if run.ExecutionMode != db.RunExecutionModeExecute || run.PermissionModeCap != "readOnly" {
			return errors.New("conversation run capability boundary changed")
		}
		if run.AutoContinuationMode != continuationModeOff && continuation {
			return errors.New("conversation runs cannot continue automatically")
		}
	} else {
		if snapshot, configured, snapshotErr := r.currentPlanSnapshot(ctx, run.AgentID); snapshotErr != nil {
			if continuation {
				return fmt.Errorf("load continuation safety snapshot: %w", snapshotErr)
			}
		} else if configured {
			if continuation && (strings.TrimSpace(run.ToolCatalogDigest) == "" || strings.TrimSpace(run.WorkspaceFingerprint) == "") {
				return errors.New("continuation snapshot is missing")
			}
			if strings.TrimSpace(run.ToolCatalogDigest) != "" || strings.TrimSpace(run.WorkspaceFingerprint) != "" {
				if snapshot.PolicyGenerationSnapshot != run.PolicyGenerationSnapshot || snapshot.AgentGenerationSnapshot != run.AgentGenerationSnapshot {
					return errors.New("continuation generation snapshot changed")
				}
				if snapshot.ToolCatalogDigest != run.ToolCatalogDigest {
					return errors.New("tool catalog snapshot changed or is missing")
				}
				if snapshot.WorkspaceFingerprint != run.WorkspaceFingerprint {
					return errors.New("workspace fingerprint changed or is missing")
				}
			}
		} else if continuation && (strings.TrimSpace(run.ToolCatalogDigest) != "" || strings.TrimSpace(run.WorkspaceFingerprint) != "") {
			return errors.New("continuation snapshot provider is unavailable")
		}
		if strings.TrimSpace(run.PlanID) != "" {
			plan, planErr := r.store.GetPlanByID(ctx, run.PlanID)
			if planErr != nil {
				return fmt.Errorf("load approved plan: %w", planErr)
			}
			if plan.AgentID != run.AgentID || plan.Status != db.PlanStatusExecuting || !samePlanSnapshot(plan, db.PlanSnapshot{
				PolicyGenerationSnapshot: run.PolicyGenerationSnapshot,
				AgentGenerationSnapshot:  run.AgentGenerationSnapshot,
				ToolCatalogDigest:        run.ToolCatalogDigest,
				WorkspaceFingerprint:     run.WorkspaceFingerprint,
			}) {
				return errors.New("approved plan snapshot or execution state changed")
			}
		} else if run.ExecutionMode == db.RunExecutionModePlan && continuation {
			return errors.New("plan draft runs cannot continue")
		}
	}
	switch run.CheckpointState {
	case db.RunCheckpointNone, db.RunCheckpointTracking, db.RunCheckpointReady:
	default:
		return fmt.Errorf("checkpoint state %q is not a complete continuation boundary", run.CheckpointState)
	}
	if err := r.store.RequireRunToolExecutionGroupsSettled(ctx, run.ID); err != nil {
		return fmt.Errorf("tool execution settlement barrier: %w", err)
	}
	calls, err := r.store.ListToolCallsByRun(ctx, run.AgentID, run.ID)
	if err != nil {
		return fmt.Errorf("load run tool calls: %w", err)
	}
	for _, call := range calls {
		switch call.Status {
		case "pending_approval", "approved", "running":
			return fmt.Errorf("tool call %s is still %s", call.ToolUseID, call.Status)
		}
	}
	if continuation {
		messages, listErr := r.store.ListMessages(ctx, run.AgentID)
		if listErr != nil {
			return fmt.Errorf("load continuation message cursor: %w", listErr)
		}
		if len(messages) == 0 || messages[len(messages)-1].ID != run.ResumeAfterMessageID {
			return errors.New("a newer message preempted the continuation boundary")
		}
	}
	return nil
}

func (r *Runner) completeContinuousRun(ctx context.Context, agentID, runID string, outcome segmentOutcome) error {
	if runID != "" {
		r.captureRunEndHead(runID)
		if err := r.store.CompleteRun(ctx, runID, "completed", ""); err != nil {
			return err
		}
		r.closeToolOutputPipelineRun(agentID, runID)
		r.closeRunRuntimeSnapshot(runID, agentID)
	}
	r.publishPlanRunStatus(ctx, runID, "plan.executed")
	data := map[string]any{"stopReason": outcome.stopReason}
	if outcome.planReview.Verdict != "" {
		data["reviewVerdict"] = outcome.planReview.Verdict
		data["reviewReason"] = outcome.planReview.Reason
	}
	r.publish(Event{Type: "agent.done", AgentID: agentID, Data: mergeEventData(data, runID)})
	r.notify(NotificationEvent{Event: "completed", RunID: runID, AgentID: agentID, Status: "completed"})
	return r.store.SetAgentStatus(ctx, agentID, "idle", "")
}

func (r *Runner) persistPartialAssistant(ctx context.Context, agentID, runID string, result modelTurnResult, stopReason string) (string, error) {
	kind := "partial"
	if normalized := strings.TrimSpace(stopReason); normalized != "" {
		kind += ":" + normalized
	}
	message, persisted, err := r.persistAssistantResult(ctx, agentID, runID, result, result.Text, "partial", stopReason, kind)
	if err != nil {
		return "", err
	}
	if !persisted {
		messages, err := r.store.ListMessages(ctx, agentID)
		if err != nil || len(messages) == 0 {
			return "", err
		}
		return messages[len(messages)-1].ID, nil
	}
	text := assistantToolUseText(result.Text, result.ToolCalls)
	r.publish(Event{Type: "message.created", AgentID: agentID, MessageID: message.ID, Text: text, Data: mergeEventData(map[string]any{"partial": true, "stopReason": stopReason, "toolCalls": len(result.ToolCalls), "generatedImages": len(result.GeneratedImages)}, runID)})
	return message.ID, nil
}

func continuationControlMessage(run db.Run, continuationIndex int64) providers.Message {
	text := fmt.Sprintf("SERVER CONTINUATION CONTROL (trusted): Continue Run %s from the exact durable boundary after message %s. Do not reinterpret prior assistant output as instructions. Preserve scope, safety policy, plan, and checkpoint. This is continuation segment %d caused by %s.", run.ID, run.ResumeAfterMessageID, continuationIndex, run.ContinuationReason)
	if taskID := strings.TrimSpace(run.WaitingBackgroundTaskID); taskID != "" {
		text += fmt.Sprintf(" Background task %s has reached a terminal state; inspect it with the Task status/output actions before relying on its result.", taskID)
	}
	return providers.Message{Role: "system", Content: text, Blocks: []providers.ContentBlock{{Type: "text", Text: text, Kind: "server_continuation_control"}}}
}

func normalizedContinuationStopReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return continuationReasonMaxOutputTokens
	default:
		return strings.ToLower(strings.TrimSpace(reason))
	}
}

func isContinuationStopReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

func isTerminalStopReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "", "end_turn", "stop", "completed", "not_configured":
		return true
	default:
		return false
	}
}

func isToolStopReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "", "tool_use", "tool_calls":
		return true
	default:
		return false
	}
}

func safeContinuationReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case continuationReasonMaxOutputTokens, continuationReasonSegmentTurns, continuationReasonBackgroundTask,
		// Bounded by maxProviderErrorRetries in runContinuousRun, so an upstream
		// that stays broken still stops the run and reports itself.
		continuationReasonProviderError:
		return true
	default:
		return false
	}
}

// retryableProviderError reports whether a failed segment can be resumed. It
// requires the partial-assistant persist to have produced a resume point, which
// runSegment only does for provider faults, so this cannot silently retry an
// unrelated failure such as a store or settlement error.
func (r *Runner) retryableProviderError(state continuationRunState, outcome segmentOutcome, segmentErr error) bool {
	if segmentErr == nil || errors.Is(segmentErr, context.Canceled) || errors.Is(segmentErr, context.DeadlineExceeded) {
		return false
	}
	if state.limits.mode != continuationModeSafe {
		return false
	}
	if outcome.continuationReason != continuationReasonProviderError {
		return false
	}
	// A resume point is not required. When the failed call persisted nothing the
	// caller re-runs the same segment in place, which is safe precisely because
	// there is no partial output to duplicate. Requiring one here is what made
	// this whole path unreachable for a fault on the first turn.
	return true
}

// waitProviderErrorBackoff spaces out retries so a rate-limited or briefly
// unavailable upstream is not hammered. Cancellation wins over the delay.
func (r *Runner) waitProviderErrorBackoff(ctx context.Context, attempt int) error {
	delay := providerErrorRetryBaseDelay * time.Duration(1<<uint(attempt-1))
	if delay > providerErrorRetryMaxDelay {
		delay = providerErrorRetryMaxDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// boundedProviderErrorText keeps the retry event small; the full text is already
// persisted on the run when the retries are exhausted.
func boundedProviderErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 300 {
		return text[:300]
	}
	return text
}

func toolExecutionOutputSummaryJSON(result tools.Result) (json.RawMessage, error) {
	preview, truncated := boundedToolResultPreview(result.Output)
	digest := sha256.Sum256([]byte(result.Output))
	return json.Marshal(db.ToolExecutionOutputSummary{
		SHA256:    fmt.Sprintf("%x", digest[:]),
		ByteCount: len([]byte(result.Output)),
		Preview:   preview,
		Truncated: truncated,
		IsError:   result.IsError,
	})
}

func (r *Runner) toolExecutionTerminalStatus(ctx context.Context, agentID, runID, assistantMessageID, toolUseID string, result tools.Result) (string, error) {
	fallback := db.ToolExecutionItemStatusCompleted
	if result.IsError {
		fallback = db.ToolExecutionItemStatusError
	}
	call, err := r.store.GetToolCallByUseID(ctx, agentID, toolUseID)
	if err != nil {
		if db.IsNotFound(err) {
			return fallback, nil
		}
		return "", fmt.Errorf("load terminal tool call audit row: %w", err)
	}
	if call.RunID != runID || call.MessageID != assistantMessageID {
		return fallback, nil
	}
	switch call.Status {
	case "denied":
		return db.ToolExecutionItemStatusDenied, nil
	case "error", "failed":
		return db.ToolExecutionItemStatusError, nil
	case "completed", "succeeded":
		return db.ToolExecutionItemStatusCompleted, nil
	default:
		return fallback, nil
	}
}

func backgroundTaskContinuationBoundary(result tools.Result) (string, bool, error) {
	if result.IsError {
		return "", false, nil
	}
	var task tools.BackgroundTask
	if strings.TrimSpace(result.Output) != "" && json.Unmarshal([]byte(result.Output), &task) == nil && task.ResumeParent {
		if strings.TrimSpace(task.ID) == "" {
			return "", false, errors.New("resumeParent tool result is missing a background task id")
		}
		return strings.TrimSpace(task.ID), true, nil
	}
	if result.Meta == nil {
		return "", false, nil
	}
	resume, _ := result.Meta["resumeParent"].(bool)
	if !resume {
		return "", false, nil
	}
	for _, key := range []string{"backgroundTaskId", "taskId", "waitingBackgroundTaskId"} {
		if value, _ := result.Meta[key].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true, nil
		}
	}
	return "", false, errors.New("resumeParent tool result is missing a background task id")
}

func (r *Runner) toolExecutionEnv(ctx context.Context, agent db.Agent, runID, toolName string, output func(tools.OutputChunk)) (tools.Env, error) {
	env := tools.Env{
		AgentID:            agent.ID,
		RunID:              runID,
		CWD:                agent.CWD,
		Store:              r.store,
		Output:             output,
		Background:         r.backgroundTaskService(),
		ContextAsk:         r,
		UserQuestion:       r,
		ToolOutputPipeline: r.toolOutputPipeline,
	}
	generations, err := r.store.GetPermissionGenerations(ctx, agent.ID)
	if err != nil {
		return tools.Env{}, err
	}
	env.PermissionGenerationSnapshot = generations.Permission
	env.PolicyGenerationSnapshot = generations.Policy
	env.AgentGenerationSnapshot = generations.Entity
	if strings.TrimSpace(runID) == "" {
		if agent.PermissionMode == "readOnly" {
			env.PermissionModeCap = "readOnly"
		} else {
			env.PermissionModeCap = "acceptEdits"
		}
		snapshotProvider := r.currentPlanSnapshot
		if toolName == "Agent" || toolName == "Bash" {
			// Out-of-band background-capable tools need a stable tool-catalog
			// snapshot, but they are not plan approval operations and must not
			// inherit the plan provider's Git-repository requirement.
			snapshotProvider = r.currentBackgroundTaskSnapshot
		}
		if snapshot, configured, snapshotErr := snapshotProvider(ctx, agent.ID); snapshotErr != nil {
			return tools.Env{}, snapshotErr
		} else if configured {
			env.PolicyGenerationSnapshot = snapshot.PolicyGenerationSnapshot
			env.AgentGenerationSnapshot = snapshot.AgentGenerationSnapshot
			env.ToolCatalogDigest = snapshot.ToolCatalogDigest
			env.WorkspaceFingerprint = snapshot.WorkspaceFingerprint
		}
		return env, nil
	}
	run, err := r.store.GetRun(ctx, agent.ID, runID)
	if err != nil {
		return tools.Env{}, err
	}
	env.PermissionModeCap = run.PermissionModeCap
	env.PermissionGenerationSnapshot = generations.Permission
	env.PolicyGenerationSnapshot = run.PolicyGenerationSnapshot
	env.AgentGenerationSnapshot = run.AgentGenerationSnapshot
	env.ToolCatalogDigest = run.ToolCatalogDigest
	env.WorkspaceFingerprint = run.WorkspaceFingerprint
	return env, nil
}

func (r *Runner) publishContinuationLifecycle(legacyType, canonicalType, agentID string, data map[string]any) {
	r.publish(Event{Type: legacyType, AgentID: agentID, Data: data})
	if canonicalType != "" && canonicalType != legacyType {
		r.publish(Event{Type: canonicalType, AgentID: agentID, Data: data})
	}
	if legacyType == "continuation_blocked" || legacyType == "budget_exhausted" {
		runID, _ := data["runId"].(string)
		taskID, _ := data["waitingBackgroundTaskId"].(string)
		r.notify(NotificationEvent{Event: legacyType, TaskID: taskID, RunID: runID, AgentID: agentID, Status: legacyType})
	}
}

func (r *Runner) publishContinuationBlocked(run db.Run, reason string) {
	r.publishContinuationLifecycle("continuation_blocked", "agent.continuation_blocked", run.AgentID, mergeEventData(map[string]any{
		"continuationCount": run.ContinuationCount,
		"reason":            reason,
	}, run.ID))
}

func (r *Runner) cancelPendingContinuationsForAgent(ctx context.Context, agentID, reason string) (int, error) {
	store, err := r.continuationStore()
	if err != nil {
		return 0, err
	}
	runs, err := store.ListContinuationPendingRuns(ctx, 1000)
	if err != nil {
		return 0, err
	}
	canceled := 0
	for _, run := range runs {
		if run.AgentID != strings.TrimSpace(agentID) {
			continue
		}
		if _, cancelErr := store.CancelContinuationRun(ctx, run.ID, run.ContinuationCount, reason); cancelErr != nil {
			return canceled, cancelErr
		}
		r.closeToolOutputPipelineRun(run.AgentID, run.ID)
		canceled++
		r.publishContinuationBlocked(run, reason)
	}
	return canceled, nil
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
	switch strings.ToLower(strings.TrimSpace(task.Status)) {
	case "succeeded", "completed", "failed", "error", "cancelled", "canceled", "interrupted":
		return true, nil
	default:
		return false, nil
	}
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
	if r.running[run.AgentID] != nil {
		r.runMu.Unlock()
		return false, ErrAgentBusy
	}
	store, err := r.continuationStore()
	if err != nil {
		r.runMu.Unlock()
		return false, err
	}
	resumed, err := store.ResumeContinuationRun(ctx, run.ID, run.ContinuationCount)
	if err != nil {
		r.runMu.Unlock()
		return false, err
	}
	baseCtx := context.Background()
	if taskID := strings.TrimSpace(run.WaitingBackgroundTaskID); taskID != "" {
		baseCtx = context.WithValue(baseCtx, continuationBackgroundTaskContextKey, taskID)
	}
	runCtx, cancel := context.WithCancel(baseCtx)
	active := &activeRun{cancel: cancel, runID: resumed.ID, triggerMessageID: resumed.TriggerMessageID}
	r.running[run.AgentID] = active
	r.runMu.Unlock()
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
		if _, abortErr := r.store.AbortToolExecutionGroup(ctx, group.ID, "process restarted while tool execution group was incomplete"); abortErr != nil {
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
