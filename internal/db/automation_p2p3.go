package db

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	P2P3PayloadMaxBytes             = 32 * 1024
	SchedulePromptMaxBytes          = 128 * 1024
	P2P3MaxListLimit                = 200
	DefaultPairingMaxFailedAttempts = 5
	DefaultPairingLockDuration      = 15 * time.Minute
)

type Schedule struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	AgentID         string `json:"agentId"`
	Expression      string `json:"expression"`
	Timezone        string `json:"timezone"`
	Prompt          string `json:"prompt"`
	PermissionMode  string `json:"permissionMode"`
	EnvironmentMode string `json:"environmentMode"`
	NarratorMode    string `json:"narratorMode"`
	Enabled         bool   `json:"enabled"`
	NextRunAt       string `json:"nextRunAt,omitempty"`
	LastRunAt       string `json:"lastRunAt,omitempty"`
	LastRunID       string `json:"lastRunId,omitempty"`
	LastOutcome     string `json:"lastOutcome,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	LeaseUntil      string `json:"leaseUntil,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type ScheduleListOptions struct {
	AgentID string
	Enabled *bool
	Limit   int
}

type ScheduleStats struct {
	Total    int64 `json:"total"`
	Enabled  int64 `json:"enabled"`
	Disabled int64 `json:"disabled"`
	Due      int64 `json:"due"`
	Leased   int64 `json:"leased"`
	Failed   int64 `json:"failed"`
}

type NotificationDelivery struct {
	ID                  string          `json:"id"`
	DedupeKey           string          `json:"dedupeKey"`
	SinkType            string          `json:"sinkType"`
	SinkID              string          `json:"sinkId"`
	EventType           string          `json:"eventType"`
	AgentID             string          `json:"agentId,omitempty"`
	RunID               string          `json:"runId,omitempty"`
	ToolUseID           string          `json:"toolUseId,omitempty"`
	ExecutionGeneration int64           `json:"executionGeneration"`
	PayloadJSON         json.RawMessage `json:"payload"`
	Status              string          `json:"status"`
	AttemptCount        int             `json:"attemptCount"`
	MaxAttempts         int             `json:"maxAttempts"`
	NextAttemptAt       string          `json:"nextAttemptAt"`
	LeaseUntil          string          `json:"leaseUntil,omitempty"`
	LastHTTPStatus      int             `json:"lastHttpStatus,omitempty"`
	LastError           string          `json:"lastError,omitempty"`
	DeliveredAt         string          `json:"deliveredAt,omitempty"`
	CreatedAt           string          `json:"createdAt"`
	UpdatedAt           string          `json:"updatedAt"`
}

type NotificationDeliveryListOptions struct {
	Status    string
	SinkType  string
	AgentID   string
	RunID     string
	EventType string
	Limit     int
	Offset    int
}

type NotificationDeliveryStats struct {
	Total     int64 `json:"total"`
	Queued    int64 `json:"queued"`
	Inflight  int64 `json:"inflight"`
	RetryWait int64 `json:"retryWait"`
	Delivered int64 `json:"delivered"`
	Dead      int64 `json:"dead"`
	Attempts  int64 `json:"attempts"`
	HTTPError int64 `json:"httpError"`
	Exhausted int64 `json:"exhausted"`
}

type ChannelPairing struct {
	ID                 string `json:"id"`
	ConnectionID       string `json:"connectionId"`
	AgentID            string `json:"agentId"`
	Status             string `json:"status"`
	CodeHash           string `json:"-"`
	ExpiresAt          string `json:"expiresAt,omitempty"`
	ChatID             string `json:"chatId,omitempty"`
	UserID             string `json:"userId,omitempty"`
	FailedAttempts     int    `json:"failedAttempts"`
	LockedUntil        string `json:"lockedUntil,omitempty"`
	CredentialRevision int64  `json:"credentialRevision"`
	PairedAt           string `json:"pairedAt,omitempty"`
	RevokedAt          string `json:"revokedAt,omitempty"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

type ChannelPairingListOptions struct {
	ConnectionID string
	AgentID      string
	Status       string
	Limit        int
}

type ChannelPairingStats struct {
	Total   int64 `json:"total"`
	Pending int64 `json:"pending"`
	Active  int64 `json:"active"`
	Revoked int64 `json:"revoked"`
	Locked  int64 `json:"locked"`
}

type ChannelEvent struct {
	ID              string          `json:"id"`
	ConnectionID    string          `json:"connectionId"`
	ExternalEventID string          `json:"externalEventId"`
	EventType       string          `json:"eventType"`
	AgentID         string          `json:"agentId,omitempty"`
	RunID           string          `json:"runId,omitempty"`
	ToolUseID       string          `json:"toolUseId,omitempty"`
	ChatID          string          `json:"chatId,omitempty"`
	UserID          string          `json:"userId,omitempty"`
	PayloadJSON     json.RawMessage `json:"payload"`
	OccurredAt      string          `json:"occurredAt,omitempty"`
	ProcessedAt     string          `json:"processedAt,omitempty"`
	CreatedAt       string          `json:"createdAt"`
}

type ChannelEventListOptions struct {
	ConnectionID    string
	AgentID         string
	EventType       string
	OnlyUnprocessed bool
	Limit           int
	Offset          int
}

type ChannelCursor struct {
	ConnectionID string `json:"connectionId"`
	Offset       int64  `json:"offset"`
	UpdatedAt    string `json:"updatedAt"`
}

type ChannelStats struct {
	Pairings ChannelPairingStats `json:"pairings"`
	Events   int64               `json:"events"`
	Pending  int64               `json:"pendingEvents"`
}

type DeviceActionRequest struct {
	ID           string          `json:"id"`
	ConnectionID string          `json:"connectionId"`
	EntityID     string          `json:"entityId"`
	Domain       string          `json:"domain"`
	Service      string          `json:"service"`
	PayloadJSON  json.RawMessage `json:"payload"`
	Risk         string          `json:"risk"`
	Status       string          `json:"status"`
	RequestedBy  string          `json:"requestedBy"`
	ApprovedBy   string          `json:"approvedBy,omitempty"`
	ExpiresAt    string          `json:"expiresAt"`
	LastError    string          `json:"lastError,omitempty"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
	CompletedAt  string          `json:"completedAt,omitempty"`
}

type DeviceActionRequestListOptions struct {
	ConnectionID string
	Status       string
	Risk         string
	Limit        int
	Offset       int
}

type DeviceActionRequestStats struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Approved  int64 `json:"approved"`
	Executing int64 `json:"executing"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Denied    int64 `json:"denied"`
	Expired   int64 `json:"expired"`
	HighRisk  int64 `json:"highRisk"`
}

type p2p3Scanner func(dest ...any) error

const scheduleSelectSQL = `SELECT id, name, agent_id, expression, timezone, prompt, permission_mode, environment_mode, narrator_mode, enabled, COALESCE(next_run_at,''), COALESCE(last_run_at,''), COALESCE(last_run_id,''), COALESCE(last_outcome,''), COALESCE(last_error,''), COALESCE(lease_until,''), created_at, updated_at FROM schedules`
const notificationDeliverySelectSQL = `SELECT id, dedupe_key, sink_type, sink_id, event_type, COALESCE(agent_id,''), COALESCE(run_id,''), COALESCE(tool_use_id,''), COALESCE(execution_generation,0), payload_json, status, attempt_count, max_attempts, next_attempt_at, COALESCE(lease_until,''), COALESCE(last_http_status,0), COALESCE(last_error,''), COALESCE(delivered_at,''), created_at, updated_at FROM notification_deliveries`
const channelPairingSelectSQL = `SELECT id, connection_id, agent_id, status, COALESCE(code_hash,''), COALESCE(expires_at,''), COALESCE(chat_id,''), COALESCE(user_id,''), failed_attempts, COALESCE(locked_until,''), credential_revision, COALESCE(paired_at,''), COALESCE(revoked_at,''), created_at, updated_at FROM channel_pairings`
const channelEventSelectSQL = `SELECT id, connection_id, external_event_id, event_type, COALESCE(agent_id,''), COALESCE(run_id,''), COALESCE(tool_use_id,''), COALESCE(chat_id,''), COALESCE(user_id,''), payload_json, COALESCE(occurred_at,''), COALESCE(processed_at,''), created_at FROM channel_events`
const deviceActionRequestSelectSQL = `SELECT id, connection_id, entity_id, domain, service, payload_json, risk, status, requested_by, COALESCE(approved_by,''), expires_at, COALESCE(last_error,''), created_at, updated_at, COALESCE(completed_at,'') FROM device_action_requests`

func (s *Store) CreateSchedule(ctx context.Context, schedule Schedule) (Schedule, error) {
	canonical, err := canonicalSchedule(schedule, true)
	if err != nil {
		return Schedule{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO schedules (id, name, agent_id, expression, timezone, prompt, permission_mode, environment_mode, narrator_mode, enabled, next_run_at, last_run_at, last_run_id, last_outcome, last_error, lease_until, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, ?)`, canonical.ID, canonical.Name, canonical.AgentID, canonical.Expression, canonical.Timezone, canonical.Prompt, canonical.PermissionMode, canonical.EnvironmentMode, canonical.NarratorMode, boolInt(canonical.Enabled), canonical.NextRunAt, canonical.LastRunAt, canonical.LastRunID, canonical.LastOutcome, canonical.LastError, canonical.LeaseUntil, canonical.CreatedAt, canonical.UpdatedAt)
	if err != nil {
		return Schedule{}, fmt.Errorf("create schedule: %w", err)
	}
	return canonical, nil
}

