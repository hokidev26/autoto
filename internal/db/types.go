package db

import (
	"encoding/json"
)

const (
	DefaultMessagePageLimit = 100
	MaxMessagePageLimit     = 200
)

type User struct {
	ID        string `json:"id"`
	Username  string `json:"username,omitempty"`
	Handle    string `json:"handle"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
}

type AuthSession struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	TokenHash string `json:"-"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	RevokedAt string `json:"revokedAt,omitempty"`
}

type UserAccessKey struct {
	ID         string `json:"id"`
	UserID     string `json:"userId"`
	TokenHash  string `json:"-"`
	Label      string `json:"label"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
	RevokedAt  string `json:"revokedAt,omitempty"`
}

type MessageDraft struct {
	UserID      string `json:"userId"`
	AgentID     string `json:"agentId"`
	ContentText string `json:"contentText"`
	Version     int64  `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

const (
	ProjectFlowModeWorkspace    = "workspace"
	ProjectFlowModeConversation = "conversation"
)

type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Status        string `json:"status"`
	FlowMode      string `json:"flowMode"`
	GitPath       string `json:"gitPath,omitempty"`
	RemoteURL     string `json:"remoteUrl,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Pinned        bool   `json:"pinned"`
	ArchivedAt    string `json:"archivedAt,omitempty"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

type ProjectMember struct {
	ProjectID string `json:"projectId"`
	UserID    string `json:"userId"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
}

type Workline struct {
	ID                   string `json:"id"`
	ProjectID            string `json:"projectId"`
	Title                string `json:"title"`
	Description          string `json:"description,omitempty"`
	Status               string `json:"status"`
	Role                 string `json:"role"`
	Branch               string `json:"branch,omitempty"`
	WorktreePath         string `json:"worktreePath,omitempty"`
	BaseBranch           string `json:"baseBranch,omitempty"`
	ParentWorklineID     string `json:"parentWorklineId,omitempty"`
	ForkPoint            string `json:"forkPoint,omitempty"`
	MergedIntoWorklineID string `json:"mergedIntoWorklineId,omitempty"`
	MergeCommitSHA       string `json:"mergeCommitSha,omitempty"`
	MergeStrategy        string `json:"mergeStrategy,omitempty"`
	PreMergeTargetSHA    string `json:"preMergeTargetSha,omitempty"`
	HeadCommitSHA        string `json:"headCommitSha,omitempty"`
	StartCommitSHA       string `json:"startCommitSha,omitempty"`
	IsRoot               bool   `json:"isRoot"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type Agent struct {
	ID                     string `json:"id"`
	WorklineID             string `json:"worklineId,omitempty"`
	ParentAgentID          string `json:"parentAgentId,omitempty"`
	ForkMessageID          string `json:"forkMessageId,omitempty"`
	InheritMode            string `json:"inheritMode,omitempty"`
	Type                   string `json:"type"`
	SubagentType           string `json:"subagentType,omitempty"`
	Title                  string `json:"title"`
	Model                  string `json:"model"`
	SystemPrompt           string `json:"systemPrompt,omitempty"`
	PermissionMode         string `json:"permissionMode"`
	EntityGeneration       int64  `json:"entityGeneration"`
	PermissionGeneration   int64  `json:"permissionGeneration"`
	ExecutionGeneration    int64  `json:"executionGeneration"`
	ReasoningEffort        string `json:"reasoningEffort,omitempty"`
	FastMode               bool   `json:"fastMode"`
	ExecutionDeviceID      string `json:"executionDeviceId"`
	Status                 string `json:"status"`
	PlanMode               bool   `json:"planMode"`
	Pinned                 bool   `json:"pinned"`
	ArchivedAt             string `json:"archivedAt,omitempty"`
	CWD                    string `json:"cwd,omitempty"`
	MessageCount           int    `json:"messageCount"`
	ContextSummary         string `json:"-"`
	PruneBoundaryMessageID string `json:"-"`
	PrunedPercent          int    `json:"-"`
	PruneEnabled           bool   `json:"pruneEnabled"`
	CreatedAt              string `json:"createdAt"`
	UpdatedAt              string `json:"updatedAt"`
}

type NavigationConversation struct {
	Context           string `json:"context"`
	ProjectID         string `json:"projectId"`
	ProjectName       string `json:"projectName"`
	ProjectPath       string `json:"projectPath"`
	ProjectUpdatedAt  string `json:"projectUpdatedAt"`
	ProjectPinned     bool   `json:"projectPinned"`
	ProjectArchivedAt string `json:"projectArchivedAt,omitempty"`
	WorklineID        string `json:"worklineId"`
	WorklineParentID  string `json:"worklineParentId,omitempty"`
	WorklineTitle     string `json:"worklineTitle"`
	WorklineRole      string `json:"worklineRole"`
	WorklineBranch    string `json:"worklineBranch"`
	WorklineUpdatedAt string `json:"worklineUpdatedAt"`
	AgentID           string `json:"agentId"`
	AgentTitle        string `json:"agentTitle"`
	AgentType         string `json:"agentType"`
	AgentStatus       string `json:"agentStatus"`
	AgentPinned       bool   `json:"agentPinned"`
	AgentArchivedAt   string `json:"agentArchivedAt,omitempty"`
	Model             string `json:"model"`
	PermissionMode    string `json:"permissionMode"`
	CWD               string `json:"cwd"`
	MessageCount      int    `json:"messageCount"`
	LastActivityAt    string `json:"lastActivityAt"`
}

type MessageTurnUsage struct {
	InputTokens       int64   `json:"inputTokens,omitempty"`
	OutputTokens      int64   `json:"outputTokens,omitempty"`
	CachedInputTokens int64   `json:"cachedInputTokens,omitempty"`
	ReasoningTokens   int64   `json:"reasoningTokens,omitempty"`
	TTFTMS            int64   `json:"ttftMs,omitempty"`
	DurationMS        int64   `json:"durationMs,omitempty"`
	TokensPerSecond   float64 `json:"tokensPerSecond,omitempty"`
	Estimated         bool    `json:"estimated,omitempty"`
}

// Message.ReasoningText holds the model's readable account of an assistant
// turn, when the provider exposed one. It is stored beside the answer but never
// replayed into a later request: it explains the turn, it is not part of it.
type Message struct {
	ID                    string            `json:"id"`
	AgentID               string            `json:"agentId"`
	RunID                 string            `json:"runId,omitempty"`
	Role                  string            `json:"role"`
	ContentJSON           json.RawMessage   `json:"contentJson,omitempty"`
	ProviderStateJSON     json.RawMessage   `json:"-"`
	ContentText           string            `json:"contentText"`
	ReasoningText         string            `json:"reasoningText,omitempty"`
	TurnUsage             *MessageTurnUsage `json:"turnUsage,omitempty"`
	ParentToolID          string            `json:"parentToolUseId,omitempty"`
	CommandText           string            `json:"commandText,omitempty"`
	CorrectionOfMessageID string            `json:"correctionOfMessageId,omitempty"`
	// SupersededAt is set on the messages a correction retired. They stay in the
	// transcript so the history remains readable, but are withheld from the model.
	SupersededAt    string           `json:"supersededAt,omitempty"`
	CreatedBy       string           `json:"createdBy,omitempty"`
	CompletionState string           `json:"completionState,omitempty"`
	StopReason      string           `json:"stopReason,omitempty"`
	CreatedAt       string           `json:"createdAt"`
	Attachments     []Attachment     `json:"attachments,omitempty"`
	GeneratedImages []GeneratedImage `json:"generatedImages,omitempty"`
}

type MessagePage struct {
	Messages      []Message `json:"messages"`
	HasMoreBefore bool      `json:"hasMoreBefore"`
	NextBefore    string    `json:"nextBefore,omitempty"`
}

type Run struct {
	ID                       string `json:"id"`
	AgentID                  string `json:"agentId"`
	TriggerMessageID         string `json:"triggerMessageId,omitempty"`
	Status                   string `json:"status"`
	StartedAt                string `json:"startedAt,omitempty"`
	CompletedAt              string `json:"completedAt,omitempty"`
	ErrorMessage             string `json:"errorMessage,omitempty"`
	BaseHead                 string `json:"baseHead,omitempty"`
	EndHead                  string `json:"endHead,omitempty"`
	CheckpointRepoRoot       string `json:"checkpointRepoRoot,omitempty"`
	GitSnapshotAt            string `json:"gitSnapshotAt,omitempty"`
	CheckpointState          string `json:"checkpointState"`
	CheckpointError          string `json:"checkpointError,omitempty"`
	RolledBackAt             string `json:"rolledBackAt,omitempty"`
	Source                   string `json:"source"`
	SourceID                 string `json:"sourceId,omitempty"`
	PermissionModeCap        string `json:"permissionModeCap,omitempty"`
	ExecutionGeneration      int64  `json:"executionGeneration"`
	DispatchID               string `json:"dispatchId,omitempty"`
	DurationMS               int64  `json:"durationMs,omitempty"`
	TriggerType              string `json:"triggerType"`
	ExecutionDeviceID        string `json:"executionDeviceId"`
	ExecutionMode            string `json:"executionMode"`
	PlanID                   string `json:"planId,omitempty"`
	PolicyGenerationSnapshot int64  `json:"policyGenerationSnapshot"`
	AgentGenerationSnapshot  int64  `json:"agentGenerationSnapshot"`
	ToolCatalogDigest        string `json:"toolCatalogDigest,omitempty"`
	WorkspaceFingerprint     string `json:"workspaceFingerprint,omitempty"`
	AutoContinuationMode     string `json:"autoContinuationMode"`
	ContinuationCount        int64  `json:"continuationCount"`
	ContinuationSegmentTurns int64  `json:"continuationSegmentTurns"`
	TurnCount                int64  `json:"turnCount"`
	MaxTotalTurns            int64  `json:"maxTotalTurns"`
	MaxContinuations         int64  `json:"maxContinuations"`
	MaxTotalTokens           int64  `json:"maxTotalTokens"`
	ConsumedInputTokens      int64  `json:"consumedInputTokens"`
	ConsumedOutputTokens     int64  `json:"consumedOutputTokens"`
	DeadlineAt               string `json:"deadlineAt,omitempty"`
	ResumeAfterMessageID     string `json:"resumeAfterMessageId,omitempty"`
	LastStopReason           string `json:"lastStopReason,omitempty"`
	ContinuationReason       string `json:"continuationReason,omitempty"`
	WaitingBackgroundTaskID  string `json:"waitingBackgroundTaskId,omitempty"`
	CreatedAt                string `json:"createdAt"`
	UpdatedAt                string `json:"updatedAt"`
}

const (
	RunExecutionModePlan    = "plan"
	RunExecutionModeExecute = "execute"

	RunSourceManual       = "manual"
	RunSourceConversation = "conversation"

	RunCheckpointNone        = "none"
	RunCheckpointTracking    = "tracking"
	RunCheckpointCapturing   = "capturing"
	RunCheckpointReady       = "ready"
	RunCheckpointRollingBack = "rolling_back"
	RunCheckpointInvalid     = "invalid"
	RunCheckpointRolledBack  = "rolled_back"
)

type RunGitChange struct {
	RunID               string `json:"runId"`
	Path                string `json:"path"`
	OrigPath            string `json:"origPath,omitempty"`
	IndexStatus         string `json:"indexStatus"`
	WorktreeStatus      string `json:"worktreeStatus"`
	Untracked           bool   `json:"untracked"`
	IndexFingerprint    string `json:"indexFingerprint,omitempty"`
	WorktreeFingerprint string `json:"worktreeFingerprint"`
}

type RunSummary struct {
	Run              Run   `json:"run"`
	MessageCount     int64 `json:"messageCount"`
	ToolCallCount    int64 `json:"toolCallCount"`
	PendingApprovals int64 `json:"pendingApprovals"`
	DeniedToolCalls  int64 `json:"deniedToolCalls"`
	ErrorToolCalls   int64 `json:"errorToolCalls"`
	APIRequestCount  int64 `json:"apiRequestCount"`
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	// api_requests has carried this column all along, but the run summary never
	// selected it, so the conversation details panel showed a cache figure of
	// zero for every run.
	CachedInputTokens int64               `json:"cachedInputTokens"`
	CostUSD           float64             `json:"costUsd"`
	ToolCalls         []ToolCallPreview   `json:"toolCalls"`
	RecentMessages    []RunMessagePreview `json:"recentMessages,omitempty"`
}

type ActiveRunSummary struct {
	Run              Run               `json:"run"`
	MessageCount     int64             `json:"messageCount"`
	ToolCallCount    int64             `json:"toolCallCount"`
	PendingApprovals int64             `json:"pendingApprovals"`
	ToolCalls        []ToolCallPreview `json:"toolCalls"`
}

type ToolCallPreview struct {
	ID                  string `json:"id"`
	RunID               string `json:"runId,omitempty"`
	MessageID           string `json:"messageId,omitempty"`
	ToolUseID           string `json:"toolUseId"`
	ToolName            string `json:"toolName"`
	Status              string `json:"status"`
	DurationMS          int64  `json:"durationMs,omitempty"`
	ErrorMessage        string `json:"errorMessage,omitempty"`
	PermissionDecidedBy string `json:"permissionDecidedBy,omitempty"`
	PermissionDecidedAt string `json:"permissionDecidedAt,omitempty"`
	StartedAt           string `json:"startedAt,omitempty"`
	CompletedAt         string `json:"completedAt,omitempty"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
}

type RunMessagePreview struct {
	ID           string `json:"id"`
	Role         string `json:"role"`
	ContentText  string `json:"contentText"`
	ParentToolID string `json:"parentToolUseId,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type Attachment struct {
	ID               string `json:"id"`
	MessageID        string `json:"messageId"`
	AgentID          string `json:"agentId"`
	Filename         string `json:"filename"`
	MIMEType         string `json:"mimeType"`
	Kind             string `json:"kind"`
	SizeBytes        int64  `json:"sizeBytes"`
	Data             []byte `json:"-"`
	ModelData        []byte `json:"-"`
	ModelMIME        string `json:"modelMime,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	ProcessingStatus string `json:"processingStatus,omitempty"`
	ProcessingCode   string `json:"processingCode,omitempty"`
	ProcessingError  string `json:"processingError,omitempty"`
	ExtractedText    string `json:"extractedText,omitempty"`
	CreatedAt        string `json:"createdAt"`
}

type GeneratedImage struct {
	ID            string `json:"id"`
	AgentID       string `json:"agentId"`
	MessageID     string `json:"messageId"`
	RunID         string `json:"runId,omitempty"`
	GenerationID  string `json:"generationId"`
	StorageKey    string `json:"-"`
	SHA256        string `json:"sha256"`
	MIMEType      string `json:"mimeType"`
	Filename      string `json:"filename"`
	ByteSize      int64  `json:"byteSize"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	RevisedPrompt string `json:"revisedPrompt,omitempty"`
	OutputIndex   int    `json:"outputIndex"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
}

type ToolCall struct {
	ID                       string          `json:"id"`
	AgentID                  string          `json:"agentId"`
	RunID                    string          `json:"runId,omitempty"`
	MessageID                string          `json:"messageId,omitempty"`
	ToolUseID                string          `json:"toolUseId"`
	ToolName                 string          `json:"toolName"`
	InputJSON                json.RawMessage `json:"inputJson,omitempty"`
	OutputJSON               json.RawMessage `json:"outputJson,omitempty"`
	Status                   string          `json:"status"`
	DurationMS               int64           `json:"durationMs,omitempty"`
	ErrorMessage             string          `json:"errorMessage,omitempty"`
	PermissionDecidedBy      string          `json:"permissionDecidedBy,omitempty"`
	PermissionDecidedAt      string          `json:"permissionDecidedAt,omitempty"`
	PermissionDenyMessage    string          `json:"permissionDenyMessage,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	PermissionSuggestions    string          `json:"permissionSuggestions,omitempty"`
	PermissionGeneration     int64           `json:"permissionGeneration"`
	PolicyGeneration         int64           `json:"policyGeneration"`
	ExecutionDeviceID        string          `json:"executionDeviceId"`
	StartedAt                string          `json:"startedAt,omitempty"`
	CompletedAt              string          `json:"completedAt,omitempty"`
	CreatedAt                string          `json:"createdAt"`
	UpdatedAt                string          `json:"updatedAt"`
}

type APIRequest struct {
	ID                string          `json:"id"`
	AgentID           string          `json:"agentId,omitempty"`
	RunID             string          `json:"runId,omitempty"`
	MessageID         string          `json:"messageId,omitempty"`
	Kind              string          `json:"kind"`
	Provider          string          `json:"provider,omitempty"`
	CredentialID      string          `json:"credentialId,omitempty"`
	GatewayKeyID      string          `json:"gatewayKeyId,omitempty"`
	Model             string          `json:"model,omitempty"`
	InputTokens       int64           `json:"inputTokens,omitempty"`
	OutputTokens      int64           `json:"outputTokens,omitempty"`
	CachedInputTokens int64           `json:"cachedInputTokens,omitempty"`
	ReasoningTokens   int64           `json:"reasoningTokens,omitempty"`
	TTFTMS            int64           `json:"ttftMs,omitempty"`
	DurationMS        int64           `json:"durationMs,omitempty"`
	CostUSD           float64         `json:"costUsd,omitempty"`
	ErrorMessage      string          `json:"errorMessage,omitempty"`
	RawDumpJSON       json.RawMessage `json:"rawDumpJson,omitempty"`
	StopReason        string          `json:"stopReason,omitempty"`
	TurnIndex         int64           `json:"turnIndex"`
	ContinuationIndex int64           `json:"continuationIndex"`
	CreatedAt         string          `json:"createdAt"`
}

type Backend struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey,omitempty"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type MCPServer struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	CWD       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Enabled   bool              `json:"enabled"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
}

type Skill struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Command              string          `json:"command"`
	Description          string          `json:"description"`
	Prompt               string          `json:"prompt"`
	Source               string          `json:"source"`
	Scope                string          `json:"scope"`
	ProjectID            string          `json:"projectId,omitempty"`
	WorklineID           string          `json:"worklineId,omitempty"`
	DeletedAt            string          `json:"deletedAt,omitempty"`
	RevisionNo           int64           `json:"revisionNo"`
	ContentHash          string          `json:"contentHash"`
	Enabled              bool            `json:"enabled"`
	ScanVerdict          string          `json:"scanVerdict"`
	ScanFindings         json.RawMessage `json:"scanFindings"`
	ScannerVersion       int             `json:"scannerVersion"`
	RiskAcknowledgedAt   string          `json:"riskAcknowledgedAt,omitempty"`
	RiskAcknowledgedBy   string          `json:"riskAcknowledgedBy,omitempty"`
	RiskAcknowledgedHash string          `json:"riskAcknowledgedHash,omitempty"`
	CreatedAt            string          `json:"createdAt"`
	UpdatedAt            string          `json:"updatedAt"`
}

