package peercontrol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	challengeRandomBytes = 32
	opaqueRandomBytes    = 32
	minRandomTokenBytes  = 16
	maxRandomTokenBytes  = 64

	// ChallengeClockSkew is the small grace interval allowed by verification.
	ChallengeClockSkew = 30 * time.Second
)

var challengeDomain = []byte("autoto-peercontrol/challenge/v1\x00")
var hostChallengeDomain = []byte("autoto-peercontrol/host-challenge/v1\x00")

// SignedChallenge is a signed, domain-separated pairing challenge response.
type SignedChallenge struct {
	ProtocolVersion   int       `json:"protocolVersion"`
	PairingID         string    `json:"pairingId"`
	Challenge         string    `json:"challenge"`
	ExpiresAt         time.Time `json:"expiresAt"`
	EndpointOrigin    string    `json:"endpointOrigin"`
	SignerPublicKey   string    `json:"signerPublicKey"`
	SignerFingerprint string    `json:"signerFingerprint"`
	Signature         string    `json:"signature"`
}

// GenerateChallenge returns a fixed-size cryptographically random base64url
// challenge within the protocol's accepted bounds.
func GenerateChallenge() (string, error) {
	return generateRandomToken(challengeRandomBytes)
}

// GenerateOpaqueToken returns a fixed-size cryptographically random base64url
// opaque token within the protocol's accepted bounds.
func GenerateOpaqueToken() (string, error) {
	return generateRandomToken(opaqueRandomBytes)
}

// SignHostSessionChallenge binds a host identity to a freshly issued challenge
// so the controller can authenticate the paired Autoto before sending its own
// signature or accepting a bearer session.
func SignHostSessionChallenge(identity *Identity, challenge SessionChallenge) (SessionChallenge, error) {
	if identity == nil {
		return SessionChallenge{}, errors.New("peer identity is unavailable")
	}
	payload, err := canonicalHostChallengePayload(challenge.ProtocolVersion, challenge.PairingID, challenge.Challenge, challenge.ExpiresAt, challenge.EndpointOrigin)
	if err != nil {
		return SessionChallenge{}, err
	}
	signature, err := identity.Sign(payload)
	if err != nil {
		return SessionChallenge{}, err
	}
	public := identity.Public()
	challenge.ExpiresAt = challenge.ExpiresAt.UTC()
	challenge.EndpointOrigin, _ = normalizeOrigin(challenge.EndpointOrigin)
	challenge.HostPublicKey = public.PublicKey
	challenge.HostFingerprint = public.Fingerprint
	challenge.HostSignature = base64.StdEncoding.EncodeToString(signature)
	return challenge, nil
}

