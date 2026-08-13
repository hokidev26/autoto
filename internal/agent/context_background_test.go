package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// blockingSummaryProvider parks its first Generate call until the context is
// cancelled, standing in for a slow summary model; later calls answer quickly
// so a preempting manual compaction can still finish.
type blockingSummaryProvider struct{ calls atomic.Int32 }

func (p *blockingSummaryProvider) Name() string { return "blocking" }

func (p *blockingSummaryProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Streaming: true}
}

func (p *blockingSummaryProvider) ModelCapabilities(string) providers.ModelCapabilities {
	return providers.ModelCapabilities{}
}

func (p *blockingSummaryProvider) ListModels(context.Context) ([]string, error) {
	return []string{"model"}, nil
}

func (p *blockingSummaryProvider) Generate(ctx context.Context, _ providers.GenerateRequest) (<-chan providers.Event, error) {
	call := p.calls.Add(1)
	out := make(chan providers.Event, 2)
	go func() {
		defer close(out)
		if call == 1 {
			<-ctx.Done()
			return
		}
		out <- providers.Event{Type: "text", Text: "manual summary"}
		out <- providers.Event{Type: "done", Done: true, StopReason: "end_turn"}
	}()
	return out, nil
}

func newBackgroundCompactionTestRunner(store *db.Store, summary providers.Provider, cfg config.AgentConfig) *Runner {
	registry := providers.NewRegistry()
	registry.Register(&scriptedProvider{})
	if summary != nil {
		registry.Register(summary)
	}
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	return NewRunner(store, registry, toolRegistry, NewHub(), cfg)
}

func addBackgroundCompactionMessages(t *testing.T, store *db.Store, agentID string, count int) []db.Message {
	t.Helper()
	ctx := context.Background()
	var messages []db.Message
	for i := 0; i < count; i++ {
		msg, err := store.AddMessage(ctx, db.Message{AgentID: agentID, Role: "user", ContentText: "message " + string(rune('a'+i)) + " " + strings.Repeat("body ", 200)})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
	return messages
}

func waitForBoundary(t *testing.T, store *db.Store, agentID string, timeout time.Duration) db.Agent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		agent, err := store.GetAgent(context.Background(), agentID)
		if err != nil {
			t.Fatal(err)
		}
		if agent.PruneBoundaryMessageID != "" {
			return agent
		}
		if time.Now().After(deadline) {
			t.Fatalf("background compaction did not land a boundary: %+v", agent)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitForCompactionSlotRelease(t *testing.T, runner *Runner, agentID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		runner.runMu.Lock()
		_, compacting := runner.compacting[agentID]
		runner.runMu.Unlock()
		if !compacting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("compaction slot was not released")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestBackgroundCompactionCompactsIdleConversation(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	messages := addBackgroundCompactionMessages(t, store, agent.ID, 12)
	limit := estimateRequestTokens(agent.SystemPrompt, providerMessagesForContext(agent, messages), nil)
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{ContextTokenLimit: limit, SummaryModel: "missing:test"})
	events := runner.hub.Subscribe(ctx, agent.ID)

	runner.maybeCompactContextInBackground(agent.ID)
	compacted := waitForBoundary(t, store, agent.ID, 10*time.Second)
	if compacted.ContextSummary == "" || compacted.PrunedPercent <= 0 {
		t.Fatalf("unexpected compaction state: %+v", compacted)
	}

	sawCompactedEvent := false
	deadline := time.After(5 * time.Second)
	for !sawCompactedEvent {
		select {
		case event := <-events:
			if event.Type == "context.updated" {
				if value, ok := event.Data["compacted"].(bool); ok && value {
					sawCompactedEvent = true
				}
			}
		case <-deadline:
			t.Fatal("background compaction did not publish a compacted context.updated event")
		}
	}
}

func TestBackgroundCompactionSkipsBelowThreshold(t *testing.T) {
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	messages := addBackgroundCompactionMessages(t, store, agent.ID, 12)
	limit := estimateRequestTokens(agent.SystemPrompt, providerMessagesForContext(agent, messages), nil) * 3
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{ContextTokenLimit: limit, SummaryModel: "missing:test"})

	runner.maybeCompactContextInBackground(agent.ID)
	waitForCompactionSlotRelease(t, runner, agent.ID, 5*time.Second)
	unchanged, err := store.GetAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.PruneBoundaryMessageID != "" || unchanged.ContextSummary != "" {
		t.Fatalf("below-threshold conversation must not compact: %+v", unchanged)
	}
}

func TestManualCompactionPreemptsBackgroundCompaction(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	messages := addBackgroundCompactionMessages(t, store, agent.ID, 12)
	limit := estimateRequestTokens(agent.SystemPrompt, providerMessagesForContext(agent, messages), nil)
	blocking := &blockingSummaryProvider{}
	runner := newBackgroundCompactionTestRunner(store, blocking, config.AgentConfig{ContextTokenLimit: limit, SummaryModel: "blocking:model"})

	runner.maybeCompactContextInBackground(agent.ID)
	deadline := time.Now().Add(5 * time.Second)
	for blocking.calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("background compaction never reached the summary model")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The manual request must cancel the parked background compaction, take
	// the slot, and complete with the second (fast) summary call.
	current, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := runner.CompactAgentContext(ctx, agent.ID, current.EntityGeneration)
	if err != nil {
		t.Fatalf("manual compaction should preempt the background one, got %v", err)
	}
	if !result.Compacted {
		t.Fatalf("manual compaction did not compact: %+v", result)
	}
	compacted, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compacted.ContextSummary, "manual summary") {
		t.Fatalf("expected the manual summary to win, got %q", compacted.ContextSummary)
	}
	if calls := blocking.calls.Load(); calls < 2 {
		t.Fatalf("expected a second summary call from the manual path, got %d", calls)
	}
}

// The trigger must sit on executeRegisteredRun's tail, after unregisterRun
// released the running slot: fired from inside the run (completeContinuousRun)
// it always found the finishing run still registered and bailed out busy, so
// idle pre-compaction never ran in production. The scripted turn crosses the
// threshold with its own oversized reply, so the synchronous turn-start path
// cannot be the one that compacts -- only the completion trigger can.
func TestRunCompletionStartsBackgroundCompaction(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	messages := addBackgroundCompactionMessages(t, store, agent.ID, 6)
	limit := estimateRequestTokens(agent.SystemPrompt, providerMessagesForContext(agent, messages), nil) * 2
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: strings.Repeat("weather detail ", 4000)},
		{Type: "done", Done: true, StopReason: "end_turn"},
	}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{ContextTokenLimit: limit, SummaryModel: "missing:test", MaxTurns: 3})
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "summarize the weather"}); err != nil {
		t.Fatal(err)
	}

	runner.Run(ctx, agent.ID)

	compacted := waitForBoundary(t, store, agent.ID, 10*time.Second)
	if compacted.ContextSummary == "" {
		t.Fatalf("run completion did not pre-compact the idle conversation: %+v", compacted)
	}
}

func TestManualCompactionKeepsBusySemantics(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{ContextTokenLimit: 1000})

	if err := runner.beginContextCompaction(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := runner.beginContextCompaction(ctx, agent.ID); !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("second manual compaction must stay busy, got %v", err)
	}
	runner.finishContextCompaction(agent.ID)
	if err := runner.beginContextCompaction(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	runner.finishContextCompaction(agent.ID)
}