// SkillSummary is deliberately safe for list responses: it excludes prompt
// content and finding messages, which are fetched only for an individual skill.
type SkillSummary struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Command              string `json:"command"`
	Description          string `json:"description"`
	Source               string `json:"source"`
	Scope                string `json:"scope"`
	ProjectID            string `json:"projectId,omitempty"`
	WorklineID           string `json:"worklineId,omitempty"`
	RevisionNo           int64  `json:"revisionNo"`
	ContentHash          string `json:"contentHash"`
	Enabled              bool   `json:"enabled"`
	ScanVerdict          string `json:"scanVerdict"`
	FindingCount         int    `json:"findingCount"`
	ScannerVersion       int    `json:"scannerVersion"`
	RiskAcknowledgedAt   string `json:"riskAcknowledgedAt,omitempty"`
	RiskAcknowledgedBy   string `json:"riskAcknowledgedBy,omitempty"`
	RiskAcknowledgedHash string `json:"riskAcknowledgedHash,omitempty"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type SkillScopeTarget struct {
	Scope      string `json:"scope"`
	ProjectID  string `json:"projectId,omitempty"`
	WorklineID string `json:"worklineId,omitempty"`
}

type SkillRevision struct {
	Sequence               int64           `json:"sequence"`
	ID                     string          `json:"id"`
	SkillID                string          `json:"skillId"`
	RevisionNo             int64           `json:"revisionNo"`
	Operation              string          `json:"operation"`
	Actor                  string          `json:"actor"`
	RestoredFromRevisionNo int64           `json:"restoredFromRevisionNo,omitempty"`
	Name                   string          `json:"name"`
	Command                string          `json:"command"`
	Description            string          `json:"description"`
	Prompt                 string          `json:"prompt"`
	Source                 string          `json:"source"`
	Scope                  string          `json:"scope"`
	ProjectID              string          `json:"projectId,omitempty"`
	WorklineID             string          `json:"worklineId,omitempty"`
	DeletedAt              string          `json:"deletedAt,omitempty"`
	ContentHash            string          `json:"contentHash"`
	Enabled                bool            `json:"enabled"`
	ScanVerdict            string          `json:"scanVerdict"`
	ScanFindings           json.RawMessage `json:"scanFindings"`
	ScannerVersion         int             `json:"scannerVersion"`
	RiskAcknowledgedAt     string          `json:"riskAcknowledgedAt,omitempty"`
	RiskAcknowledgedBy     string          `json:"riskAcknowledgedBy,omitempty"`
	RiskAcknowledgedHash   string          `json:"riskAcknowledgedHash,omitempty"`
	HeadCreatedAt          string          `json:"headCreatedAt"`
	HeadUpdatedAt          string          `json:"headUpdatedAt"`
	CreatedAt              string          `json:"createdAt"`
}

type SkillRevisionSummary struct {
	Sequence               int64  `json:"sequence"`
	SkillID                string `json:"skillId"`
	RevisionNo             int64  `json:"revisionNo"`
	Operation              string `json:"operation"`
	Actor                  string `json:"actor"`
	RestoredFromRevisionNo int64  `json:"restoredFromRevisionNo,omitempty"`
	ContentHash            string `json:"contentHash"`
	Enabled                bool   `json:"enabled"`
	ScanVerdict            string `json:"scanVerdict"`
	Deleted                bool   `json:"deleted"`
	CreatedAt              string `json:"createdAt"`
}

type SkillPage struct {
	Items            []SkillSummary `json:"items"`
	NextCursor       string         `json:"nextCursor,omitempty"`
	SnapshotSequence int64          `json:"snapshotSequence"`
}

type SkillRevisionPage struct {
	Items            []SkillRevisionSummary `json:"items"`
	NextCursor       string                 `json:"nextCursor,omitempty"`
	SnapshotSequence int64                  `json:"snapshotSequence"`
}

// SkillAuditEvent stores security-relevant lifecycle metadata without copying
// the prompt or scanner finding messages.
type SkillAuditEvent struct {
	ID                 string          `json:"id"`
	Action             string          `json:"action"`
	Actor              string          `json:"actor"`
	SkillID            string          `json:"skillId"`
	ContentHash        string          `json:"contentHash"`
	ScanVerdict        string          `json:"scanVerdict"`
	FindingCodes       json.RawMessage `json:"findingCodes"`
	RiskAcknowledgedAt string          `json:"riskAcknowledgedAt,omitempty"`
	CreatedAt          string          `json:"createdAt"`
}

const (
	AutomationAuditDetailsMaxBytes = 16 * 1024
	AutomationAuditMaxListLimit    = 100
)

// AutomationAuditEvent contains structured security metadata. DetailsJSON is
// deliberately limited to a small JSON object and must not contain secrets or
// raw tool input.
type AutomationAuditEvent struct {
	ID          string          `json:"id"`
	Category    string          `json:"category"`
	Action      string          `json:"action"`
	Actor       string          `json:"actor"`
	AgentID     string          `json:"agentId,omitempty"`
	RunID       string          `json:"runId,omitempty"`
	SubjectType string          `json:"subjectType,omitempty"`
	SubjectID   string          `json:"subjectId,omitempty"`
	Outcome     string          `json:"outcome"`
	Risk        string          `json:"risk"`
	DetailsJSON json.RawMessage `json:"details"`
	CreatedAt   string          `json:"createdAt"`
}

const (
	IntegrationSettingsMaxBytes   = 16 * 1024
	IntegrationSecretRefsMaxBytes = 8 * 1024
)

// IntegrationConnection stores configuration and secret references only. Secret
// values are resolved outside the database package and are never serialized.
type IntegrationConnection struct {
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	Name             string            `json:"name"`
	Enabled          bool              `json:"enabled"`
	Endpoint         string            `json:"endpoint,omitempty"`
	SettingsJSON     json.RawMessage   `json:"settings"`
	SecretRefs       map[string]string `json:"-"`
	SecretConfigured map[string]bool   `json:"secretConfigured"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt"`
}

