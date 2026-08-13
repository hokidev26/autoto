package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"autoto/internal/config"
)

func TestResolveLocalTokenPersistsAcrossServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AUTOTO_LOCAL_TOKEN", "")

	first := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil)
	if first.localToken == "" {
		t.Fatal("expected non-empty local token")
	}
	path := localAPITokenPath(home)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected persisted token file: %v", err)
	}
	if got := string(data); !containsToken(got, first.localToken) {
		t.Fatalf("token file %q does not contain token", got)
	}

	second := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil)
	if second.localToken != first.localToken {
		t.Fatalf("token changed across restarts: first=%q second=%q", first.localToken, second.localToken)
	}
}

func TestResolveLocalTokenPrefersEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AUTOTO_LOCAL_TOKEN", "env-token-value-1234567890abcdef")
	token := resolveLocalToken(home)
	if token != "env-token-value-1234567890abcdef" {
		t.Fatalf("expected env token, got %q", token)
	}
	if _, err := os.Stat(localAPITokenPath(home)); !os.IsNotExist(err) {
		t.Fatalf("env override should not require a token file, stat err=%v", err)
	}
}

func TestHardenLocalTokenPermissions(t *testing.T) {
	home := t.TempDir()
	path := localAPITokenPath(home)
	if err := writeLocalTokenFile(path, "harden-me-1234567890abcdef"); err != nil {
		t.Fatal(err)
	}
	// The platform implementation (Windows DACL rewrite or POSIX chmod) must
	// succeed for both the secrets dir and the token file.
	if err := hardenTokenPermissions(filepath.Dir(path)); err != nil {
		t.Fatalf("harden secrets dir: %v", err)
	}
	if err := hardenTokenPermissions(path); err != nil {
		t.Fatalf("harden token file: %v", err)
	}
	// The owner must keep access after hardening.
	token, err := loadLocalTokenFile(path)
	if err != nil || token != "harden-me-1234567890abcdef" {
		t.Fatalf("token unreadable after hardening: token=%q err=%v", token, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("expected owner-only token file mode, got %v", got)
		}
	}
	// Rewriting through the tmp-file-and-rename path must still work on the
	// hardened directory.
	if err := writeLocalTokenFile(path, "harden-me-second-0987654321"); err != nil {
		t.Fatalf("rewrite hardened token: %v", err)
	}
	if token, err := loadLocalTokenFile(path); err != nil || token != "harden-me-second-0987654321" {
		t.Fatalf("token unreadable after rewrite: token=%q err=%v", token, err)
	}
}

func containsToken(fileBody, token string) bool {
	return filepath.Base(fileBody) != token && len(token) > 0 && (fileBody == token || fileBody == token+"\n" || len(fileBody) >= len(token) && (fileBody[:len(token)] == token))
}
