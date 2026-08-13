package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	defaultChildContextMessageLimit   = 200
	maxChildContextMessageLimit       = 500
	defaultChildContextToolCallLimit  = 120
	maxChildContextToolCallLimit      = 300
	defaultChildContextContentBytes   = 256 * 1024
	maxChildContextContentBytes       = 4 * 1024 * 1024
	maxChildContextItemContentBytes   = 64 * 1024
	maxChildContextMetadataFieldBytes = 4096
)

// ChildContextSnapshotOptions bounds the durable child-agent context exposed to
// a parent agent. A zero value uses conservative defaults.
type ChildContextSnapshotOptions struct {
	// ParentRunID, when set, binds the task to the requesting parent run and also
	// requires RunID to be the child run recorded on that same task.
	ParentRunID     string
	RunID           string
	MessageLimit    int
	ToolCallLimit   int
	MaxContentBytes int
}

// AgentMessage is the safe message projection used by child context snapshots.
// Provider state and attachment data are intentionally excluded.
type AgentMessage struct {
	ID               string          `json:"id"`
	AgentID          string          `json:"agentId"`
	RunID            string          `json:"runId,omitempty"`
	Role             string          `json:"role"`
	ContentJSON      json.RawMessage `json:"contentJson,omitempty"`
	ContentText      string          `json:"contentText,omitempty"`
	ParentToolID     string          `json:"parentToolUseId,omitempty"`
	CommandText      string          `json:"commandText,omitempty"`
	CompletionState  string          `json:"completionState,omitempty"`
	StopReason       string          `json:"stopReason,omitempty"`
	CreatedAt        string          `json:"createdAt"`
	ContentTruncated bool            `json:"contentTruncated,omitempty"`
}

// AgentToolCall is the safe audit projection used by child context snapshots.
// It omits permission policy internals, provider metadata, and device state.
type AgentToolCall struct {
	ID               string          `json:"id"`
	AgentID          string          `json:"agentId"`
	RunID            string          `json:"runId"`
	MessageID        string          `json:"messageId,omitempty"`
	ToolUseID        string          `json:"toolUseId"`
	ToolName         string          `json:"toolName"`
	InputJSON        json.RawMessage `json:"inputJson,omitempty"`
	OutputJSON       json.RawMessage `json:"outputJson,omitempty"`
	Status           string          `json:"status"`
	DurationMS       int64           `json:"durationMs,omitempty"`
	ErrorMessage     string          `json:"errorMessage,omitempty"`
	StartedAt        string          `json:"startedAt,omitempty"`
	CompletedAt      string          `json:"completedAt,omitempty"`
	CreatedAt        string          `json:"createdAt"`
	UpdatedAt        string          `json:"updatedAt"`
	ContentTruncated bool            `json:"contentTruncated,omitempty"`
}

// ChildContextSnapshot is an authorized, bounded, point-in-time view of a
// directly owned subagent. It never contains the child's system prompt or
// provider continuation state.
type ChildContextSnapshot struct {
	TaskID                  string          `json:"taskId"`
	OwnerAgentID            string          `json:"ownerAgentId"`
	ParentRunID             string          `json:"-"`
	TaskChildRunID          string          `json:"-"`
	ChildAgentID            string          `json:"childAgentId"`
	ChildAgentName          string          `json:"childAgentName"`
	ChildAgentStatus        string          `json:"childAgentStatus"`
	ContextSummary          string          `json:"contextSummary,omitempty"`
	RunID                   string          `json:"runId,omitempty"`
	RunStatus               string          `json:"runStatus,omitempty"`
	Messages                []AgentMessage  `json:"messages"`
	ToolCalls               []AgentToolCall `json:"toolCalls"`
	Partial                 bool            `json:"partial"`
	DurableThroughMessageID string          `json:"durableThroughMessageId,omitempty"`
	Digest                  string          `json:"digest,omitempty"`
}

