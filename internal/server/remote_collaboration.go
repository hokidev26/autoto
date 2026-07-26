package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/peercontrol"
)

const (
	remoteCollaborationRequestBytes = 128 << 10
	remoteInvitationDefaultTTL      = 10 * time.Minute
	remoteInvitationMinimumTTL      = time.Minute
	remoteInvitationMaximumTTL      = time.Hour
	remoteCollaborationMaxClaims    = 128
	remoteCollaborationMaxTasks     = 512
)

type remoteCollaborationRuntime struct {
	manager *peercontrol.Manager

	mu                        sync.Mutex
	allowLoopbackHTTPForTests bool
	clients                   map[string]*peercontrol.Client
	claims                    map[string]outboundPeerClaim
	taskResults               map[string]remotePeerTaskRecord
	taskOrder                 []string
	claimNonces               map[string]time.Time
}

type outboundPeerClaim struct {
	invitation peercontrol.InvitationEnvelope
	proof      peercontrol.SignedPairingClaim
	client     *peercontrol.Client
	createdAt  time.Time
}

type remotePeerPairingView struct {
	Pairing db.RemotePeerPairing `json:"pairing"`
	Grants  []db.RemotePeerGrant `json:"grants"`
}

type remoteCollaborationStatusResponse struct {
	Available      bool                         `json:"available"`
	SharingEnabled bool                         `json:"sharingEnabled"`
	Identity       peercontrol.PublicIdentity   `json:"identity"`
	Tunnel         TemporaryTunnelSnapshot      `json:"tunnel"`
	Invitations    []db.RemotePairingInvitation `json:"invitations"`
	Pairings       []remotePeerPairingView      `json:"pairings"`
}

func (s *Server) SetPeerControlManager(manager *peercontrol.Manager) {
	if s == nil {
		return
	}
	if manager == nil {
		s.peerControl = nil
		return
	}
	s.peerControl = &remoteCollaborationRuntime{
		manager:     manager,
		clients:     make(map[string]*peercontrol.Client),
		claims:      make(map[string]outboundPeerClaim),
		taskResults: make(map[string]remotePeerTaskRecord),
		claimNonces: make(map[string]time.Time),
	}
}

func (s *Server) setPeerControlLoopbackHTTPForTests(enabled bool) {
	if s == nil || s.peerControl == nil {
		return
	}
	s.peerControl.mu.Lock()
	s.peerControl.allowLoopbackHTTPForTests = enabled
	s.peerControl.mu.Unlock()
}

func isPeerProtocolPath(path string) bool {
	return path == "/api/peer" || strings.HasPrefix(path, "/api/peer/") || path == "/ws/peer" || strings.HasPrefix(path, "/ws/peer/")
}

func (s *Server) mountRemoteCollaborationRoutes(router chi.Router) {
	router.Route("/api/remote-collaboration", func(router chi.Router) {
		router.Use(s.sensitiveLocalTokenGuard)
		router.Get("/status", s.remoteCollaborationStatus)
		router.Put("/sharing", s.updateRemoteCollaborationSharing)
		router.Post("/invitations", s.createRemoteCollaborationInvitation)
		router.Post("/invitations/{id}/approve", s.approveRemoteCollaborationInvitation)
		router.Post("/invitations/{id}/reject", s.rejectRemoteCollaborationInvitation)
		router.Post("/invitations/{id}/revoke", s.revokeRemoteCollaborationInvitation)
		router.Put("/pairings/{id}/authorization", s.replaceRemoteCollaborationAuthorization)
		router.Post("/pairings/{id}/revoke", s.revokeRemoteCollaborationPairing)
		router.Post("/connect", s.connectRemoteCollaborationPeer)
		router.Post("/claims/{id}/poll", s.pollRemoteCollaborationClaim)
		router.Get("/peers/{id}/snapshot", s.proxyRemoteCollaborationSnapshot)
		router.Post("/peers/{id}/agents/{agentId}/tasks", s.proxyRemoteCollaborationTask)
		router.Post("/peers/{id}/agents/{agentId}/approvals/{approvalId}", s.proxyRemoteCollaborationApproval)
	})
}

