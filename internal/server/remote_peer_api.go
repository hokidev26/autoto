package server

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	agentpkg "autoto/internal/agent"
	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/peercontrol"
	"autoto/internal/tools"
)

const (
	remotePeerMaxClaimNonces     = 4096
	remotePeerTaskResultTTL      = 24 * time.Hour
	remotePeerMessageTextBytes   = 16 << 10
	remotePeerApprovalTextBytes  = 2 << 10
	remotePeerMaxTaskMessageSize = 120 << 10
	remotePeerPairingMaxAttempts = 5
	remotePeerPairingLockTTL     = 5 * time.Minute
)

// remotePeerTaskRecord remains an alias because remoteCollaborationRuntime is
// initialized in remote_collaboration.go with the protocol response map type.
// Cache keys below bind the response to pairing, request, agent, and digest.
type remotePeerTaskRecord = peercontrol.SendTaskResponse

type peerAuthorizedRequest struct {
	session peercontrol.AuthenticatedSession
	pairing db.RemotePeerPairing
	grants  []db.RemotePeerGrant
	grant   db.RemotePeerGrant
}

func (s *Server) mountPeerAPIRoutes(router chi.Router) {
	router.Route("/api/peer/v1", func(router chi.Router) {
		router.Post("/claim", s.peerClaim)
		router.Post("/claim/poll", s.peerClaimPoll)
		router.Post("/session/challenge", s.peerSessionChallenge)
		router.Post("/session/establish", s.peerSessionEstablish)
		router.Post("/snapshot", s.peerSnapshot)
		router.Post("/tasks", s.peerSendTask)
		router.Post("/agents/runtime", s.peerUpdateAgentRuntime)
		router.Post("/approvals/resolve", s.peerResolveApproval)
		router.Post("/execution/heartbeat", s.peerExecutionHeartbeat)
		router.Post("/execution/claim", s.peerExecutionClaim)
		router.Post("/execution/report", s.peerExecutionReport)
	})
}

func (s *Server) peerClaim(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	if !runtime.manager.SharingEnabled() {
		writePeerControlError(w, peercontrol.ErrDisabled)
		return
	}
	var request peercontrol.ClaimRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid peer request")
		return
	}
	claim, err := peercontrol.VerifyPairingClaim(request.Proof, s.now().UTC())
	if err != nil || !runtime.acceptPeerClaimNonce(claim.Nonce, s.now().UTC()) {
		writeError(w, http.StatusUnauthorized, "peer claim authentication failed")
		return
	}
	invitation, err := s.store.GetRemotePairingInvitation(r.Context(), claim.InvitationID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "peer claim authentication failed")
		return
	}

	secret, secretErr := peercontrol.DecodeInvitationSecretToken(request.Secret)
	digest := peercontrol.HashInvitationSecret(secret)
	proofDigest, proofErr := hex.DecodeString(claim.SecretHash)
	storedDigest, storedErr := hex.DecodeString(invitation.CodeHash)
	secretMatches := secretErr == nil && proofErr == nil && storedErr == nil &&
		len(proofDigest) == len(digest) && len(storedDigest) == len(digest) &&
		subtle.ConstantTimeCompare(digest[:], proofDigest) == 1 &&
		subtle.ConstantTimeCompare(digest[:], storedDigest) == 1
	if !secretMatches || invitation.ProtocolVersion != claim.ProtocolVersion {
		if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
			Category: "peer", Action: "claim.verify", Actor: peerActor(claim.Fingerprint), SubjectType: "peer_invitation", SubjectID: claim.InvitationID,
			Outcome: "denied", Risk: "high", Details: map[string]any{"reason": "claim_verification_failed"},
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "peer claim was rejected because audit persistence failed")
			return
		}
		if recordErr := s.recordPeerInvitationFailure(r, invitation); recordErr != nil && !db.IsConflict(recordErr) {
			writeError(w, http.StatusInternalServerError, "peer claim failed")
			return
		}
		writeError(w, http.StatusUnauthorized, "peer claim authentication failed")
		return
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "claim.accept", Actor: peerActor(claim.Fingerprint), SubjectType: "peer_invitation", SubjectID: claim.InvitationID,
		Outcome: "success", Risk: "high", Details: map[string]any{"requesterFingerprintPrefix": fingerprintPrefix(claim.Fingerprint)},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "peer claim was not accepted because audit persistence failed")
		return
	}
	claimed, err := s.store.ClaimRemotePairingInvitation(r.Context(), invitation.ID, hex.EncodeToString(digest[:]), invitation.Revision, db.RemotePairingRequester{
		DisplayName: claim.DisplayName, InstallationID: claim.InstallationID, PublicKey: claim.PublicKey, Fingerprint: claim.Fingerprint,
	})
	if err != nil {
		if db.IsConflict(err) {
			writeError(w, http.StatusConflict, "peer claim conflicts with current invitation state")
		} else {
			writeError(w, http.StatusInternalServerError, "peer claim failed")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, peercontrol.ClaimResponse{
		ProtocolVersion: peercontrol.ProtocolVersion, InvitationID: claimed.ID, Status: claimed.Status, Revision: claimed.Revision,
	})
}

