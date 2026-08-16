package agent

import (
	"context"
	"strings"

	"autoto/internal/db"
)

// injectQueuedSteering claims at most one parked follow-up and persists it as a
// user message on the same Run, so the next Generate can see it. Callers invoke
// this after a settled tool batch, and again at the start of a resumed segment
// that woke from a background-task wait. Interrupt still cancels the tree; this
// is not a second run.
func (r *Runner) injectQueuedSteering(ctx context.Context, agent db.Agent, run db.Run, messages []db.Message) ([]db.Message, error) {
	if r == nil || r.store == nil {
		return messages, nil
	}
	agentID := strings.TrimSpace(agent.ID)
	runID := strings.TrimSpace(run.ID)
	if agentID == "" || runID == "" {
		return messages, nil
	}
	if err := ctx.Err(); err != nil {
		return messages, err
	}
	if r.agentHasPendingSteerGate(agentID) {
		return messages, nil
	}
	generations, err := r.store.GetPermissionGenerations(ctx, agentID)
	if err != nil {
		return messages, err
	}
	if generations.Execution != run.ExecutionGeneration ||
		generations.Entity != run.AgentGenerationSnapshot ||
		generations.Policy != run.PolicyGenerationSnapshot {
		return messages, nil
	}
	item, ok, err := r.store.ClaimNextQueuedMessage(ctx, agentID)
	if err != nil || !ok {
		return messages, err
	}
	stored, persistErr := r.persistQueuedSteeringMessage(ctx, agentID, runID, item)
	if persistErr != nil {
		_ = r.store.RestoreQueuedMessage(ctx, item)
		return messages, persistErr
	}
	r.publish(Event{
		Type:      "message.created",
		AgentID:   agentID,
		MessageID: stored.ID,
		Text:      stored.ContentText,
		Data:      mergeEventData(map[string]any{"attachments": len(stored.Attachments), "queued": true}, runID),
	})
	return append(messages, stored), nil
}

func (r *Runner) persistQueuedSteeringMessage(ctx context.Context, agentID, runID string, item db.QueuedMessage) (db.Message, error) {
	msg := db.Message{
		AgentID:     agentID,
		RunID:       runID,
		Role:        "user",
		ContentText: item.Text,
		CreatedBy:   item.CreatedBy,
	}
	if len(item.Attachments) == 0 {
		return r.store.AddMessage(ctx, msg)
	}
	copied := make([]db.Attachment, len(item.Attachments))
	copy(copied, item.Attachments)
	for i := range copied {
		copied[i].ID = ""
		copied[i].MessageID = ""
	}
	stored, err := r.store.AddMessageWithAttachments(ctx, msg, copied)
	if err != nil {
		return db.Message{}, err
	}
	for i := range stored.Attachments {
		if i >= len(copied) {
			break
		}
		stored.Attachments[i].Data = copied[i].Data
		stored.Attachments[i].ModelData = copied[i].ModelData
	}
	return stored, nil
}

func (r *Runner) agentHasPendingSteerGate(agentID string) bool {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false
	}
	r.approvalMu.Lock()
	for _, approval := range r.approvals {
		if approval != nil && approval.AgentID == agentID {
			r.approvalMu.Unlock()
			return true
		}
	}
	r.approvalMu.Unlock()
	r.userQuestionMu.Lock()
	defer r.userQuestionMu.Unlock()
	for _, pending := range r.userQuestions {
		if pending != nil && pending.AgentID == agentID {
			return true
		}
	}
	return false
}
