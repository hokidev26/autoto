package secrets

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type fakeProviderSecretStore struct {
	records map[string]ProviderSecretRecord
}

func newFakeProviderSecretStore() *fakeProviderSecretStore {
	return &fakeProviderSecretStore{records: make(map[string]ProviderSecretRecord)}
}

func (s *fakeProviderSecretStore) key(name, kind string) string { return name + "\x00" + kind }
func (s *fakeProviderSecretStore) GetProviderSecret(_ context.Context, name, kind string) (ProviderSecretRecord, error) {
	record, ok := s.records[s.key(name, kind)]
	if !ok {
		return ProviderSecretRecord{}, sql.ErrNoRows
	}
	return cloneProviderSecretRecord(record), nil
}
func (s *fakeProviderSecretStore) ListProviderSecrets(_ context.Context) ([]ProviderSecretRecord, error) {
	out := make([]ProviderSecretRecord, 0, len(s.records))
	for _, record := range s.records {
		out = append(out, cloneProviderSecretRecord(record))
	}
	return out, nil
}
func (s *fakeProviderSecretStore) CountProviderSecrets(_ context.Context) (int, error) {
	// Mirrors db.Store.CountProviderSecrets: only key-dependent ciphertext
	// counts; pending clear/delete rows carry none.
	count := 0
	for _, record := range s.records {
		if len(record.ActiveCiphertext) > 0 || len(record.PendingCiphertext) > 0 {
			count++
		}
	}
	return count, nil
}
func (s *fakeProviderSecretStore) PutProviderSecretPending(_ context.Context, pending ProviderSecretPending) error {
	key := s.key(pending.ProviderName, pending.SecretKind)
	record := s.records[key]
	record.ProviderName, record.SecretKind = pending.ProviderName, pending.SecretKind
	record.PendingAction = pending.Action
	record.PendingCiphertext = append([]byte(nil), pending.Ciphertext...)
	record.PendingNonce = append([]byte(nil), pending.Nonce...)
	record.PendingBindingFingerprint = append([]byte(nil), pending.BindingFingerprint...)
	record.PendingKeyVersion = pending.KeyVersion
	record.PendingLastFive = pending.LastFive
	record.PendingSecretRevision = pending.SecretRevision
	s.records[key] = record
	return nil
}
func (s *fakeProviderSecretStore) CommitProviderSecretPending(_ context.Context, name, kind string) error {
	key := s.key(name, kind)
	record, ok := s.records[key]
	if !ok {
		return sql.ErrNoRows
	}
	switch record.PendingAction {
	case ProviderSecretPendingSet:
		record.ActiveCiphertext = append([]byte(nil), record.PendingCiphertext...)
		record.ActiveNonce = append([]byte(nil), record.PendingNonce...)
		record.ActiveBindingFingerprint = append([]byte(nil), record.PendingBindingFingerprint...)
		record.ActiveKeyVersion = record.PendingKeyVersion
		record.ActiveLastFive = record.PendingLastFive
		record.ActiveSecretRevision = record.PendingSecretRevision
		record.PendingAction = ProviderSecretPendingNone
		record.PendingCiphertext, record.PendingNonce, record.PendingBindingFingerprint = nil, nil, nil
		record.PendingKeyVersion, record.PendingLastFive, record.PendingSecretRevision = 0, "", 0
		s.records[key] = record
	case ProviderSecretPendingClear, ProviderSecretPendingDelete:
		delete(s.records, key)
	default:
		return errors.New("no pending action")
	}
	return nil
}
func (s *fakeProviderSecretStore) RollbackProviderSecretPending(_ context.Context, name, kind string) error {
	key := s.key(name, kind)
	record, ok := s.records[key]
	if !ok {
		return sql.ErrNoRows
	}
	if len(record.ActiveCiphertext) == 0 {
		delete(s.records, key)
		return nil
	}
	record.PendingAction = ProviderSecretPendingNone
	record.PendingCiphertext, record.PendingNonce, record.PendingBindingFingerprint = nil, nil, nil
	record.PendingKeyVersion, record.PendingLastFive, record.PendingSecretRevision = 0, "", 0
	s.records[key] = record
	return nil
}
func (s *fakeProviderSecretStore) DeleteProviderSecret(_ context.Context, name, kind string) error {
	delete(s.records, s.key(name, kind))
	return nil
}