func (s *Server) recordPeerInvitationFailure(r *http.Request, invitation db.RemotePairingInvitation) error {
	now := s.now().UTC()
	expiresAt, err := time.Parse(time.RFC3339Nano, invitation.ExpiresAt)
	if err != nil || !now.Before(expiresAt) {
		return nil
	}
	lockedUntil := now.Add(remotePeerPairingLockTTL)
	if expiresAt.Before(lockedUntil) {
		lockedUntil = expiresAt
	}
	if !now.Before(lockedUntil) {
		return nil
	}
	_, err = s.store.RecordRemotePairingInvitationFailure(r.Context(), invitation.ID, invitation.Revision, remotePeerPairingMaxAttempts, lockedUntil.Format(time.RFC3339Nano))
	return err
}

func (s *Server) peerClaimPoll(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	if !runtime.manager.SharingEnabled() {
		writePeerControlError(w, peercontrol.ErrDisabled)
		return
	}
	var request peercontrol.PollClaimRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid peer request")
		return
	}
	claim, err := peercontrol.VerifyPairingClaim(request.Proof, s.now().UTC())
	if err != nil || !runtime.acceptPeerClaimNonce(claim.Nonce, s.now().UTC()) {
		writeError(w, http.StatusUnauthorized, "peer claim authentication failed")
		return
	}
	invitation, err := s.store.GetRemotePairingInvitation(r.Context(), claim.InvitationID)
	if err != nil || !peerClaimMatchesRequester(claim, invitation) || !constantStringBytesEqual(claim.SecretHash, invitation.CodeHash) {
		writeError(w, http.StatusUnauthorized, "peer claim authentication failed")
		return
	}
	response := peercontrol.PollClaimResponse{
		ProtocolVersion: peercontrol.ProtocolVersion, InvitationID: invitation.ID, Status: invitation.Status, Revision: invitation.Revision,
	}
	if invitation.Status != db.RemotePairingInvitationStatusApproved {
		writeJSON(w, http.StatusOK, response)
		return
	}
	pairing, err := s.store.GetRemotePeerPairing(r.Context(), invitation.ID)
	if err != nil || pairing.LocalRole != db.RemotePeerLocalRoleHost || pairing.Status != db.RemotePeerPairingStatusActive || remotePairingExpired(pairing, s.now()) ||
		!constantStringBytesEqual(pairing.PeerPublicKey, invitation.RequesterPublicKey) || !constantStringBytesEqual(pairing.PeerFingerprint, invitation.RequesterFingerprint) {
		writeError(w, http.StatusGone, "peer pairing is inactive")
		return
	}
	origin, err := s.peerRequestOrigin(r)
	if err != nil {
		writeError(w, http.StatusForbidden, "peer protocol requires HTTPS")
		return
	}
	settings, err := s.store.GetRuntimeSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "peer claim polling failed")
		return
	}
	scopes, err := peercontrol.NormalizeScopes(pairing.Scopes)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "peer pairing authorization is invalid")
		return
	}
	var expiresAt time.Time
	if pairing.ExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339Nano, pairing.ExpiresAt)
		if err != nil || !s.now().UTC().Before(expiresAt) {
			writeError(w, http.StatusGone, "peer pairing is inactive")
			return
		}
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "claim.poll_approved", Actor: peerActor(claim.Fingerprint), SubjectType: "peer_pairing", SubjectID: pairing.ID,
		Outcome: "success", Risk: "high", Details: map[string]any{"endpointOrigin": origin},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "peer credentials were not returned because audit persistence failed")
		return
	}
	response.PairingID = pairing.ID
	response.EndpointOrigin = origin
	response.HostIdentity = runtime.manager.Identity()
	response.HostInstallationID = settings.InstallationID
	response.HostDisplayName = "Autoto"
	response.Scopes = scopes
	response.ExpiresAt = expiresAt
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) peerSessionChallenge(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	var request peercontrol.IssueSessionChallengeRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid peer request")
		return
	}
	origin, err := s.peerRequestOrigin(r)
	if err != nil {
		writeError(w, http.StatusForbidden, "peer protocol requires HTTPS")
		return
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "session.challenge", Actor: "peer", SubjectType: "peer_pairing", SubjectID: strings.TrimSpace(request.PairingID),
		Outcome: "success", Risk: "high", Details: map[string]any{"endpointOrigin": origin},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "peer session challenge was not issued because audit persistence failed")
		return
	}
	challenge, err := runtime.manager.IssueSessionChallengeForOrigin(r.Context(), strings.TrimSpace(request.PairingID), origin)
	if err != nil {
		writePeerControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, peercontrol.IssueSessionChallengeResponse{Challenge: challenge})
}

