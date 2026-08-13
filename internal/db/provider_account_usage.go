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
// a provider account. The recent windows are fixed to match the Codex UI's
// common 5-hour and 7-day quota windows.
type ProviderAccountUsage struct {
	Total      ProviderAccountUsageWindow
	Last5Hours ProviderAccountUsageWindow
	Last7Days  ProviderAccountUsageWindow
}

// ListProviderAccountUsage returns usage grouped by credential_id in one query.
// Requests without a credential_id are intentionally excluded because they
// cannot be safely attributed to a specific account.
func (s *providerAccountStore) ListProviderAccountUsage(ctx context.Context, provider string, accountIDs []string, now time.Time) (map[string]ProviderAccountUsage, error) {
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

	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+3)
	last5 := now.Add(-5 * time.Hour).UTC().Format(time.RFC3339Nano)
	last7 := now.Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	args = append(args, last5, last7, provider)
	for index, id := range ids {
		placeholders[index] = "?"
		args = append(args, id)
	}

	query := `
WITH filtered AS (
  SELECT
    credential_id,
    COALESCE(input_tokens, 0) AS input_tokens,
    COALESCE(output_tokens, 0) AS output_tokens,
    COALESCE(cost_usd, 0) AS cost_usd,
    CASE WHEN julianday(created_at) >= julianday(?) THEN 1 ELSE 0 END AS in_last_5_hours,
    CASE WHEN julianday(created_at) >= julianday(?) THEN 1 ELSE 0 END AS in_last_7_days
  FROM api_requests
  WHERE provider = ? AND credential_id IN (` + strings.Join(placeholders, ",") + `)
)
SELECT
  credential_id,
  COUNT(*),
  COALESCE(SUM(input_tokens), 0),
  COALESCE(SUM(output_tokens), 0),
  COALESCE(SUM(cost_usd), 0),
  COALESCE(SUM(in_last_5_hours), 0),
  COALESCE(SUM(CASE WHEN in_last_5_hours = 1 THEN input_tokens ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN in_last_5_hours = 1 THEN output_tokens ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN in_last_5_hours = 1 THEN cost_usd ELSE 0 END), 0),
  COALESCE(SUM(in_last_7_days), 0),
  COALESCE(SUM(CASE WHEN in_last_7_days = 1 THEN input_tokens ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN in_last_7_days = 1 THEN output_tokens ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN in_last_7_days = 1 THEN cost_usd ELSE 0 END), 0)
FROM filtered
GROUP BY credential_id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			accountID string
			total     ProviderAccountUsageWindow
			last5     ProviderAccountUsageWindow
			last7     ProviderAccountUsageWindow
		)
		if err := rows.Scan(
			&accountID,
			&total.RequestCount, &total.InputTokens, &total.OutputTokens, &total.CostUSD,
			&last5.RequestCount, &last5.InputTokens, &last5.OutputTokens, &last5.CostUSD,
			&last7.RequestCount, &last7.InputTokens, &last7.OutputTokens, &last7.CostUSD,
		); err != nil {
			return nil, err
		}
		result[accountID] = ProviderAccountUsage{Total: total, Last5Hours: last5, Last7Days: last7}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
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
