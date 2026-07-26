package peercontrol

import "time"

const (
	claimEndpoint                 = "/api/peer/v1/claim"
	pollClaimEndpoint             = "/api/peer/v1/claim/poll"
	issueSessionChallengeEndpoint = "/api/peer/v1/session/challenge"
	establishSessionEndpoint      = "/api/peer/v1/session/establish"
	getSnapshotEndpoint           = "/api/peer/v1/snapshot"
	sendTaskEndpoint              = "/api/peer/v1/tasks"
	resolveApprovalEndpoint       = "/api/peer/v1/approvals/resolve"
)

// ClaimRequest submits the controller's signed invitation claim proof.
type ClaimRequest struct {
	Proof  SignedPairingClaim `json:"proof"`
	Secret string             `json:"secret"`
}

// ClaimResponse reports the host's current invitation state.
type ClaimResponse struct {
	ProtocolVersion int    `json:"protocolVersion"`
	InvitationID    string `json:"invitationId"`
	Status          string `json:"status"`
	Revision        int64  `json:"revision"`
}

// PollClaimRequest carries a fresh signed proof from the same controller that
// claimed the invitation. Invitation IDs alone are never polling credentials.
type PollClaimRequest struct {
	Proof SignedPairingClaim `json:"proof"`
}

// PollClaimResponse returns approval state and, once approved, the controller
// pairing details needed for session establishment.
type PollClaimResponse struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	InvitationID       string         `json:"invitationId"`
	Status             string         `json:"status"`
	Revision           int64          `json:"revision"`
	PairingID          string         `json:"pairingId,omitempty"`
	EndpointOrigin     string         `json:"endpointOrigin,omitempty"`
	HostIdentity       PublicIdentity `json:"hostIdentity,omitempty"`
	HostInstallationID string         `json:"hostInstallationId,omitempty"`
	HostDisplayName    string         `json:"hostDisplayName,omitempty"`
	Scopes             []Scope        `json:"scopes,omitempty"`
	ExpiresAt          time.Time      `json:"expiresAt,omitempty"`
}

// IssueSessionChallengeRequest asks for a one-time pairing challenge.
type IssueSessionChallengeRequest struct {
	PairingID string `json:"pairingId"`
}

// IssueSessionChallengeResponse carries the host-issued challenge.
type IssueSessionChallengeResponse struct {
	Challenge SessionChallenge `json:"challenge"`
}

// EstablishSessionRequest carries the controller-signed one-time challenge.
type EstablishSessionRequest struct {
	PairingID       string          `json:"pairingId"`
	SignedChallenge SignedChallenge `json:"signedChallenge"`
}

// EstablishSessionResponse returns the short-lived dedicated peer bearer.
type EstablishSessionResponse struct {
	BearerToken string    `json:"bearerToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// GetSnapshotRequest identifies the paired host snapshot being requested.
type GetSnapshotRequest struct {
	PairingID    string `json:"pairingId"`
	AgentID      string `json:"agentId,omitempty"`
	Before       string `json:"before,omitempty"`
	MessageLimit int    `json:"messageLimit,omitempty"`
	RunLimit     int    `json:"runLimit,omitempty"`
}

// SnapshotAgent is the bounded agent summary shared with a controller. It never
// includes the model system prompt, workspace path, provider identity, or local
// execution-device details.
type SnapshotAgent struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	PermissionModeCap string  `json:"permissionModeCap"`
	Status            string  `json:"status"`
	PlanMode          bool    `json:"planMode"`
	MessageCount      int     `json:"messageCount"`
	Scopes            []Scope `json:"scopes"`
	UpdatedAt         string  `json:"updatedAt"`
}

// SnapshotProject is the bounded project summary shared with a controller.
type SnapshotProject struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Status string          `json:"status"`
	Agents []SnapshotAgent `json:"agents"`
}

// SnapshotMessage is a bounded conversation projection.
type SnapshotMessage struct {
	ID              string `json:"id"`
	RunID           string `json:"runId,omitempty"`
	Role            string `json:"role"`
	ContentText     string `json:"contentText"`
	CompletionState string `json:"completionState,omitempty"`
	StopReason      string `json:"stopReason,omitempty"`
	CreatedAt       string `json:"createdAt"`
}

// SnapshotRun is a bounded run projection without repository paths, provider
// data, workspace fingerprints, or raw tool input/output.
type SnapshotRun struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Source      string `json:"source"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	DurationMS  int64  `json:"durationMs,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// SnapshotApproval is the safe projection needed for an allow-once or deny
// decision. InputJSON and raw Bash commands are deliberately absent.
type SnapshotApproval struct {
	ApprovalID           string `json:"approvalId"`
	AgentID              string `json:"agentId"`
	RunID                string `json:"runId,omitempty"`
	ToolName             string `json:"toolName"`
	Risk                 string `json:"risk"`
	Reason               string `json:"reason,omitempty"`
	Warning              string `json:"warning,omitempty"`
	PermissionGeneration int64  `json:"permissionGeneration"`
	PolicyGeneration     int64  `json:"policyGeneration"`
	CreatedAt            string `json:"createdAt"`
}

// SnapshotAgentState contains bounded detail only for the explicitly requested
// and authorized agent.
type SnapshotAgentState struct {
	AgentID          string             `json:"agentId"`
	Messages         []SnapshotMessage  `json:"messages"`
	HasMoreMessages  bool               `json:"hasMoreMessages"`
	NextBefore       string             `json:"nextBefore,omitempty"`
	Runs             []SnapshotRun      `json:"runs"`
	PendingApprovals []SnapshotApproval `json:"pendingApprovals"`
}

// GetSnapshotResponse is a versioned remote collaboration snapshot.
type GetSnapshotResponse struct {
	ProtocolVersion    int                 `json:"protocolVersion"`
	PairingID          string              `json:"pairingId"`
	CredentialRevision int64               `json:"credentialRevision"`
	GrantRevision      int64               `json:"grantRevision"`
	GeneratedAt        time.Time           `json:"generatedAt"`
	Projects           []SnapshotProject   `json:"projects"`
	SelectedAgent      *SnapshotAgentState `json:"selectedAgent,omitempty"`
}

// SendTaskRequest is the fixed remote task submission shape.
type SendTaskRequest struct {
	PairingID string `json:"pairingId"`
	AgentID   string `json:"agentId"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

// SendTaskResponse identifies the accepted remote task.
type SendTaskResponse struct {
	TaskID    string    `json:"taskId"`
	RequestID string    `json:"requestId"`
	AgentID   string    `json:"agentId"`
	MessageID string    `json:"messageId"`
	RunID     string    `json:"runId"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// ResolveApprovalRequest is the fixed one-time approval decision shape.
type ResolveApprovalRequest struct {
	PairingID  string `json:"pairingId"`
	AgentID    string `json:"agentId"`
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason,omitempty"`
}

// ResolveApprovalResponse reports the final approval state.
type ResolveApprovalResponse struct {
	ApprovalID string    `json:"approvalId"`
	Status     string    `json:"status"`
	ResolvedAt time.Time `json:"resolvedAt"`
}
