package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) CreateRun(ctx context.Context, run Run) (Run, error) {
	run.ID = strings.TrimSpace(run.ID)
	run.AgentID = strings.TrimSpace(run.AgentID)
	run.TriggerMessageID = strings.TrimSpace(run.TriggerMessageID)
	run.DispatchID = strings.TrimSpace(run.DispatchID)
	run.Source = strings.TrimSpace(run.Source)
	run.SourceID = strings.TrimSpace(run.SourceID)
	run.PermissionModeCap = strings.TrimSpace(run.PermissionModeCap)
	run.TriggerType = strings.TrimSpace(run.TriggerType)
	run.ExecutionDeviceID = strings.TrimSpace(run.ExecutionDeviceID)
	run.ExecutionMode = strings.TrimSpace(run.ExecutionMode)
	run.PlanID = strings.TrimSpace(run.PlanID)
	run.ToolCatalogDigest = strings.TrimSpace(run.ToolCatalogDigest)
	run.WorkspaceFingerprint = strings.TrimSpace(run.WorkspaceFingerprint)
	run.AutoContinuationMode = strings.TrimSpace(run.AutoContinuationMode)
	run.DeadlineAt = strings.TrimSpace(run.DeadlineAt)
	run.ResumeAfterMessageID = strings.TrimSpace(run.ResumeAfterMessageID)
	run.LastStopReason = strings.TrimSpace(run.LastStopReason)
	run.ContinuationReason = strings.TrimSpace(run.ContinuationReason)
	run.WaitingBackgroundTaskID = strings.TrimSpace(run.WaitingBackgroundTaskID)
	if run.ID == "" {
		run.ID = NewID()
	}
	now := Now()
	if run.CreatedAt == "" {
		run.CreatedAt = now
	}
	if run.UpdatedAt == "" {
		run.UpdatedAt = run.CreatedAt
	}
	if run.Status == "" {
		run.Status = "running"
	}
	switch run.Status {
	case "pending":
		// Queued work has not started. Do not synthesize a start timestamp.
		run.StartedAt = ""
		run.CompletedAt = ""
	case "running", "continuation_pending":
		if run.StartedAt == "" {
			run.StartedAt = now
		}
		run.CompletedAt = ""
	case "completed", "error", "interrupted", "superseded", "skipped":
		if run.StartedAt == "" {
			run.StartedAt = run.CreatedAt
		}
		if run.CompletedAt == "" {
			run.CompletedAt = now
		}
	default:
		return Run{}, fmt.Errorf("invalid run status %q", run.Status)
	}
	if run.CheckpointState == "" {
		run.CheckpointState = RunCheckpointNone
	}
	if run.Source == "" {
		run.Source = RunSourceManual
	}
	if run.ExecutionMode == "" {
		run.ExecutionMode = RunExecutionModeExecute
	}
	if run.AutoContinuationMode == "" {
		run.AutoContinuationMode = "off"
	}
	if run.TriggerType == "" {
		switch run.Source {
		case "schedule", "scheduled":
			run.TriggerType = "scheduled"
		case "goal":
			run.TriggerType = "goal"
		case "internal":
			run.TriggerType = "internal"
		default:
			run.TriggerType = "manual"
		}
	}
	for _, field := range []struct {
		name     string
		value    string
		max      int
		required bool
		token    bool
	}{
		{"run id", run.ID, 128, true, false},
		{"run agent id", run.AgentID, 128, true, false},
		{"run trigger message id", run.TriggerMessageID, 128, false, false},
		{"run dispatch id", run.DispatchID, 256, false, false},
		{"run source", run.Source, 64, true, true},
		{"run source id", run.SourceID, 256, false, false},
		{"run plan id", run.PlanID, 128, false, false},
		{"run tool catalog digest", run.ToolCatalogDigest, 512, false, false},
		{"run workspace fingerprint", run.WorkspaceFingerprint, 512, false, false},
		{"run resume after message id", run.ResumeAfterMessageID, 128, false, false},
		{"run last stop reason", run.LastStopReason, 256, false, false},
		{"run continuation reason", run.ContinuationReason, 4096, false, false},
		{"run waiting background task id", run.WaitingBackgroundTaskID, 128, false, false},
	} {
		if err := validateP2P3Text(field.name, field.value, field.max, field.required, field.token); err != nil {
			return Run{}, err
		}
	}
	if !validRunStatus(run.Status) {
		return Run{}, errors.New("invalid run status")
	}
	if run.PermissionModeCap != "" && run.PermissionModeCap != "readOnly" && run.PermissionModeCap != "acceptEdits" {
		return Run{}, errors.New("invalid run permission mode cap")
	}
	if run.TriggerType != "manual" && run.TriggerType != "scheduled" && run.TriggerType != "goal" && run.TriggerType != "internal" {
		return Run{}, errors.New("invalid run trigger type")
	}
	if run.ExecutionMode != RunExecutionModePlan && run.ExecutionMode != RunExecutionModeExecute {
		return Run{}, errors.New("invalid run execution mode")
	}
	if run.AutoContinuationMode != "off" && run.AutoContinuationMode != "safe" {
		return Run{}, errors.New("invalid run auto continuation mode")
	}
	// Budgets are stored as 0 for "no ceiling" (see durableBudget in
	// internal/agent), so this stays a plain non-negative check.
	if run.ContinuationCount < 0 || run.ContinuationSegmentTurns < 0 || run.TurnCount < 0 || run.MaxTotalTurns < 0 || run.MaxContinuations < 0 || run.MaxTotalTokens < 0 || run.ConsumedInputTokens < 0 || run.ConsumedOutputTokens < 0 {
		return Run{}, errors.New("run continuation counters must not be negative")
	}
	if run.ExecutionMode == RunExecutionModePlan && run.PlanID != "" {
		return Run{}, errors.New("plan mode run cannot execute a plan")
	}
	if run.PolicyGenerationSnapshot < 0 || run.AgentGenerationSnapshot < 0 {
		return Run{}, errors.New("run generation snapshots must not be negative")
	}
	if run.DurationMS < 0 {
		return Run{}, errors.New("invalid run duration")
	}
	var err error
	for name, value := range map[string]*string{
		"run created_at": &run.CreatedAt,
		"run updated_at": &run.UpdatedAt,
	} {
		if *value, err = canonicalP2P3Time(name, *value, true); err != nil {
			return Run{}, err
		}
	}
	for name, value := range map[string]*string{
		"run started_at":      &run.StartedAt,
		"run completed_at":    &run.CompletedAt,
		"run git_snapshot_at": &run.GitSnapshotAt,
		"run rolled_back_at":  &run.RolledBackAt,
		"run deadline_at":     &run.DeadlineAt,
	} {
		if *value, err = canonicalP2P3Time(name, *value, false); err != nil {
			return Run{}, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agents SET execution_generation = COALESCE(execution_generation,0) + 1 WHERE id = ?`, run.AgentID)
	if err != nil {
		return Run{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Run{}, err
	} else if affected != 1 {
		return Run{}, sql.ErrNoRows
	}
	var agentDeviceID string
	var currentAgentGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT execution_generation, COALESCE(entity_generation,1), COALESCE(execution_device_id,'local') FROM agents WHERE id = ?`, run.AgentID).Scan(&run.ExecutionGeneration, &currentAgentGeneration, &agentDeviceID); err != nil {
		return Run{}, err
	}
	currentPolicyGeneration := int64(1)
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(policy_generation,1) FROM workflow_preferences WHERE id = 'default'`).Scan(&currentPolicyGeneration); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Run{}, err
	}
	if run.PlanID != "" {
		plan, err := scanPlan(func(dest ...any) error {
			return tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE id = ?`, run.PlanID).Scan(dest...)
		})
		if err != nil {
			return Run{}, err
		}
		if plan.AgentID != run.AgentID || plan.Status != PlanStatusApproved {
			return Run{}, fmt.Errorf("%w: plan is not approved for this agent", ErrConflict)
		}
		if plan.PolicyGenerationSnapshot != currentPolicyGeneration || plan.AgentGenerationSnapshot != currentAgentGeneration {
			return Run{}, fmt.Errorf("%w: approved plan generations are stale", ErrConflict)
		}
		if run.PolicyGenerationSnapshot != 0 && run.PolicyGenerationSnapshot != plan.PolicyGenerationSnapshot || run.AgentGenerationSnapshot != 0 && run.AgentGenerationSnapshot != plan.AgentGenerationSnapshot || run.ToolCatalogDigest != "" && run.ToolCatalogDigest != plan.ToolCatalogDigest || run.WorkspaceFingerprint != "" && run.WorkspaceFingerprint != plan.WorkspaceFingerprint {
			return Run{}, fmt.Errorf("%w: execution run snapshots do not match plan", ErrConflict)
		}
		run.PolicyGenerationSnapshot = plan.PolicyGenerationSnapshot
		run.AgentGenerationSnapshot = plan.AgentGenerationSnapshot
		run.ToolCatalogDigest = plan.ToolCatalogDigest
		run.WorkspaceFingerprint = plan.WorkspaceFingerprint
	}
	if run.AgentGenerationSnapshot == 0 {
		run.AgentGenerationSnapshot = currentAgentGeneration
	}
	if run.PolicyGenerationSnapshot == 0 {
		run.PolicyGenerationSnapshot = currentPolicyGeneration
	}
	if run.ExecutionDeviceID == "" {
		run.ExecutionDeviceID = agentDeviceID
	}
	if err := validateP2P3Text("run execution device id", run.ExecutionDeviceID, 128, true, false); err != nil {
		return Run{}, err
	}
	var deviceExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM execution_devices WHERE id = ?`, run.ExecutionDeviceID).Scan(&deviceExists); err != nil {
		return Run{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs (
		id, agent_id, trigger_message_id, status, started_at, completed_at, error_message, base_head, end_head,
		checkpoint_repo_root, git_snapshot_at, checkpoint_state, checkpoint_error, rolled_back_at, source, source_id,
		permission_mode_cap, execution_generation, dispatch_id, duration_ms, trigger_type, execution_device_id,
		execution_mode, plan_id, policy_generation_snapshot, agent_generation_snapshot, tool_catalog_digest,
		workspace_fingerprint, auto_continuation_mode, continuation_count, continuation_segment_turns, turn_count, max_total_turns,
		max_continuations, max_total_tokens, consumed_input_tokens, consumed_output_tokens, deadline_at,
		resume_after_message_id, last_stop_reason, continuation_reason, waiting_background_task_id, created_at, updated_at
	) VALUES (
		?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
		NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, 0), ?, ?,
		?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
		NULLIF(?, ''), NULLIF(?, ''), ?, ?
	)`, run.ID, run.AgentID, run.TriggerMessageID, run.Status, run.StartedAt, run.CompletedAt, run.ErrorMessage, run.BaseHead, run.EndHead, run.CheckpointRepoRoot, run.GitSnapshotAt, run.CheckpointState, run.CheckpointError, run.RolledBackAt, run.Source, run.SourceID, run.PermissionModeCap, run.ExecutionGeneration, run.DispatchID, run.DurationMS, run.TriggerType, run.ExecutionDeviceID, run.ExecutionMode, run.PlanID, run.PolicyGenerationSnapshot, run.AgentGenerationSnapshot, run.ToolCatalogDigest, run.WorkspaceFingerprint, run.AutoContinuationMode, run.ContinuationCount, run.ContinuationSegmentTurns, run.TurnCount, run.MaxTotalTurns, run.MaxContinuations, run.MaxTotalTokens, run.ConsumedInputTokens, run.ConsumedOutputTokens, run.DeadlineAt, run.ResumeAfterMessageID, run.LastStopReason, run.ContinuationReason, run.WaitingBackgroundTaskID, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return Run{}, fmt.Errorf("%w: run dispatch or execution generation already exists", ErrConflict)
		}
		return Run{}, err
	}
	if run.PlanID != "" {
		result, err := tx.ExecContext(ctx, `UPDATE plans SET status = ?, stale_reason = NULL, updated_at = ? WHERE id = ? AND agent_id = ? AND status = ?`, PlanStatusExecuting, Now(), run.PlanID, run.AgentID, PlanStatusApproved)
		if err != nil {
			return Run{}, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return Run{}, err
		} else if affected != 1 {
			return Run{}, fmt.Errorf("%w: plan status changed", ErrConflict)
		}
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func validRunStatus(status string) bool {
	switch status {
	case "pending", "running", "continuation_pending", "completed", "interrupted", "error", "superseded", "skipped":
		return true
	default:
		return false
	}
}

func (s *Store) UpdateRunStatus(ctx context.Context, runID, status, errorMessage string) error {
	if status != "running" {
		return fmt.Errorf("invalid non-terminal run status transition to %q", status)
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'running', started_at = COALESCE(NULLIF(started_at, ''), ?), completed_at = NULL, error_message = NULL, updated_at = ? WHERE id = ? AND status = 'pending'`, now, now, runID)
	if err != nil {
		return err
	}
	return s.requireRunTransition(ctx, result, runID, "start")
}

func (s *Store) requireRunTransition(ctx context.Context, result sql.Result, runID, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id = ?`, runID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	return fmt.Errorf("%w: run cannot %s: %s", ErrConflict, action, runID)
}

func checkpointTransitionError(runID, action string) error {
	return fmt.Errorf("run checkpoint cannot %s: %s", action, runID)
}

func requireCheckpointTransition(result sql.Result, runID, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return checkpointTransitionError(runID, action)
	}
	return nil
}

func (s *Store) BeginRunGitCheckpoint(ctx context.Context, runID, baseHead, repoRoot string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_git_changes WHERE run_id = ?`, runID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET base_head = NULLIF(?, ''), end_head = NULL, checkpoint_repo_root = NULLIF(?, ''), git_snapshot_at = NULL, checkpoint_state = ?, checkpoint_error = NULL, rolled_back_at = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, strings.TrimSpace(baseHead), strings.TrimSpace(repoRoot), RunCheckpointTracking, Now(), runID, RunCheckpointNone)
	if err != nil {
		return err
	}
	if err := requireCheckpointTransition(result, runID, "begin tracking"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkRunGitCheckpointCapturing(ctx context.Context, runID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET checkpoint_state = ?, checkpoint_error = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, RunCheckpointCapturing, Now(), runID, RunCheckpointTracking)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected != 1 {
		return errors.New("run checkpoint is not tracking")
	}
	return nil
}

func (s *Store) ReplaceRunGitCheckpointChanges(ctx context.Context, runID string, changes []RunGitChange) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_git_changes WHERE run_id = ?`, runID); err != nil {
		return err
	}
	for _, change := range changes {
		change.RunID = runID
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_git_changes (run_id, path, orig_path, index_status, worktree_status, untracked, index_fingerprint, worktree_fingerprint) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?)`, change.RunID, change.Path, change.OrigPath, change.IndexStatus, change.WorktreeStatus, boolInt(change.Untracked), change.IndexFingerprint, change.WorktreeFingerprint); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET checkpoint_state = ?, checkpoint_error = NULL, end_head = NULL, git_snapshot_at = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, RunCheckpointTracking, Now(), runID, RunCheckpointCapturing)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected != 1 {
		return errors.New("run checkpoint is not capturing")
	}
	return tx.Commit()
}

func (s *Store) InvalidateRunGitCheckpoint(ctx context.Context, runID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "run checkpoint capture failed"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT checkpoint_state FROM runs WHERE id = ?`, runID).Scan(&state); err != nil {
		return err
	}
	if state == RunCheckpointRolledBack {
		return checkpointTransitionError(runID, "invalidate a rolled back checkpoint")
	}
	if state != RunCheckpointTracking && state != RunCheckpointCapturing {
		return checkpointTransitionError(runID, "invalidate from the current state")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_git_changes WHERE run_id = ?`, runID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET end_head = NULL, git_snapshot_at = NULL, checkpoint_state = ?, checkpoint_error = ?, rolled_back_at = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, RunCheckpointInvalid, reason, Now(), runID, state)
	if err != nil {
		return err
	}
	if err := requireCheckpointTransition(result, runID, "invalidate"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinalizeRunGitCheckpoint(ctx context.Context, runID, endHead string) (bool, error) {
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET end_head = NULLIF(?, ''), git_snapshot_at = ?, checkpoint_state = ?, checkpoint_error = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, strings.TrimSpace(endHead), now, RunCheckpointReady, now, runID, RunCheckpointTracking)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *Store) ClaimRunGitRollback(ctx context.Context, runID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET checkpoint_state = ?, checkpoint_error = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, RunCheckpointRollingBack, Now(), runID, RunCheckpointReady)
	if err != nil {
		return err
	}
	return requireCheckpointTransition(result, runID, "start rollback")
}

func (s *Store) MarkRunGitCheckpointRolledBack(ctx context.Context, runID string) error {
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET checkpoint_state = ?, rolled_back_at = ?, checkpoint_error = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, RunCheckpointRolledBack, now, now, runID, RunCheckpointRollingBack)
	if err != nil {
		return err
	}
	return requireCheckpointTransition(result, runID, "finish rollback")
}

func (s *Store) FailRunGitRollback(ctx context.Context, runID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "rollback failed"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET checkpoint_state = ?, checkpoint_error = ?, rolled_back_at = NULL, updated_at = ? WHERE id = ? AND checkpoint_state = ?`, RunCheckpointInvalid, reason, Now(), runID, RunCheckpointRollingBack)
	if err != nil {
		return err
	}
	return requireCheckpointTransition(result, runID, "mark rollback failure")
}

