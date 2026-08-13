package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/peercontrol"
)

// The execution transport is the device-facing half of remote execution: a
// remote device claims queued ledger tasks and reports their outcome. It rides
// the peer protocol rather than a channel of its own, so a device is exactly a
// pairing that holds the execute_tools scope, and every claim is bounded by the
// same grants, revisions, and audit trail as the rest of peer control.
//
// A device is bound to a pairing by fingerprint: execution_devices stores the
// identity fingerprint at registration, and only the pairing whose peer identity
// matches it can act as that device. Owning a bearer for some other pairing is
// therefore not enough to collect another device's work.
const (
	executionTransportDefaultLeaseSeconds = 120
	executionTransportMaxResultBytes      = 16 << 10
	executionTransportErrorTextBytes      = 2 << 10
)

func (s *Server) peerExecutionHeartbeat(w http.ResponseWriter, r *http.Request) {
	var request peercontrol.ExecutionHeartbeatRequest
	authorized, ok := s.authorizePeerRequest(w, r, &request, peercontrol.ScopeExecuteTools, "")
	if !ok {
		return
	}
	status, err := db.NormalizeExecutionDeviceHeartbeatStatus(request.Status)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid execution device heartbeat status")
		return
	}
	device, ok := s.authorizePeerExecutionDevice(w, r, authorized, request.DeviceID)
	if !ok {
		return
	}
	// Heartbeats are liveness, not authority: they change no grant, deliver no
	// payload, and arrive on a timer. Auditing them would bury the claim and
	// report rows that do carry security meaning under thousands of pings.
	updated, err := s.store.RecordExecutionDeviceHeartbeat(r.Context(), device.ID, authorized.pairing.PeerFingerprint, status)
	if err != nil {
		s.writePeerExecutionDeviceError(w, err)
		return
	}
	if _, err := s.store.TouchRemotePeerPairingLastSeen(r.Context(), authorized.pairing.ID, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "peer execution heartbeat failed")
		return
	}
	agentIDs := peerExecutionAgentIDs(authorized)
	queued := 0
	if len(agentIDs) > 0 {
		queued, err = s.store.CountQueuedRemoteExecutionTasks(r.Context(), device.ID, agentIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "peer execution heartbeat failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, peercontrol.ExecutionHeartbeatResponse{
		ProtocolVersion:  peercontrol.ProtocolVersion,
		DeviceID:         updated.ID,
		Status:           updated.Status,
		QueuedTasks:      queued,
		LeaseMaxSeconds:  int(db.ExecutionLeaseMaxDuration / time.Second),
		HeartbeatSeconds: int(db.ExecutionDeviceHeartbeatTTL / time.Second / 3),
	})
}

func (s *Server) peerExecutionClaim(w http.ResponseWriter, r *http.Request) {
	var request peercontrol.ExecutionClaimRequest
	authorized, ok := s.authorizePeerRequest(w, r, &request, peercontrol.ScopeExecuteTools, "")
	if !ok {
		return
	}
	device, ok := s.authorizePeerExecutionDevice(w, r, authorized, request.DeviceID)
	if !ok {
		return
	}
	agentIDs := peerExecutionAgentIDs(authorized)
	if len(agentIDs) == 0 {
		writeError(w, http.StatusForbidden, "peer grant does not authorize execution for any agent")
		return
	}
	lease := time.Duration(request.LeaseSeconds) * time.Second
	if request.LeaseSeconds <= 0 {
		lease = executionTransportDefaultLeaseSeconds * time.Second
	}
	if lease > db.ExecutionLeaseMaxDuration {
		writeError(w, http.StatusBadRequest, "requested execution lease is too long")
		return
	}
	// A device that died holding a lease would otherwise pin its task forever,
	// and no_fallback means nothing else will ever pick it up.
	if _, err := s.store.ExpireRemoteExecutionLeases(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "peer execution claim failed")
		return
	}
	// The lease deadline is measured on the store's logical clock, because that
	// is the clock the store validates the window against. A server test clock
	// would otherwise produce a deadline the store reads as already expired.
	task, err := s.store.ClaimRemoteExecutionTaskForAgents(r.Context(), device.ID, authorized.pairing.ID, db.LogicalNow().Add(lease), agentIDs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, peercontrol.ExecutionClaimResponse{ProtocolVersion: peercontrol.ProtocolVersion})
			return
		}
		if db.IsConflict(err) {
			writeError(w, http.StatusConflict, "remote execution task was already claimed")
			return
		}
		writeError(w, http.StatusInternalServerError, "peer execution claim failed")
		return
	}
	grant, granted := peerExecutionGrantForAgent(authorized, task.AgentID)
	if !granted {
		s.releasePeerExecutionLease(r, task, "grant_missing")
		writeError(w, http.StatusForbidden, "peer grant does not authorize this agent")
		return
	}
	// The claim is audited after the ledger row moves, so the record can name the
	// task that was handed over. If the audit cannot be persisted the lease is
	// cancelled again rather than left as an unaudited delivery.
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "execution.claim", Actor: peerActor(authorized.pairing.PeerFingerprint), AgentID: task.AgentID, RunID: task.RunID,
		SubjectType: "remote_execution_task", SubjectID: task.ID, Outcome: "success", Risk: "high",
		Details: map[string]any{"pairingId": authorized.pairing.ID, "executionDeviceId": device.ID, "attemptCount": task.AttemptCount},
	}); err != nil {
		s.releasePeerExecutionLease(r, task, "audit_failed")
		writeError(w, http.StatusInternalServerError, "remote execution task was not delivered because audit persistence failed")
		return
	}
	leaseUntil, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(task.LeaseUntil))
	if err != nil {
		s.releasePeerExecutionLease(r, task, "invalid_lease")
		writeError(w, http.StatusInternalServerError, "peer execution claim failed")
		return
	}
	// The device is busy from here until it reports, which is what 'online'
	// means for a device; 'ready' is reserved for idle and available. If the
	// device stopped being usable in the meantime the lease is released, because
	// the payload is not going out in this response.
	if _, err := s.store.RecordExecutionDeviceHeartbeat(r.Context(), device.ID, authorized.pairing.PeerFingerprint, "online"); err != nil {
		s.releasePeerExecutionLease(r, task, "device_unavailable")
		s.writePeerExecutionDeviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, peercontrol.ExecutionClaimResponse{
		ProtocolVersion: peercontrol.ProtocolVersion,
		Task: &peercontrol.ExecutionTask{
			TaskID: task.ID, ProjectID: task.ProjectID, AgentID: task.AgentID, RunID: task.RunID,
			Payload: task.Payload, PermissionModeCap: grant.PermissionModeCap,
			Revision: task.Revision, AttemptCount: task.AttemptCount, LeaseUntil: leaseUntil,
		},
	})
}

