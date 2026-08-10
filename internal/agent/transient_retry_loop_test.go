package agent

import (
	"context"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

// The unlimited sentinel has to reach the retry loop, not just the accessor.
// runModelTurn clamped any negative ceiling to zero, so a saved "unlimited" meant
// the first transient failure ended the turn -- the opposite of what was asked
// for. Four failures then a success only completes if the loop keeps going.
func TestRunnerRetriesWithoutLimitWhenCeilingIsUnlimited(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "text", Text: "recovered"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1, MaxTransientRetries: -1})

	if err := runner.run(ctx, agent.ID, ""); err != nil {
		t.Fatalf("an unlimited ceiling must keep retrying until the call succeeds: %v", err)
	}
	if got := provider.requestCount(); got != 5 {
		t.Fatalf("expected five provider requests before the success, got %d", got)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].ContentText != "recovered" {
		t.Fatalf("expected the recovered assistant message, got %+v", messages)
	}
}

// Saving the setting must affect the next turn, so the loop reads the live value
// rather than the config frozen into the runner.
func TestRunnerRetryLoopReadsTheLiveCeiling(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "text", Text: "recovered"}, {Type: "done", Done: true}},
	}}
	// Config says never retry; the live setting says twice. The live one wins.
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1, MaxTransientRetries: 0})
	runner.SetMaxTransientRetries(2)

	if err := runner.run(ctx, agent.ID, ""); err != nil {
		t.Fatalf("the live ceiling should have allowed two retries: %v", err)
	}
	if got := provider.requestCount(); got != 3 {
		t.Fatalf("expected three provider requests, got %d", got)
	}
}

// Zero keeps its own meaning next to unlimited: report the failure immediately.
func TestRunnerDoesNotRetryWhenCeilingIsZero(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "text", Text: "recovered"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1, MaxTransientRetries: 0})

	if err := runner.run(ctx, agent.ID, ""); err == nil {
		t.Fatal("a zero ceiling must surface the first transient failure")
	}
	if got := provider.requestCount(); got != 1 {
		t.Fatalf("expected exactly one provider request, got %d", got)
	}
}
