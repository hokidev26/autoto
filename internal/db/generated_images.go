package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var generatedImageStorageKeyRE = regexp.MustCompile(`^objects/([0-9a-f]{2})/([0-9a-f]{64})\.png$`)

func (s *Store) InsertGeneratedImage(ctx context.Context, image GeneratedImage) (GeneratedImage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GeneratedImage{}, err
	}
	defer tx.Rollback()
	stored, err := insertGeneratedImageTx(ctx, tx, image)
	if err != nil {
		return GeneratedImage{}, err
	}
	if err := tx.Commit(); err != nil {
		return GeneratedImage{}, err
	}
	return stored, nil
}

func (s *Store) GetGeneratedImage(ctx context.Context, agentID, messageID, assetID string) (GeneratedImage, error) {
	var image GeneratedImage
	err := scanGeneratedImage(s.db.QueryRowContext(ctx, generatedImageSelect+` WHERE agent_id = ? AND message_id = ? AND id = ?`, agentID, messageID, assetID), &image)
	return image, err
}

func (s *Store) ListGeneratedImagesByMessage(ctx context.Context, agentID, messageID string) ([]GeneratedImage, error) {
	rows, err := s.db.QueryContext(ctx, generatedImageSelect+` WHERE agent_id = ? AND message_id = ? ORDER BY output_index ASC, created_at ASC, id ASC`, agentID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	images := make([]GeneratedImage, 0)
	for rows.Next() {
		var image GeneratedImage
		if err := scanGeneratedImage(rows, &image); err != nil {
			return nil, err
		}
		images = append(images, image)
	}
	return images, rows.Err()
}

func (s *Store) ListReferencedGeneratedImageStorageKeys(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT storage_key FROM agent_message_generated_images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make(map[string]struct{})
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = struct{}{}
	}
	return keys, rows.Err()
}

