package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
)

type usageHistoryTestFixture struct {
	app       *Server
	store     *db.Store
	agentID   string
	agentName string
	messageID string
}

type usageHistoryTestRequest struct {
	ID                string
	CreatedAt         string
	Kind              string
	Provider          string
	Model             string
	InputTokens       any
	OutputTokens      any
	ReasoningTokens   any
	CachedInputTokens any
	TTFTMS            any
	DurationMS        any
	CostUSD           any
	ErrorMessage      string
}

func TestUsageHistorySummaryItemsOptionsAndSensitiveFields(t *testing.T) {
	fixture := newUsageHistoryTestFixture(t)
	status, encoded := requestUsageHistory(t, fixture, "/api/usage/history")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, encoded)
	}
	var response usageHistoryResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if response.GeneratedAt == "" {
		t.Fatal("expected generatedAt")
	}
	if response.Summary.RequestCount != 6 || response.Summary.InputTokens != 151 || response.Summary.OutputTokens != 61 || response.Summary.TotalTokens != 212 {
		t.Fatalf("unexpected summary token counts: %+v", response.Summary)
	}
	if response.Summary.ReasoningTokens != 12 || response.Summary.CachedInputTokens != 15 || response.Summary.Errors != 1 {
		t.Fatalf("unexpected summary details: %+v", response.Summary)
	}
	if !usageHistoryFloatEqual(response.Summary.TotalCostUSD, 0.16) || !usageHistoryFloatEqual(response.Summary.AverageTTFTMS, 275) || !usageHistoryFloatEqual(response.Summary.AverageDurationMS, 2750) || !usageHistoryFloatEqual(response.Summary.SuccessRate, 5.0/6.0) {
		t.Fatalf("unexpected summary rates: %+v", response.Summary)
	}
	wantOrder := []string{"request-f", "request-e", "request-d", "request-c", "request-b", "request-a"}
	if len(response.Items) != len(wantOrder) {
		t.Fatalf("expected %d items, got %+v", len(wantOrder), response.Items)
	}
	for index, id := range wantOrder {
		if response.Items[index].ID != id {
			t.Fatalf("unexpected item order at %d: want %s, got %+v", index, id, response.Items)
		}
	}
	if response.Items[0].AgentID != fixture.agentID || response.Items[0].AgentTitle != fixture.agentName || response.Items[0].TotalTokens != 2 || response.Items[0].Status != "success" {
		t.Fatalf("unexpected joined item: %+v", response.Items[0])
	}
	if response.Items[4].Status != "error" || response.Items[4].ErrorMessage != "boom" {
		t.Fatalf("unexpected failed item status: %+v", response.Items[4])
	}
	if strings.Join(response.Options.Providers, ",") != "anthropic,openai" || strings.Join(response.Options.Kinds, ",") != "embeddings,model" || strings.Join(response.Options.Models, ",") != "claude,gpt-a,gpt-b,no-timing" {
		t.Fatalf("unexpected stable options: %+v", response.Options)
	}

	body := string(encoded)
	for _, sensitive := range []string{"credential_id", "raw_dump_json", "credential-secret", "raw-secret", "request-body-secret"} {
		if strings.Contains(body, sensitive) {
			t.Fatalf("response leaked sensitive value %q: %s", sensitive, body)
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	items, ok := raw["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("unexpected raw items: %#v", raw["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected raw item: %#v", items[0])
	}
	for _, forbidden := range []string{"credentialId", "credential_id", "rawDumpJson", "raw_dump_json", "request", "requestContent"} {
		if _, exists := first[forbidden]; exists {
			t.Fatalf("item exposed forbidden field %q: %#v", forbidden, first)
		}
	}
}

func TestUsageHistoryFiltersDatesMissingAveragesAndProviderModels(t *testing.T) {
	fixture := newUsageHistoryTestFixture(t)
	status, encoded := requestUsageHistory(t, fixture, "/api/usage/history?from=2024-02-01&to=2024-02-28&provider=openai")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, encoded)
	}
	var response usageHistoryResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.RequestCount != 2 || response.Summary.InputTokens != 50 || response.Summary.OutputTokens != 10 || response.Summary.Errors != 1 {
		t.Fatalf("date/provider boundary filter failed: %+v", response.Summary)
	}
	if len(response.Items) != 2 || response.Items[0].ID != "request-c" || response.Items[1].ID != "request-b" {
		t.Fatalf("unexpected filtered items: %+v", response.Items)
	}
	if strings.Join(response.Options.Models, ",") != "gpt-a,gpt-b,no-timing" {
		t.Fatalf("selected provider should restrict model options: %+v", response.Options)
	}

	status, encoded = requestUsageHistory(t, fixture, "/api/usage/history?provider=openai&model=gpt-a&kind=model&from=2024-02-01&to=2024-02-28")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, encoded)
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.RequestCount != 1 || len(response.Items) != 1 || response.Items[0].ID != "request-b" {
		t.Fatalf("provider/model/kind filter failed: %+v", response)
	}

	status, encoded = requestUsageHistory(t, fixture, "/api/usage/history?provider=openai&model=no-timing")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, encoded)
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.RequestCount != 1 || response.Summary.AverageTTFTMS != 0 || response.Summary.AverageDurationMS != 0 {
		t.Fatalf("null timing values must be excluded from averages: %+v", response.Summary)
	}

	status, encoded = requestUsageHistory(t, fixture, "/api/usage/history?to=9999-12-31")
	if status != http.StatusOK {
		t.Fatalf("maximum valid calendar date should be accepted, got %d: %s", status, encoded)
	}
}

