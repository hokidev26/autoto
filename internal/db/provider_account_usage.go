package db

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ProviderAccountUsageWindow contains locally recorded model-request usage for
// one provider account and one time range. CostUSD is the value stored on the
// request records; it must not be interpreted as a provider billing statement.
type ProviderAccountUsageWindow struct {
	RequestCount int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

func (window ProviderAccountUsageWindow) TotalTokens() int64 {
	return window.InputTokens + window.OutputTokens
}

// ProviderAccountUsage contains all-time and recent locally recorded usage for
// a provider account. The recent windows default to rolling 5-hour and 7-day
// ranges and can be aligned to a provider's current quota reset time.
type ProviderAccountUsage struct {
	Total      ProviderAccountUsageWindow
	Last5Hours ProviderAccountUsageWindow
	Last7Days  ProviderAccountUsageWindow
}

type ProviderAccountUsageWindowStarts struct {
	Last5Hours time.Time
	Last7Days  time.Time
}

// ListProviderAccountUsage returns usage grouped by credential_id.
// Requests without a credential_id are intentionally excluded because they
// cannot be safely attributed to a specific account. windowStarts, when set
// for an account, replace the rolling now-5h / now-7d bounds.
func (s *providerAccountStore) ListProviderAccountUsage(ctx context.Context, provider string, accountIDs []string, now time.Time, windowStarts map[string]ProviderAccountUsageWindowStarts) (map[string]ProviderAccountUsage, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database store is unavailable")
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, errors.New("provider is required")
	}
	ids := uniqueProviderAccountIDs(accountIDs)
	result := make(map[string]ProviderAccountUsage, len(ids))
	for _, id := range ids {
		result[id] = ProviderAccountUsage{}
	}
	if len(ids) == 0 {
		return result, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, provider)
	for index, id := range ids {
		placeholders[index] = "?"
		args = append(args, id)
	}
	inClause := strings.Join(placeholders, ",")

	totalsQuery := `
SELECT
  credential_id,
  COUNT(*),
  COALESCE(SUM(input_tokens), 0),
  COALESCE(SUM(output_tokens), 0),
  COALESCE(SUM(cost_usd), 0)
FROM api_requests
WHERE provider = ? AND credential_id IN (` + inClause + `)
GROUP BY credential_id`
	rows, err := s.db.QueryContext(ctx, totalsQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			accountID string
			total     ProviderAccountUsageWindow
		)
		if err := rows.Scan(&accountID, &total.RequestCount, &total.InputTokens, &total.OutputTokens, &total.CostUSD); err != nil {
			return nil, err
		}
		usage := result[accountID]
		usage.Total = total
		result[accountID] = usage
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	floor := now.Add(-7 * 24 * time.Hour)
	for _, starts := range windowStarts {
		if !starts.Last7Days.IsZero() && starts.Last7Days.Before(floor) {
			floor = starts.Last7Days.UTC()
		}
		if !starts.Last5Hours.IsZero() && starts.Last5Hours.Before(floor) {
			floor = starts.Last5Hours.UTC()
		}
	}
	recentArgs := append([]any{floor.UTC().Format(time.RFC3339Nano)}, args...)
	recentQuery := `
SELECT credential_id, created_at, COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(cost_usd, 0)
FROM api_requests
WHERE julianday(created_at) >= julianday(?) AND provider = ? AND credential_id IN (` + inClause + `)`
	recentRows, err := s.db.QueryContext(ctx, recentQuery, recentArgs...)
	if err != nil {
		return nil, err
	}
	defer recentRows.Close()
	for recentRows.Next() {
		var (
			accountID string
			createdAt string
			input     int64
			output    int64
			cost      float64
		)
		if err := recentRows.Scan(&accountID, &createdAt, &input, &output, &cost); err != nil {
			return nil, err
		}
		when, ok := parseProviderAccountUsageTime(createdAt)
		if !ok {
			continue
		}
		starts := accountUsageWindowStarts(windowStarts, accountID, now)
		usage := result[accountID]
		if !when.Before(starts.Last5Hours) {
			usage.Last5Hours.RequestCount++
			usage.Last5Hours.InputTokens += input
			usage.Last5Hours.OutputTokens += output
			usage.Last5Hours.CostUSD += cost
		}
		if !when.Before(starts.Last7Days) {
			usage.Last7Days.RequestCount++
			usage.Last7Days.InputTokens += input
			usage.Last7Days.OutputTokens += output
			usage.Last7Days.CostUSD += cost
		}
		result[accountID] = usage
	}
	return result, recentRows.Err()
}

func accountUsageWindowStarts(windowStarts map[string]ProviderAccountUsageWindowStarts, accountID string, now time.Time) ProviderAccountUsageWindowStarts {
	starts := ProviderAccountUsageWindowStarts{
		Last5Hours: now.Add(-5 * time.Hour),
		Last7Days:  now.Add(-7 * 24 * time.Hour),
	}
	if windowStarts == nil {
		return starts
	}
	custom, ok := windowStarts[accountID]
	if !ok {
		return starts
	}
	if !custom.Last5Hours.IsZero() {
		starts.Last5Hours = custom.Last5Hours.UTC()
	}
	if !custom.Last7Days.IsZero() {
		starts.Last7Days = custom.Last7Days.UTC()
	}
	return starts
}

func parseProviderAccountUsageTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), true
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), true
	}
	return time.Time{}, false
}

func uniqueProviderAccountIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsRune(value, 0) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
