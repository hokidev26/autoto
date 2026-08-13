package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newExecutionTransportStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "execution-transport.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, _, agent, err := store.CreateProject(ctx, "Transport", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	return store, project.ID, agent.ID
}

const executionTransportFingerprint = "sha256:00112233445566778899aabbccddeeff"

func registerLiveExecutionDevice(t *testing.T, store *Store, projectID, name string) ExecutionDevice {
	t.Helper()
	ctx := context.Background()
	device, err := store.RegisterRemoteExecutionDevice(ctx, ExecutionDeviceRegistration{
		Name: name, IdentityFingerprint: executionTransportFingerprint, Capabilities: json.RawMessage(`{"tools":["Read"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetExecutionDeviceEnabled(ctx, device.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectDeviceGrant(ctx, ProjectDeviceGrant{ProjectID: projectID, DeviceID: device.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	live, err := store.RecordExecutionDeviceHeartbeat(ctx, device.ID, executionTransportFingerprint, "ready")
	if err != nil {
		t.Fatal(err)
	}
	return live
}

func TestExecutionDeviceHeartbeatBindsFingerprintAndRespectsDisable(t *testing.T) {
	ctx := context.Background()
	store, projectID, _ := newExecutionTransportStore(t)
	device := registerLiveExecutionDevice(t, store, projectID, "heartbeat-device")
	if device.Status != "ready" {
		t.Fatalf("heartbeat did not bring the device up: %+v", device)
	}

	if _, err := store.RecordExecutionDeviceHeartbeat(ctx, device.ID, "sha256:ffffffffffffffffffff", "ready"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a foreign fingerprint refreshed the device: %v", err)
	}
	if _, err := store.RecordExecutionDeviceHeartbeat(ctx, device.ID, executionTransportFingerprint, "not-a-status"); err == nil {
		t.Fatalf("an invented status was accepted")
	}
	if _, err := store.RecordExecutionDeviceHeartbeat(ctx, "local", executionTransportFingerprint, "ready"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the local device accepted a remote heartbeat: %v", err)
	}

	busy, err := store.RecordExecutionDeviceHeartbeat(ctx, device.ID, executionTransportFingerprint, "online")
	if err != nil || busy.Status != "online" {
		t.Fatalf("device could not report itself busy: %v %+v", err, busy)
	}

	if _, err := store.SetExecutionDeviceEnabled(ctx, device.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordExecutionDeviceHeartbeat(ctx, device.ID, executionTransportFingerprint, "ready"); !IsConflict(err) {
		t.Fatalf("a disabled device heartbeat its way back into service: %v", err)
	}
	disabled, err := store.GetExecutionDevice(ctx, device.ID)
	if err != nil || disabled.Status != "disabled" {
		t.Fatalf("disabled device status changed: %v %+v", err, disabled)
	}
}

func TestMarkStaleExecutionDevicesOfflineKeepsHeartbeatClock(t *testing.T) {
	ctx := context.Background()
	store, projectID, _ := newExecutionTransportStore(t)
	device := registerLiveExecutionDevice(t, store, projectID, "stale-device")

	fresh, err := store.MarkStaleExecutionDevicesOffline(ctx, ExecutionDeviceHeartbeatTTL)
	if err != nil || fresh != 0 {
		t.Fatalf("a fresh device was swept: %v %d", err, fresh)
	}

	stale := LogicalNow().Add(-10 * time.Minute).Format(timestampLayout)
	if _, err := store.DB().ExecContext(ctx, `UPDATE execution_devices SET updated_at = ? WHERE id = ?`, stale, device.ID); err != nil {
		t.Fatal(err)
	}
	swept, err := store.MarkStaleExecutionDevicesOffline(ctx, ExecutionDeviceHeartbeatTTL)
	if err != nil || swept != 1 {
		t.Fatalf("stale device was not swept: %v %d", err, swept)
	}
	var status, updatedAt string
	if err := store.DB().QueryRowContext(ctx, `SELECT status, updated_at FROM execution_devices WHERE id = ?`, device.ID).Scan(&status, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "offline" {
		t.Fatalf("stale device kept a live status: %s", status)
	}
	// Sweeping must not restart the heartbeat clock, otherwise a device that is
	// gone looks like it just checked in.
	if updatedAt != stale {
		t.Fatalf("sweep rewrote the heartbeat timestamp: %s", updatedAt)
	}
	if again, err := store.MarkStaleExecutionDevicesOffline(ctx, ExecutionDeviceHeartbeatTTL); err != nil || again != 0 {
		t.Fatalf("offline device was swept again: %v %d", err, again)
	}

	// The owner can bring it back only by hearing from the device again.
	revived, err := store.RecordExecutionDeviceHeartbeat(ctx, device.ID, executionTransportFingerprint, "ready")
	if err != nil || revived.Status != "ready" {
		t.Fatalf("device could not come back: %v %+v", err, revived)
	}
}

func TestClaimRemoteExecutionTaskRestrictsToAuthorizedAgents(t *testing.T) {
	ctx := context.Background()
	store, projectID, agentID := newExecutionTransportStore(t)
	device := registerLiveExecutionDevice(t, store, projectID, "claim-device")
	task, err := store.CreateRemoteExecutionTask(ctx, RemoteExecutionTask{
		IdempotencyKey: "claim-1", ProjectID: projectID, AgentID: agentID,
		ExecutionDeviceID: device.ID, Payload: json.RawMessage(`{"operation":"read"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseUntil := LogicalNow().Add(time.Minute)

	if _, err := store.ClaimRemoteExecutionTaskForAgents(ctx, device.ID, "pairing-1", leaseUntil, []string{}); err == nil {
		t.Fatalf("an empty allow list claimed work")
	}
	if _, err := store.ClaimRemoteExecutionTaskForAgents(ctx, device.ID, "pairing-1", leaseUntil, []string{"some-other-agent"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a task was delivered for an unauthorized agent: %v", err)
	}
	if _, err := store.ClaimRemoteExecutionTaskForAgents(ctx, device.ID, "pairing-1", LogicalNow().Add(2*ExecutionLeaseMaxDuration), []string{agentID}); err == nil {
		t.Fatalf("an unbounded lease was accepted")
	}

	queued, err := store.CountQueuedRemoteExecutionTasks(ctx, device.ID, []string{agentID})
	if err != nil || queued != 1 {
		t.Fatalf("queued count was wrong: %v %d", err, queued)
	}
	if hidden, err := store.CountQueuedRemoteExecutionTasks(ctx, device.ID, []string{"some-other-agent"}); err != nil || hidden != 0 {
		t.Fatalf("queued count leaked another agent's work: %v %d", err, hidden)
	}

	claimed, err := store.ClaimRemoteExecutionTaskForAgents(ctx, device.ID, "pairing-1", leaseUntil, []string{agentID})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != task.ID || claimed.Status != "leased" || claimed.LeaseOwner != "pairing-1" || claimed.AttemptCount != 1 || claimed.Revision != task.Revision+1 {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	if _, err := store.ClaimRemoteExecutionTaskForAgents(ctx, device.ID, "pairing-2", leaseUntil, []string{agentID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a leased task was handed out twice: %v", err)
	}
}

func TestExpireRemoteExecutionLeasesReleasesAbandonedWork(t *testing.T) {
	ctx := context.Background()
	store, projectID, agentID := newExecutionTransportStore(t)
	device := registerLiveExecutionDevice(t, store, projectID, "lease-device")
	if _, err := store.CreateRemoteExecutionTask(ctx, RemoteExecutionTask{
		IdempotencyKey: "lease-1", ProjectID: projectID, AgentID: agentID,
		ExecutionDeviceID: device.ID, Payload: json.RawMessage(`{"operation":"read"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRemoteExecutionTaskForAgents(ctx, device.ID, "pairing-1", LogicalNow().Add(time.Minute), []string{agentID})
	if err != nil {
		t.Fatal(err)
	}

	if expired, err := store.ExpireRemoteExecutionLeases(ctx); err != nil || expired != 0 {
		t.Fatalf("a live lease was expired: %v %d", err, expired)
	}
	past := LogicalNow().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := store.DB().ExecContext(ctx, `UPDATE remote_execution_tasks SET lease_until = ? WHERE id = ?`, past, claimed.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := store.ExpireRemoteExecutionLeases(ctx)
	if err != nil || expired != 1 {
		t.Fatalf("abandoned lease was not released: %v %d", err, expired)
	}
	settled, err := store.GetRemoteExecutionTask(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	// no_fallback stays set: an abandoned remote task is closed out, never
	// silently retried on the host.
	if settled.Status != "expired" || settled.LeaseOwner != "" || settled.CompletedAt == "" || !settled.NoFallback {
		t.Fatalf("expired task was not closed out: %+v", settled)
	}
}
