package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExecutionDeviceHeartbeatTTL is how long one heartbeat keeps a remote device
// eligible for work.
//
// execution_devices has no last_seen_at column, and heartbeats are the only
// writer that sets the three live statuses below, so updated_at on a row in one
// of those statuses is the time of its last heartbeat. Enable and disable write
// 'unknown' and 'disabled', which are outside that set and therefore never
// mistaken for liveness.
const ExecutionDeviceHeartbeatTTL = 90 * time.Second

// ExecutionDeviceMaxClaimAgents bounds the agent allow list a single claim may
// carry, so a pairing with a large grant set cannot build an unbounded query.
const ExecutionDeviceMaxClaimAgents = 200

// NormalizeExecutionDeviceHeartbeatStatus resolves the status a device may
// report about itself. A device can say it is busy, idle, degraded, or going
// away; it cannot promote itself out of 'disabled' or claim 'error', because
// those are the host's verdicts about the device.
func NormalizeExecutionDeviceHeartbeatStatus(status string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "ready":
		return "ready", nil
	case "online":
		return "online", nil
	case "degraded":
		return "degraded", nil
	case "offline":
		return "offline", nil
	default:
		return "", errors.New("invalid execution device heartbeat status")
	}
}

// GetRemoteExecutionDeviceForFingerprint returns the remote device bound to
// identityFingerprint, or sql.ErrNoRows when no device belongs to that identity.
//
// The stored fingerprint never leaves the store: callers pass the peer identity
// they have already authenticated and compare inside the query, so no handler
// needs to read a device's fingerprint in order to check it.
func (s *executionStore) GetRemoteExecutionDeviceForFingerprint(ctx context.Context, id, identityFingerprint string) (ExecutionDevice, error) {
	id = strings.TrimSpace(id)
	identityFingerprint = strings.TrimSpace(identityFingerprint)
	if id == "" || id == "local" || len(identityFingerprint) < 16 || len(identityFingerprint) > 512 {
		return ExecutionDevice{}, sql.ErrNoRows
	}
	return scanExecutionDevice(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, kind, name, enabled, status, capabilities_json, created_at, updated_at FROM execution_devices WHERE id = ? AND kind = 'remote' AND identity_fingerprint = ?`, id, identityFingerprint).Scan(dest...)
	})
}

// RecordExecutionDeviceHeartbeat refreshes the liveness of a remote device that
// proved it owns identityFingerprint. A disabled device stays disabled: liveness
// is not authorization, so a device cannot heartbeat its way back into service
// after the owner switched it off.
func (s *executionStore) RecordExecutionDeviceHeartbeat(ctx context.Context, id, identityFingerprint, status string) (ExecutionDevice, error) {
	normalized, err := NormalizeExecutionDeviceHeartbeatStatus(status)
	if err != nil {
		return ExecutionDevice{}, err
	}
	device, err := s.GetRemoteExecutionDeviceForFingerprint(ctx, id, identityFingerprint)
	if err != nil {
		return ExecutionDevice{}, err
	}
	if !device.Enabled {
		return ExecutionDevice{}, fmt.Errorf("%w: remote execution device is disabled", ErrConflict)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE execution_devices SET status = ?, updated_at = ? WHERE id = ? AND kind = 'remote' AND enabled = 1 AND identity_fingerprint = ?`, normalized, Now(), device.ID, strings.TrimSpace(identityFingerprint))
	if err != nil {
		return ExecutionDevice{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ExecutionDevice{}, err
	} else if affected != 1 {
		return ExecutionDevice{}, fmt.Errorf("%w: remote execution device changed", ErrConflict)
	}
	return s.GetExecutionDevice(ctx, device.ID)
}

// MarkStaleExecutionDevicesOffline drops devices that stopped heartbeating back
// to 'offline'. Without this sweep a device that lost power stays 'ready'
// forever and keeps accepting agent bindings and task creation, so callers that
// are about to trust device liveness run it first.
//
// updated_at is left untouched: rewriting it here would restart the heartbeat
// clock for a device that is not talking to us.
func (s *executionStore) MarkStaleExecutionDevicesOffline(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		maxAge = ExecutionDeviceHeartbeatTTL
	}
	cutoff := LogicalNow().Add(-maxAge).Format(timestampLayout)
	result, err := s.db.ExecContext(ctx, `UPDATE execution_devices SET status = 'offline' WHERE kind = 'remote' AND status IN ('online', 'ready', 'degraded') AND updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CountQueuedRemoteExecutionTasks reports how much work is waiting for a device,
// restricted to the agents the caller is allowed to see. It exists so a polling
// device can back off instead of claiming in a tight loop.
func (s *executionStore) CountQueuedRemoteExecutionTasks(ctx context.Context, deviceID string, agentIDs []string) (int, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || deviceID == "local" {
		return 0, errors.New("invalid remote execution device")
	}
	filter, args, err := executionTaskAgentFilter(agentIDs)
	if err != nil {
		return 0, err
	}
	count := 0
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_execution_tasks WHERE execution_device_id = ? AND status = 'queued'`+filter, append([]any{deviceID}, args...)...).Scan(&count)
	return count, err
}

