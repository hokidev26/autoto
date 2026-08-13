package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	BackgroundTaskKindShell = "shell"
	BackgroundTaskKindAgent = "agent"

	BackgroundTaskStatusQueued          = "queued"
	BackgroundTaskStatusWaitingApproval = "waiting_approval"
	BackgroundTaskStatusRunning         = "running"
	BackgroundTaskStatusCancelRequested = "cancel_requested"
	BackgroundTaskStatusSucceeded       = "succeeded"
	BackgroundTaskStatusFailed          = "failed"
	BackgroundTaskStatusCanceled        = "canceled"
	BackgroundTaskStatusInterrupted     = "interrupted"

	BackgroundTaskOutputChunkBytes = 16 * 1024
	BackgroundTaskDefaultOutputMax = 4 * 1024 * 1024
	BackgroundTaskDefaultListLimit = 50
	BackgroundTaskMaxListLimit     = 200
)

var backgroundTaskTruncatedMarker = []byte("\n[output truncated]\n")

type BackgroundTask struct {
	ID                           string          `json:"id"`
	OwnerAgentID                 string          `json:"ownerAgentId"`
	ParentRunID                  string          `json:"parentRunId,omitempty"`
	ParentToolUseID              string          `json:"parentToolUseId,omitempty"`
	Kind                         string          `json:"kind"`
	Status                       string          `json:"status"`
	Revision                     int64           `json:"revision"`
	Priority                     int             `json:"priority"`
	PayloadJSON                  json.RawMessage `json:"-"`
	PublicSummaryJSON            json.RawMessage `json:"publicSummary,omitempty"`
	PermissionModeCap            string          `json:"permissionModeCap,omitempty"`
	PermissionGenerationSnapshot int64           `json:"permissionGenerationSnapshot"`
	PolicyGenerationSnapshot     int64           `json:"policyGenerationSnapshot"`
	AgentGenerationSnapshot      int64           `json:"agentGenerationSnapshot"`
	ToolCatalogDigest            string          `json:"toolCatalogDigest,omitempty"`
	WorkspaceFingerprint         string          `json:"workspaceFingerprint,omitempty"`
	ChildAgentID                 string          `json:"childAgentId,omitempty"`
	ChildRunID                   string          `json:"childRunId,omitempty"`
	ResumeParent                 bool            `json:"resumeParent"`
	AttemptCount                 int             `json:"attemptCount"`
	MaxAttempts                  int             `json:"maxAttempts"`
	WorkerInstanceID             string          `json:"workerInstanceId,omitempty"`
	CancelRequestedAt            string          `json:"cancelRequestedAt,omitempty"`
	StartedAt                    string          `json:"startedAt,omitempty"`
	CompletedAt                  string          `json:"completedAt,omitempty"`
	ResultJSON                   json.RawMessage `json:"result,omitempty"`
	ErrorCode                    string          `json:"errorCode,omitempty"`
	ErrorMessage                 string          `json:"errorMessage,omitempty"`
	ExitCode                     *int            `json:"exitCode,omitempty"`
	LastOutputSequence           int64           `json:"lastOutputSequence"`
	OutputBytes                  int64           `json:"outputBytes"`
	OutputTruncated              bool            `json:"outputTruncated"`
	CreatedAt                    string          `json:"createdAt"`
	UpdatedAt                    string          `json:"updatedAt"`
}

type BackgroundTaskListOptions struct {
	OwnerAgentID string
	ParentRunID  string
	Statuses     []string
	Limit        int
	Offset       int
}

type BackgroundTaskClaimOptions struct {
	WorkerInstanceID     string
	ExcludeOwnerAgentIDs []string
}

type BackgroundTaskTransition struct {
	ExpectedRevision int64
	FromStatuses     []string
	Status           string
	WorkerInstanceID string
	ResultJSON       json.RawMessage
	ErrorCode        string
	ErrorMessage     string
	ExitCode         *int
}

type BackgroundTaskOutput struct {
	TaskID    string `json:"taskId"`
	Sequence  int64  `json:"sequence"`
	Stream    string `json:"stream"`
	Chunk     []byte `json:"chunk"`
	ByteCount int    `json:"byteCount"`
	CreatedAt string `json:"createdAt"`
}

type BackgroundTaskOutputPage struct {
	Items        []BackgroundTaskOutput `json:"items"`
	NextSequence int64                  `json:"nextSequence"`
	HasMore      bool                   `json:"hasMore"`
	Bytes        int                    `json:"bytes"`
	Truncated    bool                   `json:"truncated"`
}

type BackgroundTaskOutputAppendResult struct {
	LastSequence int64 `json:"lastSequence"`
	OutputBytes  int64 `json:"outputBytes"`
	Truncated    bool  `json:"truncated"`
}

type BackgroundTaskStats struct {
	Total          int64            `json:"total"`
	ByStatus       map[string]int64 `json:"byStatus"`
	RunningByAgent map[string]int64 `json:"runningByAgent"`
	OutputBytes    int64            `json:"outputBytes"`
	TruncatedTasks int64            `json:"truncatedTasks"`
}

func (task BackgroundTask) Terminal() bool {
	return backgroundTaskTerminal(task.Status)
}

