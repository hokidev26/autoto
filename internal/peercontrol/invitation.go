package peercontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	invitationPrefix          = "autoto-pair:"
	maxInvitationEncodedBytes = 8192
	maxInvitationJSONBytes    = 4096
	maxInvitationIDBytes      = 128
	minSecretBytes            = 16
	maxSecretBytes            = 128
)

// InvitationEnvelope carries the one-time pairing material. The secret is
// unexported so ordinary JSON marshaling and formatting cannot disclose it.
type InvitationEnvelope struct {
	Version         int       `json:"version"`
	Origin          string    `json:"origin"`
	InvitationID    string    `json:"invitationId"`
	HostPublicKey   string    `json:"hostPublicKey"`
	HostFingerprint string    `json:"hostFingerprint"`
	ExpiresAt       time.Time `json:"expiresAt"`
	secret          []byte
}

type invitationWire struct {
	Version         int    `json:"version"`
	Origin          string `json:"origin"`
	InvitationID    string `json:"invitationId"`
	Secret          string `json:"secret"`
	HostPublicKey   string `json:"hostPublicKey"`
	HostFingerprint string `json:"hostFingerprint"`
	ExpiresAt       string `json:"expiresAt"`
}

// NewInvitationEnvelope validates and constructs an invitation envelope.
func NewInvitationEnvelope(origin, invitationID string, secret []byte, host PublicIdentity, expiresAt time.Time) (InvitationEnvelope, error) {
	envelope := InvitationEnvelope{
		Version:         ProtocolVersion,
		Origin:          origin,
		InvitationID:    invitationID,
		HostPublicKey:   host.PublicKey,
		HostFingerprint: host.Fingerprint,
		ExpiresAt:       expiresAt,
		secret:          append([]byte(nil), secret...),
	}
	if err := envelope.validate(time.Time{}, false); err != nil {
		return InvitationEnvelope{}, err
	}
	return envelope, nil
}

// Secret returns a defensive copy of the one-time secret.
func (e InvitationEnvelope) Secret() []byte {
	return append([]byte(nil), e.secret...)
}

// SecretToken returns the canonical base64url representation sent only over the
// dedicated HTTPS claim endpoint. It must never be logged or persisted.
func (e InvitationEnvelope) SecretToken() (string, error) {
	if len(e.secret) < minSecretBytes || len(e.secret) > maxSecretBytes {
		return "", errors.New("invalid invitation secret length")
	}
	return base64.RawURLEncoding.EncodeToString(e.secret), nil
}

