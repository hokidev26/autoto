package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoto/internal/db"
	"autoto/internal/peercontrol"
)

type executionTransportFixture struct {
	app     *Server
	store   *db.Store
	pairing db.RemotePeerPairing
	agent   db.Agent
	project string
	device  db.ExecutionDevice
	token   string
}

func newExecutionTransportFixture(t *testing.T, pairingScopes, grantScopes []string) executionTransportFixture {
	t.Helper()
	ctx := context.Background()
	app, store, manager := newPeerAPITestServer(t)
	enablePeerManagerForAPI(t, manager)
	controller := newPeerAPIIdentity(t)
	pairing, agent := createPeerAPIHostPairing(t, store, controller, pairingScopes, grantScopes, db.RemotePeerPermissionModeAcceptEdits)
	grants, err := store.ListRemotePeerGrants(ctx, pairing.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("expected a single grant: %v %+v", err, grants)
	}
	device, err := store.RegisterRemoteExecutionDevice(ctx, db.ExecutionDeviceRegistration{
		Name: "paired-device", IdentityFingerprint: pairing.PeerFingerprint, Capabilities: json.RawMessage(`{"tools":["Read"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetExecutionDeviceEnabled(ctx, device.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetProjectDeviceGrant(ctx, db.ProjectDeviceGrant{ProjectID: grants[0].ProjectID, DeviceID: device.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return executionTransportFixture{
		app: app, store: store, pairing: pairing, agent: agent, project: grants[0].ProjectID, device: device,
		token: peerAPISessionToken(t, manager, controller, pairing.ID),
	}
}

func (fixture executionTransportFixture) call(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	request := peerAPITestRequest(t, http.MethodPost, path, body)
	request.Header.Set("Authorization", "Bearer "+fixture.token)
	response := httptest.NewRecorder()
	fixture.app.Routes().ServeHTTP(response, request)
	return response
}

func (fixture executionTransportFixture) queueTask(t *testing.T, key string, payload string) db.RemoteExecutionTask {
	t.Helper()
	task, err := fixture.store.CreateRemoteExecutionTask(context.Background(), db.RemoteExecutionTask{
		IdempotencyKey: key, ProjectID: fixture.project, AgentID: fixture.agent.ID,
		ExecutionDeviceID: fixture.device.ID, Payload: json.RawMessage(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (fixture executionTransportFixture) deviceStatus(t *testing.T) string {
	t.Helper()
	device, err := fixture.store.GetExecutionDevice(context.Background(), fixture.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	return device.Status
}

func executionTransportAuditActions(t *testing.T, store *db.Store) []string {
	t.Helper()
	rows, err := store.DB().QueryContext(context.Background(), `SELECT action FROM automation_audit_events WHERE category = 'peer' AND action LIKE 'execution.%' ORDER BY created_at ASC, id ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actions := []string{}
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return actions
}

func TestPeerExecutionTransportDeliversClaimsAndReportsTask(t *testing.T) {
	fixture := newExecutionTransportFixture(t,
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
	)

	heartbeat := fixture.call(t, "/api/peer/v1/execution/heartbeat", peercontrol.ExecutionHeartbeatRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID,
	})
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat returned %d: %s", heartbeat.Code, heartbeat.Body.String())
	}
	var live peercontrol.ExecutionHeartbeatResponse
	if err := json.Unmarshal(heartbeat.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if live.Status != "ready" || live.QueuedTasks != 0 || live.LeaseMaxSeconds <= 0 || live.HeartbeatSeconds <= 0 {
		t.Fatalf("unexpected heartbeat response: %+v", live)
	}
	// A device only becomes bindable once it is live, so the ledger must accept
	// work for it right after the first heartbeat.
	queued := fixture.queueTask(t, "transport-happy", `{"operation":"read","path":"README.md"}`)

	pending := fixture.call(t, "/api/peer/v1/execution/heartbeat", peercontrol.ExecutionHeartbeatRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, Status: "ready",
	})
	live = peercontrol.ExecutionHeartbeatResponse{}
	if err := json.Unmarshal(pending.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if pending.Code != http.StatusOK || live.QueuedTasks != 1 {
		t.Fatalf("queued count did not reach the device: %d %+v", pending.Code, live)
	}

	claim := fixture.call(t, "/api/peer/v1/execution/claim", peercontrol.ExecutionClaimRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, LeaseSeconds: 60,
	})
	if claim.Code != http.StatusOK {
		t.Fatalf("claim returned %d: %s", claim.Code, claim.Body.String())
	}
	var claimed peercontrol.ExecutionClaimResponse
	if err := json.Unmarshal(claim.Body.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.Task == nil {
		t.Fatalf("claim delivered no task: %s", claim.Body.String())
	}
	if claimed.Task.TaskID != queued.ID || claimed.Task.AgentID != fixture.agent.ID ||
		claimed.Task.PermissionModeCap != db.RemotePeerPermissionModeAcceptEdits ||
		claimed.Task.AttemptCount != 1 || !strings.Contains(string(claimed.Task.Payload), "README.md") {
		t.Fatalf("unexpected claimed task: %+v", claimed.Task)
	}
	if status := fixture.deviceStatus(t); status != "online" {
		t.Fatalf("claiming did not mark the device busy: %s", status)
	}

	running := fixture.call(t, "/api/peer/v1/execution/report", peercontrol.ExecutionReportRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, TaskID: claimed.Task.TaskID,
		Revision: claimed.Task.Revision, Status: "running",
	})
	if running.Code != http.StatusOK {
		t.Fatalf("running report returned %d: %s", running.Code, running.Body.String())
	}
	var progressed peercontrol.ExecutionReportResponse
	if err := json.Unmarshal(running.Body.Bytes(), &progressed); err != nil {
		t.Fatal(err)
	}
	if progressed.Status != "running" || progressed.Revision <= claimed.Task.Revision {
		t.Fatalf("running report did not advance the ledger: %+v", progressed)
	}

	stale := fixture.call(t, "/api/peer/v1/execution/report", peercontrol.ExecutionReportRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, TaskID: claimed.Task.TaskID,
		Revision: claimed.Task.Revision, Status: "succeeded",
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale revision was accepted: %d %s", stale.Code, stale.Body.String())
	}

	done := fixture.call(t, "/api/peer/v1/execution/report", peercontrol.ExecutionReportRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, TaskID: claimed.Task.TaskID,
		Revision: progressed.Revision, Status: "succeeded", Result: json.RawMessage(`{"exitCode":0}`),
	})
	if done.Code != http.StatusOK {
		t.Fatalf("terminal report returned %d: %s", done.Code, done.Body.String())
	}
	settled, err := fixture.store.GetRemoteExecutionTask(context.Background(), claimed.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "succeeded" || settled.LeaseOwner != "" || settled.CompletedAt == "" || !strings.Contains(string(settled.Result), "exitCode") {
		t.Fatalf("terminal report did not settle the ledger: %+v", settled)
	}
	if status := fixture.deviceStatus(t); status != "ready" {
		t.Fatalf("finishing did not return the device to idle: %s", status)
	}

	empty := fixture.call(t, "/api/peer/v1/execution/claim", peercontrol.ExecutionClaimRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID,
	})
	var drained peercontrol.ExecutionClaimResponse
	if err := json.Unmarshal(empty.Body.Bytes(), &drained); err != nil {
		t.Fatal(err)
	}
	if empty.Code != http.StatusOK || drained.Task != nil {
		t.Fatalf("empty queue was not a normal answer: %d %s", empty.Code, empty.Body.String())
	}

	// One delivery plus three reports: the stale-revision attempt rejected above
	// is audited too, because a report is recorded before the ledger is allowed
	// to move.
	actions := executionTransportAuditActions(t, fixture.store)
	if len(actions) != 4 || actions[0] != "execution.claim" {
		t.Fatalf("transport did not audit delivery and outcomes: %v", actions)
	}
	for _, action := range actions[1:] {
		if action != "execution.report" {
			t.Fatalf("unexpected transport audit action %q in %v", action, actions)
		}
	}

	// The ledger reports the transport as usable only while peer sharing can
	// actually reach a device.
	ledger := makeRemoteExecutionTaskLedger(settled, fixture.app.remoteExecutionTransportReady())
	if !ledger.TransportImplemented {
		t.Fatalf("transport was not reported as available while sharing is on")
	}
	if err := fixture.app.peerControl.manager.SetSharingEnabled(false); err != nil {
		t.Fatal(err)
	}
	if fixture.app.remoteExecutionTransportReady() {
		t.Fatalf("transport stayed available after sharing was switched off")
	}
}

