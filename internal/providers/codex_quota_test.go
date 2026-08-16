package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/config"
)

type recordingAccountQuotaStore struct {
	recordingAccountTelemetry
	mu        sync.Mutex
	existing  json.RawMessage
	snapshots []codexauth.QuotaSnapshot
}

func (r *recordingAccountQuotaStore) UpdateProviderAccountQuota(_ context.Context, _, _ string, quota any, _ time.Time) error {
	encoded, err := json.Marshal(quota)
	if err != nil {
		return err
	}
	var snapshot codexauth.QuotaSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, snapshot)
	r.existing = encoded
	return nil
}

func (r *recordingAccountQuotaStore) CurrentProviderAccountQuota(context.Context, string, string) (json.RawMessage, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.existing, time.Time{}, nil
}

func TestParseCodexRateLimitHeadersMapsPrimarySevenDayAndSecondaryFiveHour(t *testing.T) {
	header := make(http.Header)
	header.Set("x-codex-primary-used-percent", "100")
	header.Set("x-codex-primary-reset-after-seconds", "384607")
	header.Set("x-codex-primary-window-minutes", "10080")
	header.Set("x-codex-secondary-used-percent", "3")
	header.Set("x-codex-secondary-reset-after-seconds", "17369")
	header.Set("x-codex-secondary-window-minutes", "300")

	now := time.Date(2026, 8, 16, 6, 0, 0, 0, time.UTC)
	snapshot := parseCodexRateLimitHeaders(header, now)
	if snapshot == nil || snapshot.PrimaryWindow == nil || snapshot.SecondaryWindow == nil {
		t.Fatalf("expected both windows, got %+v", snapshot)
	}
	if snapshot.PrimaryWindow.UsedPercent != 100 || snapshot.PrimaryWindow.LimitWindowSeconds != 604800 {
		t.Fatalf("primary window: %+v", snapshot.PrimaryWindow)
	}
	if snapshot.SecondaryWindow.UsedPercent != 3 || snapshot.SecondaryWindow.LimitWindowSeconds != 18000 {
		t.Fatalf("secondary window: %+v", snapshot.SecondaryWindow)
	}
	fiveHour, sevenDay := classifyCodexQuotaWindows(snapshot)
	if fiveHour != snapshot.SecondaryWindow || sevenDay != snapshot.PrimaryWindow {
		t.Fatalf("window classification swapped: five=%+v seven=%+v", fiveHour, sevenDay)
	}
}

func TestParseCodexRateLimitHeadersAccepts429AndFillsLegacyWindows(t *testing.T) {
	header := make(http.Header)
	header.Set("x-codex-primary-used-percent", "12.5")
	header.Set("x-codex-secondary-used-percent", "40")
	snapshot := parseCodexRateLimitHeaders(header, time.Unix(1, 0).UTC())
	if snapshot == nil || snapshot.PrimaryWindow.LimitWindowSeconds != 604800 || snapshot.SecondaryWindow.LimitWindowSeconds != 18000 {
		t.Fatalf("legacy header windows were not stamped: %+v", snapshot)
	}
}

func TestMergeCodexQuotaSnapshotKeepsPlanAndCredits(t *testing.T) {
	existing := codexauth.QuotaSnapshot{
		PlanType:              "plus",
		Credits:               &codexauth.CreditBalance{HasCredits: true, Balance: 9},
		RateLimitResetCredits: &codexauth.RateLimitResetCredits{AvailableCount: 2},
		PrimaryWindow:         &codexauth.RateLimitWindow{UsedPercent: 1, LimitWindowSeconds: 18000},
	}
	incoming := codexauth.QuotaSnapshot{
		PrimaryWindow:   &codexauth.RateLimitWindow{UsedPercent: 80, LimitWindowSeconds: 18000},
		SecondaryWindow: &codexauth.RateLimitWindow{UsedPercent: 10, LimitWindowSeconds: 604800},
		FetchedAt:       "2026-08-16T06:00:00Z",
	}
	merged := mergeCodexQuotaSnapshot(existing, incoming)
	if merged.PlanType != "plus" || merged.Credits == nil || merged.Credits.Balance != 9 || merged.RateLimitResetCredits.AvailableCount != 2 {
		t.Fatalf("header merge dropped account metadata: %+v", merged)
	}
	if merged.PrimaryWindow.UsedPercent != 80 || merged.SecondaryWindow.UsedPercent != 10 || merged.FetchedAt != incoming.FetchedAt {
		t.Fatalf("header merge did not replace windows: %+v", merged)
	}
}