func (s *Store) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Schedule{}, sql.ErrNoRows
	}
	return scanSchedule(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, scheduleSelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) ListSchedules(ctx context.Context, args ...any) ([]Schedule, error) {
	options, err := parseScheduleListOptions(args)
	if err != nil {
		return nil, err
	}
	query := scheduleSelectSQL + ` WHERE 1 = 1`
	params := make([]any, 0, 4)
	if options.AgentID != "" {
		query += ` AND agent_id = ?`
		params = append(params, options.AgentID)
	}
	if options.Enabled != nil {
		query += ` AND enabled = ?`
		params = append(params, boolInt(*options.Enabled))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	params = append(params, normalizedP2P3Limit(options.Limit, 50))
	rows, err := s.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Schedule, 0)
	for rows.Next() {
		item, scanErr := scanSchedule(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateSchedule(ctx context.Context, schedule Schedule) (Schedule, error) {
	expectedUpdatedAt := p2p3TimeCAS(schedule.UpdatedAt)
	if expectedUpdatedAt == "" {
		return Schedule{}, errors.New("schedule updated_at is required for CAS update")
	}
	canonical, err := canonicalSchedule(schedule, false)
	if err != nil {
		return Schedule{}, err
	}
	canonical.UpdatedAt = nextP2P3UpdatedAt(expectedUpdatedAt)
	result, err := s.db.ExecContext(ctx, `UPDATE schedules SET name = ?, agent_id = ?, expression = ?, timezone = ?, prompt = ?, permission_mode = ?, environment_mode = ?, narrator_mode = ?, enabled = ?, next_run_at = NULLIF(?,''), last_run_at = NULLIF(?,''), last_run_id = NULLIF(?,''), last_outcome = NULLIF(?,''), last_error = NULLIF(?,''), lease_until = NULLIF(?,''), updated_at = ? WHERE id = ? AND updated_at = ?`, canonical.Name, canonical.AgentID, canonical.Expression, canonical.Timezone, canonical.Prompt, canonical.PermissionMode, canonical.EnvironmentMode, canonical.NarratorMode, boolInt(canonical.Enabled), canonical.NextRunAt, canonical.LastRunAt, canonical.LastRunID, canonical.LastOutcome, canonical.LastError, canonical.LeaseUntil, canonical.UpdatedAt, canonical.ID, expectedUpdatedAt)
	if err != nil {
		return Schedule{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "schedules", canonical.ID, "update schedule"); err != nil {
		return Schedule{}, err
	}
	return s.GetSchedule(ctx, canonical.ID)
}

func (s *Store) DeleteSchedule(ctx context.Context, id string, expectedUpdatedAt ...string) error {
	id = strings.TrimSpace(id)
	if err := validateP2P3Text("schedule id", id, 128, true, false); err != nil {
		return err
	}
	query := `DELETE FROM schedules WHERE id = ?`
	params := []any{id}
	if expected := p2p3TimeCAS(firstOrEmpty(expectedUpdatedAt)); expected != "" {
		query += ` AND updated_at = ?`
		params = append(params, expected)
	}
	result, err := s.db.ExecContext(ctx, query, params...)
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "schedules", id, "delete schedule")
}

func (s *Store) ClaimDueSchedules(ctx context.Context, now, leaseUntil string, limit int) ([]Schedule, error) {
	now, err := canonicalP2P3Time("schedule claim time", now, true)
	if err != nil {
		return nil, err
	}
	leaseUntil, err = canonicalP2P3Time("schedule lease_until", leaseUntil, true)
	if err != nil {
		return nil, err
	}
	if !p2p3TimeAfter(leaseUntil, now) {
		return nil, errors.New("schedule lease_until must be after claim time")
	}
	limit = normalizedP2P3Limit(limit, 20)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM schedules WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ? AND (lease_until IS NULL OR lease_until <= ?) ORDER BY next_run_at ASC, id ASC LIMIT ?`, now, now, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	claimed := make([]Schedule, 0, len(ids))
	for _, id := range ids {
		updatedAt := Now()
		result, err := tx.ExecContext(ctx, `UPDATE schedules SET lease_until = ?, updated_at = ? WHERE id = ? AND enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ? AND (lease_until IS NULL OR lease_until <= ?)`, leaseUntil, updatedAt, id, now, now)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected != 1 {
			continue
		}
		item, err := scanSchedule(func(dest ...any) error {
			return tx.QueryRowContext(ctx, scheduleSelectSQL+` WHERE id = ?`, id).Scan(dest...)
		})
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *Store) ReleaseScheduleLease(ctx context.Context, id, leaseUntil string) error {
	id = strings.TrimSpace(id)
	// The lease predicate must use the same canonical layout the claim wrote,
	// otherwise an equivalent instant with trailing zeros never matches.
	leaseUntil, err := canonicalP2P3Time("schedule lease_until", leaseUntil, true)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE schedules SET lease_until = NULL, updated_at = ? WHERE id = ? AND lease_until = ?`, Now(), id, leaseUntil)
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "schedules", id, "release schedule lease")
}

// ClaimScheduleNow takes the same lease ClaimDueSchedules takes, for a run the
// user asked for by hand rather than one that came due. It exists so the lease
// is written through the same canonicalization: a caller formatting the
// timestamp itself stores text that GetSchedule then reads back in a different
// shape, and every compare-and-swap against it -- clearing the lease, recording
// the outcome -- silently matches no rows and reports a conflict.
//
// Returns sql.ErrNoRows when the schedule is gone and ErrConflict when another
// run already holds the lease.
func (s *Store) ClaimScheduleNow(ctx context.Context, id, now, leaseUntil string) (Schedule, error) {
	id = strings.TrimSpace(id)
	now, err := canonicalP2P3Time("schedule claim time", now, true)
	if err != nil {
		return Schedule{}, err
	}
	leaseUntil, err = canonicalP2P3Time("schedule lease_until", leaseUntil, true)
	if err != nil {
		return Schedule{}, err
	}
	if !p2p3TimeAfter(leaseUntil, now) {
		return Schedule{}, errors.New("schedule lease_until must be after claim time")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE schedules SET lease_until = ?, updated_at = ? WHERE id = ? AND (lease_until IS NULL OR lease_until <= ?)`, leaseUntil, Now(), id, now)
	if err != nil {
		return Schedule{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "schedules", id, "claim schedule"); err != nil {
		return Schedule{}, err
	}
	return s.GetSchedule(ctx, id)
}

func (s *Store) RecordScheduleRun(ctx context.Context, id, leaseUntil, runID, outcome, lastError, nextRunAt string) (Schedule, error) {
	id = strings.TrimSpace(id)
	runID = strings.TrimSpace(runID)
	outcome = strings.TrimSpace(outcome)
	lastError = strings.TrimSpace(lastError)
	// Match the canonical lease value persisted by ClaimDueSchedules.
	leaseUntil, leaseErr := canonicalP2P3Time("schedule lease_until", leaseUntil, true)
	if leaseErr != nil {
		return Schedule{}, leaseErr
	}
	if !validScheduleOutcome(outcome) || outcome == "" {
		return Schedule{}, errors.New("invalid schedule outcome")
	}
	if err := validateP2P3Text("schedule run id", runID, 128, true, false); err != nil {
		return Schedule{}, err
	}
	if err := validateP2P3Text("schedule last error", lastError, 4096, false, false); err != nil {
		return Schedule{}, err
	}
	var err error
	nextRunAt, err = canonicalP2P3Time("schedule next_run_at", nextRunAt, false)
	if err != nil {
		return Schedule{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE schedules SET next_run_at = NULLIF(?,''), last_run_at = ?, last_run_id = ?, last_outcome = ?, last_error = NULLIF(?,''), lease_until = NULL, updated_at = ? WHERE id = ? AND lease_until = ?`, nextRunAt, now, runID, outcome, lastError, now, id, leaseUntil)
	if err != nil {
		return Schedule{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "schedules", id, "record schedule run"); err != nil {
		return Schedule{}, err
	}
	return s.GetSchedule(ctx, id)
}

func (s *Store) CompleteScheduleRun(ctx context.Context, id, leaseUntil, runID, outcome, lastError, nextRunAt string) (Schedule, error) {
	return s.RecordScheduleRun(ctx, id, leaseUntil, runID, outcome, lastError, nextRunAt)
}

func (s *Store) ScheduleStats(ctx context.Context, at string) (ScheduleStats, error) {
	if strings.TrimSpace(at) == "" {
		at = Now()
	}
	var err error
	at, err = canonicalP2P3Time("schedule stats time", at, true)
	if err != nil {
		return ScheduleStats{}, err
	}
	var stats ScheduleStats
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled = 1 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN enabled = 0 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ? AND (lease_until IS NULL OR lease_until <= ?) THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN lease_until > ? THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN last_outcome IN ('failure','error') THEN 1 ELSE 0 END),0) FROM schedules`, at, at, at).Scan(&stats.Total, &stats.Enabled, &stats.Disabled, &stats.Due, &stats.Leased, &stats.Failed)
	return stats, err
}

func canonicalSchedule(schedule Schedule, create bool) (Schedule, error) {
	schedule.ID = strings.TrimSpace(schedule.ID)
	schedule.Name = strings.TrimSpace(schedule.Name)
	schedule.AgentID = strings.TrimSpace(schedule.AgentID)
	schedule.Expression = strings.TrimSpace(schedule.Expression)
	schedule.Timezone = strings.TrimSpace(schedule.Timezone)
	schedule.Prompt = strings.TrimSpace(schedule.Prompt)
	schedule.PermissionMode = strings.TrimSpace(schedule.PermissionMode)
	schedule.EnvironmentMode = strings.TrimSpace(schedule.EnvironmentMode)
	schedule.NarratorMode = strings.TrimSpace(schedule.NarratorMode)
	schedule.NextRunAt = strings.TrimSpace(schedule.NextRunAt)
	schedule.LastRunAt = strings.TrimSpace(schedule.LastRunAt)
	schedule.LastRunID = strings.TrimSpace(schedule.LastRunID)
	schedule.LastOutcome = strings.TrimSpace(schedule.LastOutcome)
	schedule.LastError = strings.TrimSpace(schedule.LastError)
	schedule.LeaseUntil = strings.TrimSpace(schedule.LeaseUntil)
	if schedule.ID == "" && create {
		schedule.ID = NewID()
	}
	if schedule.Timezone == "" {
		schedule.Timezone = "UTC"
	}
	if schedule.PermissionMode == "" {
		schedule.PermissionMode = "readOnly"
	}
	if schedule.EnvironmentMode == "" {
		schedule.EnvironmentMode = "workline"
	}
	if schedule.NarratorMode == "" {
		schedule.NarratorMode = "reuse"
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
		token    bool
	}{
		{"schedule id", schedule.ID, 128, true, false},
		{"schedule name", schedule.Name, 120, true, false},
		{"schedule agent id", schedule.AgentID, 128, true, false},
		{"schedule expression", schedule.Expression, 256, true, false},
		{"schedule timezone", schedule.Timezone, 128, true, false},
		{"schedule prompt", schedule.Prompt, SchedulePromptMaxBytes, true, false},
		{"schedule last run id", schedule.LastRunID, 128, false, false},
		{"schedule last error", schedule.LastError, 4096, false, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, field.token); err != nil {
			return Schedule{}, err
		}
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return Schedule{}, errors.New("invalid schedule timezone")
	}
	if schedule.PermissionMode != "readOnly" && schedule.PermissionMode != "acceptEdits" {
		return Schedule{}, errors.New("invalid schedule permission mode")
	}
	if schedule.EnvironmentMode != "workline" && schedule.EnvironmentMode != "standalone" {
		return Schedule{}, errors.New("invalid schedule environment mode")
	}
	if schedule.NarratorMode != "reuse" && schedule.NarratorMode != "new" {
		return Schedule{}, errors.New("invalid schedule narrator mode")
	}
	if !validScheduleOutcome(schedule.LastOutcome) {
		return Schedule{}, errors.New("invalid schedule last outcome")
	}
	var err error
	for name, value := range map[string]*string{"schedule next_run_at": &schedule.NextRunAt, "schedule last_run_at": &schedule.LastRunAt, "schedule lease_until": &schedule.LeaseUntil} {
		*value, err = canonicalP2P3Time(name, *value, false)
		if err != nil {
			return Schedule{}, err
		}
	}
	if create {
		now := Now()
		if schedule.CreatedAt == "" {
			schedule.CreatedAt = now
		}
		if schedule.UpdatedAt == "" {
			schedule.UpdatedAt = schedule.CreatedAt
		}
	}
	if schedule.CreatedAt != "" {
		if schedule.CreatedAt, err = canonicalP2P3Time("schedule created_at", schedule.CreatedAt, true); err != nil {
			return Schedule{}, err
		}
	}
	if schedule.UpdatedAt != "" {
		if schedule.UpdatedAt, err = canonicalP2P3Time("schedule updated_at", schedule.UpdatedAt, true); err != nil {
			return Schedule{}, err
		}
	}
	return schedule, nil
}

func scanSchedule(scan p2p3Scanner) (Schedule, error) {
	var item Schedule
	var enabled int
	if err := scan(&item.ID, &item.Name, &item.AgentID, &item.Expression, &item.Timezone, &item.Prompt, &item.PermissionMode, &item.EnvironmentMode, &item.NarratorMode, &enabled, &item.NextRunAt, &item.LastRunAt, &item.LastRunID, &item.LastOutcome, &item.LastError, &item.LeaseUntil, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return Schedule{}, err
	}
	item.Enabled = enabled != 0
	canonical, err := canonicalSchedule(item, false)
	if err != nil {
		return Schedule{}, fmt.Errorf("stored schedule %s is invalid: %w", item.ID, err)
	}
	return canonical, nil
}

func validScheduleOutcome(value string) bool {
	switch value {
	case "", "success", "failure", "skipped", "error":
		return true
	default:
		return false
	}
}

func parseScheduleListOptions(args []any) (ScheduleListOptions, error) {
	var options ScheduleListOptions
	if len(args) > 1 {
		return options, errors.New("too many schedule list options")
	}
	if len(args) == 0 || args[0] == nil {
		return options, nil
	}
	switch value := args[0].(type) {
	case ScheduleListOptions:
		options = value
	case *ScheduleListOptions:
		if value != nil {
			options = *value
		}
	case string:
		options.AgentID = value
	default:
		return options, errors.New("invalid schedule list options")
	}
	options.AgentID = strings.TrimSpace(options.AgentID)
	if options.AgentID != "" {
		if err := validateP2P3Text("schedule agent id", options.AgentID, 128, true, false); err != nil {
			return ScheduleListOptions{}, err
		}
	}
	if options.Limit < 0 || options.Limit > P2P3MaxListLimit {
		return ScheduleListOptions{}, fmt.Errorf("schedule limit must be between 0 and %d", P2P3MaxListLimit)
	}
	return options, nil
}

func (s *Store) CreateNotificationDelivery(ctx context.Context, delivery NotificationDelivery) (NotificationDelivery, error) {
	canonical, err := canonicalNotificationDelivery(delivery, true)
	if err != nil {
		return NotificationDelivery{}, err
	}
	if canonical.ExecutionGeneration == 0 && canonical.RunID != "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(execution_generation,0) FROM runs WHERE id = ?`, canonical.RunID).Scan(&canonical.ExecutionGeneration); err != nil {
			return NotificationDelivery{}, err
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO notification_deliveries (id, dedupe_key, sink_type, sink_id, event_type, agent_id, run_id, tool_use_id, execution_generation, payload_json, status, attempt_count, max_attempts, next_attempt_at, lease_until, last_http_status, last_error, delivered_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, ?, ?, ?, ?, ?, NULLIF(?,''), NULLIF(?,0), NULLIF(?,''), NULLIF(?,''), ?, ?)`, canonical.ID, canonical.DedupeKey, canonical.SinkType, canonical.SinkID, canonical.EventType, canonical.AgentID, canonical.RunID, canonical.ToolUseID, canonical.ExecutionGeneration, string(canonical.PayloadJSON), canonical.Status, canonical.AttemptCount, canonical.MaxAttempts, canonical.NextAttemptAt, canonical.LeaseUntil, canonical.LastHTTPStatus, canonical.LastError, canonical.DeliveredAt, canonical.CreatedAt, canonical.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return NotificationDelivery{}, fmt.Errorf("%w: notification delivery dedupe key already exists", ErrConflict)
		}
		return NotificationDelivery{}, err
	}
	return canonical, nil
}

func (s *Store) EnqueueNotificationDelivery(ctx context.Context, delivery NotificationDelivery) (NotificationDelivery, bool, error) {
	created, err := s.CreateNotificationDelivery(ctx, delivery)
	if err == nil {
		return created, true, nil
	}
	if !IsConflict(err) {
		return NotificationDelivery{}, false, err
	}
	existing, getErr := s.GetNotificationDeliveryByDedupeKey(ctx, strings.TrimSpace(delivery.DedupeKey))
	return existing, false, getErr
}

func (s *Store) GetNotificationDelivery(ctx context.Context, id string) (NotificationDelivery, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return NotificationDelivery{}, sql.ErrNoRows
	}
	return scanNotificationDelivery(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, notificationDeliverySelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) GetNotificationDeliveryByDedupeKey(ctx context.Context, dedupeKey string) (NotificationDelivery, error) {
	dedupeKey = strings.TrimSpace(dedupeKey)
	if dedupeKey == "" {
		return NotificationDelivery{}, sql.ErrNoRows
	}
	return scanNotificationDelivery(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, notificationDeliverySelectSQL+` WHERE dedupe_key = ?`, dedupeKey).Scan(dest...)
	})
}

func (s *Store) ListNotificationDeliveries(ctx context.Context, args ...any) ([]NotificationDelivery, error) {
	options, err := parseNotificationDeliveryListOptions(args)
	if err != nil {
		return nil, err
	}
	query := notificationDeliverySelectSQL + ` WHERE 1 = 1`
	params := make([]any, 0, 8)
	for _, filter := range []struct {
		column string
		value  string
	}{{"status", options.Status}, {"sink_type", options.SinkType}, {"agent_id", options.AgentID}, {"run_id", options.RunID}, {"event_type", options.EventType}} {
		if filter.value != "" {
			query += ` AND ` + filter.column + ` = ?`
			params = append(params, filter.value)
		}
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	params = append(params, normalizedP2P3Limit(options.Limit, 50), options.Offset)
	rows, err := s.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]NotificationDelivery, 0)
	for rows.Next() {
		item, scanErr := scanNotificationDelivery(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteNotificationDelivery(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	result, err := s.db.ExecContext(ctx, `DELETE FROM notification_deliveries WHERE id = ? AND status IN ('delivered','dead')`, id)
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "notification_deliveries", id, "delete notification delivery")
}

func (s *Store) ClaimNotificationDeliveries(ctx context.Context, now, leaseUntil string, limit int) ([]NotificationDelivery, error) {
	var err error
	if now, err = canonicalP2P3Time("notification claim time", now, true); err != nil {
		return nil, err
	}
	if leaseUntil, err = canonicalP2P3Time("notification lease_until", leaseUntil, true); err != nil {
		return nil, err
	}
	if !p2p3TimeAfter(leaseUntil, now) {
		return nil, errors.New("notification lease_until must be after claim time")
	}
	limit = normalizedP2P3Limit(limit, 20)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'dead', lease_until = NULL, last_error = COALESCE(NULLIF(last_error,''), 'maximum attempts exhausted'), updated_at = ? WHERE status IN ('queued','retry_wait') AND attempt_count >= max_attempts`, Now()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM notification_deliveries WHERE status IN ('queued','retry_wait') AND next_attempt_at <= ? AND attempt_count < max_attempts AND (lease_until IS NULL OR lease_until <= ?) ORDER BY next_attempt_at ASC, created_at ASC, id ASC LIMIT ?`, now, now, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	items := make([]NotificationDelivery, 0, len(ids))
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'inflight', attempt_count = attempt_count + 1, lease_until = ?, last_error = NULL, updated_at = ? WHERE id = ? AND status IN ('queued','retry_wait') AND next_attempt_at <= ? AND attempt_count < max_attempts AND (lease_until IS NULL OR lease_until <= ?)`, leaseUntil, Now(), id, now, now)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected != 1 {
			continue
		}
		item, err := scanNotificationDelivery(func(dest ...any) error {
			return tx.QueryRowContext(ctx, notificationDeliverySelectSQL+` WHERE id = ?`, id).Scan(dest...)
		})
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) MarkNotificationDeliveryDelivered(ctx context.Context, id string, httpStatus int) error {
	if err := validateHTTPStatus(httpStatus, true); err != nil {
		return err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'delivered', lease_until = NULL, last_http_status = ?, last_error = NULL, delivered_at = ?, updated_at = ? WHERE id = ? AND status = 'inflight'`, httpStatus, now, now, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "notification_deliveries", strings.TrimSpace(id), "mark notification delivered")
}

func (s *Store) MarkNotificationDeliverySucceeded(ctx context.Context, id string, httpStatus int) error {
	return s.MarkNotificationDeliveryDelivered(ctx, id, httpStatus)
}

func (s *Store) MarkNotificationDeliveryRetry(ctx context.Context, id string, httpStatus int, lastError, nextAttemptAt string) error {
	if err := validateHTTPStatus(httpStatus, false); err != nil {
		return err
	}
	lastError = strings.TrimSpace(lastError)
	if err := validateP2P3Text("notification last error", lastError, 4096, true, false); err != nil {
		return err
	}
	var err error
	if nextAttemptAt, err = canonicalP2P3Time("notification next_attempt_at", nextAttemptAt, true); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'retry_wait', next_attempt_at = ?, lease_until = NULL, last_http_status = NULLIF(?,0), last_error = ?, delivered_at = NULL, updated_at = ? WHERE id = ? AND status = 'inflight' AND attempt_count < max_attempts`, nextAttemptAt, httpStatus, lastError, Now(), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "notification_deliveries", strings.TrimSpace(id), "schedule notification retry")
}