// ReadOwnedChildContextSnapshot reads an authorized child-agent snapshot from a
// single read-only transaction. All authorization failures intentionally
// collapse to sql.ErrNoRows to avoid exposing the existence of other agents,
// tasks, or runs.
func (s *contextAskStore) ReadOwnedChildContextSnapshot(ctx context.Context, ownerAgentID, taskID string, opts ChildContextSnapshotOptions) (ChildContextSnapshot, error) {
	options, err := normalizeChildContextSnapshotOptions(opts)
	if err != nil {
		return ChildContextSnapshot{}, err
	}
	ownerAgentID = strings.TrimSpace(ownerAgentID)
	taskID = strings.TrimSpace(taskID)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ChildContextSnapshot{}, err
	}
	defer tx.Rollback()

	fetchBytes := minInt(options.MaxContentBytes, maxChildContextItemContentBytes) + utf8.UTFMax
	var taskIDRaw, ownerIDRaw, parentRunIDRaw, taskChildRunIDRaw, childIDRaw []byte
	var childNameRaw, childStatusRaw, contextSummaryRaw []byte
	err = tx.QueryRowContext(ctx, `
SELECT
	CAST(task.id AS BLOB),
	CAST(task.owner_agent_id AS BLOB),
	CAST(COALESCE(task.parent_run_id, '') AS BLOB),
	CAST(COALESCE(task.child_run_id, '') AS BLOB),
	CAST(child.id AS BLOB),
	substr(CAST(child.title AS BLOB), 1, ?),
	substr(CAST(child.status AS BLOB), 1, ?),
	substr(CAST(COALESCE(child.context_summary, '') AS BLOB), 1, ?)
FROM background_tasks AS task
JOIN agents AS child ON child.id = task.child_agent_id
WHERE task.id = ?
  AND task.owner_agent_id = ?
  AND task.kind = 'agent'
  AND COALESCE(task.child_agent_id, '') <> ''
  AND child.id <> task.owner_agent_id
  AND child.parent_agent_id = task.owner_agent_id
  AND child.type = 'subagent'
  AND (? = '' OR (task.parent_run_id = ? AND task.child_run_id = ?))`,
		maxChildContextMetadataFieldBytes+utf8.UTFMax,
		256+utf8.UTFMax,
		fetchBytes,
		taskID,
		ownerAgentID,
		options.ParentRunID,
		options.ParentRunID,
		options.RunID,
	).Scan(&taskIDRaw, &ownerIDRaw, &parentRunIDRaw, &taskChildRunIDRaw, &childIDRaw, &childNameRaw, &childStatusRaw, &contextSummaryRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ChildContextSnapshot{}, sql.ErrNoRows
		}
		return ChildContextSnapshot{}, err
	}

	childIDQuery := string(childIDRaw)
	snapshot := ChildContextSnapshot{
		TaskID:           boundedUTF8String(taskIDRaw, 256),
		OwnerAgentID:     boundedUTF8String(ownerIDRaw, 256),
		ParentRunID:      boundedUTF8String(parentRunIDRaw, 256),
		TaskChildRunID:   boundedUTF8String(taskChildRunIDRaw, 256),
		ChildAgentID:     boundedUTF8String(childIDRaw, 256),
		ChildAgentName:   boundedUTF8String(childNameRaw, maxChildContextMetadataFieldBytes),
		ChildAgentStatus: boundedUTF8String(childStatusRaw, 256),
		Messages:         make([]AgentMessage, 0, options.MessageLimit),
		ToolCalls:        make([]AgentToolCall, 0, options.ToolCallLimit),
	}

	var runIDRaw, runStatusRaw []byte
	if options.RunID != "" {
		err = tx.QueryRowContext(ctx, `SELECT CAST(id AS BLOB), substr(CAST(status AS BLOB), 1, ?) FROM runs WHERE id = ? AND agent_id = ?`, 256+utf8.UTFMax, options.RunID, childIDQuery).Scan(&runIDRaw, &runStatusRaw)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ChildContextSnapshot{}, sql.ErrNoRows
			}
			return ChildContextSnapshot{}, err
		}
	} else {
		err = tx.QueryRowContext(ctx, `SELECT CAST(id AS BLOB), substr(CAST(status AS BLOB), 1, ?) FROM runs WHERE agent_id = ? ORDER BY execution_generation DESC, created_at DESC, id DESC LIMIT 1`, 256+utf8.UTFMax, childIDQuery).Scan(&runIDRaw, &runStatusRaw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ChildContextSnapshot{}, err
		}
	}
	if len(runIDRaw) > 0 {
		snapshot.RunID = boundedUTF8String(runIDRaw, 256)
		snapshot.RunStatus = boundedUTF8String(runStatusRaw, 256)
	}

	budget := snapshotContentBudget{remaining: options.MaxContentBytes}
	if options.RunID == "" {
		summaryItemBudget := minInt(budget.remaining, maxChildContextItemContentBytes)
		snapshot.ContextSummary, snapshot.Partial = budget.takeText(contextSummaryRaw, &summaryItemBudget)
	}
	if !terminalChildAgentStatus(snapshot.ChildAgentStatus) || snapshot.RunID != "" && !terminalChildRunStatus(snapshot.RunStatus) {
		snapshot.Partial = true
	}

	rawMessages, moreMessages, err := readChildContextMessages(ctx, tx, childIDQuery, snapshot.RunID, options.MessageLimit, fetchBytes)
	if err != nil {
		return ChildContextSnapshot{}, err
	}
	if moreMessages {
		snapshot.Partial = true
	}
	for _, raw := range rawMessages {
		itemBudget := minInt(budget.remaining, maxChildContextItemContentBytes)
		message := AgentMessage{
			ID:              boundedUTF8String(raw.id, 256),
			AgentID:         boundedUTF8String(raw.agentID, 256),
			RunID:           boundedUTF8String(raw.runID, 256),
			Role:            boundedUTF8String(raw.role, 256),
			ParentToolID:    boundedUTF8String(raw.parentToolID, 256),
			CompletionState: boundedUTF8String(raw.completionState, 256),
			StopReason:      boundedUTF8String(raw.stopReason, maxChildContextMetadataFieldBytes),
			CreatedAt:       boundedUTF8String(raw.createdAt, 256),
		}
		var truncated bool
		message.ContentText, truncated = budget.takeText(raw.contentText, &itemBudget)
		message.ContentTruncated = message.ContentTruncated || truncated
		message.ContentJSON, truncated = budget.takeJSON(raw.contentJSON, &itemBudget)
		message.ContentTruncated = message.ContentTruncated || truncated
		message.CommandText, truncated = budget.takeText(raw.commandText, &itemBudget)
		message.ContentTruncated = message.ContentTruncated || truncated
		if message.ContentTruncated {
			snapshot.Partial = true
		}
		snapshot.Messages = append(snapshot.Messages, message)
	}
	if len(snapshot.Messages) > 0 {
		snapshot.DurableThroughMessageID = snapshot.Messages[len(snapshot.Messages)-1].ID
	}

	if snapshot.RunID != "" {
		rawCalls, moreCalls, err := readChildContextToolCalls(ctx, tx, childIDQuery, string(runIDRaw), options.ToolCallLimit, fetchBytes)
		if err != nil {
			return ChildContextSnapshot{}, err
		}
		if moreCalls {
			snapshot.Partial = true
		}
		for _, raw := range rawCalls {
			itemBudget := minInt(budget.remaining, maxChildContextItemContentBytes)
			call := AgentToolCall{
				ID:          boundedUTF8String(raw.id, 256),
				AgentID:     boundedUTF8String(raw.agentID, 256),
				RunID:       boundedUTF8String(raw.runID, 256),
				MessageID:   boundedUTF8String(raw.messageID, 256),
				ToolUseID:   boundedUTF8String(raw.toolUseID, 256),
				ToolName:    boundedUTF8String(raw.toolName, maxChildContextMetadataFieldBytes),
				Status:      boundedUTF8String(raw.status, 256),
				DurationMS:  raw.durationMS,
				StartedAt:   boundedUTF8String(raw.startedAt, 256),
				CompletedAt: boundedUTF8String(raw.completedAt, 256),
				CreatedAt:   boundedUTF8String(raw.createdAt, 256),
				UpdatedAt:   boundedUTF8String(raw.updatedAt, 256),
			}
			var truncated bool
			call.OutputJSON, truncated = budget.takeJSON(raw.outputJSON, &itemBudget)
			call.ContentTruncated = call.ContentTruncated || truncated
			call.InputJSON, truncated = budget.takeJSON(raw.inputJSON, &itemBudget)
			call.ContentTruncated = call.ContentTruncated || truncated
			call.ErrorMessage, truncated = budget.takeText(raw.errorMessage, &itemBudget)
			call.ContentTruncated = call.ContentTruncated || truncated
			if call.ContentTruncated {
				snapshot.Partial = true
			}
			snapshot.ToolCalls = append(snapshot.ToolCalls, call)
		}
	}

	digestPayload, err := json.Marshal(snapshot)
	if err != nil {
		return ChildContextSnapshot{}, fmt.Errorf("marshal child context snapshot digest: %w", err)
	}
	digest := sha256.Sum256(digestPayload)
	snapshot.Digest = hex.EncodeToString(digest[:])

	if err := tx.Commit(); err != nil {
		return ChildContextSnapshot{}, err
	}
	return snapshot, nil
}

