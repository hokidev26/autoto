package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddMessageRoundTripsTurnUsage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	usage := &MessageTurnUsage{InputTokens: 12, OutputTokens: 40, CachedInputTokens: 3, ReasoningTokens: 2, TTFTMS: 250, DurationMS: 2250, TokensPerSecond: 20}
	message, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "hello", TurnUsage: usage})
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMessagesPage(ctx, agent.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != message.ID || page.Messages[0].TurnUsage == nil || *page.Messages[0].TurnUsage != *usage {
		t.Fatalf("unexpected turn usage round trip: %+v", page.Messages)
	}
}

func TestAddMessageRoundTripsToolContentJSON(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`[{"type":"tool_result","toolUseId":"tool-1","toolName":"Read","output":"ok","isError":true}]`)
	message, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "tool result", ContentJSON: raw, ParentToolID: "tool-1"})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != message.ID || string(messages[0].ContentJSON) != string(raw) || messages[0].ParentToolID != "tool-1" {
		t.Fatalf("unexpected round-trip message: %+v", messages)
	}
}

func TestListMessagesPageUsesStableBackwardCursor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"message-a", "message-b", "message-c", "message-d", "message-e"} {
		if _, err := store.AddMessage(ctx, Message{
			ID:          id,
			AgentID:     agent.ID,
			Role:        "user",
			ContentText: id,
			CreatedAt:   fmt.Sprintf("2026-01-01T00:00:%02dZ", index),
		}); err != nil {
			t.Fatal(err)
		}
	}

	latest, err := store.ListMessagesPage(ctx, agent.ID, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !latest.HasMoreBefore || latest.NextBefore == "" || len(latest.Messages) != 2 || latest.Messages[0].ID != "message-d" || latest.Messages[1].ID != "message-e" {
		t.Fatalf("unexpected latest page: %+v", latest)
	}
	older, err := store.ListMessagesPage(ctx, agent.ID, latest.NextBefore, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !older.HasMoreBefore || older.NextBefore == "" || len(older.Messages) != 2 || older.Messages[0].ID != "message-b" || older.Messages[1].ID != "message-c" {
		t.Fatalf("unexpected older page: %+v", older)
	}
	oldest, err := store.ListMessagesPage(ctx, agent.ID, older.NextBefore, 2)
	if err != nil {
		t.Fatal(err)
	}
	if oldest.HasMoreBefore || oldest.NextBefore != "" || len(oldest.Messages) != 1 || oldest.Messages[0].ID != "message-a" {
		t.Fatalf("unexpected oldest page: %+v", oldest)
	}
	if _, err := store.ListMessagesPage(ctx, agent.ID, "not-a-cursor", 2); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("expected invalid cursor error, got %v", err)
	}
}

func TestMigrationV16AddsInternalProviderStateColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `ALTER TABLE agent_messages DROP COLUMN provider_state_json`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 15`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !testColumnExists(t, ctx, store.DB(), "agent_messages", "provider_state_json") {
		t.Fatal("expected v16 migration to add provider_state_json")
	}
}

func TestMessageProviderStateAndReasoningEffortRemainInternal(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "gemini:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"tool-1":{"thought_signature":"secret-signature"}}`)
	if _, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "tool call", ProviderStateJSON: state}); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || string(messages[0].ProviderStateJSON) != string(state) {
		t.Fatalf("provider state did not round-trip: %+v", messages)
	}
	encoded, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-signature") || strings.Contains(string(encoded), "providerState") {
		t.Fatalf("provider state leaked through public JSON: %s", encoded)
	}
	updated, err := store.UpdateAgentReasoningEffort(ctx, agent.ID, "high")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort did not round-trip: %+v", updated)
	}
}

func TestAttachmentModelImageStatePersistsAndCopiesWithCorrection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "attachments.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Images", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 1))); err != nil {
		t.Fatal(err)
	}
	original := append([]byte("original-metadata:"), encoded.Bytes()...)
	modelData := append([]byte(nil), encoded.Bytes()...)
	message, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "image"}, []Attachment{{
		Filename: "image.png", MIMEType: "image/png", Kind: "image", SizeBytes: int64(len(original)), Data: original,
		ModelData: modelData, ModelMIME: "image/png", Width: 2, Height: 1, SHA256: strings.Repeat("a", 64), ProcessingStatus: "ready",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Attachments) != 1 || len(message.Attachments[0].Data) != 0 || len(message.Attachments[0].ModelData) != 0 || message.Attachments[0].ProcessingStatus != "ready" {
		t.Fatalf("unexpected public attachment metadata: %+v", message.Attachments)
	}
	attachmentID := message.Attachments[0].ID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stored, err := store.GetAttachment(ctx, agent.ID, message.ID, attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.Data, original) || !bytes.Equal(stored.ModelData, modelData) || stored.ModelMIME != "image/png" || stored.Width != 2 || stored.Height != 1 || stored.SHA256 != strings.Repeat("a", 64) || stored.ProcessingStatus != "ready" {
		t.Fatalf("model image state did not survive restart: %+v", stored)
	}
	correction, _, err := store.CreateCorrectionWithRun(ctx, agent.ID, message.ID, "corrected", "", "", []string{attachmentID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(correction.Attachments) != 1 || correction.Attachments[0].ID == attachmentID {
		t.Fatalf("expected an immutable copied attachment: %+v", correction.Attachments)
	}
	copied, err := store.GetAttachment(ctx, agent.ID, correction.ID, correction.Attachments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copied.Data, original) || !bytes.Equal(copied.ModelData, modelData) || copied.ProcessingStatus != "ready" {
		t.Fatalf("correction lost model image state: %+v", copied)
	}
}

func TestAttachmentModelImageStateFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Images", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	invalid := []Attachment{
		{Filename: "missing.png", MIMEType: "image/png", Kind: "image", Data: []byte("raw"), ProcessingStatus: "ready", ModelMIME: "image/png", Width: 1, Height: 1, SHA256: strings.Repeat("a", 64)},
		{Filename: "bad.png", MIMEType: "image/png", Kind: "image", Data: []byte("raw"), ProcessingStatus: "rejected", SHA256: strings.Repeat("b", 64), ProcessingCode: "invalid_image"},
		{Filename: "legacy.png", MIMEType: "image/png", Kind: "image", Data: []byte("raw"), SHA256: strings.Repeat("c", 64)},
	}
	for _, attachment := range invalid {
		if _, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: "user"}, []Attachment{attachment}); err == nil {
			t.Fatalf("expected invalid model state to fail closed: %+v", attachment)
		}
	}
}