func (s *Store) MarkNotificationDeliveryDead(ctx context.Context, id string, httpStatus int, lastError string) error {
	if err := validateHTTPStatus(httpStatus, false); err != nil {
		return err
	}
	lastError = strings.TrimSpace(lastError)
	if err := validateP2P3Text("notification last error", lastError, 4096, true, false); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'dead', lease_until = NULL, last_http_status = NULLIF(?,0), last_error = ?, delivered_at = NULL, updated_at = ? WHERE id = ? AND status IN ('queued','inflight','retry_wait')`, httpStatus, lastError, Now(), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "notification_deliveries", strings.TrimSpace(id), "mark notification dead")
}

func (s *Store) NotificationDeliveryStats(ctx context.Context) (NotificationDeliveryStats, error) {
	var stats NotificationDeliveryStats
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'queued' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'inflight' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'retry_wait' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'delivered' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'dead' THEN 1 ELSE 0 END),0), COALESCE(SUM(attempt_count),0), COALESCE(SUM(CASE WHEN last_http_status >= 400 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN attempt_count >= max_attempts AND status <> 'delivered' THEN 1 ELSE 0 END),0) FROM notification_deliveries`).Scan(&stats.Total, &stats.Queued, &stats.Inflight, &stats.RetryWait, &stats.Delivered, &stats.Dead, &stats.Attempts, &stats.HTTPError, &stats.Exhausted)
	return stats, err
}

