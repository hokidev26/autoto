package peercontrol

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestIdentityStoreCreateReload(t *testing.T) {
	home := t.TempDir()
	store, err := NewIdentityStore(home)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if first.Public() != second.Public() {
		t.Fatalf("reloaded identity changed: first=%+v second=%+v", first.Public(), second.Public())
	}
	public := first.Public()
	if public.ProtocolVersion != ProtocolVersion || public.PublicKey == "" || !validFingerprint(public.Fingerprint) {
		t.Fatalf("invalid public identity: %+v", public)
	}
	message := []byte("peercontrol identity test")
	signature, err := first.Sign(message)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Verify(message, signature) || !public.Verify(message, signature) {
		t.Fatal("reloaded identity did not verify signature")
	}
	if second.Verify([]byte("tampered"), signature) {
		t.Fatal("identity verified a tampered message")
	}

	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "seed") || strings.Contains(string(encoded), "private") {
		t.Fatalf("identity JSON leaked private material: %s", encoded)
	}
	if runtime.GOOS != "windows" {
		fileInfo, err := os.Stat(store.Path())
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("identity mode = %04o, want 0600", fileInfo.Mode().Perm())
		}
		directoryInfo, err := os.Stat(filepath.Dir(store.Path()))
		if err != nil {
			t.Fatal(err)
		}
		if directoryInfo.Mode().Perm() != 0o700 {
			t.Fatalf("secrets directory mode = %04o, want 0700", directoryInfo.Mode().Perm())
		}
	}
}

func TestIdentityStoreConcurrentLoadOrCreate(t *testing.T) {
	home := t.TempDir()
	const callers = 32
	fingerprints := make(chan string, callers)
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			store, err := NewIdentityStore(home)
			if err != nil {
				errorsFound <- err
				return
			}
			identity, err := store.LoadOrCreate()
			if err != nil {
				errorsFound <- err
				return
			}
			fingerprints <- identity.Public().Fingerprint
		}()
	}
	wait.Wait()
	close(fingerprints)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	var expected string
	for fingerprint := range fingerprints {
		if expected == "" {
			expected = fingerprint
		}
		if fingerprint != expected {
			t.Fatalf("concurrent identities differ: got %q want %q", fingerprint, expected)
		}
	}
	if expected == "" {
		t.Fatal("no identity returned")
	}
}

func TestIdentityStoreRejectsParentTraversal(t *testing.T) {
	home := t.TempDir()
	traversal := home + string(filepath.Separator) + "child" + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside"
	if _, err := NewIdentityStore(traversal); err == nil {
		t.Fatal("parent traversal in home directory was accepted")
	}
}

func TestIdentityStoreRejectsMalformedAndOversizedFiles(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "malformed", data: []byte(`{"version":1,"algorithm":"Ed25519","seed":"bad"}`)},
		{name: "oversized", data: []byte(strings.Repeat("x", maxIdentityBytes+1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			secrets := filepath.Join(home, "secrets")
			if err := os.Mkdir(secrets, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(secrets, identityFilename)
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := NewIdentityStore(home)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadOrCreate(); err == nil {
				t.Fatal("unsafe identity file was accepted")
			}
		})
	}
}

func TestIdentityStoreRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("Windows symlink creation may require developer mode or elevated privileges")
	}
	externalHome := t.TempDir()
	externalStore, err := NewIdentityStore(externalHome)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := externalStore.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	secrets := filepath.Join(home, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(secrets, identityFilename)
	if err := os.Symlink(externalStore.Path(), link); err != nil {
		t.Skipf("symlink unavailable on %s: %v", runtime.GOOS, err)
	}
	store, err := NewIdentityStore(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOrCreate(); err == nil {
		t.Fatal("symlink identity file was accepted")
	}
	contents, err := os.ReadFile(externalStore.Path())
	if err != nil || !strings.Contains(string(contents), `"seed"`) {
		t.Fatalf("external identity changed: %v", err)
	}
}

func TestIdentityFormattingDoesNotExposeSeed(t *testing.T) {
	store, err := NewIdentityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var file identityFile
	if err := json.Unmarshal(persisted, &file); err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%v %#v %+v", identity, identity, identity)
	if strings.Contains(formatted, file.Seed) {
		t.Fatal("formatted identity leaked seed")
	}
}
