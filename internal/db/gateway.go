package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrGatewayKeyRevoked = errors.New("gateway key is revoked")

type GatewayKey struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	KeyPrefix         string   `json:"keyPrefix"`
	TokenHash         string   `json:"-"`
	Enabled           bool     `json:"enabled"`
	AllowedModels     []string `json:"allowedModels"`
	RequestsPerMinute int64    `json:"requestsPerMinute"`
	MonthlyTokenLimit int64    `json:"monthlyTokenLimit"`
	MaxConcurrency    int64    `json:"maxConcurrency"`
	ExpiresAt         string   `json:"expiresAt,omitempty"`
	LastUsedAt        string   `json:"lastUsedAt,omitempty"`
	RevokedAt         string   `json:"revokedAt,omitempty"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

type GatewayKeyPolicy struct {
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	AllowedModels     []string `json:"allowedModels"`
	RequestsPerMinute int64    `json:"requestsPerMinute"`
	MonthlyTokenLimit int64    `json:"monthlyTokenLimit"`
	MaxConcurrency    int64    `json:"maxConcurrency"`
	ExpiresAt         string   `json:"expiresAt,omitempty"`
}

type GatewayModel struct {
	Alias       string `json:"alias"`
	TargetModel string `json:"targetModel"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type GatewayMonthlyUsage struct {
	GatewayKeyID string  `json:"gatewayKeyId"`
	MonthUTC     string  `json:"monthUtc"`
	RequestCount int64   `json:"requestCount"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	TotalTokens  int64   `json:"totalTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// GatewayRequestSummary intentionally exposes only accounting and attribution
// metadata. It must not grow raw request/response payloads or bearer tokens.
type GatewayRequestSummary struct {
	ID                string  `json:"id"`
	GatewayKeyID      string  `json:"gatewayKeyId,omitempty"`
	GatewayKeyName    string  `json:"gatewayKeyName,omitempty"`
	GatewayKeyPrefix  string  `json:"gatewayKeyPrefix,omitempty"`
	Provider          string  `json:"provider,omitempty"`
	CredentialID      string  `json:"credentialId,omitempty"`
	Model             string  `json:"model,omitempty"`
	InputTokens       int64   `json:"inputTokens,omitempty"`
	OutputTokens      int64   `json:"outputTokens,omitempty"`
	CachedInputTokens int64   `json:"cachedInputTokens,omitempty"`
	ReasoningTokens   int64   `json:"reasoningTokens,omitempty"`
	TotalTokens       int64   `json:"totalTokens,omitempty"`
	TTFTMS            int64   `json:"ttftMs,omitempty"`
	DurationMS        int64   `json:"durationMs,omitempty"`
	CostUSD           float64 `json:"costUsd,omitempty"`
	StopReason        string  `json:"stopReason,omitempty"`
	CreatedAt         string  `json:"createdAt"`
}

const gatewayKeyColumns = `id, name, key_prefix, token_hash, enabled, allowed_models_json, requests_per_minute, monthly_token_limit, max_concurrency, COALESCE(expires_at,''), COALESCE(last_used_at,''), COALESCE(revoked_at,''), created_at, updated_at`

func (s *Store) CreateGatewayKey(ctx context.Context, key GatewayKey) (GatewayKey, error) {
	canonical, allowedJSON, err := canonicalGatewayKeyForCreate(key)
	if err != nil {
		return GatewayKey{}, err
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	if err := validateGatewayID(canonical.ID); err != nil {
		return GatewayKey{}, err
	}
	now := Now()
	canonical.CreatedAt, canonical.UpdatedAt = now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO gateway_keys (id, name, key_prefix, token_hash, enabled, allowed_models_json, requests_per_minute, monthly_token_limit, max_concurrency, expires_at, last_used_at, revoked_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,''), NULL, NULL, ?, ?)`,
		canonical.ID, canonical.Name, canonical.KeyPrefix, canonical.TokenHash, boolInt(canonical.Enabled), allowedJSON,
		canonical.RequestsPerMinute, canonical.MonthlyTokenLimit, canonical.MaxConcurrency, canonical.ExpiresAt, now, now)
	if err != nil {
		if isUniqueConstraint(err) {
			return GatewayKey{}, fmt.Errorf("%w: gateway key already exists", ErrConflict)
		}
		return GatewayKey{}, errors.New("create gateway key failed")
	}
	return canonical, nil
}

func (s *Store) GetGatewayKey(ctx context.Context, id string) (GatewayKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return GatewayKey{}, sql.ErrNoRows
	}
	return scanGatewayKey(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+gatewayKeyColumns+` FROM gateway_keys WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) GetGatewayKeyByTokenHash(ctx context.Context, tokenHash string) (GatewayKey, error) {
	if !validGatewayTokenHash(tokenHash) {
		return GatewayKey{}, errors.New("invalid gateway token hash")
	}
	return scanGatewayKey(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT `+gatewayKeyColumns+` FROM gateway_keys WHERE token_hash = ?`, tokenHash).Scan(dest...)
	})
}

func (s *Store) ListGatewayKeys(ctx context.Context) ([]GatewayKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+gatewayKeyColumns+` FROM gateway_keys ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]GatewayKey, 0)
	for rows.Next() {
		key, err := scanGatewayKey(rows.Scan)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) UpdateGatewayKeyPolicy(ctx context.Context, id string, policy GatewayKeyPolicy) (GatewayKey, error) {
	current, err := s.GetGatewayKey(ctx, id)
	if err != nil {
		return GatewayKey{}, err
	}
	return s.UpdateGatewayKeyPolicyCAS(ctx, id, policy, current.UpdatedAt)
}

// UpdateGatewayKeyPolicyCAS changes a key policy only when the caller's view of
// updated_at is current. This prevents a stale admin PATCH from overwriting a
// more recent policy change.
func (s *Store) UpdateGatewayKeyPolicyCAS(ctx context.Context, id string, policy GatewayKeyPolicy, expectedUpdatedAt string) (GatewayKey, error) {
	id = strings.TrimSpace(id)
	expectedUpdatedAt = strings.TrimSpace(expectedUpdatedAt)
	if id == "" {
		return GatewayKey{}, sql.ErrNoRows
	}
	if expectedUpdatedAt == "" {
		return GatewayKey{}, errors.New("gateway key expected updated time is required")
	}
	canonical, allowedJSON, err := canonicalGatewayKeyPolicy(policy)
	if err != nil {
		return GatewayKey{}, err
	}
	current, err := s.GetGatewayKey(ctx, id)
	if err != nil {
		return GatewayKey{}, err
	}
	if current.RevokedAt != "" && canonical.Enabled {
		return GatewayKey{}, ErrGatewayKeyRevoked
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE gateway_keys SET name = ?, enabled = CASE WHEN revoked_at IS NULL THEN ? ELSE 0 END, allowed_models_json = ?, requests_per_minute = ?, monthly_token_limit = ?, max_concurrency = ?, expires_at = NULLIF(?,''), updated_at = ? WHERE id = ? AND updated_at = ?`,
		canonical.Name, boolInt(canonical.Enabled), allowedJSON, canonical.RequestsPerMinute, canonical.MonthlyTokenLimit, canonical.MaxConcurrency, canonical.ExpiresAt, now, id, expectedUpdatedAt)
	if err != nil {
		return GatewayKey{}, errors.New("update gateway key policy failed")
	}
	if affected, err := result.RowsAffected(); err != nil {
		return GatewayKey{}, err
	} else if affected == 0 {
		if _, err := s.GetGatewayKey(ctx, id); err != nil {
			return GatewayKey{}, err
		}
		return GatewayKey{}, fmt.Errorf("%w: gateway key changed", ErrConflict)
	}
	return s.GetGatewayKey(ctx, id)
}

func (s *Store) RotateGatewayKey(ctx context.Context, id, keyPrefix, tokenHash string) (GatewayKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return GatewayKey{}, sql.ErrNoRows
	}
	if !validGatewayKeyPrefix(keyPrefix) {
		return GatewayKey{}, errors.New("invalid gateway key prefix")
	}
	if !validGatewayTokenHash(tokenHash) {
		return GatewayKey{}, errors.New("invalid gateway token hash")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gateway_keys SET key_prefix = ?, token_hash = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL`, keyPrefix, tokenHash, Now(), id)
	if err != nil {
		if isUniqueConstraint(err) {
			return GatewayKey{}, fmt.Errorf("%w: gateway key already exists", ErrConflict)
		}
		return GatewayKey{}, errors.New("rotate gateway key failed")
	}
	if affected, err := result.RowsAffected(); err != nil {
		return GatewayKey{}, err
	} else if affected == 0 {
		key, getErr := s.GetGatewayKey(ctx, id)
		if getErr != nil {
			return GatewayKey{}, getErr
		}
		if key.RevokedAt != "" {
			return GatewayKey{}, ErrGatewayKeyRevoked
		}
		return GatewayKey{}, sql.ErrNoRows
	}
	return s.GetGatewayKey(ctx, id)
}

func (s *Store) RevokeGatewayKey(ctx context.Context, id string) (GatewayKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return GatewayKey{}, sql.ErrNoRows
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE gateway_keys SET enabled = 0, revoked_at = COALESCE(revoked_at, ?), updated_at = CASE WHEN revoked_at IS NULL THEN ? ELSE updated_at END WHERE id = ?`, now, now, id)
	if err != nil {
		return GatewayKey{}, errors.New("revoke gateway key failed")
	}
	if affected, err := result.RowsAffected(); err != nil {
		return GatewayKey{}, err
	} else if affected == 0 {
		return GatewayKey{}, sql.ErrNoRows
	}
	return s.GetGatewayKey(ctx, id)
}

func (s *Store) DeleteGatewayKey(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return sql.ErrNoRows
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_keys WHERE id = ?`, id)
	if err != nil {
		return errors.New("delete gateway key failed")
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) TouchGatewayKeyLastUsed(ctx context.Context, id, usedAt string) (GatewayKey, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return GatewayKey{}, sql.ErrNoRows
	}
	if usedAt == "" {
		usedAt = Now()
	} else {
		var err error
		usedAt, err = canonicalGatewayTime(usedAt, "last used time")
		if err != nil {
			return GatewayKey{}, err
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE gateway_keys SET last_used_at = CASE WHEN last_used_at IS NULL OR julianday(last_used_at) < julianday(?) THEN ? ELSE last_used_at END WHERE id = ? AND enabled = 1 AND revoked_at IS NULL`, usedAt, usedAt, id)
	if err != nil {
		return GatewayKey{}, errors.New("touch gateway key failed")
	}
	if affected, err := result.RowsAffected(); err != nil {
		return GatewayKey{}, err
	} else if affected == 0 {
		key, getErr := s.GetGatewayKey(ctx, id)
		if getErr != nil {
			return GatewayKey{}, getErr
		}
		if key.RevokedAt != "" {
			return GatewayKey{}, ErrGatewayKeyRevoked
		}
		return GatewayKey{}, sql.ErrNoRows
	}
	return s.GetGatewayKey(ctx, id)
}

func (s *Store) ListGatewayModels(ctx context.Context) ([]GatewayModel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT alias, target_model, enabled, created_at, updated_at FROM gateway_models ORDER BY alias ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := make([]GatewayModel, 0)
	for rows.Next() {
		model, err := scanGatewayModel(rows.Scan)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

func (s *Store) GetGatewayModel(ctx context.Context, alias string) (GatewayModel, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return GatewayModel{}, sql.ErrNoRows
	}
	return scanGatewayModel(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT alias, target_model, enabled, created_at, updated_at FROM gateway_models WHERE alias = ?`, alias).Scan(dest...)
	})
}