func TestCodexLocalUsageWindowStartsUsesResetAt(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	quota := &codexauth.QuotaSnapshot{
		PrimaryWindow:   &codexauth.RateLimitWindow{UsedPercent: 20, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Format(time.RFC3339)},
		SecondaryWindow: &codexauth.RateLimitWindow{UsedPercent: 40, LimitWindowSeconds: 604800, ResetAt: now.Add(3 * 24 * time.Hour).Format(time.RFC3339)},
	}
	last5, last7 := CodexLocalUsageWindowStarts(quota, now)
	if !last5.Equal(now.Add(-3 * time.Hour)) {
		t.Fatalf("5h start = %s, want %s", last5, now.Add(-3*time.Hour))
	}
	if !last7.Equal(now.Add(-4 * 24 * time.Hour)) {
		t.Fatalf("7d start = %s, want %s", last7, now.Add(-4*24*time.Hour))
	}
}

func TestCodexLocalUsageWindowStartsUsesResetAfterSeconds(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	fetchedAt := now.Add(-10 * time.Minute)
	quota := &codexauth.QuotaSnapshot{
		FetchedAt:       fetchedAt.Format(time.RFC3339Nano),
		PrimaryWindow:   &codexauth.RateLimitWindow{UsedPercent: 20, LimitWindowSeconds: 18000, ResetAfterSeconds: int64((2*time.Hour + 10*time.Minute) / time.Second)},
		SecondaryWindow: &codexauth.RateLimitWindow{UsedPercent: 40, LimitWindowSeconds: 604800, ResetAfterSeconds: int64((3*24*time.Hour + 10*time.Minute) / time.Second)},
	}
	last5, last7 := CodexLocalUsageWindowStarts(quota, now)
	if !last5.Equal(now.Add(-3 * time.Hour)) {
		t.Fatalf("5h start = %s, want %s", last5, now.Add(-3*time.Hour))
	}
	if !last7.Equal(now.Add(-4 * 24 * time.Hour)) {
		t.Fatalf("7d start = %s, want %s", last7, now.Add(-4*24*time.Hour))
	}
}

func TestCodexProviderGenerateRecordsQuotaFromResponseHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-test"}]}`))
			return
		}
		w.Header().Set("x-codex-primary-used-percent", "88")
		w.Header().Set("x-codex-primary-window-minutes", "10080")
		w.Header().Set("x-codex-secondary-used-percent", "12")
		w.Header().Set("x-codex-secondary-window-minutes", "300")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "headers.json", Content: []byte(`{"type":"codex","access_token":"header-access","account_id":"header-account","plan_type":"plus"}`)}}); err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingAccountQuotaStore{
		existing: json.RawMessage(`{"plan_type":"plus","rate_limit_reset_credits":{"available_count":1}}`),
	}
	provider := NewCodexProvider(config.ProviderConfig{
		Name:                           "codex",
		Type:                           config.ProviderTypeCodex,
		BaseURL:                        upstream.URL,
		Model:                          "gpt-test",
		CredentialStorePath:            storeDir,
		CodexAllowInsecureTestEndpoint: true,
	})
	provider.SetAccountTelemetry(telemetry)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error: %s", event.Text)
		}
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.snapshots) != 1 {
		t.Fatalf("expected one quota snapshot, got %+v", telemetry.snapshots)
	}
	got := telemetry.snapshots[0]
	if got.PlanType != "plus" || got.RateLimitResetCredits == nil || got.RateLimitResetCredits.AvailableCount != 1 {
		t.Fatalf("header snapshot dropped existing quota metadata: %+v", got)
	}
	if got.PrimaryWindow == nil || got.PrimaryWindow.UsedPercent != 88 || got.SecondaryWindow == nil || got.SecondaryWindow.UsedPercent != 12 {
		t.Fatalf("header snapshot windows: %+v", got)
	}
}

