package peercontrol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPairingClaimSignVerifyAndSecretHashOnly(t *testing.T) {
	identity := testIdentity(t)
	host := testIdentity(t)
	now := time.Date(2026, 7, 26, 12, 0, 0, 123, time.UTC)
	secret := []byte("claim-secret-must-not-be-signed-directly")
	invitation, err := NewInvitationEnvelope("https://host.example", "invite-1", secret, host.Public(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := NewPairingClaimFromInvitation(invitation, " Controller ", "controller:one", identity, now)
	if err != nil {
		t.Fatal(err)
	}
	if claim.DisplayName != "Controller" || claim.SecretHash != HashInvitationSecretHex(secret) {
		t.Fatalf("claim was not canonicalized: %+v", claim)
	}
	signed, err := identity.SignPairingClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyPairingClaim(signed, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !equalPairingClaim(verified, signed.Claim) {
		t.Fatalf("verified claim changed: got=%+v want=%+v", verified, signed.Claim)
	}
	encoded, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, secret) || strings.Contains(string(encoded), string(secret)) {
		t.Fatalf("signed claim exposed raw invitation secret: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(HashInvitationSecretHex(secret))) {
		t.Fatalf("signed claim omitted secret hash: %s", encoded)
	}
}

func TestPairingClaimRejectsTamperingExpirationAndFingerprintMismatch(t *testing.T) {
	identity := testIdentity(t)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	claim, err := NewPairingClaim("invite-1", strings.Repeat("a", 64), "Controller", "controller-one", identity, now)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := identity.SignPairingClaim(claim)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("tampered field", func(t *testing.T) {
		tampered := signed
		tampered.Claim.DisplayName = "Other controller"
		if _, err := VerifyPairingClaim(tampered, now); err == nil {
			t.Fatal("tampered pairing claim was accepted")
		}
	})
	t.Run("non-canonical field", func(t *testing.T) {
		tampered := signed
		tampered.Claim.DisplayName = " Controller "
		if _, err := VerifyPairingClaim(tampered, now); err == nil || !strings.Contains(err.Error(), "canonical") {
			t.Fatalf("non-canonical pairing claim error = %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		if _, err := VerifyPairingClaim(signed, now.Add(PairingClaimMaxAge+PairingClaimClockSkew+time.Second)); err == nil {
			t.Fatal("expired pairing claim was accepted")
		}
	})
	t.Run("future", func(t *testing.T) {
		futureClaim := claim
		futureClaim.IssuedAt = now.Add(PairingClaimClockSkew + time.Second).Format(time.RFC3339Nano)
		futureSigned, err := identity.SignPairingClaim(futureClaim)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyPairingClaim(futureSigned, now); err == nil {
			t.Fatal("future pairing claim was accepted")
		}
	})
	t.Run("wrong fingerprint", func(t *testing.T) {
		tampered := signed
		tampered.Claim.Fingerprint = strings.Repeat("0", 64)
		if _, err := VerifyPairingClaim(tampered, now); err == nil || !strings.Contains(err.Error(), "fingerprint") {
			t.Fatalf("wrong fingerprint error = %v", err)
		}
	})
	t.Run("wrong signer", func(t *testing.T) {
		other := testIdentity(t)
		if _, err := other.SignPairingClaim(claim); err == nil {
			t.Fatal("identity signed a claim bound to another public key")
		}
	})
}
