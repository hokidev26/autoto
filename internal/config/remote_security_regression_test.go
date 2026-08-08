package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteSecurityRegressionEnvironmentOverridesDoNotReplacePersistedPasswordHash(t *testing.T) {
	for _, setting := range []struct {
		name  string
		value string
	}{
		{name: "AUTOTO_EXPOSED", value: "true"},
		{name: "AUTOTO_ACCESS_PASSWORD", value: "Environment-Only-Remote-Password-1!"},
		{name: "AUTOTO_ALLOW_REMOTE_FULL_ACCESS", value: "true"},
		{name: "AUTOTO_DEFAULT_REMOTE_ACCESS_MODE", value: "full"},
		{name: "AUTOTO_ALLOW_REMOTE_NATIVE_PICKER", value: "true"},
	} {
		t.Setenv(setting.name, setting.value)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	persistedSecurity := SecurityConfig{
		Exposed:                 false,
		AccessPasswordHash:      "sha256-bcrypt-v1$persisted-hash-placeholder",
		AllowRemoteFullAccess:   false,
		DefaultRemoteAccessMode: "restricted",
		AllowRemoteNativePicker: false,
		CredentialRevision:      9,
	}
	initial := Config{
		SchemaVersion: CurrentConfigVersion,
		Agent:         AgentConfig{DefaultModel: "before-save", SummaryModel: "before-save"},
		Security:      persistedSecurity,
	}
	if err := Save(path, initial); err != nil {
		t.Fatal(err)
	}

	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.Security.Exposed || runtime.Security.AccessPassword != "" || runtime.Security.AccessPasswordHash != persistedSecurity.AccessPasswordHash || !runtime.Security.AllowRemoteFullAccess || runtime.Security.DefaultRemoteAccessMode != "full" || !runtime.Security.AllowRemoteNativePicker {
		t.Fatalf("environment security overrides were not active without replacing the persisted password hash: %+v", runtime.Security)
	}

	// Simulate an unrelated settings update made while environment overrides are active.
	runtime.Agent.DefaultModel = "after-unrelated-save"
	runtime.Agent.SummaryModel = "after-unrelated-save"
	if err := Save(path, runtime); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk Config
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Agent.DefaultModel != "after-unrelated-save" {
		t.Fatalf("unrelated update was not persisted: %+v", disk.Agent)
	}
	if disk.Security.Exposed != persistedSecurity.Exposed || disk.Security.AllowRemoteFullAccess != persistedSecurity.AllowRemoteFullAccess || disk.Security.DefaultRemoteAccessMode != persistedSecurity.DefaultRemoteAccessMode || disk.Security.AllowRemoteNativePicker != persistedSecurity.AllowRemoteNativePicker || disk.Security.CredentialRevision != persistedSecurity.CredentialRevision || disk.Security.AccessPasswordHash != persistedSecurity.AccessPasswordHash {
		t.Fatalf("environment-derived remote security policy leaked into saved config: %+v", disk.Security)
	}
	if disk.Security.AccessPassword != "" {
		t.Fatalf("environment password must never persist: %+v", disk.Security)
	}
}