func normalizeChildContextSnapshotOptions(opts ChildContextSnapshotOptions) (ChildContextSnapshotOptions, error) {
	opts.ParentRunID = strings.TrimSpace(opts.ParentRunID)
	opts.RunID = strings.TrimSpace(opts.RunID)
	if opts.ParentRunID != "" && opts.RunID == "" {
		return ChildContextSnapshotOptions{}, errors.New("child context parent run binding requires a child run")
	}
	if opts.MessageLimit == 0 {
		opts.MessageLimit = defaultChildContextMessageLimit
	}
	if opts.MessageLimit < 1 || opts.MessageLimit > maxChildContextMessageLimit {
		return ChildContextSnapshotOptions{}, fmt.Errorf("child context message limit must be between 1 and %d", maxChildContextMessageLimit)
	}
	if opts.ToolCallLimit == 0 {
		opts.ToolCallLimit = defaultChildContextToolCallLimit
	}
	if opts.ToolCallLimit < 1 || opts.ToolCallLimit > maxChildContextToolCallLimit {
		return ChildContextSnapshotOptions{}, fmt.Errorf("child context tool call limit must be between 1 and %d", maxChildContextToolCallLimit)
	}
	if opts.MaxContentBytes == 0 {
		opts.MaxContentBytes = defaultChildContextContentBytes
	}
	if opts.MaxContentBytes < 1 || opts.MaxContentBytes > maxChildContextContentBytes {
		return ChildContextSnapshotOptions{}, fmt.Errorf("child context content budget must be between 1 and %d bytes", maxChildContextContentBytes)
	}
	return opts, nil
}

