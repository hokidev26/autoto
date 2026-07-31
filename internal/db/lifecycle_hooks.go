package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/hooks"
)

const lifecycleHookDocumentLimit = 256 << 10

const lifecycleHookSchemaSQL = `
CREATE TABLE IF NOT EXISTS lifecycle_hooks (
  id TEXT PRIMARY KEY,
  event_name TEXT NOT NULL CHECK (event_name IN ('run.before','run.after','tool.before','tool.after')),
  scope_kind TEXT NOT NULL CHECK (scope_kind IN ('global','project','agent')),
  scope_id TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL CHECK (priority BETWEEN -1000 AND 1000),
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
  mode TEXT NOT NULL CHECK (mode IN ('sync','async')),
  failure_policy TEXT NOT NULL CHECK (failure_policy IN ('continue','fail_run','retry','disable_hook')),
  document_json TEXT NOT NULL CHECK (json_valid(document_json)),
  revision INTEGER NOT NULL CHECK (revision >= 1),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK ((scope_kind = 'global' AND scope_id = '') OR (scope_kind IN ('project','agent') AND length(scope_id) BETWEEN 1 AND 128))
);
CREATE INDEX IF NOT EXISTS lifecycle_hooks_dispatch_idx ON lifecycle_hooks (event_name, enabled, scope_kind, scope_id, priority DESC, id);
CREATE TABLE IF NOT EXISTS lifecycle_hook_run_bindings (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL UNIQUE,
  snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json)),
  snapshot_digest TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active','closed')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS lifecycle_hook_events (
  id TEXT PRIMARY KEY,
  binding_id TEXT NOT NULL REFERENCES lifecycle_hook_run_bindings(id) ON DELETE CASCADE,
  run_id TEXT NOT NULL,
  event_name TEXT NOT NULL CHECK (event_name IN ('run.before','run.after','tool.before','tool.after')),
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed','cancelled')),
  error_text TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS lifecycle_hook_events_run_idx ON lifecycle_hook_events (run_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_lifecycle_hook_events_binding ON lifecycle_hook_events(binding_id);
CREATE TABLE IF NOT EXISTS lifecycle_hook_executions (
  id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL REFERENCES lifecycle_hook_events(id) ON DELETE CASCADE,
  hook_id TEXT NOT NULL,
  hook_revision INTEGER NOT NULL CHECK (hook_revision >= 1),
  mode TEXT NOT NULL CHECK (mode IN ('sync','async')),
  failure_policy TEXT NOT NULL CHECK (failure_policy IN ('continue','fail_run','retry','disable_hook')),
  status TEXT NOT NULL CHECK (status IN ('pending','running','succeeded','failed','cancelled')),
  retry_of_execution_id TEXT NOT NULL DEFAULT '',
  cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancel_requested IN (0,1)),
  result_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(result_json)),
  error_text TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  completed_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS lifecycle_hook_executions_hook_idx ON lifecycle_hook_executions (hook_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS lifecycle_hook_executions_event_idx ON lifecycle_hook_executions (event_id, created_at ASC, id ASC);
CREATE TABLE IF NOT EXISTS lifecycle_hook_attempts (
  id TEXT PRIMARY KEY,
  execution_id TEXT NOT NULL REFERENCES lifecycle_hook_executions(id) ON DELETE CASCADE,
  attempt_number INTEGER NOT NULL CHECK (attempt_number >= 1),
  status TEXT NOT NULL CHECK (status IN ('running','succeeded','failed','cancelled')),
  request_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(request_json)),
  response_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_json)),
  error_text TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  completed_at TEXT NOT NULL DEFAULT '',
  UNIQUE (execution_id, attempt_number)
);
CREATE INDEX IF NOT EXISTS lifecycle_hook_attempts_execution_idx ON lifecycle_hook_attempts (execution_id, attempt_number ASC);
`

type LifecycleHookRunBinding struct {
	ID        string                 `json:"id"`
	RunID     string                 `json:"runId"`
	Snapshot  hooks.Snapshot         `json:"snapshot"`
	Status    hooks.RunBindingStatus `json:"status"`
	CreatedAt string                 `json:"createdAt"`
	UpdatedAt string                 `json:"updatedAt"`
}

