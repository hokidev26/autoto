package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	// Attachments are persisted with the row so a parked screenshot survives the
	// browser closing. Blobs are omitted from JSON by Attachment's own tags.
	Attachments []Attachment `json:"attachments,omitempty"`
}

const queuedMessageMaxBytes = 512 << 10

// The upload limits in internal/server cannot be imported here without a cycle,
// so parking has its own ceilings. They are lower than the live send path on
// purpose: queued blobs sit in the database indefinitely, where a live upload
// only has to survive one request.
const (
	queuedMessageAttachmentLimit    = 10
	queuedMessageAttachmentMaxBytes = 32 << 20
)

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

// validQueuedContent accepts blank text when files came with it. The live send
// path already allows an image with no caption, and refusing it only when the
// message is parked would make the queue reject what the user could send
// directly. A message with neither is still nothing at all.
func validQueuedContent(text string, attachments []Attachment) bool {
	if len(text) > queuedMessageMaxBytes || !utf8.ValidString(text) {
		return false
	}
	if strings.TrimSpace(text) == "" {
		return len(attachments) > 0
	}
	return true
}

func validQueuedAttachments(attachments []Attachment) error {
	if len(attachments) > queuedMessageAttachmentLimit {
		return ErrQueuedMessageInvalid
	}
	var total int64
	for _, attachment := range attachments {
		// The destination table enforces these too. Failing here means the user
		// sees the error on the request that parked the message, instead of the
		// drain hitting it later with nobody watching.
		if err := validateAttachmentModelState(attachment); err != nil {
			return fmt.Errorf("%w: %v", ErrQueuedMessageInvalid, err)
		}
		total += int64(len(attachment.Data)) + int64(len(attachment.ModelData))
		if total > queuedMessageAttachmentMaxBytes {
			return ErrQueuedMessageInvalid
		}
	}
	return nil
}

const queuedMessageColumns = `id, agent_id, COALESCE(created_by,''), content_text, COALESCE(run_mode,''), COALESCE(run_context,''), position, created_at, updated_at`

// queued_message_id lands in Attachment.MessageID: the parked file has no
// message yet, and the queue row is what it belongs to until the drain sends it.
const queuedAttachmentSelectColumns = `id, queued_message_id, agent_id, filename, COALESCE(mime_type,''), kind, size_bytes, %s, %s, COALESCE(model_mime_type,''), COALESCE(image_width,0), COALESCE(image_height,0), COALESCE(sha256,''), COALESCE(processing_status,''), COALESCE(processing_code,''), COALESCE(processing_error,''), %s, created_at`

func queuedAttachmentColumns(includeData bool) string {
	if includeData {
		return fmt.Sprintf(queuedAttachmentSelectColumns, "data_blob", "COALESCE(model_data_blob,X'')", "COALESCE(extracted_text,'')")
	}
	return fmt.Sprintf(queuedAttachmentSelectColumns, "X''", "X''", "''")
}