// ClaimRemoteExecutionTaskForAgents leases the oldest queued task for deviceID
// from the agents in agentIDs, which is required and must be non-empty.
//
// The allow list is part of the claim query rather than a check on the delivered
// task because a claim is irreversible from the device's point of view: the
// payload is already in the response by the time a caller could notice the
// agent was not granted, and the ledger has no leased-to-queued transition to
// undo it with. For the same reason there is no unrestricted claim: every caller
// has to say which agents the claimer is authorized for.
func (s *executionStore) ClaimRemoteExecutionTaskForAgents(ctx context.Context, deviceID, leaseOwner string, leaseUntil time.Time, agentIDs []string) (RemoteExecutionTask, error) {
	deviceID = strings.TrimSpace(deviceID)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if deviceID == "" || deviceID == "local" || leaseOwner == "" || len(leaseOwner) > 128 {
		return RemoteExecutionTask{}, errors.New("invalid remote execution lease")
	}
	now := LogicalNow()
	if !leaseUntil.After(now) || leaseUntil.After(now.Add(ExecutionLeaseMaxDuration)) {
		return RemoteExecutionTask{}, errors.New("invalid remote execution lease duration")
	}
	filter, filterArgs, err := executionTaskAgentFilter(agentIDs)
	if err != nil {
		return RemoteExecutionTask{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemoteExecutionTask{}, err
	}
	defer tx.Rollback()
	var id string
	selectArgs := append([]any{deviceID}, filterArgs...)
	if err := tx.QueryRowContext(ctx, `SELECT id FROM remote_execution_tasks WHERE execution_device_id = ? AND status = 'queued'`+filter+` ORDER BY created_at ASC, id ASC LIMIT 1`, selectArgs...).Scan(&id); err != nil {
		return RemoteExecutionTask{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE remote_execution_tasks SET status = 'leased', lease_owner = ?, lease_until = ?, attempt_count = attempt_count + 1, revision = revision + 1, updated_at = ? WHERE id = ? AND status = 'queued'`, leaseOwner, leaseUntil.UTC().Format(time.RFC3339Nano), Now(), id)
	if err != nil {
		return RemoteExecutionTask{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return RemoteExecutionTask{}, err
		}
		return RemoteExecutionTask{}, fmt.Errorf("%w: remote task was claimed", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return RemoteExecutionTask{}, err
	}
	return s.GetRemoteExecutionTask(ctx, id)
}

// ExpireRemoteExecutionLeases fails leases whose deadline passed, so a device
// that dies mid-task releases the ledger row instead of pinning it forever.
// no_fallback stays set, so an expired task is never retried locally.
func (s *executionStore) ExpireRemoteExecutionLeases(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, revision, lease_until FROM remote_execution_tasks WHERE status IN ('leased', 'running')`)
	if err != nil {
		return 0, err
	}
	type expiring struct {
		id       string
		revision int64
	}
	now := LogicalNow()
	var stale []expiring
	for rows.Next() {
		var candidate expiring
		var leaseUntil string
		if err := rows.Scan(&candidate.id, &candidate.revision, &leaseUntil); err != nil {
			rows.Close()
			return 0, err
		}
		deadline, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(leaseUntil))
		if parseErr != nil || now.Before(deadline) {
			continue
		}
		stale = append(stale, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	expired := int64(0)
	for _, candidate := range stale {
		// A concurrent report may have finished the task between the scan and
		// this transition. The revision check makes that a no-op rather than an
		// overwrite of a real result.
		if _, err := s.TransitionRemoteExecutionTask(ctx, candidate.id, candidate.revision, "expired", nil, ""); err != nil {
			if IsConflict(err) {
				continue
			}
			return expired, err
		}
		expired++
	}
	return expired, nil
}

func executionTaskAgentFilter(agentIDs []string) (string, []any, error) {
	unique := make(map[string]struct{}, len(agentIDs))
	args := make([]any, 0, len(agentIDs))
	placeholders := make([]string, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			continue
		}
		if _, exists := unique[agentID]; exists {
			continue
		}
		unique[agentID] = struct{}{}
		args = append(args, agentID)
		placeholders = append(placeholders, "?")
	}
	if len(args) == 0 {
		return "", nil, errors.New("remote execution claim requires at least one authorized agent")
	}
	if len(args) > ExecutionDeviceMaxClaimAgents {
		return "", nil, errors.New("remote execution claim carries too many authorized agents")
	}
	return " AND agent_id IN (" + strings.Join(placeholders, ", ") + ")", args, nil
}