func (s *Store) SetGeneratedImageStatus(ctx context.Context, agentID, messageID, assetID, status string) error {
	if status != "ready" && status != "unavailable" {
		return errors.New("invalid generated image status")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agent_message_generated_images SET status = ? WHERE agent_id = ? AND message_id = ? AND id = ?`, status, agentID, messageID, assetID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteGeneratedImage(ctx context.Context, agentID, messageID, assetID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_message_generated_images WHERE agent_id = ? AND message_id = ? AND id = ?`, agentID, messageID, assetID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// AddMessageWithGeneratedImages atomically stores an assistant message and its
// lightweight disk-asset metadata. The caller must publish all files first.
func (s *Store) AddMessageWithGeneratedImages(ctx context.Context, msg Message, images []GeneratedImage) (Message, error) {
	if msg.Role != "assistant" {
		return Message{}, errors.New("generated images require an assistant message")
	}
	msg, turnUsageJSON, createdBy, err := prepareGeneratedImageMessage(msg)
	if err != nil {
		return Message{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_messages (id, agent_id, run_id, parent_tool_use_id, role, content_json, provider_state_json, content_text, turn_usage_json, command_text, correction_of_message_id, created_by, completion_state, stop_reason, created_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`, msg.ID, msg.AgentID, msg.RunID, nullEmpty(msg.ParentToolID), msg.Role, string(msg.ContentJSON), string(msg.ProviderStateJSON), msg.ContentText, turnUsageJSON, nullEmpty(msg.CommandText), msg.CorrectionOfMessageID, createdBy, msg.CompletionState, msg.StopReason, msg.CreatedAt); err != nil {
		return Message{}, err
	}
	stored := make([]GeneratedImage, 0, len(images))
	for _, image := range images {
		image.AgentID = msg.AgentID
		image.MessageID = msg.ID
		if image.RunID == "" {
			image.RunID = msg.RunID
		}
		if image.CreatedAt == "" {
			image.CreatedAt = msg.CreatedAt
		}
		image, err = insertGeneratedImageTx(ctx, tx, image)
		if err != nil {
			return Message{}, err
		}
		stored = append(stored, image)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET message_count = message_count + 1, last_message_at = ?, updated_at = ? WHERE id = ?`, msg.CreatedAt, msg.CreatedAt, msg.AgentID); err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	msg.GeneratedImages = stored
	return msg, nil
}

func (s *Store) populateMessageGeneratedImages(ctx context.Context, messages []Message) error {
	for i := range messages {
		images, err := s.ListGeneratedImagesByMessage(ctx, messages[i].AgentID, messages[i].ID)
		if err != nil {
			return err
		}
		messages[i].GeneratedImages = images
	}
	return nil
}

const generatedImageSelect = `SELECT id, agent_id, message_id, COALESCE(run_id,''), generation_id, storage_key, sha256, mime_type, filename, byte_size, width, height, COALESCE(revised_prompt,''), output_index, status, created_at FROM agent_message_generated_images`

type generatedImageScanner interface {
	Scan(...any) error
}

func scanGeneratedImage(scanner generatedImageScanner, image *GeneratedImage) error {
	return scanner.Scan(&image.ID, &image.AgentID, &image.MessageID, &image.RunID, &image.GenerationID, &image.StorageKey, &image.SHA256, &image.MIMEType, &image.Filename, &image.ByteSize, &image.Width, &image.Height, &image.RevisedPrompt, &image.OutputIndex, &image.Status, &image.CreatedAt)
}

func insertGeneratedImageTx(ctx context.Context, tx *sql.Tx, image GeneratedImage) (GeneratedImage, error) {
	if image.ID == "" {
		image.ID = NewID()
	}
	if image.CreatedAt == "" {
		image.CreatedAt = Now()
	}
	if image.MIMEType == "" {
		image.MIMEType = "image/png"
	}
	if image.Status == "" {
		image.Status = "ready"
	}
	if err := validateGeneratedImage(image); err != nil {
		return GeneratedImage{}, err
	}
	var messageRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM agent_messages WHERE id = ? AND agent_id = ?`, image.MessageID, image.AgentID).Scan(&messageRole); err != nil {
		return GeneratedImage{}, err
	}
	if messageRole != "assistant" {
		return GeneratedImage{}, fmt.Errorf("%w: generated images require an assistant message", ErrConflict)
	}
	if image.RunID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ? AND agent_id = ?`, image.RunID, image.AgentID).Scan(&count); err != nil {
			return GeneratedImage{}, err
		}
		if count != 1 {
			return GeneratedImage{}, fmt.Errorf("%w: generated image run does not belong to agent", ErrConflict)
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_message_generated_images (id, agent_id, message_id, run_id, generation_id, storage_key, sha256, mime_type, filename, byte_size, width, height, revised_prompt, output_index, status, created_at) VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)`, image.ID, image.AgentID, image.MessageID, image.RunID, image.GenerationID, image.StorageKey, image.SHA256, image.MIMEType, image.Filename, image.ByteSize, image.Width, image.Height, image.RevisedPrompt, image.OutputIndex, image.Status, image.CreatedAt)
	if err != nil {
		return GeneratedImage{}, err
	}
	return image, nil
}

func validateGeneratedImage(image GeneratedImage) error {
	matches := generatedImageStorageKeyRE.FindStringSubmatch(image.StorageKey)
	validKey := len(matches) == 3 && matches[1] == matches[2][:2] && matches[2] == image.SHA256
	if image.ID == "" || image.AgentID == "" || image.MessageID == "" || image.GenerationID == "" || !validKey ||
		image.MIMEType != "image/png" || image.Filename == "" || !utf8.ValidString(image.Filename) || len([]byte(image.Filename)) > 255 ||
		image.ByteSize <= 0 || image.ByteSize > 10<<20 || image.Width <= 0 || image.Width > 8192 || image.Height <= 0 || image.Height > 8192 ||
		int64(image.Width)*int64(image.Height) > 32_000_000 || image.OutputIndex < 0 ||
		(image.Status != "ready" && image.Status != "unavailable") || len([]byte(image.GenerationID)) > 256 || len([]byte(image.RevisedPrompt)) > 131072 {
		return errors.New("invalid generated image metadata")
	}
	return nil
}

func prepareGeneratedImageMessage(msg Message) (Message, string, string, error) {
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
			return Message{}, "", "", err
		}
		turnUsageJSON = string(encoded)
	}
	createdBy := strings.TrimSpace(msg.CreatedBy)
	if createdBy == "api" {
		createdBy = ""
	}
	return msg, turnUsageJSON, createdBy, nil
}
