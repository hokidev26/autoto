package tools

import (
	"context"
	"encoding/json"
)

// PeerPairingSummary describes an active controller-side pairing the local
// agent may drive. It deliberately carries no credentials or endpoint origin;
// the service resolves those internally per pairing id.
type PeerPairingSummary struct {
	PairingID   string   `json:"pairingId"`
	DisplayName string   `json:"displayName"`
	Fingerprint string   `json:"peerFingerprint"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
	LastSeenAt  string   `json:"lastSeenAt,omitempty"`
}

// PeerSnapshotRequest asks the remote peer for its shared projects/agents and,
// when AgentID is set, that agent's recent messages, runs and pending
// approvals. Limits of zero let the service apply its defaults.
type PeerSnapshotRequest struct {
	PairingID    string
	AgentID      string
	Before       string
	MessageLimit int
	RunLimit     int
}

// PeerTaskRequest forwards one instruction to a remote shared agent.
// LocalAgentID identifies the local agent driving the peer so the audit trail
// distinguishes agent-initiated forwards from UI-initiated ones.
type PeerTaskRequest struct {
	PairingID    string
	AgentID      string
	Message      string
	RequestID    string
	LocalAgentID string
}

// PeerApprovalRequest resolves one pending tool approval on the remote peer.
// Decision must be "allow_once" or "deny"; the service validates it again.
type PeerApprovalRequest struct {
	PairingID    string
	AgentID      string
	ApprovalID   string
	Decision     string
	Reason       string
	LocalAgentID string
}

// PeerCollaborationService is the runner-facing bridge into the peer control
// channel. internal/server implements it over the authenticated peer client;
// keeping the interface here avoids a tools -> server import cycle, exactly
// like BackgroundTaskService. Responses are the peer protocol JSON bodies
// (already redacted and size-bounded by the host before they leave the peer),
// passed through raw so tools do not re-model the protocol.
type PeerCollaborationService interface {
	ListPeerPairings(ctx context.Context) ([]PeerPairingSummary, error)
	PeerSnapshot(ctx context.Context, request PeerSnapshotRequest) (json.RawMessage, error)
	PeerSendTask(ctx context.Context, request PeerTaskRequest) (json.RawMessage, error)
	PeerResolveApproval(ctx context.Context, request PeerApprovalRequest) (json.RawMessage, error)
}
