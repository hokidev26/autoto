package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newMessageOpsFixture(t *testing.T, name string) (*Store, Agent, func(role, text string) Message) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, _, agent, err := store.CreateProject(ctx, name, "", t.TempDir(), "openai:test", "acceptEdits")
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
	return store, agent, add
}

func TestRollbackConversationToMessageSupersedesEverythingAfterIt(t *testing.T) {
	ctx := context.Background()
	store, agent, add := newMessageOpsFixture(t, "rollback")
	add("user", "one")
	add("assistant", "one reply")
	target := add("user", "two")
	add("assistant", "two reply")
	add("user", "three")

	superseded, err := store.RollbackConversationToMessage(ctx, agent.ID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if superseded != 2 {
		t.Fatalf("expected 2 superseded messages, got %d", superseded)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"one": false, "one reply": false, "two": false, "two reply": true, "three": true}
	for _, message := range messages {
		if got := message.SupersededAt != ""; got != want[message.ContentText] {
			t.Errorf("%q superseded = %v, want %v", message.ContentText, got, want[message.ContentText])
		}
	}

	// Rolling back to a retired message is refused.
	retired := messages[len(messages)-1]
	if _, err := store.RollbackConversationToMessage(ctx, agent.ID, retired.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict for a superseded target, got %v", err)
	}
}

func TestDeleteConversationMessageRemovesToolExchange(t *testing.T) {
	ctx := context.Background()
	store, agent, add := newMessageOpsFixture(t, "delete")
	add("user", "question")
	assistant := add("assistant", "calling a tool")
	if _, err := store.AddToolCall(ctx, ToolCall{AgentID: agent.ID, MessageID: assistant.ID, ToolUseID: "tool-1", ToolName: "shell", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	result, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: "user", ParentToolID: "tool-1", ContentText: "tool output"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	add("assistant", "final answer")

	deleted, err := store.DeleteConversationMessage(ctx, agent.ID, assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected the assistant message and its tool result deleted, got %v", deleted)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == assistant.ID || message.ID == result.ID {
			t.Fatalf("message %s should have been deleted", message.ID)
		}
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 surviving messages, got %d", len(messages))
	}
	if calls, err := store.ListPendingToolCalls(ctx, agent.ID); err != nil || len(calls) != 0 {
		t.Fatalf("tool calls should be gone: %v %v", calls, err)
	}
	updated, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MessageCount != 2 {
		t.Fatalf("expected message_count 2 after delete, got %d", updated.MessageCount)
	}
}

func TestDeleteConversationMessageDetachesCorrections(t *testing.T) {
	ctx := context.Background()
	store, agent, add := newMessageOpsFixture(t, "delete-correction")
	source := add("user", "original")
	if _, _, err := store.CreateCorrectionWithRun(ctx, agent.ID, source.ID, "corrected", "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	// The source is referenced by the correction via ON DELETE RESTRICT; the
	// delete must detach it instead of failing.
	if _, err := store.DeleteConversationMessage(ctx, agent.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ContentText != "corrected" || messages[0].CorrectionOfMessageID != "" {
		t.Fatalf("expected a single detached correction, got %+v", messages)
	}
}

func TestForkConversationFromMessageCopiesTranscriptPrefix(t *testing.T) {
	ctx := context.Background()
	store, agent, add := newMessageOpsFixture(t, "fork")
	first := add("user", "one")
	if _, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "one reply"}, []Attachment{}); err != nil {
		t.Fatal(err)
	}
	target, err := store.AddMessageWithAttachments(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "two"}, []Attachment{{Filename: "note.txt", Kind: "text", MIMEType: "text/plain", SizeBytes: 4, Data: []byte("data")}})
	if err != nil {
		t.Fatal(err)
	}
	add("assistant", "two reply")
	add("user", "three")

	fork, err := store.ForkConversationFromMessage(ctx, agent.ID, target.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if fork.ParentAgentID != agent.ID || fork.ForkMessageID != target.ID {
		t.Fatalf("fork lineage missing: %+v", fork)
	}
	if fork.WorklineID != agent.WorklineID {
		t.Fatalf("fork should stay in the same workline: %q != %q", fork.WorklineID, agent.WorklineID)
	}
	if fork.MessageCount != 3 {
		t.Fatalf("expected 3 copied messages, got %d", fork.MessageCount)
	}
	copied, err := store.ListMessages(ctx, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 3 {
		t.Fatalf("expected 3 copied rows, got %d", len(copied))
	}
	texts := []string{copied[0].ContentText, copied[1].ContentText, copied[2].ContentText}
	if texts[0] != "one" || texts[1] != "one reply" || texts[2] != "two" {
		t.Fatalf("copied transcript out of order or wrong: %v", texts)
	}
	for _, message := range copied {
		if message.ID == first.ID || message.ID == target.ID {
			t.Fatal("copied messages must receive new ids")
		}
		if message.AgentID != fork.ID {
			t.Fatalf("copied message belongs to %s, want %s", message.AgentID, fork.ID)
		}
	}
	if len(copied[2].Attachments) != 1 || copied[2].Attachments[0].Filename != "note.txt" {
		t.Fatalf("attachment was not copied: %+v", copied[2].Attachments)
	}
	// The source stays untouched.
	original, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(original) != 5 {
		t.Fatalf("source transcript changed: %d messages", len(original))
	}
}

func TestForkConversationFromMessageSkipsSupersededRows(t *testing.T) {
	ctx := context.Background()
	store, agent, add := newMessageOpsFixture(t, "fork-superseded")
	add("user", "one")
	corrected := add("user", "two")
	add("assistant", "two reply")
	correction, _, err := store.CreateCorrectionWithRun(ctx, agent.ID, corrected.ID, "two fixed", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fork, err := store.ForkConversationFromMessage(ctx, agent.ID, correction.ID, "branch")
	if err != nil {
		t.Fatal(err)
	}
	copied, err := store.ListMessages(ctx, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 2 {
		t.Fatalf("expected the live prefix only, got %d rows", len(copied))
	}
	if copied[0].ContentText != "one" || copied[1].ContentText != "two fixed" {
		t.Fatalf("unexpected fork transcript: %q %q", copied[0].ContentText, copied[1].ContentText)
	}
	if copied[1].CorrectionOfMessageID != "" {
		t.Fatal("correction reference must be dropped when its source is not copied")
	}
	if fork.Title != "branch" {
		t.Fatalf("explicit title ignored: %q", fork.Title)
	}
	// Forking from a superseded message is refused.
	if _, err := store.ForkConversationFromMessage(ctx, agent.ID, corrected.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict for a superseded fork point, got %v", err)
	}
}
