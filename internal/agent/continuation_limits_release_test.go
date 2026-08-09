package agent

import (
	"context"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

// Per-run budgets are frozen when a run starts so editing the settings midway
// cannot change the budgets of a run already under way. Nothing ever released
// them, so the map kept one entry per run for the lifetime of the process and a
// local server left open for days accumulated every run it had executed.
//
// The release is deliberately tied to the run reaching a terminal status rather
// than to a segment ending, because a run parked as continuation_pending resumes
// later under the same ID and must find its original budgets still frozen.
func TestCompletingARunReleasesItsFrozenBudgets(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{DefaultModel: "fake:test"})

	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	runner.freezeContinuationLimits(run.ID, runner.currentContinuationLimits())
	if got := runner.countFrozenContinuationLimits(); got != 1 {
		t.Fatalf("frozen budgets = %d, want 1 before completion", got)
	}

	if err := runner.completeRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if got := runner.countFrozenContinuationLimits(); got != 0 {
		t.Fatalf("frozen budgets = %d, want 0 once the run is terminal", got)
	}
}

func TestAFailedTerminalTransitionKeepsTheFrozenBudgets(t *testing.T) {
	// If the store did not make the run terminal, the run may still be live, so
	// dropping its budgets would let a later segment rebuild them from whatever the
	// settings say then. An invalid status is rejected before any row changes,
	// which is the cheapest way to exercise that branch.
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{DefaultModel: "fake:test"})

	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	runner.freezeContinuationLimits(run.ID, runner.currentContinuationLimits())

	if err := runner.completeRun(ctx, run.ID, "not-a-terminal-status", ""); err == nil {
		t.Fatal("an invalid terminal status must be rejected")
	}
	if got := runner.countFrozenContinuationLimits(); got != 1 {
		t.Fatalf("frozen budgets = %d, want 1 kept when the run did not become terminal", got)
	}
}

func TestFrozenBudgetsSurviveASegmentSoResumeKeepsThem(t *testing.T) {
	// continuation_pending is not terminal: the same run ID comes back later.
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{
		DefaultModel:             "fake:test",
		ContinuationSegmentTurns: 7,
	})

	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	original := runner.currentContinuationLimits()
	runner.freezeContinuationLimits(run.ID, original)

	// The user edits the budget while the run is parked.
	runner.SetContinuationSettings(ContinuationSettings{Mode: continuationModeSafe, SegmentTurns: 99})

	frozen, ok := runner.frozenContinuationLimits(run.ID)
	if !ok {
		t.Fatal("a parked run must keep its frozen budgets")
	}
	if frozen.segmentTurns != original.segmentTurns {
		t.Fatalf("segmentTurns = %d, want the frozen %d rather than the edited value", frozen.segmentTurns, original.segmentTurns)
	}
}
