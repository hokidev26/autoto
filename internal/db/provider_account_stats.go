package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"autoto/internal/providers"
)

type ProviderAccountStats struct {
	Provider          string          `json:"provider"`
	AccountID         string          `json:"account_id"`
	SuccessCount      int64           `json:"success_count"`
	FailureCount      int64           `json:"failure_count"`
	LastAttemptAt     string          `json:"last_attempt_at,omitempty"`
	LastUseAt         string          `json:"last_use_at,omitempty"`
	LastSuccessAt     string          `json:"last_success_at,omitempty"`
	LastFailureAt     string          `json:"last_failure_at,omitempty"`
	LastHTTPStatus    int             `json:"last_http_status,omitempty"`
	LastStatusCode    string          `json:"last_status_code,omitempty"`
	LastErrorCode     string          `json:"last_error_code,omitempty"`
	QuotaSnapshotJSON json.RawMessage `json:"-"`
	QuotaFetchedAt    string          `json:"quota_fetched_at,omitempty"`
}

func (s *providerAccountStore) RecordProviderAccountAttempt(ctx context.Context, attempt providers.ProviderAccountAttempt) error {
	if s == nil || s.db == nil {
		return errors.New("database store is unavailable")
	}
	provider, accountID, err := validateProviderAccountKey(attempt.Provider, attempt.AccountID)
	if err != nil {
		return err
	}
	attemptedAt := attempt.AttemptedAt.UTC()
	if attemptedAt.IsZero() {
		attemptedAt = time.Now().UTC()
	}
	timestamp := formatProviderAccountTimestamp(attemptedAt)
	successCount, failureCount := 0, 1
	lastUse, lastSuccess, lastFailure := "", "", timestamp
	if attempt.Success {
		successCount, failureCount = 1, 0
		lastUse, lastSuccess, lastFailure = timestamp, timestamp, ""
	}
	httpStatus := any(nil)
	if attempt.HTTPStatus >= 100 && attempt.HTTPStatus <= 599 {
		httpStatus = attempt.HTTPStatus
	}
	statusCode := safeProviderAccountCode(attempt.StatusCode)
	errorCode := safeProviderAccountCode(attempt.ErrorCode)
	if attempt.Success {
		errorCode = ""
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO provider_account_stats (
  provider, account_id, success_count, failure_count, last_attempt_at, last_use_at,
  last_success_at, last_failure_at, last_http_status, last_status_code, last_error_code
) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''))
ON CONFLICT(provider, account_id) DO UPDATE SET
  success_count = provider_account_stats.success_count + excluded.success_count,
  failure_count = provider_account_stats.failure_count + excluded.failure_count,
  last_attempt_at = CASE WHEN excluded.last_attempt_at > COALESCE(provider_account_stats.last_attempt_at, '') OR julianday(excluded.last_attempt_at) > julianday(provider_account_stats.last_attempt_at) THEN excluded.last_attempt_at ELSE provider_account_stats.last_attempt_at END,
  last_use_at = CASE WHEN excluded.success_count = 1 AND (excluded.last_use_at > COALESCE(provider_account_stats.last_use_at, '') OR julianday(excluded.last_use_at) > julianday(provider_account_stats.last_use_at)) THEN excluded.last_use_at ELSE provider_account_stats.last_use_at END,
  last_success_at = CASE WHEN excluded.success_count = 1 AND (excluded.last_success_at > COALESCE(provider_account_stats.last_success_at, '') OR julianday(excluded.last_success_at) > julianday(provider_account_stats.last_success_at)) THEN excluded.last_success_at ELSE provider_account_stats.last_success_at END,
  last_failure_at = CASE WHEN excluded.failure_count = 1 AND (excluded.last_failure_at > COALESCE(provider_account_stats.last_failure_at, '') OR julianday(excluded.last_failure_at) > julianday(provider_account_stats.last_failure_at)) THEN excluded.last_failure_at ELSE provider_account_stats.last_failure_at END,
  last_http_status = CASE WHEN excluded.last_attempt_at > COALESCE(provider_account_stats.last_attempt_at, '') OR julianday(excluded.last_attempt_at) > julianday(provider_account_stats.last_attempt_at) THEN excluded.last_http_status ELSE provider_account_stats.last_http_status END,
  last_status_code = CASE WHEN excluded.last_attempt_at > COALESCE(provider_account_stats.last_attempt_at, '') OR julianday(excluded.last_attempt_at) > julianday(provider_account_stats.last_attempt_at) THEN excluded.last_status_code ELSE provider_account_stats.last_status_code END,
  last_error_code = CASE WHEN excluded.last_attempt_at > COALESCE(provider_account_stats.last_attempt_at, '') OR julianday(excluded.last_attempt_at) > julianday(provider_account_stats.last_attempt_at) THEN excluded.last_error_code ELSE provider_account_stats.last_error_code END
