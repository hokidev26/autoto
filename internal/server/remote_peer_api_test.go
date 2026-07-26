package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/audit"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/peercontrol"
)

func TestPeerRoutesRejectBrowserOriginAndDisabledSharing(t *testing.T) {
	app, _, manager := newPeerAPITestServer(t)

	browser := peerAPITestRequest(t, http.MethodPost, "/api/peer/v1/claim", map[string]any{})
	browser.Header.Set("Origin", "https://browser.example")
	browserResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(browserResponse, browser)
	if browserResponse.Code != http.StatusForbidden {
		t.Fatalf("browser peer request returned %d: %s", browserResponse.Code, browserResponse.Body.String())
	}

	disabled := peerAPITestRequest(t, http.MethodPost, "/api/peer/v1/claim", map[string]any{})
	disabledResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(disabledResponse, disabled)
	if disabledResponse.Code != http.StatusServiceUnavailable || manager.SharingEnabled() {
		t.Fatalf("disabled sharing returned %d: %s", disabledResponse.Code, disabledResponse.Body.String())
	}
}

func TestPeerClaimWrongSecretCannotClaimInvitation(t *testing.T) {
	app, store, manager := newPeerAPITestServer(t)
	enablePeerManagerForAPI(t, manager)
	controller := newPeerAPIIdentity(t)
	correctSecret := bytes.Repeat([]byte{0x42}, 32)
	invitation, err := store.CreateRemotePairingInvitation(context.Background(), db.RemotePairingInvitation{
		ID: db.NewID(), CodeHash: peercontrol.HashInvitationSecretHex(correctSecret), ProtocolVersion: peercontrol.ProtocolVersion,
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := peercontrol.NewPairingClaim(invitation.ID, invitation.CodeHash, "Controller", "controller-installation", controller, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := controller.SignPairingClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	request := peerAPITestRequest(t, http.MethodPost, "/api/peer/v1/claim", peercontrol.ClaimRequest{
		Proof: proof, Secret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32)),
	})
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret returned %d: %s", response.Code, response.Body.String())
	}
	stored, err := store.GetRemotePairingInvitation(context.Background(), invitation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != db.RemotePairingInvitationStatusOpen || stored.RequesterFingerprint != "" {
		t.Fatalf("wrong secret mutated claim state: %+v", stored)
	}
}

func TestPeerSnapshotRequiresDedicatedBearer(t *testing.T) {
	app, _, manager := newPeerAPITestServer(t)
	enablePeerManagerForAPI(t, manager)
	body := peercontrol.GetSnapshotRequest{PairingID: "pairing-1"}

	missing := peerAPITestRequest(t, http.MethodPost, "/api/peer/v1/snapshot", body)
	missingResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer returned %d: %s", missingResponse.Code, missingResponse.Body.String())
	}

	invalid := peerAPITestRequest(t, http.MethodPost, "/api/peer/v1/snapshot", body)
	invalid.Header.Set("Authorization", "Bearer not-a-peer-token")
	invalidResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer returned %d: %s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func TestPeerSnapshotRejectsMissingGrantScope(t *testing.T) {
	app, store, manager := newPeerAPITestServer(t)
	enablePeerManagerForAPI(t, manager)
	controller := newPeerAPIIdentity(t)
	pairing, agent := createPeerAPIHostPairing(t, store, controller,
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeSendTask},
		[]string{db.RemotePeerScopeSendTask},
		db.RemotePeerPermissionModeReadOnly,
	)
	token := peerAPISessionToken(t, manager, controller, pairing.ID)
	request := peerAPITestRequest(t, http.MethodPost, "/api/peer/v1/snapshot", peercontrol.GetSnapshotRequest{PairingID: pairing.ID, AgentID: agent.ID})
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing grant scope returned %d: %s", response.Code, response.Body.String())
	}
}