func (s *backgroundTaskStore) CreateBackgroundTask(ctx context.Context, task BackgroundTask) (BackgroundTask, error) {
	canonical, err := canonicalBackgroundTask(task)
	if err != nil {
		return BackgroundTask{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO background_tasks (
		id, owner_agent_id, parent_run_id, parent_tool_use_id, kind, status, revision, priority,
		payload_json, public_summary_json, permission_mode_cap, permission_generation_snapshot,
		policy_generation_snapshot, agent_generation_snapshot, tool_catalog_digest, workspace_fingerprint,
		child_agent_id, child_run_id, resume_parent, attempt_count, max_attempts, worker_instance_id,
		cancel_requested_at, started_at, completed_at, result_json, error_code, error_message, exit_code,
		last_output_sequence, output_bytes, output_truncated, created_at, updated_at
	) VALUES (?, ?, NULLIF(?,''), NULLIF(?,''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?,''), NULLIF(?,''), ?, ?, ?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), ?, NULLIF(?,''), NULLIF(?,''), ?, ?, ?, ?, ?, ?)`,
		canonical.ID, canonical.OwnerAgentID, canonical.ParentRunID, canonical.ParentToolUseID, canonical.Kind,
		canonical.Status, canonical.Revision, canonical.Priority, string(canonical.PayloadJSON), string(canonical.PublicSummaryJSON),
		canonical.PermissionModeCap, canonical.PermissionGenerationSnapshot, canonical.PolicyGenerationSnapshot,
		canonical.AgentGenerationSnapshot, canonical.ToolCatalogDigest, canonical.WorkspaceFingerprint, canonical.ChildAgentID,
		canonical.ChildRunID, boolInt(canonical.ResumeParent), canonical.AttemptCount, canonical.MaxAttempts,
		canonical.WorkerInstanceID, canonical.CancelRequestedAt, canonical.StartedAt, canonical.CompletedAt,
		string(canonical.ResultJSON), canonical.ErrorCode, canonical.ErrorMessage, canonical.ExitCode,
		canonical.LastOutputSequence, canonical.OutputBytes, boolInt(canonical.OutputTruncated), canonical.CreatedAt, canonical.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return BackgroundTask{}, fmt.Errorf("%w: background task already exists", ErrConflict)
		}
		return BackgroundTask{}, err
	}
	return canonical, nil
}

func (s *backgroundTaskStore) GetBackgroundTask(ctx context.Context, id string) (BackgroundTask, error) {
	return s.getBackgroundTask(ctx, id, false)
}

func (s *backgroundTaskStore) GetBackgroundTaskForExecution(ctx context.Context, id string) (BackgroundTask, error) {
	return s.getBackgroundTask(ctx, id, true)
}

func (s *backgroundTaskStore) getBackgroundTask(ctx context.Context, id string, includePayload bool) (BackgroundTask, error) {
	payloadExpr := `'{}'`
	if includePayload {
		payloadExpr = "payload_json"
	}
	return scanBackgroundTask(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, backgroundTaskSelectSQL(payloadExpr)+` WHERE id = ?`, strings.TrimSpace(id)).Scan(dest...)
	})
}

func (s *backgroundTaskStore) ListBackgroundTasks(ctx context.Context, options BackgroundTaskListOptions) ([]BackgroundTask, error) {
	limit := options.Limit
	if limit == 0 {
		limit = BackgroundTaskDefaultListLimit
	}
	if limit < 1 || limit > BackgroundTaskMaxListLimit {
		return nil, fmt.Errorf("background task limit must be between 1 and %d", BackgroundTaskMaxListLimit)
	}
	if options.Offset < 0 {
		return nil, errors.New("background task offset must not be negative")
	}
	query := backgroundTaskSelectSQL(`'{}'`) + ` WHERE 1 = 1`
	args := make([]any, 0, len(options.Statuses)+4)
	if owner := strings.TrimSpace(options.OwnerAgentID); owner != "" {
		query += ` AND owner_agent_id = ?`
		args = append(args, owner)
	}
	if parentRun := strings.TrimSpace(options.ParentRunID); parentRun != "" {
		query += ` AND parent_run_id = ?`
		args = append(args, parentRun)
	}
	if len(options.Statuses) > 0 {
		query += ` AND status IN (` + placeholders(len(options.Statuses)) + `)`
		for _, status := range options.Statuses {
			status = strings.TrimSpace(status)
			if !validBackgroundTaskStatus(status) {
				return nil, fmt.Errorf("invalid background task status %q", status)
			}
			args = append(args, status)
		}
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, options.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]BackgroundTask, 0, limit)
	for rows.Next() {
		task, err := scanBackgroundTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *backgroundTaskStore) ClaimQueuedBackgroundTask(ctx context.Context, options BackgroundTaskClaimOptions) (BackgroundTask, error) {
	workerID := strings.TrimSpace(options.WorkerInstanceID)
	if err := validateBackgroundTaskText("worker instance id", workerID, 256, true); err != nil {
		return BackgroundTask{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BackgroundTask{}, err
	}
	defer tx.Rollback()
	query := backgroundTaskSelectSQL("payload_json") + ` WHERE status = 'queued' AND attempt_count < max_attempts`
	args := make([]any, 0, len(options.ExcludeOwnerAgentIDs)+1)
	if len(options.ExcludeOwnerAgentIDs) > 0 {
		query += ` AND owner_agent_id NOT IN (` + placeholders(len(options.ExcludeOwnerAgentIDs)) + `)`
		for _, id := range options.ExcludeOwnerAgentIDs {
			args = append(args, strings.TrimSpace(id))
		}
	}
	query += ` ORDER BY priority DESC, created_at ASC, id ASC LIMIT 1`
	task, err := scanBackgroundTask(func(dest ...any) error { return tx.QueryRowContext(ctx, query, args...).Scan(dest...) })
	if err != nil {
		return BackgroundTask{}, err
	}
	now := Now()
	result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status = 'running', revision = revision + 1, attempt_count = attempt_count + 1, worker_instance_id = ?, started_at = COALESCE(started_at, ?), completed_at = NULL, error_code = NULL, error_message = NULL, updated_at = ? WHERE id = ? AND status = 'queued' AND revision = ?`, workerID, now, now, task.ID, task.Revision)
	if err != nil {
		return BackgroundTask{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return BackgroundTask{}, err
	} else if affected != 1 {
		return BackgroundTask{}, fmt.Errorf("%w: background task claim lost", ErrConflict)
	}
	claimed, err := scanBackgroundTask(func(dest ...any) error {
		return tx.QueryRowContext(ctx, backgroundTaskSelectSQL("payload_json")+` WHERE id = ?`, task.ID).Scan(dest...)
	})
	if err != nil {
		return BackgroundTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return BackgroundTask{}, err
	}
	return claimed, nil
}

func (s *backgroundTaskStore) TransitionBackgroundTask(ctx context.Context, taskID string, transition BackgroundTaskTransition) (BackgroundTask, error) {
	transition.Status = strings.TrimSpace(transition.Status)
	transition.WorkerInstanceID = strings.TrimSpace(transition.WorkerInstanceID)
	transition.ErrorCode = strings.TrimSpace(transition.ErrorCode)
	transition.ErrorMessage = boundedText(strings.TrimSpace(transition.ErrorMessage), 4096)
	if transition.ExpectedRevision < 1 {
		return BackgroundTask{}, errors.New("background task expected revision must be positive")
	}
	if !validBackgroundTaskStatus(transition.Status) {
		return BackgroundTask{}, fmt.Errorf("invalid background task status %q", transition.Status)
	}
	if err := validateBackgroundTaskText("error code", transition.ErrorCode, 128, false); err != nil {
		return BackgroundTask{}, err
	}
	resultJSON, err := normalizeBackgroundTaskJSON(transition.ResultJSON, `{}`, 32768, true)
	if err != nil {
		return BackgroundTask{}, fmt.Errorf("background task result: %w", err)
	}
	from := make(map[string]struct{}, len(transition.FromStatuses))
	for _, status := range transition.FromStatuses {
		status = strings.TrimSpace(status)
		if !validBackgroundTaskStatus(status) {
			return BackgroundTask{}, fmt.Errorf("invalid background task source status %q", status)
		}
		from[status] = struct{}{}
	}
	if len(from) == 0 {
		return BackgroundTask{}, errors.New("background task transition requires source statuses")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BackgroundTask{}, err
	}
	defer tx.Rollback()
	current, err := scanBackgroundTask(func(dest ...any) error {
		return tx.QueryRowContext(ctx, backgroundTaskSelectSQL("payload_json")+` WHERE id = ?`, strings.TrimSpace(taskID)).Scan(dest...)
	})
	if err != nil {
		return BackgroundTask{}, err
	}
	if current.Revision != transition.ExpectedRevision {
		return BackgroundTask{}, fmt.Errorf("%w: background task revision changed", ErrConflict)
	}
	if _, ok := from[current.Status]; !ok || !validBackgroundTaskTransition(current.Status, transition.Status) {
		return BackgroundTask{}, fmt.Errorf("%w: background task cannot transition from %s to %s", ErrConflict, current.Status, transition.Status)
	}
	now := Now()
	completedAt := ""
	cancelRequestedAt := current.CancelRequestedAt
	workerID := transition.WorkerInstanceID
	if workerID == "" {
		workerID = current.WorkerInstanceID
	}
	if transition.Status == BackgroundTaskStatusCancelRequested && cancelRequestedAt == "" {
		cancelRequestedAt = now
	}
	if backgroundTaskTerminal(transition.Status) {
		completedAt = now
		workerID = ""
	}
	startedAt := current.StartedAt
	if transition.Status == BackgroundTaskStatusRunning && startedAt == "" {
		startedAt = now
	}
	if transition.Status == BackgroundTaskStatusQueued || transition.Status == BackgroundTaskStatusWaitingApproval {
		workerID = ""
		startedAt = ""
		cancelRequestedAt = ""
	}
	result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status = ?, revision = revision + 1, worker_instance_id = NULLIF(?,''), cancel_requested_at = NULLIF(?,''), started_at = NULLIF(?,''), completed_at = NULLIF(?,''), result_json = ?, error_code = NULLIF(?,''), error_message = NULLIF(?,''), exit_code = ?, updated_at = ? WHERE id = ? AND revision = ? AND status = ?`, transition.Status, workerID, cancelRequestedAt, startedAt, completedAt, string(resultJSON), transition.ErrorCode, transition.ErrorMessage, transition.ExitCode, now, current.ID, current.Revision, current.Status)
	if err != nil {
		return BackgroundTask{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return BackgroundTask{}, err
	} else if affected != 1 {
		return BackgroundTask{}, fmt.Errorf("%w: background task transition lost", ErrConflict)
	}
	updated, err := scanBackgroundTask(func(dest ...any) error {
		return tx.QueryRowContext(ctx, backgroundTaskSelectSQL("payload_json")+` WHERE id = ?`, current.ID).Scan(dest...)
	})
	if err != nil {
		return BackgroundTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return BackgroundTask{}, err
	}
	return updated, nil
}

func (s *backgroundTaskStore) RequestBackgroundTaskCancel(ctx context.Context, taskID string) (BackgroundTask, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BackgroundTask{}, err
	}
	defer tx.Rollback()
	current, err := scanBackgroundTask(func(dest ...any) error {
		return tx.QueryRowContext(ctx, backgroundTaskSelectSQL("payload_json")+` WHERE id = ?`, strings.TrimSpace(taskID)).Scan(dest...)
	})
	if err != nil {
		return BackgroundTask{}, err
	}
	if current.Terminal() || current.Status == BackgroundTaskStatusCancelRequested {
		if err := tx.Commit(); err != nil {
			return BackgroundTask{}, err
		}
		return current, nil
	}
	now := Now()
	status := BackgroundTaskStatusCancelRequested
	completedAt := ""
	workerID := current.WorkerInstanceID
	if current.Status == BackgroundTaskStatusQueued || current.Status == BackgroundTaskStatusWaitingApproval {
		status = BackgroundTaskStatusCanceled
		completedAt = now
		workerID = ""
	} else if current.Status != BackgroundTaskStatusRunning {
		return BackgroundTask{}, fmt.Errorf("%w: background task cannot be canceled from %s", ErrConflict, current.Status)
	}
	result, err := tx.ExecContext(ctx, `UPDATE background_tasks SET status = ?, revision = revision + 1, cancel_requested_at = ?, completed_at = NULLIF(?,''), worker_instance_id = NULLIF(?,''), error_code = CASE WHEN ? = 'canceled' THEN 'canceled' ELSE error_code END, error_message = CASE WHEN ? = 'canceled' THEN 'canceled before execution' ELSE error_message END, updated_at = ? WHERE id = ? AND revision = ? AND status = ?`, status, now, completedAt, workerID, status, status, now, current.ID, current.Revision, current.Status)
	if err != nil {
		return BackgroundTask{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return BackgroundTask{}, err
	} else if affected != 1 {
		return BackgroundTask{}, fmt.Errorf("%w: background task cancel lost", ErrConflict)
	}
	updated, err := scanBackgroundTask(func(dest ...any) error {
		return tx.QueryRowContext(ctx, backgroundTaskSelectSQL("payload_json")+` WHERE id = ?`, current.ID).Scan(dest...)
	})
	if err != nil {
		return BackgroundTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return BackgroundTask{}, err
	}
	return updated, nil
}

func (s *backgroundTaskStore) AttachBackgroundTaskChild(ctx context.Context, taskID string, expectedRevision int64, childAgentID, childRunID string) (BackgroundTask, error) {
	if expectedRevision < 1 {
		return BackgroundTask{}, errors.New("background task expected revision must be positive")
	}
	childAgentID = strings.TrimSpace(childAgentID)
	childRunID = strings.TrimSpace(childRunID)
	if childAgentID == "" && childRunID == "" {
		return BackgroundTask{}, errors.New("background task child agent or run is required")
	}
	if err := validateBackgroundTaskText("child agent id", childAgentID, 128, false); err != nil {
		return BackgroundTask{}, err
	}
	if err := validateBackgroundTaskText("child run id", childRunID, 128, false); err != nil {
		return BackgroundTask{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE background_tasks SET child_agent_id = NULLIF(?,''), child_run_id = NULLIF(?,''), revision = revision + 1, updated_at = ? WHERE id = ? AND revision = ? AND status IN ('running','cancel_requested')`, childAgentID, childRunID, now, strings.TrimSpace(taskID), expectedRevision)
	if err != nil {
		return BackgroundTask{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return BackgroundTask{}, err
	} else if affected != 1 {
		if _, getErr := s.GetBackgroundTask(ctx, taskID); errors.Is(getErr, sql.ErrNoRows) {
			return BackgroundTask{}, sql.ErrNoRows
		}
		return BackgroundTask{}, fmt.Errorf("%w: background task child attachment lost", ErrConflict)
	}
	return s.GetBackgroundTaskForExecution(ctx, taskID)
}

func (s *backgroundTaskStore) AppendBackgroundTaskOutput(ctx context.Context, taskID, stream string, chunk []byte, maxBytes int64) (BackgroundTaskOutputAppendResult, error) {
	stream = strings.TrimSpace(stream)
	if !validBackgroundTaskOutputStream(stream) || stream == "truncated" {
		return BackgroundTaskOutputAppendResult{}, fmt.Errorf("invalid background task output stream %q", stream)
	}
	if len(chunk) > BackgroundTaskOutputChunkBytes {
		return BackgroundTaskOutputAppendResult{}, fmt.Errorf("background task output chunk exceeds %d bytes", BackgroundTaskOutputChunkBytes)
	}
	if len(chunk) == 0 {
		task, err := s.GetBackgroundTask(ctx, taskID)
		return BackgroundTaskOutputAppendResult{LastSequence: task.LastOutputSequence, OutputBytes: task.OutputBytes, Truncated: task.OutputTruncated}, err
	}
	if maxBytes <= 0 {
		maxBytes = BackgroundTaskDefaultOutputMax
	}
	if maxBytes > BackgroundTaskDefaultOutputMax {
		maxBytes = BackgroundTaskDefaultOutputMax
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BackgroundTaskOutputAppendResult{}, err
	}
	defer tx.Rollback()
	var sequence, outputBytes int64
	var truncated int
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT last_output_sequence, output_bytes, output_truncated, status FROM background_tasks WHERE id = ?`, strings.TrimSpace(taskID)).Scan(&sequence, &outputBytes, &truncated, &status); err != nil {
		return BackgroundTaskOutputAppendResult{}, err
	}
	if backgroundTaskTerminal(status) {
		return BackgroundTaskOutputAppendResult{}, fmt.Errorf("%w: background task output is closed", ErrConflict)
	}
	if truncated != 0 {
		return BackgroundTaskOutputAppendResult{LastSequence: sequence, OutputBytes: outputBytes, Truncated: true}, tx.Commit()
	}
	marker := backgroundTaskTruncatedMarker
	if int64(len(marker)) > maxBytes {
		marker = marker[:maxBytes]
	}
	dataBudget := maxBytes - int64(len(marker))
	if dataBudget < 0 {
		dataBudget = 0
	}
	available := dataBudget - outputBytes
	toWrite := chunk
	mustTruncate := int64(len(chunk)) > available
	if mustTruncate {
		if available <= 0 {
			toWrite = nil
		} else {
			toWrite = chunk[:available]
		}
	}
	now := Now()
	if len(toWrite) > 0 {
		sequence++
		if _, err := tx.ExecContext(ctx, `INSERT INTO background_task_output (task_id, sequence, stream, chunk_blob, byte_count, created_at) VALUES (?, ?, ?, ?, ?, ?)`, taskID, sequence, stream, toWrite, len(toWrite), now); err != nil {
			return BackgroundTaskOutputAppendResult{}, err
		}
		outputBytes += int64(len(toWrite))
	}
	if mustTruncate {
		if len(marker) > 0 && outputBytes+int64(len(marker)) <= maxBytes {
			sequence++
			if _, err := tx.ExecContext(ctx, `INSERT INTO background_task_output (task_id, sequence, stream, chunk_blob, byte_count, created_at) VALUES (?, ?, 'truncated', ?, ?, ?)`, taskID, sequence, marker, len(marker), now); err != nil {
				return BackgroundTaskOutputAppendResult{}, err
			}
			outputBytes += int64(len(marker))
		}
		truncated = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE background_tasks SET last_output_sequence = ?, output_bytes = ?, output_truncated = ?, updated_at = ? WHERE id = ?`, sequence, outputBytes, truncated, now, taskID); err != nil {
		return BackgroundTaskOutputAppendResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BackgroundTaskOutputAppendResult{}, err
	}
	return BackgroundTaskOutputAppendResult{LastSequence: sequence, OutputBytes: outputBytes, Truncated: truncated != 0}, nil
}

