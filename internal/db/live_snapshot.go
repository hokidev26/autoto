package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type PermissionGenerations struct {
	Entity     int64 `json:"entity"`
	Permission int64 `json:"permission"`
	Execution  int64 `json:"execution"`
	Policy     int64 `json:"policy"`
}

type AgentLiveSnapshot struct {
	Agent                Agent                 `json:"agent"`
	Messages             []Message             `json:"messages"`
	MessageHasMoreBefore bool                  `json:"messageHasMoreBefore"`
	MessageNextBefore    string                `json:"messageNextBefore,omitempty"`
	PendingApprovals     []ToolCall            `json:"pendingApprovals"`
	LatestRun            *Run                  `json:"latestRun,omitempty"`
	LatestPlan           *Plan                 `json:"latestPlan,omitempty"`
	Generations          PermissionGenerations `json:"generations"`
}

func (s *liveSnapshotStore) GetPermissionGenerations(ctx context.Context, agentID string) (PermissionGenerations, error) {
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PermissionGenerations{}, err
	}
	defer tx.Rollback()
	generations := PermissionGenerations{Policy: 1}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(entity_generation,1), COALESCE(permission_generation,1), COALESCE(execution_generation,0) FROM agents WHERE id = ?`, agentID).Scan(&generations.Entity, &generations.Permission, &generations.Execution); err != nil {
		return PermissionGenerations{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(policy_generation,1) FROM workflow_preferences WHERE id = 'default'`).Scan(&generations.Policy); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PermissionGenerations{}, err
	}
	if err := tx.Commit(); err != nil {
		return PermissionGenerations{}, err
	}
	return generations, nil
}

