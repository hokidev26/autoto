package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const toolExecutionGroupSchemaSQL = `
CREATE TABLE IF NOT EXISTS tool_execution_groups (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  assistant_message_id TEXT NOT NULL REFERENCES agent_messages(id) ON DELETE CASCADE,
  expected_count INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  abort_reason TEXT,
  settled_at TEXT,
  aborted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (run_id, assistant_message_id),
  CHECK (expected_count > 0 AND expected_count <= 4096),
  CHECK (status IN ('open', 'settled', 'aborted')),
  CHECK (length(CAST(COALESCE(abort_reason, '') AS BLOB)) <= 4096)
);
CREATE INDEX IF NOT EXISTS idx_tool_execution_groups_run_status
  ON tool_execution_groups(run_id, status, created_at, id);
CREATE INDEX IF NOT EXISTS idx_tool_execution_groups_status
  ON tool_execution_groups(status, created_at, id);
CREATE INDEX IF NOT EXISTS idx_tool_execution_groups_assistant_message
  ON tool_execution_groups(assistant_message_id);

CREATE TABLE IF NOT EXISTS tool_execution_group_items (
  group_id TEXT NOT NULL REFERENCES tool_execution_groups(id) ON DELETE CASCADE,
  tool_use_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  ordinal INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  replay_class TEXT NOT NULL DEFAULT 'never',
  result_message_id TEXT UNIQUE REFERENCES agent_messages(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
  output_summary_json TEXT,
  terminal_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (group_id, tool_use_id),
  UNIQUE (group_id, ordinal),
  CHECK (ordinal >= 0),
  CHECK (status IN ('pending', 'completed', 'error', 'denied', 'failed', 'aborted')),
  CHECK (replay_class IN ('never', 'safe')),
  CHECK (length(CAST(tool_use_id AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(CAST(tool_name AS BLOB)) BETWEEN 1 AND 256),
  CHECK (output_summary_json IS NULL OR length(CAST(output_summary_json AS BLOB)) <= 8192)
);
CREATE INDEX IF NOT EXISTS idx_tool_execution_group_items_status
  ON tool_execution_group_items(group_id, status, ordinal);
`

// ToolExecutionGroupSchemaSQL returns the exact DDL that must be added to the
// shared schema/migration sequence before the ledger store is used.
func ToolExecutionGroupSchemaSQL() string {
	return toolExecutionGroupSchemaSQL
}