type LifecycleHookEvent struct {
	ID        string            `json:"id"`
	BindingID string            `json:"bindingId"`
	RunID     string            `json:"runId"`
	Name      hooks.EventName   `json:"name"`
	Payload   json.RawMessage   `json:"payload"`
	Status    hooks.EventStatus `json:"status"`
	Error     string            `json:"error,omitempty"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
}

type LifecycleHookExecution struct {
	ID                 string                `json:"id"`
	EventID            string                `json:"eventId"`
	HookID             string                `json:"hookId"`
	HookRevision       int64                 `json:"hookRevision"`
	Mode               hooks.DispatchMode    `json:"mode"`
	FailurePolicy      hooks.FailurePolicy   `json:"failurePolicy"`
	Status             hooks.ExecutionStatus `json:"status"`
	RetryOfExecutionID string                `json:"retryOfExecutionId,omitempty"`
	CancelRequested    bool                  `json:"cancelRequested"`
	Result             json.RawMessage       `json:"result"`
	Error              string                `json:"error,omitempty"`
	CreatedAt          string                `json:"createdAt"`
	StartedAt          string                `json:"startedAt,omitempty"`
	CompletedAt        string                `json:"completedAt,omitempty"`
	UpdatedAt          string                `json:"updatedAt"`
}

type LifecycleHookAttempt struct {
	ID            string              `json:"id"`
	ExecutionID   string              `json:"executionId"`
	AttemptNumber int                 `json:"attemptNumber"`
	Status        hooks.AttemptStatus `json:"status"`
	Request       json.RawMessage     `json:"request"`
	Response      json.RawMessage     `json:"response"`
	Error         string              `json:"error,omitempty"`
	CreatedAt     string              `json:"createdAt"`
	CompletedAt   string              `json:"completedAt,omitempty"`
}

func (s *Store) EnsureLifecycleHookSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, lifecycleHookSchemaSQL)
	return err
}

func (s *Store) CreateLifecycleHook(ctx context.Context, input hooks.Hook) (hooks.Hook, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return hooks.Hook{}, err
	}
	canonical, err := hooks.NormalizeAndValidateHook(input)
	if err != nil {
		return hooks.Hook{}, err
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	canonical.Revision = 1
	now := Now()
	canonical.CreatedAt, canonical.UpdatedAt = now, now
	document, err := lifecycleHookDocument(canonical)
	if err != nil {
		return hooks.Hook{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO lifecycle_hooks (id,event_name,scope_kind,scope_id,priority,enabled,mode,failure_policy,document_json,revision,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, canonical.ID, canonical.Event, canonical.Scope.Kind, canonical.Scope.ID, canonical.Priority, boolInt(canonical.Enabled), canonical.Mode, canonical.FailurePolicy, document, canonical.Revision, now, now)
	if err != nil {
		if isUniqueConstraint(err) {
			return hooks.Hook{}, fmt.Errorf("%w: lifecycle hook already exists", ErrConflict)
		}
		return hooks.Hook{}, err
	}
	return canonical, nil
}

func (s *Store) GetLifecycleHook(ctx context.Context, id string) (hooks.Hook, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return hooks.Hook{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return hooks.Hook{}, sql.ErrNoRows
	}
	return scanLifecycleHook(s.db.QueryRowContext(ctx, `SELECT document_json,revision,created_at,updated_at FROM lifecycle_hooks WHERE id=?`, id).Scan)
}

func (s *Store) ListLifecycleHooks(ctx context.Context) ([]hooks.Hook, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT document_json,revision,created_at,updated_at FROM lifecycle_hooks ORDER BY priority DESC,id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]hooks.Hook, 0)
	for rows.Next() {
		hook, err := scanLifecycleHook(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, hook)
	}
	return result, rows.Err()
}

func (s *Store) UpdateLifecycleHookCAS(ctx context.Context, id string, expectedRevision int64, input hooks.Hook) (hooks.Hook, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return hooks.Hook{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" || expectedRevision < 1 {
		return hooks.Hook{}, errors.New("lifecycle hook id and positive expected revision are required")
	}
	canonical, err := hooks.NormalizeAndValidateHook(input)
	if err != nil {
		return hooks.Hook{}, err
	}
	canonical.ID = id
	canonical.Revision = expectedRevision + 1
	current, err := s.GetLifecycleHook(ctx, id)
	if err != nil {
		return hooks.Hook{}, err
	}
	canonical.CreatedAt = current.CreatedAt
	canonical.UpdatedAt = Now()
	document, err := lifecycleHookDocument(canonical)
	if err != nil {
		return hooks.Hook{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_hooks SET event_name=?,scope_kind=?,scope_id=?,priority=?,enabled=?,mode=?,failure_policy=?,document_json=?,revision=?,updated_at=? WHERE id=? AND revision=?`, canonical.Event, canonical.Scope.Kind, canonical.Scope.ID, canonical.Priority, boolInt(canonical.Enabled), canonical.Mode, canonical.FailurePolicy, document, canonical.Revision, canonical.UpdatedAt, id, expectedRevision)
	if err != nil {
		return hooks.Hook{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return hooks.Hook{}, err
	}
	if affected != 1 {
		return hooks.Hook{}, fmt.Errorf("%w: lifecycle hook changed", ErrConflict)
	}
	return canonical, nil
}

