package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/peercontrol"
	"autoto/internal/tools"
)

// The server is the runner-facing peer collaboration bridge: it resolves
// pairing ids to authenticated peer clients and enforces the same audit and
// validation rules as the HTTP controller proxies.
var _ tools.PeerCollaborationService = (*Server)(nil)

var (
	errRemoteCollaborationUnavailable = errors.New("remote collaboration is unavailable on this instance")
	errRemotePeerPairingInactive      = errors.New("remote peer pairing is inactive")
)

// remotePeerStoreError marks database failures from the controller-client core
// so HTTP handlers can keep translating them through writeStoreError while the
// runner bridge maps them to readable messages.
type remotePeerStoreError struct{ err error }

func (e *remotePeerStoreError) Error() string { return e.err.Error() }
func (e *remotePeerStoreError) Unwrap() error { return e.err }

// controllerPeerClient is the transport-agnostic core shared by the HTTP
// controller proxies and the runner bridge. It resolves an active, unexpired
// controller-role pairing to an authenticated peer client. Errors are either
// errRemoteCollaborationUnavailable, a *remotePeerStoreError wrapping the
// store failure, errRemotePeerPairingInactive, or a peercontrol client error.
func (s *Server) controllerPeerClient(ctx context.Context, pairingID string) (db.RemotePeerPairing, *peercontrol.Client, error) {
	if s == nil || s.peerControl == nil || s.peerControl.manager == nil || s.store == nil {
		return db.RemotePeerPairing{}, nil, errRemoteCollaborationUnavailable
	}
	pairing, err := s.store.GetRemotePeerPairing(ctx, pairingID)
	if err != nil {
		return db.RemotePeerPairing{}, nil, &remotePeerStoreError{err: err}
	}
	if pairing.LocalRole != db.RemotePeerLocalRoleController || pairing.Status != db.RemotePeerPairingStatusActive || remotePairingExpired(pairing, s.now()) {
		return db.RemotePeerPairing{}, nil, errRemotePeerPairingInactive
	}
	client, err := s.peerControl.clientForPairing(pairing)
	if err != nil {
		return db.RemotePeerPairing{}, nil, err
	}
	return pairing, client, nil
}

// peerBridgeError maps controllerPeerClient and peercontrol failures onto the
// stable, human-readable messages the model-facing tools surface. The
// peercontrol phrasing intentionally matches writePeerControlError.
func peerBridgeError(err error) error {
	var storeErr *remotePeerStoreError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errRemoteCollaborationUnavailable), errors.Is(err, errRemotePeerPairingInactive):
		return err
	case errors.As(err, &storeErr):
		inner := storeErr.Unwrap()
		switch {
		case db.IsNotFound(inner):
			return errors.New("remote peer pairing was not found")
		case db.IsConflict(inner):
			return inner
		default:
			return errors.New("remote collaboration storage request failed")
		}
	case errors.Is(err, peercontrol.ErrDisabled), errors.Is(err, peercontrol.ErrClosed):
		return errors.New("remote collaboration is unavailable")
	case errors.Is(err, peercontrol.ErrUnauthorized):
		return errors.New("peer authentication failed")
	case errors.Is(err, peercontrol.ErrConflict):
		return errors.New("peer request conflicts with current state")
	case errors.Is(err, peercontrol.ErrProtocol):
		return errors.New("peer protocol validation failed")
	default:
		return errors.New("remote collaboration request failed")
	}
}

func (s *Server) bridgeControllerPeerClient(ctx context.Context, pairingID string) (db.RemotePeerPairing, *peercontrol.Client, error) {
	pairing, client, err := s.controllerPeerClient(ctx, pairingID)
	if err != nil {
		return db.RemotePeerPairing{}, nil, peerBridgeError(err)
	}
	return pairing, client, nil
}

func clampPeerBridgeLimit(value, fallback int) int {
	if value == 0 {
		value = fallback
	}
	if value < 1 {
		return 1
	}
	if value > 100 {
		return 100
	}
	return value
}

// ListPeerPairings returns the active, unexpired controller-role pairings the
// local agent may drive. An empty result is not an error.
func (s *Server) ListPeerPairings(ctx context.Context) ([]tools.PeerPairingSummary, error) {
	if s == nil || s.peerControl == nil || s.peerControl.manager == nil || s.store == nil {
		return nil, errRemoteCollaborationUnavailable
	}
	pairings, err := s.store.ListRemotePeerPairings(ctx, db.RemotePeerPairingListOptions{
		LocalRole: db.RemotePeerLocalRoleController, Status: db.RemotePeerPairingStatusActive, Limit: 200,
	})
	if err != nil {
		return nil, peerBridgeError(&remotePeerStoreError{err: err})
	}
	now := s.now()
	summaries := make([]tools.PeerPairingSummary, 0, len(pairings))
	for _, pairing := range pairings {
		if remotePairingExpired(pairing, now) {
			continue
		}
		summaries = append(summaries, tools.PeerPairingSummary{
			PairingID:   pairing.ID,
			DisplayName: pairing.DisplayName,
			Fingerprint: pairing.PeerFingerprint,
			Scopes:      pairing.Scopes,
			ExpiresAt:   pairing.ExpiresAt,
			LastSeenAt:  pairing.LastSeenAt,
		})
	}
	return summaries, nil
}

