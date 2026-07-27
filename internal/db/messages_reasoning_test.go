package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMessageReasoningRoundTrips proves the v56 column is written and read back
// on the same paths the transcript uses, and that a turn without reasoning
// stays empty rather than picking up a neighbouring turn's text.
func TestMessageReasoningRoundTrips(t *testing.T) {
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

	if _, err := store.AddMessage(ctx, Message{
		AgentID:       agent.ID,
		Role:          "assistant",
		ContentText:   "I widened the gap.",
		ReasoningText: "Measuring the composer first.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "Done."}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListMessagesPage(ctx, agent.ID, "", DefaultMessagePageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(page.Messages))
	}
	if got := page.Messages[0].ReasoningText; got != "Measuring the composer first." {
		t.Fatalf("reasoning did not round-trip: %q", got)
	}
	if got := page.Messages[1].ReasoningText; got != "" {
		t.Fatalf("a turn without reasoning must stay empty, got %q", got)
	}
	if got := page.Messages[0].ContentText; got != "I widened the gap." {
		t.Fatalf("reasoning must not disturb the answer text: %q", got)
	}
}