func (s *toolExecutionGroupStore) CreateToolExecutionGroup(ctx context.Context, input ToolExecutionGroupCreateInput) (ToolExecutionGroup, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.AssistantMessageID = strings.TrimSpace(input.AssistantMessageID)
	if input.ID == "" {
		input.ID = NewID()
	}
	if input.ExpectedCount <= 0 || input.ExpectedCount > 4096 || input.ExpectedCount != len(input.Items) {
		return ToolExecutionGroup{}, errors.New("tool execution group expected count must match its non-empty item ledger")
	}
	for name, value := range map[string]string{
		"tool execution group id":                   input.ID,
		"tool execution group run id":               input.RunID,
		"tool execution group assistant message id": input.AssistantMessageID,
	} {
		if err := validateP2P3Text(name, value, 256, true, false); err != nil {
			return ToolExecutionGroup{}, err
		}
	}
	seen := make(map[string]struct{}, len(input.Items))
	for index := range input.Items {
		input.Items[index].ToolUseID = strings.TrimSpace(input.Items[index].ToolUseID)
		input.Items[index].ToolName = strings.TrimSpace(input.Items[index].ToolName)
		if err := validateP2P3Text("tool execution item tool use id", input.Items[index].ToolUseID, 256, true, false); err != nil {
			return ToolExecutionGroup{}, err
		}
		if err := validateP2P3Text("tool execution item tool name", input.Items[index].ToolName, 256, true, false); err != nil {
			return ToolExecutionGroup{}, err
		}
		if _, exists := seen[input.Items[index].ToolUseID]; exists {
			return ToolExecutionGroup{}, fmt.Errorf("duplicate tool use id in execution group: %s", input.Items[index].ToolUseID)
		}
		seen[input.Items[index].ToolUseID] = struct{}{}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	defer tx.Rollback()

	var assistantRunID, assistantRole string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(run_id,''), role FROM agent_messages WHERE id = ?`, input.AssistantMessageID).Scan(&assistantRunID, &assistantRole); err != nil {
		return ToolExecutionGroup{}, err
	}
	if assistantRunID != input.RunID || assistantRole != "assistant" {
		return ToolExecutionGroup{}, fmt.Errorf("%w: tool execution group assistant message does not own the run", ErrConflict)
	}
	var runExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id = ?`, input.RunID).Scan(&runExists); err != nil {
		return ToolExecutionGroup{}, err
	}

	now := Now()
	result, err := tx.ExecContext(ctx, `INSERT INTO tool_execution_groups (id, run_id, assistant_message_id, expected_count, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'open', ?, ?) ON CONFLICT(run_id, assistant_message_id) DO NOTHING`, input.ID, input.RunID, input.AssistantMessageID, input.ExpectedCount, now, now)
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	if inserted == 1 {
		for ordinal, item := range input.Items {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tool_execution_group_items (group_id, tool_use_id, tool_name, ordinal, status, replay_class, created_at, updated_at) VALUES (?, ?, ?, ?, 'pending', ?, ?, ?)`, input.ID, item.ToolUseID, item.ToolName, ordinal, normalizeStoredReplayClass(item.ReplayClass), now, now); err != nil {
				return ToolExecutionGroup{}, err
			}
		}
	}
	group, err := getToolExecutionGroupTx(ctx, tx, input.RunID, input.AssistantMessageID)
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	if !sameToolExecutionGroupInput(group, input) {
		return ToolExecutionGroup{}, fmt.Errorf("%w: assistant message already has a different tool execution group", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return ToolExecutionGroup{}, err
	}
	return group, nil
}

func (s *toolExecutionGroupStore) GetToolExecutionGroup(ctx context.Context, groupID string) (ToolExecutionGroup, error) {
	groupID = strings.TrimSpace(groupID)
	group, err := scanToolExecutionGroup(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE id = ?`, groupID).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	group.Items, err = listToolExecutionItems(ctx, s.db, group.ID)
	return group, err
}

func (s *toolExecutionGroupStore) GetToolExecutionGroupByAssistantMessage(ctx context.Context, runID, assistantMessageID string) (ToolExecutionGroup, error) {
	group, err := scanToolExecutionGroup(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE run_id = ? AND assistant_message_id = ?`, strings.TrimSpace(runID), strings.TrimSpace(assistantMessageID)).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	group.Items, err = listToolExecutionItems(ctx, s.db, group.ID)
	return group, err
}

// GetToolExecutionGroupAssistantMessage reads only the assistant-message fields
// needed to cross-check a ledger against the turn that produced it.
//
// It is intentionally narrower than a general message getter: recovery needs the
// role and the tool-call content, and nothing else. Attachment blobs and
// provider state are excluded so a startup integrity check cannot pull image
// payloads or opaque adapter state into memory.
func (s *toolExecutionGroupStore) GetToolExecutionGroupAssistantMessage(ctx context.Context, messageID string) (Message, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return Message{}, errors.New("assistant message id is required")
	}
	var message Message
	var contentJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(run_id,''), role, COALESCE(content_json,'') FROM agent_messages WHERE id = ?`,
		messageID,
	).Scan(&message.ID, &message.RunID, &message.Role, &contentJSON)
	if err != nil {
		return Message{}, err
	}
	if contentJSON != "" {
		message.ContentJSON = json.RawMessage(contentJSON)
	}
	return message, nil
}

func (s *toolExecutionGroupStore) RecordToolExecutionItemTerminal(ctx context.Context, groupID string, input ToolExecutionItemTerminalInput) (ToolExecutionItem, error) {
	groupID = strings.TrimSpace(groupID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	input.Status = strings.TrimSpace(input.Status)
	input.ResultMessageID = strings.TrimSpace(input.ResultMessageID)
	if err := validateP2P3Text("tool execution group id", groupID, 256, true, false); err != nil {
		return ToolExecutionItem{}, err
	}
	if err := validateP2P3Text("tool execution item tool use id", input.ToolUseID, 256, true, false); err != nil {
		return ToolExecutionItem{}, err
	}
	if err := validateP2P3Text("tool execution item result message id", input.ResultMessageID, 256, true, false); err != nil {
		return ToolExecutionItem{}, err
	}
	if !recordableToolExecutionTerminalStatus(input.Status) {
		return ToolExecutionItem{}, fmt.Errorf("invalid tool execution terminal status %q", input.Status)
	}
	summary, err := normalizeToolExecutionSummary(input.OutputSummaryJSON)
	if err != nil {
		return ToolExecutionItem{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolExecutionItem{}, err
	}
	defer tx.Rollback()
	group, err := scanToolExecutionGroup(func(dest ...any) error {
		return tx.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE id = ?`, groupID).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionItem{}, err
	}
	if group.Status == ToolExecutionGroupStatusAborted {
		return ToolExecutionItem{}, fmt.Errorf("%w: tool execution group is aborted", ErrConflict)
	}
	item, err := getToolExecutionItem(ctx, tx, groupID, input.ToolUseID)
	if err != nil {
		return ToolExecutionItem{}, err
	}
	var messageRunID, messageRole, parentToolUseID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(run_id,''), role, COALESCE(parent_tool_use_id,'') FROM agent_messages WHERE id = ?`, input.ResultMessageID).Scan(&messageRunID, &messageRole, &parentToolUseID); err != nil {
		return ToolExecutionItem{}, err
	}
	if messageRunID != group.RunID || messageRole != "user" || parentToolUseID != item.ToolUseID {
		return ToolExecutionItem{}, fmt.Errorf("%w: result message does not match the tool execution ledger item", ErrConflict)
	}
	if item.Terminal() {
		if item.Status != input.Status || item.ResultMessageID != input.ResultMessageID || !bytes.Equal(item.OutputSummaryJSON, summary) {
			return ToolExecutionItem{}, fmt.Errorf("%w: terminal tool execution item cannot be changed", ErrConflict)
		}
		if err := tx.Commit(); err != nil {
			return ToolExecutionItem{}, err
		}
		return item, nil
	}
	if group.Status != ToolExecutionGroupStatusOpen {
		return ToolExecutionItem{}, fmt.Errorf("%w: tool execution group is not open", ErrConflict)
	}
	now := Now()
	result, err := tx.ExecContext(ctx, `UPDATE tool_execution_group_items SET status = ?, result_message_id = ?, output_summary_json = ?, terminal_at = ?, updated_at = ? WHERE group_id = ? AND tool_use_id = ? AND status = 'pending'`, input.Status, input.ResultMessageID, string(summary), now, now, groupID, input.ToolUseID)
	if err != nil {
		if isUniqueConstraint(err) {
			return ToolExecutionItem{}, fmt.Errorf("%w: result message already belongs to another tool execution item", ErrConflict)
		}
		return ToolExecutionItem{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ToolExecutionItem{}, err
	} else if affected != 1 {
		return ToolExecutionItem{}, fmt.Errorf("%w: tool execution item terminal CAS lost", ErrConflict)
	}
	item, err = getToolExecutionItem(ctx, tx, groupID, input.ToolUseID)
	if err != nil {
		return ToolExecutionItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolExecutionItem{}, err
	}
	return item, nil
}

func (s *toolExecutionGroupStore) SettleToolExecutionGroup(ctx context.Context, groupID string) (ToolExecutionGroup, error) {
	groupID = strings.TrimSpace(groupID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	defer tx.Rollback()
	group, err := scanToolExecutionGroup(func(dest ...any) error {
		return tx.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE id = ?`, groupID).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	if group.Status == ToolExecutionGroupStatusAborted {
		return ToolExecutionGroup{}, fmt.Errorf("%w: aborted tool execution group cannot settle", ErrConflict)
	}
	var total, terminal int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status <> 'pending' THEN 1 ELSE 0 END),0) FROM tool_execution_group_items WHERE group_id = ?`, group.ID).Scan(&total, &terminal); err != nil {
		return ToolExecutionGroup{}, err
	}
	if total != group.ExpectedCount || terminal != group.ExpectedCount {
		return ToolExecutionGroup{}, fmt.Errorf("%w: tool execution group is incomplete: expected=%d total=%d terminal=%d", ErrConflict, group.ExpectedCount, total, terminal)
	}
	if group.Status == ToolExecutionGroupStatusOpen {
		now := Now()
		result, err := tx.ExecContext(ctx, `UPDATE tool_execution_groups SET status = 'settled', settled_at = ?, updated_at = ? WHERE id = ? AND status = 'open'`, now, now, group.ID)
		if err != nil {
			return ToolExecutionGroup{}, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return ToolExecutionGroup{}, err
		} else if affected != 1 {
			return ToolExecutionGroup{}, fmt.Errorf("%w: tool execution group settlement CAS lost", ErrConflict)
		}
	}
	group, err = scanToolExecutionGroup(func(dest ...any) error {
		return tx.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE id = ?`, group.ID).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	group.Items, err = listToolExecutionItems(ctx, tx, group.ID)
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolExecutionGroup{}, err
	}
	return group, nil
}

func (s *toolExecutionGroupStore) AbortToolExecutionGroup(ctx context.Context, groupID, reason string) (ToolExecutionGroup, error) {
	groupID = strings.TrimSpace(groupID)
	reason = boundedText(strings.TrimSpace(reason), 4096)
	if reason == "" {
		reason = "tool execution group aborted"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	defer tx.Rollback()
	group, err := scanToolExecutionGroup(func(dest ...any) error {
		return tx.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE id = ?`, groupID).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	if group.Status == ToolExecutionGroupStatusSettled {
		return ToolExecutionGroup{}, fmt.Errorf("%w: settled tool execution group cannot be aborted", ErrConflict)
	}
	if group.Status == ToolExecutionGroupStatusOpen {
		now := Now()
		if _, err := tx.ExecContext(ctx, `UPDATE tool_execution_group_items SET status = 'aborted', terminal_at = ?, updated_at = ? WHERE group_id = ? AND status = 'pending'`, now, now, group.ID); err != nil {
			return ToolExecutionGroup{}, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE tool_execution_groups SET status = 'aborted', abort_reason = ?, aborted_at = ?, updated_at = ? WHERE id = ? AND status = 'open'`, reason, now, now, group.ID)
		if err != nil {
			return ToolExecutionGroup{}, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return ToolExecutionGroup{}, err
		} else if affected != 1 {
			return ToolExecutionGroup{}, fmt.Errorf("%w: tool execution group abort CAS lost", ErrConflict)
		}
	}
	group, err = scanToolExecutionGroup(func(dest ...any) error {
		return tx.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE id = ?`, group.ID).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	group.Items, err = listToolExecutionItems(ctx, tx, group.ID)
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolExecutionGroup{}, err
	}
	return group, nil
}

func (s *toolExecutionGroupStore) ListUnsettledToolExecutionGroups(ctx context.Context, limit int) ([]ToolExecutionGroup, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, toolExecutionGroupSelectSQL+` WHERE status = 'open' ORDER BY created_at ASC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	groups := make([]ToolExecutionGroup, 0)
	for rows.Next() {
		group, err := scanToolExecutionGroup(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range groups {
		groups[index].Items, err = listToolExecutionItems(ctx, s.db, groups[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return groups, nil
}

// RequireRunToolExecutionGroupsSettled is the durable settlement barrier used
// before a continuation, background wait, or subsequent model turn.
func (s *toolExecutionGroupStore) RequireRunToolExecutionGroupsSettled(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	var invalid int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_execution_groups AS g WHERE g.run_id = ? AND (
		g.status <> 'settled' OR
		g.expected_count <> (SELECT COUNT(*) FROM tool_execution_group_items AS i WHERE i.group_id = g.id) OR
		g.expected_count <> (SELECT COUNT(*) FROM tool_execution_group_items AS i WHERE i.group_id = g.id AND i.status <> 'pending')
	)`, runID).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("%w: run has %d unsettled or incomplete tool execution group(s)", ErrConflict, invalid)
	}
	return nil
}

const toolExecutionGroupSelectSQL = `SELECT id, run_id, assistant_message_id, expected_count, status, COALESCE(abort_reason,''), COALESCE(settled_at,''), COALESCE(aborted_at,''), created_at, updated_at FROM tool_execution_groups`

type toolExecutionScanner func(dest ...any) error

type toolExecutionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func scanToolExecutionGroup(scan toolExecutionScanner) (ToolExecutionGroup, error) {
	var group ToolExecutionGroup
	err := scan(&group.ID, &group.RunID, &group.AssistantMessageID, &group.ExpectedCount, &group.Status, &group.AbortReason, &group.SettledAt, &group.AbortedAt, &group.CreatedAt, &group.UpdatedAt)
	return group, err
}

func getToolExecutionGroupTx(ctx context.Context, tx *sql.Tx, runID, assistantMessageID string) (ToolExecutionGroup, error) {
	group, err := scanToolExecutionGroup(func(dest ...any) error {
		return tx.QueryRowContext(ctx, toolExecutionGroupSelectSQL+` WHERE run_id = ? AND assistant_message_id = ?`, runID, assistantMessageID).Scan(dest...)
	})
	if err != nil {
		return ToolExecutionGroup{}, err
	}
	group.Items, err = listToolExecutionItems(ctx, tx, group.ID)
	return group, err
}

func listToolExecutionItems(ctx context.Context, query toolExecutionQuerier, groupID string) ([]ToolExecutionItem, error) {
	rows, err := query.QueryContext(ctx, `SELECT group_id, tool_use_id, tool_name, ordinal, status, COALESCE(replay_class,'never'), COALESCE(result_message_id,''), COALESCE(output_summary_json,''), COALESCE(terminal_at,''), created_at, updated_at FROM tool_execution_group_items WHERE group_id = ? ORDER BY ordinal ASC`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ToolExecutionItem, 0)
	for rows.Next() {
		var item ToolExecutionItem
		var summary string
		if err := rows.Scan(&item.GroupID, &item.ToolUseID, &item.ToolName, &item.Ordinal, &item.Status, &item.ReplayClass, &item.ResultMessageID, &summary, &item.TerminalAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		// Re-narrow on read. The CHECK constraint covers rows this build wrote, but
		// a hand-edited database or a downgraded schema must not grant replay.
		item.ReplayClass = normalizeStoredReplayClass(item.ReplayClass)
		if summary != "" {
			item.OutputSummaryJSON = json.RawMessage(summary)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func getToolExecutionItem(ctx context.Context, query toolExecutionQuerier, groupID, toolUseID string) (ToolExecutionItem, error) {
	var item ToolExecutionItem
	var summary string
	err := query.QueryRowContext(ctx, `SELECT group_id, tool_use_id, tool_name, ordinal, status, COALESCE(replay_class,'never'), COALESCE(result_message_id,''), COALESCE(output_summary_json,''), COALESCE(terminal_at,''), created_at, updated_at FROM tool_execution_group_items WHERE group_id = ? AND tool_use_id = ?`, groupID, toolUseID).Scan(&item.GroupID, &item.ToolUseID, &item.ToolName, &item.Ordinal, &item.Status, &item.ReplayClass, &item.ResultMessageID, &summary, &item.TerminalAt, &item.CreatedAt, &item.UpdatedAt)
	item.ReplayClass = normalizeStoredReplayClass(item.ReplayClass)
	if summary != "" {
		item.OutputSummaryJSON = json.RawMessage(summary)
	}
	return item, err
}

func sameToolExecutionGroupInput(group ToolExecutionGroup, input ToolExecutionGroupCreateInput) bool {
	if group.RunID != input.RunID || group.AssistantMessageID != input.AssistantMessageID || group.ExpectedCount != input.ExpectedCount || len(group.Items) != len(input.Items) {
		return false
	}
	for index, item := range group.Items {
		if item.Ordinal != index || item.ToolUseID != input.Items[index].ToolUseID || item.ToolName != input.Items[index].ToolName {
			return false
		}
	}
	return true
}

func recordableToolExecutionTerminalStatus(status string) bool {
	switch status {
	case ToolExecutionItemStatusCompleted, ToolExecutionItemStatusError, ToolExecutionItemStatusDenied, ToolExecutionItemStatusFailed:
		return true
	default:
		return false
	}
}

// Replay classes are duplicated as plain strings here because internal/tools
// already imports internal/db; the store cannot import the tools package back.
// tools.ReplayClass values must stay identical to these.
const (
	ToolExecutionReplayNever = "never"
	ToolExecutionReplaySafe  = "safe"
)

// normalizeStoredReplayClass narrows any caller-supplied value to the known set
// before it is written, defaulting to "never". Callers derive the class from the
// resolved tool, but the store still refuses to persist a value it cannot
// enforce, so an unrecognised or future class can never widen replay permission.
func normalizeStoredReplayClass(value string) string {
	if strings.TrimSpace(value) == ToolExecutionReplaySafe {
		return ToolExecutionReplaySafe
	}
	return ToolExecutionReplayNever
}

func normalizeToolExecutionSummary(value json.RawMessage) (json.RawMessage, error) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || len(value) > 8192 || !json.Valid(value) {
		return nil, errors.New("tool execution output summary must be valid JSON no larger than 8192 bytes")
	}
	var summary ToolExecutionOutputSummary
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&summary); err != nil {
		return nil, fmt.Errorf("invalid tool execution output summary: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid tool execution output summary trailing data")
	}
	if len(summary.SHA256) != 64 || !isLowerHex(summary.SHA256) || summary.ByteCount < 0 {
		return nil, errors.New("invalid tool execution output summary digest or byte count")
	}
	if err := validateP2P3Text("tool execution output preview", summary.Preview, 4096, false, false); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}