`, provider, accountID, successCount, failureCount, timestamp, lastUse, lastSuccess, lastFailure, httpStatus, statusCode, errorCode)
	return err
}

func (s *providerAccountStore) UpdateProviderAccountQuota(ctx context.Context, provider, accountID string, quota any, fetchedAt time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("database store is unavailable")
	}
	provider, accountID, err := validateProviderAccountKey(provider, accountID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(quota)
	if err != nil || len(encoded) == 0 || len(encoded) > 64<<10 {
		return errors.New("invalid provider account quota snapshot")
	}
	var object map[string]any
	if json.Unmarshal(encoded, &object) != nil || object == nil {
		return errors.New("invalid provider account quota snapshot")
	}
	if key, found := providerAccountSensitiveKey(object); found {
		return fmt.Errorf("provider account quota contains forbidden sensitive key %q", key)
	}
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	timestamp := formatProviderAccountTimestamp(fetchedAt)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO provider_account_stats (provider, account_id, quota_snapshot_json, quota_fetched_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(provider, account_id) DO UPDATE SET
  quota_snapshot_json = CASE WHEN excluded.quota_fetched_at > COALESCE(provider_account_stats.quota_fetched_at, '') OR julianday(excluded.quota_fetched_at) > julianday(provider_account_stats.quota_fetched_at) THEN excluded.quota_snapshot_json ELSE provider_account_stats.quota_snapshot_json END,
  quota_fetched_at = CASE WHEN excluded.quota_fetched_at > COALESCE(provider_account_stats.quota_fetched_at, '') OR julianday(excluded.quota_fetched_at) > julianday(provider_account_stats.quota_fetched_at) THEN excluded.quota_fetched_at ELSE provider_account_stats.quota_fetched_at END
`, provider, accountID, string(encoded), timestamp)
	return err
}

func (s *providerAccountStore) ListProviderAccountStats(ctx context.Context, provider string) (map[string]ProviderAccountStats, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database store is unavailable")
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, errors.New("provider is required")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT provider, account_id, success_count, failure_count,
       COALESCE(last_attempt_at, ''), COALESCE(last_use_at, ''), COALESCE(last_success_at, ''), COALESCE(last_failure_at, ''),
       COALESCE(last_http_status, 0), COALESCE(last_status_code, ''), COALESCE(last_error_code, ''),
       COALESCE(quota_snapshot_json, ''), COALESCE(quota_fetched_at, '')
FROM provider_account_stats WHERE provider = ?
`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]ProviderAccountStats)
	for rows.Next() {
		stats, err := scanProviderAccountStats(rows.Scan)
		if err != nil {
			return nil, err
		}
		result[stats.AccountID] = stats
	}
	return result, rows.Err()
}

func (s *providerAccountStore) GetProviderAccountStats(ctx context.Context, provider, accountID string) (ProviderAccountStats, error) {
	if s == nil || s.db == nil {
		return ProviderAccountStats{}, errors.New("database store is unavailable")
	}
	provider, accountID, err := validateProviderAccountKey(provider, accountID)
	if err != nil {
		return ProviderAccountStats{}, err
	}
	return scanProviderAccountStats(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `
SELECT provider, account_id, success_count, failure_count,
       COALESCE(last_attempt_at, ''), COALESCE(last_use_at, ''), COALESCE(last_success_at, ''), COALESCE(last_failure_at, ''),
       COALESCE(last_http_status, 0), COALESCE(last_status_code, ''), COALESCE(last_error_code, ''),
       COALESCE(quota_snapshot_json, ''), COALESCE(quota_fetched_at, '')
FROM provider_account_stats WHERE provider = ? AND account_id = ?
`, provider, accountID).Scan(dest...)
	})
}

func (s *providerAccountStore) DeleteProviderAccountStats(ctx context.Context, provider, accountID string) error {
	if s == nil || s.db == nil {
		return errors.New("database store is unavailable")
	}
	provider, accountID, err := validateProviderAccountKey(provider, accountID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM provider_account_stats WHERE provider = ? AND account_id = ?`, provider, accountID)
	return err
}

type providerAccountStatsScanner func(...any) error

func scanProviderAccountStats(scan providerAccountStatsScanner) (ProviderAccountStats, error) {
	var stats ProviderAccountStats
	var quota string
	if err := scan(
		&stats.Provider, &stats.AccountID, &stats.SuccessCount, &stats.FailureCount,
		&stats.LastAttemptAt, &stats.LastUseAt, &stats.LastSuccessAt, &stats.LastFailureAt,
		&stats.LastHTTPStatus, &stats.LastStatusCode, &stats.LastErrorCode, &quota, &stats.QuotaFetchedAt,
	); err != nil {
		return ProviderAccountStats{}, err
	}
	if quota != "" {
		if !json.Valid([]byte(quota)) {
			return ProviderAccountStats{}, errors.New("stored provider account quota is invalid")
		}
		stats.QuotaSnapshotJSON = json.RawMessage(quota)
	}
	for _, value := range []*string{&stats.LastAttemptAt, &stats.LastUseAt, &stats.LastSuccessAt, &stats.LastFailureAt, &stats.QuotaFetchedAt} {
		if parsed, err := time.Parse(time.RFC3339Nano, *value); err == nil {
			*value = parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return stats, nil
}

func formatProviderAccountTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func validateProviderAccountKey(provider, accountID string) (string, string, error) {
	provider = strings.TrimSpace(provider)
	accountID = strings.TrimSpace(accountID)
	if provider == "" || accountID == "" || len(provider) > 128 || len(accountID) > 128 || strings.ContainsRune(provider, 0) || strings.ContainsRune(accountID, 0) {
		return "", "", errors.New("invalid provider account key")
	}
	return provider, accountID, nil
}

func safeProviderAccountCode(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._:-", char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func providerAccountSensitiveKey(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
			for _, marker := range []string{"accesstoken", "refreshtoken", "idtoken", "authorization", "apikey", "clientsecret", "privatekey", "cookie", "jwt", "bearer", "credential", "secret", "password"} {
				if strings.Contains(normalized, marker) {
					return key, true
				}
			}
			if normalized == "token" || strings.HasSuffix(normalized, "token") {
				return key, true
			}
			if key, found := providerAccountSensitiveKey(child); found {
				return key, true
			}
		}
	case []any:
		for _, child := range typed {
			if key, found := providerAccountSensitiveKey(child); found {
				return key, true
			}
		}
	}
	return "", false
}

var _ providers.AccountTelemetry = (*Store)(nil)