func (s *Server) remoteCollaborationStatus(w http.ResponseWriter, r *http.Request) {
	response := remoteCollaborationStatusResponse{Available: s.peerControl != nil}
	if s.temporaryTunnel != nil {
		response.Tunnel = s.temporaryTunnel.Snapshot()
	}
	if s.peerControl == nil || s.peerControl.manager == nil {
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.SharingEnabled = s.peerControl.manager.SharingEnabled()
	response.Identity = s.peerControl.manager.Identity()
	invitations, err := s.store.ListRemotePairingInvitations(r.Context(), db.RemotePairingInvitationListOptions{Limit: 200})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	pairings, err := s.store.ListRemotePeerPairings(r.Context(), db.RemotePeerPairingListOptions{Limit: 200})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	views := make([]remotePeerPairingView, 0, len(pairings))
	for _, pairing := range pairings {
		grants, grantErr := s.store.ListRemotePeerGrants(r.Context(), pairing.ID)
		if grantErr != nil {
			writeStoreError(w, grantErr)
			return
		}
		views = append(views, remotePeerPairingView{Pairing: pairing, Grants: grants})
	}
	response.Invitations = invitations
	response.Pairings = views
	writeJSON(w, http.StatusOK, response)
}

type updateRemoteCollaborationSharingRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) updateRemoteCollaborationSharing(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	var request updateRemoteCollaborationSharingRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "sharing.update", Actor: "local-api", SubjectType: "peer_sharing", SubjectID: "runtime",
		Outcome: "success", Risk: "high", Details: map[string]any{"enabled": request.Enabled},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "remote collaboration was not changed because audit persistence failed")
		return
	}
	if err := runtime.manager.SetSharingEnabled(request.Enabled); err != nil {
		writePeerControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sharingEnabled": runtime.manager.SharingEnabled()})
}

type createRemoteCollaborationInvitationRequest struct {
	Origin           string `json:"origin,omitempty"`
	ExpiresInSeconds int64  `json:"expiresInSeconds,omitempty"`
}

func (s *Server) createRemoteCollaborationInvitation(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	if !runtime.manager.SharingEnabled() {
		writeError(w, http.StatusConflict, "enable remote collaboration sharing before creating an invitation")
		return
	}
	var request createRemoteCollaborationInvitationRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	origin, err := s.remoteCollaborationInvitationOrigin(strings.TrimSpace(request.Origin))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ttl := remoteInvitationDefaultTTL
	if request.ExpiresInSeconds != 0 {
		ttl = time.Duration(request.ExpiresInSeconds) * time.Second
	}
	if ttl < remoteInvitationMinimumTTL || ttl > remoteInvitationMaximumTTL {
		writeError(w, http.StatusBadRequest, "invitation lifetime must be between 60 and 3600 seconds")
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate invitation")
		return
	}
	expiresAt := s.now().UTC().Add(ttl)
	invitationID := db.NewID()
	envelope, err := peercontrol.NewInvitationEnvelope(origin, invitationID, secret, runtime.manager.Identity(), expiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	encoded, err := envelope.Encode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not encode invitation")
		return
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "invitation.create", Actor: "local-api", SubjectType: "peer_invitation", SubjectID: invitationID,
		Outcome: "success", Risk: "high", Details: map[string]any{"expiresAt": expiresAt.UTC().Format(time.RFC3339Nano)},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "invitation was not created because audit persistence failed")
		return
	}
	invitation, err := s.store.CreateRemotePairingInvitation(r.Context(), db.RemotePairingInvitation{
		ID: invitationID, CodeHash: peercontrol.HashInvitationSecretHex(secret), ProtocolVersion: peercontrol.ProtocolVersion, ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": invitation, "encodedInvitation": encoded, "hostFingerprint": envelope.HostFingerprint})
}

type remoteInvitationTransitionRequest struct {
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
}

type approveRemoteCollaborationInvitationRequest struct {
	Revision  int64                `json:"revision"`
	Scopes    []string             `json:"scopes"`
	ExpiresAt string               `json:"expiresAt,omitempty"`
	Grants    []db.RemotePeerGrant `json:"grants"`
}

func (s *Server) approveRemoteCollaborationInvitation(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	var request approveRemoteCollaborationInvitationRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	invitationID := chi.URLParam(r, "id")
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "pairing.approve", Actor: "local-api", SubjectType: "peer_invitation", SubjectID: invitationID,
		Outcome: "success", Risk: "critical", Details: map[string]any{"revision": request.Revision, "grantCount": len(request.Grants), "scopes": request.Scopes},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "pairing was not approved because audit persistence failed")
		return
	}
	pairing, grants, err := s.store.ApproveRemotePairingInvitation(r.Context(), invitationID, request.Revision, db.RemotePeerPairing{
		ID: invitationID, Scopes: request.Scopes, ExpiresAt: request.ExpiresAt,
	}, request.Grants)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	_ = runtime.manager.InvalidatePairing(pairing.ID)
	writeJSON(w, http.StatusOK, remotePeerPairingView{Pairing: pairing, Grants: grants})
}

