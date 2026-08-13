package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/agent"
	"autoto/internal/audit"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/peercontrol"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// peerBridgeTestPair wires a real host instance (with runner and peer API
// served over loopback HTTP) to a controller instance whose peer manager signs
// with the identity the host pairing was approved for.
type peerBridgeTestPair struct {
	controller      *Server
	controllerStore *db.Store
	host            *Server
	hostStore       *db.Store
	pairingID       string
	hostAgent       db.Agent
	localAgent      db.Agent
}

func newPeerBridgeLoopbackPair(t *testing.T) peerBridgeTestPair {
	t.Helper()
	ctx := context.Background()

	// The controller's private identity must be shared between the host-side
	// pairing fixture and the controller manager, so pin it to one home dir.
	identityHome := t.TempDir()
	identityStore, err := peercontrol.NewIdentityStore(identityHome)
	if err != nil {
		t.Fatal(err)
	}
	controllerIdentity, err := identityStore.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}

	hostStore, err := db.Open(ctx, filepath.Join(t.TempDir(), "peer-bridge-host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hostStore.Close() })
	hostManager, err := peercontrol.NewManager(peercontrol.ManagerOptions{HomeDir: t.TempDir(), Store: hostStore})
	if err != nil {
		t.Fatal(err)
	}
	if err := hostManager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hostManager.Close(context.Background()) })
	var cfg config.Config
	hostRunner := agent.NewRunner(hostStore, providers.NewRegistry(), tools.NewRegistry(), agent.NewHub(), cfg.Agent)
	hostApp := New(cfg, hostStore, hostRunner, agent.NewHub())
	hostApp.SetAuditRecorder(audit.NewRecorder(hostStore))
	hostApp.SetPeerControlManager(hostManager)
	hostApp.setPeerControlLoopbackHTTPForTests(true)
	enablePeerManagerForAPI(t, hostManager)

	scopes := []string{db.RemotePeerScopeObserve, db.RemotePeerScopeSendTask, db.RemotePeerScopeApproveOnce}
	hostPairing, hostAgent := createPeerAPIHostPairing(t, hostStore, controllerIdentity, scopes, scopes, db.RemotePeerPermissionModeReadOnly)

	hostServer := httptest.NewServer(hostApp.Routes())
	t.Cleanup(hostServer.Close)

	controllerStore, err := db.Open(ctx, filepath.Join(t.TempDir(), "peer-bridge-controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controllerStore.Close() })
	controllerManager, err := peercontrol.NewManager(peercontrol.ManagerOptions{HomeDir: identityHome, Store: controllerStore})
	if err != nil {
		t.Fatal(err)
	}
	if err := controllerManager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controllerManager.Close(context.Background()) })
	controllerApp := New(cfg, controllerStore, nil, nil)
	controllerApp.SetAuditRecorder(audit.NewRecorder(controllerStore))
	controllerApp.SetPeerControlManager(controllerManager)
	controllerApp.setPeerControlLoopbackHTTPForTests(true)

	hostIdentity := hostManager.Identity()
	pairing, err := controllerStore.CreateRemotePeerPairing(ctx, db.RemotePeerPairing{
		ID: hostPairing.ID, DisplayName: "Loopback host", PeerInstallationID: "host-installation",
		PeerPublicKey: hostIdentity.PublicKey, PeerFingerprint: hostIdentity.Fingerprint,
		EndpointOrigin: hostServer.URL, Scopes: scopes,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, localAgent, err := controllerStore.CreateProject(ctx, "Local driver", "", t.TempDir(), "test:model", "readOnly")
	if err != nil {
		t.Fatal(err)
	}

	return peerBridgeTestPair{
		controller: controllerApp, controllerStore: controllerStore,
		host: hostApp, hostStore: hostStore,
		pairingID: pairing.ID, hostAgent: hostAgent, localAgent: localAgent,
	}
}

func listPeerBridgeAudits(t *testing.T, store *db.Store, action string) []db.AutomationAuditEvent {
	t.Helper()
	events, err := store.ListAutomationAuditEvents(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched := make([]db.AutomationAuditEvent, 0)
	for _, event := range events {
		if event.Category == "peer" && event.Action == action {
			matched = append(matched, event)
		}
	}
	return matched
}

func TestPeerAgentBridgeListPeerPairings(t *testing.T) {
	app, store, _ := newPeerAPITestServer(t)
	ctx := context.Background()

	summaries, err := app.ListPeerPairings(ctx)
	if err != nil {
		t.Fatalf("empty list returned error: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("expected no pairings, got %+v", summaries)
	}

	// A host-role pairing must never be offered to the local agent.
	createPeerAPIHostPairing(t, store, newPeerAPIIdentity(t),
		[]string{db.RemotePeerScopeObserve}, []string{db.RemotePeerScopeObserve}, db.RemotePeerPermissionModeReadOnly)

	peerIdentity := newPeerAPIIdentity(t).Public()
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	pairing, err := store.CreateRemotePeerPairing(ctx, db.RemotePeerPairing{
		ID: db.NewID(), DisplayName: "Paired host", PeerInstallationID: "host-installation",
		PeerPublicKey: peerIdentity.PublicKey, PeerFingerprint: peerIdentity.Fingerprint,
		EndpointOrigin: "https://peer.example.test", Scopes: []string{db.RemotePeerScopeObserve, db.RemotePeerScopeSendTask},
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	summaries, err = app.ListPeerPairings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected exactly the controller pairing, got %+v", summaries)
	}
	summary := summaries[0]
	if summary.PairingID != pairing.ID || summary.DisplayName != "Paired host" || summary.Fingerprint != peerIdentity.Fingerprint {
		t.Fatalf("summary fields not mapped: %+v", summary)
	}
	if len(summary.Scopes) != 2 || summary.ExpiresAt != pairing.ExpiresAt {
		t.Fatalf("summary scopes/expiry not mapped: %+v", summary)
	}

	// Expired pairings are dropped even when the store row is still active.
	app.clock = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
	summaries, err = app.ListPeerPairings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("expired pairing was not dropped: %+v", summaries)
	}
}

func TestPeerAgentBridgeSnapshotLoopback(t *testing.T) {
	pair := newPeerBridgeLoopbackPair(t)
	ctx := context.Background()

	raw, err := pair.controller.PeerSnapshot(ctx, tools.PeerSnapshotRequest{PairingID: pair.pairingID})
	if err != nil {
		t.Fatalf("snapshot with default limits failed: %v", err)
	}
	if !strings.Contains(string(raw), pair.pairingID) {
		t.Fatalf("snapshot does not mention the pairing id: %s", raw)
	}

	raw, err = pair.controller.PeerSnapshot(ctx, tools.PeerSnapshotRequest{
		PairingID: pair.pairingID, AgentID: pair.hostAgent.ID, MessageLimit: 1000, RunLimit: 1000,
	})
	if err != nil {
		t.Fatalf("snapshot with oversized limits should be clamped, got error: %v", err)
	}
	if !strings.Contains(string(raw), pair.hostAgent.ID) {
		t.Fatalf("snapshot does not include the selected agent: %s", raw)
	}
}

func TestPeerAgentBridgeSendTask(t *testing.T) {
	pair := newPeerBridgeLoopbackPair(t)
	ctx := context.Background()

	// Validation failures must not write audits or reach the peer.
	if _, err := pair.controller.PeerSendTask(ctx, tools.PeerTaskRequest{
		PairingID: pair.pairingID, AgentID: pair.hostAgent.ID, Message: "   ",
	}); err == nil {
		t.Fatal("empty message was accepted")
	}
	if _, err := pair.controller.PeerSendTask(ctx, tools.PeerTaskRequest{
		PairingID: pair.pairingID, AgentID: pair.hostAgent.ID, Message: strings.Repeat("a", 256<<10+1),
	}); err == nil || !strings.Contains(err.Error(), "256 KiB") {
		t.Fatalf("oversized message was not rejected: %v", err)
	}
	if audits := listPeerBridgeAudits(t, pair.controllerStore, "task.forward"); len(audits) != 0 {
		t.Fatalf("validation failures wrote audits: %+v", audits)
	}

	requestID := db.NewID()
	raw, err := pair.controller.PeerSendTask(ctx, tools.PeerTaskRequest{
		PairingID: pair.pairingID, AgentID: pair.hostAgent.ID, Message: "hello from the local agent",
		RequestID: requestID, LocalAgentID: pair.localAgent.ID,
	})
	if err != nil {
		t.Fatalf("send task failed: %v", err)
	}
	var response peercontrol.SendTaskResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("send task response is not a SendTaskResponse: %v: %s", err, raw)
	}
	if response.RequestID != requestID || response.AgentID != pair.hostAgent.ID || response.Status != "accepted" {
		t.Fatalf("unexpected send task response: %+v", response)
	}

	audits := listPeerBridgeAudits(t, pair.controllerStore, "task.forward")
	if len(audits) != 1 {
		t.Fatalf("expected one task.forward audit, got %+v", audits)
	}
	event := audits[0]
	if event.Actor != "agent" || event.AgentID != pair.localAgent.ID || event.SubjectType != "peer_task" || event.SubjectID != requestID || event.Risk != "high" {
		t.Fatalf("unexpected task.forward audit: %+v", event)
	}
	details := string(event.DetailsJSON)
	if !strings.Contains(details, pair.pairingID) || !strings.Contains(details, "messageDigest") || !strings.Contains(details, pair.localAgent.ID) {
		t.Fatalf("task.forward audit details incomplete: %s", details)
	}
}

func TestPeerAgentBridgeResolveApproval(t *testing.T) {
	pair := newPeerBridgeLoopbackPair(t)
	ctx := context.Background()

	// Invalid decisions fail before any audit is written or the peer is reached.
	if _, err := pair.controller.PeerResolveApproval(ctx, tools.PeerApprovalRequest{
		PairingID: pair.pairingID, AgentID: pair.hostAgent.ID, ApprovalID: "approval-1", Decision: "allow_always",
	}); err == nil || !strings.Contains(err.Error(), "allow_once, allow_session or deny") {
		t.Fatalf("invalid decision was not rejected: %v", err)
	}
	if audits := listPeerBridgeAudits(t, pair.controllerStore, "approval.forward"); len(audits) != 0 {
		t.Fatalf("rejected decision wrote audits: %+v", audits)
	}

	// A nonexistent approval id is audited first, then fails at the peer,
	// mirroring the HTTP proxy: audit before send, peer error surfaced.
	_, err := pair.controller.PeerResolveApproval(ctx, tools.PeerApprovalRequest{
		PairingID: pair.pairingID, AgentID: pair.hostAgent.ID, ApprovalID: "missing-approval",
		Decision: "deny", Reason: "not needed", LocalAgentID: pair.localAgent.ID,
	})
	if err == nil {
		t.Fatal("nonexistent approval did not return an error")
	}
	audits := listPeerBridgeAudits(t, pair.controllerStore, "approval.forward")
	if len(audits) != 1 {
		t.Fatalf("expected one approval.forward audit, got %+v", audits)
	}
	event := audits[0]
	if event.Actor != "agent" || event.AgentID != pair.localAgent.ID || event.SubjectType != "tool_use" || event.SubjectID != "missing-approval" || event.Risk != "critical" {
		t.Fatalf("unexpected approval.forward audit: %+v", event)
	}
	if !strings.Contains(string(event.DetailsJSON), `"decision":"deny"`) {
		t.Fatalf("approval.forward audit details incomplete: %s", event.DetailsJSON)
	}
}

func TestPeerAgentBridgeUnavailableWithoutRuntime(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "peer-bridge-unavailable.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(config.Config{}, store, nil, nil)
	app.SetAuditRecorder(audit.NewRecorder(store))

	const unavailable = "remote collaboration is unavailable on this instance"
	if _, err := app.ListPeerPairings(ctx); err == nil || !strings.Contains(err.Error(), unavailable) {
		t.Fatalf("ListPeerPairings: %v", err)
	}
	if _, err := app.PeerSnapshot(ctx, tools.PeerSnapshotRequest{PairingID: "pairing-1"}); err == nil || !strings.Contains(err.Error(), unavailable) {
		t.Fatalf("PeerSnapshot: %v", err)
	}
	if _, err := app.PeerSendTask(ctx, tools.PeerTaskRequest{PairingID: "pairing-1", AgentID: "agent-1", Message: "hi"}); err == nil || !strings.Contains(err.Error(), unavailable) {
		t.Fatalf("PeerSendTask: %v", err)
	}
	if _, err := app.PeerResolveApproval(ctx, tools.PeerApprovalRequest{PairingID: "pairing-1", AgentID: "agent-1", ApprovalID: "approval-1", Decision: "deny"}); err == nil || !strings.Contains(err.Error(), unavailable) {
		t.Fatalf("PeerResolveApproval: %v", err)
	}
}
