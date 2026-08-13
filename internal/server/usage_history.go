package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/db"
)

const (
	usageHistoryDefaultLimit = 50
	usageHistoryMaxLimit     = 100
	usageHistoryMaxTrend     = 1000
	usageHistoryMaxProviders = 100
	usageHistoryMaxKinds     = 100
	usageHistoryMaxModels    = 500
	usageHistoryMaxFilterLen = 256
	usageHistoryMaxCursorLen = 4096
)

type usageHistoryResponse struct {
	GeneratedAt    string                   `json:"generatedAt"`
	Summary        usageHistoryMetrics      `json:"summary"`
	Trend          []usageHistoryTrendPoint `json:"trend"`
	TrendTruncated bool                     `json:"trendTruncated"`
	Options        usageHistoryOptions      `json:"options"`
	Items          []usageHistoryItem       `json:"items"`
	NextCursor     string                   `json:"nextCursor"`
}

type usageHistoryMetrics struct {
	RequestCount      int64   `json:"requestCount"`
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	TotalTokens       int64   `json:"totalTokens"`
	ReasoningTokens   int64   `json:"reasoningTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	TotalCostUSD      float64 `json:"totalCostUsd"`
	AverageTTFTMS     float64 `json:"averageTTFTMs"`
	AverageDurationMS float64 `json:"averageDurationMs"`
	Errors            int64   `json:"errors"`
	SuccessRate       float64 `json:"successRate"`
}

type usageHistoryTrendPoint struct {
	Bucket string `json:"bucket"`
	usageHistoryMetrics
}

type usageHistoryOptions struct {
	Providers []string `json:"providers"`
	Kinds     []string `json:"kinds"`
	Models    []string `json:"models"`
}

type usageHistoryItem struct {
	ID                string  `json:"id"`
	CreatedAt         string  `json:"createdAt"`
	AgentID           string  `json:"agentId"`
	AgentTitle        string  `json:"agentTitle"`
	RunID             string  `json:"runId"`
	MessageID         string  `json:"messageId"`
	Kind              string  `json:"kind"`
	Provider          string  `json:"provider"`
	Model             string  `json:"model"`
	InputTokens       int64   `json:"inputTokens"`
	OutputTokens      int64   `json:"outputTokens"`
	TotalTokens       int64   `json:"totalTokens"`
	ReasoningTokens   int64   `json:"reasoningTokens"`
	CachedInputTokens int64   `json:"cachedInputTokens"`
	TTFTMS            int64   `json:"ttftMs"`
	DurationMS        int64   `json:"durationMs"`
	CostUSD           float64 `json:"costUsd"`
	ErrorMessage      string  `json:"errorMessage"`
	Status            string  `json:"status"`
}

type usageHistoryFilters struct {
	Provider    string
	Model       string
	Kind        string
	FromDate    string
	ToDate      string
	From        string
	ToExclusive string
	Bucket      string
	TZOffset    int
	Limit       int
	SnapshotAt  string
	Cursor      *usageHistoryCursor
}

type usageHistoryCursor struct {
	Version         int    `json:"v"`
	SnapshotAt      string `json:"snapshotAt"`
	CreatedAt       string `json:"createdAt"`
	ID              string `json:"id"`
	FilterSignature string `json:"filterSignature"`
}

func (s *Server) usageHistory(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database store is not initialized")
		return
	}
	filters, err := parseUsageHistoryFilters(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	response, err := buildUsageHistory(r.Context(), s.store.ReadDB(), filters)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func parseUsageHistoryFilters(r *http.Request) (usageHistoryFilters, error) {
	if err := rejectUnknownQuery(r, "provider", "model", "kind", "from", "to", "bucket", "tzOffset", "limit", "cursor"); err != nil {
		return usageHistoryFilters{}, err
	}
	query := r.URL.Query()
	if values, present := query["limit"]; present && strings.TrimSpace(values[0]) == "" {
		return usageHistoryFilters{}, errors.New("invalid limit")
	}
	if values, present := query["cursor"]; present && strings.TrimSpace(values[0]) == "" {
		return usageHistoryFilters{}, errors.New("invalid cursor")
	}
	limit, err := queryInt(r, "limit", usageHistoryDefaultLimit, 1, usageHistoryMaxLimit)
	if err != nil {
		return usageHistoryFilters{}, err
	}
	filters := usageHistoryFilters{
		Provider: strings.TrimSpace(query.Get("provider")),
		Model:    strings.TrimSpace(query.Get("model")),
		Kind:     strings.TrimSpace(query.Get("kind")),
		FromDate: strings.TrimSpace(query.Get("from")),
		ToDate:   strings.TrimSpace(query.Get("to")),
		Bucket:   strings.TrimSpace(query.Get("bucket")),
		Limit:    limit,
	}
	if filters.Bucket == "" {
		filters.Bucket = "day"
	}
	// Bounds match the widest real UTC offsets (UTC-12:00 to UTC+14:00).
	tzOffset, err := queryInt(r, "tzOffset", 0, -840, 840)
	if err != nil {
		return usageHistoryFilters{}, err
	}
	filters.TZOffset = tzOffset
	if _, err := usageHistoryBucketExpression(filters.Bucket, filters.TZOffset); err != nil {
		return usageHistoryFilters{}, err
	}
	for name, value := range map[string]string{"provider": filters.Provider, "model": filters.Model, "kind": filters.Kind} {
		if err := validateUsageHistoryFilter(name, value); err != nil {
			return usageHistoryFilters{}, err
		}
	}
	if filters.FromDate != "" {
		from, err := parseUsageHistoryDate("from", filters.FromDate)
		if err != nil {
			return usageHistoryFilters{}, err
		}
		filters.From = from.Format(time.RFC3339)
	}
	if filters.ToDate != "" {
		to, err := parseUsageHistoryDate("to", filters.ToDate)
		if err != nil {
			return usageHistoryFilters{}, err
		}
		if to.Year() == 9999 {
			filters.ToExclusive = "9999-12-32"
		} else {
			filters.ToExclusive = to.AddDate(0, 0, 1).Format(time.RFC3339)
		}
	}
	if filters.FromDate != "" && filters.ToDate != "" && filters.FromDate > filters.ToDate {
		return usageHistoryFilters{}, errors.New("from date must not be after to date")
	}

	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor == "" {
		filters.SnapshotAt = db.Now()
		return filters, nil
	}
	cursor, err := decodeUsageHistoryCursor(rawCursor)
	if err != nil {
		return usageHistoryFilters{}, err
	}
	if cursor.FilterSignature != usageHistoryFilterSignature(filters) {
		return usageHistoryFilters{}, errors.New("cursor does not match the current filters")
	}
	filters.SnapshotAt = cursor.SnapshotAt
	filters.Cursor = &cursor
	return filters, nil
}

func validateUsageHistoryFilter(name, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > usageHistoryMaxFilterLen || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid %s", name)
	}
	return nil
}

func parseUsageHistoryDate(name, value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return time.Time{}, fmt.Errorf("invalid %s date", name)
	}
	return parsed.UTC(), nil
}

func usageHistoryFilterSignature(filters usageHistoryFilters) string {
	payload := strings.Join([]string{
		filters.Provider,
		filters.Model,
		filters.Kind,
		filters.FromDate,
		filters.ToDate,
		filters.Bucket,
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func decodeUsageHistoryCursor(raw string) (usageHistoryCursor, error) {
	if raw == "" || len(raw) > usageHistoryMaxCursorLen {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 || len(decoded) > usageHistoryMaxCursorLen {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	var cursor usageHistoryCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	if cursor.Version != 1 || cursor.SnapshotAt == "" || cursor.CreatedAt == "" || cursor.ID == "" || cursor.FilterSignature == "" {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.SnapshotAt); err != nil {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt); err != nil {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	if len(cursor.CreatedAt) > 128 || len(cursor.ID) > 256 || strings.ContainsRune(cursor.CreatedAt, 0) || strings.ContainsRune(cursor.ID, 0) {
		return usageHistoryCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func encodeUsageHistoryCursor(cursor usageHistoryCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func buildUsageHistory(ctx context.Context, database *sql.DB, filters usageHistoryFilters) (usageHistoryResponse, error) {
	response := usageHistoryResponse{
		GeneratedAt: db.Now(),
		Trend:       []usageHistoryTrendPoint{},
		Options: usageHistoryOptions{
			Providers: []string{},
			Kinds:     []string{},
			Models:    []string{},
		},
		Items: []usageHistoryItem{},
	}
	var err error
	response.Summary, err = queryUsageHistorySummary(ctx, database, filters)
	if err != nil {
		return usageHistoryResponse{}, err
	}
	response.Trend, response.TrendTruncated, err = queryUsageHistoryTrend(ctx, database, filters)
	if err != nil {
		return usageHistoryResponse{}, err
	}
	response.Options, err = queryUsageHistoryOptions(ctx, database, filters)
	if err != nil {
		return usageHistoryResponse{}, err
	}
	response.Items, response.NextCursor, err = queryUsageHistoryItems(ctx, database, filters)
	if err != nil {
		return usageHistoryResponse{}, err
	}
	return response, nil
}

const usageHistoryMetricsSelect = `
COUNT(*),
COALESCE(SUM(COALESCE(input_tokens, 0)), 0),
COALESCE(SUM(COALESCE(output_tokens, 0)), 0),
COALESCE(SUM(COALESCE(reasoning_tokens, 0)), 0),
COALESCE(SUM(COALESCE(cached_input_tokens, 0)), 0),
COALESCE(SUM(COALESCE(cost_usd, 0)), 0),
COALESCE(AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms END), 0),
COALESCE(AVG(CASE WHEN duration_ms > 0 THEN duration_ms END), 0),
COALESCE(SUM(CASE WHEN COALESCE(error_message, '') <> '' THEN 1 ELSE 0 END), 0),
CASE WHEN COUNT(*) = 0 THEN 0 ELSE COALESCE(SUM(CASE WHEN COALESCE(error_message, '') = '' THEN 1 ELSE 0 END), 0) * 1.0 / COUNT(*) END`

func queryUsageHistorySummary(ctx context.Context, database *sql.DB, filters usageHistoryFilters) (usageHistoryMetrics, error) {
	where, args := usageHistoryWhere("api_requests", filters)
	query := `SELECT ` + usageHistoryMetricsSelect + ` FROM api_requests WHERE ` + where
	var metrics usageHistoryMetrics
	if err := database.QueryRowContext(ctx, query, args...).Scan(usageHistoryMetricDestinations(&metrics)...); err != nil {
		return usageHistoryMetrics{}, err
	}
	metrics.TotalTokens = metrics.InputTokens + metrics.OutputTokens
	return metrics, nil
}

func queryUsageHistoryTrend(ctx context.Context, database *sql.DB, filters usageHistoryFilters) ([]usageHistoryTrendPoint, bool, error) {
	return queryUsageHistoryTrendWithLimit(ctx, database, filters, usageHistoryMaxTrend)
}

func queryUsageHistoryTrendWithLimit(ctx context.Context, database *sql.DB, filters usageHistoryFilters, maxTrend int) ([]usageHistoryTrendPoint, bool, error) {
	bucketExpression, err := usageHistoryBucketExpression(filters.Bucket, filters.TZOffset)
	if err != nil {
		return nil, false, err
	}
	where, args := usageHistoryWhere("api_requests", filters)
	query := `SELECT ` + bucketExpression + ` AS bucket, ` + usageHistoryMetricsSelect + `