func TestUsageHistoryTrendHourDayAndMonth(t *testing.T) {
	fixture := newUsageHistoryTestFixture(t)
	tests := []struct {
		name      string
		target    string
		buckets   []string
		counts    []int64
		truncated bool
	}{
		{name: "hour", target: "/api/usage/history?bucket=hour&from=2024-02-01&to=2024-02-01", buckets: []string{"2024-02-01T00:00:00Z", "2024-02-01T01:00:00Z"}, counts: []int64{1, 1}},
		{name: "day", target: "/api/usage/history?bucket=day", buckets: []string{"2024-01-31", "2024-02-01", "2024-02-28", "2024-03-01"}, counts: []int64{1, 2, 1, 2}},
		{name: "month", target: "/api/usage/history?bucket=month", buckets: []string{"2024-01", "2024-02", "2024-03"}, counts: []int64{1, 3, 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, encoded := requestUsageHistory(t, fixture, test.target)
			if status != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", status, encoded)
			}
			var response usageHistoryResponse
			if err := json.Unmarshal(encoded, &response); err != nil {
				t.Fatal(err)
			}
			if response.TrendTruncated != test.truncated || len(response.Trend) != len(test.buckets) {
				t.Fatalf("unexpected trend shape: %+v", response)
			}
			for index, bucket := range test.buckets {
				point := response.Trend[index]
				if point.Bucket != bucket || point.RequestCount != test.counts[index] || point.TotalTokens != point.InputTokens+point.OutputTokens {
					t.Fatalf("unexpected trend point %d: %+v", index, point)
				}
			}
		})
	}
}

// created_at is stored in UTC, so a viewer east or west of UTC needs the day
// grouping shifted or their late-evening and early-morning work lands on the
// neighbouring calendar day. The fixture straddles three UTC midnights, so each
// offset regroups it differently.
func TestUsageHistoryTrendDayBucketsHonorTimezoneOffset(t *testing.T) {
	fixture := newUsageHistoryTestFixture(t)
	tests := []struct {
		name    string
		target  string
		buckets []string
		counts  []int64
	}{
		{
			name:    "utc",
			target:  "/api/usage/history?bucket=day&tzOffset=0",
			buckets: []string{"2024-01-31", "2024-02-01", "2024-02-28", "2024-03-01"},
			counts:  []int64{1, 2, 1, 2},
		},
		{
			// UTC+8 pulls 2024-01-31T23:30Z forward into 2024-02-01.
			name:    "east",
			target:  "/api/usage/history?bucket=day&tzOffset=480",
			buckets: []string{"2024-02-01", "2024-02-28", "2024-03-01"},
			counts:  []int64{3, 1, 2},
		},
		{
			// UTC-8 pushes both 2024-02-01 rows back to 2024-01-31 and lands the
			// two 2024-03-01T00:00Z rows on the leap day.
			name:    "west",
			target:  "/api/usage/history?bucket=day&tzOffset=-480",
			buckets: []string{"2024-01-31", "2024-02-28", "2024-02-29"},
			counts:  []int64{3, 1, 2},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, encoded := requestUsageHistory(t, fixture, test.target)
			if status != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", status, encoded)
			}
			var response usageHistoryResponse
			if err := json.Unmarshal(encoded, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Trend) != len(test.buckets) {
				t.Fatalf("expected %d buckets, got %+v", len(test.buckets), response.Trend)
			}
			for index, bucket := range test.buckets {
				point := response.Trend[index]
				if point.Bucket != bucket || point.RequestCount != test.counts[index] {
					t.Fatalf("bucket %d: want %s=%d, got %s=%d", index, bucket, test.counts[index], point.Bucket, point.RequestCount)
				}
			}
		})
	}
}

