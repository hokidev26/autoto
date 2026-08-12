package agent

import (
	"context"
	"errors"
	"strings"

	"autoto/internal/db"
)

// RollbackConversationToMessage retires everything after the target message.
// It borrows the compaction busy guard: retiring turns while a run is writing
// new ones would race the run's own view of history.
func (r *Runner) RollbackConversationToMessage(ctx context.Context, agentID, messageID string) (int64, error) {
	if r == nil || r.store == nil {
		return 0, errors.New("agent runner is not initialized")
	}
	agentID, messageID = strings.TrimSpace(agentID), strings.TrimSpace(messageID)
	if agentID == "" || messageID == "" {
		return 0, errors.New("agent id and message id are required")
	}
	if err := r.beginContextCompaction(ctx, agentID); err != nil {
		return 0, err
	}
	defer r.finishContextCompaction(agentID)
	superseded, err := r.store.RollbackConversationToMessage(ctx, agentID, messageID)
	if err != nil {
		return 0, err
	}
	r.publish(Event{Type: "message.rollback", AgentID: agentID, MessageID: messageID, Data: map[string]any{"supersededCount": superseded}})
	return superseded, nil
}

// DeleteConversationMessage permanently removes one message (plus the hidden
// tool-result rows that belonged to its tool calls). Guarded like rollback:
// deleting rows out from under an active run must not be possible.
func (r *Runner) DeleteConversationMessage(ctx context.Context, agentID, messageID string) ([]string, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("agent runner is not initialized")
	}
	agentID, messageID = strings.TrimSpace(agentID), strings.TrimSpace(messageID)
	if agentID == "" || messageID == "" {
		return nil, errors.New("agent id and message id are required")
	}
	if err := r.beginContextCompaction(ctx, agentID); err != nil {
		return nil, err
	}
	defer r.finishContextCompaction(agentID)
	deleted, err := r.store.DeleteConversationMessage(ctx, agentID, messageID)
	if err != nil {
		return nil, err
	}
	r.publish(Event{Type: "message.deleted", AgentID: agentID, MessageID: messageID, Data: map[string]any{"deletedMessageIds": deleted}})
	return deleted, nil
}

// ForkConversationFromMessage copies the transcript up to the target message
// into a new sibling conversation. The source is only read -- a bounded
// snapshot that a concurrent run appending newer messages cannot disturb -- so
// no busy guard is needed.
func (r *Runner) ForkConversationFromMessage(ctx context.Context, agentID, messageID, title string) (db.Agent, error) {
	if r == nil || r.store == nil {
		return db.Agent{}, errors.New("agent runner is not initialized")
	}
	agentID, messageID = strings.TrimSpace(agentID), strings.TrimSpace(messageID)
	if agentID == "" || messageID == "" {
		return db.Agent{}, errors.New("agent id and message id are required")
	}
	fork, err := r.store.ForkConversationFromMessage(ctx, agentID, messageID, title)
	if err != nil {
		return db.Agent{}, err
	}
	r.publish(Event{Type: "message.forked", AgentID: agentID, MessageID: messageID, Data: map[string]any{"forkAgentId": fork.ID}})
	return fork, nil
}
