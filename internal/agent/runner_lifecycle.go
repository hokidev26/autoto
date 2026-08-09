package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"autoto/internal/db"
)

type runCompletion struct {
	pending          bool
	interrupted      bool
	runID            string
	triggerMessageID string
}

func (r *Runner) ActiveRunCount() int {
	if r == nil {
		return 0
	}
	r.runMu.Lock()
	defer r.runMu.Unlock()
	return len(r.running)
}

// IsAgentRunning reports whether the given agent currently has an in-memory run
// loop registered. Callers pair this with the durable runs check when they need
// to refuse a destructive operation on a live conversation.
func (r *Runner) IsAgentRunning(agentID string) bool {
	if r == nil {
		return false
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false
	}
	r.runMu.Lock()
	defer r.runMu.Unlock()
	return r.running[agentID] != nil
}

func (r *Runner) RunWithTrigger(ctx context.Context, agentID, triggerMessageID string) {
	r.runWithRun(ctx, agentID, "", triggerMessageID)
}

func (r *Runner) Run(ctx context.Context, agentID string) {
	r.runWithRun(ctx, agentID, "", "")
}

func (r *Runner) runWithRun(ctx context.Context, agentID, runID, triggerMessageID string) {
	runCtx, active, started, registeredRunID, registerErr := r.registerRun(ctx, agentID, runID, triggerMessageID)
	if registerErr != nil {
		// Prefer the ID registration reported: for a fresh turn the caller's runID
		// is empty, and the row that needs a terminal status was created inside
		// registerRun. Falling back keeps the older path intact for a resumed run.
		failedRunID := registeredRunID
		if strings.TrimSpace(failedRunID) == "" {
			failedRunID = runID
		}
		slog.Error("register agent run failed", "agentId", agentID, "runId", failedRunID, "error", registerErr)
		_ = r.store.SetAgentStatus(context.Background(), agentID, "error", registerErr.Error())
		if failedRunID != "" {
			r.captureRunEndHead(failedRunID)
			_ = r.completeRun(context.Background(), failedRunID, "error", registerErr.Error())
		}
		// Carrying the ID lets the client fetch the failure it just heard about.
		// With the empty one it had no run to ask for, and the reason reached the
		// screen only because the event text carries it too.
		r.publish(Event{Type: "agent.error", AgentID: agentID, Text: registerErr.Error(), Data: runEventData(failedRunID)})
		r.notify(NotificationEvent{Event: "error", RunID: failedRunID, AgentID: agentID, Status: "error", Error: registerErr.Error()})
		return
	}
	if !started {
		return
	}

	r.executeRegisteredRun(runCtx, agentID, active)
}

func (r *Runner) executeRegisteredRun(runCtx context.Context, agentID string, active *activeRun) {
	defer r.closeTerminalRuntimeSnapshot(agentID, activeRunID(active))
	err := r.run(runCtx, agentID, active.runID)
	if err != nil && active != nil {
		r.closeToolOutputPipelineRun(agentID, active.runID)
	}
	completion := r.unregisterRun(agentID, active)
	if err == nil && active != nil && strings.TrimSpace(active.runID) != "" {
		r.resumeReadyBackgroundContinuation(active.runID)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if completion.interrupted || !completion.pending {
				slog.Info("agent loop interrupted", "agentId", agentID, "runId", activeRunID(active))
				_ = r.store.SetAgentStatus(context.Background(), agentID, "interrupted", "")
				if active != nil && active.runID != "" {
					r.captureRunEndHead(active.runID)
					_ = r.completeRun(context.Background(), active.runID, "interrupted", "")
				}
				r.publish(Event{Type: "agent.interrupted", AgentID: agentID, Data: runEventData(activeRunID(active))})
				r.notify(NotificationEvent{Event: "interrupted", RunID: activeRunID(active), AgentID: agentID, Status: "interrupted"})
				return
			}
			if active != nil && active.runID != "" {
				r.captureRunEndHead(active.runID)
				_ = r.completeRun(context.Background(), active.runID, "superseded", "")
				r.notify(NotificationEvent{Event: "superseded", RunID: active.runID, AgentID: agentID, Status: "superseded"})
			}
			go r.runWithRun(context.Background(), agentID, completion.runID, completion.triggerMessageID)
			return
		}
		slog.Error("agent loop failed", "agentId", agentID, "runId", activeRunID(active), "error", err)
		_ = r.store.SetAgentStatus(context.Background(), agentID, "error", err.Error())
		if active != nil && active.runID != "" {
			r.captureRunEndHead(active.runID)
			_ = r.completeRun(context.Background(), active.runID, "error", err.Error())
		}
		r.publish(Event{Type: "agent.error", AgentID: agentID, Text: err.Error(), Data: runEventData(activeRunID(active))})
		r.notify(NotificationEvent{Event: "error", RunID: activeRunID(active), AgentID: agentID, Status: "error", Error: err.Error()})
		if completion.pending {
			go r.runWithRun(context.Background(), agentID, completion.runID, completion.triggerMessageID)
		}
		return
	}
	if completion.pending {
		go r.runWithRun(context.Background(), agentID, completion.runID, completion.triggerMessageID)
	}
}

