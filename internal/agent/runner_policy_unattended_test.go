package agent

import (
	"context"
	"encoding/json"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/tools"
)

func TestIsUnattendedRunFailsClosedForUnknownSources(t *testing.T) {
	for _, testCase := range []struct {
		source string
		want   bool
	}{
		{db.RunSourceManual, false},
		{db.RunSourceConversation, false},
		{" manual ", false},
		{"schedule", true},
		{"internal", true},
		{"", true},
		{"some-future-trigger", true},
	} {
		if got := isUnattendedRun(db.Run{Source: testCase.source}); got != testCase.want {
			t.Errorf("isUnattendedRun(%q) = %v, want %v", testCase.source, got, testCase.want)
		}
	}
}

// A session grant is one human's answer to one interactive question. It
// outranks execRequiresHumanReview, so letting a schedule dispatch spend a
// grant given hours earlier in a different Run turns "approve this once, for
// this session" into unattended execution at 03:00 with nobody watching.
func TestUnattendedRunDoesNotSpendSessionGrant(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()

	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	input, err := json.Marshal(map[string]string{"command": "pkill -f stale-worker"})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	// Without a grant the command must reach a human: it is the serious-but-
	// recoverable tier that outranks stored allow rules.
	before := runner.resolveToolPermissionWithSession(ctx, agent.ID, "acceptEdits", "Bash", tools.RiskExec, input, true)
	if before.Decision != toolPermissionAsk {
		t.Fatalf("expected the review gate to ask before any grant, got %+v", before)
	}

	generations, err := store.GetPermissionGenerations(ctx, agent.ID)
	if err != nil {
		t.Fatalf("generations: %v", err)
	}
	grantKey := sessionGrantKey("Bash", input)
	if grantKey == "" {
		t.Fatal("expected a session grant key for the command")
	}
	runner.addSessionGrant(agent.ID, grantKey, generations.Permission, generations.Policy)

	attended := runner.resolveToolPermissionWithSession(ctx, agent.ID, "acceptEdits", "Bash", tools.RiskExec, input, true)
	if attended.Decision != toolPermissionAllow || attended.Source != decisionSourceSessionApproval {
		t.Fatalf("an attended Run must still reuse its own grant, got %+v", attended)
	}

	// Which gate catches it afterwards depends on how the platform's static
	// analysis classifies the command; what matters is that the answer is no
	// longer "allow", so the call has to reach a human.
	unattended := runner.resolveToolPermissionWithSession(ctx, agent.ID, "acceptEdits", "Bash", tools.RiskExec, input, false)
	if unattended.Decision != toolPermissionAsk {
		t.Fatalf("an unattended Run must not spend the grant, got %+v", unattended)
	}
	if unattended.Source == decisionSourceSessionApproval {
		t.Fatalf("the unattended decision must not come from the session grant: %+v", unattended)
	}

	// The grant still belongs to the interactive session that earned it.
	if again := runner.resolveToolPermissionWithSession(ctx, agent.ID, "acceptEdits", "Bash", tools.RiskExec, input, true); again.Decision != toolPermissionAllow {
		t.Fatalf("the unattended check must not consume or invalidate the grant, got %+v", again)
	}
}

func TestPolicyContextMarksScheduleRunsUnattended(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})

	schedule, err := store.CreateSchedule(ctx, db.Schedule{
		Name: "nightly", AgentID: agent.ID, Expression: "0 3 * * *", Timezone: "UTC",
		Prompt: "run the nightly checks", PermissionMode: "acceptEdits",
		EnvironmentMode: "workline", NarratorMode: "reuse", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	run, err := runner.SubmitScheduleDispatch(ctx, schedule, "dispatch-1")
	if err != nil {
		t.Fatalf("submit schedule dispatch: %v", err)
	}

	_, policy, err := runner.policyContext(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatalf("policy context: %v", err)
	}
	if !policy.Unattended {
		t.Fatalf("a schedule dispatch must produce an unattended policy context: %+v", policy)
	}
	waitForRunSettled(t, store, runner, agent.ID, run.ID)
}
