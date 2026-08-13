package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxPersistedModelImageBytes = 4 << 20

const attachmentSelectColumns = `id, message_id, agent_id, filename, COALESCE(mime_type,''), kind, size_bytes, %s, %s, COALESCE(model_mime_type,''), COALESCE(image_width,0), COALESCE(image_height,0), COALESCE(sha256,''), COALESCE(processing_status,''), COALESCE(processing_code,''), COALESCE(processing_error,''), %s, created_at`

type attachmentScanner func(...any) error

func validateAttachmentModelState(attachment Attachment) error {
	if len(attachment.ModelData) > maxPersistedModelImageBytes {
		return errors.New("attachment model image exceeds 4 MiB")
	}
	if len(attachment.ProcessingCode) > 64 || !utf8.ValidString(attachment.ProcessingCode) {
		return errors.New("invalid attachment processing code")
	}
	if len([]byte(attachment.ProcessingError)) > 2048 || !utf8.ValidString(attachment.ProcessingError) {
		return errors.New("invalid attachment processing error")
	}
	validSHA := attachment.SHA256 == "" || isLowerHexSHA256(attachment.SHA256)
	if !validSHA {
		return errors.New("invalid attachment sha256")
	}
	switch attachment.ProcessingStatus {
	case "":
		if len(attachment.ModelData) != 0 || attachment.ModelMIME != "" || attachment.Width != 0 || attachment.Height != 0 || attachment.SHA256 != "" || attachment.ProcessingCode != "" || attachment.ProcessingError != "" {
			return errors.New("legacy attachment model state must be empty")
		}
	case "ready":
		if len(attachment.ModelData) == 0 || (attachment.ModelMIME != "image/png" && attachment.ModelMIME != "image/jpeg") || attachment.Width < 1 || attachment.Width > 8192 || attachment.Height < 1 || attachment.Height > 8192 || attachment.SHA256 == "" || attachment.ProcessingCode != "" || attachment.ProcessingError != "" {
			return errors.New("invalid ready attachment model state")
		}
	case "rejected":
		if len(attachment.ModelData) != 0 || attachment.ModelMIME != "" || attachment.Width != 0 || attachment.Height != 0 || attachment.SHA256 == "" || strings.TrimSpace(attachment.ProcessingCode) == "" || strings.TrimSpace(attachment.ProcessingError) == "" {
			return errors.New("invalid rejected attachment model state")
		}
	default:
		return errors.New("invalid attachment processing status")
	}
	return nil
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func insertMessageAttachment(ctx context.Context, tx *sql.Tx, attachment Attachment) error {
	if err := validateAttachmentModelState(attachment); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_message_attachments (id, message_id, agent_id, filename, mime_type, kind, size_bytes, data_blob, model_data_blob, model_mime_type, image_width, image_height, sha256, processing_status, processing_code, processing_error, extracted_text, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?)`, attachment.ID, attachment.MessageID, attachment.AgentID, attachment.Filename, attachment.MIMEType, attachment.Kind, attachment.SizeBytes, attachment.Data, attachment.ModelData, attachment.ModelMIME, attachment.Width, attachment.Height, attachment.SHA256, attachment.ProcessingStatus, attachment.ProcessingCode, attachment.ProcessingError, attachment.ExtractedText, attachment.CreatedAt)
	return err
}

func scanMessageAttachment(scan attachmentScanner) (Attachment, error) {
	var attachment Attachment
	if err := scan(&attachment.ID, &attachment.MessageID, &attachment.AgentID, &attachment.Filename, &attachment.MIMEType, &attachment.Kind, &attachment.SizeBytes, &attachment.Data, &attachment.ModelData, &attachment.ModelMIME, &attachment.Width, &attachment.Height, &attachment.SHA256, &attachment.ProcessingStatus, &attachment.ProcessingCode, &attachment.ProcessingError, &attachment.ExtractedText, &attachment.CreatedAt); err != nil {
		return Attachment{}, err
	}
	return attachment, nil
}

func (s *messageStore) GetMessageDraft(ctx context.Context, userID, agentID string) (MessageDraft, error) {
	var draft MessageDraft
	err := s.db.QueryRowContext(ctx, `SELECT user_id, agent_id, content_text, version, updated_at FROM message_drafts WHERE user_id = ? AND agent_id = ?`, userID, agentID).Scan(&draft.UserID, &draft.AgentID, &draft.ContentText, &draft.Version, &draft.UpdatedAt)
	return draft, err
}

func (s *messageStore) PutMessageDraft(ctx context.Context, draft MessageDraft, expectedVersion int64) (MessageDraft, error) {
	if draft.UserID == "" || draft.AgentID == "" || expectedVersion < 0 || !utf8.ValidString(draft.ContentText) {
		return MessageDraft{}, errors.New("invalid message draft")
	}
	now := Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MessageDraft{}, err
	}
	defer tx.Rollback()
	if expectedVersion == 0 {
		draft.Version, draft.UpdatedAt = 1, now
		result, err := tx.ExecContext(ctx, `INSERT INTO message_drafts (user_id, agent_id, content_text, version, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(user_id, agent_id) DO NOTHING`, draft.UserID, draft.AgentID, draft.ContentText, draft.Version, draft.UpdatedAt)
		if err != nil {
			return MessageDraft{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return MessageDraft{}, err
		}
		if affected != 1 {
			return MessageDraft{}, fmt.Errorf("%w: message draft was updated by another client", ErrConflict)
		}
	} else {
		draft.Version, draft.UpdatedAt = expectedVersion+1, now
		result, err := tx.ExecContext(ctx, `UPDATE message_drafts SET content_text = ?, version = ?, updated_at = ? WHERE user_id = ? AND agent_id = ? AND version = ?`, draft.ContentText, draft.Version, draft.UpdatedAt, draft.UserID, draft.AgentID, expectedVersion)
		if err != nil {
			return MessageDraft{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return MessageDraft{}, err
		}
		if affected != 1 {
			return MessageDraft{}, fmt.Errorf("%w: message draft was updated by another client", ErrConflict)
		}
	}
	if err := tx.Commit(); err != nil {
		return MessageDraft{}, err
	}
	return draft, nil
}

func (s *messageStore) DeleteMessageDraft(ctx context.Context, userID, agentID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM message_drafts WHERE user_id = ? AND agent_id = ?`, userID, agentID)
	return err
}

func (s *messageStore) AddMessage(ctx context.Context, msg Message) (Message, error) {
	return s.AddMessageWithAttachments(ctx, msg, msg.Attachments)
}

// AssignMessageRun reports sql.ErrNoRows when the message it was told to bind
// does not exist, so IsNotFound recognises it.
//
// It used to be a bare UPDATE returning only the driver error. A wrong or deleted
// message ID matched no row, changed nothing, and reported success, so the message
// silently kept no run. Every caller here creates the message immediately before
// binding it, which means a miss is a real inconsistency rather than an expected
// outcome, and staying quiet about it hid the failure from the caller that could
// still do something about it.
func (s *messageStore) AssignMessageRun(ctx context.Context, agentID, messageID, runID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_messages SET run_id = NULLIF(?, '') WHERE agent_id = ? AND id = ?`, runID, agentID, messageID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("assign run %s to message %s for agent %s: %w", runID, messageID, agentID, sql.ErrNoRows)
	}
	return nil
}

// messageCreatedByColumn maps non-user actors to NULL before the insert:
// agent_messages.created_by has a foreign key to users(id), so only local user
// ids may be stored. "api" and remote peers ("peer:<fingerprint prefix>") keep
// their attribution in the audit trail instead of this column.
func messageCreatedByColumn(createdBy string) string {
	createdBy = strings.TrimSpace(createdBy)
	if createdBy == "api" || strings.HasPrefix(createdBy, "peer:") {
		return ""
	}
	return createdBy
}

func (s *messageStore) AddMessageWithAttachments(ctx context.Context, msg Message, attachments []Attachment) (Message, error) {
	if msg.ID == "" {
		msg.ID = NewID()
	}
	if msg.CreatedAt == "" {
		msg.CreatedAt = Now()
	}
	if msg.ContentJSON == nil && msg.ContentText != "" {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": msg.ContentText}})
		msg.ContentJSON = content
	}
	turnUsageJSON := ""
	if msg.TurnUsage != nil {
		encoded, err := json.Marshal(msg.TurnUsage)
		if err != nil {
			return Message{}, err
		}
		turnUsageJSON = string(encoded)
	}
	createdBy := messageCreatedByColumn(msg.CreatedBy)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_messages (id, agent_id, run_id, parent_tool_use_id, role, content_json, provider_state_json, content_text, reasoning_text, turn_usage_json, command_text, correction_of_message_id, created_by, completion_state, stop_reason, created_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`, msg.ID, msg.AgentID, msg.RunID, nullEmpty(msg.ParentToolID), msg.Role, string(msg.ContentJSON), string(msg.ProviderStateJSON), msg.ContentText, msg.ReasoningText, turnUsageJSON, nullEmpty(msg.CommandText), msg.CorrectionOfMessageID, createdBy, msg.CompletionState, msg.StopReason, msg.CreatedAt); err != nil {
		return Message{}, err
	}
	storedAttachments := make([]Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.ID == "" {
			attachment.ID = NewID()
		}
		attachment.MessageID = msg.ID
		attachment.AgentID = msg.AgentID
		if attachment.CreatedAt == "" {
			attachment.CreatedAt = msg.CreatedAt
		}
		if err := insertMessageAttachment(ctx, tx, attachment); err != nil {
			return Message{}, err
		}
		storedAttachments = append(storedAttachments, attachment)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET message_count = message_count + 1, last_message_at = ?, updated_at = ? WHERE id = ?`, msg.CreatedAt, msg.CreatedAt, msg.AgentID); err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	msg.Attachments = attachmentMetadata(storedAttachments)
	return msg, nil
}

// CreateCorrectionWithRun creates a new user message instead of modifying its
// source. Retained attachments are copied into new rows so the original message
// remains immutable even if the correction is later deleted.
func (s *messageStore) CreateCorrectionWithRun(ctx context.Context, agentID, sourceMessageID, contentText, commandText, createdBy string, keepAttachmentIDs []string, attachments []Attachment) (Message, Run, error) {
	if strings.TrimSpace(contentText) == "" && len(keepAttachmentIDs) == 0 && len(attachments) == 0 {
		return Message{}, Run{}, errors.New("text, files, or keepAttachmentIds is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, Run{}, err
	}
	defer tx.Rollback()
	var role, sourceCreatedAt string
	if err := tx.QueryRowContext(ctx, `SELECT role, created_at FROM agent_messages WHERE id = ? AND agent_id = ?`, sourceMessageID, agentID).Scan(&role, &sourceCreatedAt); err != nil {
		return Message{}, Run{}, err
	}
	if role != "user" {
		return Message{}, Run{}, fmt.Errorf("%w: corrections require a user source message", ErrConflict)
	}

	retained := make([]Attachment, 0, len(keepAttachmentIDs))
	seen := make(map[string]struct{}, len(keepAttachmentIDs))
	for _, attachmentID := range keepAttachmentIDs {
		if attachmentID == "" {
			return Message{}, Run{}, errors.New("invalid keepAttachmentIds")
		}
		if _, ok := seen[attachmentID]; ok {
			return Message{}, Run{}, errors.New("duplicate keepAttachmentIds")
		}
		seen[attachmentID] = struct{}{}
		attachment, err := scanMessageAttachment(tx.QueryRowContext(ctx, `SELECT `+fmt.Sprintf(attachmentSelectColumns, "data_blob", "COALESCE(model_data_blob,X'')", "COALESCE(extracted_text,'')")+` FROM agent_message_attachments WHERE id = ? AND message_id = ? AND agent_id = ?`, attachmentID, sourceMessageID, agentID).Scan)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Message{}, Run{}, fmt.Errorf("%w: attachment does not belong to source message", ErrConflict)
			}
			return Message{}, Run{}, err
		}
		attachment.ID = ""
		retained = append(retained, attachment)
	}

	now := Now()
	message := Message{ID: NewID(), AgentID: agentID, Role: "user", ContentText: contentText, CommandText: commandText, CorrectionOfMessageID: sourceMessageID, CreatedBy: createdBy, CreatedAt: now}
	if message.ContentText != "" {
		content, _ := json.Marshal([]map[string]string{{"type": "text", "text": message.ContentText}})
		message.ContentJSON = content
	}
	createdBy = messageCreatedByColumn(createdBy)
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_messages (id, agent_id, role, content_json, content_text, command_text, correction_of_message_id, created_by, created_at) VALUES (?, ?, 'user', ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?)`, message.ID, message.AgentID, string(message.ContentJSON), message.ContentText, message.CommandText, sourceMessageID, createdBy, message.CreatedAt); err != nil {
		return Message{}, Run{}, err
	}

	allAttachments := append(retained, attachments...)
	storedAttachments := make([]Attachment, 0, len(allAttachments))
	for _, attachment := range allAttachments {
		if attachment.ID == "" {
			attachment.ID = NewID()
		}
		attachment.MessageID = message.ID
		attachment.AgentID = agentID
		if attachment.CreatedAt == "" {
			attachment.CreatedAt = now
		}
		if err := insertMessageAttachment(ctx, tx, attachment); err != nil {
			return Message{}, Run{}, err
		}
		storedAttachments = append(storedAttachments, attachment)
	}
	// Retire the conversation that followed the corrected message, and the
	// corrected message itself: replying to a question the user withdrew is what
	// makes an edited turn feel ignored. Ordering is the (created_at, id) tuple
	// the whole conversation is read by — there is no sequence column — and the
	// new message is excluded because it was inserted with this same `now`.
	// Already-superseded rows keep their original timestamp so repeated
	// corrections do not rewrite history that was retired earlier.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_messages SET superseded_at = ?
		WHERE agent_id = ? AND superseded_at IS NULL AND id <> ?
		  AND (created_at > ? OR (created_at = ? AND id >= ?))`,
		now, agentID, message.ID, sourceCreatedAt, sourceCreatedAt, sourceMessageID); err != nil {
		return Message{}, Run{}, err
	}

	run := Run{ID: NewID(), AgentID: agentID, TriggerMessageID: message.ID, Status: "pending", CheckpointState: RunCheckpointNone, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, agent_id, trigger_message_id, status, checkpoint_state, created_at, updated_at) VALUES (?, ?, ?, 'pending', ?, ?, ?)`, run.ID, run.AgentID, run.TriggerMessageID, run.CheckpointState, run.CreatedAt, run.UpdatedAt); err != nil {
		return Message{}, Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_messages SET run_id = ? WHERE id = ? AND agent_id = ?`, run.ID, message.ID, agentID); err != nil {
		return Message{}, Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET message_count = message_count + 1, last_message_at = ?, updated_at = ? WHERE id = ?`, now, now, agentID); err != nil {
		return Message{}, Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, Run{}, err
	}
	message.RunID = run.ID
	message.Attachments = attachmentMetadata(storedAttachments)
	return message, run, nil
}

