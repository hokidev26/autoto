package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveRejectsUnsupportedFutureSchemaWithoutChangingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte("existing configuration\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := Save(path, Config{SchemaVersion: CurrentConfigVersion + 1})
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(original) {
		t.Fatalf("unsupported schema save modified config: %q", data)
	}
}

func TestSaveAtomicallyReplacesConfigWithPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("old configuration\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Save(path, Config{SchemaVersion: CurrentConfigVersion, Agent: AgentConfig{DefaultModel: "saved-model", SummaryModel: "saved-model"}}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected config mode 0600, got %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"defaultModel": "saved-model"`) {
		t.Fatalf("saved config missing replacement content: %s", data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config.json.tmp-") {
			t.Fatalf("temporary config file was not removed: %s", entry.Name())
		}
	}
}

func TestSavePreservesSecurityDefaultsWhenEnvironmentOverridesAreActive(t *testing.T) {
	t.Setenv("AUTOTO_EXPOSED", "true")
	t.Setenv("AUTOTO_ALLOW_REMOTE_FULL_ACCESS", "true")
	t.Setenv("AUTOTO_DEFAULT_REMOTE_ACCESS_MODE", "full")
	t.Setenv("AUTOTO_ALLOW_REMOTE_NATIVE_PICKER", "true")

	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Config{SchemaVersion: CurrentConfigVersion}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Security.Exposed || !loaded.Security.AllowRemoteFullAccess || loaded.Security.DefaultRemoteAccessMode != "full" || !loaded.Security.AllowRemoteNativePicker {
		t.Fatalf("expected environment overrides at runtime, got %+v", loaded.Security)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"exposed": true`) || strings.Contains(string(data), `"allowRemoteFullAccess": true`) || strings.Contains(string(data), `"defaultRemoteAccessMode": "full"`) || strings.Contains(string(data), `"allowRemoteNativePicker": true`) {
		t.Fatalf("environment-derived security policy persisted: %s", data)
	}
}
