package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCredentialCipherRoundTripAndDetection(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "keys", "codex.key")
	cipher := NewCredentialCipher("codex", keyPath)
	plaintext := []byte(`{"access_token":"super-secret-token","refresh_token":"rt-secret"}` + "\n")

	envelope, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret-token", "rt-secret"} {
		if bytes.Contains(envelope, []byte(secret)) {
			t.Fatalf("envelope leaked plaintext %q: %s", secret, envelope)
		}
	}
	if !IsEncryptedCredential(envelope) {
		t.Fatalf("envelope was not detected as encrypted: %s", envelope)
	}
	if IsEncryptedCredential(plaintext) {
		t.Fatal("plaintext credential was misdetected as encrypted")
	}

	decrypted, err := cipher.Decrypt(envelope)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypt mismatch: %q err=%v", decrypted, err)
	}
	// A fresh instance must decrypt via the persisted key file.
	fresh := NewCredentialCipher("codex", keyPath)
	decrypted, err = fresh.Decrypt(envelope)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("fresh cipher decrypt mismatch: %q err=%v", decrypted, err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected key file mode 0600, got %o", info.Mode().Perm())
		}
	}
}

func TestCredentialCipherMissingKeyFileYieldsKeyUnavailable(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "store.key")
	cipher := NewCredentialCipher("codex", keyPath)
	envelope, err := cipher.Encrypt([]byte(`{"access_token":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	fresh := NewCredentialCipher("codex", keyPath)
	if _, err := fresh.Decrypt(envelope); !errors.Is(err, ErrCredentialKeyUnavailable) {
		t.Fatalf("missing key error = %v, want ErrCredentialKeyUnavailable", err)
	}
}

func TestCredentialCipherRejectsTamperingAndForeignLabel(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "store.key")
	cipher := NewCredentialCipher("codex", keyPath)
	envelope, err := cipher.Encrypt([]byte(`{"access_token":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}

	var parsed credentialEnvelope
	if err := json.Unmarshal(envelope, &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.Ciphertext[0] ^= 0x01
	tampered, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.Decrypt(tampered); !errors.Is(err, ErrCredentialTampered) {
		t.Fatalf("tampered envelope error = %v, want ErrCredentialTampered", err)
	}

	other := NewCredentialCipher("anthropic", keyPath)
	if _, err := other.Decrypt(envelope); !errors.Is(err, ErrCredentialTampered) {
		t.Fatalf("foreign label error = %v, want ErrCredentialTampered", err)
	}
}

func TestCredentialCipherKeyFileFormats(t *testing.T) {
	// A DPAPI-prefixed key file with an invalid blob is unusable on every
	// platform: non-Windows builds cannot unwrap it at all, and Windows cannot
	// authenticate garbage.
	badPath := filepath.Join(t.TempDir(), "bad.key")
	badContent := append(append([]byte(nil), credentialKeyDPAPIPrefix...), []byte("not-a-dpapi-blob")...)
	if err := os.WriteFile(badPath, badContent, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := NewCredentialCipher("codex", badPath)
	if _, err := bad.Encrypt([]byte(`{"access_token":"secret"}`)); !errors.Is(err, ErrCredentialKeyUnavailable) {
		t.Fatalf("unusable key file error = %v, want ErrCredentialKeyUnavailable", err)
	}

	// A raw 32-byte key file (the non-Windows format) is accepted everywhere,
	// so profiles copied across platforms keep working.
	rawPath := filepath.Join(t.TempDir(), "raw.key")
	rawKey := bytes.Repeat([]byte{0x42}, credentialKeyBytes)
	if err := os.WriteFile(rawPath, rawKey, 0o600); err != nil {
		t.Fatal(err)
	}
	writer := NewCredentialCipher("codex", rawPath)
	plaintext := []byte(`{"access_token":"raw-key-secret"}`)
	envelope, err := writer.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewCredentialCipher("codex", rawPath)
	decrypted, err := reader.Decrypt(envelope)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("raw key round-trip mismatch: %q err=%v", decrypted, err)
	}
}
