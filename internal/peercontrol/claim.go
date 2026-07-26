package peercontrol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// PairingClaimMaxAge bounds how long a signed invitation claim is accepted.
	PairingClaimMaxAge = 5 * time.Minute
	// PairingClaimClockSkew permits a small amount of clock disagreement.
	PairingClaimClockSkew = 30 * time.Second

	maxClaimDisplayNameBytes    = 256
	maxClaimInstallationIDBytes = 128
)

var pairingClaimDomain = []byte("autoto-peercontrol/pairing-claim/v1\x00")

// PairingClaim is the canonical, non-secret controller claim signed during
// invitation pairing. SecretHash is SHA-256 of the invitation secret; the
// plaintext invitation secret is never part of this object or its signature.
type PairingClaim struct {
	ProtocolVersion int    `json:"protocolVersion"`
	InvitationID    string `json:"invitationId"`
	SecretHash      string `json:"secretHash"`
	DisplayName     string `json:"displayName"`
	InstallationID  string `json:"installationId"`
	PublicKey       string `json:"publicKey"`
	Fingerprint     string `json:"fingerprint"`
	IssuedAt        string `json:"issuedAt"`
	Nonce           string `json:"nonce"`
}

// SignedPairingClaim carries a canonical claim and its domain-separated
// Ed25519 signature.
type SignedPairingClaim struct {
	Claim     PairingClaim `json:"claim"`
	Signature string       `json:"signature"`
}

// NewPairingClaim constructs a canonical claim and generates a fresh nonce.
func NewPairingClaim(invitationID, secretHash, displayName, installationID string, identity *Identity, issuedAt time.Time) (PairingClaim, error) {
	if identity == nil {
		return PairingClaim{}, errors.New("peer identity is unavailable")
	}
	if issuedAt.IsZero() {
		issuedAt = time.Now()
	}
	nonce, err := GenerateOpaqueToken()
	if err != nil {
		return PairingClaim{}, err
	}
	public := identity.Public()
	return NormalizePairingClaim(PairingClaim{
		ProtocolVersion: ProtocolVersion,
		InvitationID:    invitationID,
		SecretHash:      secretHash,
		DisplayName:     displayName,
		InstallationID:  installationID,
		PublicKey:       public.PublicKey,
		Fingerprint:     public.Fingerprint,
		IssuedAt:        issuedAt.UTC().Format(time.RFC3339Nano),
		Nonce:           nonce,
	})
}

// NewPairingClaimFromInvitation derives only the invitation secret hash and
// never copies the plaintext secret into the returned claim.
func NewPairingClaimFromInvitation(invitation InvitationEnvelope, displayName, installationID string, identity *Identity, issuedAt time.Time) (PairingClaim, error) {
	digest := invitation.SecretHash()
	return NewPairingClaim(invitation.InvitationID, hex.EncodeToString(digest[:]), displayName, installationID, identity, issuedAt)
}

// NormalizePairingClaim validates and canonicalizes claim fields for signing.
func NormalizePairingClaim(claim PairingClaim) (PairingClaim, error) {
	if claim.ProtocolVersion != ProtocolVersion {
		return PairingClaim{}, errors.New("unsupported pairing claim protocol version")
	}
	if err := validateIdentifier(claim.InvitationID); err != nil {
		return PairingClaim{}, errors.New("invalid pairing claim invitation ID")
	}
	claim.SecretHash = strings.ToLower(strings.TrimSpace(claim.SecretHash))
	if !validFingerprint(claim.SecretHash) {
		return PairingClaim{}, errors.New("invalid pairing claim secret hash")
	}
	claim.DisplayName = strings.TrimSpace(claim.DisplayName)
	if err := validateClaimText(claim.DisplayName, maxClaimDisplayNameBytes); err != nil {
		return PairingClaim{}, errors.New("invalid pairing claim display name")
	}
	claim.InstallationID = strings.TrimSpace(claim.InstallationID)
	if !validClaimInstallationID(claim.InstallationID) {
		return PairingClaim{}, errors.New("invalid pairing claim installation ID")
	}
	publicKey, err := decodePublicKey(claim.PublicKey)
	if err != nil || !validFingerprint(claim.Fingerprint) {
		return PairingClaim{}, errors.New("invalid pairing claim identity")
	}
	fingerprint, _ := FingerprintPublicKey(publicKey)
	if !constantStringEqual(fingerprint, claim.Fingerprint) {
		return PairingClaim{}, errors.New("pairing claim fingerprint does not match public key")
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, claim.IssuedAt)
	if err != nil || issuedAt.Year() < 2000 || issuedAt.Year() > 9999 {
		return PairingClaim{}, errors.New("invalid pairing claim issue time")
	}
	claim.IssuedAt = issuedAt.UTC().Format(time.RFC3339Nano)
	if err := validateRandomToken(claim.Nonce); err != nil {
		return PairingClaim{}, errors.New("invalid pairing claim nonce")
	}
	return claim, nil
}