func cloneProviderSecretRecord(record ProviderSecretRecord) ProviderSecretRecord {
	record.ActiveCiphertext = append([]byte(nil), record.ActiveCiphertext...)
	record.ActiveNonce = append([]byte(nil), record.ActiveNonce...)
	record.ActiveBindingFingerprint = append([]byte(nil), record.ActiveBindingFingerprint...)
	record.PendingCiphertext = append([]byte(nil), record.PendingCiphertext...)
	record.PendingNonce = append([]byte(nil), record.PendingNonce...)
	record.PendingBindingFingerprint = append([]byte(nil), record.PendingBindingFingerprint...)
	return record
}

func TestProviderVaultEncryptsAndRestoresSecret(t *testing.T) {
	store := newFakeProviderSecretStore()
	vault := NewProviderVault(store, t.TempDir())
	binding := ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://relay.example/v1", SecretRevision: 1}
	ctx := context.Background()
	if _, err := vault.PrepareSet(ctx, binding, "relay-secret-value"); err != nil {
		t.Fatal(err)
	}
	record, err := store.GetProviderSecret(ctx, "relay", ProviderAPIKeyKind)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.PendingCiphertext) == "relay-secret-value" || len(record.PendingNonce) == 0 {
		t.Fatalf("secret was not encrypted: %+v", record)
	}
	if err := vault.CommitPending(ctx, "relay"); err != nil {
		t.Fatal(err)
	}
	secret, metadata, err := vault.Resolve(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "relay-secret-value" || metadata.LastFive != "value" || metadata.Source != ProviderSecretSourceStored || !metadata.Persisted {
		t.Fatalf("unexpected resolved secret metadata: secret=%q metadata=%+v", secret, metadata)
	}
	info, err := os.Stat(vault.KeyPath())
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not retain Unix 0600 bits; ownership ACLs apply instead.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o, want 600", info.Mode().Perm())
	}
}

type countingProviderSecretStore struct {
	*fakeProviderSecretStore
	lists int
}

func (s *countingProviderSecretStore) ListProviderSecrets(ctx context.Context) ([]ProviderSecretRecord, error) {
	s.lists++
	return s.fakeProviderSecretStore.ListProviderSecrets(ctx)
}