func (r *Runner) Interrupt(ctx context.Context, agentID string) (bool, error) {
	if _, err := r.store.GetAgent(ctx, agentID); err != nil {
		return false, err
	}
	r.runMu.Lock()
	active := r.running[agentID]
	var cancel context.CancelFunc
	pendingRunID := ""
	if active != nil {
		pendingRunID = active.pendingRunID
		active.pending = false
		active.pendingRunID = ""
		active.pendingTriggerMessageID = ""
		active.interrupted = true
		cancel = active.cancel
	}
	r.runMu.Unlock()
	if pendingRunID != "" {
		r.captureRunEndHead(pendingRunID)
		_ = r.completeRun(context.Background(), pendingRunID, "interrupted", "")
		r.closeToolOutputPipelineRun(agentID, pendingRunID)
	}
	if cancel == nil {
		canceled, cancelErr := r.cancelPendingContinuationsForAgent(ctx, agentID, "interrupted by user")
		if cancelErr != nil && !errors.Is(cancelErr, errContinuationStoreUnavailable) {
			return false, cancelErr
		}
		if canceled > 0 {
			r.closeToolOutputPipelineAgent(agentID)
		}
		return canceled > 0, nil
	}
	cancel()
	return true, nil
}

// registerRun also reports the run ID that still needs a terminal status when it
// fails. Without it the caller could only guess from the run ID it passed in,
// which is empty for a fresh turn, so a row created here and then abandoned by a
// later error stayed "running" forever. Nothing reaps it, and because the client
// only renders a run summary for a terminal run, that phantom row is also why an
// agent could stop with no visible reason at all.
func (r *Runner) registerRun(ctx context.Context, agentID, runID, triggerMessageID string) (context.Context, *activeRun, bool, string, error) {
	var runRequest db.Run
	if strings.TrimSpace(runID) == "" {
		agent, err := r.store.GetAgent(ctx, agentID)
		if err != nil {
			return nil, nil, false, "", err
		}
		runRequest, err = r.bindPlanRunSnapshot(ctx, db.Run{AgentID: agentID, TriggerMessageID: triggerMessageID, Status: "running", ExecutionMode: runExecutionModeForAgent(agent)})
		if err != nil {
			return nil, nil, false, "", err
		}
		runRequest, err = r.prepareContinuationRun(ctx, runRequest)
		if err != nil {
			// prepareContinuationRun assigns run.ID and freezes budgets against it,
			// but no row is written until CreateRun below, so there is nothing to
			// complete: reporting that ID would ask the store to transition a row
			// that does not exist.
			return nil, nil, false, "", err
		}
	}
	r.runMu.Lock()
	if r.running == nil {
		r.running = make(map[string]*activeRun)
	}
	if _, compacting := r.compacting[agentID]; compacting {
		r.runMu.Unlock()
		return nil, nil, false, runID, ErrAgentBusy
	}
	if active := r.running[agentID]; active != nil {
		// Keep only the newest queued request. Persistently supersede the prior
		// pending run before it can be forgotten by this in-memory slot.
		previousPending, previousRunID, previousTriggerID := active.pending, active.pendingRunID, active.pendingTriggerMessageID
		replacedPendingRunID := ""
		if runID != "" && active.pendingRunID != "" && active.pendingRunID != runID {
			replacedPendingRunID = active.pendingRunID
		}
		active.pending = true
		if runID != "" {
			active.pendingRunID = runID
		}
		if triggerMessageID != "" {
			active.pendingTriggerMessageID = triggerMessageID
		}
		cancel := active.cancel
		r.runMu.Unlock()
		if replacedPendingRunID != "" {
			r.captureRunEndHead(replacedPendingRunID)
			if err := r.completeRun(context.Background(), replacedPendingRunID, "superseded", ""); err != nil && !db.IsConflict(err) && !db.IsNotFound(err) {
				// Do not leave the old durable run stranded if its terminal write
				// failed. Restore it as the queued successor, then let the new run
				// follow its normal registration-error path.
				r.runMu.Lock()
				if r.running[agentID] == active && active.pendingRunID == runID {
					active.pending, active.pendingRunID, active.pendingTriggerMessageID = previousPending, previousRunID, previousTriggerID
				}
				r.runMu.Unlock()
				if cancel != nil {
					cancel()
				}
				return nil, nil, false, runID, err
			}
		}
		if cancel != nil {
			cancel()
		}
		return nil, nil, false, runID, nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	active := &activeRun{cancel: cancel, runID: runID, triggerMessageID: triggerMessageID}
	if active.runID == "" {
		runningRun, err := r.store.CreateRun(context.Background(), runRequest)
		if err != nil {
			r.runMu.Unlock()
			cancel()
			// CreateRun is what writes the row, so a failure here leaves nothing
			// behind to complete.
			return nil, nil, false, "", err
		}
		active.runID = runningRun.ID
		if triggerMessageID != "" {
			if err := r.store.AssignMessageRun(context.Background(), agentID, triggerMessageID, active.runID); err != nil {
				r.runMu.Unlock()
				cancel()
				// The row exists from here on. This is the path that used to strand
				// it: the ID lived only in this frame, the caller saw the empty ID it
				// passed in, and the run stayed "running" with no way to reach it.
				return nil, nil, false, active.runID, err
			}
		}
	} else if err := r.store.UpdateRunStatus(context.Background(), active.runID, "running", ""); err != nil {
		r.runMu.Unlock()
		cancel()
		return nil, nil, false, active.runID, err
	}
	r.running[agentID] = active
	r.runMu.Unlock()
	return runCtx, active, true, active.runID, nil
}

func (r *Runner) unregisterRun(agentID string, active *activeRun) runCompletion {
	completion := runCompletion{}
	r.runMu.Lock()
	if r.running[agentID] == active {
		completion.pending = active.pending
		completion.interrupted = active.interrupted
		completion.runID = active.pendingRunID
		completion.triggerMessageID = active.pendingTriggerMessageID
		delete(r.running, agentID)
	}
	r.runMu.Unlock()
	if active != nil && active.cancel != nil {
		active.cancel()
	}
	return completion
}
