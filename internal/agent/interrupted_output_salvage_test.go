package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

// Stopping a run one moment too late used to cost the answer. The model stream
// had closed cleanly, so the text existed, but the segment noticed the
// cancellation immediately afterwards and returned without storing anything. The
// live card is cleared when the run reaches a terminal state, so the reader's
// transcript came back missing the stretch of work they had just watched arrive.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestInterruptedAssistantOutputSurvivesTheStop(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})

	result := modelTurnResult{Text: "the work the reader watched arrive", StopReason: "end_turn"}
	// The write has to succeed against an already-cancelled run context, which is
	// the whole point: the store would refuse the cancelled one.
	messageID := runner.salvageInterruptedAssistantOutput(cancelledContext(), agent.ID, "", result)
	if messageID == "" {
		t.Fatal("expected the salvaged output to be stored")
	}

	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored *db.Message
	for i := range messages {
		if messages[i].ID == messageID {
			stored = &messages[i]
			break
		}
	}
	if stored == nil {
		t.Fatalf("stored message %s is not in the transcript", messageID)
	}
	if !strings.Contains(stored.ContentText, "the work the reader watched arrive") {
		t.Fatalf("the salvaged text is not what was stored: %q", stored.ContentText)
	}
	if stored.Role != "assistant" {
		t.Fatalf("expected an assistant turn, got %q", stored.Role)
	}
	// Partial, not completed: the run was stopped, so this is not the finished
	// product of the turn even though the model call itself finished.
	if stored.CompletionState != "partial" {
		t.Fatalf("expected the stored turn to be marked partial, got %q", stored.CompletionState)
	}
}

func TestInterruptedSalvageStoresNothingWhenTheTurnProducedNothing(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})

	before, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing was generated, so there is nothing to keep. An empty assistant turn
	// on every cancelled run would be noise in the transcript.
	if messageID := runner.salvageInterruptedAssistantOutput(cancelledContext(), agent.ID, "", modelTurnResult{}); messageID != "" {
		t.Fatalf("expected no message for an empty result, got %s", messageID)
	}
	after, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("expected no new messages, went from %d to %d", len(before), len(after))
	}
}

func TestInterruptedSalvageKeepsToolCallsAndImagesToo(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})

	// A turn can finish with tool calls and no narration. That is still work worth
	// keeping: it is what the activity strip is built from.
	result := modelTurnResult{ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "Read", Input: json.RawMessage(`{"path":"a.txt"}`)}}}
	messageID := runner.salvageInterruptedAssistantOutput(cancelledContext(), agent.ID, "", result)
	if messageID == "" {
		t.Fatal("expected a tool-call-only turn to be stored")
	}

	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages {
		if message.ID == messageID {
			found = true
			if message.CompletionState != "partial" {
				t.Fatalf("expected partial, got %q", message.CompletionState)
			}
		}
	}
	if !found {
		t.Fatal("the tool-call turn is not in the transcript")
	}
}