// CanonicalPairingClaimPayload returns the exact bytes signed for a canonical
// claim. Non-canonical input is rejected rather than silently reinterpreted.
func CanonicalPairingClaimPayload(claim PairingClaim) ([]byte, error) {
	canonical, err := NormalizePairingClaim(claim)
	if err != nil {
		return nil, err
	}
	if !equalPairingClaim(claim, canonical) {
		return nil, errors.New("pairing claim is not canonical")
	}
	return pairingClaimPayload(canonical), nil
}

// SignPairingClaim canonicalizes claim, binds it to this Identity, and signs it.
func (i *Identity) SignPairingClaim(claim PairingClaim) (SignedPairingClaim, error) {
	if i == nil {
		return SignedPairingClaim{}, errors.New("peer identity is unavailable")
	}
	canonical, err := NormalizePairingClaim(claim)
	if err != nil {
		return SignedPairingClaim{}, err
	}
	public := i.Public()
	if !constantStringEqual(canonical.PublicKey, public.PublicKey) || !constantStringEqual(canonical.Fingerprint, public.Fingerprint) {
		return SignedPairingClaim{}, errors.New("pairing claim identity does not match signer")
	}
	signature, err := i.Sign(pairingClaimPayload(canonical))
	if err != nil {
		return SignedPairingClaim{}, err
	}
	return SignedPairingClaim{
		Claim:     canonical,
		Signature: base64.StdEncoding.EncodeToString(signature),
	}, nil
}

// VerifyPairingClaim checks canonical form, public-key fingerprint binding,
// issue time, domain separation, and the Ed25519 signature.
func VerifyPairingClaim(signed SignedPairingClaim, now time.Time) (PairingClaim, error) {
	payload, err := CanonicalPairingClaimPayload(signed.Claim)
	if err != nil {
		return PairingClaim{}, err
	}
	issuedAt, _ := time.Parse(time.RFC3339Nano, signed.Claim.IssuedAt)
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	if issuedAt.After(now.Add(PairingClaimClockSkew)) || now.After(issuedAt.Add(PairingClaimMaxAge+PairingClaimClockSkew)) {
		return PairingClaim{}, errors.New("pairing claim is outside the accepted time window")
	}
	publicKey, err := decodePublicKey(signed.Claim.PublicKey)
	if err != nil {
		return PairingClaim{}, errors.New("invalid pairing claim identity")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(signed.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != signed.Signature {
		return PairingClaim{}, errors.New("invalid pairing claim signature encoding")
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return PairingClaim{}, errors.New("invalid pairing claim signature")
	}
	return signed.Claim, nil
}

func pairingClaimPayload(claim PairingClaim) []byte {
	var payload bytes.Buffer
	payload.Write(pairingClaimDomain)
	_ = binary.Write(&payload, binary.BigEndian, uint32(claim.ProtocolVersion))
	for _, value := range []string{
		claim.InvitationID,
		claim.SecretHash,
		claim.DisplayName,
		claim.InstallationID,
		claim.PublicKey,
		claim.Fingerprint,
		claim.IssuedAt,
		claim.Nonce,
	} {
		writeCanonicalString(&payload, value)
	}
	return payload.Bytes()
}

func equalPairingClaim(left, right PairingClaim) bool {
	return left.ProtocolVersion == right.ProtocolVersion &&
		left.InvitationID == right.InvitationID &&
		left.SecretHash == right.SecretHash &&
		left.DisplayName == right.DisplayName &&
		left.InstallationID == right.InstallationID &&
		left.PublicKey == right.PublicKey &&
		left.Fingerprint == right.Fingerprint &&
		left.IssuedAt == right.IssuedAt &&
		left.Nonce == right.Nonce
}

func validateClaimText(value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return errors.New("invalid text")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("invalid text")
		}
	}
	return nil
}

func validClaimInstallationID(value string) bool {
	if value == "" || len(value) > maxClaimInstallationIDBytes || strings.TrimSpace(value) != value {
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