func (s *Server) peerSessionEstablish(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	var request peercontrol.EstablishSessionRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid peer request")
		return
	}
	origin, err := s.peerRequestOrigin(r)
	if err != nil || !constantStringBytesEqual(origin, request.SignedChallenge.EndpointOrigin) {
		writeError(w, http.StatusUnauthorized, "peer session authentication failed")
		return
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "session.establish", Actor: peerActor(request.SignedChallenge.SignerFingerprint), SubjectType: "peer_pairing", SubjectID: strings.TrimSpace(request.PairingID),
		Outcome: "success", Risk: "critical", Details: map[string]any{"endpointOrigin": origin},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "peer session was not established because audit persistence failed")
		return
	}
	credential, err := runtime.manager.EstablishSession(r.Context(), strings.TrimSpace(request.PairingID), request.SignedChallenge)
	if err != nil {
		writePeerControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, peercontrol.EstablishSessionResponse{BearerToken: credential.BearerToken, ExpiresAt: credential.ExpiresAt})
}

func (s *Server) peerSnapshot(w http.ResponseWriter, r *http.Request) {
	var request peercontrol.GetSnapshotRequest
	authorized, ok := s.authorizePeerRequest(w, r, &request, peercontrol.ScopeObserve, "")
	if !ok {
		return
	}
	messageLimit := request.MessageLimit
	if messageLimit == 0 {
		messageLimit = 30
	}
	runLimit := request.RunLimit
	if runLimit == 0 {
		runLimit = 20
	}
	if messageLimit < 1 || messageLimit > 100 || runLimit < 1 || runLimit > 100 {
		writeError(w, http.StatusBadRequest, "invalid peer snapshot limits")
		return
	}

	projects := make([]peercontrol.SnapshotProject, 0)
	projectIndexes := make(map[string]int)
	var selectedGrant *db.RemotePeerGrant
	for _, grant := range authorized.grants {
		if grant.Revision != authorized.session.GrantRevision || !scopeStringsContain(grant.Scopes, peercontrol.ScopeObserve) {
			continue
		}
		project, agent, valid := s.loadPeerGrantedAgent(r, grant)
		if !valid {
			writeError(w, http.StatusUnauthorized, "peer grant authorization is invalid")
			return
		}
		scopes, err := peercontrol.NormalizeScopes(grant.Scopes)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "peer grant authorization is invalid")
			return
		}
		index, exists := projectIndexes[project.ID]
		if !exists {
			index = len(projects)
			projectIndexes[project.ID] = index
			projects = append(projects, peercontrol.SnapshotProject{ID: project.ID, Name: project.Name, Status: project.Status, Agents: []peercontrol.SnapshotAgent{}})
		}
		projects[index].Agents = append(projects[index].Agents, peercontrol.SnapshotAgent{
			ID: agent.ID, Name: agent.Title, Model: boundedPeerText(agent.Model, 128), ReasoningEffort: boundedPeerText(agent.ReasoningEffort, 32),
			PermissionModeCap: grant.PermissionModeCap, Status: agent.Status, PlanMode: agent.PlanMode,
			MessageCount: agent.MessageCount, Scopes: scopes, UpdatedAt: agent.UpdatedAt,
		})
		if request.AgentID != "" && agent.ID == request.AgentID {
			copy := grant
			selectedGrant = &copy
		}
	}
	if request.AgentID != "" && selectedGrant == nil {
		writeError(w, http.StatusForbidden, "peer grant does not authorize this agent")
		return
	}
	response := peercontrol.GetSnapshotResponse{
		ProtocolVersion: peercontrol.ProtocolVersion, PairingID: authorized.pairing.ID,
		CredentialRevision: authorized.session.CredentialRevision, GrantRevision: authorized.session.GrantRevision,
		GeneratedAt: s.now().UTC(), Projects: projects,
	}
	if selectedGrant != nil {
		state, err := s.peerSnapshotAgentState(r, authorized, *selectedGrant, request, messageLimit, runLimit)
		if err != nil {
			if db.IsNotFound(err) {
				writeError(w, http.StatusNotFound, "peer snapshot agent data was not found")
			} else {
				writeError(w, http.StatusBadRequest, "peer snapshot could not be generated")
			}
			return
		}
		response.SelectedAgent = &state
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "snapshot.read", Actor: peerActor(authorized.pairing.PeerFingerprint), AgentID: request.AgentID,
		SubjectType: "peer_pairing", SubjectID: authorized.pairing.ID, Outcome: "success", Risk: "medium",
		Details: map[string]any{"projectCount": len(projects), "selectedAgent": request.AgentID != ""},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "peer snapshot was not returned because audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) peerSnapshotAgentState(r *http.Request, authorized peerAuthorizedRequest, grant db.RemotePeerGrant, request peercontrol.GetSnapshotRequest, messageLimit, runLimit int) (peercontrol.SnapshotAgentState, error) {
	page, err := s.store.ListMessagesPage(r.Context(), request.AgentID, strings.TrimSpace(request.Before), messageLimit)
	if err != nil {
		return peercontrol.SnapshotAgentState{}, err
	}
	messages := make([]peercontrol.SnapshotMessage, 0, len(page.Messages))
	for _, message := range page.Messages {
		messages = append(messages, peercontrol.SnapshotMessage{
			ID: message.ID, RunID: message.RunID, Role: message.Role,
			ParentToolUseID: boundedPeerText(message.ParentToolID, 128),
			ContentText:     boundedPeerText(agentpkg.RedactToolActivityText(message.ContentText), remotePeerMessageTextBytes),
			CompletionState: boundedPeerText(agentpkg.RedactToolActivityText(message.CompletionState), 256),
			StopReason:      boundedPeerText(agentpkg.RedactToolActivityText(message.StopReason), remotePeerApprovalTextBytes),
			CreatedAt:       message.CreatedAt,
		})
	}
	runs, err := s.store.ListRuns(r.Context(), request.AgentID, runLimit)
	if err != nil {
		return peercontrol.SnapshotAgentState{}, err
	}
	projectedRuns := make([]peercontrol.SnapshotRun, 0, len(runs))
	for _, run := range runs {
		projectedRuns = append(projectedRuns, peercontrol.SnapshotRun{
			ID: run.ID, Status: run.Status, Source: run.Source, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
			DurationMS: run.DurationMS, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		})
	}
	approvals := make([]peercontrol.SnapshotApproval, 0)
	if scopeContains(authorized.session.Scopes, peercontrol.ScopeApproveOnce) && scopeStringsContain(grant.Scopes, peercontrol.ScopeApproveOnce) {
		calls, err := s.store.ListPendingToolCalls(r.Context(), request.AgentID)
		if err != nil {
			return peercontrol.SnapshotAgentState{}, err
		}
		registry := s.toolRegistrySnapshot()
		for _, call := range calls {
			risk := tools.RiskDanger
			if tool, found := registry.Get(call.ToolName); found {
				risk = tool.Risk(call.InputJSON)
			}
			approvals = append(approvals, peercontrol.SnapshotApproval{
				ApprovalID: call.ToolUseID, AgentID: call.AgentID, RunID: call.RunID, ToolName: call.ToolName, Risk: string(risk),
				Reason:               boundedPeerText(agentpkg.RedactToolActivityText(call.PermissionDecisionReason), remotePeerApprovalTextBytes),
				Warning:              boundedPeerText(agentpkg.RedactToolActivityText(call.PermissionDenyMessage), remotePeerApprovalTextBytes),
				PermissionGeneration: call.PermissionGeneration, PolicyGeneration: call.PolicyGeneration, CreatedAt: call.CreatedAt,
			})
		}
	}
	return peercontrol.SnapshotAgentState{
		AgentID: request.AgentID, Messages: messages, HasMoreMessages: page.HasMoreBefore, NextBefore: page.NextBefore,
		Runs: projectedRuns, PendingApprovals: approvals,
	}, nil
}