type rawChildContextMessage struct {
	id              []byte
	agentID         []byte
	runID           []byte
	role            []byte
	contentJSON     []byte
	contentText     []byte
	parentToolID    []byte
	commandText     []byte
	completionState []byte
	stopReason      []byte
	createdAt       []byte
}

func readChildContextMessages(ctx context.Context, tx *sql.Tx, childAgentID, runID string, limit, fetchBytes int) ([]rawChildContextMessage, bool, error) {
	query := `
SELECT
	CAST(id AS BLOB),
	CAST(agent_id AS BLOB),
	CAST(COALESCE(run_id, '') AS BLOB),
	CAST(role AS BLOB),
	substr(CAST(COALESCE(content_json, '') AS BLOB), 1, ?),
	substr(CAST(COALESCE(content_text, '') AS BLOB), 1, ?),
	CAST(COALESCE(parent_tool_use_id, '') AS BLOB),
	substr(CAST(COALESCE(command_text, '') AS BLOB), 1, ?),
	CAST(COALESCE(completion_state, '') AS BLOB),
	CAST(COALESCE(stop_reason, '') AS BLOB),
	CAST(created_at AS BLOB)
FROM agent_messages
WHERE agent_id = ? AND COALESCE(run_id, '') = ?
ORDER BY created_at DESC, id DESC
LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, fetchBytes, fetchBytes, fetchBytes, childAgentID, runID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	messages := make([]rawChildContextMessage, 0, limit+1)
	for rows.Next() {
		var message rawChildContextMessage
		if err := rows.Scan(
			&message.id,
			&message.agentID,
			&message.runID,
			&message.role,
			&message.contentJSON,
			&message.contentText,
			&message.parentToolID,
			&message.commandText,
			&message.completionState,
			&message.stopReason,
			&message.createdAt,
		); err != nil {
			return nil, false, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(messages) > limit
	if more {
		messages = messages[:limit]
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages, more, nil
}

type rawChildContextToolCall struct {
	id           []byte
	agentID      []byte
	runID        []byte
	messageID    []byte
	toolUseID    []byte
	toolName     []byte
	inputJSON    []byte
	outputJSON   []byte
	status       []byte
	durationMS   int64
	errorMessage []byte
	startedAt    []byte
	completedAt  []byte
	createdAt    []byte
	updatedAt    []byte
}

func readChildContextToolCalls(ctx context.Context, tx *sql.Tx, childAgentID, runID string, limit, fetchBytes int) ([]rawChildContextToolCall, bool, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT
	CAST(id AS BLOB),
	CAST(agent_id AS BLOB),
	CAST(COALESCE(run_id, '') AS BLOB),
	CAST(COALESCE(message_id, '') AS BLOB),
	CAST(tool_use_id AS BLOB),
	CAST(tool_name AS BLOB),
	substr(CAST(COALESCE(input_json, '') AS BLOB), 1, ?),
	substr(CAST(COALESCE(output_json, '') AS BLOB), 1, ?),
	CAST(status AS BLOB),
	COALESCE(duration_ms, 0),
	substr(CAST(COALESCE(error_message, '') AS BLOB), 1, ?),
	CAST(COALESCE(started_at, '') AS BLOB),
	CAST(COALESCE(completed_at, '') AS BLOB),
	CAST(created_at AS BLOB),
	CAST(COALESCE(updated_at, created_at) AS BLOB)
FROM agent_tool_calls
WHERE agent_id = ? AND run_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?`, fetchBytes, fetchBytes, fetchBytes, childAgentID, runID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	calls := make([]rawChildContextToolCall, 0, limit+1)
	for rows.Next() {
		var call rawChildContextToolCall
		if err := rows.Scan(
			&call.id,
			&call.agentID,
			&call.runID,
			&call.messageID,
			&call.toolUseID,
			&call.toolName,
			&call.inputJSON,
			&call.outputJSON,
			&call.status,
			&call.durationMS,
			&call.errorMessage,
			&call.startedAt,
			&call.completedAt,
			&call.createdAt,
			&call.updatedAt,
		); err != nil {
			return nil, false, err
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(calls) > limit
	if more {
		calls = calls[:limit]
	}
	for left, right := 0, len(calls)-1; left < right; left, right = left+1, right-1 {
		calls[left], calls[right] = calls[right], calls[left]
	}
	return calls, more, nil
}

type snapshotContentBudget struct {
	remaining int
}

func (budget *snapshotContentBudget) takeText(raw []byte, itemRemaining *int) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	valid := []byte(strings.ToValidUTF8(string(raw), "\uFFFD"))
	allowance := minInt(budget.remaining, *itemRemaining)
	bounded := truncateValidUTF8(valid, allowance)
	budget.remaining -= len(bounded)
	*itemRemaining -= len(bounded)
	return string(bounded), len(bounded) < len(valid) || !utf8.Valid(raw)
}

func (budget *snapshotContentBudget) takeJSON(raw []byte, itemRemaining *int) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	wasValidUTF8 := utf8.Valid(raw)
	valid := []byte(strings.ToValidUTF8(string(raw), "\uFFFD"))
	allowance := minInt(budget.remaining, *itemRemaining)
	if wasValidUTF8 && json.Valid(valid) && len(valid) <= allowance {
		out := append(json.RawMessage(nil), valid...)
		budget.remaining -= len(out)
		*itemRemaining -= len(out)
		return out, false
	}
	out := boundedJSONString(valid, allowance)
	budget.remaining -= len(out)
	*itemRemaining -= len(out)
	return out, true
}