func (s *Server) peerExecutionReport(w http.ResponseWriter, r *http.Request) {
	var request peercontrol.ExecutionReportRequest
	authorized, ok := s.authorizePeerRequest(w, r, &request, peercontrol.ScopeExecuteTools, "")
	if !ok {
		return
	}
	device, ok := s.authorizePeerExecutionDevice(w, r, authorized, request.DeviceID)
	if !ok {
		return
	}
	status := strings.ToLower(strings.TrimSpace(request.Status))
	switch status {
	case "running", "succeeded", "failed":
	default:
		writeError(w, http.StatusBadRequest, "invalid remote execution report status")
		return
	}
	if len(request.Result) > executionTransportMaxResultBytes || request.Revision < 1 {
		writeError(w, http.StatusBadRequest, "invalid remote execution report")
		return
	}
	// A device reports failures in its own words, so the text is redacted and
	// bounded before it reaches the ledger, events, or the owner's screen.
	failure := boundedPeerText(agentpkg.RedactToolActivityText(strings.TrimSpace(request.Error)), executionTransportErrorTextBytes)
	if status == "failed" && failure == "" {
		writeError(w, http.StatusBadRequest, "failed remote execution report requires an error")
		return
	}
	task, err := s.store.GetRemoteExecutionTask(r.Context(), strings.TrimSpace(request.TaskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "remote execution task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "peer execution report failed")
		return
	}
	// Holding the lease is what authorizes a report. Without this a second paired
	// device could finish, or silently fail, work it never received.
	if task.ExecutionDeviceID != device.ID || !constantStringBytesEqual(task.LeaseOwner, authorized.pairing.ID) {
		writeError(w, http.StatusForbidden, "peer does not hold the lease for this task")
		return
	}
	if _, granted := peerExecutionGrantForAgent(authorized, task.AgentID); !granted {
		writeError(w, http.StatusForbidden, "peer grant does not authorize this agent")
		return
	}
	// The report is audited before the ledger moves, so no outcome can be applied
	// without a record. The event describes what the device asserted, which stays
	// true even when the compare-and-swap below rejects it; the reported revision
	// is included so an audit reader can tell accepted reports from stale ones.
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "execution.report", Actor: peerActor(authorized.pairing.PeerFingerprint), AgentID: task.AgentID, RunID: task.RunID,
		SubjectType: "remote_execution_task", SubjectID: task.ID, Outcome: peerExecutionAuditOutcome(status), Risk: "high",
		Details: map[string]any{
			"pairingId": authorized.pairing.ID, "executionDeviceId": device.ID, "status": status,
			"reportedRevision": request.Revision, "ledgerRevision": task.Revision,
		},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "remote execution report was not applied because audit persistence failed")
		return
	}
	// Liveness is refreshed before the ledger moves. Reporting proves the device
	// is alive whether or not the compare-and-swap below accepts the outcome, and
	// doing it afterwards would turn a failed liveness write into a 500 for a
	// report the host had already committed, which a device can only answer by
	// retrying a report that is now stale.
	deviceStatus := "online"
	if status != "running" {
		deviceStatus = "ready"
	}
	if _, err := s.store.RecordExecutionDeviceHeartbeat(r.Context(), device.ID, authorized.pairing.PeerFingerprint, deviceStatus); err != nil {
		s.writePeerExecutionDeviceError(w, err)
		return
	}
	updated, err := s.store.TransitionRemoteExecutionTask(r.Context(), task.ID, request.Revision, status, request.Result, failure)
	if err != nil {
		if db.IsConflict(err) {
			writeError(w, http.StatusConflict, "remote execution task changed")
			return
		}
		// Result payloads are device-controlled, so validation failures are never
		// echoed back.
		writeError(w, http.StatusBadRequest, "remote execution report was rejected")
		return
	}
	writeJSON(w, http.StatusOK, peercontrol.ExecutionReportResponse{
		ProtocolVersion: peercontrol.ProtocolVersion, TaskID: updated.ID, Status: updated.Status,
		Revision: updated.Revision, ReportedAt: s.now().UTC(),
	})
}

