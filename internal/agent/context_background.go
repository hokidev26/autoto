package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// Background compaction runs the same threshold compaction as
// managedContextForTurn, but between runs instead of at the start of the next
// turn. Crossing the compact threshold mid-run previously meant the next user
// message first waited for a synchronous summary-model call (up to a minute)
// before the model could answer. Pre-compacting while the conversation is idle
// removes that stall; the synchronous path stays as the safety net.
//
// A background compaction must never make the conversation feel busy: user
// submissions preempt it by cancelling its context and briefly waiting for the
// slot, while a manual compaction (user-initiated API call) keeps its existing
// ErrAgentBusy semantics.
const (
	// Rolling summarization can chain several bounded model calls.
	backgroundCompactionTimeout = 8 * time.Minute
	// A cancelled compaction aborts at the next context check; the wait only
	// needs to cover one in-flight store call or stream teardown.
	backgroundCompactionPreemptWait = 5 * time.Second
)

type backgroundCompactionHandle struct {
	cancel context.CancelFunc
	// done closes once the compaction released the busy slot, so a preempting
	// submission knows exactly when it may proceed.
	done chan struct{}
}

// maybeCompactContextInBackground starts an idle compaction when the finished
// conversation already sits above the compact threshold. Claiming the busy
// slot (including its durable-run check) happens synchronously so the caller
// that just released the run slot cannot lose it to a racing submission; the
// threshold evaluation and the summary call run in the spawned goroutine.
func (r *Runner) maybeCompactContextInBackground(agentID string) {
	if r == nil || r.store == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backgroundCompactionTimeout)
	handle := &backgroundCompactionHandle{cancel: cancel, done: make(chan struct{})}
	if err := r.beginBackgroundContextCompaction(ctx, agentID, handle); err != nil {
		cancel()
		// Busy is the expected outcome whenever anything else holds the slot;
		// anything else is a real failure that would otherwise look exactly
		// like "below threshold" forever.
		if !errors.Is(err, ErrAgentBusy) {
			slog.Warn("begin background context compaction", "agentId", agentID, "error", err)
		}
		return
	}
	go func() {
		defer recoverGoroutine("background context compaction", "agentId", agentID)
		defer cancel()
		defer r.finishBackgroundContextCompaction(agentID, handle)
		r.backgroundCompactContext(ctx, agentID)
	}()
}

func (r *Runner) beginBackgroundContextCompaction(ctx context.Context, agentID string, handle *backgroundCompactionHandle) error {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if r.running == nil {
		r.running = make(map[string]*activeRun)
	}
	if r.compacting == nil {
		r.compacting = make(map[string]struct{})
	}
	if r.backgroundCompactions == nil {
		r.backgroundCompactions = make(map[string]*backgroundCompactionHandle)
	}
	if r.running[agentID] != nil {
		return ErrAgentBusy
	}
	if _, compacting := r.compacting[agentID]; compacting {
		return ErrAgentBusy
	}
	var durableBusy int
	if err := r.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE agent_id = ? AND status IN ('pending','running','continuation_pending')`, agentID).Scan(&durableBusy); err != nil {
		return err
	}
	if durableBusy > 0 {
		return ErrAgentBusy
	}
	r.compacting[agentID] = struct{}{}
	r.backgroundCompactions[agentID] = handle
	return nil
}

func (r *Runner) finishBackgroundContextCompaction(agentID string, handle *backgroundCompactionHandle) {
	r.runMu.Lock()
	delete(r.compacting, agentID)
	if r.backgroundCompactions[agentID] == handle {
		delete(r.backgroundCompactions, agentID)
	}
	r.runMu.Unlock()
	close(handle.done)
}

// waitForBackgroundCompactionLocked resolves a compacting-slot conflict for a
// caller that holds runMu and wants to start a run. A manual compaction keeps
// the slot (returns false, preserving ErrAgentBusy); a background compaction is
// cancelled and briefly awaited. The method returns with runMu held either
// way, and reports whether the slot is now free. Callers must re-run their
// remaining slot checks after it returns, because the lock was dropped while
// waiting.
func (r *Runner) waitForBackgroundCompactionLocked(agentID string) bool {
	for attempt := 0; attempt < 2; attempt++ {
		if _, compacting := r.compacting[agentID]; !compacting {
			return true
		}
		handle := r.backgroundCompactions[agentID]
		if handle == nil {
			return false
		}
		r.runMu.Unlock()
		handle.cancel()
		select {
		case <-handle.done:
		case <-time.After(backgroundCompactionPreemptWait):
		}
		r.runMu.Lock()
	}
	_, compacting := r.compacting[agentID]
	return !compacting
}

// backgroundCompactContext mirrors the automatic compaction block in
// managedContextForTurn: same calibrated threshold, same candidate selection,
// same summary pipeline and event. It re-reads all state under the busy slot,
// and drops its result instead of writing when the context was cancelled by a
// preempting run.
func (r *Runner) backgroundCompactContext(ctx context.Context, agentID string) {
	agent, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("background context compaction: load agent", "agentId", agentID, "error", err)
		}
		return
	}
	messages, err := r.store.ListMessages(ctx, agentID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("background context compaction: list messages", "agentId", agentID, "error", err)
		}
		return
	}
	agent = contextAgentForMessages(agent, messages)
	status := r.contextStatusForAgent(agent, messages, nil)
	if status.LimitTokens <= 0 || status.EstimatedTokens*100 < status.LimitTokens*status.Thresholds.CompactStartPercent {
		return
	}
	candidates := selectContextTurnCandidates(messages, agent.PruneBoundaryMessageID, r.ContextManagementConfig().CompactKeepTurns)
	if len(candidates) == 0 {
		return
	}
	summary := strings.TrimSpace(r.summarizeOldestMessages(ctx, agent, candidates))
	if summary == "" || ctx.Err() != nil {
		return
	}
	boundaryID := contextCandidateBoundary(candidates)
	_, prunedPercent := contextPrunedProgress(messages, boundaryID)
	if err := r.store.UpdateAgentContextSummary(ctx, agent.ID, summary, boundaryID, prunedPercent); err != nil {
		if ctx.Err() == nil {
			slog.Warn("background context compaction: store summary", "agentId", agentID, "error", err)
		}
		return
	}
	agent.ContextSummary, agent.PruneBoundaryMessageID, agent.PrunedPercent = summary, boundaryID, prunedPercent
	data := r.contextUpdatedData(agent, messages, nil)
	data["compacted"] = true
	r.publish(Event{Type: "context.updated", AgentID: agent.ID, Data: data})
}