// PeerSnapshot fetches the remote peer's shared projects/agents view over the
// authenticated peer client and returns the raw protocol JSON.
func (s *Server) PeerSnapshot(ctx context.Context, request tools.PeerSnapshotRequest) (json.RawMessage, error) {
	pairingID := strings.TrimSpace(request.PairingID)
	if pairingID == "" {
		return nil, errors.New("peer snapshot requires a pairing id")
	}
	pairing, client, err := s.bridgeControllerPeerClient(ctx, pairingID)
	if err != nil {
		return nil, err
	}
	response, err := client.GetSnapshot(ctx, peercontrol.GetSnapshotRequest{
		PairingID:    pairing.ID,
		AgentID:      strings.TrimSpace(request.AgentID),
		Before:       strings.TrimSpace(request.Before),
		MessageLimit: clampPeerBridgeLimit(request.MessageLimit, 30),
		RunLimit:     clampPeerBridgeLimit(request.RunLimit, 20),
	})
	if err != nil {
		return nil, peerBridgeError(err)
	}
	return json.Marshal(response)
}

// PeerSendTask forwards one instruction to a remote shared agent. The required
// audit event is written before the task leaves this instance; if audit
// persistence fails the task is not sent.
func (s *Server) PeerSendTask(ctx context.Context, request tools.PeerTaskRequest) (json.RawMessage, error) {
	pairingID := strings.TrimSpace(request.PairingID)
	agentID := strings.TrimSpace(request.AgentID)
	message := strings.TrimSpace(request.Message)
	if pairingID == "" || agentID == "" || message == "" {
		return nil, errors.New("peer task requires a pairing id, agent id, and message")
	}
	if len(message) > 256<<10 {
		return nil, errors.New("remote task message is required and must be at most 256 KiB")
	}
	pairing, client, err := s.bridgeControllerPeerClient(ctx, pairingID)
	if err != nil {
		return nil, err
	}
	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" {
		requestID = db.NewID()
	}
	localAgentID := strings.TrimSpace(request.LocalAgentID)
	// The audit agent_id column references the local agents table, so it
	// carries the local driving agent; the remote agent id lives in Details.
	if err := s.recordRequiredPeerAudit(ctx, audit.Event{
		Category: "peer", Action: "task.forward", Actor: "agent", AgentID: localAgentID,
		SubjectType: "peer_task", SubjectID: requestID, Outcome: "success", Risk: "high",
		Details: map[string]any{"pairingId": pairing.ID, "remoteAgentId": agentID, "messageDigest": digestString(message), "localAgentId": localAgentID},
	}); err != nil {
		return nil, fmt.Errorf("remote task was not sent because audit persistence failed: %w", err)
	}
	response, err := client.SendTask(ctx, peercontrol.SendTaskRequest{
		PairingID: pairing.ID, AgentID: agentID, Message: message, RequestID: requestID,
	})
	if err != nil {
		return nil, peerBridgeError(err)
	}
	return json.Marshal(response)
}

// PeerResolveApproval resolves one pending remote tool approval. The critical
// audit event is written before the decision leaves this instance; if audit
// persistence fails the decision is not sent.
func (s *Server) PeerResolveApproval(ctx context.Context, request tools.PeerApprovalRequest) (json.RawMessage, error) {
	pairingID := strings.TrimSpace(request.PairingID)
	agentID := strings.TrimSpace(request.AgentID)
	approvalID := strings.TrimSpace(request.ApprovalID)
	if pairingID == "" || agentID == "" || approvalID == "" {
		return nil, errors.New("peer approval requires a pairing id, agent id, and approval id")
	}
	decision := strings.TrimSpace(request.Decision)
	if decision != "allow_once" && decision != "allow_session" && decision != "deny" {
		return nil, errors.New("remote approval decision must be allow_once, allow_session or deny")
	}
	pairing, client, err := s.bridgeControllerPeerClient(ctx, pairingID)
	if err != nil {
		return nil, err
	}
	localAgentID := strings.TrimSpace(request.LocalAgentID)
	if err := s.recordRequiredPeerAudit(ctx, audit.Event{
		Category: "peer", Action: "approval.forward", Actor: "agent", AgentID: localAgentID,
		SubjectType: "tool_use", SubjectID: approvalID, Outcome: "success", Risk: "critical",
		Details: map[string]any{"pairingId": pairing.ID, "remoteAgentId": agentID, "decision": decision, "localAgentId": localAgentID},
	}); err != nil {
		return nil, fmt.Errorf("remote approval was not sent because audit persistence failed: %w", err)
	}
	response, err := client.ResolveApproval(ctx, peercontrol.ResolveApprovalRequest{
		PairingID: pairing.ID, AgentID: agentID, ApprovalID: approvalID, Decision: decision, Reason: strings.TrimSpace(request.Reason),
	})
	if err != nil {
		return nil, peerBridgeError(err)
	}
	return json.Marshal(response)
}