func (s *Store) DeleteLifecycleHookCAS(ctx context.Context, id string, expectedRevision int64) error {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" || expectedRevision < 1 {
		return errors.New("lifecycle hook id and positive expected revision are required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM lifecycle_hooks WHERE id=? AND revision=?`, id, expectedRevision)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	if _, err := s.GetLifecycleHook(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("%w: lifecycle hook changed", ErrConflict)
}

func (s *Store) CreateLifecycleHookRunBinding(ctx context.Context, runID string, snapshot hooks.Snapshot) (LifecycleHookRunBinding, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookRunBinding{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" || len(runID) > 128 || snapshot.Version != 1 || snapshot.Digest == "" {
		return LifecycleHookRunBinding{}, errors.New("invalid lifecycle hook run binding")
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > lifecycleHookDocumentLimit {
		return LifecycleHookRunBinding{}, errors.New("invalid lifecycle hook snapshot")
	}
	now := Now()
	binding := LifecycleHookRunBinding{ID: NewID(), RunID: runID, Snapshot: snapshot, Status: hooks.BindingActive, CreatedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO lifecycle_hook_run_bindings (id,run_id,snapshot_json,snapshot_digest,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, binding.ID, binding.RunID, string(data), snapshot.Digest, binding.Status, now, now)
	if err != nil {
		if isUniqueConstraint(err) {
			return LifecycleHookRunBinding{}, fmt.Errorf("%w: run already has a hook binding", ErrConflict)
		}
		return LifecycleHookRunBinding{}, err
	}
	return binding, nil
}

func (s *Store) GetLifecycleHookRunBinding(ctx context.Context, runID string) (LifecycleHookRunBinding, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookRunBinding{}, err
	}
	var binding LifecycleHookRunBinding
	var snapshotJSON string
	err := s.db.QueryRowContext(ctx, `SELECT id,run_id,snapshot_json,status,created_at,updated_at FROM lifecycle_hook_run_bindings WHERE run_id=?`, strings.TrimSpace(runID)).Scan(&binding.ID, &binding.RunID, &snapshotJSON, &binding.Status, &binding.CreatedAt, &binding.UpdatedAt)
	if err != nil {
		return LifecycleHookRunBinding{}, err
	}
	if err := json.Unmarshal([]byte(snapshotJSON), &binding.Snapshot); err != nil {
		return LifecycleHookRunBinding{}, errors.New("invalid stored lifecycle hook snapshot")
	}
	return binding, nil
}

func (s *Store) CloseLifecycleHookRunBinding(ctx context.Context, runID string) (LifecycleHookRunBinding, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookRunBinding{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_hook_run_bindings SET status='closed',updated_at=? WHERE run_id=? AND status='active'`, now, strings.TrimSpace(runID))
	if err != nil {
		return LifecycleHookRunBinding{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return LifecycleHookRunBinding{}, err
	}
	if affected != 1 {
		return LifecycleHookRunBinding{}, fmt.Errorf("%w: lifecycle hook run binding is not active", ErrConflict)
	}
	return s.GetLifecycleHookRunBinding(ctx, runID)
}

func (s *Store) CreateLifecycleHookEvent(ctx context.Context, event LifecycleHookEvent) (LifecycleHookEvent, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookEvent{}, err
	}
	if event.ID == "" {
		event.ID = NewID()
	}
	event.BindingID = strings.TrimSpace(event.BindingID)
	event.RunID = strings.TrimSpace(event.RunID)
	if event.BindingID == "" || event.RunID == "" || !validLifecycleEventName(event.Name) {
		return LifecycleHookEvent{}, errors.New("invalid lifecycle hook event")
	}
	event.Payload = normalizeLifecycleJSON(event.Payload)
	if !json.Valid(event.Payload) || len(event.Payload) > lifecycleHookDocumentLimit {
		return LifecycleHookEvent{}, errors.New("invalid lifecycle hook event payload")
	}
	if event.Status == "" {
		event.Status = hooks.EventPending
	}
	if !validEventStatus(event.Status) {
		return LifecycleHookEvent{}, errors.New("invalid lifecycle hook event status")
	}
	now := Now()
	event.CreatedAt, event.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO lifecycle_hook_events (id,binding_id,run_id,event_name,payload_json,status,error_text,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, event.ID, event.BindingID, event.RunID, event.Name, string(event.Payload), event.Status, event.Error, now, now)
	if err != nil {
		return LifecycleHookEvent{}, err
	}
	return event, nil
}

func (s *Store) UpdateLifecycleHookEventStatus(ctx context.Context, id string, status hooks.EventStatus, errorText string) (LifecycleHookEvent, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookEvent{}, err
	}
	if !validEventStatus(status) {
		return LifecycleHookEvent{}, errors.New("invalid lifecycle hook event status")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_hook_events SET status=?,error_text=?,updated_at=? WHERE id=?`, status, hooks.RedactText(errorText), Now(), strings.TrimSpace(id))
	if err != nil {
		return LifecycleHookEvent{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return LifecycleHookEvent{}, err
	}
	if affected != 1 {
		return LifecycleHookEvent{}, sql.ErrNoRows
	}
	return s.GetLifecycleHookEvent(ctx, id)
}

func (s *Store) GetLifecycleHookEvent(ctx context.Context, id string) (LifecycleHookEvent, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookEvent{}, err
	}
	var event LifecycleHookEvent
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT id,binding_id,run_id,event_name,payload_json,status,error_text,created_at,updated_at FROM lifecycle_hook_events WHERE id=?`, strings.TrimSpace(id)).Scan(&event.ID, &event.BindingID, &event.RunID, &event.Name, &payload, &event.Status, &event.Error, &event.CreatedAt, &event.UpdatedAt)
	if err != nil {
		return LifecycleHookEvent{}, err
	}
	event.Payload = json.RawMessage(payload)
	return event, nil
}

func (s *Store) CreateLifecycleHookExecution(ctx context.Context, execution LifecycleHookExecution) (LifecycleHookExecution, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookExecution{}, err
	}
	if execution.ID == "" {
		execution.ID = NewID()
	}
	if strings.TrimSpace(execution.EventID) == "" || strings.TrimSpace(execution.HookID) == "" || execution.HookRevision < 1 {
		return LifecycleHookExecution{}, errors.New("invalid lifecycle hook execution")
	}
	if execution.Mode != hooks.ModeSync && execution.Mode != hooks.ModeAsync {
		return LifecycleHookExecution{}, errors.New("invalid lifecycle hook execution mode")
	}
	if !validFailurePolicy(execution.FailurePolicy) {
		return LifecycleHookExecution{}, errors.New("invalid lifecycle hook execution failure policy")
	}
	if execution.Status == "" {
		execution.Status = hooks.ExecutionPending
	}
	if !validExecutionStatus(execution.Status) {
		return LifecycleHookExecution{}, errors.New("invalid lifecycle hook execution status")
	}
	execution.Result = normalizeLifecycleJSON(execution.Result)
	if !json.Valid(execution.Result) || len(execution.Result) > lifecycleHookDocumentLimit {
		return LifecycleHookExecution{}, errors.New("invalid lifecycle hook execution result")
	}
	now := Now()
	execution.CreatedAt, execution.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO lifecycle_hook_executions (id,event_id,hook_id,hook_revision,mode,failure_policy,status,retry_of_execution_id,cancel_requested,result_json,error_text,created_at,started_at,completed_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, execution.ID, execution.EventID, execution.HookID, execution.HookRevision, execution.Mode, execution.FailurePolicy, execution.Status, execution.RetryOfExecutionID, boolInt(execution.CancelRequested), string(execution.Result), hooks.RedactText(execution.Error), now, execution.StartedAt, execution.CompletedAt, now)
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	return execution, nil
}

func (s *Store) GetLifecycleHookExecution(ctx context.Context, id string) (LifecycleHookExecution, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookExecution{}, err
	}
	return scanLifecycleExecution(s.db.QueryRowContext(ctx, `SELECT id,event_id,hook_id,hook_revision,mode,failure_policy,status,retry_of_execution_id,cancel_requested,result_json,error_text,created_at,started_at,completed_at,updated_at FROM lifecycle_hook_executions WHERE id=?`, strings.TrimSpace(id)).Scan)
}

func (s *Store) ListLifecycleHookExecutions(ctx context.Context, hookID string, limit int) ([]LifecycleHookExecution, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,event_id,hook_id,hook_revision,mode,failure_policy,status,retry_of_execution_id,cancel_requested,result_json,error_text,created_at,started_at,completed_at,updated_at FROM lifecycle_hook_executions WHERE hook_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, strings.TrimSpace(hookID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]LifecycleHookExecution, 0)
	for rows.Next() {
		execution, err := scanLifecycleExecution(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, execution)
	}
	return result, rows.Err()
}

func (s *Store) TransitionLifecycleHookExecution(ctx context.Context, id string, to hooks.ExecutionStatus, resultJSON json.RawMessage, errorText string) (LifecycleHookExecution, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookExecution{}, err
	}
	current, err := s.GetLifecycleHookExecution(ctx, id)
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	if !validExecutionTransition(current.Status, to) {
		return LifecycleHookExecution{}, fmt.Errorf("%w: invalid lifecycle hook execution transition", ErrConflict)
	}
	resultJSON = normalizeLifecycleJSON(resultJSON)
	if !json.Valid(resultJSON) || len(resultJSON) > lifecycleHookDocumentLimit {
		return LifecycleHookExecution{}, errors.New("invalid lifecycle hook execution result")
	}
	now := Now()
	started := current.StartedAt
	completed := current.CompletedAt
	if to == hooks.ExecutionRunning && started == "" {
		started = now
	}
	if isTerminalExecution(to) {
		completed = now
	}
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_hook_executions SET status=?,result_json=?,error_text=?,started_at=?,completed_at=?,updated_at=? WHERE id=? AND status=?`, to, string(hooks.RedactJSON(resultJSON)), hooks.RedactText(errorText), started, completed, now, id, current.Status)
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	if affected != 1 {
		return LifecycleHookExecution{}, fmt.Errorf("%w: lifecycle hook execution changed", ErrConflict)
	}
	return s.GetLifecycleHookExecution(ctx, id)
}

func (s *Store) CancelLifecycleHookExecution(ctx context.Context, id string) (LifecycleHookExecution, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookExecution{}, err
	}
	current, err := s.GetLifecycleHookExecution(ctx, id)
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	if isTerminalExecution(current.Status) {
		return LifecycleHookExecution{}, fmt.Errorf("%w: lifecycle hook execution already completed", ErrConflict)
	}
	now := Now()
	status := current.Status
	completed := current.CompletedAt
	if current.Status == hooks.ExecutionPending {
		status = hooks.ExecutionCancelled
		completed = now
	}
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_hook_executions SET cancel_requested=1,status=?,completed_at=?,updated_at=? WHERE id=? AND status=?`, status, completed, now, id, current.Status)
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	if affected != 1 {
		return LifecycleHookExecution{}, fmt.Errorf("%w: lifecycle hook execution changed", ErrConflict)
	}
	return s.GetLifecycleHookExecution(ctx, id)
}

func (s *Store) RetryLifecycleHookExecution(ctx context.Context, id string) (LifecycleHookExecution, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookExecution{}, err
	}
	current, err := s.GetLifecycleHookExecution(ctx, id)
	if err != nil {
		return LifecycleHookExecution{}, err
	}
	if current.Status != hooks.ExecutionFailed && current.Status != hooks.ExecutionCancelled {
		return LifecycleHookExecution{}, fmt.Errorf("%w: only failed or cancelled executions can be retried", ErrConflict)
	}
	return s.CreateLifecycleHookExecution(ctx, LifecycleHookExecution{EventID: current.EventID, HookID: current.HookID, HookRevision: current.HookRevision, Mode: current.Mode, FailurePolicy: current.FailurePolicy, Status: hooks.ExecutionPending, RetryOfExecutionID: current.ID, Result: json.RawMessage(`{}`)})
}

func (s *Store) CreateLifecycleHookAttempt(ctx context.Context, attempt LifecycleHookAttempt) (LifecycleHookAttempt, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookAttempt{}, err
	}
	if attempt.ID == "" {
		attempt.ID = NewID()
	}
	if strings.TrimSpace(attempt.ExecutionID) == "" || attempt.AttemptNumber < 1 {
		return LifecycleHookAttempt{}, errors.New("invalid lifecycle hook attempt")
	}
	if attempt.Status == "" {
		attempt.Status = hooks.AttemptRunning
	}
	if !validAttemptStatus(attempt.Status) {
		return LifecycleHookAttempt{}, errors.New("invalid lifecycle hook attempt status")
	}
	attempt.Request = normalizeLifecycleJSON(attempt.Request)
	attempt.Response = normalizeLifecycleJSON(attempt.Response)
	if !json.Valid(attempt.Request) || !json.Valid(attempt.Response) || len(attempt.Request) > lifecycleHookDocumentLimit || len(attempt.Response) > lifecycleHookDocumentLimit {
		return LifecycleHookAttempt{}, errors.New("invalid lifecycle hook attempt payload")
	}
	attempt.Request = hooks.RedactJSON(attempt.Request)
	attempt.Response = hooks.RedactJSON(attempt.Response)
	attempt.Error = hooks.RedactText(attempt.Error)
	attempt.CreatedAt = Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO lifecycle_hook_attempts (id,execution_id,attempt_number,status,request_json,response_json,error_text,created_at,completed_at) VALUES (?,?,?,?,?,?,?,?,?)`, attempt.ID, attempt.ExecutionID, attempt.AttemptNumber, attempt.Status, string(attempt.Request), string(attempt.Response), attempt.Error, attempt.CreatedAt, attempt.CompletedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return LifecycleHookAttempt{}, fmt.Errorf("%w: lifecycle hook attempt already exists", ErrConflict)
		}
		return LifecycleHookAttempt{}, err
	}
	return attempt, nil
}