func (s *Store) ListRunGitChanges(ctx context.Context, runID string) ([]RunGitChange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, path, COALESCE(orig_path,''), index_status, worktree_status, untracked, COALESCE(index_fingerprint,''), worktree_fingerprint FROM run_git_changes WHERE run_id = ? ORDER BY path ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := make([]RunGitChange, 0)
	for rows.Next() {
		var change RunGitChange
		var untracked int
		if err := rows.Scan(&change.RunID, &change.Path, &change.OrigPath, &change.IndexStatus, &change.WorktreeStatus, &untracked, &change.IndexFingerprint, &change.WorktreeFingerprint); err != nil {
			return nil, err
		}
		change.Untracked = untracked != 0
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func (s *Store) CompleteRun(ctx context.Context, runID, status, errorMessage string) error {
	// Direct runner tests and legacy callers may run without durable tracking.
	// There is no row to transition in that mode, so preserve the prior no-op.
	if strings.TrimSpace(runID) == "" {
		return nil
	}
	var allowed string
	switch status {
	case "interrupted", "error":
		allowed = "('pending', 'running', 'continuation_pending')"
	case "completed":
		allowed = "('running')"
	case "superseded":
		// Pending is included for latest-wins queue replacement; without it a
		// third queued submission would leave the replaced pending run stranded.
		allowed = "('pending', 'running')"
	default:
		return fmt.Errorf("invalid terminal run status %q", status)
	}
	now := Now()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status = ?, completed_at = ?, duration_ms = MAX(0, CAST(ROUND((julianday(?) - julianday(started_at)) * 86400000.0) AS INTEGER)), error_message = NULLIF(?, ''), updated_at = ? WHERE id = ? AND status IN `+allowed, status, now, now, errorMessage, now, runID)
	if err != nil {
		return err
	}
	if err := s.requireRunTransition(ctx, result, runID, status); err != nil {
		return err
	}
	planStatus := PlanStatusApproved
	if status == "completed" {
		planStatus = PlanStatusExecuted
	}
	_, err = s.db.ExecContext(ctx, `UPDATE plans SET status = ?, updated_at = ? WHERE id = (SELECT plan_id FROM runs WHERE id = ?) AND status = ?`, planStatus, now, runID, PlanStatusExecuting)
	return err
}

func (s *Store) RecoverInterruptedRun(ctx context.Context, runID string) error {
	const restartReason = "process restarted"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var agentID, currentStatus, currentError string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id, status, COALESCE(error_message,'') FROM runs WHERE id = ?`, runID).Scan(&agentID, &currentStatus, &currentError); err != nil {
		return err
	}
	now := Now()
	if currentStatus != "interrupted" || currentError != restartReason {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'interrupted', completed_at = ?, duration_ms = MAX(0, CAST(ROUND((julianday(?) - julianday(started_at)) * 86400000.0) AS INTEGER)), error_message = ?, updated_at = ? WHERE id = ? AND status IN ('pending', 'running')`, now, now, restartReason, now, runID)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return err
		} else if affected != 1 {
			return fmt.Errorf("run is not recoverable after process restart: %s", runID)
		}
	}
	// Match CompleteRun's non-completed outcome. The status predicate preserves
	// concurrent stale/cancelled transitions and makes retrying recovery safe.
	if _, err := tx.ExecContext(ctx, `UPDATE plans SET status = ?, updated_at = ? WHERE id = (SELECT plan_id FROM runs WHERE id = ?) AND status = ?`, PlanStatusApproved, now, runID, PlanStatusExecuting); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = 'interrupted', error_message = ?, updated_at = ? WHERE id = ?`, restartReason, now, agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tool_calls SET status = 'denied', completed_at = COALESCE(completed_at, ?), updated_at = ?, error_message = ?, permission_decided_by = 'system', permission_decided_at = ?, permission_deny_message = ?, permission_decision_reason = ?, permission_suggestions = NULL WHERE run_id = ? AND status IN ('pending_approval', 'approved', 'running')`, now, now, restartReason, now, restartReason, restartReason, runID); err != nil {
		return err
	}
	return tx.Commit()
}

const runSelectSQL = `SELECT id, agent_id, COALESCE(trigger_message_id,''), status, COALESCE(started_at,''), COALESCE(completed_at,''), COALESCE(error_message,''), COALESCE(base_head,''), COALESCE(end_head,''), COALESCE(checkpoint_repo_root,''), COALESCE(git_snapshot_at,''), COALESCE(checkpoint_state,'none'), COALESCE(checkpoint_error,''), COALESCE(rolled_back_at,''), COALESCE(source,'manual'), COALESCE(source_id,''), COALESCE(permission_mode_cap,''), COALESCE(execution_generation,0), COALESCE(dispatch_id,''), COALESCE(duration_ms,0), COALESCE(trigger_type,'manual'), COALESCE(execution_device_id,'local'), COALESCE(execution_mode,'execute'), COALESCE(plan_id,''), COALESCE(policy_generation_snapshot,0), COALESCE(agent_generation_snapshot,0), COALESCE(tool_catalog_digest,''), COALESCE(workspace_fingerprint,''), COALESCE(auto_continuation_mode,'off'), COALESCE(continuation_count,0), COALESCE(continuation_segment_turns,0), COALESCE(turn_count,0), COALESCE(max_total_turns,0), COALESCE(max_continuations,0), COALESCE(max_total_tokens,0), COALESCE(consumed_input_tokens,0), COALESCE(consumed_output_tokens,0), COALESCE(deadline_at,''), COALESCE(resume_after_message_id,''), COALESCE(last_stop_reason,''), COALESCE(continuation_reason,''), COALESCE(waiting_background_task_id,''), created_at, updated_at FROM runs`

type runScanner func(dest ...any) error

func scanRun(scan runScanner) (Run, error) {
	var run Run
	err := scan(&run.ID, &run.AgentID, &run.TriggerMessageID, &run.Status, &run.StartedAt, &run.CompletedAt, &run.ErrorMessage, &run.BaseHead, &run.EndHead, &run.CheckpointRepoRoot, &run.GitSnapshotAt, &run.CheckpointState, &run.CheckpointError, &run.RolledBackAt, &run.Source, &run.SourceID, &run.PermissionModeCap, &run.ExecutionGeneration, &run.DispatchID, &run.DurationMS, &run.TriggerType, &run.ExecutionDeviceID, &run.ExecutionMode, &run.PlanID, &run.PolicyGenerationSnapshot, &run.AgentGenerationSnapshot, &run.ToolCatalogDigest, &run.WorkspaceFingerprint, &run.AutoContinuationMode, &run.ContinuationCount, &run.ContinuationSegmentTurns, &run.TurnCount, &run.MaxTotalTurns, &run.MaxContinuations, &run.MaxTotalTokens, &run.ConsumedInputTokens, &run.ConsumedOutputTokens, &run.DeadlineAt, &run.ResumeAfterMessageID, &run.LastStopReason, &run.ContinuationReason, &run.WaitingBackgroundTaskID, &run.CreatedAt, &run.UpdatedAt)
	return run, err
}

func (s *Store) GetRun(ctx context.Context, agentID, runID string) (Run, error) {
	return scanRun(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, runSelectSQL+` WHERE agent_id = ? AND id = ?`, agentID, runID).Scan(dest...)
	})
}

func (s *Store) GetRunByID(ctx context.Context, runID string) (Run, error) {
	return scanRun(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, runSelectSQL+` WHERE id = ?`, runID).Scan(dest...)
	})
}

func (s *Store) ListRuns(ctx context.Context, agentID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, runSelectSQL+` WHERE agent_id = ? ORDER BY execution_generation DESC, id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) ListRecoverableRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, runSelectSQL+` WHERE status IN ('pending', 'running') ORDER BY execution_generation ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) ListRollingBackRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, runSelectSQL+` WHERE checkpoint_state = ? ORDER BY execution_generation ASC, id ASC`, RunCheckpointRollingBack)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// BindPendingCorrectionRun upgrades the legacy correction transaction with the
// same execution-generation and capability snapshots used by ordinary runs. It
// must complete before the correction loop is scheduled.
func (s *Store) BindPendingCorrectionRun(ctx context.Context, runID, source string) (Run, error) {
	runID = strings.TrimSpace(runID)
	source = strings.TrimSpace(source)
	if runID == "" {
		return Run{}, errors.New("run id is required")
	}
	if source == "" {
		source = RunSourceManual
	}
	if source != RunSourceManual && source != RunSourceConversation {
		return Run{}, errors.New("invalid correction run source")
	}
	permissionModeCap := ""
	if source == RunSourceConversation {
		permissionModeCap = "readOnly"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	var agentID, status string
	var existingGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT agent_id, status, COALESCE(execution_generation,0) FROM runs WHERE id = ?`, runID).Scan(&agentID, &status, &existingGeneration); err != nil {
		return Run{}, err
	}
	if status != "pending" || existingGeneration != 0 {
		return Run{}, fmt.Errorf("%w: correction run is already bound or no longer pending", ErrConflict)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agents SET execution_generation = COALESCE(execution_generation,0) + 1 WHERE id = ?`, agentID)
	if err != nil {
		return Run{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Run{}, err
	} else if affected != 1 {
		return Run{}, sql.ErrNoRows
	}
	var executionGeneration, agentGeneration int64
	var executionDeviceID string
	if err := tx.QueryRowContext(ctx, `SELECT execution_generation, COALESCE(entity_generation,1), COALESCE(execution_device_id,'local') FROM agents WHERE id = ?`, agentID).Scan(&executionGeneration, &agentGeneration, &executionDeviceID); err != nil {
		return Run{}, err
	}
	policyGeneration := int64(1)
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(policy_generation,1) FROM workflow_preferences WHERE id = 'default'`).Scan(&policyGeneration); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Run{}, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE runs SET source = ?, permission_mode_cap = ?, execution_generation = ?, trigger_type = 'manual', execution_device_id = ?, execution_mode = 'execute', policy_generation_snapshot = ?, agent_generation_snapshot = ?, auto_continuation_mode = 'off', tool_catalog_digest = '', workspace_fingerprint = '', updated_at = ? WHERE id = ? AND status = 'pending' AND COALESCE(execution_generation,0) = 0`, source, permissionModeCap, executionGeneration, executionDeviceID, policyGeneration, agentGeneration, Now(), runID)
	if err != nil {
		return Run{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Run{}, err
	} else if affected != 1 {
		return Run{}, fmt.Errorf("%w: correction run binding changed concurrently", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return s.GetRunByID(ctx, runID)
}