func (s *Server) peerSendTask(w http.ResponseWriter, r *http.Request) {
	var request peercontrol.SendTaskRequest
	authorized, ok := s.authorizePeerRequest(w, r, &request, peercontrol.ScopeSendTask, request.AgentID)
	if !ok {
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.AgentID = strings.TrimSpace(request.AgentID)
	if request.Message == "" || len(request.Message) > remotePeerMaxTaskMessageSize || !validPeerRequestID(request.RequestID) {
		writeError(w, http.StatusBadRequest, "invalid peer task request")
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "peer task execution is unavailable")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), request.AgentID)
	if err != nil {
		writeError(w, http.StatusForbidden, "peer grant does not authorize this agent")
		return
	}
	digest := digestString(request.Message)
	cached, state := s.peerControl.beginPeerTask(authorized.pairing.ID, request.RequestID, request.AgentID, digest, s.now().UTC())
	switch state {
	case peerTaskCacheHit:
		writeJSON(w, http.StatusAccepted, cached)
		return
	case peerTaskCacheConflict:
		writeError(w, http.StatusConflict, "peer task request id conflicts with an existing request")
		return
	}
	cacheKey := peerTaskCacheKey(authorized.pairing.ID, request.RequestID, request.AgentID, digest)
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "task.submit", Actor: peerActor(authorized.pairing.PeerFingerprint), AgentID: request.AgentID,
		SubjectType: "peer_task", SubjectID: request.RequestID, Outcome: "success", Risk: "high",
		Details: map[string]any{"pairingId": authorized.pairing.ID, "messageDigest": digest},
	}); err != nil {
		s.peerControl.abortPeerTask(cacheKey)
		writeError(w, http.StatusInternalServerError, "peer task was not executed because audit persistence failed")
		return
	}
	mode := agentpkg.ExecutionModeExecute
	if agent.PlanMode {
		mode = agentpkg.ExecutionModePlan
	}
	message, err := s.runner.SubmitUserMessageWithModeAndPermissionCap(r.Context(), request.AgentID, request.Message, peerActor(authorized.pairing.PeerFingerprint), mode, authorized.grant.PermissionModeCap)
	if err != nil {
		s.peerControl.abortPeerTask(cacheKey)
		writeError(w, http.StatusInternalServerError, "peer task execution failed")
		return
	}
	response := peercontrol.SendTaskResponse{
		TaskID: cached.TaskID, RequestID: request.RequestID, AgentID: request.AgentID, MessageID: message.ID, RunID: message.RunID,
		Status: "accepted", CreatedAt: cached.CreatedAt,
	}
	s.peerControl.completePeerTask(cacheKey, response)
	writeJSON(w, http.StatusAccepted, response)
}