func TestPeerExecutionTransportRequiresExecuteToolsScope(t *testing.T) {
	fixture := newExecutionTransportFixture(t,
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeSendTask},
		[]string{db.RemotePeerScopeObserve},
	)
	response := fixture.call(t, "/api/peer/v1/execution/heartbeat", peercontrol.ExecutionHeartbeatRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID,
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("observe-only pairing reached the transport: %d %s", response.Code, response.Body.String())
	}
	if status := fixture.deviceStatus(t); status != "unknown" {
		t.Fatalf("unauthorized heartbeat changed device liveness: %s", status)
	}
}

func TestPeerExecutionTransportBindsDeviceToPairingFingerprint(t *testing.T) {
	fixture := newExecutionTransportFixture(t,
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
	)
	ctx := context.Background()
	foreign, err := fixture.store.RegisterRemoteExecutionDevice(ctx, db.ExecutionDeviceRegistration{
		Name: "someone-elses-device", IdentityFingerprint: "sha256:0000000000000000feed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SetExecutionDeviceEnabled(ctx, foreign.ID, true); err != nil {
		t.Fatal(err)
	}
	response := fixture.call(t, "/api/peer/v1/execution/heartbeat", peercontrol.ExecutionHeartbeatRequest{
		PairingID: fixture.pairing.ID, DeviceID: foreign.ID,
	})
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), foreign.ID) {
		t.Fatalf("a pairing acted as another device: %d %s", response.Code, response.Body.String())
	}

	disabled, err := fixture.store.SetExecutionDeviceEnabled(ctx, fixture.device.ID, false)
	if err != nil || disabled.Enabled {
		t.Fatalf("device was not disabled: %v %+v", err, disabled)
	}
	blocked := fixture.call(t, "/api/peer/v1/execution/heartbeat", peercontrol.ExecutionHeartbeatRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, Status: "ready",
	})
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("a disabled device heartbeat its way back into service: %d %s", blocked.Code, blocked.Body.String())
	}
	if status := fixture.deviceStatus(t); status != "disabled" {
		t.Fatalf("disabled device changed status: %s", status)
	}
}

