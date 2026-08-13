package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// RollbackConversationToMessage retires every message that follows the target,
// leaving the target itself as the newest live turn. Nothing is deleted: like a
// correction, the rows stay readable in the transcript but are withheld from
// the model. Unlike a correction, no new message and no run are created -- the
// conversation simply resumes from the chosen point.
func (s *messageStore) RollbackConversationToMessage(ctx context.Context, agentID, messageID string) (int64, error) {
	agentID, messageID = strings.TrimSpace(agentID), strings.TrimSpace(messageID)
	if agentID == "" || messageID == "" {
		return 0, errors.New("agent id and message id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var createdAt, supersededAt string
	if err := tx.QueryRowContext(ctx, `SELECT created_at, COALESCE(superseded_at,'') FROM agent_messages WHERE id = ? AND agent_id = ?`, messageID, agentID).Scan(&createdAt, &supersededAt); err != nil {
		return 0, err
	}
	if supersededAt != "" {
		return 0, fmt.Errorf("%w: rollback requires a message that is still current", ErrConflict)
	}
	now := Now()
	// Strictly after, exactly like a rerun: the target stays live.
	result, err := tx.ExecContext(ctx, `UPDATE agent_messages SET superseded_at = ?
		WHERE agent_id = ? AND superseded_at IS NULL
		  AND (created_at > ? OR (created_at = ? AND id > ?))`,
		now, agentID, createdAt, createdAt, messageID)
	if err != nil {
		return 0, err
	}
	superseded, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET updated_at = ? WHERE id = ?`, now, agentID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return superseded, nil
}

// DeleteConversationMessage removes a single message permanently. When the
// message carried tool calls, the hidden tool-result rows that answered them
// are removed in the same transaction: leaving either half of a tool exchange
// behind would poison every later provider replay with an unmatched pair.
// Attachments and generated images go with their message via FK cascade.
func (s *messageStore) DeleteConversationMessage(ctx context.Context, agentID, messageID string) ([]string, error) {
	agentID, messageID = strings.TrimSpace(agentID), strings.TrimSpace(messageID)
	if agentID == "" || messageID == "" {
		return nil, errors.New("agent id and message id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM agent_messages WHERE id = ? AND agent_id = ?`, messageID, agentID).Scan(&role); err != nil {
		return nil, err
	}

	deleteIDs := []string{messageID}
	toolUseIDs, err := queryStrings(ctx, tx, `SELECT tool_use_id FROM agent_tool_calls WHERE agent_id = ? AND message_id = ?`, agentID, messageID)
	if err != nil {
		return nil, err
	}
	if len(toolUseIDs) > 0 {
		query, args := inClause(`SELECT id FROM agent_messages WHERE agent_id = ? AND parent_tool_use_id IN `, []any{agentID}, toolUseIDs)
		resultIDs, err := queryStrings(ctx, tx, query, args...)
		if err != nil {
			return nil, err
		}
		deleteIDs = append(deleteIDs, resultIDs...)
	}

	// correction_of_message_id is ON DELETE RESTRICT; detach any correction that
	// points at a row we are about to remove so the delete cannot be vetoed.
	query, args := inClause(`UPDATE agent_messages SET correction_of_message_id = NULL WHERE agent_id = ? AND correction_of_message_id IN `, []any{agentID}, deleteIDs)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return nil, err
	}
	query, args = inClause(`DELETE FROM agent_tool_calls WHERE agent_id = ? AND message_id IN `, []any{agentID}, deleteIDs)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return nil, err
	}
	query, args = inClause(`DELETE FROM agent_messages WHERE agent_id = ? AND id IN `, []any{agentID}, deleteIDs)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	now := Now()
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET message_count = CASE WHEN message_count > ? THEN message_count - ? ELSE 0 END, updated_at = ? WHERE id = ?`, deleted, deleted, now, agentID); err != nil {
		return nil, err
	}
	// A summary whose boundary row is gone must not survive it: the stale-boundary
	// fallback would drop it at read time anyway, so drop it durably here.
	query, args = inClause(`UPDATE agents SET context_summary = NULL, prune_boundary_message_id = NULL, pruned_percent = 0 WHERE id = ? AND prune_boundary_message_id IN `, []any{agentID}, deleteIDs)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleteIDs, nil
}

// ForkConversationFromMessage creates a sibling conversation in the same
// workline whose transcript is a copy of the source up to and including the
// target message. Navigation treats every agent in a workline as its own
// conversation, so no new workline (and no git work) is needed. Superseded
// rows are not copied: the fork starts from what the model would actually see.
// Runs are not copied either -- run history belongs to the original.
func (s *messageStore) ForkConversationFromMessage(ctx context.Context, agentID, messageID, title string) (Agent, error) {
	agentID, messageID = strings.TrimSpace(agentID), strings.TrimSpace(messageID)
	if agentID == "" || messageID == "" {
		return Agent{}, errors.New("agent id and message id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	source, err := scanAgent(tx.QueryRowContext(ctx, agentSelectSQL+` WHERE id = ?`, agentID).Scan)
	if err != nil {
		return Agent{}, err
	}
	var targetCreatedAt, targetSupersededAt string
	if err := tx.QueryRowContext(ctx, `SELECT created_at, COALESCE(superseded_at,'') FROM agent_messages WHERE id = ? AND agent_id = ?`, messageID, agentID).Scan(&targetCreatedAt, &targetSupersededAt); err != nil {
		return Agent{}, err
	}
	if targetSupersededAt != "" {
		return Agent{}, fmt.Errorf("%w: forking requires a message that is still current", ErrConflict)
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = source.Title + " (fork)"
	}
	now := Now()
	forkID := NewID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, workline_id, type, title, inherit_mode, parent_agent_id, fork_message_id, model, system_prompt, permission_mode, reasoning_effort, fast_mode, execution_device_id, status, plan_mode, cwd, prune_enabled, created_at, updated_at)
		VALUES (?, NULLIF(?,''), 'primary', ?, NULLIF(?,''), ?, ?, ?, NULLIF(?,''), ?, NULLIF(?,''), ?, ?, 'idle', ?, NULLIF(?,''), ?, ?, ?)`,
		forkID, source.WorklineID, title, source.InheritMode, agentID, messageID, source.Model, source.SystemPrompt, source.PermissionMode, source.ReasoningEffort, boolInt(source.FastMode), source.ExecutionDeviceID, boolInt(source.PlanMode), source.CWD, boolInt(source.PruneEnabled), now, now); err != nil {
		return Agent{}, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(correction_of_message_id,''), created_at FROM agent_messages
		WHERE agent_id = ? AND superseded_at IS NULL
		  AND (created_at < ? OR (created_at = ? AND id <= ?))
		ORDER BY created_at ASC, id ASC`, agentID, targetCreatedAt, targetCreatedAt, messageID)
	if err != nil {
		return Agent{}, err
	}
	type sourceRow struct {
		id, correctionOf, createdAt string
	}
	sourceRows := make([]sourceRow, 0)
	for rows.Next() {
		var row sourceRow
		if err := rows.Scan(&row.id, &row.correctionOf, &row.createdAt); err != nil {
			rows.Close()
			return Agent{}, err
		}
		sourceRows = append(sourceRows, row)
	}
	if err := rows.Close(); err != nil {
		return Agent{}, err
	}
	if len(sourceRows) == 0 || sourceRows[len(sourceRows)-1].id != messageID {
		return Agent{}, fmt.Errorf("%w: fork target message is not part of the current conversation", ErrConflict)
	}

	idMap := make(map[string]string, len(sourceRows))
	for _, row := range sourceRows {
		idMap[row.id] = NewID()
	}
	for _, row := range sourceRows {
		// Copy every column in SQL so new message fields cannot silently go
		// missing from forks; only identity, run linkage, and the correction
		// reference (remapped below) are rewritten.
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_messages (id, agent_id, run_id, sdk_message_uuid, parent_tool_use_id, role, content_json, provider_state_json, content_text, reasoning_text, tokens_in, cost_usd, turn_usage_json, context_percent, meter_usage, meter_unit, commit_sha, command_text, correction_of_message_id, superseded_at, created_by, completion_state, stop_reason, created_at)
			SELECT ?, ?, NULL, sdk_message_uuid, parent_tool_use_id, role, content_json, provider_state_json, content_text, reasoning_text, tokens_in, cost_usd, turn_usage_json, context_percent, meter_usage, meter_unit, commit_sha, command_text, NULL, NULL, created_by, completion_state, stop_reason, created_at
			FROM agent_messages WHERE id = ?`, idMap[row.id], forkID, row.id); err != nil {
			return Agent{}, err
		}
		if mapped, ok := idMap[row.correctionOf]; ok && row.correctionOf != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_messages SET correction_of_message_id = ? WHERE id = ?`, mapped, idMap[row.id]); err != nil {
				return Agent{}, err
			}
		}
	}

	copiedIDs := make([]string, 0, len(sourceRows))
	for _, row := range sourceRows {
		copiedIDs = append(copiedIDs, row.id)
	}
	if err := copyMessageDependents(ctx, tx, agentID, forkID, copiedIDs, idMap); err != nil {
		return Agent{}, err
	}

	lastCreatedAt := sourceRows[len(sourceRows)-1].createdAt
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET message_count = ?, last_message_at = ? WHERE id = ?`, len(sourceRows), lastCreatedAt, forkID); err != nil {
		return Agent{}, err
	}
	// Carry the rolling context summary over when its boundary row was copied;
	// a boundary outside the fork would only trip the stale-boundary fallback.
	if source.ContextSummary != "" {
		if mapped, ok := idMap[source.PruneBoundaryMessageID]; ok {
			if _, err := tx.ExecContext(ctx, `UPDATE agents SET context_summary = ?, prune_boundary_message_id = ?, pruned_percent = ? WHERE id = ?`, source.ContextSummary, mapped, source.PrunedPercent, forkID); err != nil {
				return Agent{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, err
	}
	return s.GetAgent(ctx, forkID)
}

func copyMessageDependents(ctx context.Context, tx *sql.Tx, sourceAgentID, forkAgentID string, copiedIDs []string, idMap map[string]string) error {
	copyRows := func(listQuery string, insert func(oldID, oldMessageID string) error) error {
		query, args := inClause(listQuery, []any{sourceAgentID}, copiedIDs)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		type pair struct{ id, messageID string }
		pairs := make([]pair, 0)
		for rows.Next() {
			var p pair
			if err := rows.Scan(&p.id, &p.messageID); err != nil {
				rows.Close()
				return err
			}
			pairs = append(pairs, p)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, p := range pairs {
			if err := insert(p.id, p.messageID); err != nil {
				return err
			}
		}
		return nil
	}

	if err := copyRows(`SELECT id, message_id FROM agent_message_attachments WHERE agent_id = ? AND message_id IN `, func(oldID, oldMessageID string) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_message_attachments (id, message_id, agent_id, filename, mime_type, kind, size_bytes, data_blob, model_data_blob, model_mime_type, image_width, image_height, sha256, processing_status, processing_code, processing_error, extracted_text, created_at)
			SELECT ?, ?, ?, filename, mime_type, kind, size_bytes, data_blob, model_data_blob, model_mime_type, image_width, image_height, sha256, processing_status, processing_code, processing_error, extracted_text, created_at
			FROM agent_message_attachments WHERE id = ?`, NewID(), idMap[oldMessageID], forkAgentID, oldID)
		return err
	}); err != nil {
		return err
	}

	if err := copyRows(`SELECT id, message_id FROM agent_tool_calls WHERE agent_id = ? AND message_id IN `, func(oldID, oldMessageID string) error {
		var resultMessageID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(result_message_id,'') FROM agent_tool_calls WHERE id = ?`, oldID).Scan(&resultMessageID); err != nil {
			return err
		}
		mappedResult := idMap[resultMessageID]
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_calls (id, agent_id, run_id, message_id, tool_use_id, tool_name, input_json, output_json, status, duration_ms, error_message, permission_decided_by, permission_decided_at, permission_deny_message, permission_decision_reason, permission_suggestions, permission_generation, policy_generation, execution_device_id, is_background, input_tokens, output_tokens, total_cost, provider, model, result_message_id, started_at, completed_at, created_at, updated_at)
			SELECT ?, ?, NULL, ?, tool_use_id, tool_name, input_json, output_json, status, duration_ms, error_message, permission_decided_by, permission_decided_at, permission_deny_message, permission_decision_reason, permission_suggestions, permission_generation, policy_generation, execution_device_id, is_background, input_tokens, output_tokens, total_cost, provider, model, NULLIF(?, ''), started_at, completed_at, created_at, updated_at
			FROM agent_tool_calls WHERE id = ?`, NewID(), forkAgentID, idMap[oldMessageID], mappedResult, oldID)
		return err
	}); err != nil {
		return err
	}

	// Generated image rows share content-addressed storage keys; garbage
	// collection keeps an object alive while any row references its key, so
	// copies are safe and cheap.
	return copyRows(`SELECT id, message_id FROM agent_message_generated_images WHERE agent_id = ? AND message_id IN `, func(oldID, oldMessageID string) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_message_generated_images (id, agent_id, message_id, run_id, generation_id, storage_key, sha256, mime_type, filename, byte_size, width, height, revised_prompt, output_index, status, created_at)
			SELECT ?, ?, ?, NULL, generation_id, storage_key, sha256, mime_type, filename, byte_size, width, height, revised_prompt, output_index, status, created_at
			FROM agent_message_generated_images WHERE id = ?`, NewID(), forkAgentID, idMap[oldMessageID], oldID)
		return err
	})
}

type txQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryStrings(ctx context.Context, q txQuerier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// inClause appends "(?, ?, ...)" for values to a query ending in "IN " and
// returns the combined argument list.
func inClause(query string, leading []any, values []string) (string, []any) {
	placeholders := make([]string, len(values))
	args := append([]any{}, leading...)
	for i, value := range values {
		placeholders[i] = "?"
		args = append(args, value)
	}
	return query + "(" + strings.Join(placeholders, ", ") + ")", args
}