// A zero offset must keep emitting the original expression so the existing
// callers keep generating byte-identical SQL.
func TestUsageHistoryBucketExpressionOmitsZeroOffsetModifier(t *testing.T) {
	expression, err := usageHistoryBucketExpression("day", 0)
	if err != nil || expression != `strftime('%Y-%m-%d', created_at)` {
		t.Fatalf("unexpected zero-offset expression %q (err %v)", expression, err)
	}
	expression, err = usageHistoryBucketExpression("day", 480)
	if err != nil || expression != `strftime('%Y-%m-%d', created_at, '+480 minutes')` {
		t.Fatalf("unexpected east expression %q (err %v)", expression, err)
	}
	expression, err = usageHistoryBucketExpression("hour", -300)
	if err != nil || expression != `strftime('%Y-%m-%dT%H:00:00Z', created_at, '-300 minutes')` {
		t.Fatalf("unexpected west expression %q (err %v)", expression, err)
	}
	if _, err := usageHistoryBucketExpression("week", 0); err == nil {
		t.Fatal("expected an invalid bucket error")
	}
}

func TestUsageHistoryTrendTruncationKeepsNewestBuckets(t *testing.T) {
	fixture := newUsageHistoryTestFixture(t)
	points, truncated, err := queryUsageHistoryTrendWithLimit(context.Background(), fixture.store.DB(), usageHistoryFilters{
		Bucket:     "month",
		SnapshotAt: db.Now(),
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(points) != 2 || points[0].Bucket != "2024-02" || points[1].Bucket != "2024-03" {
		t.Fatalf("expected newest truncated buckets in ascending order, got truncated=%v points=%+v", truncated, points)
	}
}

func TestUsageHistoryCursorPaginationSnapshotAndFilterBinding(t *testing.T) {
	fixture := newUsageHistoryTestFixture(t)
	status, encoded := requestUsageHistory(t, fixture, "/api/usage/history?limit=2")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, encoded)
	}
	var first usageHistoryResponse
	if err := json.Unmarshal(encoded, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "request-f" || first.Items[1].ID != "request-e" || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	insertUsageHistoryTestRequest(t, fixture, usageHistoryTestRequest{
		ID: "request-after-snapshot", CreatedAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), Kind: "model", Provider: "openai", Model: "gpt-a",
		InputTokens: 1, OutputTokens: 1, ReasoningTokens: 0, CachedInputTokens: 0, TTFTMS: 1, DurationMS: 1, CostUSD: 0,
	})

	allIDs := []string{first.Items[0].ID, first.Items[1].ID}
	cursor := first.NextCursor
	for cursor != "" {
		target := "/api/usage/history?limit=2&cursor=" + url.QueryEscape(cursor)
		status, encoded = requestUsageHistory(t, fixture, target)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", status, encoded)
		}
		var page usageHistoryResponse
		if err := json.Unmarshal(encoded, &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			allIDs = append(allIDs, item.ID)
		}
		cursor = page.NextCursor
	}
	if strings.Join(allIDs, ",") != "request-f,request-e,request-d,request-c,request-b,request-a" {
		t.Fatalf("cursor pagination repeated, skipped, or crossed the snapshot: %v", allIDs)
	}

	status, encoded = requestUsageHistory(t, fixture, "/api/usage/history?provider=openai&limit=2")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, encoded)
	}
	var filtered usageHistoryResponse
	if err := json.Unmarshal(encoded, &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.NextCursor == "" {
		t.Fatal("expected provider-filtered cursor")
	}
	status, _ = requestUsageHistory(t, fixture, "/api/usage/history?provider=anthropic&limit=2&cursor="+url.QueryEscape(filtered.NextCursor))
	if status != http.StatusBadRequest {
		t.Fatalf("expected cross-filter cursor to return 400, got %d", status)
	}
}

