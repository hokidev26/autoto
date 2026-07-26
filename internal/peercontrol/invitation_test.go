package peercontrol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestInvitationRoundTripAndRedaction(t *testing.T) {
	identity := testIdentity(t)
	secret := []byte("one-time-secret-material-123456")
	expiresAt := time.Date(2026, 7, 26, 12, 30, 0, 123, time.UTC)
	envelope, err := NewInvitationEnvelope("https://Example.COM/", "invite_123", secret, identity.Public(), expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Origin != "https://example.com" {
		t.Fatalf("origin = %q, want normalized origin", envelope.Origin)
	}
	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, invitationPrefix) {
		t.Fatalf("unexpected invitation prefix: %q", encoded)
	}
	decoded, err := DecodeInvitation(encoded, expiresAt.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != envelope.Version || decoded.Origin != envelope.Origin || decoded.InvitationID != envelope.InvitationID || decoded.HostPublicKey != envelope.HostPublicKey || decoded.HostFingerprint != envelope.HostFingerprint || !decoded.ExpiresAt.Equal(envelope.ExpiresAt) {
		t.Fatalf("round trip changed envelope: got=%+v want=%+v", decoded, envelope)
	}
	if !bytes.Equal(decoded.Secret(), secret) {
		t.Fatal("round trip changed invitation secret")
	}
	if decoded.SecretHash() != HashInvitationSecret(secret) || HashInvitationSecretHex(secret) == "" {
		t.Fatal("invitation secret hash mismatch")
	}
	formatted := fmt.Sprintf("%v %+v %#v", envelope, envelope, envelope)
	if strings.Contains(formatted, string(secret)) || !strings.Contains(formatted, "[REDACTED]") {
		t.Fatalf("formatted invitation did not redact secret: %s", formatted)
	}
	ordinaryJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ordinaryJSON, secret) || bytes.Contains(ordinaryJSON, []byte("secret")) {
		t.Fatalf("ordinary envelope JSON exposed secret: %s", ordinaryJSON)
	}
}

func TestInvitationRejectsExpiredAndTamperedIdentity(t *testing.T) {
	identity := testIdentity(t)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	envelope, err := NewInvitationEnvelope("http://127.0.0.1:8787", "pair-1", bytes.Repeat([]byte{7}, 32), identity.Public(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeInvitation(encoded, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired invitation was accepted")
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, invitationPrefix))
	if err != nil {
		t.Fatal(err)
	}
	var wire invitationWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	wire.HostFingerprint = strings.Repeat("0", 64)
	payload, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	tampered := invitationPrefix + base64.RawURLEncoding.EncodeToString(payload)
	if _, err := DecodeInvitation(tampered, now); err == nil {
		t.Fatal("invitation with mismatched fingerprint was accepted")
	}
}

func TestInvitationRejectsNonOriginURLs(t *testing.T) {
	identity := testIdentity(t)
	expiresAt := time.Now().Add(time.Minute)
	for _, origin := range []string{
		"http://example.com",
		"https://user@example.com",
		"https://example.com/path",
		"https://example.com?query=1",
		"https://example.com#fragment",
		"ftp://example.com",
	} {
		t.Run(origin, func(t *testing.T) {
			if _, err := NewInvitationEnvelope(origin, "pairing", bytes.Repeat([]byte{1}, 32), identity.Public(), expiresAt); err == nil {
				t.Fatalf("unsafe origin %q was accepted", origin)
			}
		})
	}
}

func testIdentity(t *testing.T) *Identity {
	t.Helper()
	store, err := NewIdentityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
