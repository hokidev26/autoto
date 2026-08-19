package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"autoto/internal/peercontrol"
)

func TestWriteRemoteCollaborationOwnerErrorDoesNotUseUnauthorized(t *testing.T) {
	owner := httptest.NewRecorder()
	writeRemoteCollaborationOwnerError(owner, peercontrol.ErrUnauthorized)
	if owner.Code != http.StatusConflict {
		t.Fatalf("owner-facing peer auth failed with %d: %s", owner.Code, owner.Body.String())
	}
	var ownerBody map[string]string
	if err := json.Unmarshal(owner.Body.Bytes(), &ownerBody); err != nil {
		t.Fatalf("decode owner body: %v", err)
	}
	if ownerBody["error"] != "peer authentication failed" {
		t.Fatalf("owner-facing message = %q", ownerBody["error"])
	}

	inbound := httptest.NewRecorder()
	writePeerControlError(inbound, peercontrol.ErrUnauthorized)
	if inbound.Code != http.StatusUnauthorized {
		t.Fatalf("inbound peer auth must stay 401, got %d: %s", inbound.Code, inbound.Body.String())
	}
}

func TestWriteRemoteCollaborationOwnerErrorKeepsDisabledAndProtocolMapping(t *testing.T) {
	disabled := httptest.NewRecorder()
	writeRemoteCollaborationOwnerError(disabled, peercontrol.ErrDisabled)
	if disabled.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled sharing returned %d: %s", disabled.Code, disabled.Body.String())
	}
	protocol := httptest.NewRecorder()
	writeRemoteCollaborationOwnerError(protocol, peercontrol.ErrProtocol)
	if protocol.Code != http.StatusBadGateway {
		t.Fatalf("protocol failure returned %d: %s", protocol.Code, protocol.Body.String())
	}
}
