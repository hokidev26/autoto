package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func newQueueTestStore(t *testing.T) (*Store, Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	agent, err := store.CreateAgent(ctx, Agent{Title: "queue-owner", Type: "primary", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	return store, agent
}

// The queue is the user's typed backlog, so order has to be the order they
// entered and editing must not move a message.
func TestMessageQueueKeepsOrderAcrossEnqueueAndEdit(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)

	for _, text := range []string{"first", "second", "third"} {
		if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	queue, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 3 || queue[0].Text != "first" || queue[2].Text != "third" {
		t.Fatalf("queue order = %+v", queue)
	}

	// Editing the head keeps it at the head.
	if _, err := store.UpdateQueuedMessageText(ctx, agent.ID, queue[0].ID, "first edited"); err != nil {
		t.Fatal(err)
	}
	queue, err = store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queue[0].Text != "first edited" {
		t.Fatalf("edited head = %q, want %q", queue[0].Text, "first edited")
	}
	if len(queue) != 3 {
		t.Fatalf("editing changed the queue length: %+v", queue)
	}
}

// Claiming deletes in the same transaction, which is what stops two concurrent
// drains from sending one message twice.
func TestClaimNextQueuedMessageHandsEachMessageToOneCaller(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)
	const total = 12
	for i := 0; i < total; i++ {
		if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				item, ok, err := store.ClaimNextQueuedMessage(ctx, agent.ID)
				if err != nil || !ok {
					return
				}
				mu.Lock()
				seen[item.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != total {
		t.Fatalf("claimed %d distinct messages, want %d", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("message %s was claimed %d times, want 1", id, count)
		}
	}
	remaining, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("queue still holds %d messages", len(remaining))
	}
}

// A failed send must not cost the user their text.
func TestRestoreQueuedMessageReturnsItToItsOriginalPlace(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)
	for _, text := range []string{"head", "tail"} {
		if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	claimed, ok, err := store.ClaimNextQueuedMessage(ctx, agent.ID)
	if err != nil || !ok {
		t.Fatalf("claim failed: %v ok=%v", err, ok)
	}
	if err := store.RestoreQueuedMessage(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	queue, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 || queue[0].Text != "head" {
		t.Fatalf("restored queue = %+v, want head first", queue)
	}
}

func TestEnqueueMessageRejectsBlankTextAndEnforcesTheLimit(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)

	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "   "}); !errors.Is(err, ErrQueuedMessageInvalid) {
		t.Fatalf("blank text error = %v, want ErrQueuedMessageInvalid", err)
	}
	for i := 0; i < QueuedMessageLimitPerAgent; i++ {
		if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "filler"}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "one too many"}); !errors.Is(err, ErrQueuedMessageLimit) {
		t.Fatalf("over-limit error = %v, want ErrQueuedMessageLimit", err)
	}
}

// Fresh installs come from schema.go while upgrades run the migration, so both
// paths have to produce the table.
func TestAgentMessageQueueTableExistsOnFreshInstall(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !testTableExists(t, ctx, store.DB(), "agent_message_queue") {
		t.Fatal("fresh database is missing agent_message_queue")
	}
	if readUserVersion(t, ctx, store.DB()) != CurrentDBVersion {
		t.Fatalf("fresh database version = %d, want %d", readUserVersion(t, ctx, store.DB()), CurrentDBVersion)
	}
	if !testTableExists(t, ctx, store.DB(), "agent_queued_message_attachments") {
		t.Fatal("fresh database is missing agent_queued_message_attachments")
	}
}

func queuedTestAttachments() []Attachment {
	return []Attachment{
		{Filename: "notes.txt", MIMEType: "text/plain", Kind: "document", SizeBytes: 5, Data: []byte("first"), ExtractedText: "first"},
		{Filename: "second.txt", MIMEType: "text/plain", Kind: "document", SizeBytes: 6, Data: []byte("second")},
	}
}

// Parked files have to reach the database, because the browser that uploaded
// them is allowed to close before the queue drains.
func TestEnqueueMessageRoundTripsAttachments(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)

	enqueued, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "look at these", Attachments: queuedTestAttachments()})
	if err != nil {
		t.Fatal(err)
	}
	if len(enqueued.Attachments) != 2 || enqueued.Attachments[0].ID == "" || enqueued.Attachments[0].MessageID != enqueued.ID {
		t.Fatalf("enqueued attachments = %+v", enqueued.Attachments)
	}
	if enqueued.Attachments[0].AgentID != agent.ID || enqueued.Attachments[0].CreatedAt == "" {
		t.Fatalf("enqueued attachment identity = %+v", enqueued.Attachments[0])
	}

	queue, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || len(queue[0].Attachments) != 2 {
		t.Fatalf("listed queue = %+v", queue)
	}
	// Slice order is send order, so the list has to come back the way it went in.
	if queue[0].Attachments[0].Filename != "notes.txt" || queue[0].Attachments[1].Filename != "second.txt" {
		t.Fatalf("attachment order = %+v", queue[0].Attachments)
	}
	// Listing is for display: the blobs stay in the database.
	if len(queue[0].Attachments[0].Data) != 0 || queue[0].Attachments[0].ExtractedText != "" {
		t.Fatalf("list leaked attachment payload: %+v", queue[0].Attachments[0])
	}
	if queue[0].Attachments[0].SizeBytes != 5 || queue[0].Attachments[0].MIMEType != "text/plain" {
		t.Fatalf("attachment metadata = %+v", queue[0].Attachments[0])
	}
}