func canonicalNotificationDelivery(delivery NotificationDelivery, create bool) (NotificationDelivery, error) {
	delivery.ID = strings.TrimSpace(delivery.ID)
	delivery.DedupeKey = strings.TrimSpace(delivery.DedupeKey)
	delivery.SinkType = strings.TrimSpace(delivery.SinkType)
	delivery.SinkID = strings.TrimSpace(delivery.SinkID)
	delivery.EventType = strings.TrimSpace(delivery.EventType)
	delivery.AgentID = strings.TrimSpace(delivery.AgentID)
	delivery.RunID = strings.TrimSpace(delivery.RunID)
	delivery.ToolUseID = strings.TrimSpace(delivery.ToolUseID)
	delivery.Status = strings.TrimSpace(delivery.Status)
	delivery.NextAttemptAt = strings.TrimSpace(delivery.NextAttemptAt)
	delivery.LeaseUntil = strings.TrimSpace(delivery.LeaseUntil)
	delivery.LastError = strings.TrimSpace(delivery.LastError)
	delivery.DeliveredAt = strings.TrimSpace(delivery.DeliveredAt)
	if delivery.ID == "" && create {
		delivery.ID = NewID()
	}
	if delivery.Status == "" {
		delivery.Status = "queued"
	}
	if delivery.MaxAttempts == 0 {
		delivery.MaxAttempts = 5
	}
	if delivery.NextAttemptAt == "" {
		delivery.NextAttemptAt = Now()
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
		token    bool
	}{
		{"notification delivery id", delivery.ID, 128, true, false},
		{"notification dedupe key", delivery.DedupeKey, 256, true, false},
		{"notification sink id", delivery.SinkID, 256, true, false},
		{"notification event type", delivery.EventType, 96, true, true},
		{"notification agent id", delivery.AgentID, 128, false, false},
		{"notification run id", delivery.RunID, 128, false, false},
		{"notification tool use id", delivery.ToolUseID, 256, false, false},
		{"notification last error", delivery.LastError, 4096, false, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, field.token); err != nil {
			return NotificationDelivery{}, err
		}
	}
	if delivery.SinkType != "webhook" && delivery.SinkType != "telegram" {
		return NotificationDelivery{}, errors.New("invalid notification sink type")
	}
	if !validNotificationStatus(delivery.Status) {
		return NotificationDelivery{}, errors.New("invalid notification delivery status")
	}
	if delivery.ExecutionGeneration < 0 {
		return NotificationDelivery{}, errors.New("invalid notification execution generation")
	}
	if delivery.AttemptCount < 0 || delivery.MaxAttempts < 1 || delivery.MaxAttempts > 100 || delivery.AttemptCount > delivery.MaxAttempts {
		return NotificationDelivery{}, errors.New("invalid notification delivery attempt counts")
	}
	if err := validateHTTPStatus(delivery.LastHTTPStatus, false); err != nil {
		return NotificationDelivery{}, err
	}
	var err error
	delivery.PayloadJSON, err = normalizeP2P3JSONObject("notification payload", delivery.PayloadJSON, P2P3PayloadMaxBytes)
	if err != nil {
		return NotificationDelivery{}, err
	}
	for name, value := range map[string]*string{"notification next_attempt_at": &delivery.NextAttemptAt, "notification lease_until": &delivery.LeaseUntil, "notification delivered_at": &delivery.DeliveredAt} {
		*value, err = canonicalP2P3Time(name, *value, name == "notification next_attempt_at")
		if err != nil {
			return NotificationDelivery{}, err
		}
	}
	switch delivery.Status {
	case "queued", "retry_wait":
		if delivery.LeaseUntil != "" || delivery.DeliveredAt != "" {
			return NotificationDelivery{}, errors.New("queued notification delivery cannot have a lease or delivered_at")
		}
	case "inflight":
		if delivery.LeaseUntil == "" || delivery.DeliveredAt != "" {
			return NotificationDelivery{}, errors.New("inflight notification delivery requires a lease")
		}
	case "delivered":
		if delivery.DeliveredAt == "" || delivery.LeaseUntil != "" {
			return NotificationDelivery{}, errors.New("delivered notification delivery requires delivered_at")
		}
	case "dead":
		if delivery.LeaseUntil != "" || delivery.DeliveredAt != "" {
			return NotificationDelivery{}, errors.New("dead notification delivery cannot have a lease or delivered_at")
		}
	}
	if create {
		now := Now()
		if delivery.CreatedAt == "" {
			delivery.CreatedAt = now
		}
		if delivery.UpdatedAt == "" {
			delivery.UpdatedAt = delivery.CreatedAt
		}
	}
	for name, value := range map[string]*string{"notification created_at": &delivery.CreatedAt, "notification updated_at": &delivery.UpdatedAt} {
		if *value != "" {
			*value, err = canonicalP2P3Time(name, *value, true)
			if err != nil {
				return NotificationDelivery{}, err
			}
		}
	}
	return delivery, nil
}

func scanNotificationDelivery(scan p2p3Scanner) (NotificationDelivery, error) {
	var item NotificationDelivery
	var payload string
	if err := scan(&item.ID, &item.DedupeKey, &item.SinkType, &item.SinkID, &item.EventType, &item.AgentID, &item.RunID, &item.ToolUseID, &item.ExecutionGeneration, &payload, &item.Status, &item.AttemptCount, &item.MaxAttempts, &item.NextAttemptAt, &item.LeaseUntil, &item.LastHTTPStatus, &item.LastError, &item.DeliveredAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return NotificationDelivery{}, err
	}
	item.PayloadJSON = json.RawMessage(payload)
	canonical, err := canonicalNotificationDelivery(item, false)
	if err != nil {
		return NotificationDelivery{}, fmt.Errorf("stored notification delivery %s is invalid: %w", item.ID, err)
	}
	return canonical, nil
}

func validNotificationStatus(status string) bool {
	switch status {
	case "queued", "inflight", "retry_wait", "delivered", "dead":
		return true
	default:
		return false
	}
}

func parseNotificationDeliveryListOptions(args []any) (NotificationDeliveryListOptions, error) {
	var options NotificationDeliveryListOptions
	if len(args) > 1 {
		return options, errors.New("too many notification delivery list options")
	}
	if len(args) == 1 && args[0] != nil {
		switch value := args[0].(type) {
		case NotificationDeliveryListOptions:
			options = value
		case *NotificationDeliveryListOptions:
			if value != nil {
				options = *value
			}
		case string:
			options.Status = value
		default:
			return options, errors.New("invalid notification delivery list options")
		}
	}
	options.Status = strings.TrimSpace(options.Status)
	options.SinkType = strings.TrimSpace(options.SinkType)
	options.AgentID = strings.TrimSpace(options.AgentID)
	options.RunID = strings.TrimSpace(options.RunID)
	options.EventType = strings.TrimSpace(options.EventType)
	if options.Status != "" && !validNotificationStatus(options.Status) {
		return options, errors.New("invalid notification delivery status filter")
	}
	if options.SinkType != "" && options.SinkType != "webhook" && options.SinkType != "telegram" {
		return options, errors.New("invalid notification delivery sink filter")
	}
	if options.Limit < 0 || options.Limit > P2P3MaxListLimit || options.Offset < 0 {
		return options, errors.New("invalid notification delivery pagination")
	}
	return options, nil
}