// CreateGatewayModel creates a new alias only when no explicit key policy
// already names it. Such a reference can be left by legacy data; recreating the
// alias would silently restore that key's authorization.
func (s *Store) CreateGatewayModel(ctx context.Context, model GatewayModel) (GatewayModel, error) {
	canonical, err := canonicalGatewayModel(model)
	if err != nil {
		return GatewayModel{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GatewayModel{}, err
	}
	defer tx.Rollback()
	referenced, err := gatewayModelAliasReferenced(ctx, tx, canonical.Alias)
	if err != nil {
		return GatewayModel{}, err
	}
	if referenced {
		return GatewayModel{}, fmt.Errorf("%w: gateway model alias is still referenced by a key policy", ErrConflict)
	}
	now := Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_models (alias, target_model, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, canonical.Alias, canonical.TargetModel, boolInt(canonical.Enabled), now, now); err != nil {
		if isUniqueConstraint(err) {
			return GatewayModel{}, fmt.Errorf("%w: gateway model alias already exists", ErrConflict)
		}
		return GatewayModel{}, errors.New("create gateway model failed")
	}
	if err := tx.Commit(); err != nil {
		return GatewayModel{}, err
	}
	return s.GetGatewayModel(ctx, canonical.Alias)
}

// UpsertGatewayModel is retained for internal setup callers. Admin mutations
// use CreateGatewayModel and UpdateGatewayModelCAS so they cannot resurrect a
// previously authorized alias or overwrite a concurrent edit.
func (s *Store) UpsertGatewayModel(ctx context.Context, model GatewayModel) (GatewayModel, error) {
	canonical, err := canonicalGatewayModel(model)
	if err != nil {
		return GatewayModel{}, err
	}
	now := Now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO gateway_models (alias, target_model, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(alias) DO UPDATE SET target_model = excluded.target_model, enabled = excluded.enabled, updated_at = excluded.updated_at`, canonical.Alias, canonical.TargetModel, boolInt(canonical.Enabled), now, now)
	if err != nil {
		return GatewayModel{}, errors.New("upsert gateway model failed")
	}
	return s.GetGatewayModel(ctx, canonical.Alias)
}

// UpdateGatewayModelCAS updates or renames a model alias only if its
// updated_at matches expectedUpdatedAt. Rename and whitelist rewrites commit as
// one transaction so a key can never be left with an accidentally open policy.
func (s *Store) UpdateGatewayModelCAS(ctx context.Context, oldAlias string, model GatewayModel, expectedUpdatedAt string) (GatewayModel, error) {
	oldAlias = strings.TrimSpace(oldAlias)
	expectedUpdatedAt = strings.TrimSpace(expectedUpdatedAt)
	if oldAlias == "" {
		return GatewayModel{}, sql.ErrNoRows
	}
	if expectedUpdatedAt == "" {
		return GatewayModel{}, errors.New("gateway model expected updated time is required")
	}
	canonical, err := canonicalGatewayModel(model)
	if err != nil {
		return GatewayModel{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GatewayModel{}, err
	}
	defer tx.Rollback()

	var currentUpdatedAt string
	if err := tx.QueryRowContext(ctx, `SELECT updated_at FROM gateway_models WHERE alias = ?`, oldAlias).Scan(&currentUpdatedAt); err != nil {
		return GatewayModel{}, err
	}
	if currentUpdatedAt != expectedUpdatedAt {
		return GatewayModel{}, fmt.Errorf("%w: gateway model changed", ErrConflict)
	}
	if canonical.Alias != oldAlias {
		var collision int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_models WHERE alias = ?`, canonical.Alias).Scan(&collision); err != nil {
			return GatewayModel{}, err
		}
		if collision != 0 {
			return GatewayModel{}, fmt.Errorf("%w: gateway model alias already exists", ErrConflict)
		}
		referenced, err := gatewayModelAliasReferenced(ctx, tx, canonical.Alias)
		if err != nil {
			return GatewayModel{}, err
		}
		if referenced {
			return GatewayModel{}, fmt.Errorf("%w: gateway model alias is still referenced by a key policy", ErrConflict)
		}
	}

	now := Now()
	result, err := tx.ExecContext(ctx, `UPDATE gateway_models SET alias = ?, target_model = ?, enabled = ?, updated_at = ? WHERE alias = ? AND updated_at = ?`, canonical.Alias, canonical.TargetModel, boolInt(canonical.Enabled), now, oldAlias, expectedUpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return GatewayModel{}, fmt.Errorf("%w: gateway model alias already exists", ErrConflict)
		}
		return GatewayModel{}, errors.New("update gateway model failed")
	}
	if affected, err := result.RowsAffected(); err != nil {
		return GatewayModel{}, err
	} else if affected != 1 {
		return GatewayModel{}, fmt.Errorf("%w: gateway model changed", ErrConflict)
	}
	if canonical.Alias != oldAlias {
		if err := renameGatewayAllowedModels(ctx, tx, oldAlias, canonical.Alias, now); err != nil {
			return GatewayModel{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return GatewayModel{}, err
	}
	return s.GetGatewayModel(ctx, canonical.Alias)
}

func (s *Store) DeleteGatewayModel(ctx context.Context, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_models WHERE alias = ?`, alias).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return sql.ErrNoRows
	}
	referenced, err := gatewayModelAliasReferenced(ctx, tx, alias)
	if err != nil {
		return err
	}
	if referenced {
		return fmt.Errorf("%w: gateway model alias is referenced by a key policy", ErrConflict)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM gateway_models WHERE alias = ?`, alias)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("%w: gateway model changed", ErrConflict)
	}
	return tx.Commit()
}