FROM api_requests
WHERE ` + where + ` AND ` + bucketExpression + ` IS NOT NULL
GROUP BY bucket
ORDER BY bucket DESC
LIMIT ?`
	args = append(args, maxTrend+1)
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	points := make([]usageHistoryTrendPoint, 0)
	for rows.Next() {
		var point usageHistoryTrendPoint
		destinations := append([]any{&point.Bucket}, usageHistoryMetricDestinations(&point.usageHistoryMetrics)...)
		if err := rows.Scan(destinations...); err != nil {
			return nil, false, err
		}
		point.TotalTokens = point.InputTokens + point.OutputTokens
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(points) > maxTrend
	if truncated {
		points = points[:maxTrend]
	}
	for left, right := 0, len(points)-1; left < right; left, right = left+1, right-1 {
		points[left], points[right] = points[right], points[left]
	}
	return points, truncated, nil
}

// usageHistoryBucketExpression builds the SQLite expression that assigns a row
// to a bucket. created_at is always stored in UTC, so callers that want buckets
// aligned to a viewer's calendar day pass tzOffsetMinutes and get a shifted
// grouping; a "did I use this today" heatmap in UTC+8 would otherwise file
// everything before 08:00 local under the previous day. An offset of zero emits
// the original expression unchanged so existing callers keep identical SQL.
func usageHistoryBucketExpression(bucket string, tzOffsetMinutes int) (string, error) {
	var format string
	switch bucket {
	case "hour":
		format = `%Y-%m-%dT%H:00:00Z`
	case "day":
		format = `%Y-%m-%d`
	case "month":
		format = `%Y-%m`
	default:
		return "", errors.New("invalid bucket")
	}
	if tzOffsetMinutes == 0 {
		return `strftime('` + format + `', created_at)`, nil
	}
	// The offset is range-checked to whole minutes by parseUsageHistoryFilters,
	// so formatting it into the modifier cannot inject SQL.
	return fmt.Sprintf(`strftime('%s', created_at, '%+d minutes')`, format, tzOffsetMinutes), nil
}

func usageHistoryMetricDestinations(metrics *usageHistoryMetrics) []any {
	return []any{
		&metrics.RequestCount,
		&metrics.InputTokens,
		&metrics.OutputTokens,
		&metrics.ReasoningTokens,
		&metrics.CachedInputTokens,
		&metrics.TotalCostUSD,
		&metrics.AverageTTFTMS,
		&metrics.AverageDurationMS,
		&metrics.Errors,
		&metrics.SuccessRate,
	}
}

func usageHistoryWhere(table string, filters usageHistoryFilters) (string, []any) {
	clauses := []string{table + `.created_at <= ?`}
	args := []any{filters.SnapshotAt}
	if filters.Provider != "" {
		clauses = append(clauses, table+`.provider = ?`)
		args = append(args, filters.Provider)
	}
	if filters.Model != "" {
		clauses = append(clauses, table+`.model = ?`)
		args = append(args, filters.Model)
	}
	if filters.Kind != "" {
		clauses = append(clauses, table+`.kind = ?`)
		args = append(args, filters.Kind)
	}
	if filters.From != "" {
		clauses = append(clauses, table+`.created_at >= ?`)
		args = append(args, filters.From)
	}
	if filters.ToExclusive != "" {
		clauses = append(clauses, table+`.created_at < ?`)
		args = append(args, filters.ToExclusive)
	}
	return strings.Join(clauses, " AND "), args
}

func queryUsageHistoryOptions(ctx context.Context, database *sql.DB, filters usageHistoryFilters) (usageHistoryOptions, error) {
	providers, err := queryUsageHistoryOptionValues(ctx, database, `