func (s *Store) CreateChannelPairing(ctx context.Context, pairing ChannelPairing) (ChannelPairing, error) {
	canonical, err := canonicalChannelPairing(pairing, true)
	if err != nil {
		return ChannelPairing{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO channel_pairings (id, connection_id, agent_id, status, code_hash, expires_at, chat_id, user_id, failed_attempts, locked_until, credential_revision, paired_at, revoked_at, created_at, updated_at) VALUES (?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, NULLIF(?,''), ?, NULLIF(?,''), NULLIF(?,''), ?, ?)`, canonical.ID, canonical.ConnectionID, canonical.AgentID, canonical.Status, canonical.CodeHash, canonical.ExpiresAt, canonical.ChatID, canonical.UserID, canonical.FailedAttempts, canonical.LockedUntil, canonical.CredentialRevision, canonical.PairedAt, canonical.RevokedAt, canonical.CreatedAt, canonical.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return ChannelPairing{}, fmt.Errorf("%w: active channel pairing already exists", ErrConflict)
		}
		return ChannelPairing{}, err
	}
	return canonical, nil
}

func (s *Store) GetChannelPairing(ctx context.Context, id string) (ChannelPairing, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ChannelPairing{}, sql.ErrNoRows
	}
	return scanChannelPairing(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, channelPairingSelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) ListChannelPairings(ctx context.Context, args ...any) ([]ChannelPairing, error) {
	options, err := parseChannelPairingListOptions(args)
	if err != nil {
		return nil, err
	}
	query := channelPairingSelectSQL + ` WHERE 1 = 1`
	params := make([]any, 0, 5)
	for _, filter := range []struct{ column, value string }{{"connection_id", options.ConnectionID}, {"agent_id", options.AgentID}, {"status", options.Status}} {
		if filter.value != "" {
			query += ` AND ` + filter.column + ` = ?`
			params = append(params, filter.value)
		}
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	params = append(params, normalizedP2P3Limit(options.Limit, 50))
	rows, err := s.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ChannelPairing, 0)
	for rows.Next() {
		item, scanErr := scanChannelPairing(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ActivateChannelPairing(ctx context.Context, id, codeHash, chatID, userID string, credentialRevision int64) (ChannelPairing, error) {
	id = strings.TrimSpace(id)
	codeHash = strings.TrimSpace(codeHash)
	chatID = strings.TrimSpace(chatID)
	userID = strings.TrimSpace(userID)
	if err := validateP2P3Text("channel pairing code hash", codeHash, 256, true, false); err != nil || len(codeHash) < 32 {
		return ChannelPairing{}, errors.New("invalid channel pairing code hash")
	}
	if err := validateP2P3Text("channel pairing chat id", chatID, 256, true, false); err != nil {
		return ChannelPairing{}, err
	}
	if err := validateP2P3Text("channel pairing user id", userID, 256, true, false); err != nil {
		return ChannelPairing{}, err
	}
	if credentialRevision < 0 {
		return ChannelPairing{}, errors.New("invalid channel pairing credential revision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelPairing{}, err
	}
	defer tx.Rollback()
	current, err := scanChannelPairing(func(dest ...any) error {
		return tx.QueryRowContext(ctx, channelPairingSelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
	if err != nil {
		return ChannelPairing{}, err
	}
	now := Now()
	if current.Status != "pending" || !p2p3TimeAfter(current.ExpiresAt, now) || current.LockedUntil != "" && p2p3TimeAfter(current.LockedUntil, now) {
		return ChannelPairing{}, fmt.Errorf("%w: channel pairing cannot be activated", ErrConflict)
	}
	if subtle.ConstantTimeCompare([]byte(current.CodeHash), []byte(codeHash)) != 1 {
		if _, err := recordChannelPairingFailureTx(ctx, tx, current, DefaultPairingMaxFailedAttempts, time.Now().UTC().Add(DefaultPairingLockDuration).Format(time.RFC3339Nano)); err != nil {
			return ChannelPairing{}, err
		}
		if err := tx.Commit(); err != nil {
			return ChannelPairing{}, err
		}
		return ChannelPairing{}, fmt.Errorf("%w: channel pairing code does not match", ErrConflict)
	}
	result, err := tx.ExecContext(ctx, `UPDATE channel_pairings SET status = 'active', code_hash = NULL, chat_id = ?, user_id = ?, failed_attempts = 0, locked_until = NULL, credential_revision = ?, paired_at = ?, revoked_at = NULL, updated_at = ? WHERE id = ? AND status = 'pending' AND code_hash = ? AND expires_at > ? AND (locked_until IS NULL OR locked_until <= ?)`, chatID, userID, credentialRevision, now, now, id, codeHash, now, now)
	if err != nil {
		if isUniqueConstraint(err) {
			return ChannelPairing{}, fmt.Errorf("%w: active channel pairing already exists", ErrConflict)
		}
		return ChannelPairing{}, err
	}
	if err := requireP2P3Transition(ctx, tx, result, "channel_pairings", id, "activate channel pairing"); err != nil {
		return ChannelPairing{}, err
	}
	activated, err := scanChannelPairing(func(dest ...any) error {
		return tx.QueryRowContext(ctx, channelPairingSelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
	if err != nil {
		return ChannelPairing{}, err
	}
	if err := tx.Commit(); err != nil {
		return ChannelPairing{}, err
	}
	return activated, nil
}

func (s *Store) RecordChannelPairingFailure(ctx context.Context, id string, maxAttempts int, lockedUntil string) (ChannelPairing, error) {
	if maxAttempts <= 0 || maxAttempts > 100 {
		return ChannelPairing{}, errors.New("invalid channel pairing maximum attempts")
	}
	var err error
	if lockedUntil, err = canonicalP2P3Time("channel pairing locked_until", lockedUntil, true); err != nil {
		return ChannelPairing{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelPairing{}, err
	}
	defer tx.Rollback()
	current, err := scanChannelPairing(func(dest ...any) error {
		return tx.QueryRowContext(ctx, channelPairingSelectSQL+` WHERE id = ?`, strings.TrimSpace(id)).Scan(dest...)
	})
	if err != nil {
		return ChannelPairing{}, err
	}
	updated, err := recordChannelPairingFailureTx(ctx, tx, current, maxAttempts, lockedUntil)
	if err != nil {
		return ChannelPairing{}, err
	}
	if err := tx.Commit(); err != nil {
		return ChannelPairing{}, err
	}
	return updated, nil
}

func recordChannelPairingFailureTx(ctx context.Context, tx *sql.Tx, current ChannelPairing, maxAttempts int, lockedUntil string) (ChannelPairing, error) {
	if current.Status != "pending" {
		return ChannelPairing{}, fmt.Errorf("%w: channel pairing is not pending", ErrConflict)
	}
	failedAttempts := current.FailedAttempts + 1
	lock := ""
	if failedAttempts >= maxAttempts {
		lock = lockedUntil
	}
	result, err := tx.ExecContext(ctx, `UPDATE channel_pairings SET failed_attempts = ?, locked_until = NULLIF(?,''), updated_at = ? WHERE id = ? AND status = 'pending' AND failed_attempts = ?`, failedAttempts, lock, Now(), current.ID, current.FailedAttempts)
	if err != nil {
		return ChannelPairing{}, err
	}
	if err := requireP2P3Transition(ctx, tx, result, "channel_pairings", current.ID, "record channel pairing failure"); err != nil {
		return ChannelPairing{}, err
	}
	return scanChannelPairing(func(dest ...any) error {
		return tx.QueryRowContext(ctx, channelPairingSelectSQL+` WHERE id = ?`, current.ID).Scan(dest...)
	})
}

func (s *Store) RevokeChannelPairing(ctx context.Context, id string) (ChannelPairing, error) {
	id = strings.TrimSpace(id)
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE channel_pairings SET status = 'revoked', code_hash = NULL, locked_until = NULL, revoked_at = ?, updated_at = ? WHERE id = ? AND status IN ('pending','active')`, now, now, id)
	if err != nil {
		return ChannelPairing{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "channel_pairings", id, "revoke channel pairing"); err != nil {
		return ChannelPairing{}, err
	}
	return s.GetChannelPairing(ctx, id)
}

func (s *Store) DeleteChannelPairing(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	result, err := s.db.ExecContext(ctx, `DELETE FROM channel_pairings WHERE id = ? AND status = 'revoked'`, id)
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "channel_pairings", id, "delete channel pairing")
}

func (s *Store) ChannelPairingStats(ctx context.Context, at string) (ChannelPairingStats, error) {
	if strings.TrimSpace(at) == "" {
		at = Now()
	}
	var err error
	if at, err = canonicalP2P3Time("channel pairing stats time", at, true); err != nil {
		return ChannelPairingStats{}, err
	}
	var stats ChannelPairingStats
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'revoked' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'pending' AND locked_until > ? THEN 1 ELSE 0 END),0) FROM channel_pairings`, at).Scan(&stats.Total, &stats.Pending, &stats.Active, &stats.Revoked, &stats.Locked)
	return stats, err
}

func canonicalChannelPairing(pairing ChannelPairing, create bool) (ChannelPairing, error) {
	pairing.ID = strings.TrimSpace(pairing.ID)
	pairing.ConnectionID = strings.TrimSpace(pairing.ConnectionID)
	pairing.AgentID = strings.TrimSpace(pairing.AgentID)
	pairing.Status = strings.TrimSpace(pairing.Status)
	pairing.CodeHash = strings.TrimSpace(pairing.CodeHash)
	pairing.ExpiresAt = strings.TrimSpace(pairing.ExpiresAt)
	pairing.ChatID = strings.TrimSpace(pairing.ChatID)
	pairing.UserID = strings.TrimSpace(pairing.UserID)
	pairing.LockedUntil = strings.TrimSpace(pairing.LockedUntil)
	pairing.PairedAt = strings.TrimSpace(pairing.PairedAt)
	pairing.RevokedAt = strings.TrimSpace(pairing.RevokedAt)
	if pairing.ID == "" && create {
		pairing.ID = NewID()
	}
	if pairing.Status == "" {
		pairing.Status = "pending"
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
	}{
		{"channel pairing id", pairing.ID, 128, true},
		{"channel pairing connection id", pairing.ConnectionID, 128, true},
		{"channel pairing agent id", pairing.AgentID, 128, true},
		{"channel pairing chat id", pairing.ChatID, 256, false},
		{"channel pairing user id", pairing.UserID, 256, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, false); err != nil {
			return ChannelPairing{}, err
		}
	}
	if !validChannelPairingStatus(pairing.Status) || pairing.FailedAttempts < 0 || pairing.CredentialRevision < 0 {
		return ChannelPairing{}, errors.New("invalid channel pairing state")
	}
	var err error
	for name, value := range map[string]*string{"channel pairing expires_at": &pairing.ExpiresAt, "channel pairing locked_until": &pairing.LockedUntil, "channel pairing paired_at": &pairing.PairedAt, "channel pairing revoked_at": &pairing.RevokedAt} {
		*value, err = canonicalP2P3Time(name, *value, false)
		if err != nil {
			return ChannelPairing{}, err
		}
	}
	switch pairing.Status {
	case "pending":
		if len(pairing.CodeHash) < 32 || len(pairing.CodeHash) > 256 || pairing.ExpiresAt == "" || pairing.PairedAt != "" || pairing.RevokedAt != "" {
			return ChannelPairing{}, errors.New("invalid pending channel pairing")
		}
		if !utf8.ValidString(pairing.CodeHash) || strings.ContainsRune(pairing.CodeHash, 0) {
			return ChannelPairing{}, errors.New("invalid channel pairing code hash")
		}
	case "active":
		if pairing.CodeHash != "" || pairing.ChatID == "" || pairing.UserID == "" || pairing.PairedAt == "" || pairing.RevokedAt != "" {
			return ChannelPairing{}, errors.New("invalid active channel pairing")
		}
	case "revoked":
		if pairing.CodeHash != "" || pairing.RevokedAt == "" {
			return ChannelPairing{}, errors.New("invalid revoked channel pairing")
		}
	}
	if create {
		now := Now()
		if pairing.CreatedAt == "" {
			pairing.CreatedAt = now
		}
		if pairing.UpdatedAt == "" {
			pairing.UpdatedAt = pairing.CreatedAt
		}
	}
	for name, value := range map[string]*string{"channel pairing created_at": &pairing.CreatedAt, "channel pairing updated_at": &pairing.UpdatedAt} {
		if *value != "" {
			*value, err = canonicalP2P3Time(name, *value, true)
			if err != nil {
				return ChannelPairing{}, err
			}
		}
	}
	return pairing, nil
}

func scanChannelPairing(scan p2p3Scanner) (ChannelPairing, error) {
	var item ChannelPairing
	if err := scan(&item.ID, &item.ConnectionID, &item.AgentID, &item.Status, &item.CodeHash, &item.ExpiresAt, &item.ChatID, &item.UserID, &item.FailedAttempts, &item.LockedUntil, &item.CredentialRevision, &item.PairedAt, &item.RevokedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return ChannelPairing{}, err
	}
	canonical, err := canonicalChannelPairing(item, false)
	if err != nil {
		return ChannelPairing{}, fmt.Errorf("stored channel pairing %s is invalid: %w", item.ID, err)
	}
	return canonical, nil
}

func validChannelPairingStatus(status string) bool {
	return status == "pending" || status == "active" || status == "revoked"
}

func parseChannelPairingListOptions(args []any) (ChannelPairingListOptions, error) {
	var options ChannelPairingListOptions
	if len(args) > 1 {
		return options, errors.New("too many channel pairing list options")
	}
	if len(args) == 1 && args[0] != nil {
		switch value := args[0].(type) {
		case ChannelPairingListOptions:
			options = value
		case *ChannelPairingListOptions:
			if value != nil {
				options = *value
			}
		case string:
			options.ConnectionID = value
		default:
			return options, errors.New("invalid channel pairing list options")
		}
	}
	options.ConnectionID = strings.TrimSpace(options.ConnectionID)
	options.AgentID = strings.TrimSpace(options.AgentID)
	options.Status = strings.TrimSpace(options.Status)
	if options.Status != "" && !validChannelPairingStatus(options.Status) {
		return options, errors.New("invalid channel pairing status filter")
	}
	if options.Limit < 0 || options.Limit > P2P3MaxListLimit {
		return options, errors.New("invalid channel pairing limit")
	}
	return options, nil
}

func (s *Store) CreateChannelEvent(ctx context.Context, event ChannelEvent) (ChannelEvent, error) {
	stored, _, err := s.InsertChannelEvent(ctx, event)
	return stored, err
}

func (s *Store) InsertChannelEvent(ctx context.Context, event ChannelEvent) (ChannelEvent, bool, error) {
	canonical, err := canonicalChannelEvent(event, true)
	if err != nil {
		return ChannelEvent{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO channel_events (id, connection_id, external_event_id, event_type, agent_id, run_id, tool_use_id, chat_id, user_id, payload_json, occurred_at, processed_at, created_at) VALUES (?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, NULLIF(?,''), NULLIF(?,''), ?)`, canonical.ID, canonical.ConnectionID, canonical.ExternalEventID, canonical.EventType, canonical.AgentID, canonical.RunID, canonical.ToolUseID, canonical.ChatID, canonical.UserID, string(canonical.PayloadJSON), canonical.OccurredAt, canonical.ProcessedAt, canonical.CreatedAt)
	if err != nil {
		return ChannelEvent{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ChannelEvent{}, false, err
	}
	if affected == 1 {
		return canonical, true, nil
	}
	existing, err := s.GetChannelEventByExternalID(ctx, canonical.ConnectionID, canonical.ExternalEventID)
	return existing, false, err
}

