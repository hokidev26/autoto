package server

import (
	"context"
	"database/sql"
	"net/http"

	"autoto/internal/db"
)

type usageSummaryResponse struct {
	GeneratedAt string               `json:"generatedAt"`
	Counts      usageCounts          `json:"counts"`
	Messages    usageMessageStats    `json:"messages"`
	ToolCalls   usageToolCallStats   `json:"toolCalls"`
	APIRequests usageAPIRequestStats `json:"apiRequests"`
	Backends    usageBackendStats    `json:"backends"`
}

type usageCounts struct {
	Projects    int64 `json:"projects"`
	Worklines   int64 `json:"worklines"`
	Agents      int64 `json:"agents"`
	Messages    int64 `json:"messages"`
	ToolCalls   int64 `json:"toolCalls"`
	APIRequests int64 `json:"apiRequests"`
	Backends    int64 `json:"backends"`
}

type usageMessageStats struct {
	ByRole   map[string]int64 `json:"byRole"`
	LatestAt string           `json:"latestAt,omitempty"`
}

type usageToolCallStats struct {
	ByStatus          map[string]int64  `json:"byStatus"`
	TopTools          []usageNamedCount `json:"topTools"`
	AverageDurationMS float64           `json:"averageDurationMs"`
	LatestAt          string            `json:"latestAt,omitempty"`
}

type usageAPIRequestStats struct {
	ByProvider        map[string]int64 `json:"byProvider"`
	ByKind            map[string]int64 `json:"byKind"`
	InputTokens       int64            `json:"inputTokens"`
	OutputTokens      int64            `json:"outputTokens"`
	ReasoningTokens   int64            `json:"reasoningTokens"`
	CachedInputTokens int64            `json:"cachedInputTokens"`
	TotalCostUSD      float64          `json:"totalCostUsd"`
	AverageDurationMS float64          `json:"averageDurationMs"`
	Errors            int64            `json:"errors"`
	LatestAt          string           `json:"latestAt,omitempty"`
}

type usageBackendStats struct {
	Active           int64 `json:"active"`
	APIKeyConfigured int64 `json:"apiKeyConfigured"`
}

type usageNamedCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func (s *Server) usageSummary(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database store is not initialized")
		return
	}
	summary, err := buildUsageSummary(r.Context(), s.store.ReadDB())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func buildUsageSummary(ctx context.Context, database *sql.DB) (usageSummaryResponse, error) {
	summary := usageSummaryResponse{
		GeneratedAt: db.Now(),
		Messages:    usageMessageStats{ByRole: map[string]int64{}},
		ToolCalls:   usageToolCallStats{ByStatus: map[string]int64{}, TopTools: []usageNamedCount{}},
		APIRequests: usageAPIRequestStats{ByProvider: map[string]int64{}, ByKind: map[string]int64{}},
	}

	queries := []struct {
		query string
		dst   *int64
	}{
		{`SELECT COUNT(*) FROM projects`, &summary.Counts.Projects},
		{`SELECT COUNT(*) FROM worklines`, &summary.Counts.Worklines},
		{`SELECT COUNT(*) FROM agents`, &summary.Counts.Agents},
		{`SELECT COUNT(*) FROM agent_messages`, &summary.Counts.Messages},
		{`SELECT COUNT(*) FROM agent_tool_calls`, &summary.Counts.ToolCalls},
		{`SELECT COUNT(*) FROM api_requests`, &summary.Counts.APIRequests},
		{`SELECT COUNT(*) FROM agent_backends`, &summary.Counts.Backends},
		{`SELECT COUNT(*) FROM agent_backends WHERE active != 0`, &summary.Backends.Active},
		{`SELECT COUNT(*) FROM agent_backends WHERE COALESCE(api_key, '') != ''`, &summary.Backends.APIKeyConfigured},
		{`SELECT COUNT(*) FROM api_requests WHERE COALESCE(error_message, '') != ''`, &summary.APIRequests.Errors},
	}
	for _, item := range queries {
		if err := database.QueryRowContext(ctx, item.query).Scan(item.dst); err != nil {
			return usageSummaryResponse{}, err
		}
	}

	var err error
	if summary.Messages.ByRole, err = queryCountMap(ctx, database, `SELECT COALESCE(NULLIF(role, ''), 'unknown'), COUNT(*) FROM agent_messages GROUP BY COALESCE(NULLIF(role, ''), 'unknown') ORDER BY 2 DESC`); err != nil {
		return usageSummaryResponse{}, err
	}
	if summary.ToolCalls.ByStatus, err = queryCountMap(ctx, database, `SELECT COALESCE(NULLIF(status, ''), 'unknown'), COUNT(*) FROM agent_tool_calls GROUP BY COALESCE(NULLIF(status, ''), 'unknown') ORDER BY 2 DESC`); err != nil {
		return usageSummaryResponse{}, err
	}
	if summary.APIRequests.ByProvider, err = queryCountMap(ctx, database, `SELECT COALESCE(NULLIF(provider, ''), 'unknown'), COUNT(*) FROM api_requests GROUP BY COALESCE(NULLIF(provider, ''), 'unknown') ORDER BY 2 DESC`); err != nil {
		return usageSummaryResponse{}, err
	}
	if summary.APIRequests.ByKind, err = queryCountMap(ctx, database, `SELECT COALESCE(NULLIF(kind, ''), 'unknown'), COUNT(*) FROM api_requests GROUP BY COALESCE(NULLIF(kind, ''), 'unknown') ORDER BY 2 DESC`); err != nil {
		return usageSummaryResponse{}, err
	}
	if summary.ToolCalls.TopTools, err = queryNamedCounts(ctx, database, `SELECT COALESCE(NULLIF(tool_name, ''), 'unknown'), COUNT(*) FROM agent_tool_calls GROUP BY COALESCE(NULLIF(tool_name, ''), 'unknown') ORDER BY 2 DESC, 1 ASC LIMIT 8`); err != nil {
		return usageSummaryResponse{}, err
	}

	if err := database.QueryRowContext(ctx, `SELECT COALESCE(AVG(duration_ms), 0) FROM agent_tool_calls WHERE duration_ms IS NOT NULL`).Scan(&summary.ToolCalls.AverageDurationMS); err != nil {
		return usageSummaryResponse{}, err
	}
	if err := database.QueryRowContext(ctx, `SELECT COALESCE(AVG(duration_ms), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(cached_input_tokens), 0), COALESCE(SUM(cost_usd), 0) FROM api_requests`).Scan(&summary.APIRequests.AverageDurationMS, &summary.APIRequests.InputTokens, &summary.APIRequests.OutputTokens, &summary.APIRequests.ReasoningTokens, &summary.APIRequests.CachedInputTokens, &summary.APIRequests.TotalCostUSD); err != nil {
		return usageSummaryResponse{}, err
	}

	if summary.Messages.LatestAt, err = latestTimestamp(ctx, database, `SELECT COALESCE(MAX(created_at), '') FROM agent_messages`); err != nil {
		return usageSummaryResponse{}, err
	}
	if summary.ToolCalls.LatestAt, err = latestTimestamp(ctx, database, `SELECT COALESCE(MAX(created_at), '') FROM agent_tool_calls`); err != nil {
		return usageSummaryResponse{}, err
	}
	if summary.APIRequests.LatestAt, err = latestTimestamp(ctx, database, `SELECT COALESCE(MAX(created_at), '') FROM api_requests`); err != nil {
		return usageSummaryResponse{}, err
	}
	return summary, nil
}

func queryCountMap(ctx context.Context, database *sql.DB, query string) (map[string]int64, error) {
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var name string
		var count int64
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		out[name] = count
	}
	return out, rows.Err()
}

func queryNamedCounts(ctx context.Context, database *sql.DB, query string) ([]usageNamedCount, error) {
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]usageNamedCount, 0)
	for rows.Next() {
		var item usageNamedCount
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func latestTimestamp(ctx context.Context, database *sql.DB, query string) (string, error) {
	var latest string
	if err := database.QueryRowContext(ctx, query).Scan(&latest); err != nil {
		return "", err
	}
	return latest, nil
}
