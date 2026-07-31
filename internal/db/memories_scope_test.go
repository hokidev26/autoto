package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func memoryScopeStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, ctx
}

func memoryIDs(memories []Memory) []string {
	ids := make([]string, 0, len(memories))
	for _, memory := range memories {
		ids = append(ids, memory.ID)
	}
	return ids
}

func TestConversationScopedMemoryInjectsWithoutKeywordsAndIgnoresLedger(t *testing.T) {
	store, ctx := memoryScopeStore(t)
	_, _, owner, err := store.CreateProject(ctx, "Owner", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, other, err := store.CreateProject(ctx, "Other", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	// No keywords at all: a global memory like this is never injected, but an
	// owned one is durable state for its conversation.
	owned, err := store.CreateMemory(ctx, Memory{AgentID: owner.ID, Content: "retained summary for this conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if owned.AgentID != owner.ID {
		t.Fatalf("ownership must round-trip, got %+v", owned)
	}

	matches, err := store.ListMatchingUninjectedMemories(ctx, owner.ID, "totally unrelated trigger text", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != owned.ID {
		t.Fatalf("owned memory must inject without a keyword hit, got %v", memoryIDs(matches))
	}

	// The ledger governs global memories only. Marking an owned memory injected
	// must not stop it from being sent again, because every run rebuilds the
	// system prompt and a later compaction would otherwise lose it for good.
	if err := store.MarkMemoriesInjected(ctx, owner.ID, []string{owned.ID}); err != nil {
		t.Fatal(err)
	}
	matches, err = store.ListMatchingUninjectedMemories(ctx, owner.ID, "still unrelated", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != owned.ID {
		t.Fatalf("owned memory must survive the injection ledger, got %v", memoryIDs(matches))
	}

	// Another conversation must never see it, even with matching text.
	matches, err = store.ListMatchingUninjectedMemories(ctx, other.ID, "retained summary", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("owned memory must not leak to another conversation, got %v", memoryIDs(matches))
	}
}

func TestOwnedMemoriesTakePriorityOverGlobalWithinInjectionLimit(t *testing.T) {
	store, ctx := memoryScopeStore(t)
	_, _, agent, err := store.CreateProject(ctx, "Priority", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	global, err := store.CreateMemory(ctx, Memory{Content: "global pnpm note", Keywords: []string{"pnpm"}})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := store.CreateMemory(ctx, Memory{AgentID: agent.ID, Content: "owned pnpm note"})
	if err != nil {
		t.Fatal(err)
	}

	matches, err := store.ListMatchingUninjectedMemories(ctx, agent.ID, "we use pnpm here", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].ID != owned.ID || matches[1].ID != global.ID {
		t.Fatalf("owned memories must sort ahead of global ones, got %v", memoryIDs(matches))
	}

	// A tight budget must go to the owned memory.
	matches, err = store.ListMatchingUninjectedMemories(ctx, agent.ID, "we use pnpm here", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != owned.ID {
		t.Fatalf("owned memory must win a single injection slot, got %v", memoryIDs(matches))
	}
}

func TestMemoryScopeListingFiltersAndValidates(t *testing.T) {
	store, ctx := memoryScopeStore(t)
	_, _, first, err := store.CreateProject(ctx, "First", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, second, err := store.CreateProject(ctx, "Second", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	global, err := store.CreateMemory(ctx, Memory{Content: "global entry", Keywords: []string{"global"}})
	if err != nil {
		t.Fatal(err)
	}
	firstOwned, err := store.CreateMemory(ctx, Memory{AgentID: first.ID, Content: "first owned entry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(ctx, Memory{AgentID: second.ID, Content: "second owned entry"}); err != nil {
		t.Fatal(err)
	}

	all, err := store.ListMemories(ctx, MemoryListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("default scope must list every memory, got %v", memoryIDs(all))
	}

	globals, err := store.ListMemories(ctx, MemoryListOptions{Scope: MemoryScopeGlobal})
	if err != nil {
		t.Fatal(err)
	}
	if len(globals) != 1 || globals[0].ID != global.ID {
		t.Fatalf("global scope must exclude owned memories, got %v", memoryIDs(globals))
	}

	owned, err := store.ListMemories(ctx, MemoryListOptions{Scope: MemoryScopeAgent, AgentID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].ID != firstOwned.ID {
		t.Fatalf("agent scope must list only that conversation, got %v", memoryIDs(owned))
	}

	if _, err := store.ListMemories(ctx, MemoryListOptions{Scope: MemoryScopeAgent}); err == nil {
		t.Fatal("agent scope without an agent id must be rejected")
	}
	if _, err := store.ListMemories(ctx, MemoryListOptions{Scope: "sideways"}); err == nil {
		t.Fatal("an unknown scope must be rejected")
	}
}

func TestScopedMemoryRejectsMissingAgentAndCascadesOnDelete(t *testing.T) {
	store, ctx := memoryScopeStore(t)
	_, _, agent, err := store.CreateProject(ctx, "Cascade", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(ctx, Memory{AgentID: "missing-agent", Content: "orphan"}); err == nil {
		t.Fatal("a memory owned by a nonexistent conversation must be rejected")
	}
	if _, err := store.CreateMemory(ctx, Memory{AgentID: strings.Repeat("x", 129), Content: "too long"}); err == nil {
		t.Fatal("an oversized agent id must be rejected")
	}
	owned, err := store.CreateMemory(ctx, Memory{AgentID: agent.ID, Content: "dies with its conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMemory(ctx, owned.ID); !IsNotFound(err) {
		t.Fatalf("deleting the conversation must remove its memories, got %v", err)
	}
}

func TestMemoryOwnershipIsImmutableAcrossUpdates(t *testing.T) {
	store, ctx := memoryScopeStore(t)
	_, _, owner, err := store.CreateProject(ctx, "Owner", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, other, err := store.CreateProject(ctx, "Other", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	owned, err := store.CreateMemory(ctx, Memory{AgentID: owner.ID, Content: "original"})
	if err != nil {
		t.Fatal(err)
	}
	// Reassigning ownership would silently change who can read the memory.
	owned.AgentID = other.ID
	owned.Content = "edited"
	updated, err := store.UpdateMemory(ctx, owned)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AgentID != owner.ID || updated.Content != "edited" {
		t.Fatalf("update must keep ownership and apply the edit, got %+v", updated)
	}
	stored, err := store.GetMemory(ctx, owned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AgentID != owner.ID {
		t.Fatalf("stored ownership must not move, got %+v", stored)
	}
}
