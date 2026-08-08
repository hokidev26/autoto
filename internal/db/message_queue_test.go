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
}
