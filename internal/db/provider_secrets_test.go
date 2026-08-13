package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderSecretsFreshSchemaAndV41Migration(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if version := readUserVersion(t, ctx, fresh.DB()); version != CurrentDBVersion {
		fresh.Close()
		t.Fatalf("fresh version = %d, want %d", version, CurrentDBVersion)
	}
	if !testTableExists(t, ctx, fresh.DB(), "provider_secrets") {
		fresh.Close()
		t.Fatal("fresh schema missing provider_secrets")
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "v41.db")
	raw := openRawDB(t, path)
	v41Schema := strings.Replace(schemaSQL, providerSecretsSchemaSQL, "", 1)
	if _, err := raw.ExecContext(ctx, v41Schema+"\nPRAGMA user_version = 41;"); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version := readUserVersion(t, ctx, migrated.DB()); version != CurrentDBVersion {
		t.Fatalf("migrated version = %d, want %d", version, CurrentDBVersion)
	}
	if !testTableExists(t, ctx, migrated.DB(), "provider_secrets") {
		t.Fatal("v41 migration missing provider_secrets")
	}
}

func TestProviderSecretsCRUDAndPendingLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "provider-secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ciphertext := []byte{0x01, 0x02, 0x03}
	nonce := []byte{0x04, 0x05}
	binding := []byte{0x06, 0x07}
	pending := ProviderSecretPending{
		ProviderName: "openai", SecretKind: "api_key", Action: ProviderSecretPendingSet,
		Ciphertext: ciphertext, Nonce: nonce, BindingFingerprint: binding,
		KeyVersion: 2, LastFive: "abcde", SecretRevision: 7,
	}
	if err := store.PutProviderSecretPending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProviderSecret(ctx, "openai", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveCiphertext != nil || got.PendingAction != ProviderSecretPendingSet || !bytes.Equal(got.PendingCiphertext, ciphertext) || got.PendingKeyVersion != 2 {
		t.Fatalf("unexpected pending-only record: %+v", got)
	}
	if count, err := store.CountProviderSecrets(ctx); err != nil || count != 1 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
	listed, err := store.ListProviderSecrets(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %#v, err = %v", listed, err)
	}
	if err := store.CommitProviderSecretPending(ctx, "openai", "api_key"); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetProviderSecret(ctx, "openai", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.ActiveCiphertext, ciphertext) || !bytes.Equal(got.ActiveNonce, nonce) || !bytes.Equal(got.ActiveBindingFingerprint, binding) || got.PendingAction != ProviderSecretPendingNone || got.ActiveSecretRevision != 7 {
		t.Fatalf("unexpected committed record: %+v", got)
	}

	if err := store.PutProviderSecretPending(ctx, ProviderSecretPending{ProviderName: "openai", SecretKind: "api_key", Action: ProviderSecretPendingClear, BindingFingerprint: binding, SecretRevision: 8}); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitProviderSecretPending(ctx, "openai", "api_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProviderSecret(ctx, "openai", "api_key"); err == nil {
		t.Fatal("clear commit retained the record")
	}
	if err := store.PutProviderSecretPending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitProviderSecretPending(ctx, "openai", "api_key"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProviderSecretPending(ctx, ProviderSecretPending{ProviderName: "openai", SecretKind: "api_key", Action: ProviderSecretPendingDelete}); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitProviderSecretPending(ctx, "openai", "api_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProviderSecret(ctx, "openai", "api_key"); err == nil {
		t.Fatal("delete commit retained the record")
	}

	if err := store.PutProviderSecretPending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProviderSecret(ctx, "openai", "api_key"); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountProviderSecrets(ctx); err != nil || count != 0 {
		t.Fatalf("count after delete = %d, err = %v", count, err)
	}
}

// The count backs the vault's key-regeneration guard, so only rows holding
// key-dependent ciphertext may be counted. A pending clear carries none: on a
// fresh install the transport-secret flow prepares a proxy-auth clear before
// the first set, and counting that row blocked the first key creation forever.
func TestCountProviderSecretsIgnoresCiphertextFreePendingRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "provider-secrets-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.PutProviderSecretPending(ctx, ProviderSecretPending{
		ProviderName: "relay", SecretKind: "proxy_auth", Action: ProviderSecretPendingClear,
		BindingFingerprint: []byte{0x01}, SecretRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountProviderSecrets(ctx); err != nil || count != 0 {
		t.Fatalf("pending clear must not count as stored ciphertext: count = %d, err = %v", count, err)
	}

	if err := store.PutProviderSecretPending(ctx, ProviderSecretPending{
		ProviderName: "relay", SecretKind: "request_headers", Action: ProviderSecretPendingSet,
		Ciphertext: []byte{0x02}, Nonce: []byte{0x03}, BindingFingerprint: []byte{0x01},
		KeyVersion: 1, SecretRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountProviderSecrets(ctx); err != nil || count != 1 {
		t.Fatalf("pending set holds ciphertext and must count: count = %d, err = %v", count, err)
	}
}

func TestProviderSecretsRollbackAndNoPlaintextSQLiteContent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "provider-secrets.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	active := ProviderSecretPending{ProviderName: "anthropic", SecretKind: "api_key", Action: ProviderSecretPendingSet, Ciphertext: []byte{0xa1}, Nonce: []byte{0xb1}, BindingFingerprint: []byte{0xe1}, KeyVersion: 1, LastFive: "a1", SecretRevision: 1}
	if err := store.PutProviderSecretPending(ctx, active); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitProviderSecretPending(ctx, active.ProviderName, active.SecretKind); err != nil {
		t.Fatal(err)
	}
	replacement := active
	replacement.Action = ProviderSecretPendingSet
	replacement.Ciphertext = []byte{0xc1}
	replacement.Nonce = []byte{0xd1}
	replacement.SecretRevision = 2
	if err := store.PutProviderSecretPending(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackProviderSecretPending(ctx, active.ProviderName, active.SecretKind); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProviderSecret(ctx, active.ProviderName, active.SecretKind)
	if err != nil || !bytes.Equal(got.ActiveCiphertext, active.Ciphertext) || got.PendingAction != ProviderSecretPendingNone {
		t.Fatalf("rollback did not preserve active: %+v, err=%v", got, err)
	}

	pendingOnly := active
	pendingOnly.ProviderName = "google"
	if err := store.PutProviderSecretPending(ctx, pendingOnly); err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackProviderSecretPending(ctx, pendingOnly.ProviderName, pendingOnly.SecretKind); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProviderSecret(ctx, pendingOnly.ProviderName, pendingOnly.SecretKind); err == nil {
		t.Fatal("pending-only rollback retained the record")
	}

	plaintext := []byte("super-secret-api-key")
	ciphertext := sha256.Sum256(plaintext)
	if err := store.PutProviderSecretPending(ctx, ProviderSecretPending{ProviderName: "azure", SecretKind: "api_key", Action: ProviderSecretPendingSet, Ciphertext: ciphertext[:], Nonce: []byte{0xf1}, BindingFingerprint: []byte{0xf2}, KeyVersion: 1, LastFive: "e1", SecretRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, plaintext) {
		t.Fatal("raw SQLite file contains plaintext secret")
	}
	if _, err := store.GetProviderSecret(ctx, "azure", "api_key"); err == nil {
		t.Fatal("closed store unexpectedly served a record")
	}
}

func TestProviderSecretsPendingErrorsDoNotLeakBlobContent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "provider-secrets-errors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret := "ciphertext-that-must-not-be-in-errors"
	if err := store.PutProviderSecretPending(ctx, ProviderSecretPending{ProviderName: "p", SecretKind: "k", Action: "bad", Ciphertext: []byte(secret)}); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if err := store.CommitProviderSecretPending(ctx, "p", "k"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unexpected missing pending error: %v", err)
	}
}