func gatewayModelAliasReferenced(ctx context.Context, tx *sql.Tx, alias string) (bool, error) {
	var referenced int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_keys AS key, json_each(key.allowed_models_json) AS allowed WHERE allowed.value = ?)`, alias).Scan(&referenced)
	return referenced != 0, err
}

func renameGatewayAllowedModels(ctx context.Context, tx *sql.Tx, oldAlias, newAlias, updatedAt string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, allowed_models_json FROM gateway_keys`)
	if err != nil {
		return err
	}
	type policyUpdate struct {
		id      string
		encoded string
	}
	updates := make([]policyUpdate, 0)
	for rows.Next() {
		var id, encoded string
		if err := rows.Scan(&id, &encoded); err != nil {
			_ = rows.Close()
			return err
		}
		var models []string
		if err := json.Unmarshal([]byte(encoded), &models); err != nil {
			_ = rows.Close()
			return errors.New("invalid stored gateway key policy")
		}
		changed := false
		unique := make(map[string]struct{}, len(models))
		for _, alias := range models {
			if alias == oldAlias {
				alias = newAlias
				changed = true
			}
			unique[alias] = struct{}{}
		}
		if !changed {
			continue
		}
		normalized := make([]string, 0, len(unique))
		for alias := range unique {
			normalized = append(normalized, alias)
		}
		sort.Strings(normalized)
		data, err := json.Marshal(normalized)
		if err != nil || len(data) > 32768 {
			_ = rows.Close()
			return errors.New("gateway allowed models are too large")
		}
		updates = append(updates, policyUpdate{id: id, encoded: string(data)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_keys SET allowed_models_json = ?, updated_at = ? WHERE id = ?`, update.encoded, updatedAt, update.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListRecentGatewayRequests(ctx context.Context, limit int) ([]GatewayRequestSummary, error) {
	if limit <= 0 {
		return nil, errors.New("gateway request limit must be positive")
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		r.id,
		COALESCE(r.gateway_key_id,''),
		COALESCE(k.name,''),
		COALESCE(k.key_prefix,''),
		COALESCE(r.provider,''),
		COALESCE(r.credential_id,''),
		COALESCE(r.model,''),
		r.input_tokens,
		r.output_tokens,
		r.cached_input_tokens,
		r.reasoning_tokens,
		r.input_tokens + r.output_tokens,
		r.ttft_ms,
		r.duration_ms,
		r.cost_usd,
		COALESCE(r.stop_reason,''),
		r.created_at
	FROM api_requests AS r
	LEFT JOIN gateway_keys AS k ON k.id = r.gateway_key_id
	WHERE r.kind = 'gateway'
	ORDER BY julianday(r.created_at) DESC, r.id DESC
	LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]GatewayRequestSummary, 0, limit)
	for rows.Next() {
		var request GatewayRequestSummary
		if err := rows.Scan(
			&request.ID,
			&request.GatewayKeyID,
			&request.GatewayKeyName,
			&request.GatewayKeyPrefix,
			&request.Provider,
			&request.CredentialID,
			&request.Model,
			&request.InputTokens,
			&request.OutputTokens,
			&request.CachedInputTokens,
			&request.ReasoningTokens,
			&request.TotalTokens,
			&request.TTFTMS,
			&request.DurationMS,
			&request.CostUSD,
			&request.StopReason,
			&request.CreatedAt,
		); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return requests, nil
}

func (s *Store) GetGatewayKeyMonthlyUsage(ctx context.Context, gatewayKeyID string, month time.Time) (GatewayMonthlyUsage, error) {
	gatewayKeyID = strings.TrimSpace(gatewayKeyID)
	if gatewayKeyID == "" {
		return GatewayMonthlyUsage{}, errors.New("gateway key id is required")
	}
	if month.IsZero() {
		return GatewayMonthlyUsage{}, errors.New("gateway usage month is required")
	}
	month = month.UTC()
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	usage := GatewayMonthlyUsage{GatewayKeyID: gatewayKeyID, MonthUTC: start.Format("2006-01")}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(input_tokens),0) + COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost_usd),0) FROM api_requests WHERE gateway_key_id = ? AND julianday(created_at) >= julianday(?) AND julianday(created_at) < julianday(?)`, gatewayKeyID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)).Scan(&usage.RequestCount, &usage.InputTokens, &usage.OutputTokens, &usage.TotalTokens, &usage.CostUSD)
	if err != nil {
		return GatewayMonthlyUsage{}, err
	}
	return usage, nil
}

