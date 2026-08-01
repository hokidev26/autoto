package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Retrying a failed run used to go through CreateCorrectionWithRun, which
// writes a new user row and retires the old one. Every retry therefore added a
// visible copy of the same prompt to the transcript, labelled as a correction.
func TestCreateRerunForMessageLeavesTheConversationAlone(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "rerun.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	asked, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "50嵐的~"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "half an answer before the provider failed"}); err != nil {
		t.Fatal(err)
	}

	before, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	run, err := store.CreateRerunForMessage(ctx, agent.ID, asked.ID)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if run.TriggerMessageID != asked.ID || run.Status != "pending" {
		t.Fatalf("the run must be bound to the original message and still pending: %+v", run)
	}

	after, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a rerun must not add a message: %d -> %d", len(before), len(after))
	}

	var reran Message
	for _, message := range after {
		if message.ID == asked.ID {
			reran = message
		}
		if message.Role == "assistant" && message.SupersededAt == "" {
			t.Fatalf("the failed attempt's output must be retired, got %+v", message)
		}
	}
	if reran.ID == "" {
		t.Fatal("the original message disappeared")
	}
	if reran.SupersededAt != "" {
		t.Fatalf("the message being rerun must stay current, got supersededAt=%q", reran.SupersededAt)
	}
	if reran.CorrectionOfMessageID != "" {
		t.Fatalf("a rerun is not a correction, got correctionOf=%q", reran.CorrectionOfMessageID)
	}
	if reran.ContentText != "50嵐的~" {
		t.Fatalf("the prompt must be untouched, got %q", reran.ContentText)
	}
	if reran.RunID != run.ID {
		t.Fatalf("the message should point at the run now working on it, got %q want %q", reran.RunID, run.ID)
	}

	// Repeating it is the case that used to stack copies.
	if _, err := store.CreateRerunForMessage(ctx, agent.ID, asked.ID); err != nil {
		t.Fatalf("second rerun: %v", err)
	}
	again, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(before) {
		t.Fatalf("repeated reruns must not grow the transcript: %d -> %d", len(before), len(again))
	}
}

func TestCreateRerunForMessageRejectsWhatItCannotRerun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "rerun-guards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	assistant, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "not a prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRerunForMessage(ctx, agent.ID, assistant.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("an assistant message is not rerunnable, got %v", err)
	}

	first, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateCorrectionWithRun(ctx, agent.ID, first.ID, "edited", "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	// The correction retired it, so it is no longer part of the conversation.
	if _, err := store.CreateRerunForMessage(ctx, agent.ID, first.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a superseded message is not rerunnable, got %v", err)
	}
}