func TestCodexProviderGenerateRecordsQuotaFrom429Headers(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-test"}]}`))
			return
		}
		w.Header().Set("x-codex-primary-used-percent", "100")
		w.Header().Set("x-codex-primary-window-minutes", "10080")
		w.Header().Set("x-codex-secondary-used-percent", "100")
		w.Header().Set("x-codex-secondary-window-minutes", "300")
		http.Error(w, `{"error":{"code":"rate_limit_exceeded"}}`, http.StatusTooManyRequests)
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "limited.json", Content: []byte(`{"type":"codex","access_token":"limited-access","account_id":"limited-account"}`)}}); err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingAccountQuotaStore{}
	provider := NewCodexProvider(config.ProviderConfig{
		Name:                           "codex",
		Type:                           config.ProviderTypeCodex,
		BaseURL:                        upstream.URL,
		Model:                          "gpt-test",
		CredentialStorePath:            storeDir,
		CodexAllowInsecureTestEndpoint: true,
	})
	provider.SetAccountTelemetry(telemetry)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.snapshots) != 1 || telemetry.snapshots[0].PrimaryWindow == nil || telemetry.snapshots[0].PrimaryWindow.UsedPercent != 100 || telemetry.snapshots[0].SecondaryWindow == nil || telemetry.snapshots[0].SecondaryWindow.UsedPercent != 100 {
		t.Fatalf("429 headers were not recorded: %+v", telemetry.snapshots)
	}
}

func TestCodexProviderSyncAccountLoadsResetCredits(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000}}}`))
		case "/backend-api/wham/rate-limit-reset-credits":
			_, _ = w.Write([]byte(`{"available_count":2,"credits":[{"expires_at":"2099-01-01T00:00:00Z"},{"expires_at":"2000-01-01T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "credits.json", Content: []byte(`{"type":"codex","access_token":"credit-access","account_id":"credit-account"}`)}}); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("account setup failed: %+v err=%v", accounts, err)
	}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL + "/backend-api/codex", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	_, quota, err := provider.SyncAccount(context.Background(), accounts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if quota.RateLimitResetCredits == nil || quota.RateLimitResetCredits.AvailableCount != 1 || len(quota.RateLimitResetCredits.Credits) != 1 {
		t.Fatalf("expired reset credits were not filtered: %+v", quota.RateLimitResetCredits)
	}
}

func TestCodexProviderConsumeRateLimitResetCredit(t *testing.T) {
	var consumed bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/backend-api/wham/rate-limit-reset-credits/consume" && r.Method == http.MethodPost:
			if r.Header.Get("OpenAI-Beta") != "codex-1" {
				t.Fatalf("consume missing quota beta header")
			}
			consumed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "reset.json", Content: []byte(`{"type":"codex","access_token":"reset-access","account_id":"reset-account"}`)}}); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("account setup failed: %+v err=%v", accounts, err)
	}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL + "/backend-api/codex", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	if err := provider.ConsumeRateLimitResetCredit(context.Background(), accounts[0].ID); err != nil {
		t.Fatal(err)
	}
	if !consumed {
		t.Fatal("reset credit was not consumed")
	}
}

func TestCodexWhamResetCreditURLsFollowUsagePath(t *testing.T) {
	provider := NewCodexProvider(config.ProviderConfig{BaseURL: "http://127.0.0.1:1234/prefix/backend-api/codex", CodexAllowInsecureTestEndpoint: true})
	if got := provider.resetCreditsURL(); got != "http://127.0.0.1:1234/prefix/backend-api/wham/rate-limit-reset-credits" {
		t.Fatalf("unexpected reset credits URL: %s", got)
	}
	if got := provider.resetCreditsConsumeURL(); got != "http://127.0.0.1:1234/prefix/backend-api/wham/rate-limit-reset-credits/consume" {
		t.Fatalf("unexpected consume URL: %s", got)
	}
}