func (s *Store) CompleteLifecycleHookAttempt(ctx context.Context, id string, status hooks.AttemptStatus, response json.RawMessage, errorText string) (LifecycleHookAttempt, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookAttempt{}, err
	}
	if status != hooks.AttemptSucceeded && status != hooks.AttemptFailed && status != hooks.AttemptCancelled {
		return LifecycleHookAttempt{}, errors.New("invalid lifecycle hook attempt completion status")
	}
	response = normalizeLifecycleJSON(response)
	if !json.Valid(response) || len(response) > lifecycleHookDocumentLimit {
		return LifecycleHookAttempt{}, errors.New("invalid lifecycle hook attempt response")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_hook_attempts SET status=?,response_json=?,error_text=?,completed_at=? WHERE id=? AND status='running'`, status, string(hooks.RedactJSON(response)), hooks.RedactText(errorText), Now(), strings.TrimSpace(id))
	if err != nil {
		return LifecycleHookAttempt{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return LifecycleHookAttempt{}, err
	}
	if affected != 1 {
		return LifecycleHookAttempt{}, fmt.Errorf("%w: lifecycle hook attempt changed", ErrConflict)
	}
	return s.GetLifecycleHookAttempt(ctx, id)
}

func (s *Store) GetLifecycleHookAttempt(ctx context.Context, id string) (LifecycleHookAttempt, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return LifecycleHookAttempt{}, err
	}
	var attempt LifecycleHookAttempt
	var request, response string
	err := s.db.QueryRowContext(ctx, `SELECT id,execution_id,attempt_number,status,request_json,response_json,error_text,created_at,completed_at FROM lifecycle_hook_attempts WHERE id=?`, strings.TrimSpace(id)).Scan(&attempt.ID, &attempt.ExecutionID, &attempt.AttemptNumber, &attempt.Status, &request, &response, &attempt.Error, &attempt.CreatedAt, &attempt.CompletedAt)
	if err != nil {
		return LifecycleHookAttempt{}, err
	}
	attempt.Request = json.RawMessage(request)
	attempt.Response = json.RawMessage(response)
	return attempt, nil
}

func (s *Store) ListLifecycleHookAttempts(ctx context.Context, executionID string) ([]LifecycleHookAttempt, error) {
	if err := s.EnsureLifecycleHookSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,execution_id,attempt_number,status,request_json,response_json,error_text,created_at,completed_at FROM lifecycle_hook_attempts WHERE execution_id=? ORDER BY attempt_number ASC`, strings.TrimSpace(executionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]LifecycleHookAttempt, 0)
	for rows.Next() {
		var item LifecycleHookAttempt
		var request, response string
		if err := rows.Scan(&item.ID, &item.ExecutionID, &item.AttemptNumber, &item.Status, &request, &response, &item.Error, &item.CreatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		item.Request = json.RawMessage(request)
		item.Response = json.RawMessage(response)
		result = append(result, item)
	}
	return result, rows.Err()
}