const (
	MemoryContentMaxBytes = 16 * 1024
	MemoryMaxKeywords     = 20
	MemoryKeywordMaxRunes = 64
)

type Memory struct {
	ID string `json:"id"`
	// AgentID scopes the memory to one conversation. Empty means global.
	AgentID    string   `json:"agentId,omitempty"`
	Content    string   `json:"content"`
	Keywords   []string `json:"keywords"`
	Pinned     bool     `json:"pinned"`
	ArchivedAt string   `json:"archivedAt,omitempty"`
	CreatedAt  string   `json:"createdAt"`
	UpdatedAt  string   `json:"updatedAt"`
}

// MemoryScope selects which ownership bucket a listing covers.
const (
	MemoryScopeAll    = "all"
	MemoryScopeGlobal = "global"
	MemoryScopeAgent  = "agent"
)

type MemoryListOptions struct {
	Query           string `json:"query"`
	IncludeArchived bool   `json:"includeArchived"`
	// Scope defaults to MemoryScopeAll. MemoryScopeAgent requires AgentID.
	Scope   string `json:"scope"`
	AgentID string `json:"agentId"`
}

type NotificationSettings struct {
	ID               string `json:"id"`
	Enabled          bool   `json:"enabled"`
	WebhookURL       string `json:"webhookUrl,omitempty"`
	NotifyOnApproval bool   `json:"notifyOnApproval"`
	NotifyOnDone     bool   `json:"notifyOnDone"`
	NotifyOnError    bool   `json:"notifyOnError"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

type WorkflowPreferences struct {
	ID                           string `json:"id"`
	RequireConfirmationForExec   bool   `json:"requireConfirmationForExec"`
	RequireConfirmationForWrites bool   `json:"requireConfirmationForWrites"`
	AllowReadOnlyByDefault       bool   `json:"allowReadOnlyByDefault"`
	// DangerReflectionLevel controls how aggressively the model-in-the-loop
	// safety gate reviews Bash commands before they run without human
	// involvement. Valid values: "off", "loose", "medium", "strict".
	// "off" disables the gate entirely; "medium" is the default.
	DangerReflectionLevel string `json:"dangerReflectionLevel"`
	PolicyGeneration      int64  `json:"policyGeneration"`
	CreatedAt             string `json:"createdAt"`
	UpdatedAt             string `json:"updatedAt"`
}

type ToolPermissionRule struct {
	ID          string `json:"id"`
	Mode        string `json:"mode"`
	ToolName    string `json:"toolName"`
	Risk        string `json:"risk"`
	Decision    string `json:"decision"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

const (
	ToolExecutionGroupStatusOpen    = "open"
	ToolExecutionGroupStatusSettled = "settled"
	ToolExecutionGroupStatusAborted = "aborted"

	ToolExecutionItemStatusPending   = "pending"
	ToolExecutionItemStatusCompleted = "completed"
	ToolExecutionItemStatusError     = "error"
	ToolExecutionItemStatusDenied    = "denied"
	ToolExecutionItemStatusFailed    = "failed"
	ToolExecutionItemStatusAborted   = "aborted"
)

type ToolExecutionGroup struct {
	ID                 string              `json:"id"`
	RunID              string              `json:"runId"`
	AssistantMessageID string              `json:"assistantMessageId"`
	ExpectedCount      int                 `json:"expectedCount"`
	Status             string              `json:"status"`
	AbortReason        string              `json:"abortReason,omitempty"`
	SettledAt          string              `json:"settledAt,omitempty"`
	AbortedAt          string              `json:"abortedAt,omitempty"`
	CreatedAt          string              `json:"createdAt"`
	UpdatedAt          string              `json:"updatedAt"`
	Items              []ToolExecutionItem `json:"items"`
}

type ToolExecutionItem struct {
	GroupID   string `json:"groupId"`
	ToolUseID string `json:"toolUseId"`
	ToolName  string `json:"toolName"`
	Ordinal   int    `json:"ordinal"`
	Status    string `json:"status"`
	// ReplayClass is derived on the trusted server when the item is recorded and
	// is re-narrowed on read. Recovery uses it to decide whether an interrupted
	// call may be dispatched again; anything other than "safe" means it may not.
	ReplayClass       string          `json:"replayClass"`
	ResultMessageID   string          `json:"resultMessageId,omitempty"`
	OutputSummaryJSON json.RawMessage `json:"outputSummary,omitempty"`
	TerminalAt        string          `json:"terminalAt,omitempty"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
}

type ToolExecutionGroupCreateInput struct {
	ID                 string                   `json:"id,omitempty"`
	RunID              string                   `json:"runId"`
	AssistantMessageID string                   `json:"assistantMessageId"`
	ExpectedCount      int                      `json:"expectedCount"`
	Items              []ToolExecutionItemInput `json:"items"`
}

type ToolExecutionItemInput struct {
	ToolUseID string `json:"toolUseId"`
	ToolName  string `json:"toolName"`
	// ReplayClass must already be derived from the resolved tool and its
	// effective arguments by the caller. An empty value stores "never".
	ReplayClass string `json:"replayClass,omitempty"`
}

type ToolExecutionItemTerminalInput struct {
	ToolUseID         string          `json:"toolUseId"`
	Status            string          `json:"status"`
	ResultMessageID   string          `json:"resultMessageId"`
	OutputSummaryJSON json.RawMessage `json:"outputSummary"`
}

type ToolExecutionOutputSummary struct {
	SHA256    string `json:"sha256"`
	ByteCount int    `json:"byteCount"`
	Preview   string `json:"preview,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	IsError   bool   `json:"isError,omitempty"`
}

func (item ToolExecutionItem) Terminal() bool {
	return item.Status != "" && item.Status != ToolExecutionItemStatusPending
}