func clampPeerGrantedPermissionMode(requested, cap string) (string, error) {
	mode := strings.TrimSpace(requested)
	if mode == "dontAsk" {
		mode = "default"
	}
	if !validPermissionMode(mode) {
		return "", errors.New("invalid permission mode")
	}
	if mode == "bypassPermissions" {
		return "", errors.New("peer grant does not allow this permission mode")
	}
	switch strings.TrimSpace(cap) {
	case db.RemotePeerPermissionModeReadOnly:
		if mode != "readOnly" {
			return "", errors.New("peer grant does not allow this permission mode")
		}
	case db.RemotePeerPermissionModeAcceptEdits:
		// readOnly, acceptEdits, and default stay at or under the grant cap.
	default:
		return "", errors.New("peer grant does not allow this permission mode")
	}
	return mode, nil
}

func (s *Server) peerUpdateAgentRuntime(w http.ResponseWriter, r *http.Request) {
	var request peercontrol.UpdateAgentRuntimeRequest
	authorized, ok := s.authorizePeerRequest(w, r, &request, peercontrol.ScopeSendTask, request.AgentID)
	if !ok {
		return
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.Model = strings.TrimSpace(request.Model)
	request.ReasoningEffort = strings.ToLower(strings.TrimSpace(request.ReasoningEffort))
	request.PermissionMode = strings.TrimSpace(request.PermissionMode)
	if request.AgentID == "" || (request.Model == "" && request.ReasoningEffort == "" && request.PermissionMode == "") {
		writeError(w, http.StatusBadRequest, "invalid peer agent runtime request")
		return
	}
	if _, err := s.store.GetAgent(r.Context(), request.AgentID); err != nil {
		writeError(w, http.StatusForbidden, "peer grant does not authorize this agent")
		return
	}
	if request.PermissionMode != "" {
		mode, err := clampPeerGrantedPermissionMode(request.PermissionMode, authorized.grant.PermissionModeCap)
		if err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		request.PermissionMode = mode
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "agent.runtime.update", Actor: peerActor(authorized.pairing.PeerFingerprint), AgentID: request.AgentID,
		SubjectType: "peer_agent", SubjectID: request.AgentID, Outcome: "success", Risk: "high",
		Details: map[string]any{
			"pairingId": authorized.pairing.ID, "model": request.Model != "",
			"reasoningEffort": request.ReasoningEffort != "", "permissionMode": request.PermissionMode != "",
		},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "peer agent runtime was not updated because audit persistence failed")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), request.AgentID)
	if err != nil {
		writeError(w, http.StatusForbidden, "peer grant does not authorize this agent")
		return
	}
	if request.Model != "" {
		updated, err := s.agents().updateModel(r.Context(), request.AgentID, request.Model)
		if err != nil {
			s.writeAPIError(w, r, err)
			return
		}
		agent = updated
	}
	if request.ReasoningEffort != "" {
		effortRequest := agentReasoningRequest{}
		effortRequest.ReasoningEffort.set = true
		effortRequest.ReasoningEffort.value = request.ReasoningEffort
		updated, err := s.modelRuntime().updateReasoningEffort(r.Context(), request.AgentID, effortRequest)
		if err != nil {
			s.writeAPIError(w, r, err)
			return
		}
		agent = updated
	}
	if request.PermissionMode != "" && request.PermissionMode != agent.PermissionMode {
		updated, err := s.agents().updatePermissionMode(r.Context(), request.AgentID, request.PermissionMode)
		if err != nil {
			s.writeAPIError(w, r, err)
			return
		}
		agent = updated
	}
	writeJSON(w, http.StatusOK, peercontrol.UpdateAgentRuntimeResponse{
		AgentID: agent.ID, Model: boundedPeerText(agent.Model, 128), ReasoningEffort: boundedPeerText(agent.ReasoningEffort, 32),
		PermissionMode: agent.PermissionMode, PermissionModeCap: authorized.grant.PermissionModeCap,
	})
}

