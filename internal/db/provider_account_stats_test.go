package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"autoto/internal/providers"
)

func TestProviderAccountStatsAggregateAtomicallyAndStoreSafeQuota(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const attempts = 24
	var wg sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := store.RecordProviderAccountAttempt(context.Background(), providers.ProviderAccountAttempt{
				Provider: "codex", AccountID: "codex_fixture", Success: index%3 != 0,
				HTTPStatus: 200, StatusCode: "OK", ErrorCode: "fixture_error", AttemptedAt: time.Unix(int64(1000+index), 0),
			}); err != nil {
				t.Errorf("record attempt: %v", err)
			}
		}(index)
	}
	wg.Wait()
	quota := map[string]any{"plan_type": "plus", "primary_window": map[string]any{"used_percent": 25}}
	if err := store.UpdateProviderAccountQuota(context.Background(), "codex", "codex_fixture", quota, time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.GetProviderAccountStats(context.Background(), "codex", "codex_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if stats.SuccessCount != 16 || stats.FailureCount != 8 || stats.LastAttemptAt == "" || stats.LastUseAt == "" || stats.QuotaFetchedAt == "" {
		t.Fatalf("unexpected aggregate: %+v", stats)
	}
	newest := time.Unix(5000, 500_000_000).UTC()
	if err := store.RecordProviderAccountAttempt(context.Background(), providers.ProviderAccountAttempt{Provider: "codex", AccountID: "codex_fixture", Success: true, HTTPStatus: 201, StatusCode: "newest", AttemptedAt: newest}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProviderAccountAttempt(context.Background(), providers.ProviderAccountAttempt{Provider: "codex", AccountID: "codex_fixture", Success: false, HTTPStatus: 503, StatusCode: "stale", ErrorCode: "old_failure", AttemptedAt: time.Unix(5000, 0)}); err != nil {
		t.Fatal(err)
	}
	newestQuotaAt := time.Unix(6000, 250_000_000).UTC()
	if err := store.UpdateProviderAccountQuota(context.Background(), "codex", "codex_fixture", map[string]any{"plan_type": "pro"}, newestQuotaAt); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateProviderAccountQuota(context.Background(), "codex", "codex_fixture", map[string]any{"plan_type": "stale"}, time.Unix(6000, 0)); err != nil {
		t.Fatal(err)
	}
	stats, err = store.GetProviderAccountStats(context.Background(), "codex", "codex_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if stats.LastAttemptAt != newest.Format(time.RFC3339Nano) || stats.LastHTTPStatus != 201 || stats.LastStatusCode != "newest" || stats.LastErrorCode != "" || stats.SuccessCount != 17 || stats.FailureCount != 9 {
		t.Fatalf("stale event moved recent status backwards: %+v", stats)
	}
	if stats.QuotaFetchedAt != newestQuotaAt.Format(time.RFC3339Nano) {
		t.Fatalf("stale quota moved fetch time backwards: %+v", stats)
	}
	var stored map[string]any
	if err := json.Unmarshal(stats.QuotaSnapshotJSON, &stored); err != nil || stored["plan_type"] != "pro" {
		t.Fatalf("stale quota replaced recent snapshot: %s err=%v", stats.QuotaSnapshotJSON, err)
	}
	for _, quota := range []any{
		map[string]any{"access_token": "forbidden"},
		map[string]any{"nested": []any{map[string]any{"api-key": "forbidden"}}},
		map[string]any{"clientSecret": "forbidden"},
		map[string]any{"sessionToken": "forbidden"},
	} {
		if err := store.UpdateProviderAccountQuota(context.Background(), "codex", "codex_fixture", quota, time.Now()); err == nil {
			t.Fatalf("expected sensitive quota key to be rejected: %#v", quota)
		}
	}
	if err := store.DeleteProviderAccountStats(context.Background(), "codex", "codex_fixture"); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListProviderAccountStats(context.Background(), "codex")
	if err != nil || len(listed) != 0 {
		t.Fatalf("stats were not deleted: %+v err=%v", listed, err)
	}
}

func TestProviderAccountStatsPersistsQuotaUnauthorizedCode(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "quota-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	attemptedAt := time.Unix(8000, 123_000_000).UTC()
	if err := store.RecordProviderAccountAttempt(context.Background(), providers.ProviderAccountAttempt{
		Provider: "codex", AccountID: "quota-account", Success: false,
		HTTPStatus: 401, StatusCode: "quota_unauthorized", ErrorCode: "quota_unauthorized", AttemptedAt: attemptedAt,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := store.GetProviderAccountStats(context.Background(), "codex", "quota-account")
	if err != nil {
		t.Fatal(err)
	}
	if stats.LastErrorCode != "quota_unauthorized" || stats.LastStatusCode != "quota_unauthorized" || stats.LastHTTPStatus != 401 || stats.LastAttemptAt != attemptedAt.Format(time.RFC3339Nano) {
		t.Fatalf("quota unauthorized status was not persisted: %+v", stats)
	}
}

func TestProviderAccountStatsMigrationCompletesLegacyTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-stats.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `
CREATE TABLE provider_account_stats (
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  success_count INTEGER NOT NULL DEFAULT 0,
  failure_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (provider, account_id)
);
INSERT INTO provider_account_stats (provider, account_id, success_count, failure_count) VALUES ('codex', 'legacy-account', 3, 2);
PRAGMA user_version = 27;
`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, raw); err != nil {
		t.Fatal(err)
	}
	legacyTimestamp := time.Unix(7000, 0).UTC().Format(time.RFC3339Nano)
	if _, err := raw.ExecContext(ctx, `UPDATE provider_account_stats SET last_attempt_at = ?, last_http_status = 503, last_status_code = 'legacy' WHERE provider = 'codex' AND account_id = 'legacy-account'`, legacyTimestamp); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: raw}
	wireSubstores(store)
	if err := store.RecordProviderAccountAttempt(ctx, providers.ProviderAccountAttempt{
		Provider: "codex", AccountID: "legacy-account", Success: true, HTTPStatus: 200, StatusCode: "ok", AttemptedAt: time.Unix(7000, 500_000_000),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateProviderAccountQuota(ctx, "codex", "legacy-account", map[string]any{"plan_type": "plus"}, time.Unix(7100, 0)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.GetProviderAccountStats(ctx, "codex", "legacy-account")
	if err != nil {
		t.Fatal(err)
	}
	if stats.SuccessCount != 4 || stats.FailureCount != 2 || stats.LastStatusCode != "ok" || len(stats.QuotaSnapshotJSON) == 0 {
		t.Fatalf("legacy stats were not preserved and completed: %+v", stats)
	}
	var version int
	if err := raw.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != CurrentDBVersion {
		t.Fatalf("unexpected migrated version %d: %v", version, err)
	}
}
