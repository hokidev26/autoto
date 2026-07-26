package db

import (
	"context"
	"path/filepath"
	"testing"
)

// A correction that leaves the following turns in place has the model answer a
// question the user already withdrew, which is the whole reason the edit felt
// ignored. The rows stay so the transcript is still readable; only the model's
// view is truncated.
func TestCorrectionSupersedesTheConversationAfterIt(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "supersede.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Supersede", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	add := func(role, text string) Message {
		t.Helper()
		message, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: role, ContentText: text}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return message
	}
	add("user", "first question")
	add("assistant", "first answer")
	target := add("user", "second question")
	add("assistant", "second answer")
	add("user", "third question")
	add("assistant", "third answer")

	correction, _, err := store.CreateCorrectionWithRun(ctx, agent.ID, target.ID, "second question, corrected", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is deleted: every message is still there to read.
	if len(messages) != 7 {
		t.Fatalf("expected 7 messages retained, got %d", len(messages))
	}

	state := map[string]bool{}
	for _, message := range messages {
		state[message.ContentText] = message.SupersededAt != ""
	}
	for text, wantSuperseded := range map[string]bool{
		"first question":             false,
		"first answer":               false,
		"second question":            true, // the corrected message itself
		"second answer":              true,
		"third question":             true,
		"third answer":               true,
		"second question, corrected": false, // the replacement must survive
	} {
		if state[text] != wantSuperseded {
			t.Errorf("%q superseded = %v, want %v", text, state[text], wantSuperseded)
		}
	}
	if state[correction.ContentText] {
		t.Fatal("the correction superseded itself, so the re-run would have no prompt")
	}
}

// Correcting twice must not rewrite when the first batch was retired, and must
// not resurrect anything.
func TestRepeatedCorrectionsKeepEarlierSupersededTimestamps(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "supersede-twice.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Supersede", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	add := func(role, text string) Message {
		t.Helper()
		message, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: role, ContentText: text}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return message
	}
	first := add("user", "one")
	add("assistant", "one reply")
	second := add("user", "two")
	add("assistant", "two reply")

	if _, _, err := store.CreateCorrectionWithRun(ctx, agent.ID, second.ID, "two fixed", "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstBatch := map[string]string{}
	for _, message := range messages {
		if message.SupersededAt != "" {
			firstBatch[message.ID] = message.SupersededAt
		}
	}
	if len(firstBatch) != 2 {
		t.Fatalf("expected 2 superseded after the first correction, got %d", len(firstBatch))
	}

	if _, _, err := store.CreateCorrectionWithRun(ctx, agent.ID, first.ID, "one fixed", "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	messages, err = store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, message := range messages {
		if message.SupersededAt == "" {
			live++
			continue
		}
		if original, ok := firstBatch[message.ID]; ok && original != message.SupersededAt {
			t.Errorf("message %s had its supersede timestamp rewritten: %s -> %s", message.ID, original, message.SupersededAt)
		}
	}
	// Only the newest correction survives the second pass.
	if live != 1 {
		t.Fatalf("expected exactly the newest correction live, got %d", live)
	}
}
