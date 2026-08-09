package db

import (
	"context"
	"path/filepath"
	"testing"
)

// AssignMessageRun binds a message to the run that produced it. It used to be a
// bare UPDATE that returned only the driver error, so a message ID that matched no
// row changed nothing and still reported success. Every production caller creates
// the message immediately before binding it, so a miss means the two disagree
// about what exists, and reporting success left the caller unable to react.
func newAssignTestStore(t *testing.T) (*Store, Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "assign.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	return store, agent
}

func TestAssignMessageRunReportsAMissingMessage(t *testing.T) {
	ctx := context.Background()
	store, agent := newAssignTestStore(t)

	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}

	err = store.AssignMessageRun(ctx, agent.ID, "message-that-does-not-exist", run.ID)
	if err == nil {
		t.Fatal("binding a run to a message that does not exist must not report success")
	}
	if !IsNotFound(err) {
		t.Fatalf("error must be recognisable by IsNotFound, got %v", err)
	}
}

func TestAssignMessageRunReportsAMessageBelongingToAnotherAgent(t *testing.T) {
	// The agent ID is part of the WHERE clause, so a mismatch is the same class of
	// miss: the row exists but not for this agent, and it must not look like a bind.
	ctx := context.Background()
	store, agent := newAssignTestStore(t)
	_, _, other, err := store.CreateProject(ctx, "Other", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	message, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: other.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.AssignMessageRun(ctx, other.ID, message.ID, run.ID); !IsNotFound(err) {
		t.Fatalf("cross-agent bind must report not found, got %v", err)
	}
}

func TestAssignMessageRunStaysIdempotent(t *testing.T) {
	// Re-binding the same run must keep working. SQLite counts rows matched by the
	// WHERE clause rather than rows whose values changed, but that is the behaviour
	// the not-found check now depends on, so it is worth pinning.
	ctx := context.Background()
	store, agent := newAssignTestStore(t)

	message, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := store.AssignMessageRun(ctx, agent.ID, message.ID, run.ID); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}

	// Clearing the binding passes an empty run ID, which NULLIF turns into NULL.
	// That still matches the row, so it must not be mistaken for a missing message.
	if err := store.AssignMessageRun(ctx, agent.ID, message.ID, ""); err != nil {
		t.Fatalf("clearing the run binding must succeed: %v", err)
	}
}