func (s *Server) rejectRemoteCollaborationInvitation(w http.ResponseWriter, r *http.Request) {
	s.transitionRemoteCollaborationInvitation(w, r, "invitation.reject")
}

func (s *Server) revokeRemoteCollaborationInvitation(w http.ResponseWriter, r *http.Request) {
	s.transitionRemoteCollaborationInvitation(w, r, "invitation.revoke")
}

func (s *Server) transitionRemoteCollaborationInvitation(w http.ResponseWriter, r *http.Request, action string) {
	if _, ok := s.requireRemoteCollaborationRuntime(w); !ok {
		return
	}
	var request remoteInvitationTransitionRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	invitationID := chi.URLParam(r, "id")
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: action, Actor: "local-api", SubjectType: "peer_invitation", SubjectID: invitationID,
		Outcome: "success", Risk: "high", Details: map[string]any{"revision": request.Revision, "status": request.Status},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "invitation was not changed because audit persistence failed")
		return
	}
	var invitation db.RemotePairingInvitation
	var err error
	if action == "invitation.reject" {
		invitation, err = s.store.RejectRemotePairingInvitation(r.Context(), invitationID, request.Status, request.Revision)
	} else {
		invitation, err = s.store.RevokeRemotePairingInvitation(r.Context(), invitationID, request.Status, request.Revision)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, invitation)
}

type replaceRemoteCollaborationAuthorizationRequest struct {
	GrantRevision int64                `json:"grantRevision"`
	Scopes        []string             `json:"scopes"`
	ExpiresAt     string               `json:"expiresAt,omitempty"`
	Grants        []db.RemotePeerGrant `json:"grants"`
}

func (s *Server) replaceRemoteCollaborationAuthorization(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	var request replaceRemoteCollaborationAuthorizationRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pairingID := chi.URLParam(r, "id")
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "pairing.authorization_update", Actor: "local-api", SubjectType: "peer_pairing", SubjectID: pairingID,
		Outcome: "success", Risk: "critical", Details: map[string]any{"grantRevision": request.GrantRevision, "grantCount": len(request.Grants), "scopes": request.Scopes},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "authorization was not changed because audit persistence failed")
		return
	}
	pairing, grants, err := s.store.ReplaceRemotePeerAuthorization(r.Context(), pairingID, request.GrantRevision, request.Scopes, request.ExpiresAt, request.Grants)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	_ = runtime.manager.InvalidatePairing(pairingID)
	writeJSON(w, http.StatusOK, remotePeerPairingView{Pairing: pairing, Grants: grants})
}

type revokeRemoteCollaborationPairingRequest struct {
	Status             string `json:"status"`
	CredentialRevision int64  `json:"credentialRevision"`
}

func (s *Server) revokeRemoteCollaborationPairing(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	var request revokeRemoteCollaborationPairingRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	pairingID := chi.URLParam(r, "id")
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "pairing.revoke", Actor: "local-api", SubjectType: "peer_pairing", SubjectID: pairingID,
		Outcome: "success", Risk: "critical", Details: map[string]any{"revision": request.CredentialRevision, "status": request.Status},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "pairing was not revoked because audit persistence failed")
		return
	}
	pairing, err := s.store.RevokeRemotePeerPairing(r.Context(), pairingID, request.Status, request.CredentialRevision)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	_ = runtime.manager.InvalidatePairing(pairingID)
	runtime.removeClient(pairingID)
	writeJSON(w, http.StatusOK, pairing)
}

