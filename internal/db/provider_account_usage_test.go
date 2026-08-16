package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListProviderAccountUsageAggregatesTotalAndRecentWindows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	insert := func(id, provider, credentialID, createdAt string, input, output int64, cost float64) {
		t.Helper()
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO api_requests (id, provider, credential_id, input_tokens, output_tokens, cost_usd, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, provider, credentialID, input, output, cost, createdAt); err != nil {
			t.Fatalf("insert request %s: %v", id, err)
		}
	}

	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	insert("a-old", "codex", "account-a", "2026-07-09T11:59:59Z", 100, 10, 1.25)
	insert("a-week", "codex", "account-a", "2026-07-12T12:00:00Z", 200, 20, 2.50)
	insert("a-hours", "codex", "account-a", "2026-07-18T09:00:00Z", 300, 30, 0)
	insert("b-hours", "codex", "account-b", "2026-07-18T10:00:00Z", 400, 40, 4.00)
	insert("other-provider", "openai", "account-a", "2026-07-18T10:00:00Z", 999, 999, 99)
	insert("unattributed", "codex", "", "2026-07-18T10:00:00Z", 999, 999, 99)

	usage, err := store.ListProviderAccountUsage(ctx, "codex", []string{"account-a", "account-b", "account-a", ""}, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 {
		t.Fatalf("unexpected account map: %+v", usage)
	}

	a := usage["account-a"]
	if a.Total.RequestCount != 3 || a.Total.InputTokens != 600 || a.Total.OutputTokens != 60 || a.Total.TotalTokens() != 660 || a.Total.CostUSD != 3.75 {
		t.Fatalf("unexpected account-a total: %+v", a.Total)
	}
	if a.Last5Hours.RequestCount != 1 || a.Last5Hours.InputTokens != 300 || a.Last5Hours.OutputTokens != 30 || a.Last5Hours.CostUSD != 0 {
		t.Fatalf("unexpected account-a 5h usage: %+v", a.Last5Hours)
	}
	if a.Last7Days.RequestCount != 2 || a.Last7Days.InputTokens != 500 || a.Last7Days.OutputTokens != 50 || a.Last7Days.CostUSD != 2.5 {
		t.Fatalf("unexpected account-a 7d usage: %+v", a.Last7Days)
	}

	b := usage["account-b"]
	if b.Total.RequestCount != 1 || b.Total.TotalTokens() != 440 || b.Total.CostUSD != 4 || b.Last5Hours.RequestCount != 1 || b.Last7Days.RequestCount != 1 {
		t.Fatalf("unexpected account-b usage: %+v", b)
	}

	empty, err := store.ListProviderAccountUsage(ctx, "codex", nil, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty usage map, got %+v", empty)
	}
}

func TestListProviderAccountUsageUsesTimezoneAwareCreatedAtComparisons(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "timezone-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.DB().ExecContext(ctx, `INSERT INTO api_requests (id, provider, credential_id, input_tokens, output_tokens, cost_usd, created_at) VALUES ('offset', 'codex', 'account-a', 1, 2, 0.5, '2026-07-18T06:30:00-05:00')`); err != nil {
		t.Fatal(err)
	}
	usage, err := store.ListProviderAccountUsage(ctx, "codex", []string{"account-a"}, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatal(err)
	}
	if usage["account-a"].Last5Hours.RequestCount != 1 {
		t.Fatalf("offset timestamp should be inside the 5h window: %+v", usage["account-a"])
	}
}

func TestListProviderAccountUsageAlignsWindowsToCustomResetStarts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "aligned-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO api_requests (id, provider, credential_id, input_tokens, output_tokens, cost_usd, created_at) VALUES
		('inside', 'codex', 'account-a', 10, 1, 0.5, '2026-07-18T10:00:00Z'),
		('outside', 'codex', 'account-a', 20, 2, 1.5, '2026-07-18T07:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	usage, err := store.ListProviderAccountUsage(ctx, "codex", []string{"account-a"}, now, map[string]ProviderAccountUsageWindowStarts{
		"account-a": {Last5Hours: now.Add(-3 * time.Hour), Last7Days: now.Add(-3 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := usage["account-a"]
	if got.Total.RequestCount != 2 || got.Last5Hours.RequestCount != 1 || got.Last7Days.RequestCount != 1 || got.Last5Hours.InputTokens != 10 {
		t.Fatalf("custom reset window was not applied: %+v", got)
	}
}