func canonicalGatewayKeyForCreate(key GatewayKey) (GatewayKey, string, error) {
	key.ID = strings.TrimSpace(key.ID)
	if key.ID != "" {
		if err := validateGatewayID(key.ID); err != nil {
			return GatewayKey{}, "", err
		}
	}
	if key.LastUsedAt != "" || key.RevokedAt != "" {
		return GatewayKey{}, "", errors.New("new gateway key cannot have usage or revocation timestamps")
	}
	policy, allowedJSON, err := canonicalGatewayKeyPolicy(GatewayKeyPolicy{
		Name: key.Name, Enabled: key.Enabled, AllowedModels: key.AllowedModels,
		RequestsPerMinute: key.RequestsPerMinute, MonthlyTokenLimit: key.MonthlyTokenLimit,
		MaxConcurrency: key.MaxConcurrency, ExpiresAt: key.ExpiresAt,
	})
	if err != nil {
		return GatewayKey{}, "", err
	}
	key.Name = policy.Name
	key.Enabled = policy.Enabled
	key.AllowedModels = policy.AllowedModels
	key.RequestsPerMinute = policy.RequestsPerMinute
	key.MonthlyTokenLimit = policy.MonthlyTokenLimit
	key.MaxConcurrency = policy.MaxConcurrency
	key.ExpiresAt = policy.ExpiresAt
	key.KeyPrefix = strings.TrimSpace(key.KeyPrefix)
	if !validGatewayKeyPrefix(key.KeyPrefix) {
		return GatewayKey{}, "", errors.New("invalid gateway key prefix")
	}
	if !validGatewayTokenHash(key.TokenHash) {
		return GatewayKey{}, "", errors.New("invalid gateway token hash")
	}
	key.CreatedAt, key.UpdatedAt = "", ""
	return key, allowedJSON, nil
}