func (s *Store) GetChannelEvent(ctx context.Context, id string) (ChannelEvent, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ChannelEvent{}, sql.ErrNoRows
	}
	return scanChannelEvent(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, channelEventSelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) GetChannelEventByExternalID(ctx context.Context, connectionID, externalEventID string) (ChannelEvent, error) {
	connectionID = strings.TrimSpace(connectionID)
	externalEventID = strings.TrimSpace(externalEventID)
	if connectionID == "" || externalEventID == "" {
		return ChannelEvent{}, sql.ErrNoRows
	}
	return scanChannelEvent(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, channelEventSelectSQL+` WHERE connection_id = ? AND external_event_id = ?`, connectionID, externalEventID).Scan(dest...)
	})
}

func (s *Store) ListChannelEvents(ctx context.Context, args ...any) ([]ChannelEvent, error) {
	options, err := parseChannelEventListOptions(args)
	if err != nil {
		return nil, err
	}
	query := channelEventSelectSQL + ` WHERE 1 = 1`
	params := make([]any, 0, 7)
	for _, filter := range []struct{ column, value string }{{"connection_id", options.ConnectionID}, {"agent_id", options.AgentID}, {"event_type", options.EventType}} {
		if filter.value != "" {
			query += ` AND ` + filter.column + ` = ?`
			params = append(params, filter.value)
		}
	}
	if options.OnlyUnprocessed {
		query += ` AND processed_at IS NULL`
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	params = append(params, normalizedP2P3Limit(options.Limit, 50), options.Offset)
	rows, err := s.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ChannelEvent, 0)
	for rows.Next() {
		item, scanErr := scanChannelEvent(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) MarkChannelEventProcessed(ctx context.Context, id, processedAt string) (ChannelEvent, error) {
	if strings.TrimSpace(processedAt) == "" {
		processedAt = Now()
	}
	var err error
	if processedAt, err = canonicalP2P3Time("channel event processed_at", processedAt, true); err != nil {
		return ChannelEvent{}, err
	}
	id = strings.TrimSpace(id)
	result, err := s.db.ExecContext(ctx, `UPDATE channel_events SET processed_at = ? WHERE id = ? AND processed_at IS NULL`, processedAt, id)
	if err != nil {
		return ChannelEvent{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "channel_events", id, "mark channel event processed"); err != nil {
		return ChannelEvent{}, err
	}
	return s.GetChannelEvent(ctx, id)
}

func (s *Store) DeleteChannelEvent(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	result, err := s.db.ExecContext(ctx, `DELETE FROM channel_events WHERE id = ? AND processed_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "channel_events", id, "delete channel event")
}

func canonicalChannelEvent(event ChannelEvent, create bool) (ChannelEvent, error) {
	event.ID = strings.TrimSpace(event.ID)
	event.ConnectionID = strings.TrimSpace(event.ConnectionID)
	event.ExternalEventID = strings.TrimSpace(event.ExternalEventID)
	event.EventType = strings.TrimSpace(event.EventType)
	event.AgentID = strings.TrimSpace(event.AgentID)
	event.RunID = strings.TrimSpace(event.RunID)
	event.ToolUseID = strings.TrimSpace(event.ToolUseID)
	event.ChatID = strings.TrimSpace(event.ChatID)
	event.UserID = strings.TrimSpace(event.UserID)
	event.OccurredAt = strings.TrimSpace(event.OccurredAt)
	event.ProcessedAt = strings.TrimSpace(event.ProcessedAt)
	if event.ID == "" && create {
		event.ID = NewID()
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
		token    bool
	}{
		{"channel event id", event.ID, 128, true, false},
		{"channel event connection id", event.ConnectionID, 128, true, false},
		{"channel external event id", event.ExternalEventID, 256, true, false},
		{"channel event type", event.EventType, 96, true, true},
		{"channel event agent id", event.AgentID, 128, false, false},
		{"channel event run id", event.RunID, 128, false, false},
		{"channel event tool use id", event.ToolUseID, 256, false, false},
		{"channel event chat id", event.ChatID, 256, false, false},
		{"channel event user id", event.UserID, 256, false, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, field.token); err != nil {
			return ChannelEvent{}, err
		}
	}
	var err error
	event.PayloadJSON, err = normalizeP2P3JSONObject("channel event payload", event.PayloadJSON, P2P3PayloadMaxBytes)
	if err != nil {
		return ChannelEvent{}, err
	}
	for name, value := range map[string]*string{"channel event occurred_at": &event.OccurredAt, "channel event processed_at": &event.ProcessedAt} {
		*value, err = canonicalP2P3Time(name, *value, false)
		if err != nil {
			return ChannelEvent{}, err
		}
	}
	if create && event.CreatedAt == "" {
		event.CreatedAt = Now()
	}
	if event.CreatedAt != "" {
		if event.CreatedAt, err = canonicalP2P3Time("channel event created_at", event.CreatedAt, true); err != nil {
			return ChannelEvent{}, err
		}
	}
	return event, nil
}

func scanChannelEvent(scan p2p3Scanner) (ChannelEvent, error) {
	var item ChannelEvent
	var payload string
	if err := scan(&item.ID, &item.ConnectionID, &item.ExternalEventID, &item.EventType, &item.AgentID, &item.RunID, &item.ToolUseID, &item.ChatID, &item.UserID, &payload, &item.OccurredAt, &item.ProcessedAt, &item.CreatedAt); err != nil {
		return ChannelEvent{}, err
	}
	item.PayloadJSON = json.RawMessage(payload)
	canonical, err := canonicalChannelEvent(item, false)
	if err != nil {
		return ChannelEvent{}, fmt.Errorf("stored channel event %s is invalid: %w", item.ID, err)
	}
	return canonical, nil
}

func parseChannelEventListOptions(args []any) (ChannelEventListOptions, error) {
	var options ChannelEventListOptions
	if len(args) > 1 {
		return options, errors.New("too many channel event list options")
	}
	if len(args) == 1 && args[0] != nil {
		switch value := args[0].(type) {
		case ChannelEventListOptions:
			options = value
		case *ChannelEventListOptions:
			if value != nil {
				options = *value
			}
		case string:
			options.ConnectionID = value
		default:
			return options, errors.New("invalid channel event list options")
		}
	}
	options.ConnectionID = strings.TrimSpace(options.ConnectionID)
	options.AgentID = strings.TrimSpace(options.AgentID)
	options.EventType = strings.TrimSpace(options.EventType)
	if options.Limit < 0 || options.Limit > P2P3MaxListLimit || options.Offset < 0 {
		return options, errors.New("invalid channel event pagination")
	}
	return options, nil
}

func (s *Store) GetChannelCursor(ctx context.Context, connectionID string) (ChannelCursor, error) {
	connectionID = strings.TrimSpace(connectionID)
	if err := validateP2P3Text("channel cursor connection id", connectionID, 128, true, false); err != nil {
		return ChannelCursor{}, err
	}
	var cursor ChannelCursor
	err := s.db.QueryRowContext(ctx, `SELECT connection_id, offset, updated_at FROM channel_cursors WHERE connection_id = ?`, connectionID).Scan(&cursor.ConnectionID, &cursor.Offset, &cursor.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelCursor{ConnectionID: connectionID, Offset: 0}, nil
	}
	if err != nil {
		return ChannelCursor{}, err
	}
	return canonicalChannelCursor(cursor)
}

func (s *Store) UpsertChannelCursor(ctx context.Context, cursor ChannelCursor) (ChannelCursor, error) {
	canonical, err := canonicalChannelCursor(cursor)
	if err != nil {
		return ChannelCursor{}, err
	}
	if canonical.UpdatedAt == "" {
		canonical.UpdatedAt = Now()
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO channel_cursors (connection_id, offset, updated_at) VALUES (?, ?, ?) ON CONFLICT(connection_id) DO UPDATE SET offset = excluded.offset, updated_at = excluded.updated_at WHERE excluded.offset >= channel_cursors.offset`, canonical.ConnectionID, canonical.Offset, canonical.UpdatedAt)
	if err != nil {
		return ChannelCursor{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ChannelCursor{}, err
	}
	if affected != 1 {
		return ChannelCursor{}, fmt.Errorf("%w: channel cursor cannot move backwards", ErrConflict)
	}
	return s.GetChannelCursor(ctx, canonical.ConnectionID)
}

func (s *Store) AdvanceChannelCursor(ctx context.Context, connectionID string, expectedOffset, nextOffset int64) (ChannelCursor, error) {
	connectionID = strings.TrimSpace(connectionID)
	if expectedOffset < 0 || nextOffset < expectedOffset {
		return ChannelCursor{}, errors.New("invalid channel cursor transition")
	}
	now := Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelCursor{}, err
	}
	defer tx.Rollback()
	var result sql.Result
	if expectedOffset == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO channel_cursors (connection_id, offset, updated_at) VALUES (?, ?, ?) ON CONFLICT(connection_id) DO UPDATE SET offset = excluded.offset, updated_at = excluded.updated_at WHERE channel_cursors.offset = ?`, connectionID, nextOffset, now, expectedOffset)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE channel_cursors SET offset = ?, updated_at = ? WHERE connection_id = ? AND offset = ?`, nextOffset, now, connectionID, expectedOffset)
	}
	if err != nil {
		return ChannelCursor{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ChannelCursor{}, err
	}
	if affected != 1 {
		return ChannelCursor{}, fmt.Errorf("%w: channel cursor offset changed", ErrConflict)
	}
	var cursor ChannelCursor
	if err := tx.QueryRowContext(ctx, `SELECT connection_id, offset, updated_at FROM channel_cursors WHERE connection_id = ?`, connectionID).Scan(&cursor.ConnectionID, &cursor.Offset, &cursor.UpdatedAt); err != nil {
		return ChannelCursor{}, err
	}
	if err := tx.Commit(); err != nil {
		return ChannelCursor{}, err
	}
	return cursor, nil
}

func (s *Store) RecordChannelEventAndAdvanceCursor(ctx context.Context, event ChannelEvent, expectedOffset, nextOffset int64) (ChannelEvent, bool, ChannelCursor, error) {
	canonical, err := canonicalChannelEvent(event, true)
	if err != nil {
		return ChannelEvent{}, false, ChannelCursor{}, err
	}
	if expectedOffset < 0 || nextOffset < expectedOffset {
		return ChannelEvent{}, false, ChannelCursor{}, errors.New("invalid channel cursor transition")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelEvent{}, false, ChannelCursor{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO channel_events (id, connection_id, external_event_id, event_type, agent_id, run_id, tool_use_id, chat_id, user_id, payload_json, occurred_at, processed_at, created_at) VALUES (?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, NULLIF(?,''), NULLIF(?,''), ?)`, canonical.ID, canonical.ConnectionID, canonical.ExternalEventID, canonical.EventType, canonical.AgentID, canonical.RunID, canonical.ToolUseID, canonical.ChatID, canonical.UserID, string(canonical.PayloadJSON), canonical.OccurredAt, canonical.ProcessedAt, canonical.CreatedAt)
	if err != nil {
		return ChannelEvent{}, false, ChannelCursor{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return ChannelEvent{}, false, ChannelCursor{}, err
	}
	if inserted == 0 {
		canonical, err = scanChannelEvent(func(dest ...any) error {
			return tx.QueryRowContext(ctx, channelEventSelectSQL+` WHERE connection_id = ? AND external_event_id = ?`, canonical.ConnectionID, canonical.ExternalEventID).Scan(dest...)
		})
		if err != nil {
			return ChannelEvent{}, false, ChannelCursor{}, err
		}
	}
	now := Now()
	cursorResult, err := tx.ExecContext(ctx, `INSERT INTO channel_cursors (connection_id, offset, updated_at) VALUES (?, ?, ?) ON CONFLICT(connection_id) DO UPDATE SET offset = excluded.offset, updated_at = excluded.updated_at WHERE channel_cursors.offset = ?`, canonical.ConnectionID, nextOffset, now, expectedOffset)
	if err != nil {
		return ChannelEvent{}, false, ChannelCursor{}, err
	}
	affected, err := cursorResult.RowsAffected()
	if err != nil {
		return ChannelEvent{}, false, ChannelCursor{}, err
	}
	if affected != 1 {
		return ChannelEvent{}, false, ChannelCursor{}, fmt.Errorf("%w: channel cursor offset changed", ErrConflict)
	}
	cursor := ChannelCursor{ConnectionID: canonical.ConnectionID, Offset: nextOffset, UpdatedAt: now}
	if err := tx.Commit(); err != nil {
		return ChannelEvent{}, false, ChannelCursor{}, err
	}
	return canonical, inserted == 1, cursor, nil
}

func canonicalChannelCursor(cursor ChannelCursor) (ChannelCursor, error) {
	cursor.ConnectionID = strings.TrimSpace(cursor.ConnectionID)
	if err := validateP2P3Text("channel cursor connection id", cursor.ConnectionID, 128, true, false); err != nil {
		return ChannelCursor{}, err
	}
	if cursor.Offset < 0 {
		return ChannelCursor{}, errors.New("channel cursor offset must not be negative")
	}
	if cursor.UpdatedAt != "" {
		var err error
		if cursor.UpdatedAt, err = canonicalP2P3Time("channel cursor updated_at", cursor.UpdatedAt, true); err != nil {
			return ChannelCursor{}, err
		}
	}
	return cursor, nil
}

func (s *Store) ChannelStats(ctx context.Context, connectionID string) (ChannelStats, error) {
	connectionID = strings.TrimSpace(connectionID)
	where := ""
	params := []any{}
	if connectionID != "" {
		where = ` WHERE connection_id = ?`
		params = append(params, connectionID)
	}
	var stats ChannelStats
	query := `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'revoked' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'pending' AND locked_until > ? THEN 1 ELSE 0 END),0) FROM channel_pairings` + where
	pairingParams := append([]any{Now()}, params...)
	if err := s.db.QueryRowContext(ctx, query, pairingParams...).Scan(&stats.Pairings.Total, &stats.Pairings.Pending, &stats.Pairings.Active, &stats.Pairings.Revoked, &stats.Pairings.Locked); err != nil {
		return ChannelStats{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN processed_at IS NULL THEN 1 ELSE 0 END),0) FROM channel_events`+where, params...).Scan(&stats.Events, &stats.Pending); err != nil {
		return ChannelStats{}, err
	}
	return stats, nil
}

func (s *Store) CreateDeviceActionRequest(ctx context.Context, request DeviceActionRequest) (DeviceActionRequest, error) {
	canonical, err := canonicalDeviceActionRequest(request, true)
	if err != nil {
		return DeviceActionRequest{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO device_action_requests (id, connection_id, entity_id, domain, service, payload_json, risk, status, requested_by, approved_by, expires_at, last_error, created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,''), ?, NULLIF(?,''), ?, ?, NULLIF(?,''))`, canonical.ID, canonical.ConnectionID, canonical.EntityID, canonical.Domain, canonical.Service, string(canonical.PayloadJSON), canonical.Risk, canonical.Status, canonical.RequestedBy, canonical.ApprovedBy, canonical.ExpiresAt, canonical.LastError, canonical.CreatedAt, canonical.UpdatedAt, canonical.CompletedAt)
	if err != nil {
		return DeviceActionRequest{}, err
	}
	return canonical, nil
}

func (s *Store) GetDeviceActionRequest(ctx context.Context, id string) (DeviceActionRequest, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return DeviceActionRequest{}, sql.ErrNoRows
	}
	return scanDeviceActionRequest(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, deviceActionRequestSelectSQL+` WHERE id = ?`, id).Scan(dest...)
	})
}

func (s *Store) ListDeviceActionRequests(ctx context.Context, args ...any) ([]DeviceActionRequest, error) {
	options, err := parseDeviceActionRequestListOptions(args)
	if err != nil {
		return nil, err
	}
	query := deviceActionRequestSelectSQL + ` WHERE 1 = 1`
	params := make([]any, 0, 6)
	for _, filter := range []struct{ column, value string }{{"connection_id", options.ConnectionID}, {"status", options.Status}, {"risk", options.Risk}} {
		if filter.value != "" {
			query += ` AND ` + filter.column + ` = ?`
			params = append(params, filter.value)
		}
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	params = append(params, normalizedP2P3Limit(options.Limit, 50), options.Offset)
	rows, err := s.db.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DeviceActionRequest, 0)
	for rows.Next() {
		item, scanErr := scanDeviceActionRequest(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ApproveDeviceActionRequest(ctx context.Context, id, approvedBy string) (DeviceActionRequest, error) {
	id = strings.TrimSpace(id)
	approvedBy = strings.TrimSpace(approvedBy)
	if err := validateP2P3Text("device action approved_by", approvedBy, 200, true, false); err != nil {
		return DeviceActionRequest{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE device_action_requests SET status = 'approved', approved_by = ?, last_error = NULL, updated_at = ? WHERE id = ? AND status = 'pending' AND expires_at > ?`, approvedBy, now, id, now)
	if err != nil {
		return DeviceActionRequest{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "device_action_requests", id, "approve device action request"); err != nil {
		return DeviceActionRequest{}, err
	}
	return s.GetDeviceActionRequest(ctx, id)
}

func (s *Store) DenyDeviceActionRequest(ctx context.Context, id, deniedBy, reason string) (DeviceActionRequest, error) {
	id = strings.TrimSpace(id)
	deniedBy = strings.TrimSpace(deniedBy)
	reason = strings.TrimSpace(reason)
	if err := validateP2P3Text("device action denied_by", deniedBy, 200, true, false); err != nil {
		return DeviceActionRequest{}, err
	}
	if err := validateP2P3Text("device action denial reason", reason, 4096, false, false); err != nil {
		return DeviceActionRequest{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE device_action_requests SET status = 'denied', approved_by = ?, last_error = NULLIF(?,''), completed_at = ?, updated_at = ? WHERE id = ? AND status = 'pending'`, deniedBy, reason, now, now, id)
	if err != nil {
		return DeviceActionRequest{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "device_action_requests", id, "deny device action request"); err != nil {
		return DeviceActionRequest{}, err
	}
	return s.GetDeviceActionRequest(ctx, id)
}

func (s *Store) StartDeviceActionRequest(ctx context.Context, id string) (DeviceActionRequest, error) {
	id = strings.TrimSpace(id)
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE device_action_requests SET status = 'executing', last_error = NULL, updated_at = ? WHERE id = ? AND status = 'approved' AND expires_at > ?`, now, id, now)
	if err != nil {
		return DeviceActionRequest{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "device_action_requests", id, "start device action request"); err != nil {
		return DeviceActionRequest{}, err
	}
	return s.GetDeviceActionRequest(ctx, id)
}

func (s *Store) ClaimDeviceActionRequest(ctx context.Context, id string) (DeviceActionRequest, error) {
	return s.StartDeviceActionRequest(ctx, id)
}

func (s *Store) CompleteDeviceActionRequest(ctx context.Context, id, status, lastError string) (DeviceActionRequest, error) {
	id = strings.TrimSpace(id)
	status = strings.TrimSpace(status)
	lastError = strings.TrimSpace(lastError)
	if status != "succeeded" && status != "failed" {
		return DeviceActionRequest{}, errors.New("device action completion status must be succeeded or failed")
	}
	if status == "failed" {
		if err := validateP2P3Text("device action last error", lastError, 4096, true, false); err != nil {
			return DeviceActionRequest{}, err
		}
	} else {
		lastError = ""
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE device_action_requests SET status = ?, last_error = NULLIF(?,''), completed_at = ?, updated_at = ? WHERE id = ? AND status = 'executing'`, status, lastError, now, now, id)
	if err != nil {
		return DeviceActionRequest{}, err
	}
	if err := requireP2P3Transition(ctx, s.db, result, "device_action_requests", id, "complete device action request"); err != nil {
		return DeviceActionRequest{}, err
	}
	return s.GetDeviceActionRequest(ctx, id)
}

func (s *Store) ExpireDeviceActionRequests(ctx context.Context, at string, limit int) ([]DeviceActionRequest, error) {
	var err error
	if at, err = canonicalP2P3Time("device action expiry time", at, true); err != nil {
		return nil, err
	}
	limit = normalizedP2P3Limit(limit, 50)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM device_action_requests WHERE status IN ('pending','approved') AND expires_at <= ? ORDER BY expires_at ASC, id ASC LIMIT ?`, at, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	items := make([]DeviceActionRequest, 0, len(ids))
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE device_action_requests SET status = 'expired', completed_at = ?, updated_at = ? WHERE id = ? AND status IN ('pending','approved') AND expires_at <= ?`, at, at, id, at)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected != 1 {
			continue
		}
		item, err := scanDeviceActionRequest(func(dest ...any) error {
			return tx.QueryRowContext(ctx, deviceActionRequestSelectSQL+` WHERE id = ?`, id).Scan(dest...)
		})
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) DeleteDeviceActionRequest(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	result, err := s.db.ExecContext(ctx, `DELETE FROM device_action_requests WHERE id = ? AND status IN ('denied','succeeded','failed','expired')`, id)
	if err != nil {
		return err
	}
	return requireP2P3Transition(ctx, s.db, result, "device_action_requests", id, "delete device action request")
}

func (s *Store) DeviceActionRequestStats(ctx context.Context) (DeviceActionRequestStats, error) {
	var stats DeviceActionRequestStats
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'approved' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'executing' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'denied' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN status = 'expired' THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN risk IN ('high','critical') THEN 1 ELSE 0 END),0) FROM device_action_requests`).Scan(&stats.Total, &stats.Pending, &stats.Approved, &stats.Executing, &stats.Succeeded, &stats.Failed, &stats.Denied, &stats.Expired, &stats.HighRisk)
	return stats, err
}

func canonicalDeviceActionRequest(request DeviceActionRequest, create bool) (DeviceActionRequest, error) {
	request.ID = strings.TrimSpace(request.ID)
	request.ConnectionID = strings.TrimSpace(request.ConnectionID)
	request.EntityID = strings.TrimSpace(request.EntityID)
	request.Domain = strings.TrimSpace(request.Domain)
	request.Service = strings.TrimSpace(request.Service)
	request.Risk = strings.TrimSpace(request.Risk)
	request.Status = strings.TrimSpace(request.Status)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.ApprovedBy = strings.TrimSpace(request.ApprovedBy)
	request.ExpiresAt = strings.TrimSpace(request.ExpiresAt)
	request.LastError = strings.TrimSpace(request.LastError)
	request.CompletedAt = strings.TrimSpace(request.CompletedAt)
	if request.ID == "" && create {
		request.ID = NewID()
	}
	if request.Status == "" {
		request.Status = "pending"
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
		token    bool
	}{
		{"device action id", request.ID, 128, true, false},
		{"device action connection id", request.ConnectionID, 128, true, false},
		{"device action entity id", request.EntityID, 256, true, false},
		{"device action domain", request.Domain, 64, true, true},
		{"device action service", request.Service, 64, true, true},
		{"device action requested_by", request.RequestedBy, 200, true, false},
		{"device action approved_by", request.ApprovedBy, 200, false, false},
		{"device action last error", request.LastError, 4096, false, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, field.token); err != nil {
			return DeviceActionRequest{}, err
		}
	}
	if !validDeviceActionRisk(request.Risk) || !validDeviceActionStatus(request.Status) {
		return DeviceActionRequest{}, errors.New("invalid device action risk or status")
	}
	var err error
	request.PayloadJSON, err = normalizeP2P3JSONObject("device action payload", request.PayloadJSON, P2P3PayloadMaxBytes)
	if err != nil {
		return DeviceActionRequest{}, err
	}
	if request.ExpiresAt, err = canonicalP2P3Time("device action expires_at", request.ExpiresAt, true); err != nil {
		return DeviceActionRequest{}, err
	}
	if request.CompletedAt, err = canonicalP2P3Time("device action completed_at", request.CompletedAt, false); err != nil {
		return DeviceActionRequest{}, err
	}
	switch request.Status {
	case "pending":
		if request.ApprovedBy != "" || request.CompletedAt != "" {
			return DeviceActionRequest{}, errors.New("invalid pending device action request")
		}
	case "approved", "executing":
		if request.ApprovedBy == "" || request.CompletedAt != "" {
			return DeviceActionRequest{}, errors.New("invalid approved device action request")
		}
	case "denied", "succeeded", "failed", "expired":
		if request.CompletedAt == "" {
			return DeviceActionRequest{}, errors.New("terminal device action request requires completed_at")
		}
	}
	if request.Status == "failed" && request.LastError == "" {
		return DeviceActionRequest{}, errors.New("failed device action request requires last_error")
	}
	if create {
		if request.Status != "pending" {
			return DeviceActionRequest{}, errors.New("new device action request must be pending")
		}
		now := Now()
		if request.CreatedAt == "" {
			request.CreatedAt = now
		}
		if request.UpdatedAt == "" {
			request.UpdatedAt = request.CreatedAt
		}
	}
	for name, value := range map[string]*string{"device action created_at": &request.CreatedAt, "device action updated_at": &request.UpdatedAt} {
		if *value != "" {
			*value, err = canonicalP2P3Time(name, *value, true)
			if err != nil {
				return DeviceActionRequest{}, err
			}
		}
	}
	return request, nil
}

func scanDeviceActionRequest(scan p2p3Scanner) (DeviceActionRequest, error) {
	var item DeviceActionRequest
	var payload string
	if err := scan(&item.ID, &item.ConnectionID, &item.EntityID, &item.Domain, &item.Service, &payload, &item.Risk, &item.Status, &item.RequestedBy, &item.ApprovedBy, &item.ExpiresAt, &item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt); err != nil {
		return DeviceActionRequest{}, err
	}
	item.PayloadJSON = json.RawMessage(payload)
	canonical, err := canonicalDeviceActionRequest(item, false)
	if err != nil {
		return DeviceActionRequest{}, fmt.Errorf("stored device action request %s is invalid: %w", item.ID, err)
	}
	return canonical, nil
}

func parseDeviceActionRequestListOptions(args []any) (DeviceActionRequestListOptions, error) {
	var options DeviceActionRequestListOptions
	if len(args) > 1 {
		return options, errors.New("too many device action list options")
	}
	if len(args) == 1 && args[0] != nil {
		switch value := args[0].(type) {
		case DeviceActionRequestListOptions:
			options = value
		case *DeviceActionRequestListOptions:
			if value != nil {
				options = *value
			}
		case string:
			options.ConnectionID = value
		default:
			return options, errors.New("invalid device action list options")
		}
	}
	options.ConnectionID = strings.TrimSpace(options.ConnectionID)
	options.Status = strings.TrimSpace(options.Status)
	options.Risk = strings.TrimSpace(options.Risk)
	if options.Status != "" && !validDeviceActionStatus(options.Status) {
		return options, errors.New("invalid device action status filter")
	}
	if options.Risk != "" && !validDeviceActionRisk(options.Risk) {
		return options, errors.New("invalid device action risk filter")
	}
	if options.Limit < 0 || options.Limit > P2P3MaxListLimit || options.Offset < 0 {
		return options, errors.New("invalid device action pagination")
	}
	return options, nil
}

func validDeviceActionRisk(value string) bool {
	switch value {
	case "low", "medium", "high", "critical":
		return true
	default:
		return false
	}
}

func validDeviceActionStatus(value string) bool {
	switch value {
	case "pending", "approved", "denied", "executing", "succeeded", "failed", "expired":
		return true
	default:
		return false
	}
}

func normalizeP2P3JSONObject(label string, raw json.RawMessage, maxBytes int) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = `{}`
	}
	if len(trimmed) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be a valid JSON object", label)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%s must be a valid JSON object", label)
	}
	if key, found := p2p3SensitiveKey(object); found {
		return nil, fmt.Errorf("%s contains forbidden sensitive key %q", label, key)
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", label, err)
	}
	if len(encoded) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return json.RawMessage(encoded), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}

