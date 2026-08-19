package peercontrol

import (
	"encoding/json"
	"time"
)

const (
	claimEndpoint                 = "/api/peer/v1/claim"
	pollClaimEndpoint             = "/api/peer/v1/claim/poll"
	issueSessionChallengeEndpoint = "/api/peer/v1/session/challenge"
	establishSessionEndpoint      = "/api/peer/v1/session/establish"
	getSnapshotEndpoint           = "/api/peer/v1/snapshot"
	sendTaskEndpoint              = "/api/peer/v1/tasks"
	updateAgentRuntimeEndpoint    = "/api/peer/v1/agents/runtime"
	resolveApprovalEndpoint       = "/api/peer/v1/approvals/resolve"
	executionHeartbeatEndpoint    = "/api/peer/v1/execution/heartbeat"
	executionClaimEndpoint        = "/api/peer/v1/execution/claim"
	executionReportEndpoint       = "/api/peer/v1/execution/report"
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

// SnapshotAgent is the bounded agent summary shared with a controller. It
// includes the current model id and reasoning effort so the controller
// composer can show the same chips as a local chat. It never includes the
// model system prompt, workspace path, provider credentials, or local
// execution-device details.
type SnapshotAgent struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Model             string  `json:"model,omitempty"`
	ReasoningEffort   string  `json:"reasoningEffort,omitempty"`
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

// SnapshotMessage is a bounded conversation projection. ParentToolUseID is
// the tool-call id a result belongs to, not raw tool input or output.
type SnapshotMessage struct {
	ID              string `json:"id"`
	RunID           string `json:"runId,omitempty"`
	ParentToolUseID string `json:"parentToolUseId,omitempty"`
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

// SnapshotApproval is the safe projection needed for an allow_once,
// allow_session, or deny decision. InputJSON and raw Bash commands are
// deliberately absent.
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

// UpdateAgentRuntimeRequest changes the host conversation's model, thinking
// strength, or permission mode. At least one of Model, ReasoningEffort, or
// PermissionMode must be set. PermissionMode is clamped to the grant cap on
// the host; bypassPermissions is never accepted over this channel.
type UpdateAgentRuntimeRequest struct {
	PairingID       string `json:"pairingId"`
	AgentID         string `json:"agentId"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	PermissionMode  string `json:"permissionMode,omitempty"`
}

// UpdateAgentRuntimeResponse echoes the host values after a successful patch.
// It is a new endpoint, so these fields do not ship on GetSnapshotResponse.
type UpdateAgentRuntimeResponse struct {
	AgentID           string `json:"agentId"`
	Model             string `json:"model"`
	ReasoningEffort   string `json:"reasoningEffort"`
	PermissionMode    string `json:"permissionMode"`
	PermissionModeCap string `json:"permissionModeCap"`
}

// ResolveApprovalRequest is the fixed approval decision shape. Decision is
// allow_once, allow_session (requires the approve_session scope), or deny.
type ResolveApprovalRequest struct {
	PairingID  string `json:"pairingId"`
	AgentID    string `json:"agentId"`
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason,omitempty"`
}

// ResolveApprovalResponse reports the final approval state. AppliedDecision
// echoes the decision the host actually applied: an allow_session request
// degrades to allow_once when that tool call cannot carry a session grant.
type ResolveApprovalResponse struct {
	ApprovalID      string    `json:"approvalId"`
	Status          string    `json:"status"`
	AppliedDecision string    `json:"appliedDecision,omitempty"`
	ResolvedAt      time.Time `json:"resolvedAt"`
}

// ExecutionHeartbeatRequest reports that a remote execution device is alive and
// how it currently sees itself. DeviceID is the host-side registration the
// device claims to be; the host only accepts it when the registration is bound
// to this pairing's identity fingerprint.
type ExecutionHeartbeatRequest struct {
	PairingID string `json:"pairingId"`
	DeviceID  string `json:"deviceId"`
	Status    string `json:"status,omitempty"`
}

// ExecutionHeartbeatResponse returns the device state the host recorded, plus
// the bounds a polling device needs in order to pace itself.
type ExecutionHeartbeatResponse struct {
	ProtocolVersion  int    `json:"protocolVersion"`
	DeviceID         string `json:"deviceId"`
	Status           string `json:"status"`
	QueuedTasks      int    `json:"queuedTasks"`
	LeaseMaxSeconds  int    `json:"leaseMaxSeconds"`
	HeartbeatSeconds int    `json:"heartbeatSeconds"`
}

// ExecutionClaimRequest asks for the next queued task for a device. The host
// only ever hands over work for agents this pairing was granted.
type ExecutionClaimRequest struct {
	PairingID    string `json:"pairingId"`
	DeviceID     string `json:"deviceId"`
	LeaseSeconds int    `json:"leaseSeconds,omitempty"`
}

// ExecutionTask is the leased unit of work. It carries the ledger revision the
// device must echo back when reporting, and the permission cap the host's grant
// imposes, so the device cannot widen its own authority.
type ExecutionTask struct {
	TaskID            string          `json:"taskId"`
	ProjectID         string          `json:"projectId"`
	AgentID           string          `json:"agentId"`
	RunID             string          `json:"runId,omitempty"`
	Payload           json.RawMessage `json:"payload"`
	PermissionModeCap string          `json:"permissionModeCap"`
	Revision          int64           `json:"revision"`
	AttemptCount      int             `json:"attemptCount"`
	LeaseUntil        time.Time       `json:"leaseUntil"`
}

// ExecutionClaimResponse carries the leased task, or no task when the queue is
// empty. An empty queue is a normal answer rather than an error.
type ExecutionClaimResponse struct {
	ProtocolVersion int            `json:"protocolVersion"`
	Task            *ExecutionTask `json:"task,omitempty"`
}

// ExecutionReportRequest advances a leased task. Revision is the ledger revision
// the device received, which makes every report a compare-and-swap instead of a
// blind overwrite of whatever the host currently holds.
type ExecutionReportRequest struct {
	PairingID string          `json:"pairingId"`
	DeviceID  string          `json:"deviceId"`
	TaskID    string          `json:"taskId"`
	Revision  int64           `json:"revision"`
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// ExecutionReportResponse returns the ledger state the host committed.
type ExecutionReportResponse struct {
	ProtocolVersion int       `json:"protocolVersion"`
	TaskID          string    `json:"taskId"`
	Status          string    `json:"status"`
	Revision        int64     `json:"revision"`
	ReportedAt      time.Time `json:"reportedAt"`
}