type connectRemoteCollaborationPeerRequest struct {
	Invitation  string `json:"invitation"`
	DisplayName string `json:"displayName,omitempty"`
}

func (s *Server) connectRemoteCollaborationPeer(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	var request connectRemoteCollaborationPeerRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	invitation, err := peercontrol.DecodeInvitation(strings.TrimSpace(request.Invitation), s.now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := s.store.GetRuntimeSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" {
		displayName = "Autoto"
	}
	client, err := runtime.manager.NewClient(peercontrol.ClientOptions{
		Origin: invitation.Origin, PeerIdentity: peercontrol.PublicIdentity{PublicKey: invitation.HostPublicKey, Fingerprint: invitation.HostFingerprint},
		AllowLoopbackHTTPForTests: runtime.loopbackHTTPAllowed(),
	})
	if err != nil {
		writePeerControlError(w, err)
		return
	}
	prepared, err := client.PrepareClaim(invitation, displayName, settings.InstallationID)
	if err != nil {
		_ = client.Close()
		writePeerControlError(w, err)
		return
	}
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "claim.submit", Actor: "local-api", SubjectType: "peer_invitation", SubjectID: invitation.InvitationID,
		Outcome: "success", Risk: "high", Details: map[string]any{"endpointOrigin": invitation.Origin, "hostFingerprintPrefix": fingerprintPrefix(invitation.HostFingerprint)},
	}); err != nil {
		_ = client.Close()
		writeError(w, http.StatusInternalServerError, "claim was not sent because audit persistence failed")
		return
	}
	response, err := client.Claim(r.Context(), prepared)
	if err != nil {
		_ = client.Close()
		writePeerControlError(w, err)
		return
	}
	runtime.storeClaim(invitation.InvitationID, outboundPeerClaim{invitation: invitation, proof: prepared.Proof, client: client, createdAt: s.now().UTC()})
	writeJSON(w, http.StatusAccepted, map[string]any{"claim": response, "hostFingerprint": invitation.HostFingerprint, "origin": invitation.Origin, "expiresAt": invitation.ExpiresAt})
}