func (s *Server) peerResolveApproval(w http.ResponseWriter, r *http.Request) {
	var request peercontrol.ResolveApprovalRequest
	authorized, ok := s.authorizePeerRequest(w, r, &request, peercontrol.ScopeApproveOnce, request.AgentID)
	if !ok {
		return
	}
	request.Decision = strings.TrimSpace(request.Decision)
	request.Reason = boundedPeerText(agentpkg.RedactToolActivityText(strings.TrimSpace(request.Reason)), remotePeerApprovalTextBytes)
	request.ApprovalID = strings.TrimSpace(request.ApprovalID)
	validDecision := request.Decision == "allow_once" || request.Decision == "allow_session" || request.Decision == "deny"
	if !validDecision || request.ApprovalID == "" || len(request.ApprovalID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid peer approval request")
		return
	}
	// allow_session outlives the single call it approves, so it needs its own
	// scope on both the pairing and the agent grant; approve_once alone stays a
	// strictly one-shot capability.
	if request.Decision == "allow_session" &&
		(!scopeStringsContain(authorized.pairing.Scopes, peercontrol.ScopeApproveSession) || !scopeStringsContain(authorized.grant.Scopes, peercontrol.ScopeApproveSession)) {
		writeError(w, http.StatusForbidden, "peer grant does not allow session approvals")
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "peer approval execution is unavailable")
		return
	}
	call, err := s.store.GetToolCallByUseID(r.Context(), request.AgentID, request.ApprovalID)
	if err != nil || call.Status != "pending_approval" {
		writeError(w, http.StatusNotFound, "pending peer approval was not found")
		return
	}
	risk := tools.RiskDanger
	if tool, found := s.toolRegistrySnapshot().Get(call.ToolName); found {
		risk = tool.Risk(call.InputJSON)
	}
	if request.Decision != "deny" && authorized.grant.PermissionModeCap == db.RemotePeerPermissionModeReadOnly && risk != tools.RiskRead {
		writeError(w, http.StatusForbidden, "read-only peer grant cannot approve this tool")
		return
	}
	// Some tool calls cannot carry a session grant; apply the safest decision
	// that still honors the peer's intent instead of failing the resolution.
	appliedDecision := request.Decision
	if appliedDecision == "allow_session" && !s.runner.PendingApprovalAllowsSession(request.AgentID, request.ApprovalID) {
		appliedDecision = "allow_once"
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "approval.resolve", Actor: peerActor(authorized.pairing.PeerFingerprint), AgentID: request.AgentID, RunID: call.RunID,
		SubjectType: "tool_use", SubjectID: request.ApprovalID, Outcome: "success", Risk: "critical",
		Details: map[string]any{"pairingId": authorized.pairing.ID, "decision": request.Decision, "appliedDecision": appliedDecision, "toolRisk": string(risk)},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "peer approval was not applied because audit persistence failed")
		return
	}
	accepted, err := s.runner.ApproveToolCall(r.Context(), request.AgentID, request.ApprovalID, agentpkg.ToolApprovalDecision{
		Decision: appliedDecision, Reason: request.Reason, DecidedBy: peerActor(authorized.pairing.PeerFingerprint),
		PermissionGeneration: call.PermissionGeneration, PolicyGeneration: call.PolicyGeneration,
	})
	if err != nil {
		if db.IsConflict(err) {
			writeError(w, http.StatusConflict, "peer approval conflicts with current state")
		} else {
			writeError(w, http.StatusInternalServerError, "peer approval failed")
		}
		return
	}
	if !accepted {
		writeError(w, http.StatusNotFound, "pending peer approval was not found")
		return
	}
	status := "approved"
	if appliedDecision == "deny" {
		status = "denied"
	}
	writeJSON(w, http.StatusOK, peercontrol.ResolveApprovalResponse{ApprovalID: request.ApprovalID, Status: status, AppliedDecision: appliedDecision, ResolvedAt: s.now().UTC()})
}