func (s *backgroundTaskStore) ListBackgroundTaskOutput(ctx context.Context, taskID string, afterSequence int64, byteLimit int) (BackgroundTaskOutputPage, error) {
	if afterSequence < 0 {
		return BackgroundTaskOutputPage{}, errors.New("background task output sequence must not be negative")
	}
	if byteLimit <= 0 {
		byteLimit = 64 * 1024
	}
	if byteLimit > 1024*1024 {
		byteLimit = 1024 * 1024
	}
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, sequence, stream, chunk_blob, byte_count, created_at FROM background_task_output WHERE task_id = ? AND sequence > ? ORDER BY sequence ASC LIMIT 1024`, strings.TrimSpace(taskID), afterSequence)
	if err != nil {
		return BackgroundTaskOutputPage{}, err
	}
	page := BackgroundTaskOutputPage{Items: make([]BackgroundTaskOutput, 0), NextSequence: afterSequence}
	for rows.Next() {
		var item BackgroundTaskOutput
		if err := rows.Scan(&item.TaskID, &item.Sequence, &item.Stream, &item.Chunk, &item.ByteCount, &item.CreatedAt); err != nil {
			_ = rows.Close()
			return BackgroundTaskOutputPage{}, err
		}
		if len(page.Items) > 0 && page.Bytes+item.ByteCount > byteLimit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, item)
		page.Bytes += item.ByteCount
		page.NextSequence = item.Sequence
		if item.Stream == "truncated" {
			page.Truncated = true
		}
		if page.Bytes >= byteLimit {
			page.HasMore = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return BackgroundTaskOutputPage{}, err
	}
	if err := rows.Close(); err != nil {
		return BackgroundTaskOutputPage{}, err
	}
	var lastSequence int64

	var truncated int
	if err := s.db.QueryRowContext(ctx, `SELECT last_output_sequence, output_truncated FROM background_tasks WHERE id = ?`, taskID).Scan(&lastSequence, &truncated); err != nil {
		return BackgroundTaskOutputPage{}, err
	}
	page.HasMore = page.NextSequence < lastSequence
	page.Truncated = page.Truncated || truncated != 0
	return page, nil
}

func (s *backgroundTaskStore) ReconcileBackgroundTasksAfterRestart(ctx context.Context, workerInstanceID string) (int64, error) {
	workerInstanceID = strings.TrimSpace(workerInstanceID)
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE background_tasks SET status = 'interrupted', revision = revision + 1, worker_instance_id = NULL, completed_at = ?, error_code = 'process_restarted', error_message = 'background task interrupted by process restart', updated_at = ? WHERE status IN ('running','cancel_requested') AND COALESCE(worker_instance_id,'') <> ?`, now, now, workerInstanceID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *backgroundTaskStore) BackgroundTaskStats(ctx context.Context) (BackgroundTaskStats, error) {
	stats := BackgroundTaskStats{ByStatus: make(map[string]int64), RunningByAgent: make(map[string]int64)}
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*), COALESCE(SUM(output_bytes),0), COALESCE(SUM(output_truncated),0) FROM background_tasks GROUP BY status`)
	if err != nil {
		return BackgroundTaskStats{}, err
	}
	for rows.Next() {
		var status string
		var count, outputBytes, truncated int64
		if err := rows.Scan(&status, &count, &outputBytes, &truncated); err != nil {
			rows.Close()
			return BackgroundTaskStats{}, err
		}
		stats.ByStatus[status] = count
		stats.Total += count
		stats.OutputBytes += outputBytes
		stats.TruncatedTasks += truncated
	}
	if err := rows.Close(); err != nil {
		return BackgroundTaskStats{}, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT owner_agent_id, COUNT(*) FROM background_tasks WHERE status IN ('running','cancel_requested') GROUP BY owner_agent_id`)
	if err != nil {
		return BackgroundTaskStats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var agentID string
		var count int64
		if err := rows.Scan(&agentID, &count); err != nil {
			return BackgroundTaskStats{}, err
		}
		stats.RunningByAgent[agentID] = count
	}
	return stats, rows.Err()
}