SELECT DISTINCT TRIM(provider) AS value
FROM api_requests
WHERE created_at <= ? AND COALESCE(TRIM(provider), '') <> ''
ORDER BY value COLLATE NOCASE ASC, value ASC
LIMIT ?`, filters.SnapshotAt, usageHistoryMaxProviders)
	if err != nil {
		return usageHistoryOptions{}, err
	}
	kinds, err := queryUsageHistoryOptionValues(ctx, database, `
SELECT DISTINCT TRIM(kind) AS value
FROM api_requests
WHERE created_at <= ? AND COALESCE(TRIM(kind), '') <> ''
ORDER BY value COLLATE NOCASE ASC, value ASC
LIMIT ?`, filters.SnapshotAt, usageHistoryMaxKinds)
	if err != nil {
		return usageHistoryOptions{}, err
	}
	modelQuery := `
SELECT DISTINCT TRIM(model) AS value
FROM api_requests
WHERE created_at <= ? AND COALESCE(TRIM(model), '') <> ''`
	modelArgs := []any{filters.SnapshotAt}
	if filters.Provider != "" {
		modelQuery += ` AND provider = ?`
		modelArgs = append(modelArgs, filters.Provider)
	}
	modelQuery += ` ORDER BY value COLLATE NOCASE ASC, value ASC LIMIT ?`
	modelArgs = append(modelArgs, usageHistoryMaxModels)
	models, err := queryUsageHistoryOptionValues(ctx, database, modelQuery, modelArgs...)
	if err != nil {
		return usageHistoryOptions{}, err
	}
	return usageHistoryOptions{Providers: providers, Kinds: kinds, Models: models}, nil
}

func queryUsageHistoryOptionValues(ctx context.Context, database *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func queryUsageHistoryItems(ctx context.Context, database *sql.DB, filters usageHistoryFilters) ([]usageHistoryItem, string, error) {
	where, args := usageHistoryWhere("r", filters)
	if filters.Cursor != nil {
		where += ` AND (r.created_at < ? OR (r.created_at = ? AND r.id < ?))`
		args = append(args, filters.Cursor.CreatedAt, filters.Cursor.CreatedAt, filters.Cursor.ID)
	}
	query := `