func insertQueuedMessageAttachment(ctx context.Context, tx *sql.Tx, attachment Attachment, position int) error {
	if err := validateAttachmentModelState(attachment); err != nil {
		return fmt.Errorf("%w: %v", ErrQueuedMessageInvalid, err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_queued_message_attachments (id, queued_message_id, agent_id, filename, mime_type, kind, size_bytes, data_blob, model_data_blob, model_mime_type, image_width, image_height, sha256, processing_status, processing_code, processing_error, extracted_text, created_at, position) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attachment.ID, attachment.MessageID, attachment.AgentID, attachment.Filename, attachment.MIMEType, attachment.Kind, attachment.SizeBytes, attachment.Data, attachment.ModelData, attachment.ModelMIME, attachment.Width, attachment.Height, attachment.SHA256, attachment.ProcessingStatus, attachment.ProcessingCode, attachment.ProcessingError, attachment.ExtractedText, attachment.CreatedAt, position)
	return err
}

// storeQueuedMessageAttachments stamps the identity the rows need and writes
// them in the caller's transaction, so a queue row never commits without the
// files that belong to it.
func storeQueuedMessageAttachments(ctx context.Context, tx *sql.Tx, item QueuedMessage) ([]Attachment, error) {
	stored := make([]Attachment, 0, len(item.Attachments))
	for index, attachment := range item.Attachments {
		if attachment.ID == "" {
			attachment.ID = NewID()
		}
		attachment.MessageID = item.ID
		attachment.AgentID = item.AgentID
		if attachment.CreatedAt == "" {
			attachment.CreatedAt = item.CreatedAt
		}
		if err := insertQueuedMessageAttachment(ctx, tx, attachment, index); err != nil {
			return nil, err
		}
		stored = append(stored, attachment)
	}
	if len(stored) == 0 {
		return nil, nil
	}
	return stored, nil
}

func scanQueuedAttachments(rows *sql.Rows) (map[string][]Attachment, error) {
	defer rows.Close()
	grouped := make(map[string][]Attachment)
	for rows.Next() {
		attachment, err := scanMessageAttachment(rows.Scan)
		if err != nil {
			return nil, err
		}
		grouped[attachment.MessageID] = append(grouped[attachment.MessageID], attachment)
	}
	return grouped, rows.Err()
}

// loadQueuedAttachments reads every row's files in one statement. One query per
// queue entry would be up to QueuedMessageLimitPerAgent round trips for a single
// list call.
func loadQueuedAttachments(ctx context.Context, q rowsQuerier, queuedIDs []string, includeData bool) (map[string][]Attachment, error) {
	if len(queuedIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(queuedIDs))
	for _, id := range queuedIDs {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT `+queuedAttachmentColumns(includeData)+` FROM agent_queued_message_attachments WHERE queued_message_id IN (`+placeholders(len(queuedIDs))+`) ORDER BY position, id`, args...)
	if err != nil {
		return nil, err
	}
	return scanQueuedAttachments(rows)
}

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
	items, err := scanQueuedMessages(rows)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	// Listing is for display, so the blobs stay in the database; the drain is the
	// only caller that needs the bytes.
	grouped, err := loadQueuedAttachments(ctx, s.db, ids, false)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Attachments = grouped[items[i].ID]
	}
	return items, nil
}

// EnqueueMessage appends to the end of an agent's backlog.
func (s *Store) EnqueueMessage(ctx context.Context, item QueuedMessage) (QueuedMessage, error) {
	if strings.TrimSpace(item.AgentID) == "" || !validQueuedContent(item.Text, item.Attachments) {
		return QueuedMessage{}, ErrQueuedMessageInvalid
	}
	if err := validQueuedAttachments(item.Attachments); err != nil {
		return QueuedMessage{}, err
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
	stored, err := storeQueuedMessageAttachments(ctx, tx, item)
	if err != nil {
		return QueuedMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return QueuedMessage{}, err
	}
	item.Attachments = stored
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
	if err != nil {
		return QueuedMessage{}, err
	}
	grouped, err := loadQueuedAttachments(ctx, s.db, []string{item.ID}, false)
	if err != nil {
		return QueuedMessage{}, err
	}
	item.Attachments = grouped[item.ID]
	return item, nil
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
	// Read the files before the delete: the row's ON DELETE CASCADE takes them
	// with it, so anything loaded afterwards would come back empty and the
	// message would be sent without its attachments. The bytes are included
	// because the caller is about to hand them to the send path.
	grouped, err := loadQueuedAttachments(ctx, tx, []string{item.ID}, true)
	if err != nil {
		return QueuedMessage{}, false, err
	}
	item.Attachments = grouped[item.ID]
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
	if strings.TrimSpace(item.AgentID) == "" || strings.TrimSpace(item.ID) == "" || !validQueuedContent(item.Text, item.Attachments) {
		return ErrQueuedMessageInvalid
	}
	if err := validQueuedAttachments(item.Attachments); err != nil {
		return err
	}
	if item.CreatedAt == "" {
		item.CreatedAt = Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agent_message_queue (id, agent_id, created_by, content_text, run_mode, run_context, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.AgentID, item.CreatedBy, item.Text, item.RunMode, item.RunContext, item.Position, item.CreatedAt, Now()); err != nil {
		return err
	}
	// The claim's cascade already removed the attachment rows, and a REPLACE of
	// the parent cascades again, so re-inserting is the only way they come back.
	// The delete keeps a second restore of the same message from colliding on the
	// attachment primary keys.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_queued_message_attachments WHERE queued_message_id = ?`, item.ID); err != nil {
		return err
	}
	if _, err := storeQueuedMessageAttachments(ctx, tx, item); err != nil {
		return err
	}
	return tx.Commit()
}