// VerifyHostSessionChallenge authenticates the host against the identity pinned
// by the invitation/controller-side pairing.
func VerifyHostSessionChallenge(challenge SessionChallenge, expected PublicIdentity, now time.Time) error {
	publicKey, err := decodePublicKey(expected.PublicKey)
	if err != nil || !validFingerprint(expected.Fingerprint) {
		return errors.New("invalid expected host identity")
	}
	fingerprint, _ := FingerprintPublicKey(publicKey)
	if !constantStringEqual(fingerprint, expected.Fingerprint) || !constantStringEqual(challenge.HostPublicKey, expected.PublicKey) || !constantStringEqual(challenge.HostFingerprint, expected.Fingerprint) {
		return errors.New("session challenge host identity does not match")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if challenge.ExpiresAt.IsZero() || now.UTC().After(challenge.ExpiresAt.Add(ChallengeClockSkew)) {
		return errors.New("session challenge has expired")
	}
	payload, err := canonicalHostChallengePayload(challenge.ProtocolVersion, challenge.PairingID, challenge.Challenge, challenge.ExpiresAt, challenge.EndpointOrigin)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(challenge.HostSignature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != challenge.HostSignature {
		return errors.New("invalid session challenge host signature encoding")
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("invalid session challenge host signature")
	}
	return nil
}

func canonicalHostChallengePayload(protocolVersion int, pairingID, challenge string, expiresAt time.Time, endpointOrigin string) ([]byte, error) {
	if _, err := CanonicalChallengePayload(protocolVersion, pairingID, challenge, expiresAt, endpointOrigin); err != nil {
		return nil, err
	}
	origin, _ := normalizeOrigin(endpointOrigin)
	var payload bytes.Buffer
	payload.Write(hostChallengeDomain)
	_ = binary.Write(&payload, binary.BigEndian, uint32(protocolVersion))
	writeCanonicalString(&payload, pairingID)
	writeCanonicalString(&payload, challenge)
	writeCanonicalString(&payload, expiresAt.UTC().Format(time.RFC3339Nano))
	writeCanonicalString(&payload, origin)
	return payload.Bytes(), nil
}

// CanonicalChallengePayload builds the versioned, domain-separated bytes that
// are signed and verified for a pairing challenge.
func CanonicalChallengePayload(protocolVersion int, pairingID, challenge string, expiresAt time.Time, endpointOrigin string) ([]byte, error) {
	if protocolVersion != ProtocolVersion {
		return nil, errors.New("unsupported challenge protocol version")
	}
	if err := validateIdentifier(pairingID); err != nil {
		return nil, errors.New("invalid pairing ID")
	}
	if err := validateRandomToken(challenge); err != nil {
		return nil, errors.New("invalid challenge token")
	}
	origin, err := normalizeOrigin(endpointOrigin)
	if err != nil {
		return nil, err
	}
	if expiresAt.IsZero() || expiresAt.Year() < 2000 || expiresAt.Year() > 9999 {
		return nil, errors.New("invalid challenge expiration")
	}

	var payload bytes.Buffer
	payload.Write(challengeDomain)
	if err := binary.Write(&payload, binary.BigEndian, uint32(protocolVersion)); err != nil {
		return nil, err
	}
	writeCanonicalString(&payload, pairingID)
	writeCanonicalString(&payload, challenge)
	writeCanonicalString(&payload, expiresAt.UTC().Format(time.RFC3339Nano))
	writeCanonicalString(&payload, origin)
	return payload.Bytes(), nil
}

// SignChallenge signs a canonical pairing challenge with the host identity.
func SignChallenge(identity *Identity, pairingID, challenge, endpointOrigin string, expiresAt time.Time) (SignedChallenge, error) {
	if identity == nil {
		return SignedChallenge{}, errors.New("peer identity is unavailable")
	}
	origin, err := normalizeOrigin(endpointOrigin)
	if err != nil {
		return SignedChallenge{}, err
	}
	payload, err := CanonicalChallengePayload(ProtocolVersion, pairingID, challenge, expiresAt, origin)
	if err != nil {
		return SignedChallenge{}, err
	}
	signature, err := identity.Sign(payload)
	if err != nil {
		return SignedChallenge{}, err
	}
	public := identity.Public()
	return SignedChallenge{
		ProtocolVersion:   ProtocolVersion,
		PairingID:         pairingID,
		Challenge:         challenge,
		ExpiresAt:         expiresAt.UTC(),
		EndpointOrigin:    origin,
		SignerPublicKey:   public.PublicKey,
		SignerFingerprint: public.Fingerprint,
		Signature:         base64.StdEncoding.EncodeToString(signature),
	}, nil
}

// VerifyChallenge validates identity, origin, time, canonical payload, and the
// Ed25519 signature. Replay prevention is intentionally left to the caller.
func VerifyChallenge(signed SignedChallenge, expectedFingerprint, expectedOrigin string, now time.Time) error {
	if signed.ProtocolVersion != ProtocolVersion {
		return errors.New("unsupported challenge protocol version")
	}
	if !validFingerprint(expectedFingerprint) {
		return errors.New("invalid expected peer fingerprint")
	}
	expectedOrigin, err := normalizeOrigin(expectedOrigin)
	if err != nil {
		return err
	}
	signedOrigin, err := normalizeOrigin(signed.EndpointOrigin)
	if err != nil || signedOrigin != signed.EndpointOrigin || signedOrigin != expectedOrigin {
		return errors.New("challenge endpoint origin does not match")
	}
	publicKey, err := decodePublicKey(signed.SignerPublicKey)
	if err != nil || !validFingerprint(signed.SignerFingerprint) {
		return errors.New("invalid challenge signer identity")
	}
	fingerprint, _ := FingerprintPublicKey(publicKey)
	if !constantStringEqual(fingerprint, signed.SignerFingerprint) || !constantStringEqual(fingerprint, expectedFingerprint) {
		return errors.New("challenge signer fingerprint does not match")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if signed.ExpiresAt.IsZero() || now.After(signed.ExpiresAt.Add(ChallengeClockSkew)) {
		return errors.New("challenge has expired")
	}
	payload, err := CanonicalChallengePayload(signed.ProtocolVersion, signed.PairingID, signed.Challenge, signed.ExpiresAt, signed.EndpointOrigin)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(signed.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != signed.Signature {
		return errors.New("invalid challenge signature encoding")
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("invalid challenge signature")
	}
	return nil
}

func generateRandomToken(size int) (string, error) {
	if size < minRandomTokenBytes || size > maxRandomTokenBytes {
		return "", errors.New("random token size is out of bounds")
	}
	buffer := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		return "", errors.New("generate random token")
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validateRandomToken(value string) error {
	if value == "" || len(value) > base64.RawURLEncoding.EncodedLen(maxRandomTokenBytes) || strings.TrimSpace(value) != value {
		return errors.New("invalid random token")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) < minRandomTokenBytes || len(decoded) > maxRandomTokenBytes || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return errors.New("invalid random token")
	}
	return nil
}

func writeCanonicalString(writer io.Writer, value string) {
	_ = binary.Write(writer, binary.BigEndian, uint32(len(value)))
	_, _ = io.WriteString(writer, value)
}