func TestUsageHistoryCursorRejectsInvalidCreatedAt(t *testing.T) {
	encoded, err := encodeUsageHistoryCursor(usageHistoryCursor{
		Version:         1,
		SnapshotAt:      db.Now(),
		CreatedAt:       "not-a-timestamp",
		ID:              "request-id",
		FilterSignature: strings.Repeat("0", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeUsageHistoryCursor(encoded); err == nil {
		t.Fatal("expected invalid createdAt to reject the cursor")
	}
}

func TestUsageHistoryRejectsInvalidParameters(t *testing.T) {
	fixture := newUsageHistoryTestFixture(t)
	for _, target := range []string{
		"/api/usage/history?bucket=week",
		"/api/usage/history?from=2024-02-30",
		"/api/usage/history?to=02-01-2024",
		"/api/usage/history?from=2024-03-01&to=2024-02-01",
		"/api/usage/history?limit=",
		"/api/usage/history?limit=0",
		"/api/usage/history?limit=101",
		"/api/usage/history?limit=nope",
		"/api/usage/history?tzOffset=841",
		"/api/usage/history?tzOffset=-841",
		"/api/usage/history?tzOffset=nope",
		"/api/usage/history?cursor=",
		"/api/usage/history?cursor=not-a-valid-cursor",
		"/api/usage/history?unknown=value",
	} {
		t.Run(target, func(t *testing.T) {
			status, encoded := requestUsageHistory(t, fixture, target)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", status, encoded)
			}
		})
	}
}

func newUsageHistoryTestFixture(t *testing.T) usageHistoryTestFixture {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "usage-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, _, agent, err := store.CreateProject(ctx, "Usage history", "", t.TempDir(), "openai:gpt-a", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "request-body-secret"})
	if err != nil {
		t.Fatal(err)
	}
	fixture := usageHistoryTestFixture{
		app:       New(config.Config{}, store, nil, nil),
		store:     store,
		agentID:   agent.ID,
		agentName: agent.Title,
		messageID: message.ID,
	}
	for _, request := range []usageHistoryTestRequest{
		{ID: "request-a", CreatedAt: "2024-01-31T23:30:00Z", Kind: "model", Provider: "openai", Model: "gpt-a", InputTokens: 10, OutputTokens: 5, ReasoningTokens: 1, CachedInputTokens: 2, TTFTMS: 100, DurationMS: 1000, CostUSD: 0.01},
		{ID: "request-b", CreatedAt: "2024-02-01T00:15:00Z", Kind: "model", Provider: "openai", Model: "gpt-a", InputTokens: 20, OutputTokens: 10, ReasoningTokens: 2, CachedInputTokens: 3, TTFTMS: nil, DurationMS: 0, CostUSD: 0.02, ErrorMessage: "boom"},
		{ID: "request-c", CreatedAt: "2024-02-01T01:45:00Z", Kind: "embeddings", Provider: "openai", Model: "gpt-b", InputTokens: 30, OutputTokens: 0, ReasoningTokens: 0, CachedInputTokens: 4, TTFTMS: 300, DurationMS: 3000, CostUSD: 0.03},
		{ID: "request-d", CreatedAt: "2024-02-28T12:00:00Z", Kind: "model", Provider: "anthropic", Model: "claude", InputTokens: 40, OutputTokens: 20, ReasoningTokens: 4, CachedInputTokens: 0, TTFTMS: 500, DurationMS: 5000, CostUSD: 0.04},
		{ID: "request-e", CreatedAt: "2024-03-01T00:00:00Z", Kind: "model", Provider: "openai", Model: "no-timing", InputTokens: 50, OutputTokens: 25, ReasoningTokens: 5, CachedInputTokens: 6, TTFTMS: nil, DurationMS: nil, CostUSD: 0.05},
		{ID: "request-f", CreatedAt: "2024-03-01T00:00:00Z", Kind: "model", Provider: "openai", Model: "gpt-a", InputTokens: 1, OutputTokens: 1, ReasoningTokens: 0, CachedInputTokens: 0, TTFTMS: 200, DurationMS: 2000, CostUSD: 0.01},
	} {
		insertUsageHistoryTestRequest(t, fixture, request)
	}
	return fixture
}

func insertUsageHistoryTestRequest(t *testing.T, fixture usageHistoryTestFixture, request usageHistoryTestRequest) {
	t.Helper()
	_, err := fixture.store.DB().ExecContext(context.Background(), `
INSERT INTO api_requests (
	id, agent_id, message_id, kind, provider, credential_id, model,
	input_tokens, output_tokens, cached_input_tokens, reasoning_tokens,
	ttft_ms, duration_ms, cost_usd, error_message, raw_dump_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
		request.ID,
		fixture.agentID,
		fixture.messageID,
		request.Kind,
		request.Provider,
		"credential-secret",
		request.Model,
		request.InputTokens,
		request.OutputTokens,
		request.CachedInputTokens,
		request.ReasoningTokens,
		request.TTFTMS,
		request.DurationMS,
		request.CostUSD,
		request.ErrorMessage,
		`{"request":"raw-secret","content":"request-body-secret"}`,
		request.CreatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func requestUsageHistory(t *testing.T, fixture usageHistoryTestFixture, target string) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, target, nil)
	fixture.app.Routes().ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func usageHistoryFloatEqual(left, right float64) bool {
	return math.Abs(left-right) < 0.0000001
}