func p2p3SensitiveKey(value any) (string, bool) {
	if key, found := automationAuditSensitiveKey(value); found {
		return key, true
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := normalizeP2P3Key(key)
			if normalized == "raw" || strings.Contains(normalized, "rawpayload") || strings.Contains(normalized, "rawbody") || strings.Contains(normalized, "rawrequest") || strings.Contains(normalized, "requestbody") {
				return key, true
			}
			if nested, found := p2p3SensitiveKey(child); found {
				return nested, true
			}
		}
	case []any:
		for _, child := range typed {
			if nested, found := p2p3SensitiveKey(child); found {
				return nested, true
			}
		}
	}
	return "", false
}

func normalizeP2P3Key(value string) string {
	var normalized strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			normalized.WriteRune(char)
		}
	}
	return normalized.String()
}

func validateP2P3Text(name, value string, maxBytes int, required, token bool) error {
	if required && value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value == "" {
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid %s", name)
	}
	if token && !validAutomationAuditToken(value) {
		return fmt.Errorf("invalid %s", name)
	}
	return nil
}

func canonicalP2P3Time(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("%s is required", name)
		}
		return "", nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("invalid %s", name)
	}
	// Canonical stored timestamps use the fixed-width layout shared with Now()
	// so a value round-trips byte-for-byte through CAS predicates and keyset
	// cursors. RFC3339Nano would strip trailing zeros and break both.
	return parsed.UTC().Format(timestampLayout), nil
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// p2p3TimeCAS canonicalizes a caller-supplied timestamp that is used as a
// compare-and-swap predicate against a stored value. Writers store canonical
// text, so an equally valid but differently formatted input (for example
// RFC3339Nano, which strips trailing zeros) must be normalized or the predicate
// silently matches no rows and the operation reports a spurious conflict.
func p2p3TimeCAS(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	canonical, err := canonicalP2P3Time("timestamp", value, false)
	if err != nil {
		// Not a timestamp we can canonicalize; compare it verbatim so a
		// malformed predicate still fails closed instead of matching rows.
		return value
	}
	return canonical
}

func p2p3TimeAfter(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	return leftErr == nil && rightErr == nil && leftTime.After(rightTime)
}

func nextP2P3UpdatedAt(previous string) string {
	now := time.Now().UTC()
	if prior, err := time.Parse(time.RFC3339Nano, previous); err == nil && !now.After(prior) {
		now = prior.Add(time.Nanosecond)
	}
	return now.Format(timestampLayout)
}

func normalizedP2P3Limit(limit, fallback int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > P2P3MaxListLimit {
		return P2P3MaxListLimit
	}
	return limit
}

func validateHTTPStatus(status int, required bool) error {
	if status == 0 && !required {
		return nil
	}
	if status < 100 || status > 599 {
		return errors.New("invalid notification HTTP status")
	}
	return nil
}

type p2p3RowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireP2P3Transition(ctx context.Context, query p2p3RowQuerier, result sql.Result, table, id, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	if err := query.QueryRowContext(ctx, `SELECT 1 FROM `+quoteIdentifier(table)+` WHERE id = ?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	return fmt.Errorf("%w: cannot %s", ErrConflict, action)
}