// DecodeInvitationSecretToken strictly decodes the one-time claim secret.
func DecodeInvitationSecretToken(value string) ([]byte, error) {
	if value == "" || len(value) > base64.RawURLEncoding.EncodedLen(maxSecretBytes) || strings.TrimSpace(value) != value {
		return nil, errors.New("invalid invitation secret")
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(secret) < minSecretBytes || len(secret) > maxSecretBytes || base64.RawURLEncoding.EncodeToString(secret) != value {
		return nil, errors.New("invalid invitation secret")
	}
	return secret, nil
}

// SecretHash returns the SHA-256 digest of the one-time secret.
func (e InvitationEnvelope) SecretHash() [sha256.Size]byte {
	return HashInvitationSecret(e.secret)
}

// HashInvitationSecret returns a SHA-256 digest suitable for durable comparison
// without storing the invitation's plaintext secret.
func HashInvitationSecret(secret []byte) [sha256.Size]byte {
	return sha256.Sum256(secret)
}

// HashInvitationSecretHex returns the lowercase hexadecimal secret digest.
func HashInvitationSecretHex(secret []byte) string {
	digest := HashInvitationSecret(secret)
	return hex.EncodeToString(digest[:])
}

// Encode serializes the invitation as autoto-pair: followed by base64url JSON.
func (e InvitationEnvelope) Encode() (string, error) {
	if err := e.validate(time.Time{}, false); err != nil {
		return "", err
	}
	wire := invitationWire{
		Version:         e.Version,
		Origin:          e.Origin,
		InvitationID:    e.InvitationID,
		Secret:          base64.RawURLEncoding.EncodeToString(e.secret),
		HostPublicKey:   e.HostPublicKey,
		HostFingerprint: e.HostFingerprint,
		ExpiresAt:       e.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return "", errors.New("encode invitation envelope")
	}
	if len(data) > maxInvitationJSONBytes {
		return "", errors.New("invitation envelope is too large")
	}
	encoded := invitationPrefix + base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > maxInvitationEncodedBytes {
		return "", errors.New("encoded invitation is too large")
	}
	return encoded, nil
}

// EncodeInvitation serializes an invitation envelope.
func EncodeInvitation(envelope InvitationEnvelope) (string, error) {
	return envelope.Encode()
}

// DecodeInvitation strictly parses and validates an encoded invitation. A zero
// now value uses the current time.
func DecodeInvitation(encoded string, now time.Time) (InvitationEnvelope, error) {
	if len(encoded) <= len(invitationPrefix) || len(encoded) > maxInvitationEncodedBytes || !strings.HasPrefix(encoded, invitationPrefix) {
		return InvitationEnvelope{}, errors.New("invalid invitation encoding")
	}
	payload := strings.TrimPrefix(encoded, invitationPrefix)
	data, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil || len(data) == 0 || len(data) > maxInvitationJSONBytes || base64.RawURLEncoding.EncodeToString(data) != payload {
		return InvitationEnvelope{}, errors.New("invalid invitation payload")
	}
	var wire invitationWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return InvitationEnvelope{}, errors.New("invalid invitation JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return InvitationEnvelope{}, errors.New("invalid invitation JSON")
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(wire.Secret)
	if err != nil || base64.RawURLEncoding.EncodeToString(secret) != wire.Secret {
		return InvitationEnvelope{}, errors.New("invalid invitation secret")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if err != nil || expiresAt.Location() == time.Local {
		return InvitationEnvelope{}, errors.New("invalid invitation expiration")
	}
	envelope := InvitationEnvelope{
		Version:         wire.Version,
		Origin:          wire.Origin,
		InvitationID:    wire.InvitationID,
		HostPublicKey:   wire.HostPublicKey,
		HostFingerprint: wire.HostFingerprint,
		ExpiresAt:       expiresAt,
		secret:          secret,
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := envelope.validate(now, true); err != nil {
		return InvitationEnvelope{}, err
	}
	return envelope, nil
}

// Validate validates the envelope and expiration against now.
func (e InvitationEnvelope) Validate(now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	return e.validate(now, true)
}

// String redacts the one-time secret.
func (e InvitationEnvelope) String() string {
	return fmt.Sprintf("peercontrol.InvitationEnvelope{Version:%d Origin:%q InvitationID:%q Secret:[REDACTED] HostPublicKey:%q HostFingerprint:%q ExpiresAt:%q}", e.Version, e.Origin, e.InvitationID, e.HostPublicKey, e.HostFingerprint, e.ExpiresAt.UTC().Format(time.RFC3339Nano))
}

// GoString redacts the one-time secret.
func (e InvitationEnvelope) GoString() string { return e.String() }

func (e *InvitationEnvelope) validate(now time.Time, checkExpiration bool) error {
	if e.Version != ProtocolVersion {
		return errors.New("unsupported invitation protocol version")
	}
	origin, err := normalizeOrigin(e.Origin)
	if err != nil {
		return err
	}
	e.Origin = origin
	if err := validateIdentifier(e.InvitationID); err != nil {
		return errors.New("invalid invitation ID")
	}
	if len(e.secret) < minSecretBytes || len(e.secret) > maxSecretBytes {
		return errors.New("invalid invitation secret length")
	}
	publicKey, err := decodePublicKey(e.HostPublicKey)
	if err != nil || !validFingerprint(e.HostFingerprint) {
		return errors.New("invalid invitation host identity")
	}
	fingerprint, _ := FingerprintPublicKey(publicKey)
	if !constantStringEqual(fingerprint, e.HostFingerprint) {
		return errors.New("invitation host fingerprint does not match public key")
	}
	if e.ExpiresAt.IsZero() || e.ExpiresAt.Year() < 2000 || e.ExpiresAt.Year() > 9999 {
		return errors.New("invalid invitation expiration")
	}
	e.ExpiresAt = e.ExpiresAt.UTC()
	if checkExpiration && !now.Before(e.ExpiresAt) {
		return errors.New("invitation has expired")
	}
	return nil
}

func normalizeOrigin(value string) (string, error) {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("invalid endpoint origin")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", errors.New("invalid endpoint origin")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", errors.New("endpoint origin must not contain userinfo, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawPath != "" {
		return "", errors.New("endpoint origin must not contain a path")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", errors.New("endpoint origin must use HTTPS")
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.ContainsAny(hostname, "\x00/\\") {
		return "", errors.New("invalid endpoint origin host")
	}
	if scheme == "http" && !isLoopbackHost(hostname) {
		return "", errors.New("HTTP endpoint origin must be loopback")
	}
	if port := parsed.Port(); port != "" {
		for _, character := range port {
			if character < '0' || character > '9' {
				return "", errors.New("invalid endpoint origin port")
			}
		}
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.Scheme + "://" + parsed.Host, nil
}

func isLoopbackHost(hostname string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	address := net.ParseIP(hostname)
	return address != nil && address.IsLoopback()
}

func validateIdentifier(value string) error {
	if value == "" || len(value) > maxInvitationIDBytes || strings.TrimSpace(value) != value {
		return errors.New("invalid identifier")
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return errors.New("invalid identifier")
	}
	return nil
}