func (s *Server) pollRemoteCollaborationClaim(w http.ResponseWriter, r *http.Request) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return
	}
	invitationID := chi.URLParam(r, "id")
	claim, found := runtime.claim(invitationID)
	if !found {
		writeError(w, http.StatusNotFound, "pending remote claim not found")
		return
	}
	response, err := claim.client.PollClaim(r.Context(), peercontrol.PollClaimRequest{Proof: claim.proof})
	if err != nil {
		writePeerControlError(w, err)
		return
	}
	if response.Status == db.RemotePairingInvitationStatusApproved {
		if err := validateApprovedClaimResponse(claim.invitation, response); err != nil {
			runtime.removeClaim(invitationID)
			writePeerControlError(w, err)
			return
		}
		scopes := make([]string, 0, len(response.Scopes))
		for _, scope := range response.Scopes {
			scopes = append(scopes, string(scope))
		}
		expiresAt := ""
		if !response.ExpiresAt.IsZero() {
			expiresAt = response.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		pairing, createErr := s.store.CreateRemotePeerPairing(r.Context(), db.RemotePeerPairing{
			ID: response.PairingID, LocalRole: db.RemotePeerLocalRoleController, DisplayName: response.HostDisplayName,
			PeerInstallationID: response.HostInstallationID, PeerPublicKey: response.HostIdentity.PublicKey, PeerFingerprint: response.HostIdentity.Fingerprint,
			EndpointOrigin: response.EndpointOrigin, Scopes: scopes, ExpiresAt: expiresAt,
		})
		if db.IsConflict(createErr) {
			pairing, createErr = s.store.GetRemotePeerPairing(r.Context(), response.PairingID)
			if createErr == nil && (pairing.LocalRole != db.RemotePeerLocalRoleController || pairing.PeerFingerprint != response.HostIdentity.Fingerprint || pairing.EndpointOrigin != response.EndpointOrigin) {
				createErr = errors.New("existing controller pairing does not match approved claim")
			}
		}
		if createErr != nil {
			writeStoreError(w, createErr)
			return
		}
		runtime.promoteClaim(invitationID, pairing.ID)
	}
	if response.Status == db.RemotePairingInvitationStatusRejected || response.Status == db.RemotePairingInvitationStatusRevoked || response.Status == db.RemotePairingInvitationStatusExpired {
		runtime.removeClaim(invitationID)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) proxyRemoteCollaborationSnapshot(w http.ResponseWriter, r *http.Request) {
	runtime, pairing, client, ok := s.remoteControllerClient(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	_ = runtime
	messageLimit, err := queryInt(r, "messageLimit", 30, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runLimit, err := queryInt(r, "runLimit", 20, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := client.GetSnapshot(r.Context(), peercontrol.GetSnapshotRequest{
		PairingID: pairing.ID, AgentID: strings.TrimSpace(r.URL.Query().Get("agentId")), Before: strings.TrimSpace(r.URL.Query().Get("before")), MessageLimit: messageLimit, RunLimit: runLimit,
	})
	if err != nil {
		writePeerControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type proxyRemoteCollaborationTaskRequest struct {
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

func (s *Server) proxyRemoteCollaborationTask(w http.ResponseWriter, r *http.Request) {
	_, pairing, client, ok := s.remoteControllerClient(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var request proxyRemoteCollaborationTaskRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" || len(request.Message) > 256<<10 {
		writeError(w, http.StatusBadRequest, "remote task message is required and must be at most 256 KiB")
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" {
		request.RequestID = db.NewID()
	}
	agentID := chi.URLParam(r, "agentId")
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "task.forward", Actor: "local-api", AgentID: agentID, SubjectType: "peer_task", SubjectID: request.RequestID,
		Outcome: "success", Risk: "high", Details: map[string]any{"pairingId": pairing.ID, "messageDigest": digestString(request.Message)},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "remote task was not sent because audit persistence failed")
		return
	}
	response, err := client.SendTask(r.Context(), peercontrol.SendTaskRequest{PairingID: pairing.ID, AgentID: agentID, Message: request.Message, RequestID: request.RequestID})
	if err != nil {
		writePeerControlError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, response)
}

type proxyRemoteCollaborationApprovalRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func (s *Server) proxyRemoteCollaborationApproval(w http.ResponseWriter, r *http.Request) {
	_, pairing, client, ok := s.remoteControllerClient(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var request proxyRemoteCollaborationApprovalRequest
	if err := decodePeerJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.Decision = strings.TrimSpace(request.Decision)
	if request.Decision != "allow_once" && request.Decision != "deny" {
		writeError(w, http.StatusBadRequest, "remote approval decision must be allow_once or deny")
		return
	}
	agentID := chi.URLParam(r, "agentId")
	approvalID := chi.URLParam(r, "approvalId")
	if err := s.recordRequiredPeerAudit(r.Context(), audit.Event{
		Category: "peer", Action: "approval.forward", Actor: "local-api", AgentID: agentID, SubjectType: "tool_use", SubjectID: approvalID,
		Outcome: "success", Risk: "critical", Details: map[string]any{"pairingId": pairing.ID, "decision": request.Decision},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "remote approval was not sent because audit persistence failed")
		return
	}
	response, err := client.ResolveApproval(r.Context(), peercontrol.ResolveApprovalRequest{
		PairingID: pairing.ID, AgentID: agentID, ApprovalID: approvalID, Decision: request.Decision, Reason: strings.TrimSpace(request.Reason),
	})
	if err != nil {
		writePeerControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) requireRemoteCollaborationRuntime(w http.ResponseWriter) (*remoteCollaborationRuntime, bool) {
	if s == nil || s.peerControl == nil || s.peerControl.manager == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "remote collaboration is unavailable")
		return nil, false
	}
	return s.peerControl, true
}

func (s *Server) remoteControllerClient(w http.ResponseWriter, r *http.Request, pairingID string) (*remoteCollaborationRuntime, db.RemotePeerPairing, *peercontrol.Client, bool) {
	runtime, ok := s.requireRemoteCollaborationRuntime(w)
	if !ok {
		return nil, db.RemotePeerPairing{}, nil, false
	}
	pairing, err := s.store.GetRemotePeerPairing(r.Context(), pairingID)
	if err != nil {
		writeStoreError(w, err)
		return nil, db.RemotePeerPairing{}, nil, false
	}
	if pairing.LocalRole != db.RemotePeerLocalRoleController || pairing.Status != db.RemotePeerPairingStatusActive || remotePairingExpired(pairing, s.now()) {
		writeError(w, http.StatusGone, "remote peer pairing is inactive")
		return nil, db.RemotePeerPairing{}, nil, false
	}
	client, err := runtime.clientForPairing(pairing)
	if err != nil {
		writePeerControlError(w, err)
		return nil, db.RemotePeerPairing{}, nil, false
	}
	return runtime, pairing, client, true
}

func (s *Server) remoteCollaborationInvitationOrigin(requested string) (string, error) {
	if requested == "" && s.temporaryTunnel != nil {
		requested = strings.TrimSpace(s.temporaryTunnel.Snapshot().PublicURL)
	}
	if requested == "" {
		return "", errors.New("an active HTTPS tunnel or explicit HTTPS origin is required")
	}
	parsed, err := url.Parse(requested)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid remote collaboration origin")
	}
	allowLoopback := s.peerControl != nil && s.peerControl.loopbackHTTPAllowed()
	if strings.ToLower(parsed.Scheme) != "https" && !(allowLoopback && strings.ToLower(parsed.Scheme) == "http" && isLoopbackRemoteOrigin(parsed)) {
		return "", errors.New("remote collaboration origin must use HTTPS")
	}
	probe, err := peercontrol.NewInvitationEnvelope(requested, "origin-probe", make([]byte, 32), s.peerControl.manager.Identity(), s.now().UTC().Add(time.Minute))
	if err != nil {
		return "", err
	}
	return probe.Origin, nil
}

func isLoopbackRemoteOrigin(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	host := strings.Trim(strings.ToLower(parsed.Hostname()), "[]")
	return host == "127.0.0.1" || host == "::1"
}

func validateApprovedClaimResponse(invitation peercontrol.InvitationEnvelope, response peercontrol.PollClaimResponse) error {
	if response.ProtocolVersion != peercontrol.ProtocolVersion || response.InvitationID != invitation.InvitationID || response.PairingID != invitation.InvitationID || response.EndpointOrigin != invitation.Origin {
		return fmt.Errorf("%w: approved claim identity mismatch", peercontrol.ErrProtocol)
	}
	if response.HostIdentity.PublicKey != invitation.HostPublicKey || response.HostIdentity.Fingerprint != invitation.HostFingerprint || response.HostInstallationID == "" || response.HostDisplayName == "" {
		return fmt.Errorf("%w: approved host identity mismatch", peercontrol.ErrUnauthorized)
	}
	if _, err := peercontrol.NormalizeScopes(func() []string {
		values := make([]string, 0, len(response.Scopes))
		for _, scope := range response.Scopes {
			values = append(values, string(scope))
		}
		return values
	}()); err != nil {
		return fmt.Errorf("%w: approved scopes are invalid", peercontrol.ErrProtocol)
	}
	if !response.ExpiresAt.IsZero() && !time.Now().UTC().Before(response.ExpiresAt) {
		return fmt.Errorf("%w: approved pairing is expired", peercontrol.ErrUnauthorized)
	}
	return nil
}

func remotePairingExpired(pairing db.RemotePeerPairing, now time.Time) bool {
	if pairing.ExpiresAt == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, pairing.ExpiresAt)
	return err != nil || !now.UTC().Before(expiresAt)
}

func decodePeerJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	if r == nil || r.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, remoteCollaborationRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func (s *Server) recordRequiredPeerAudit(ctx context.Context, event audit.Event) error {
	if s == nil || s.audit == nil {
		return errors.New("remote collaboration requires an audit recorder")
	}
	if event.ID == "" {
		event.ID = db.NewID()
	}
	if event.CreatedAt == "" {
		event.CreatedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	return s.audit.Record(ctx, event)
}

func writePeerControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, peercontrol.ErrDisabled), errors.Is(err, peercontrol.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, "remote collaboration is unavailable")
	case errors.Is(err, peercontrol.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "peer authentication failed")
	case errors.Is(err, peercontrol.ErrConflict):
		writeError(w, http.StatusConflict, "peer request conflicts with current state")
	case errors.Is(err, peercontrol.ErrProtocol):
		writeError(w, http.StatusBadGateway, "peer protocol validation failed")
	default:
		writeError(w, http.StatusInternalServerError, "remote collaboration request failed")
	}
}

func fingerprintPrefix(fingerprint string) string {
	fingerprint = strings.TrimSpace(fingerprint)
	if len(fingerprint) > 12 {
		return fingerprint[:12]
	}
	return fingerprint
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (runtime *remoteCollaborationRuntime) loopbackHTTPAllowed() bool {
	if runtime == nil {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.allowLoopbackHTTPForTests
}

func (runtime *remoteCollaborationRuntime) storeClaim(id string, claim outboundPeerClaim) {
	runtime.mu.Lock()
	if previous, found := runtime.claims[id]; found && previous.client != claim.client {
		_ = previous.client.Close()
	}
	if len(runtime.claims) >= remoteCollaborationMaxClaims {
		oldestID := ""
		var oldest time.Time
		for candidateID, candidate := range runtime.claims {
			if oldestID == "" || candidate.createdAt.Before(oldest) {
				oldestID, oldest = candidateID, candidate.createdAt
			}
		}
		if oldestID != "" && oldestID != id {
			old := runtime.claims[oldestID]
			delete(runtime.claims, oldestID)
			_ = old.client.Close()
		}
	}
	runtime.claims[id] = claim
	runtime.mu.Unlock()
}

func (runtime *remoteCollaborationRuntime) claim(id string) (outboundPeerClaim, bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	claim, found := runtime.claims[id]
	return claim, found
}

func (runtime *remoteCollaborationRuntime) removeClaim(id string) {
	runtime.mu.Lock()
	claim, found := runtime.claims[id]
	delete(runtime.claims, id)
	runtime.mu.Unlock()
	if found && claim.client != nil {
		_ = claim.client.Close()
	}
}

func (runtime *remoteCollaborationRuntime) promoteClaim(invitationID, pairingID string) {
	runtime.mu.Lock()
	claim, found := runtime.claims[invitationID]
	delete(runtime.claims, invitationID)
	if found {
		if previous := runtime.clients[pairingID]; previous != nil && previous != claim.client {
			_ = previous.Close()
		}
		runtime.clients[pairingID] = claim.client
	}
	runtime.mu.Unlock()
}

func (runtime *remoteCollaborationRuntime) removeClient(pairingID string) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	client := runtime.clients[pairingID]
	delete(runtime.clients, pairingID)
	runtime.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func (runtime *remoteCollaborationRuntime) clientForPairing(pairing db.RemotePeerPairing) (*peercontrol.Client, error) {
	runtime.mu.Lock()
	if client := runtime.clients[pairing.ID]; client != nil {
		runtime.mu.Unlock()
		return client, nil
	}
	allowLoopback := runtime.allowLoopbackHTTPForTests
	runtime.mu.Unlock()
	client, err := runtime.manager.NewClient(peercontrol.ClientOptions{
		Origin: pairing.EndpointOrigin, PeerIdentity: peercontrol.PublicIdentity{PublicKey: pairing.PeerPublicKey, Fingerprint: pairing.PeerFingerprint}, AllowLoopbackHTTPForTests: allowLoopback,
	})
	if err != nil {
		return nil, err
	}
	runtime.mu.Lock()
	if existing := runtime.clients[pairing.ID]; existing != nil {
		runtime.mu.Unlock()
		_ = client.Close()
		return existing, nil
	}
	runtime.clients[pairing.ID] = client
	runtime.mu.Unlock()
	return client, nil
}
