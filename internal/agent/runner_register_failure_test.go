package agent

import (
	"context"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

// A run row created during registration must never be left mid-flight when a
// later step of that same registration fails.
//
// It used to be: CreateRun writes the row, AssignMessageRun fails, registerRun
// returns only an error, and the ID it had just created lives nowhere else. The
// caller could only fall back to the run ID it passed in, which is empty for a
// fresh turn, so it skipped CompleteRun and the row stayed "running" forever.
// Nothing reaps it. The client compounds that: it renders a run summary only for
// a terminal run, so the newest run being a phantom "running" is also why an
// agent could stop with no reason visible anywhere.
//
// The fault is injected by removing agent_messages, which leaves CreateRun
// working and fails exactly the statement under test. Triggering it any other way
// is not possible from outside: AssignMessageRun is a bare UPDATE with no
// rows-affected check, so a nonexistent message ID updates nothing and succeeds.
func TestRegisterRunCompletesRunItCreatedWhenTriggerAssignmentFails(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()

	message, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "do the thing"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.DB().ExecContext(ctx, `ALTER TABLE agent_messages RENAME TO agent_messages_hidden`); err != nil {
		t.Fatal(err)
	}

	// The provider is never reached: registration fails first. It only has to
	// exist so the runner can be constructed.
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{DefaultModel: "fake:test"})
	runner.RunWithTrigger(ctx, agent.ID, message.ID)

	// Restore the table so the assertions can read state back.
	if _, err := store.DB().ExecContext(ctx, `ALTER TABLE agent_messages_hidden RENAME TO agent_messages`); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListRuns(ctx, agent.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("registration created a run row and then failed, so that row must still be listed")
	}
	for _, run := range runs {
		if run.Status == "running" || run.Status == "pending" {
			t.Fatalf("run %s left mid-flight with status %q; a failed registration must give the row a terminal status", run.ID, run.Status)
		}
	}
	if got := runs[0].Status; got != "error" {
		t.Fatalf("run status = %q, want \"error\"", got)
	}
	if strings.TrimSpace(runs[0].ErrorMessage) == "" {
		t.Fatal("the failed run carries no reason, which is what left the screen empty")
	}
}