func TestPeerSnapshotOmitsWorkspacePromptAndRawToolInput(t *testing.T) {
	app, store, manager := newPeerAPITestServer(t)
	enablePeerManagerForAPI(t, manager)
	controller := newPeerAPIIdentity(t)
	pairing, agent := createPeerAPIHostPairing(t, store, controller,
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeApproveOnce},
		[]string{db.RemotePeerScopeObserve, db.RemotePeerScopeApproveOnce},
		db.RemotePeerPermissionModeReadOnly,
	)
	const cwdSecret = "CWD_SECRET_SHOULD_NOT_LEAK"
	const promptSecret = "SYSTEM_PROMPT_SECRET_SHOULD_NOT_LEAK"
	const toolSecret = "RAW_TOOL_INPUT_SECRET_SHOULD_NOT_LEAK"
	if _, err := store.DB().ExecContext(context.Background(), `UPDATE agents SET cwd = ?, system_prompt = ? WHERE id = ?`, cwdSecret, promptSecret, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(context.Background(), db.Message{
		AgentID: agent.ID, Role: "assistant", ContentText: "safe response", ContentJSON: json.RawMessage(`[{"type":"text","text":"private structured content"}]`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddToolCall(context.Background(), db.ToolCall{
		AgentID: agent.ID, ToolUseID: "approval-1", ToolName: "Bash", InputJSON: json.RawMessage(`{"command":"` + toolSecret + `"}`),
		Status: "pending_approval", PermissionDecisionReason: "review required", PermissionDenyMessage: "be careful",
	}); err != nil {
		t.Fatal(err)
	}
	token := peerAPISessionToken(t, manager, controller, pairing.ID)
	request := peerAPITestRequest(t, http.MethodPost, "/api/peer/v1/snapshot", peercontrol.GetSnapshotRequest{PairingID: pairing.ID, AgentID: agent.ID})
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot returned %d: %s", response.Code, response.Body.String())
	}
	payload := response.Body.String()
	for _, forbidden := range []string{cwdSecret, promptSecret, toolSecret, "systemPrompt", "contentJson", "inputJson"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(payload, `"approvalId":"approval-1"`) || !strings.Contains(payload, `"contentText":"safe response"`) {
		t.Fatalf("snapshot omitted safe projection fields: %s", payload)
	}
}

func newPeerAPITestServer(t *testing.T) (*Server, *db.Store, *peercontrol.Manager) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "peer-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := peercontrol.NewManager(peercontrol.ManagerOptions{HomeDir: t.TempDir(), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	app := New(config.Config{}, store, nil, nil)
	app.SetAuditRecorder(audit.NewRecorder(store))
	app.SetPeerControlManager(manager)
	app.setPeerControlLoopbackHTTPForTests(true)
	return app, store, manager
}

func newPeerAPIIdentity(t *testing.T) *peercontrol.Identity {
	t.Helper()
	store, err := peercontrol.NewIdentityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func enablePeerManagerForAPI(t *testing.T, manager *peercontrol.Manager) {
	t.Helper()
	if err := manager.SetSharingEnabled(true); err != nil {
		t.Fatal(err)
	}
}

func createPeerAPIHostPairing(t *testing.T, store *db.Store, controller *peercontrol.Identity, pairingScopes, grantScopes []string, permissionMode string) (db.RemotePeerPairing, db.Agent) {
	t.Helper()
	ctx := context.Background()
	secret := bytes.Repeat([]byte{0x55}, 32)
	invitation, err := store.CreateRemotePairingInvitation(ctx, db.RemotePairingInvitation{
		ID: db.NewID(), CodeHash: peercontrol.HashInvitationSecretHex(secret), ProtocolVersion: peercontrol.ProtocolVersion,
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	public := controller.Public()
	claimed, err := store.ClaimRemotePairingInvitation(ctx, invitation.ID, invitation.CodeHash, invitation.Revision, db.RemotePairingRequester{
		DisplayName: "Controller", InstallationID: "controller-installation", PublicKey: public.PublicKey, Fingerprint: public.Fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, _, agent, err := store.CreateProject(ctx, "Shared project", "", t.TempDir(), "test:model", "readOnly")
	if err != nil {
		t.Fatal(err)
	}
	pairing, _, err := store.ApproveRemotePairingInvitation(ctx, invitation.ID, claimed.Revision, db.RemotePeerPairing{
		ID: invitation.ID, Scopes: pairingScopes,
	}, []db.RemotePeerGrant{{ProjectID: project.ID, AgentID: agent.ID, Scopes: grantScopes, PermissionModeCap: permissionMode}})
	if err != nil {
		t.Fatal(err)
	}
	return pairing, agent
}

func peerAPISessionToken(t *testing.T, manager *peercontrol.Manager, controller *peercontrol.Identity, pairingID string) string {
	t.Helper()
	const origin = "https://peer.example.test"
	challenge, err := manager.IssueSessionChallengeForOrigin(context.Background(), pairingID, origin)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := peercontrol.SignChallenge(controller, pairingID, challenge.Challenge, origin, challenge.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.EstablishSession(context.Background(), pairingID, signed)
	if err != nil {
		t.Fatal(err)
	}
	return credential.BearerToken
}

func peerAPITestRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := newTestRequest(method, target, bytes.NewReader(data))
	request.Host = "peer.example.test"
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Content-Type", "application/json")
	return request
}