func (s *Server) authorizePeerRequest(w http.ResponseWriter, r *http.Request, destination any, requiredScope peercontrol.Scope, agentID string) (peerAuthorizedRequest, bool) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return peerAuthorizedRequest{}, false
	}
	token, ok := peerBearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "peer authentication failed")
		return peerAuthorizedRequest{}, false
	}
	session, err := runtime.manager.Authenticate(r.Context(), token)
	if err != nil {
		writePeerControlError(w, err)
		return peerAuthorizedRequest{}, false
	}
	if err := decodePeerJSON(w, r, destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid peer request")
		return peerAuthorizedRequest{}, false
	}
	pairingID := peerRequestPairingID(destination)
	if pairingID == "" || !constantStringBytesEqual(pairingID, session.PairingID) || !scopeContains(session.Scopes, requiredScope) {
		writeError(w, http.StatusForbidden, "peer request is not authorized")
		return peerAuthorizedRequest{}, false
	}
	pairing, err := s.store.GetRemotePeerPairing(r.Context(), session.PairingID)
	if err != nil || pairing.LocalRole != db.RemotePeerLocalRoleHost || pairing.Status != db.RemotePeerPairingStatusActive || remotePairingExpired(pairing, s.now()) ||
		pairing.CredentialRevision != session.CredentialRevision || pairing.GrantRevision != session.GrantRevision || !scopeStringsContain(pairing.Scopes, requiredScope) {
		writeError(w, http.StatusUnauthorized, "peer authentication failed")
		return peerAuthorizedRequest{}, false
	}
	grants, err := s.store.ListRemotePeerGrants(r.Context(), pairing.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "peer authorization failed")
		return peerAuthorizedRequest{}, false
	}
	for _, grant := range grants {
		if grant.PairingID != pairing.ID || grant.Revision != session.GrantRevision {
			writeError(w, http.StatusUnauthorized, "peer grant authorization is invalid")
			return peerAuthorizedRequest{}, false
		}
		if _, err := peercontrol.NormalizeScopes(grant.Scopes); err != nil {
			writeError(w, http.StatusUnauthorized, "peer grant authorization is invalid")
			return peerAuthorizedRequest{}, false
		}
		if _, _, valid := s.loadPeerGrantedAgent(r, grant); !valid {
			writeError(w, http.StatusUnauthorized, "peer grant authorization is invalid")
			return peerAuthorizedRequest{}, false
		}
	}
	authorized := peerAuthorizedRequest{session: session, pairing: pairing, grants: grants}
	requestedAgentID := strings.TrimSpace(agentID)
	if requestedAgentID == "" {
		switch typed := destination.(type) {
		case *peercontrol.GetSnapshotRequest:
			requestedAgentID = strings.TrimSpace(typed.AgentID)
		case *peercontrol.SendTaskRequest:
			requestedAgentID = strings.TrimSpace(typed.AgentID)
		case *peercontrol.UpdateAgentRuntimeRequest:
			requestedAgentID = strings.TrimSpace(typed.AgentID)
		case *peercontrol.ResolveApprovalRequest:
			requestedAgentID = strings.TrimSpace(typed.AgentID)
		}
	}
	if requestedAgentID == "" {
		return authorized, true
	}
	for _, grant := range grants {
		if grant.AgentID != requestedAgentID || grant.Revision != session.GrantRevision || !scopeStringsContain(grant.Scopes, requiredScope) {
			continue
		}
		_, _, valid := s.loadPeerGrantedAgent(r, grant)
		if !valid {
			break
		}
		authorized.grant = grant
		return authorized, true
	}
	writeError(w, http.StatusForbidden, "peer grant does not authorize this agent")
	return peerAuthorizedRequest{}, false
}

func (s *Server) loadPeerGrantedAgent(r *http.Request, grant db.RemotePeerGrant) (db.Project, db.Agent, bool) {
	project, err := s.store.GetProject(r.Context(), grant.ProjectID)
	if err != nil {
		return db.Project{}, db.Agent{}, false
	}
	agent, err := s.store.GetAgent(r.Context(), grant.AgentID)
	if err != nil {
		return db.Project{}, db.Agent{}, false
	}
	workline, err := s.store.GetWorkline(r.Context(), agent.WorklineID)
	if err != nil || workline.ProjectID != grant.ProjectID {
		return db.Project{}, db.Agent{}, false
	}
	return project, agent, true
}

func (s *Server) peerRequestOrigin(r *http.Request) (string, error) {
	targets := requestOriginTargets(r)
	if len(targets) != 1 {
		return "", errors.New("invalid peer request origin")
	}
	target := targets[0]
	scheme := strings.ToLower(strings.TrimSpace(target.scheme))
	host := normalizeHostPortForScheme(target.host, scheme)
	if host == "" {
		return "", errors.New("invalid peer request origin")
	}
	parsed := &url.URL{Scheme: scheme, Host: host}
	allowLoopback := s.peerControl != nil && s.peerControl.loopbackHTTPAllowed()
	if scheme != "https" && !(allowLoopback && scheme == "http" && isLoopbackRemoteOrigin(parsed)) {
		return "", errors.New("peer request origin must use HTTPS")
	}
	return parsed.String(), nil
}

func peerBearerToken(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
		return "", false
	}
	return fields[1], true
}

func peerRequestPairingID(request any) string {
	switch typed := request.(type) {
	case *peercontrol.GetSnapshotRequest:
		return strings.TrimSpace(typed.PairingID)
	case *peercontrol.SendTaskRequest:
		return strings.TrimSpace(typed.PairingID)
	case *peercontrol.UpdateAgentRuntimeRequest:
		return strings.TrimSpace(typed.PairingID)
	case *peercontrol.ResolveApprovalRequest:
		return strings.TrimSpace(typed.PairingID)
	case *peercontrol.ExecutionHeartbeatRequest:
		return strings.TrimSpace(typed.PairingID)
	case *peercontrol.ExecutionClaimRequest:
		return strings.TrimSpace(typed.PairingID)
	case *peercontrol.ExecutionReportRequest:
		return strings.TrimSpace(typed.PairingID)
	default:
		return ""
	}
}

func peerClaimMatchesRequester(claim peercontrol.PairingClaim, invitation db.RemotePairingInvitation) bool {
	return invitation.ProtocolVersion == claim.ProtocolVersion && invitation.ID == claim.InvitationID &&
		invitation.RequesterDisplayName == claim.DisplayName && invitation.RequesterInstallationID == claim.InstallationID &&
		constantStringBytesEqual(invitation.RequesterPublicKey, claim.PublicKey) && constantStringBytesEqual(invitation.RequesterFingerprint, claim.Fingerprint)
}

func constantStringBytesEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func scopeContains(scopes []peercontrol.Scope, required peercontrol.Scope) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func scopeStringsContain(scopes []string, required peercontrol.Scope) bool {
	normalized, err := peercontrol.NormalizeScopes(scopes)
	return err == nil && scopeContains(normalized, required)
}

func peerActor(fingerprint string) string {
	return "peer:" + fingerprintPrefix(fingerprint)
}

func validPeerRequestID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func boundedPeerText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func (runtime *remoteCollaborationRuntime) acceptPeerClaimNonce(nonce string, now time.Time) bool {
	if runtime == nil || nonce == "" {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.claimNonces == nil {
		runtime.claimNonces = make(map[string]time.Time)
	}
	for key, expiresAt := range runtime.claimNonces {
		if !now.Before(expiresAt) {
			delete(runtime.claimNonces, key)
		}
	}
	if _, exists := runtime.claimNonces[nonce]; exists {
		return false
	}
	if len(runtime.claimNonces) >= remotePeerMaxClaimNonces {
		oldestKey := ""
		var oldest time.Time
		for key, expiresAt := range runtime.claimNonces {
			if oldestKey == "" || expiresAt.Before(oldest) {
				oldestKey, oldest = key, expiresAt
			}
		}
		delete(runtime.claimNonces, oldestKey)
	}
	runtime.claimNonces[nonce] = now.Add(peercontrol.PairingClaimMaxAge + peercontrol.PairingClaimClockSkew)
	return true
}

type peerTaskCacheState int

const (
	peerTaskCacheMiss peerTaskCacheState = iota
	peerTaskCacheHit
	peerTaskCacheConflict
)

func peerTaskCachePrefix(pairingID, requestID string) string {
	return pairingID + "\x00" + requestID + "\x00"
}

func peerTaskCacheKey(pairingID, requestID, agentID, digest string) string {
	return peerTaskCachePrefix(pairingID, requestID) + agentID + "\x00" + digest
}

func (runtime *remoteCollaborationRuntime) beginPeerTask(pairingID, requestID, agentID, digest string, now time.Time) (peercontrol.SendTaskResponse, peerTaskCacheState) {
	key := peerTaskCacheKey(pairingID, requestID, agentID, digest)
	prefix := peerTaskCachePrefix(pairingID, requestID)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.taskResults == nil {
		runtime.taskResults = make(map[string]remotePeerTaskRecord)
	}
	runtime.cleanupPeerTasksLocked(now)
	for existingKey, record := range runtime.taskResults {
		if !strings.HasPrefix(existingKey, prefix) {
			continue
		}
		if existingKey == key && record.Status != "processing" {
			return record, peerTaskCacheHit
		}
		return peercontrol.SendTaskResponse{}, peerTaskCacheConflict
	}
	if len(runtime.taskResults) >= remoteCollaborationMaxTasks {
		for index, candidateKey := range runtime.taskOrder {
			candidate, exists := runtime.taskResults[candidateKey]
			if !exists || candidate.Status == "processing" {
				continue
			}
			delete(runtime.taskResults, candidateKey)
			runtime.taskOrder = append(runtime.taskOrder[:index], runtime.taskOrder[index+1:]...)
			break
		}
		if len(runtime.taskResults) >= remoteCollaborationMaxTasks {
			return peercontrol.SendTaskResponse{}, peerTaskCacheConflict
		}
	}
	record := peercontrol.SendTaskResponse{TaskID: db.NewID(), RequestID: requestID, AgentID: agentID, Status: "processing", CreatedAt: now}
	runtime.taskResults[key] = record
	runtime.taskOrder = append(runtime.taskOrder, key)
	return record, peerTaskCacheMiss
}

func (runtime *remoteCollaborationRuntime) completePeerTask(key string, response peercontrol.SendTaskResponse) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	if _, exists := runtime.taskResults[key]; exists {
		runtime.taskResults[key] = response
	}
	runtime.mu.Unlock()
}

func (runtime *remoteCollaborationRuntime) abortPeerTask(key string) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	delete(runtime.taskResults, key)
	runtime.mu.Unlock()
}

func (runtime *remoteCollaborationRuntime) cleanupPeerTasksLocked(now time.Time) {
	if len(runtime.taskOrder) == 0 {
		return
	}
	kept := runtime.taskOrder[:0]
	for _, key := range runtime.taskOrder {
		record, exists := runtime.taskResults[key]
		if !exists {
			continue
		}
		if !record.CreatedAt.IsZero() && !now.Before(record.CreatedAt.Add(remotePeerTaskResultTTL)) {
			delete(runtime.taskResults, key)
			continue
		}
		kept = append(kept, key)
	}
	runtime.taskOrder = kept
}