// authorizePeerExecutionDevice resolves the device a peer claims to be. Stale
// devices are swept first so an owner-visible 'ready' status, and every decision
// that trusts it, reflects a recent heartbeat rather than the last one before a
// device disappeared.
func (s *Server) authorizePeerExecutionDevice(w http.ResponseWriter, r *http.Request, authorized peerAuthorizedRequest, deviceID string) (db.ExecutionDevice, bool) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || deviceID == "local" || len(deviceID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid execution device")
		return db.ExecutionDevice{}, false
	}
	if _, err := s.store.MarkStaleExecutionDevicesOffline(r.Context(), db.ExecutionDeviceHeartbeatTTL); err != nil {
		writeError(w, http.StatusInternalServerError, "peer execution authorization failed")
		return db.ExecutionDevice{}, false
	}
	device, err := s.store.GetRemoteExecutionDeviceForFingerprint(r.Context(), deviceID, authorized.pairing.PeerFingerprint)
	if err != nil {
		s.writePeerExecutionDeviceError(w, err)
		return db.ExecutionDevice{}, false
	}
	if !device.Enabled {
		writeError(w, http.StatusForbidden, "execution device is disabled")
		return db.ExecutionDevice{}, false
	}
	return device, true
}

func (s *Server) writePeerExecutionDeviceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Neither the device id nor the reason is echoed: a peer must not be able
		// to enumerate which registrations exist by comparing error messages.
		writeError(w, http.StatusForbidden, "peer is not authorized for this execution device")
	case db.IsConflict(err):
		writeError(w, http.StatusConflict, "execution device state changed")
	default:
		writeError(w, http.StatusInternalServerError, "peer execution authorization failed")
	}
}

// releasePeerExecutionLease cancels a task that was leased but must not run. The
// ledger has no leased-to-queued transition, and no_fallback forbids running it
// locally, so cancelling is the only way to stop holding a lease nobody owns.
func (s *Server) releasePeerExecutionLease(r *http.Request, task db.RemoteExecutionTask, reason string) {
	if _, err := s.store.TransitionRemoteExecutionTask(r.Context(), task.ID, task.Revision, "cancelled", nil, ""); err != nil {
		return
	}
	_ = s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "execution.release", Actor: "host", AgentID: task.AgentID, RunID: task.RunID,
		SubjectType: "remote_execution_task", SubjectID: task.ID, Outcome: "denied", Risk: "medium",
		Details: map[string]any{"executionDeviceId": task.ExecutionDeviceID, "reason": reason},
	})
}

// peerExecutionAgentIDs lists the agents this session may execute for. Grants
// from an older revision are ignored rather than tolerated, so re-authorizing a
// pairing narrows what an already-issued bearer can claim.
func peerExecutionAgentIDs(authorized peerAuthorizedRequest) []string {
	agentIDs := make([]string, 0, len(authorized.grants))
	for _, grant := range authorized.grants {
		if grant.Revision != authorized.session.GrantRevision || !scopeStringsContain(grant.Scopes, peercontrol.ScopeExecuteTools) {
			continue
		}
		if agentID := strings.TrimSpace(grant.AgentID); agentID != "" {
			agentIDs = append(agentIDs, agentID)
		}
	}
	return agentIDs
}

func peerExecutionGrantForAgent(authorized peerAuthorizedRequest, agentID string) (db.RemotePeerGrant, bool) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return db.RemotePeerGrant{}, false
	}
	for _, grant := range authorized.grants {
		if grant.AgentID != agentID || grant.Revision != authorized.session.GrantRevision {
			continue
		}
		if !scopeStringsContain(grant.Scopes, peercontrol.ScopeExecuteTools) {
			continue
		}
		return grant, true
	}
	return db.RemotePeerGrant{}, false
}

func peerExecutionAuditOutcome(status string) string {
	switch status {
	case "failed":
		return "failure"
	case "running":
		return "unknown"
	default:
		return "success"
	}
}

// remoteExecutionTransportReady reports whether a device could actually collect
// a task right now: the transport rides peer sharing, so with sharing off the
// ledger is a queue with no reachable consumer and says so.
func (s *Server) remoteExecutionTransportReady() bool {
	return s != nil && s.peerControl != nil && s.peerControl.manager != nil && s.peerControl.manager.SharingEnabled()
}