func canonicalGatewayKeyPolicy(policy GatewayKeyPolicy) (GatewayKeyPolicy, string, error) {
	policy.Name = strings.TrimSpace(policy.Name)
	if err := validateGatewayText(policy.Name, 120, "name"); err != nil {
		return GatewayKeyPolicy{}, "", err
	}
	if policy.RequestsPerMinute < 0 || policy.MonthlyTokenLimit < 0 || policy.MaxConcurrency < 0 {
		return GatewayKeyPolicy{}, "", errors.New("gateway key limits must not be negative")
	}
	models, err := normalizeGatewayModels(policy.AllowedModels)
	if err != nil {
		return GatewayKeyPolicy{}, "", err
	}
	policy.AllowedModels = models
	if policy.ExpiresAt != "" {
		policy.ExpiresAt, err = canonicalGatewayTime(policy.ExpiresAt, "expiration time")
		if err != nil {
			return GatewayKeyPolicy{}, "", err
		}
	}
	encoded, err := json.Marshal(models)
	if err != nil || len(encoded) > 32768 {
		return GatewayKeyPolicy{}, "", errors.New("gateway allowed models are too large")
	}
	return policy, string(encoded), nil
}

func normalizeGatewayModels(models []string) ([]string, error) {
	unique := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if !validGatewayModelRef(model, 256, false) {
			return nil, errors.New("invalid gateway allowed model")
		}
		unique[model] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for model := range unique {
		normalized = append(normalized, model)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func canonicalGatewayModel(model GatewayModel) (GatewayModel, error) {
	model.Alias = strings.TrimSpace(model.Alias)
	model.TargetModel = strings.TrimSpace(model.TargetModel)
	if !validGatewayModelRef(model.Alias, 128, true) {
		return GatewayModel{}, errors.New("invalid gateway model alias")
	}
	if !validGatewayModelRef(model.TargetModel, 256, false) {
		return GatewayModel{}, errors.New("invalid gateway target model")
	}
	model.CreatedAt, model.UpdatedAt = "", ""
	return model, nil
}

func scanGatewayKey(scan func(...any) error) (GatewayKey, error) {
	var key GatewayKey
	var enabled int
	var allowedJSON string
	if err := scan(&key.ID, &key.Name, &key.KeyPrefix, &key.TokenHash, &enabled, &allowedJSON, &key.RequestsPerMinute, &key.MonthlyTokenLimit, &key.MaxConcurrency, &key.ExpiresAt, &key.LastUsedAt, &key.RevokedAt, &key.CreatedAt, &key.UpdatedAt); err != nil {
		return GatewayKey{}, err
	}
	if enabled != 0 && enabled != 1 {
		return GatewayKey{}, errors.New("invalid stored gateway key")
	}
	key.Enabled = enabled == 1
	if !validGatewayTokenHash(key.TokenHash) || !validGatewayKeyPrefix(key.KeyPrefix) {
		return GatewayKey{}, errors.New("invalid stored gateway key")
	}
	if err := json.Unmarshal([]byte(allowedJSON), &key.AllowedModels); err != nil {
		return GatewayKey{}, errors.New("invalid stored gateway key policy")
	}
	models, err := normalizeGatewayModels(key.AllowedModels)
	if err != nil {
		return GatewayKey{}, errors.New("invalid stored gateway key policy")
	}
	key.AllowedModels = models
	return key, nil
}

func scanGatewayModel(scan func(...any) error) (GatewayModel, error) {
	var model GatewayModel
	var enabled int
	if err := scan(&model.Alias, &model.TargetModel, &enabled, &model.CreatedAt, &model.UpdatedAt); err != nil {
		return GatewayModel{}, err
	}
	if enabled != 0 && enabled != 1 || !validGatewayModelRef(model.Alias, 128, true) || !validGatewayModelRef(model.TargetModel, 256, false) {
		return GatewayModel{}, errors.New("invalid stored gateway model")
	}
	model.Enabled = enabled == 1
	return model, nil
}

func validateGatewayID(id string) error {
	if err := validateGatewayText(strings.TrimSpace(id), 128, "id"); err != nil {
		return err
	}
	return nil
}

func validateGatewayText(value string, maxBytes int, name string) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("invalid gateway key %s", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("invalid gateway key %s", name)
		}
	}
	return nil
}

func validGatewayKeyPrefix(prefix string) bool {
	if prefix == "" || len(prefix) > 32 || prefix != strings.TrimSpace(prefix) {
		return false
	}
	for index, r := range prefix {
		if r > unicode.MaxASCII || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || index > 0 && (r == '.' || r == '_' || r == '-')) {
			return false
		}
	}
	return true
}

func validGatewayTokenHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, r := range hash {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validGatewayModelRef(value string, maxBytes int, alias bool) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for index, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			return false
		}
		if alias {
			if r > unicode.MaxASCII || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || index > 0 && (r == '.' || r == '_' || r == '-' || r == ':' || r == '/')) {
				return false
			}
		}
	}
	return true
}

func canonicalGatewayTime(value, name string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid gateway key %s", name)
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}