func TestProviderVaultMetadataSnapshotReportsStatusWithoutDecrypt(t *testing.T) {
	store := &countingProviderSecretStore{fakeProviderSecretStore: newFakeProviderSecretStore()}
	vault := NewProviderVault(store, t.TempDir())
	binding := ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://relay.example/v1", SecretRevision: 1}
	other := ProviderBinding{Name: "other", Type: "openai-compatible", BaseURL: "https://other.example/v1", SecretRevision: 1}
	ctx := context.Background()
	if _, err := vault.PrepareSet(ctx, binding, "relay-secret-value"); err != nil {
		t.Fatal(err)
	}
	if err := vault.CommitPending(ctx, binding.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.PrepareSet(ctx, other, "other-secret-value"); err != nil {
		t.Fatal(err)
	}
	if err := vault.CommitPending(ctx, other.Name); err != nil {
		t.Fatal(err)
	}

	snapshot, err := vault.MetadataSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if store.lists != 1 {
		t.Fatalf("MetadataSnapshot listed secrets %d times, want 1", store.lists)
	}
	matched := snapshot.Metadata(binding, ProviderAPIKeyKind)
	if !matched.Configured || !matched.Persisted || matched.LastFive != "value" || matched.Source != ProviderSecretSourceStored {
		t.Fatalf("matching snapshot metadata = %+v", matched)
	}
	mismatched := snapshot.Metadata(ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://changed.example/v1", SecretRevision: 1}, ProviderAPIKeyKind)
	if mismatched.Configured || !mismatched.Persisted || mismatched.Source != ProviderSecretSourceStoredUnavailable {
		t.Fatalf("binding mismatch must be stored_unavailable, got %+v", mismatched)
	}
	if snapshot.Metadata(ProviderBinding{Name: "missing", Type: "openai-compatible", SecretRevision: 1}, ProviderAPIKeyKind).Source != ProviderSecretSourceNone {
		t.Fatal("missing secret must report not configured")
	}

	record, err := store.GetProviderSecret(ctx, binding.Name, ProviderAPIKeyKind)
	if err != nil {
		t.Fatal(err)
	}
	record.ActiveCiphertext[0] ^= 0xff
	store.records[store.key(binding.Name, ProviderAPIKeyKind)] = record
	if _, _, err := vault.Resolve(ctx, binding); !errors.Is(err, ErrProviderSecretTampered) {
		t.Fatalf("tampering error = %v", err)
	}
	if got := snapshot.Metadata(binding, ProviderAPIKeyKind); !got.Configured || got.Source != ProviderSecretSourceStored {
		t.Fatalf("display snapshot must not decrypt, got %+v", got)
	}

	if err := os.Remove(filepath.Clean(vault.KeyPath())); err != nil {
		t.Fatal(err)
	}
	missingKey, err := vault.MetadataSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := missingKey.Metadata(other, ProviderAPIKeyKind)
	if unavailable.Configured || unavailable.Source != ProviderSecretSourceStoredUnavailable {
		t.Fatalf("missing key material must be stored_unavailable, got %+v", unavailable)
	}
}

func TestProviderVaultRejectsBindingMismatchAndTampering(t *testing.T) {
	store := newFakeProviderSecretStore()
	vault := NewProviderVault(store, t.TempDir())
	binding := ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://relay.example/v1", SecretRevision: 1}
	ctx := context.Background()
	if _, err := vault.PrepareSet(ctx, binding, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := vault.CommitPending(ctx, binding.Name); err != nil {
		t.Fatal(err)
	}
	if _, _, err := vault.Resolve(ctx, ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://other.example/v1", SecretRevision: 1}); !errors.Is(err, ErrProviderSecretBindingMismatch) {
		t.Fatalf("binding mismatch error = %v", err)
	}
	record, err := store.GetProviderSecret(ctx, "relay", ProviderAPIKeyKind)
	if err != nil {
		t.Fatal(err)
	}
	record.ActiveCiphertext[0] ^= 0xff
	store.records[store.key("relay", ProviderAPIKeyKind)] = record
	if _, _, err := vault.Resolve(ctx, binding); !errors.Is(err, ErrProviderSecretTampered) {
		t.Fatalf("tampering error = %v", err)
	}
}

func TestProviderVaultDoesNotRegenerateMissingKeyMaterial(t *testing.T) {
	home := t.TempDir()
	store := newFakeProviderSecretStore()
	vault := NewProviderVault(store, home)
	binding := ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://relay.example/v1", SecretRevision: 1}
	ctx := context.Background()
	if _, err := vault.PrepareSet(ctx, binding, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := vault.CommitPending(ctx, binding.Name); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Clean(vault.KeyPath())); err != nil {
		t.Fatal(err)
	}
	fresh := NewProviderVault(store, home)
	if _, _, err := fresh.Resolve(ctx, binding); !errors.Is(err, ErrProviderSecretKeyUnavailable) {
		t.Fatalf("resolve missing key error = %v", err)
	}
	if _, err := fresh.PrepareSet(ctx, binding, "replacement"); !errors.Is(err, ErrProviderSecretKeyUnavailable) {
		t.Fatalf("replacement missing key error = %v", err)
	}
	if _, err := os.Stat(fresh.KeyPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key was unexpectedly regenerated: %v", err)
	}
}

// Reproduces the fresh-install failure where saving request headers before any
// API key ever existed returned HTTP 500: the transport-secret flow prepares a
// proxy-auth clear first, and that ciphertext-free pending row used to trip
// the key-regeneration guard, so the subsequent PrepareSetKind could never
// create the very first vault key (and retries failed forever).
func TestProviderVaultFirstSetSucceedsAfterPreparedClear(t *testing.T) {
	store := newFakeProviderSecretStore()
	vault := NewProviderVault(store, t.TempDir())
	binding := ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://relay.example/v1", SecretRevision: 1, TransportSecretRevision: 1}
	ctx := context.Background()
	if err := vault.PrepareClearKind(ctx, binding, ProviderProxyAuthKind); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.PrepareSetKind(ctx, binding, ProviderRequestHeadersKind, `{"X-Tenant":"value"}`, ""); err != nil {
		t.Fatalf("the first header set after a prepared clear must create the vault key, got %v", err)
	}
	if _, err := os.Stat(vault.KeyPath()); err != nil {
		t.Fatalf("vault key file was not created: %v", err)
	}
	if err := vault.CommitPendingKind(ctx, binding.Name, ProviderProxyAuthKind); err != nil {
		t.Fatal(err)
	}
	if err := vault.CommitPendingKind(ctx, binding.Name, ProviderRequestHeadersKind); err != nil {
		t.Fatal(err)
	}
	if secret, _, err := vault.ResolveKind(ctx, binding, ProviderRequestHeadersKind); err != nil || secret != `{"X-Tenant":"value"}` {
		t.Fatalf("committed header secret unavailable: secret=%q err=%v", secret, err)
	}
}

func TestProviderVaultReconcilesMatchingPendingUpdate(t *testing.T) {
	store := newFakeProviderSecretStore()
	vault := NewProviderVault(store, t.TempDir())
	binding := ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://relay.example/v1", SecretRevision: 2}
	ctx := context.Background()
	if _, err := vault.PrepareSet(ctx, binding, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := vault.ReconcilePending(ctx, map[string]ProviderBinding{"relay": binding}); err != nil {
		t.Fatal(err)
	}
	if _, metadata, err := vault.Resolve(ctx, binding); err != nil || !metadata.Persisted {
		t.Fatalf("reconciled secret unavailable: metadata=%+v err=%v", metadata, err)
	}
}

func TestSecretLastFiveDoesNotExposeShortSecrets(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "empty", secret: "", want: ""},
		{name: "one ASCII", secret: "a", want: ""},
		{name: "five ASCII", secret: "abcde", want: ""},
		{name: "six ASCII", secret: "abcdef", want: "bcdef"},
		{name: "five Unicode", secret: "密钥值甲乙", want: ""},
		{name: "six Unicode", secret: "密钥值甲乙丙", want: "钥值甲乙丙"},
		{name: "mixed Unicode", secret: "ab密钥值甲", want: "b密钥值甲"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SecretLastFive(test.secret); got != test.want {
				t.Fatalf("SecretLastFive(%q) = %q, want %q", test.secret, got, test.want)
			}
		})
	}
}

func TestProviderVaultShortSecretMetadataDoesNotExposeSecret(t *testing.T) {
	store := newFakeProviderSecretStore()
	vault := NewProviderVault(store, t.TempDir())
	binding := ProviderBinding{Name: "relay", Type: "openai-compatible", BaseURL: "https://relay.example/v1", SecretRevision: 1}
	ctx := context.Background()
	metadata, err := vault.PrepareSet(ctx, binding, "短密钥")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.LastFive != "" {
		t.Fatalf("prepare metadata exposed complete short secret: %+v", metadata)
	}
	record, err := store.GetProviderSecret(ctx, binding.Name, ProviderAPIKeyKind)
	if err != nil {
		t.Fatal(err)
	}
	if record.PendingLastFive != "" {
		t.Fatalf("stored metadata exposed complete short secret: %+v", record)
	}
	if err := vault.CommitPending(ctx, binding.Name); err != nil {
		t.Fatal(err)
	}
	secret, metadata, err := vault.Resolve(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "短密钥" || metadata.LastFive != "" {
		t.Fatalf("unexpected resolved short secret metadata: secret=%q metadata=%+v", secret, metadata)
	}
}
