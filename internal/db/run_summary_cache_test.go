package db

import (
	"context"
	"path/filepath"
	"testing"
)

// api_requests has recorded cached_input_tokens all along, but RunSummary never
// selected the column, so the conversation details panel reported a cache
// figure of zero for every run while input and output were right.
func TestRunSummaryReportsCachedInputTokens(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "run-summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	agent, err := store.CreateAgent(ctx, Agent{Title: "cache-summary", Type: "primary", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}

	// Two requests, so the assertion also proves the column is summed rather
	// than read from a single row.
	for _, request := range []APIRequest{
		{AgentID: agent.ID, RunID: run.ID, InputTokens: 400, OutputTokens: 90, CachedInputTokens: 1200, CostUSD: 0.02},
		{AgentID: agent.ID, RunID: run.ID, InputTokens: 150, OutputTokens: 60, CachedInputTokens: 800, CostUSD: 0.01},
	} {
		if _, err := store.AddAPIRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
	}

	summary, err := store.RunSummary(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CachedInputTokens != 2000 {
		t.Fatalf("CachedInputTokens = %d, want 2000", summary.CachedInputTokens)
	}
	// The figures that already worked must keep working.
	if summary.InputTokens != 550 {
		t.Fatalf("InputTokens = %d, want 550", summary.InputTokens)
	}
	if summary.OutputTokens != 150 {
		t.Fatalf("OutputTokens = %d, want 150", summary.OutputTokens)
	}
}
