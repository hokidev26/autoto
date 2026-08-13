package secrets

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

// Credential files were historically plaintext JSON protected only by 0o600,
// which Windows does not enforce. CredentialCipher provides encryption at rest
// for whole credential files using the same XChaCha20-Poly1305 + local key
// file pattern as ProviderVault. On Windows the key file content is
// additionally wrapped with the per-user DPAPI secret.

var (
	// ErrCredentialKeyUnavailable means encrypted credential data exists but the
	// local key material is missing or unusable. The data cannot be recovered;
	// callers must treat the affected credentials as absent so the user can
	// sign in again.
	ErrCredentialKeyUnavailable = errors.New("credential encryption key is unavailable")
	// ErrCredentialTampered means an encrypted credential envelope could not be
	// parsed or authenticated against the local key.
	ErrCredentialTampered = errors.New("credential file could not be authenticated")
)

const (
	credentialEnvelopeVersion = 1
	credentialKeyBytes        = chacha20poly1305.KeySize
	maxCredentialKeyFileBytes = 64 << 10
)

// credentialKeyDPAPIPrefix marks key files whose content is wrapped with the
// Windows DPAPI user secret instead of holding the raw key bytes.
var credentialKeyDPAPIPrefix = []byte("autoto-dpapi-key-v1\x00")

// credentialEnvelope is the on-disk format of an encrypted credential file.
// The version field doubles as the marker distinguishing envelopes from legacy
// plaintext credential JSON documents.
type credentialEnvelope struct {
	Version    int    `json:"autoto_credential_envelope"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// IsEncryptedCredential reports whether data is an encrypted credential
// envelope rather than a legacy plaintext credential document.
func IsEncryptedCredential(data []byte) bool {
	var probe struct {
		Version int `json:"autoto_credential_envelope"`
	}
	return json.Unmarshal(data, &probe) == nil && probe.Version >= 1
}

// CredentialCipher encrypts and decrypts whole credential files. The key file
// is expected to live outside the credential directory so copying the
// directory alone is not sufficient to recover tokens.
type CredentialCipher struct {
	label   string
	keyPath string

	mu  sync.Mutex
	key []byte
}

// NewCredentialCipher returns a cipher bound to keyPath and label. The label
// is mixed into the AEAD associated data so ciphertext cannot be replayed
// across stores of different providers.
func NewCredentialCipher(label, keyPath string) *CredentialCipher {
	return &CredentialCipher{label: label, keyPath: keyPath}
}

func (c *CredentialCipher) KeyPath() string {
	if c == nil {
		return ""
	}
	return c.keyPath
}

// Encrypt seals plaintext into an envelope document, creating the key file on
// first use.
func (c *CredentialCipher) Encrypt(plaintext []byte) ([]byte, error) {
	if c == nil || c.keyPath == "" {
		return nil, ErrCredentialKeyUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key, err := c.loadOrCreateKeyLocked()
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	envelope := credentialEnvelope{
		Version:    credentialEnvelopeVersion,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, c.aad()),
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode credential envelope: %w", err)
	}
	return append(data, '\n'), nil
}

// Decrypt opens an envelope document. A missing key file yields
// ErrCredentialKeyUnavailable; an unreadable or unauthentic envelope yields
// ErrCredentialTampered.
func (c *CredentialCipher) Decrypt(data []byte) ([]byte, error) {
	if c == nil || c.keyPath == "" {
		return nil, ErrCredentialKeyUnavailable
	}
	var envelope credentialEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != credentialEnvelopeVersion {
		return nil, ErrCredentialTampered
	}
	if len(envelope.Nonce) != chacha20poly1305.NonceSizeX || len(envelope.Ciphertext) == 0 {
		return nil, ErrCredentialTampered
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key, err := c.loadExistingKeyLocked()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrCredentialKeyUnavailable
		}
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, c.aad())
	if err != nil {
		return nil, ErrCredentialTampered
	}
	return plaintext, nil
}

func (c *CredentialCipher) aad() []byte {
	return []byte("autoto-credential-envelope-v1\x00" + c.label)
}

func (c *CredentialCipher) loadOrCreateKeyLocked() ([]byte, error) {
	key, err := c.loadExistingKeyLocked()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c.createKeyLocked()
}

func (c *CredentialCipher) loadExistingKeyLocked() ([]byte, error) {
	if c.key != nil {
		return c.key, nil
	}
	path := filepath.Clean(c.keyPath)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, ErrCredentialKeyUnavailable
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxCredentialKeyFileBytes {
		return nil, ErrCredentialKeyUnavailable
	}
	// Unix ACL hygiene: key files must not be group/world readable. Windows
	// commonly surfaces 0o666 even for owner-only files, so skip there; the
	// DPAPI wrap protects Windows key files instead.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, ErrCredentialKeyUnavailable
	}
	wrapped, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	key, err := unprotectKeyMaterial(wrapped)
	if err != nil || len(key) != credentialKeyBytes {
		return nil, ErrCredentialKeyUnavailable
	}
	c.key = key
	return key, nil
}

func (c *CredentialCipher) createKeyLocked() ([]byte, error) {
	path := filepath.Clean(c.keyPath)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	key := make([]byte, credentialKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	wrapped := protectKeyMaterial(key)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another store instance or process won the creation race.
			return c.loadExistingKeyLocked()
		}
		return nil, ErrCredentialKeyUnavailable
	}
	completed := false
	defer func() {
		_ = file.Close()
		if !completed {
			_ = os.Remove(path)
		}
	}()
	// Best-effort on Windows: Chmod is limited and often a no-op.
	if err := file.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return nil, ErrCredentialKeyUnavailable
	}
	if _, err := file.Write(wrapped); err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	if err := file.Sync(); err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	if err := file.Close(); err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	// Directory fsync is not reliably supported on Windows handles.
	if directory, err := os.Open(dir); err == nil {
		syncErr := directory.Sync()
		_ = directory.Close()
		if syncErr != nil && runtime.GOOS != "windows" {
			return nil, ErrCredentialKeyUnavailable
		}
	} else if runtime.GOOS != "windows" {
		return nil, ErrCredentialKeyUnavailable
	}
	completed = true
	c.key = key
	return key, nil
}
