package server

import (
	"os"
	"path/filepath"
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

func containsToken(fileBody, token string) bool {
	return filepath.Base(fileBody) != token && len(token) > 0 && (fileBody == token || fileBody == token+"\n" || len(fileBody) >= len(token) && (fileBody[:len(token)] == token))
}
