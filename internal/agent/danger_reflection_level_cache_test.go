package agent

import (
	"context"
	"testing"

	"autoto/internal/config"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// The strictness level is deliberately absent from dangerReflectionFingerprint,
// so on its own a cached "proceed" from the loose level would still be reused
// after the user moved to strict -- inside the same Run, for the same command,
// which is exactly when the cache hits.
//
// What saves it is that the level lives in workflow_preferences, and writing
// that row bumps policy_generation, which cachedReflection binds every entry to.
// That is a real but indirect guarantee: it holds only as long as the level
// stays in a table whose writes bump the generation. This test states the
// property directly, so moving the setting somewhere cheaper fails here instead
// of silently keeping stale verdicts alive.
func TestDangerReflectionCacheIsDroppedWhenTheStrictnessLevelChanges(t *testing.T) {
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{
		reflectionToolEvents(reflectionToolProceed, "Fine under the loose level."),
		reflectionToolEvents(reflectionToolConfirm, "Wants a look under the strict level."),
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3, SummaryModel: "fake:test"})
	ctx := context.Background()

	prefs, err := store.GetWorkflowPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefs.DangerReflectionLevel = "loose"
	if _, err := store.UpdateWorkflowPreferences(ctx, prefs); err != nil {
		t.Fatal(err)
	}

	const runID = "run-same"
	call := bashCall("somecmd --thing")

	first := runner.reflectBeforeExecution(ctx, agent, agent.PermissionMode, runID, call, tools.RiskExec, allowResolution())
	if first.Decision != toolPermissionAllow {
		t.Fatalf("loose level returned proceed, so the allow must stand: %+v", first)
	}
	if provider.requestCount() != 1 {
		t.Fatalf("expected one reflection call, got %d", provider.requestCount())
	}

	// Same Run, same command: only the level changes.
	prefs.DangerReflectionLevel = "strict"
	if _, err := store.UpdateWorkflowPreferences(ctx, prefs); err != nil {
		t.Fatal(err)
	}

	second := runner.reflectBeforeExecution(ctx, agent, agent.PermissionMode, runID, call, tools.RiskExec, allowResolution())
	if provider.requestCount() != 2 {
		t.Fatalf("changing the strictness level must re-judge the action, but the cached verdict was reused: %d model calls", provider.requestCount())
	}
	if second.Decision != toolPermissionAsk {
		t.Fatalf("strict level returned confirm, so the call must escalate to approval: %+v", second)
	}
	if second.Source != decisionSourceDangerReflection {
		t.Fatalf("escalation must be attributed to danger reflection, got %q", second.Source)
	}
}

// Turning the gate off and back on within one Run must not resurrect a verdict
// decided before it was disabled.
func TestDangerReflectionCacheIsDroppedWhenTheGateIsToggled(t *testing.T) {
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{
		reflectionToolEvents(reflectionToolProceed, "First judgment."),
		reflectionToolEvents(reflectionToolConfirm, "Judged again after the toggle."),
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3, SummaryModel: "fake:test"})
	ctx := context.Background()

	prefs, err := store.GetWorkflowPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefs.DangerReflectionLevel = "medium"
	if _, err := store.UpdateWorkflowPreferences(ctx, prefs); err != nil {
		t.Fatal(err)
	}

	const runID = "run-toggle"
	call := bashCall("somecmd --other")
	runner.reflectBeforeExecution(ctx, agent, agent.PermissionMode, runID, call, tools.RiskExec, allowResolution())
	if provider.requestCount() != 1 {
		t.Fatalf("expected one reflection call, got %d", provider.requestCount())
	}

	for _, level := range []string{"off", "medium"} {
		prefs.DangerReflectionLevel = level
		if _, err := store.UpdateWorkflowPreferences(ctx, prefs); err != nil {
			t.Fatal(err)
		}
	}

	resolution := runner.reflectBeforeExecution(ctx, agent, agent.PermissionMode, runID, call, tools.RiskExec, allowResolution())
	if provider.requestCount() != 2 {
		t.Fatalf("toggling the gate must drop cached verdicts, got %d model calls", provider.requestCount())
	}
	if resolution.Decision != toolPermissionAsk {
		t.Fatalf("the fresh confirm verdict must apply: %+v", resolution)
	}
}
