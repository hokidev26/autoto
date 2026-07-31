package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryCRUDSearchArchivePinAndValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreateMemory(ctx, Memory{
		Content:  "Remember Café deployments",
		Keywords: []string{" Go ", "gO", " 项目 ", "ÜBER"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("unexpected memory metadata: %+v", created)
	}
	wantKeywords := []string{"go", "项目", "über"}
	if len(created.Keywords) != len(wantKeywords) {
		t.Fatalf("unexpected normalized keywords: %+v", created.Keywords)
	}
	for index, keyword := range wantKeywords {
		if created.Keywords[index] != keyword {
			t.Fatalf("unexpected keyword %d: want %q got %q", index, keyword, created.Keywords[index])
		}
	}
	got, err := store.GetMemory(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != created.Content || strings.Join(got.Keywords, ",") != strings.Join(wantKeywords, ",") {
		t.Fatalf("unexpected memory round trip: %+v", got)
	}

	other, err := store.CreateMemory(ctx, Memory{Content: "Other note", Keywords: []string{"other"}})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := store.PinMemory(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.Pinned || pinned.UpdatedAt == created.UpdatedAt {
		t.Fatalf("expected pin to update memory: before=%+v after=%+v", created, pinned)
	}
	listed, err := store.ListMemories(ctx, "CAFÉ", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("unexpected Unicode content search: %+v", listed)
	}
	listed, err = store.ListMemories(ctx, MemoryListOptions{Query: "项目"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("unexpected keyword search: %+v", listed)
	}
	listed, err = store.ListMemories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != created.ID || listed[1].ID != other.ID {
		t.Fatalf("expected pinned memory first, got %+v", listed)
	}

	created.Content = "Updated content"
	created.Keywords = []string{" Updated "}
	created.Pinned = false
	updated, err := store.UpdateMemory(ctx, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "Updated content" || len(updated.Keywords) != 1 || updated.Keywords[0] != "updated" || updated.Pinned || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("unexpected updated memory: %+v", updated)
	}
	archived, err := store.ArchiveMemory(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == "" {
		t.Fatalf("expected archived timestamp: %+v", archived)
	}
	listed, err = store.ListMemories(ctx, "updated", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("archived memories must be hidden by default: %+v", listed)
	}
	listed, err = store.ListMemories(ctx, "updated", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != updated.ID {
		t.Fatalf("includeArchived must include archived memory: %+v", listed)
	}
	unarchived, err := store.UnarchiveMemory(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unarchived.ArchivedAt != "" {
		t.Fatalf("expected unarchived memory: %+v", unarchived)
	}

	invalid := []Memory{
		{Content: "   "},
		{Content: strings.Repeat("x", MemoryContentMaxBytes+1)},
		{Content: "content", Keywords: []string{" "}},
		{Content: "content", Keywords: []string{strings.Repeat("界", MemoryKeywordMaxRunes+1)}},
	}
	tooManyKeywords := Memory{Content: "content"}
	for index := 0; index < MemoryMaxKeywords+1; index++ {
		tooManyKeywords.Keywords = append(tooManyKeywords.Keywords, fmt.Sprintf("keyword-%d", index))
	}
	invalid = append(invalid, tooManyKeywords)
	for _, memory := range invalid {
		if _, err := store.CreateMemory(ctx, memory); err == nil {
			t.Fatalf("expected invalid memory to fail: %+v", memory)
		}
	}
	if _, err := store.CreateMemory(ctx, Memory{Content: strings.Repeat("x", MemoryContentMaxBytes)}); err != nil {
		t.Fatalf("expected exactly 16KiB content to succeed: %v", err)
	}
	manyDuplicates := make([]string, MemoryMaxKeywords+1)
	for index := range manyDuplicates {
		manyDuplicates[index] = " Duplicate "
	}
	deduplicated, err := store.CreateMemory(ctx, Memory{Content: "duplicates normalize before limit", Keywords: manyDuplicates})
	if err != nil {
		t.Fatalf("expected duplicate keywords to normalize before enforcing limit: %v", err)
	}
	if len(deduplicated.Keywords) != 1 || deduplicated.Keywords[0] != "duplicate" {
		t.Fatalf("unexpected deduplicated keywords: %+v", deduplicated.Keywords)
	}
	if err := store.DeleteMemory(ctx, updated.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMemory(ctx, updated.ID); !IsNotFound(err) {
		t.Fatalf("expected deleted memory to be missing, got %v", err)
	}
	if err := store.DeleteMemory(ctx, updated.ID); !IsNotFound(err) {
		t.Fatalf("expected repeated delete to be not found, got %v", err)
	}
}

func TestMemoryMatchingInjectionOrderingAndIdempotency(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, firstAgent, err := store.CreateProject(ctx, "First", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, secondAgent, err := store.CreateProject(ctx, "Second", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	manual, err := store.CreateMemory(ctx, Memory{Content: "manual memory without keywords"})
	if err != nil {
		t.Fatal(err)
	}
	older, err := store.CreateMemory(ctx, Memory{Content: "older Go memory", Keywords: []string{"GO"}})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := store.CreateMemory(ctx, Memory{Content: "newer Go memory", Keywords: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := store.CreateMemory(ctx, Memory{Content: "pinned project memory", Keywords: []string{"项目"}, Pinned: true})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := store.CreateMemory(ctx, Memory{Content: "archived Go memory", Keywords: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveMemory(ctx, archived.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE memories SET updated_at = ? WHERE id = ?`, "2026-01-01T00:00:01Z", older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE memories SET updated_at = ? WHERE id = ?`, "2026-01-01T00:00:02Z", newer.ID); err != nil {
		t.Fatal(err)
	}

	matches, err := store.ListMatchingUninjectedMemories(ctx, firstAgent.ID, "正在处理项目，也在写 Go", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{pinned.ID, newer.ID, older.ID}
	if len(matches) != len(want) {
		t.Fatalf("unexpected initial matches: %+v", matches)
	}
	for index, id := range want {
		if matches[index].ID != id {
			t.Fatalf("unexpected match order at %d: want %s got %+v", index, id, matches)
		}
	}
	for _, excludedID := range []string{manual.ID, archived.ID} {
		for _, match := range matches {
			if match.ID == excludedID {
				t.Fatalf("memory %s must not be passively injected", excludedID)
			}
		}
	}

	if err := store.MarkMemoriesInjected(ctx, firstAgent.ID, []string{pinned.ID, newer.ID, pinned.ID}); err != nil {
		t.Fatal(err)
	}
	var originalInjectedAt string
	if err := store.DB().QueryRowContext(ctx, `SELECT injected_at FROM memory_injections WHERE memory_id = ? AND agent_id = ?`, pinned.ID, firstAgent.ID).Scan(&originalInjectedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkMemoriesInjected(ctx, firstAgent.ID, []string{pinned.ID, newer.ID}); err != nil {
		t.Fatal(err)
	}
	var count int
	var injectedAt string
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*), MIN(injected_at) FROM memory_injections WHERE agent_id = ?`, firstAgent.ID).Scan(&count, &injectedAt); err != nil {
		t.Fatal(err)
	}
	if count != 2 || injectedAt != originalInjectedAt {
		t.Fatalf("expected idempotent ledger writes, count=%d original=%q current=%q", count, originalInjectedAt, injectedAt)
	}
	matches, err = store.ListMatchingUninjectedMemories(ctx, firstAgent.ID, "项目 go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != older.ID {
		t.Fatalf("expected only uninjected memory for first agent, got %+v", matches)
	}
	matches, err = store.ListMatchingUninjectedMemories(ctx, secondAgent.ID, "项目 GO", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].ID != pinned.ID || matches[1].ID != newer.ID {
		t.Fatalf("injections must be scoped per agent and honor limit: %+v", matches)
	}
}

func TestMemoryInjectionValidationTransactionAndCascades(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Cascade", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateMemory(ctx, Memory{Content: "first", Keywords: []string{"first"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateMemory(ctx, Memory{Content: "second", Keywords: []string{"second"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkMemoriesInjected(ctx, "missing-agent", []string{first.ID}); !IsNotFound(err) {
		t.Fatalf("expected missing agent validation, got %v", err)
	}
	if err := store.MarkMemoriesInjected(ctx, agent.ID, []string{first.ID, "missing-memory"}); !IsNotFound(err) {
		t.Fatalf("expected missing memory validation, got %v", err)
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_injections`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("validation failure must roll back all ledger writes, got %d", count)
	}
	if err := store.MarkMemoriesInjected(ctx, agent.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMemory(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_injections`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("deleting memory must cascade its ledger rows, got %d", count)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_injections`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("deleting agent must cascade its ledger rows, got %d", count)
	}
}

func TestMemoryFreshSchemaAndV17MigrationMatch(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if version := readUserVersion(t, ctx, fresh.DB()); version != CurrentDBVersion {
		t.Fatalf("expected fresh database version %d, got %d", CurrentDBVersion, version)
	}
	freshSchema := memorySchemaSnapshot(t, ctx, fresh.DB())
	for _, name := range []string{"memories", "memory_injections", "idx_memories_pinned_updated", "idx_memories_archived", "idx_memories_agent", "idx_memory_injections_agent"} {
		if !strings.Contains(freshSchema, name) {
			t.Fatalf("fresh memory schema missing %s: %s", name, freshSchema)
		}
	}

	path := filepath.Join(t.TempDir(), "v17.db")
	raw := openRawDB(t, path)
	if _, err := raw.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TABLE memory_injections; DROP TABLE memories; PRAGMA user_version = 17`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version := readUserVersion(t, ctx, migrated.DB()); version != CurrentDBVersion {
		t.Fatalf("expected migrated database version %d, got %d", CurrentDBVersion, version)
	}
	migratedSchema := memorySchemaSnapshot(t, ctx, migrated.DB())
	if migratedSchema != freshSchema {
		t.Fatalf("fresh and migrated memory schemas differ\nfresh:\n%s\nmigrated:\n%s", freshSchema, migratedSchema)
	}
}

func memorySchemaSnapshot(t *testing.T, ctx context.Context, database *sql.DB) string {
	t.Helper()
	rows, err := database.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name IN ('memories', 'memory_injections') AND type IN ('table', 'index') AND sql IS NOT NULL ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var snapshot strings.Builder
	for rows.Next() {
		var objectType, name, definition string
		if err := rows.Scan(&objectType, &name, &definition); err != nil {
			t.Fatal(err)
		}
		snapshot.WriteString(objectType)
		snapshot.WriteByte(':')
		snapshot.WriteString(name)
		snapshot.WriteByte('=')
		snapshot.WriteString(definition)
		snapshot.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}