SELECT
	r.id,
	r.created_at,
	COALESCE(r.agent_id, ''),
	COALESCE(a.title, ''),
	COALESCE(r.run_id, ''),
	COALESCE(r.message_id, ''),
	COALESCE(r.kind, ''),
	COALESCE(r.provider, ''),
	COALESCE(r.model, ''),
	COALESCE(r.input_tokens, 0),
	COALESCE(r.output_tokens, 0),
	COALESCE(r.reasoning_tokens, 0),
	COALESCE(r.cached_input_tokens, 0),
	COALESCE(r.ttft_ms, 0),
	COALESCE(r.duration_ms, 0),
	COALESCE(r.cost_usd, 0),
	SUBSTR(COALESCE(r.error_message, ''), 1, 2000),
	CASE WHEN COALESCE(r.error_message, '') <> '' THEN 'error' ELSE 'success' END
FROM api_requests r
LEFT JOIN agents a ON a.id = r.agent_id
WHERE ` + where + `
ORDER BY r.created_at DESC, r.id DESC
LIMIT ?`
	args = append(args, filters.Limit+1)
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	items := make([]usageHistoryItem, 0, filters.Limit+1)
	for rows.Next() {
		var item usageHistoryItem
		if err := rows.Scan(
			&item.ID,
			&item.CreatedAt,
			&item.AgentID,
			&item.AgentTitle,
			&item.RunID,
			&item.MessageID,
			&item.Kind,
			&item.Provider,
			&item.Model,
			&item.InputTokens,
			&item.OutputTokens,
			&item.ReasoningTokens,
			&item.CachedInputTokens,
			&item.TTFTMS,
			&item.DurationMS,
			&item.CostUSD,
			&item.ErrorMessage,
			&item.Status,
		); err != nil {
			return nil, "", err
		}
		item.TotalTokens = item.InputTokens + item.OutputTokens
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(items) <= filters.Limit {
		return items, "", nil
	}
	items = items[:filters.Limit]
	last := items[len(items)-1]
	nextCursor, err := encodeUsageHistoryCursor(usageHistoryCursor{
		Version:         1,
		SnapshotAt:      filters.SnapshotAt,
		CreatedAt:       last.CreatedAt,
		ID:              last.ID,
		FilterSignature: usageHistoryFilterSignature(filters),
	})
	if err != nil {
		return nil, "", err
	}
	return items, nextCursor, nil
}