// ReadAgentLiveSnapshot only SELECTs, so its transaction runs on the read
// pool: the UI polls this constantly and must not queue behind run writes.
func (s *liveSnapshotStore) ReadAgentLiveSnapshot(ctx context.Context, agentID string) (AgentLiveSnapshot, error) {
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AgentLiveSnapshot{}, err
	}
	defer tx.Rollback()

	var snapshot AgentLiveSnapshot
	agent, err := scanAgent(func(dest ...any) error {
		return tx.QueryRowContext(ctx, agentSelectSQL+` WHERE id = ?`, agentID).Scan(dest...)
	})
	if err != nil {
		return AgentLiveSnapshot{}, err
	}
	snapshot.Agent = agent
	snapshot.Generations = PermissionGenerations{Entity: snapshot.Agent.EntityGeneration, Permission: snapshot.Agent.PermissionGeneration, Execution: snapshot.Agent.ExecutionGeneration, Policy: 1}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(policy_generation,1) FROM workflow_preferences WHERE id = 'default'`).Scan(&snapshot.Generations.Policy); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AgentLiveSnapshot{}, err
	}

	messageRows, err := tx.QueryContext(ctx, `SELECT id, agent_id, COALESCE(run_id,''), role, COALESCE(content_json,''), COALESCE(provider_state_json,''), COALESCE(content_text,''), COALESCE(parent_tool_use_id,''), COALESCE(command_text,''), COALESCE(correction_of_message_id,''), COALESCE(created_by,''), created_at FROM agent_messages WHERE agent_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, agentID, DefaultMessagePageLimit+1)
	if err != nil {
		return AgentLiveSnapshot{}, err
	}
	for messageRows.Next() {
		var message Message
		var raw, providerState string
		if err := messageRows.Scan(&message.ID, &message.AgentID, &message.RunID, &message.Role, &raw, &providerState, &message.ContentText, &message.ParentToolID, &message.CommandText, &message.CorrectionOfMessageID, &message.CreatedBy, &message.CreatedAt); err != nil {
			messageRows.Close()
			return AgentLiveSnapshot{}, err
		}
		if raw != "" {
			message.ContentJSON = json.RawMessage(raw)
		}
		if providerState != "" {
			message.ProviderStateJSON = json.RawMessage(providerState)
		}
		snapshot.Messages = append(snapshot.Messages, message)
	}
	if err := messageRows.Err(); err != nil {
		messageRows.Close()
		return AgentLiveSnapshot{}, err
	}
	if err := messageRows.Close(); err != nil {
		return AgentLiveSnapshot{}, err
	}
	if len(snapshot.Messages) > DefaultMessagePageLimit {
		snapshot.MessageHasMoreBefore = true
		snapshot.Messages = snapshot.Messages[:DefaultMessagePageLimit]
	}
	for i, j := 0, len(snapshot.Messages)-1; i < j; i, j = i+1, j-1 {
		snapshot.Messages[i], snapshot.Messages[j] = snapshot.Messages[j], snapshot.Messages[i]
	}
	if snapshot.MessageHasMoreBefore && len(snapshot.Messages) > 0 {
		snapshot.MessageNextBefore, err = encodeMessageCursor(messageCursor{CreatedAt: snapshot.Messages[0].CreatedAt, ID: snapshot.Messages[0].ID})
		if err != nil {
			return AgentLiveSnapshot{}, err
		}
	}
	// One batched query rather than one per message. A page is up to
	// DefaultMessagePageLimit messages, so the per-message form issued up to 101
	// queries inside this transaction, against a pool of a single connection.
	if err := attachMessageAttachments(ctx, tx, snapshot.Messages); err != nil {
		return AgentLiveSnapshot{}, err
	}

	callRows, err := tx.QueryContext(ctx, `SELECT id, agent_id, COALESCE(run_id,''), COALESCE(message_id,''), tool_use_id, tool_name, COALESCE(input_json,''), COALESCE(output_json,''), status, COALESCE(duration_ms,0), COALESCE(error_message,''), COALESCE(permission_decided_by,''), COALESCE(permission_decided_at,''), COALESCE(permission_deny_message,''), COALESCE(permission_decision_reason,''), COALESCE(permission_suggestions,''), COALESCE(permission_generation,1), COALESCE(policy_generation,1), COALESCE(execution_device_id,'local'), COALESCE(started_at,''), COALESCE(completed_at,''), created_at, COALESCE(updated_at, created_at) FROM agent_tool_calls WHERE agent_id = ? AND status = 'pending_approval' ORDER BY created_at ASC`, agentID)
	if err != nil {
		return AgentLiveSnapshot{}, err
	}
	for callRows.Next() {
		var call ToolCall
		var input, output string
		if err := callRows.Scan(&call.ID, &call.AgentID, &call.RunID, &call.MessageID, &call.ToolUseID, &call.ToolName, &input, &output, &call.Status, &call.DurationMS, &call.ErrorMessage, &call.PermissionDecidedBy, &call.PermissionDecidedAt, &call.PermissionDenyMessage, &call.PermissionDecisionReason, &call.PermissionSuggestions, &call.PermissionGeneration, &call.PolicyGeneration, &call.ExecutionDeviceID, &call.StartedAt, &call.CompletedAt, &call.CreatedAt, &call.UpdatedAt); err != nil {
			callRows.Close()
			return AgentLiveSnapshot{}, err
		}
		if input != "" {
			call.InputJSON = json.RawMessage(input)
		}
		if output != "" {
			call.OutputJSON = json.RawMessage(output)
		}
		snapshot.PendingApprovals = append(snapshot.PendingApprovals, call)
	}
	if err := callRows.Err(); err != nil {
		callRows.Close()
		return AgentLiveSnapshot{}, err
	}
	if err := callRows.Close(); err != nil {
		return AgentLiveSnapshot{}, err
	}

	run, err := scanRun(func(dest ...any) error {
		return tx.QueryRowContext(ctx, runSelectSQL+` WHERE agent_id = ? ORDER BY execution_generation DESC, id DESC LIMIT 1`, agentID).Scan(dest...)
	})
	if err == nil {
		snapshot.LatestRun = &run
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AgentLiveSnapshot{}, err
	}
	plan, err := scanPlan(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE agent_id = ? ORDER BY updated_at DESC, id DESC LIMIT 1`, agentID).Scan(dest...)
	})
	if err == nil {
		snapshot.LatestPlan = &plan
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AgentLiveSnapshot{}, err
	}

	if err := tx.Commit(); err != nil {
		return AgentLiveSnapshot{}, err
	}
	return snapshot, nil
}

// attachMessageAttachments fills in Attachments for every message in one query.
// The rows are grouped by message_id in Go, which is the same shape the pending
// approval query above already uses: fetch the set once, distribute in memory.
func attachMessageAttachments(ctx context.Context, tx *sql.Tx, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}
	// Index by id so a row lands on its message without scanning the slice, and so
	// an unexpected message_id is ignored rather than mis-filed.
	byID := make(map[string]*Message, len(messages))
	args := make([]any, 0, len(messages))
	placeholders := make([]byte, 0, len(messages)*2)
	for i := range messages {
		id := messages[i].ID
		if id == "" || byID[id] != nil {
			continue
		}
		byID[id] = &messages[i]
		if len(placeholders) > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}
	if len(args) == 0 {
		return nil
	}
	// Ordered by message first so each message's attachments stay in creation
	// order, matching what the per-message query returned.
	query := `SELECT id, message_id, agent_id, filename, COALESCE(mime_type,''), kind, size_bytes, created_at FROM agent_message_attachments WHERE message_id IN (` + string(placeholders) + `) ORDER BY message_id ASC, created_at ASC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var attachment Attachment
		if err := rows.Scan(&attachment.ID, &attachment.MessageID, &attachment.AgentID, &attachment.Filename, &attachment.MIMEType, &attachment.Kind, &attachment.SizeBytes, &attachment.CreatedAt); err != nil {
			return err
		}
		if owner := byID[attachment.MessageID]; owner != nil {
			owner.Attachments = append(owner.Attachments, attachment)
		}
	}
	return rows.Err()
}