func TestMigrationV49AddsAttachmentModelImageColumnsWithoutRewritingLegacyData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v48.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE agent_message_attachments;
CREATE TABLE agent_message_attachments (
  id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  filename TEXT NOT NULL,
  mime_type TEXT,
  kind TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  data_blob BLOB NOT NULL,
  extracted_text TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_message_attachments_message ON agent_message_attachments(message_id, created_at);
CREATE INDEX idx_message_attachments_agent ON agent_message_attachments(agent_id, created_at);
INSERT INTO agent_message_attachments (id, message_id, agent_id, filename, mime_type, kind, size_bytes, data_blob, extracted_text, created_at)
VALUES ('legacy-attachment', 'legacy-message', 'legacy-agent', 'legacy.txt', 'text/plain', 'text', 6, X'6C6567616379', 'legacy', '2026-01-01T00:00:00Z');
PRAGMA user_version = 48;`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, column := range []string{"model_data_blob", "model_mime_type", "image_width", "image_height", "sha256", "processing_status", "processing_code", "processing_error"} {
		if !testColumnExists(t, ctx, store.DB(), "agent_message_attachments", column) {
			t.Fatalf("expected v49 migration to add %s", column)
		}
	}
	attachment, err := store.GetAttachment(ctx, "legacy-agent", "legacy-message", "legacy-attachment")
	if err != nil {
		t.Fatal(err)
	}
	if string(attachment.Data) != "legacy" || attachment.ExtractedText != "legacy" || attachment.ProcessingStatus != "" || len(attachment.ModelData) != 0 {
		t.Fatalf("legacy attachment was rewritten unexpectedly: %+v", attachment)
	}
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected version %d, got %d", CurrentDBVersion, version)
	}
}

// Attachments are fetched for a whole page in one query, so the grouping happens
// in Go. This pins the part that a batch query can silently get wrong: every
// attachment landing on the message that actually owns it, in creation order,
// with untouched messages left empty.
func TestMessagePageGroupsAttachmentsOntoTheirOwnMessage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "one"}, []Attachment{
		{Filename: "a1.txt", MIMEType: "text/plain", Kind: "file", SizeBytes: 3, Data: []byte("aaa")},
		{Filename: "a2.txt", MIMEType: "text/plain", Kind: "file", SizeBytes: 3, Data: []byte("bbb")},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A message with no attachments between two that have them: the batch must not
	// spill rows onto it.
	bare, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "two"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "three"}, []Attachment{
		{Filename: "c1.txt", MIMEType: "text/plain", Kind: "file", SizeBytes: 3, Data: []byte("ccc")},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.ListMessagesPage(ctx, agent.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string][]Attachment, len(page.Messages))
	for _, message := range page.Messages {
		byID[message.ID] = message.Attachments
	}
	if got := byID[first.ID]; len(got) != 2 || got[0].Filename != "a1.txt" || got[1].Filename != "a2.txt" {
		t.Fatalf("first message lost its attachments or their order: %+v", got)
	}
	if got := byID[bare.ID]; len(got) != 0 {
		t.Fatalf("a message with no attachments must stay empty, got %+v", got)
	}
	if got := byID[third.ID]; len(got) != 1 || got[0].Filename != "c1.txt" {
		t.Fatalf("third message lost its attachment: %+v", got)
	}
	for _, attachment := range byID[first.ID] {
		if attachment.MessageID != first.ID {
			t.Fatalf("attachment filed under the wrong message: %+v", attachment)
		}
		// The page is metadata-only, so blobs must not be loaded.
		if len(attachment.Data) != 0 {
			t.Fatalf("message page must not carry attachment bytes: %+v", attachment)
		}
	}

	// The live snapshot batches the same table through its own query.
	snapshot, err := store.ReadAgentLiveSnapshot(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotByID := make(map[string][]Attachment, len(snapshot.Messages))
	for _, message := range snapshot.Messages {
		snapshotByID[message.ID] = message.Attachments
	}
	if got := snapshotByID[first.ID]; len(got) != 2 || got[0].Filename != "a1.txt" || got[1].Filename != "a2.txt" {
		t.Fatalf("snapshot lost the first message's attachments: %+v", got)
	}
	if got := snapshotByID[bare.ID]; len(got) != 0 {
		t.Fatalf("snapshot spilled attachments onto a bare message: %+v", got)
	}
	if got := snapshotByID[third.ID]; len(got) != 1 || got[0].Filename != "c1.txt" {
		t.Fatalf("snapshot lost the third message's attachment: %+v", got)
	}
}