// The claim deletes the queue row, and the cascade takes the attachments with
// it, so a claim that reads them afterwards returns an empty list and the
// message goes out without its files.
func TestClaimNextQueuedMessageReturnsAttachmentsWithData(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)
	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "with files", Attachments: queuedTestAttachments()}); err != nil {
		t.Fatal(err)
	}

	claimed, ok, err := store.ClaimNextQueuedMessage(ctx, agent.ID)
	if err != nil || !ok {
		t.Fatalf("claim failed: %v ok=%v", err, ok)
	}
	if len(claimed.Attachments) != 2 {
		t.Fatalf("claimed attachments = %+v", claimed.Attachments)
	}
	if string(claimed.Attachments[0].Data) != "first" || string(claimed.Attachments[1].Data) != "second" {
		t.Fatalf("claim did not return attachment bytes: %+v", claimed.Attachments)
	}
	if claimed.Attachments[0].ExtractedText != "first" {
		t.Fatalf("claim dropped extracted text: %+v", claimed.Attachments[0])
	}
	var remaining int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_queued_message_attachments WHERE queued_message_id = ?`, claimed.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("claim left %d attachment rows behind", remaining)
	}
}

// A failed send must not cost the user their files either.
func TestRestoreQueuedMessageReinsertsAttachments(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)
	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "with files", Attachments: queuedTestAttachments()}); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNextQueuedMessage(ctx, agent.ID)
	if err != nil || !ok {
		t.Fatalf("claim failed: %v ok=%v", err, ok)
	}
	if err := store.RestoreQueuedMessage(ctx, claimed); err != nil {
		t.Fatal(err)
	}

	restored, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || len(restored[0].Attachments) != 2 {
		t.Fatalf("restored queue = %+v", restored)
	}
	if restored[0].Attachments[0].Filename != "notes.txt" || restored[0].Attachments[1].Filename != "second.txt" {
		t.Fatalf("restored attachment order = %+v", restored[0].Attachments)
	}
	// Restoring twice happens when a send fails repeatedly, and must not collide
	// on the attachment primary keys.
	if err := store.RestoreQueuedMessage(ctx, claimed); err != nil {
		t.Fatalf("second restore: %v", err)
	}
	again, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || len(again[0].Attachments) != 2 {
		t.Fatalf("queue after second restore = %+v", again)
	}
	claimedAgain, ok, err := store.ClaimNextQueuedMessage(ctx, agent.ID)
	if err != nil || !ok {
		t.Fatalf("reclaim failed: %v ok=%v", err, ok)
	}
	if string(claimedAgain.Attachments[0].Data) != "first" {
		t.Fatalf("restored attachment lost its bytes: %+v", claimedAgain.Attachments[0])
	}
}

// An image with no caption is accepted on the live send path, so refusing it
// only when parked would make the queue stricter than sending directly.
func TestEnqueueMessageAcceptsAttachmentOnlyAndStillRejectsEmpty(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)

	parked, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "", Attachments: queuedTestAttachments()[:1]})
	if err != nil {
		t.Fatalf("attachment-only enqueue: %v", err)
	}
	if parked.Text != "" || len(parked.Attachments) != 1 {
		t.Fatalf("attachment-only parked message = %+v", parked)
	}
	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "   "}); !errors.Is(err, ErrQueuedMessageInvalid) {
		t.Fatalf("no text and no files error = %v, want ErrQueuedMessageInvalid", err)
	}
	if err := store.RestoreQueuedMessage(ctx, QueuedMessage{ID: NewID(), AgentID: agent.ID}); !errors.Is(err, ErrQueuedMessageInvalid) {
		t.Fatalf("empty restore error = %v, want ErrQueuedMessageInvalid", err)
	}
}

// Parked blobs sit in the database until the queue drains, so the count and the
// total size are both capped.
func TestEnqueueMessageEnforcesAttachmentCaps(t *testing.T) {
	ctx := context.Background()
	store, agent := newQueueTestStore(t)

	tooMany := make([]Attachment, 0, queuedMessageAttachmentLimit+1)
	for i := 0; i <= queuedMessageAttachmentLimit; i++ {
		tooMany = append(tooMany, Attachment{Filename: "f.txt", MIMEType: "text/plain", Kind: "document", SizeBytes: 1, Data: []byte("x")})
	}
	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "many", Attachments: tooMany}); !errors.Is(err, ErrQueuedMessageInvalid) {
		t.Fatalf("over-count error = %v, want ErrQueuedMessageInvalid", err)
	}
	oversized := []Attachment{
		{Filename: "big.bin", MIMEType: "application/octet-stream", Kind: "document", SizeBytes: queuedMessageAttachmentMaxBytes + 1, Data: make([]byte, queuedMessageAttachmentMaxBytes+1)},
	}
	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "big", Attachments: oversized}); !errors.Is(err, ErrQueuedMessageInvalid) {
		t.Fatalf("over-size error = %v, want ErrQueuedMessageInvalid", err)
	}
	// A row the destination table would reject has to fail at park time: the
	// drain retries the head of the queue forever, so it would never clear.
	poison := []Attachment{
		{Filename: "bad.png", MIMEType: "image/png", Kind: "image", SizeBytes: 3, Data: []byte("raw"), ProcessingStatus: "ready"},
	}
	if _, err := store.EnqueueMessage(ctx, QueuedMessage{AgentID: agent.ID, Text: "poison", Attachments: poison}); !errors.Is(err, ErrQueuedMessageInvalid) {
		t.Fatalf("inconsistent model state error = %v, want ErrQueuedMessageInvalid", err)
	}
	queue, err := store.ListQueuedMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 0 {
		t.Fatalf("rejected messages were parked anyway: %+v", queue)
	}
}
