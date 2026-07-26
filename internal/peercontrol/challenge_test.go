package peercontrol

import (
	"testing"
	"time"
)

func TestSignAndVerifyChallenge(t *testing.T) {
	identity := testIdentity(t)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	response, err := SignChallenge(identity, "pairing_123", challenge, "https://Host.Example/", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if response.EndpointOrigin != "https://host.example" {
		t.Fatalf("endpoint origin = %q", response.EndpointOrigin)
	}
	if err := VerifyChallenge(response, identity.Public().Fingerprint, "https://host.example", now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyChallenge(response, identity.Public().Fingerprint, "https://host.example", response.ExpiresAt.Add(10*time.Second)); err != nil {
		t.Fatalf("small clock skew was not allowed: %v", err)
	}
}

func TestVerifyChallengeFailures(t *testing.T) {
	identity := testIdentity(t)
	otherIdentity := testIdentity(t)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	response, err := SignChallenge(identity, "pairing-1", challenge, "http://localhost:8080", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("tampered challenge", func(t *testing.T) {
		tampered := response
		tampered.Challenge, err = GenerateChallenge()
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyChallenge(tampered, identity.Public().Fingerprint, response.EndpointOrigin, now); err == nil {
			t.Fatal("tampered challenge was accepted")
		}
	})
	t.Run("wrong fingerprint", func(t *testing.T) {
		if err := VerifyChallenge(response, otherIdentity.Public().Fingerprint, response.EndpointOrigin, now); err == nil {
			t.Fatal("wrong fingerprint was accepted")
		}
	})
	t.Run("wrong origin", func(t *testing.T) {
		if err := VerifyChallenge(response, identity.Public().Fingerprint, "https://other.example", now); err == nil {
			t.Fatal("wrong origin was accepted")
		}
	})
	t.Run("expired", func(t *testing.T) {
		if err := VerifyChallenge(response, identity.Public().Fingerprint, response.EndpointOrigin, response.ExpiresAt.Add(ChallengeClockSkew+time.Second)); err == nil {
			t.Fatal("expired challenge was accepted")
		}
	})
	t.Run("tampered signature", func(t *testing.T) {
		tampered := response
		tampered.Signature = response.Signature[:len(response.Signature)-2] + "AA"
		if err := VerifyChallenge(tampered, identity.Public().Fingerprint, response.EndpointOrigin, now); err == nil {
			t.Fatal("tampered signature was accepted")
		}
	})
}

func TestRandomTokensAndScopeNormalization(t *testing.T) {
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}
	opaque, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if challenge == opaque || validateRandomToken(challenge) != nil || validateRandomToken(opaque) != nil {
		t.Fatal("generated random tokens are invalid")
	}
	scopes, err := NormalizeScopes([]string{" execute_tools ", "OBSERVE", "observe", "send_task"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Scope{ScopeObserve, ScopeSendTask, ScopeExecuteTools}
	if len(scopes) != len(want) {
		t.Fatalf("scopes = %v, want %v", scopes, want)
	}
	for index := range want {
		if scopes[index] != want[index] {
			t.Fatalf("scopes = %v, want %v", scopes, want)
		}
	}
	if _, err := NormalizeScope("admin"); err == nil {
		t.Fatal("unknown scope was accepted")
	}
}
