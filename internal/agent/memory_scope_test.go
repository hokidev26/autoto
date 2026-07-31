package agent

import (
	"context"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

// A memory owned by the conversation is durable state: it must reach the model on
// every run, without a keyword hit, and must never be consumed by the ledger.
func TestConversationMemoryReinjectsOnEveryRunAndStaysOutOfLedger(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	owned, err := store.CreateMemory(ctx, db.Memory{AgentID: agent.ID, Content: "retained: this project ships on Tuesdays"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "text", Text: "first"}, {Type: "done", Done: true}},
		{{Type: "text", Text: "second"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})

	for _, text := range []string{"totally unrelated first ask", "totally unrelated second ask"} {
		trigger, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: text})
		if err != nil {
			t.Fatal(err)
		}
		runner.RunWithTrigger(ctx, agent.ID, trigger.ID)
	}

	if provider.requestCount() < 2 {
		t.Fatalf("expected at least two provider requests, got %d", provider.requestCount())
	}
	for i := 0; i < provider.requestCount(); i++ {
		// Memory is untrusted user context by design, never system authority.
		if !requestHasUntrustedUserContext(provider.request(i), "memory", owned.Content) {
			t.Fatalf("owned memory missing from request %d", i)
		}
		if strings.Contains(provider.request(i).SystemPrompt, owned.Content) {
			t.Fatalf("owned memory must not be promoted into request %d system prompt", i)
		}
	}
	var ledgerCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_injections WHERE agent_id = ?`, agent.ID).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if ledgerCount != 0 {
		t.Fatalf("an owned memory must not consume the injection ledger, got %d rows", ledgerCount)
	}
}

// Compaction rewrites the summary and a clear wipes it, but a retained copy lives
// in the memory table and therefore survives both.
func TestRetainedConversationMemorySurvivesCompactionAndClear(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	owned, err := store.CreateMemory(ctx, db.Memory{AgentID: agent.ID, Content: "retained: the API key lives in the vault"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAgentContextSummary(ctx, agent.ID, "rolling summary that will be replaced", "", 10); err != nil {
		t.Fatal(err)
	}
	latest, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "anchor message"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearAgentContext(ctx, agent.ID, current.EntityGeneration, latest.ID); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cleared.ContextSummary) != "" {
		t.Fatalf("expected the rolling summary to be cleared, got %q", cleared.ContextSummary)
	}
	stored, err := store.GetMemory(ctx, owned.ID)
	if err != nil {
		t.Fatalf("retained memory must outlive a context clear: %v", err)
	}
	if stored.Content != owned.Content || stored.AgentID != agent.ID {
		t.Fatalf("retained memory changed: %+v", stored)
	}
	matches, err := store.ListMatchingUninjectedMemories(ctx, agent.ID, "anything at all", memoryInjectionLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != owned.ID {
		t.Fatalf("retained memory must still be injectable after a clear, got %+v", matches)
	}
}

func TestBoundedMemorySystemContextLedgersGlobalOnlyAndCountsRendered(t *testing.T) {
	memories := []db.Memory{
		{ID: "owned-1", AgentID: "agent-1", Content: "owned one"},
		{ID: "global-1", Content: "global one", Keywords: []string{"one"}},
		{ID: "blank", AgentID: "agent-1", Content: "   "},
	}
	rendered, ledgerIDs := boundedMemorySystemContext(memories)
	if !strings.Contains(rendered, "owned one") || !strings.Contains(rendered, "global one") {
		t.Fatalf("both buckets must render: %s", rendered)
	}
	if strings.Contains(rendered, "blank") {
		t.Fatalf("empty content must be dropped: %s", rendered)
	}
	if len(ledgerIDs) != 1 || ledgerIDs[0] != "global-1" {
		t.Fatalf("only global memories belong in the ledger, got %v", ledgerIDs)
	}
	if count := renderedMemoryCount(memories); count != 2 {
		t.Fatalf("rendered count must ignore blank entries, got %d", count)
	}
}
