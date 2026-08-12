package agent

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"autoto/internal/db"
)

// DefaultAbandonedRunIdleTimeout is how long a pending or running Run may sit
// with no in-memory executor and no change of its own before it is reaped. It is
// generous because the guard that actually decides this is IsAgentRunning: the
// window only has to be longer than the gap between creating a Run row and
// registering its executor.
const DefaultAbandonedRunIdleTimeout = 10 * time.Minute

// ReapAbandonedRuns gives a terminal status to Runs that no goroutine owns any
// more. RecoverInterruptedRuns only sweeps at startup, so a Run whose executor
// disappeared inside a long-lived process stayed "running" forever: the agent
// counted as durably busy and refused new dispatches, a subagent waiting on it
// polled a status that would never change, and the client -- which only renders a
// summary for a terminal Run -- showed an agent that had stopped for no visible
// reason.
//
// A Run is only a candidate when its agent has no registered in-memory run at
// all, which is what makes this safe: a live turn is never reaped, however long
// it takes. The idle threshold is a second guard for the moment between a Run row
// being created and its executor registering.
func (r *Runner) ReapAbandonedRuns(ctx context.Context, minIdle time.Duration) (int, error) {
	if r == nil || r.store == nil {
		return 0, nil
	}
	if minIdle <= 0 {
		minIdle = DefaultAbandonedRunIdleTimeout
	}
	runs, err := r.store.ListRecoverableRuns(ctx)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return reaped, err
		}
		if !r.runIsAbandoned(run, minIdle) {
			continue
		}
		reason := "run was abandoned by its executor"
		r.captureRunEndHead(run.ID)
		if err := r.store.AbandonStrandedRun(ctx, run.ID, reason); err != nil {
			if db.IsConflict(err) || db.IsNotFound(err) {
				continue
			}
			slog.Warn("reaping an abandoned run failed", "runId", run.ID, "agentId", run.AgentID, "error", err)
			continue
		}
		r.releaseContinuationLimits(run.ID)
		r.closeToolOutputPipelineRun(run.AgentID, run.ID)
		reaped++
		slog.Warn("reaped an abandoned run", "runId", run.ID, "agentId", run.AgentID, "runStatus", run.Status, "updatedAt", run.UpdatedAt)
		r.publish(Event{Type: "agent.error", AgentID: run.AgentID, Text: reason, Data: runEventData(run.ID)})
		r.notify(NotificationEvent{Event: "error", RunID: run.ID, AgentID: run.AgentID, Status: "interrupted", Error: reason})
	}
	return reaped, nil
}

func (r *Runner) runIsAbandoned(run db.Run, minIdle time.Duration) bool {
	if r.IsAgentRunning(run.AgentID) {
		return false
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(run.UpdatedAt))
	if err != nil {
		// An unparseable timestamp cannot establish that the row is stale, and
		// guessing would reap a Run that may still be live.
		return false
	}
	return time.Since(updatedAt) >= minIdle
}