func boundedJSONString(valid []byte, limit int) json.RawMessage {
	if limit < 2 {
		return nil
	}
	if encoded, err := json.Marshal(string(valid)); err == nil && len(encoded) <= limit {
		return encoded
	}
	low, high := 0, len(valid)
	best := json.RawMessage(`""`)
	for low <= high {
		middle := low + (high-low)/2
		prefix := truncateValidUTF8(valid, middle)
		encoded, err := json.Marshal(string(prefix))
		if err != nil {
			high = middle - 1
			continue
		}
		if len(encoded) <= limit {
			best = encoded
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}

func boundedUTF8String(raw []byte, limit int) string {
	valid := []byte(strings.ToValidUTF8(string(raw), "\uFFFD"))
	return string(truncateValidUTF8(valid, limit))
}

func truncateValidUTF8(valid []byte, limit int) []byte {
	if limit <= 0 {
		return nil
	}
	if len(valid) <= limit {
		return valid
	}
	end := limit
	for end > 0 && !utf8.Valid(valid[:end]) {
		end--
	}
	return valid[:end]
}

func terminalChildAgentStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "idle", "completed", "succeeded", "failed", "error", "interrupted", "canceled", "cancelled", "superseded", "skipped", "denied":
		return true
	default:
		return false
	}
}

func terminalChildRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "failed", "error", "interrupted", "canceled", "cancelled", "superseded", "skipped", "denied":
		return true
	default:
		return false
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
