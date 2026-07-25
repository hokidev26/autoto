package db

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Stored timestamps are compared as text by every ORDER BY created_at query and
// keyset cursor in this schema, so their lexical order must match their
// chronological order.
func TestNowProducesLexicallySortableTimestamps(t *testing.T) {
	stamps := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		stamps = append(stamps, Now())
	}
	sorted := append([]string(nil), stamps...)
	sort.Strings(sorted)
	for i := range stamps {
		if stamps[i] != sorted[i] {
			t.Fatalf("lexical order diverged from generation order at %d: generated %q, sorted %q", i, stamps[i], sorted[i])
		}
	}
	width := len(stamps[0])
	for _, stamp := range stamps {
		if len(stamp) != width {
			t.Fatalf("timestamps must share one width for text comparison: %q (%d) vs %q (%d)", stamps[0], width, stamp, len(stamp))
		}
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			t.Fatalf("timestamp %q is not RFC3339: %v", stamp, err)
		}
	}
}

// A whole-second instant is the case time.RFC3339Nano formatted without any
// fractional digits, which sorted after every sub-second value in that second.
func TestNowKeepsWholeSecondInstantsSortableAgainstSubSecondInstants(t *testing.T) {
	whole := time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC).Format(timestampLayout)
	sub := time.Date(2026, 7, 25, 6, 0, 0, 1, time.UTC).Format(timestampLayout)
	nextSecond := time.Date(2026, 7, 25, 6, 0, 1, 0, time.UTC).Format(timestampLayout)
	if !(whole < sub && sub < nextSecond) {
		t.Fatalf("whole-second instants must sort before later instants: whole=%q sub=%q next=%q", whole, sub, nextSecond)
	}
}

func TestMessageOrderingSurvivesWholeSecondTimestamps(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "ordering.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Ordering", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	// Force the exact adversarial layout: a whole-second row followed by
	// sub-second rows inside that same second.
	second := time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC)
	ids := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		message, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "m"})
		if err != nil {
			t.Fatal(err)
		}
		stamp := second.Add(time.Duration(i) * time.Nanosecond).Format(timestampLayout)
		if _, err := store.DB().ExecContext(ctx, `UPDATE agent_messages SET created_at = ? WHERE id = ?`, stamp, message.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, message.ID)
	}

	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != len(ids) {
		t.Fatalf("expected %d messages, got %d", len(ids), len(messages))
	}
	for i, id := range ids {
		if messages[i].ID != id {
			t.Fatalf("message order diverged at %d: want %s, got %s (created_at %q)", i, id, messages[i].ID, messages[i].CreatedAt)
		}
	}
}

// Callers hand timestamps to compare-and-swap predicates in whatever RFC3339
// form they produced, while writers store the canonical fixed-width form. A CAS
// predicate must compare equal instants as equal instead of reporting a bogus
// conflict because the two texts differ.
func TestScheduleLeaseCASAcceptsEquivalentTimestampFormats(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "CAS", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	schedule, err := store.CreateSchedule(ctx, Schedule{
		Name: "Nightly", AgentID: agent.ID, Expression: "0 3 * * *", Timezone: "UTC",
		Prompt: "run", PermissionMode: "acceptEdits", Enabled: true,
		NextRunAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}

	// A whole-second lease is exactly where RFC3339Nano and the canonical layout
	// disagree textually.
	leaseUntil := now.Add(time.Minute).Format(time.RFC3339Nano)
	claimed, err := store.ClaimDueSchedules(ctx, now.Format(time.RFC3339Nano), leaseUntil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected one claimed schedule, got %d", len(claimed))
	}
	run, err := store.CreateRun(ctx, Run{AgentID: agent.ID, Status: "completed", Source: "schedule", SourceID: schedule.ID, PermissionModeCap: "acceptEdits"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordScheduleRun(ctx, schedule.ID, leaseUntil, run.ID, "success", "", ""); err != nil {
		t.Fatalf("lease CAS rejected an equivalent timestamp format: %v", err)
	}

	// The same must hold for the updated_at CAS used by delete.
	current, err := store.GetSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, current.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSchedule(ctx, schedule.ID, parsed.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("updated_at CAS rejected an equivalent timestamp format: %v", err)
	}
}

func TestMigrationV52NormalizesLegacyWholeSecondTimestamps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v51.db")
	seed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := seed.CreateProject(ctx, "Legacy", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	whole, err := seed.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "whole"})
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	sub, err := seed.AddMessage(ctx, Message{AgentID: agent.ID, Role: "user", ContentText: "sub"})
	if err != nil {
		seed.Close()
		t.Fatal(err)
	}
	// Rewrite both rows into the legacy time.RFC3339Nano shapes: the
	// whole-second value carries no fractional digits, the other keeps its
	// stored precision. Then rewind the version so v52 runs on re-open.
	if _, err := seed.DB().ExecContext(ctx, `UPDATE agent_messages SET created_at = '2026-07-25T06:00:00Z' WHERE id = ?`, whole.ID); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if _, err := seed.DB().ExecContext(ctx, `UPDATE agent_messages SET created_at = '2026-07-25T06:00:00.5Z' WHERE id = ?`, sub.ID); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if _, err := seed.DB().ExecContext(ctx, `PRAGMA user_version = 51`); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected version %d, got %d", CurrentDBVersion, version)
	}

	var storedWhole, storedSub string
	if err := store.DB().QueryRowContext(ctx, `SELECT created_at FROM agent_messages WHERE id = ?`, whole.ID).Scan(&storedWhole); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT created_at FROM agent_messages WHERE id = ?`, sub.ID).Scan(&storedSub); err != nil {
		t.Fatal(err)
	}
	if storedWhole != "2026-07-25T06:00:00.000000000Z" {
		t.Fatalf("expected the whole-second timestamp to be padded, got %q", storedWhole)
	}
	if storedSub != "2026-07-25T06:00:00.5Z" {
		t.Fatalf("expected rows with a fractional second to keep their stored text, got %q", storedSub)
	}
	if !(storedWhole < storedSub) {
		t.Fatalf("normalized legacy timestamps must sort chronologically: whole=%q sub=%q", storedWhole, storedSub)
	}
	if _, err := time.Parse(time.RFC3339Nano, storedWhole); err != nil {
		t.Fatalf("normalized timestamp %q is not RFC3339: %v", storedWhole, err)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID != whole.ID || messages[1].ID != sub.ID {
		t.Fatalf("expected chronological order after migration, got %+v", messages)
	}
}