func TestPeerExecutionReportRequiresHeldLease(t *testing.T) {
	fixture := newExecutionTransportFixture(t,
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
	)
	ctx := context.Background()
	if _, err := fixture.store.RecordExecutionDeviceHeartbeat(ctx, fixture.device.ID, fixture.pairing.PeerFingerprint, "ready"); err != nil {
		t.Fatal(err)
	}
	queued := fixture.queueTask(t, "transport-lease", `{"operation":"read"}`)

	unleased := fixture.call(t, "/api/peer/v1/execution/report", peercontrol.ExecutionReportRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, TaskID: queued.ID,
		Revision: queued.Revision, Status: "running",
	})
	if unleased.Code != http.StatusForbidden {
		t.Fatalf("a never-claimed task was reportable: %d %s", unleased.Code, unleased.Body.String())
	}

	claim := fixture.call(t, "/api/peer/v1/execution/claim", peercontrol.ExecutionClaimRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID,
	})
	var claimed peercontrol.ExecutionClaimResponse
	if err := json.Unmarshal(claim.Body.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	if claim.Code != http.StatusOK || claimed.Task == nil {
		t.Fatalf("claim failed: %d %s", claim.Code, claim.Body.String())
	}
	// Another pairing holding the lease must not be reportable through this one,
	// even though this pairing owns the device and the grant.
	if _, err := fixture.store.DB().ExecContext(ctx, `UPDATE remote_execution_tasks SET lease_owner = 'another-pairing' WHERE id = ?`, claimed.Task.TaskID); err != nil {
		t.Fatal(err)
	}
	stolen := fixture.call(t, "/api/peer/v1/execution/report", peercontrol.ExecutionReportRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, TaskID: claimed.Task.TaskID,
		Revision: claimed.Task.Revision, Status: "succeeded",
	})
	if stolen.Code != http.StatusForbidden {
		t.Fatalf("a foreign lease was reportable: %d %s", stolen.Code, stolen.Body.String())
	}

	missingError := fixture.call(t, "/api/peer/v1/execution/report", peercontrol.ExecutionReportRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, TaskID: claimed.Task.TaskID,
		Revision: claimed.Task.Revision, Status: "failed",
	})
	if missingError.Code != http.StatusBadRequest {
		t.Fatalf("a failure without a reason was accepted: %d %s", missingError.Code, missingError.Body.String())
	}
}

func TestPeerExecutionReportRejectsSensitiveResultPayload(t *testing.T) {
	fixture := newExecutionTransportFixture(t,
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeExecuteTools},
	)
	if _, err := fixture.store.RecordExecutionDeviceHeartbeat(context.Background(), fixture.device.ID, fixture.pairing.PeerFingerprint, "ready"); err != nil {
		t.Fatal(err)
	}
	fixture.queueTask(t, "transport-sensitive", `{"operation":"read"}`)
	claim := fixture.call(t, "/api/peer/v1/execution/claim", peercontrol.ExecutionClaimRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID,
	})
	var claimed peercontrol.ExecutionClaimResponse
	if err := json.Unmarshal(claim.Body.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.Task == nil {
		t.Fatalf("claim failed: %d %s", claim.Code, claim.Body.String())
	}
	const marker = "device-supplied-secret-marker"
	response := fixture.call(t, "/api/peer/v1/execution/report", peercontrol.ExecutionReportRequest{
		PairingID: fixture.pairing.ID, DeviceID: fixture.device.ID, TaskID: claimed.Task.TaskID,
		Revision: claimed.Task.Revision, Status: "succeeded", Result: json.RawMessage(`{"token":"` + marker + `"}`),
	})
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), marker) {
		t.Fatalf("sensitive device result was not safely rejected: %d %s", response.Code, response.Body.String())
	}
	settled, err := fixture.store.GetRemoteExecutionTask(context.Background(), claimed.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settled.Result), marker) || settled.Status == "succeeded" {
		t.Fatalf("rejected result reached the ledger: %+v", settled)
	}
}