func (s *backgroundTaskStore) RecordRunSegmentUsage(ctx context.Context, runID string, expectedContinuationCount, expectedTurnCount, turnDelta, inputDelta, outputDelta int64) (Run, error) {
	if expectedContinuationCount < 0 || expectedTurnCount < 0 || turnDelta < 0 || inputDelta < 0 || outputDelta < 0 {
		return Run{}, errors.New("run segment usage counters must not be negative")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET turn_count = turn_count + ?, consumed_input_tokens = consumed_input_tokens + ?, consumed_output_tokens = consumed_output_tokens + ?, updated_at = ? WHERE id = ? AND status = 'running' AND continuation_count = ? AND turn_count = ?`, turnDelta, inputDelta, outputDelta, Now(), strings.TrimSpace(runID), expectedContinuationCount, expectedTurnCount)
	if err != nil {
		return Run{}, err
	}
	if err := s.requireRunContinuationCAS(ctx, result, runID, "record segment usage"); err != nil {
		return Run{}, err
	}
	return s.GetRunByID(ctx, runID)
}

type RunContinuationPendingInput struct {
	ExpectedContinuationCount int64
	TurnCount                 int64
	ConsumedInputTokens       int64
	ConsumedOutputTokens      int64
	ResumeAfterMessageID      string
	LastStopReason            string
	ContinuationReason        string
	WaitingBackgroundTaskID   string
}

func (s *backgroundTaskStore) MarkRunContinuationPending(ctx context.Context, runID string, input RunContinuationPendingInput) (Run, error) {
	if input.ExpectedContinuationCount < 0 || input.TurnCount < 0 || input.ConsumedInputTokens < 0 || input.ConsumedOutputTokens < 0 {
		return Run{}, errors.New("run continuation counters must not be negative")
	}
	input.ResumeAfterMessageID = strings.TrimSpace(input.ResumeAfterMessageID)
	input.LastStopReason = strings.TrimSpace(input.LastStopReason)
	input.ContinuationReason = boundedText(strings.TrimSpace(input.ContinuationReason), 4096)
	input.WaitingBackgroundTaskID = strings.TrimSpace(input.WaitingBackgroundTaskID)
	if err := validateBackgroundTaskText("run last stop reason", input.LastStopReason, 256, false); err != nil {
		return Run{}, err
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'continuation_pending', continuation_count = continuation_count + 1, turn_count = MAX(turn_count, ?), consumed_input_tokens = MAX(consumed_input_tokens, ?), consumed_output_tokens = MAX(consumed_output_tokens, ?), resume_after_message_id = NULLIF(?,''), last_stop_reason = NULLIF(?,''), continuation_reason = NULLIF(?,''), waiting_background_task_id = NULLIF(?,''), completed_at = NULL, error_message = NULL, updated_at = ? WHERE id = ? AND status = 'running' AND continuation_count = ?`, input.TurnCount, input.ConsumedInputTokens, input.ConsumedOutputTokens, input.ResumeAfterMessageID, input.LastStopReason, input.ContinuationReason, input.WaitingBackgroundTaskID, now, strings.TrimSpace(runID), input.ExpectedContinuationCount)
	if err != nil {
		return Run{}, err
	}
	if err := s.requireRunContinuationCAS(ctx, result, runID, "mark continuation pending"); err != nil {
		return Run{}, err
	}
	return s.GetRunByID(ctx, runID)
}

