package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

// QueuedMessage is a follow-up the user parked while a run was still going.
// It lives in the database rather than one browser's storage so every device
// sees the same queue and the server can drain it without a page open.
type QueuedMessage struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	CreatedBy  string `json:"createdBy,omitempty"`
	Text       string `json:"text"`
	RunMode    string `json:"mode,omitempty"`
	RunContext string `json:"context,omitempty"`
	Position   int64  `json:"position"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

const queuedMessageMaxBytes = 512 << 10

// A single agent's backlog is capped so a stuck run cannot let the queue grow
// without bound.
const QueuedMessageLimitPerAgent = 50

var (
	ErrQueuedMessageInvalid  = errors.New("queued message is invalid")
	ErrQueuedMessageNotFound = errors.New("queued message was not found")
	ErrQueuedMessageLimit    = errors.New("this agent already has the maximum number of queued messages")
)

func validQueuedText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(text) > queuedMessageMaxBytes {
		return false
	}
	return utf8.ValidString(text)
}

const queuedMessageColumns = `id, agent_id, COALESCE(created_by,''), content_text, COALESCE(run_mode,''), COALESCE(run_context,''), position, created_at, updated_at`

func scanQueuedMessages(rows *sql.Rows) ([]QueuedMessage, error) {
	defer rows.Close()
	items := make([]QueuedMessage, 0)
	for rows.Next() {
		var item QueuedMessage
		if err := rows.Scan(&item.ID, &item.AgentID, &item.CreatedBy, &item.Text, &item.RunMode, &item.RunContext, &item.Position, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListQueuedMessages returns the backlog in send order.
func (s *Store) ListQueuedMessages(ctx context.Context, agentID string) ([]QueuedMessage, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, ErrQueuedMessageInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+queuedMessageColumns+` FROM agent_message_queue WHERE agent_id = ? ORDER BY position, id`, agentID)
	if err != nil {
		return nil, err
	}
	return scanQueuedMessages(rows)
}

// EnqueueMessage appends to the end of an agent's backlog.
func (s *Store) EnqueueMessage(ctx context.Context, item QueuedMessage) (QueuedMessage, error) {
	if strings.TrimSpace(item.AgentID) == "" || !validQueuedText(item.Text) {
		return QueuedMessage{}, ErrQueuedMessageInvalid
	}
	if item.ID == "" {
		item.ID = NewID()
	}
	now := Now()
	item.CreatedAt, item.UpdatedAt = now, now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueuedMessage{}, err
	}
	defer tx.Rollback()

	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_message_queue WHERE agent_id = ?`, item.AgentID).Scan(&count); err != nil {
		return QueuedMessage{}, err
	}
	if count >= QueuedMessageLimitPerAgent {
		return QueuedMessage{}, ErrQueuedMessageLimit
	}
	// Positions are sparse on purpose: reordering or inserting ahead of the queue
	// never has to rewrite the rows that follow.
	var nextPosition int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 10 FROM agent_message_queue WHERE agent_id = ?`, item.AgentID).Scan(&nextPosition); err != nil {
		return QueuedMessage{}, err
	}
	if item.Position <= 0 {
		item.Position = nextPosition
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_message_queue (id, agent_id, created_by, content_text, run_mode, run_context, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.AgentID, item.CreatedBy, item.Text, item.RunMode, item.RunContext, item.Position, item.CreatedAt, item.UpdatedAt); err != nil {
		return QueuedMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return QueuedMessage{}, err
	}
	return item, nil
}

// UpdateQueuedMessageText rewrites a parked message in place, keeping its spot
// in the queue so editing does not send it to the back.
func (s *Store) UpdateQueuedMessageText(ctx context.Context, agentID, id, text string) (QueuedMessage, error) {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(id) == "" || !validQueuedText(text) {
		return QueuedMessage{}, ErrQueuedMessageInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_message_queue SET content_text = ?, updated_at = ? WHERE id = ? AND agent_id = ?`, text, Now(), id, agentID)
	if err != nil {
		return QueuedMessage{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return QueuedMessage{}, err
	}
	if affected == 0 {
		return QueuedMessage{}, ErrQueuedMessageNotFound
	}
	return s.getQueuedMessage(ctx, agentID, id)
}

func (s *Store) getQueuedMessage(ctx context.Context, agentID, id string) (QueuedMessage, error) {
	var item QueuedMessage
	err := s.db.QueryRowContext(ctx, `SELECT `+queuedMessageColumns+` FROM agent_message_queue WHERE id = ? AND agent_id = ?`, id, agentID).
		Scan(&item.ID, &item.AgentID, &item.CreatedBy, &item.Text, &item.RunMode, &item.RunContext, &item.Position, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return QueuedMessage{}, ErrQueuedMessageNotFound
	}
	return item, err
}

// DeleteQueuedMessage drops one parked message.
func (s *Store) DeleteQueuedMessage(ctx context.Context, agentID, id string) error {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(id) == "" {
		return ErrQueuedMessageInvalid
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_message_queue WHERE id = ? AND agent_id = ?`, id, agentID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrQueuedMessageNotFound
	}
	return nil
}

// ClaimNextQueuedMessage removes and returns the head of the queue in one
// statement. Deleting as part of the claim is what stops two drain attempts
// from sending the same message twice.
func (s *Store) ClaimNextQueuedMessage(ctx context.Context, agentID string) (QueuedMessage, bool, error) {
	if strings.TrimSpace(agentID) == "" {
		return QueuedMessage{}, false, ErrQueuedMessageInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueuedMessage{}, false, err
	}
	defer tx.Rollback()

	var item QueuedMessage
	err = tx.QueryRowContext(ctx, `SELECT `+queuedMessageColumns+` FROM agent_message_queue WHERE agent_id = ? ORDER BY position, id LIMIT 1`, agentID).
		Scan(&item.ID, &item.AgentID, &item.CreatedBy, &item.Text, &item.RunMode, &item.RunContext, &item.Position, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return QueuedMessage{}, false, nil
	}
	if err != nil {
		return QueuedMessage{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_message_queue WHERE id = ?`, item.ID); err != nil {
		return QueuedMessage{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return QueuedMessage{}, false, err
	}
	return item, true, nil
}

// RestoreQueuedMessage puts a claimed message back at its original spot after a
// failed send, so a transient provider error does not cost the user their text.
func (s *Store) RestoreQueuedMessage(ctx context.Context, item QueuedMessage) error {
	if strings.TrimSpace(item.AgentID) == "" || strings.TrimSpace(item.ID) == "" || !validQueuedText(item.Text) {
		return ErrQueuedMessageInvalid
	}
	if item.CreatedAt == "" {
		item.CreatedAt = Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO agent_message_queue (id, agent_id, created_by, content_text, run_mode, run_context, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.AgentID, item.CreatedBy, item.Text, item.RunMode, item.RunContext, item.Position, item.CreatedAt, Now())
	return err
}