func lifecycleHookDocument(hook hooks.Hook) (string, error) {
	data, err := json.Marshal(hook)
	if err != nil || len(data) > lifecycleHookDocumentLimit {
		return "", errors.New("lifecycle hook document is too large")
	}
	return string(data), nil
}
func scanLifecycleHook(scan func(...any) error) (hooks.Hook, error) {
	var document, created, updated string
	var revision int64
	if err := scan(&document, &revision, &created, &updated); err != nil {
		return hooks.Hook{}, err
	}
	var hook hooks.Hook
	if err := json.Unmarshal([]byte(document), &hook); err != nil {
		return hooks.Hook{}, errors.New("invalid stored lifecycle hook")
	}
	hook.Revision = revision
	hook.CreatedAt = created
	hook.UpdatedAt = updated
	canonical, err := hooks.NormalizeAndValidateHook(hook)
	if err != nil {
		return hooks.Hook{}, fmt.Errorf("invalid stored lifecycle hook: %w", err)
	}
	canonical.Revision = revision
	canonical.CreatedAt = created
	canonical.UpdatedAt = updated
	return canonical, nil
}
func scanLifecycleExecution(scan func(...any) error) (LifecycleHookExecution, error) {
	var execution LifecycleHookExecution
	var cancel int
	var result string
	if err := scan(&execution.ID, &execution.EventID, &execution.HookID, &execution.HookRevision, &execution.Mode, &execution.FailurePolicy, &execution.Status, &execution.RetryOfExecutionID, &cancel, &result, &execution.Error, &execution.CreatedAt, &execution.StartedAt, &execution.CompletedAt, &execution.UpdatedAt); err != nil {
		return LifecycleHookExecution{}, err
	}
	if cancel != 0 && cancel != 1 {
		return LifecycleHookExecution{}, errors.New("invalid stored lifecycle hook execution")
	}
	execution.CancelRequested = cancel == 1
	execution.Result = json.RawMessage(result)
	return execution, nil
}
func normalizeLifecycleJSON(raw json.RawMessage) json.RawMessage {
	if strings.TrimSpace(string(raw)) == "" {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), raw...)
}
func validLifecycleEventName(value hooks.EventName) bool {
	return value == hooks.EventRunBefore || value == hooks.EventRunAfter || value == hooks.EventToolBefore || value == hooks.EventToolAfter
}
func validEventStatus(value hooks.EventStatus) bool {
	return value == hooks.EventPending || value == hooks.EventRunning || value == hooks.EventCompleted || value == hooks.EventFailed || value == hooks.EventCancelled
}
func validExecutionStatus(value hooks.ExecutionStatus) bool {
	return value == hooks.ExecutionPending || value == hooks.ExecutionRunning || value == hooks.ExecutionSucceeded || value == hooks.ExecutionFailed || value == hooks.ExecutionCancelled
}
func validAttemptStatus(value hooks.AttemptStatus) bool {
	return value == hooks.AttemptRunning || value == hooks.AttemptSucceeded || value == hooks.AttemptFailed || value == hooks.AttemptCancelled
}
func validFailurePolicy(value hooks.FailurePolicy) bool {
	return value == hooks.FailureContinue || value == hooks.FailureFailRun || value == hooks.FailureRetry || value == hooks.FailureDisableHook
}
func isTerminalExecution(value hooks.ExecutionStatus) bool {
	return value == hooks.ExecutionSucceeded || value == hooks.ExecutionFailed || value == hooks.ExecutionCancelled
}
func validExecutionTransition(from, to hooks.ExecutionStatus) bool {
	if !validExecutionStatus(to) || from == to {
		return false
	}
	switch from {
	case hooks.ExecutionPending:
		return to == hooks.ExecutionRunning || to == hooks.ExecutionCancelled || to == hooks.ExecutionFailed
	case hooks.ExecutionRunning:
		return isTerminalExecution(to)
	default:
		return false
	}
}