func (s *backgroundTaskStore) ResumeContinuationRun(ctx context.Context, runID string, expectedContinuationCount int64) (Run, error) {
	if expectedContinuationCount < 0 {
		return Run{}, errors.New("run continuation count must not be negative")
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'running', waiting_background_task_id = NULL, completed_at = NULL, error_message = NULL, updated_at = ? WHERE id = ? AND status = 'continuation_pending' AND continuation_count = ?`, now, strings.TrimSpace(runID), expectedContinuationCount)
	if err != nil {
		return Run{}, err
	}
	if err := s.requireRunContinuationCAS(ctx, result, runID, "resume continuation"); err != nil {
		return Run{}, err
	}
	return s.GetRunByID(ctx, runID)
}

func (s *backgroundTaskStore) ListContinuationPendingRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("continuation pending run limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, runSelectSQL+` WHERE status = 'continuation_pending' ORDER BY updated_at ASC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *backgroundTaskStore) CancelContinuationRun(ctx context.Context, runID string, expectedContinuationCount int64, reason string) (Run, error) {
	if expectedContinuationCount < 0 {
		return Run{}, errors.New("run continuation count must not be negative")
	}
	reason = boundedText(strings.TrimSpace(reason), 4096)
	if reason == "" {
		reason = "continuation canceled"
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'interrupted', completed_at = ?, duration_ms = MAX(0, CAST(ROUND((julianday(?) - julianday(started_at)) * 86400000.0) AS INTEGER)), error_message = ?, waiting_background_task_id = NULL, updated_at = ? WHERE id = ? AND status = 'continuation_pending' AND continuation_count = ?`, now, now, reason, now, strings.TrimSpace(runID), expectedContinuationCount)
	if err != nil {
		return Run{}, err
	}
	if err := s.requireRunContinuationCAS(ctx, result, runID, "cancel continuation"); err != nil {
		return Run{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE plans SET status = ?, updated_at = ? WHERE id = (SELECT plan_id FROM runs WHERE id = ?) AND status = ?`, PlanStatusApproved, now, strings.TrimSpace(runID), PlanStatusExecuting); err != nil {
		return Run{}, err
	}
	return s.GetRunByID(ctx, runID)
}

func (s *backgroundTaskStore) requireRunContinuationCAS(ctx context.Context, result sql.Result, runID, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id = ?`, strings.TrimSpace(runID)).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	return fmt.Errorf("%w: run cannot %s", ErrConflict, action)
}

func canonicalBackgroundTask(task BackgroundTask) (BackgroundTask, error) {
	task.ID = strings.TrimSpace(task.ID)
	task.OwnerAgentID = strings.TrimSpace(task.OwnerAgentID)
	task.ParentRunID = strings.TrimSpace(task.ParentRunID)
	task.ParentToolUseID = strings.TrimSpace(task.ParentToolUseID)
	task.Kind = strings.TrimSpace(task.Kind)
	task.Status = strings.TrimSpace(task.Status)
	task.PermissionModeCap = strings.TrimSpace(task.PermissionModeCap)
	task.ToolCatalogDigest = strings.TrimSpace(task.ToolCatalogDigest)
	task.WorkspaceFingerprint = strings.TrimSpace(task.WorkspaceFingerprint)
	task.ChildAgentID = strings.TrimSpace(task.ChildAgentID)
	task.ChildRunID = strings.TrimSpace(task.ChildRunID)
	task.WorkerInstanceID = strings.TrimSpace(task.WorkerInstanceID)
	task.ErrorCode = strings.TrimSpace(task.ErrorCode)
	task.ErrorMessage = boundedText(strings.TrimSpace(task.ErrorMessage), 4096)
	if task.ID == "" {
		task.ID = NewID()
	}
	if task.Status == "" {
		task.Status = BackgroundTaskStatusQueued
	}
	if task.Revision == 0 {
		task.Revision = 1
	}
	if task.MaxAttempts == 0 {
		task.MaxAttempts = 1
	}
	now := Now()
	if task.CreatedAt == "" {
		task.CreatedAt = now
	}
	if task.UpdatedAt == "" {
		task.UpdatedAt = task.CreatedAt
	}
	if task.ResultJSON == nil {
		task.ResultJSON = json.RawMessage(`{}`)
	}
	var err error
	if task.PayloadJSON, err = normalizeBackgroundTaskJSON(task.PayloadJSON, `{}`, 262144, false); err != nil {
		return BackgroundTask{}, fmt.Errorf("background task payload: %w", err)
	}
	if task.PublicSummaryJSON, err = normalizeBackgroundTaskJSON(task.PublicSummaryJSON, `{}`, 32768, false); err != nil {
		return BackgroundTask{}, fmt.Errorf("background task public summary: %w", err)
	}
	if task.ResultJSON, err = normalizeBackgroundTaskJSON(task.ResultJSON, `{}`, 32768, true); err != nil {
		return BackgroundTask{}, fmt.Errorf("background task result: %w", err)
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
	}{
		{"id", task.ID, 128, true}, {"owner agent id", task.OwnerAgentID, 128, true}, {"parent run id", task.ParentRunID, 128, false},
		{"parent tool use id", task.ParentToolUseID, 256, false}, {"worker instance id", task.WorkerInstanceID, 256, false},
		{"tool catalog digest", task.ToolCatalogDigest, 512, false}, {"workspace fingerprint", task.WorkspaceFingerprint, 512, false},
		{"child agent id", task.ChildAgentID, 128, false}, {"child run id", task.ChildRunID, 128, false}, {"error code", task.ErrorCode, 128, false},
	} {
		if err := validateBackgroundTaskText(field.name, field.value, field.max, field.required); err != nil {
			return BackgroundTask{}, err
		}
	}
	if task.Kind != BackgroundTaskKindShell && task.Kind != BackgroundTaskKindAgent {
		return BackgroundTask{}, fmt.Errorf("invalid background task kind %q", task.Kind)
	}
	if !validBackgroundTaskStatus(task.Status) {
		return BackgroundTask{}, fmt.Errorf("invalid background task status %q", task.Status)
	}
	if task.Status != BackgroundTaskStatusQueued && task.Status != BackgroundTaskStatusWaitingApproval {
		return BackgroundTask{}, errors.New("new background task must be queued or waiting approval")
	}
	// A task inherits its parent Run's ceiling. Once a subagent may hold bypass,
	// a subagent that dispatches another one carries bypass in here, so the agent
	// kind has to admit it. Shell tasks keep the narrower pair: nothing in that
	// path establishes a parent's bypass the way childPermissionCap does.
	if !validBackgroundTaskPermissionModeCap(task.PermissionModeCap, task.Kind) {
		return BackgroundTask{}, errors.New("invalid background task permission mode cap")
	}
	if task.Revision < 1 || task.AttemptCount < 0 || task.MaxAttempts < 1 || task.MaxAttempts > 100 || task.PermissionGenerationSnapshot < 0 || task.PolicyGenerationSnapshot < 0 || task.AgentGenerationSnapshot < 0 || task.LastOutputSequence < 0 || task.OutputBytes < 0 {
		return BackgroundTask{}, errors.New("invalid background task counters")
	}
	for name, value := range map[string]*string{"created_at": &task.CreatedAt, "updated_at": &task.UpdatedAt} {
		if *value, err = canonicalP2P3Time("background task "+name, *value, true); err != nil {
			return BackgroundTask{}, err
		}
	}
	return task, nil
}

func backgroundTaskSelectSQL(payloadExpr string) string {
	return `SELECT id, owner_agent_id, COALESCE(parent_run_id,''), COALESCE(parent_tool_use_id,''), kind, status, revision, priority, ` + payloadExpr + `, public_summary_json, COALESCE(permission_mode_cap,''), permission_generation_snapshot, policy_generation_snapshot, agent_generation_snapshot, COALESCE(tool_catalog_digest,''), COALESCE(workspace_fingerprint,''), COALESCE(child_agent_id,''), COALESCE(child_run_id,''), resume_parent, attempt_count, max_attempts, COALESCE(worker_instance_id,''), COALESCE(cancel_requested_at,''), COALESCE(started_at,''), COALESCE(completed_at,''), result_json, COALESCE(error_code,''), COALESCE(error_message,''), exit_code, last_output_sequence, output_bytes, output_truncated, created_at, updated_at FROM background_tasks`
}

type backgroundTaskScanner func(dest ...any) error

func scanBackgroundTask(scan backgroundTaskScanner) (BackgroundTask, error) {
	var task BackgroundTask
	var payload, summary, result string
	var resumeParent, outputTruncated int
	var exitCode sql.NullInt64
	if err := scan(&task.ID, &task.OwnerAgentID, &task.ParentRunID, &task.ParentToolUseID, &task.Kind, &task.Status, &task.Revision, &task.Priority, &payload, &summary, &task.PermissionModeCap, &task.PermissionGenerationSnapshot, &task.PolicyGenerationSnapshot, &task.AgentGenerationSnapshot, &task.ToolCatalogDigest, &task.WorkspaceFingerprint, &task.ChildAgentID, &task.ChildRunID, &resumeParent, &task.AttemptCount, &task.MaxAttempts, &task.WorkerInstanceID, &task.CancelRequestedAt, &task.StartedAt, &task.CompletedAt, &result, &task.ErrorCode, &task.ErrorMessage, &exitCode, &task.LastOutputSequence, &task.OutputBytes, &outputTruncated, &task.CreatedAt, &task.UpdatedAt); err != nil {
		return BackgroundTask{}, err
	}
	if !json.Valid([]byte(payload)) || !json.Valid([]byte(summary)) || !json.Valid([]byte(result)) {
		return BackgroundTask{}, fmt.Errorf("stored background task %s contains invalid JSON", task.ID)
	}
	task.PayloadJSON = json.RawMessage(payload)
	task.PublicSummaryJSON = json.RawMessage(summary)
	task.ResultJSON = json.RawMessage(result)
	task.ResumeParent = resumeParent != 0
	task.OutputTruncated = outputTruncated != 0
	if exitCode.Valid {
		value := int(exitCode.Int64)
		task.ExitCode = &value
	}
	return task, nil
}

func validBackgroundTaskStatus(status string) bool {
	switch status {
	case BackgroundTaskStatusQueued, BackgroundTaskStatusWaitingApproval, BackgroundTaskStatusRunning, BackgroundTaskStatusCancelRequested, BackgroundTaskStatusSucceeded, BackgroundTaskStatusFailed, BackgroundTaskStatusCanceled, BackgroundTaskStatusInterrupted:
		return true
	default:
		return false
	}
}

// validBackgroundTaskPermissionModeCap mirrors validRunPermissionModeCap for the
// task queue. An agent task inherits its cap from the dispatching run, so a
// subagent that itself holds bypass can carry that ceiling into a nested task.
// Shell tasks keep the narrower set: nothing about running a command needs a
// bypass ceiling, and leaving it unavailable there keeps the widening surface
// as small as the feature requires.
func validBackgroundTaskPermissionModeCap(cap, kind string) bool {
	switch cap {
	case "":
		return true
	case "readOnly", "acceptEdits":
		return true
	case "bypassPermissions":
		return kind == BackgroundTaskKindAgent
	default:
		return false
	}
}

func backgroundTaskTerminal(status string) bool {
	switch status {
	case BackgroundTaskStatusSucceeded, BackgroundTaskStatusFailed, BackgroundTaskStatusCanceled, BackgroundTaskStatusInterrupted:
		return true
	default:
		return false
	}
}

func validBackgroundTaskTransition(from, to string) bool {
	if from == to {
		return false
	}
	switch from {
	case BackgroundTaskStatusQueued:
		return to == BackgroundTaskStatusWaitingApproval || to == BackgroundTaskStatusRunning || to == BackgroundTaskStatusCanceled
	case BackgroundTaskStatusWaitingApproval:
		return to == BackgroundTaskStatusQueued || to == BackgroundTaskStatusCanceled
	case BackgroundTaskStatusRunning:
		return to == BackgroundTaskStatusCancelRequested || backgroundTaskTerminal(to)
	case BackgroundTaskStatusCancelRequested:
		return to == BackgroundTaskStatusCanceled || to == BackgroundTaskStatusFailed || to == BackgroundTaskStatusInterrupted
	default:
		return false
	}
}

func validBackgroundTaskOutputStream(stream string) bool {
	switch stream {
	case "stdout", "stderr", "system", "truncated":
		return true
	default:
		return false
	}
}

func normalizeBackgroundTaskJSON(raw json.RawMessage, fallback string, maxBytes int, allowArray bool) (json.RawMessage, error) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 {
		value = []byte(fallback)
	}
	if len(value) > maxBytes || !json.Valid(value) {
		return nil, fmt.Errorf("must be valid JSON no larger than %d bytes", maxBytes)
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errors.New("must be valid JSON")
	}
	switch decoded.(type) {
	case map[string]any:
	case []any:
		if !allowArray {
			return nil, errors.New("must be a JSON object")
		}
	default:
		return nil, errors.New("must be a JSON object or array")
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxBytes {
		return nil, fmt.Errorf("must be no larger than %d bytes", maxBytes)
	}
	return json.RawMessage(encoded), nil
}

func validateBackgroundTaskText(name, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("background task %s is required", name)
	}
	if value == "" {
		return nil
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid background task %s", name)
	}
	return nil
}

func boundedText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func parseBackgroundTaskTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