// CreateRerunForMessage starts a fresh run for a user message that is already in
// the conversation, adding nothing to it.
//
// Retrying a failed run is the same question asked again, but the only way to
// re-ask it used to be CreateCorrectionWithRun, which by design writes a new
// user row and retires the old one. Six retries therefore left six copies of
// the prompt in the transcript, each labelled as a correction of the last. A
// rerun leaves the message exactly where it is and only retires what the failed
// attempt produced after it, so the model is asked against the same history it
// saw the first time.
func (s *messageStore) CreateRerunForMessage(ctx context.Context, agentID, messageID string) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	var role, sourceCreatedAt, supersededAt string
	if err := tx.QueryRowContext(ctx, `SELECT role, created_at, COALESCE(superseded_at,'') FROM agent_messages WHERE id = ? AND agent_id = ?`, messageID, agentID).Scan(&role, &sourceCreatedAt, &supersededAt); err != nil {
		return Run{}, err
	}
	if role != "user" {
		return Run{}, fmt.Errorf("%w: a rerun requires a user message", ErrConflict)
	}
	if supersededAt != "" {
		return Run{}, fmt.Errorf("%w: a rerun requires a message that is still current", ErrConflict)
	}
	now := Now()
	// Strictly after: the correction path uses id >= because it retires the
	// message it replaces, and this one must keep it.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_messages SET superseded_at = ?
		WHERE agent_id = ? AND superseded_at IS NULL
		  AND (created_at > ? OR (created_at = ? AND id > ?))`,
		now, agentID, sourceCreatedAt, sourceCreatedAt, messageID); err != nil {
		return Run{}, err
	}
	run := Run{ID: NewID(), AgentID: agentID, TriggerMessageID: messageID, Status: "pending", CheckpointState: RunCheckpointNone, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, agent_id, trigger_message_id, status, checkpoint_state, created_at, updated_at) VALUES (?, ?, ?, 'pending', ?, ?, ?)`, run.ID, run.AgentID, run.TriggerMessageID, run.CheckpointState, run.CreatedAt, run.UpdatedAt); err != nil {
		return Run{}, err
	}
	// Point the message at the run now working on it. message_count and
	// last_message_at stay untouched: nothing was said.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_messages SET run_id = ? WHERE id = ? AND agent_id = ?`, run.ID, messageID, agentID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *messageStore) ListMessages(ctx context.Context, agentID string) ([]Message, error) {
	messages, err := s.listMessages(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if err := s.populateMessageAttachments(ctx, messages, false); err != nil {
		return nil, err
	}
	if err := s.populateMessageGeneratedImages(ctx, messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *messageStore) ListMessagesPage(ctx context.Context, agentID, before string, limit int) (MessagePage, error) {
	if limit <= 0 {
		limit = DefaultMessagePageLimit
	}
	if limit > MaxMessagePageLimit {
		limit = MaxMessagePageLimit
	}
	cursor, err := decodeMessageCursor(before)
	if err != nil {
		return MessagePage{}, err
	}
	query := `SELECT id, agent_id, COALESCE(run_id,''), role, COALESCE(content_json,''), COALESCE(provider_state_json,''), COALESCE(content_text,''), COALESCE(reasoning_text,''), COALESCE(turn_usage_json,''), COALESCE(parent_tool_use_id,''), COALESCE(command_text,''), COALESCE(correction_of_message_id,''), COALESCE(superseded_at,''), COALESCE(created_by,''), COALESCE(completion_state,''), COALESCE(stop_reason,''), created_at FROM agent_messages WHERE agent_id = ?`
	args := []any{agentID}
	if cursor.ID != "" {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return MessagePage{}, err
	}
	messages, err := scanMessages(rows)
	if err != nil {
		return MessagePage{}, err
	}
	page := MessagePage{Messages: messages}
	if len(page.Messages) > limit {
		page.HasMoreBefore = true
		page.Messages = page.Messages[:limit]
	}
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	if page.HasMoreBefore && len(page.Messages) > 0 {
		page.NextBefore, err = encodeMessageCursor(messageCursor{CreatedAt: page.Messages[0].CreatedAt, ID: page.Messages[0].ID})
		if err != nil {
			return MessagePage{}, err
		}
	}
	if err := s.populateMessageAttachments(ctx, page.Messages, false); err != nil {
		return MessagePage{}, err
	}
	if err := s.populateMessageGeneratedImages(ctx, page.Messages); err != nil {
		return MessagePage{}, err
	}
	return page, nil
}

func (s *messageStore) ListMessagesWithAttachmentData(ctx context.Context, agentID string) ([]Message, error) {
	messages, err := s.listMessages(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if err := s.populateMessageAttachments(ctx, messages, true); err != nil {
		return nil, err
	}
	if err := s.populateMessageGeneratedImages(ctx, messages); err != nil {
		return nil, err
	}
	return messages, nil
}

type messageCursor struct {
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func encodeMessageCursor(cursor messageCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeMessageCursor(value string) (messageCursor, error) {
	if strings.TrimSpace(value) == "" {
		return messageCursor{}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return messageCursor{}, ErrInvalidCursor
	}
	var cursor messageCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.CreatedAt == "" || cursor.ID == "" {
		return messageCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

func (s *messageStore) listMessages(ctx context.Context, agentID string) ([]Message, error) {
	rows, err := s.reader().QueryContext(ctx, `SELECT id, agent_id, COALESCE(run_id,''), role, COALESCE(content_json,''), COALESCE(provider_state_json,''), COALESCE(content_text,''), COALESCE(reasoning_text,''), COALESCE(turn_usage_json,''), COALESCE(parent_tool_use_id,''), COALESCE(command_text,''), COALESCE(correction_of_message_id,''), COALESCE(superseded_at,''), COALESCE(created_by,''), COALESCE(completion_state,''), COALESCE(stop_reason,''), created_at FROM agent_messages WHERE agent_id = ? ORDER BY created_at ASC, id ASC`, agentID)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	messages := make([]Message, 0)
	for rows.Next() {
		var m Message
		var raw, providerState, turnUsage string
		if err := rows.Scan(&m.ID, &m.AgentID, &m.RunID, &m.Role, &raw, &providerState, &m.ContentText, &m.ReasoningText, &turnUsage, &m.ParentToolID, &m.CommandText, &m.CorrectionOfMessageID, &m.SupersededAt, &m.CreatedBy, &m.CompletionState, &m.StopReason, &m.CreatedAt); err != nil {
			return nil, err
		}
		if raw != "" {
			m.ContentJSON = json.RawMessage(raw)
		}
		if providerState != "" {
			m.ProviderStateJSON = json.RawMessage(providerState)
		}
		if turnUsage != "" {
			var usage MessageTurnUsage
			if json.Unmarshal([]byte(turnUsage), &usage) == nil {
				m.TurnUsage = &usage
			}
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// populateMessageAttachments fills in Attachments for a whole page in one query.
// It used to call ListMessageAttachments per message, which meant a full page
// (DefaultMessagePageLimit, 100) issued 100 queries against a pool of a single
// connection. Attachments is tagged omitempty, so leaving a message without rows
// as nil rather than an empty slice is not observable in the API.
func (s *messageStore) populateMessageAttachments(ctx context.Context, messages []Message, includeData bool) error {
	if len(messages) == 0 {
		return nil
	}
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
	selectData := `X''`
	selectModelData := `X''`
	selectText := `''`
	if includeData {
		selectData = `data_blob`
		selectModelData = `COALESCE(model_data_blob,X'')`
		selectText = `COALESCE(extracted_text,'')`
	}
	// message_id leads the ordering so each message keeps its attachments in
	// creation order, exactly as the per-message query returned them.
	query := `SELECT ` + fmt.Sprintf(attachmentSelectColumns, selectData, selectModelData, selectText) +
		` FROM agent_message_attachments WHERE message_id IN (` + string(placeholders) + `) ORDER BY message_id ASC, created_at ASC`
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		attachment, err := scanMessageAttachment(rows.Scan)
		if err != nil {
			return err
		}
		if owner := byID[attachment.MessageID]; owner != nil {
			owner.Attachments = append(owner.Attachments, attachment)
		}
	}
	return rows.Err()
}

func (s *messageStore) ListMessageAttachments(ctx context.Context, messageID string, includeData bool) ([]Attachment, error) {
	selectData := `X''`
	selectModelData := `X''`
	selectText := `''`
	if includeData {
		selectData = `data_blob`
		selectModelData = `COALESCE(model_data_blob,X'')`
		selectText = `COALESCE(extracted_text,'')`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+fmt.Sprintf(attachmentSelectColumns, selectData, selectModelData, selectText)+` FROM agent_message_attachments WHERE message_id = ? ORDER BY created_at ASC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attachments := make([]Attachment, 0)
	for rows.Next() {
		attachment, err := scanMessageAttachment(rows.Scan)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *messageStore) GetAttachment(ctx context.Context, agentID, messageID, attachmentID string) (Attachment, error) {
	return scanMessageAttachment(s.db.QueryRowContext(ctx, `SELECT `+fmt.Sprintf(attachmentSelectColumns, "data_blob", "COALESCE(model_data_blob,X'')", "COALESCE(extracted_text,'')")+` FROM agent_message_attachments WHERE agent_id = ? AND message_id = ? AND id = ?`, agentID, messageID, attachmentID).Scan)
}

func attachmentMetadata(attachments []Attachment) []Attachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		attachment.Data = nil
		attachment.ModelData = nil
		attachment.ExtractedText = ""
		out = append(out, attachment)
	}
	return out
}
