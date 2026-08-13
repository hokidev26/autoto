package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestNormalizesAndHashes(t *testing.T) {
	root := t.TempDir()
	writePluginCommand(t, root, "bin/plugin")
	writeManifest(t, root, map[string]any{
		"apiVersion":  APIVersionV1Alpha1,
		"transport":   TransportStdio,
		"slug":        " My_Plugin ",
		"name":        " Demo Plugin ",
		"version":     " 1.2.3 ",
		"description": " demo ",
		"command":     "bin/plugin",
		"args":        []string{"--mode", "test"},
		"env":         map[string]string{"LOG_LEVEL": "debug"},
		"secretRefs":  map[string]string{"API_TOKEN": "env:DEMO_API_TOKEN"},
	})
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Slug != "my-plugin" || manifest.Name != "Demo Plugin" || manifest.Version != "1.2.3" || manifest.Command != "bin/plugin" {
		t.Fatalf("unexpected normalized manifest: %+v", manifest)
	}
	if !filepath.IsAbs(manifest.RootPath) || len(manifest.Hash) != 64 || manifest.SecretRefs["API_TOKEN"] != "env:DEMO_API_TOKEN" {
		t.Fatalf("incomplete normalized manifest: %+v", manifest)
	}
	firstHash := manifest.Hash
	writeManifest(t, root, map[string]any{
		"secretRefs": map[string]string{"API_TOKEN": "env:DEMO_API_TOKEN"},
		"env":        map[string]string{"LOG_LEVEL": "debug"}, "args": []string{"--mode", "test"}, "command": "bin/plugin",
		"description": "demo", "version": "1.2.3", "name": "Demo Plugin", "slug": "my-plugin",
		"transport": TransportStdio, "apiVersion": APIVersionV1Alpha1,
	})
	second, err := ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Hash != firstHash {
		t.Fatalf("semantic formatting changed hash: %s != %s", second.Hash, firstHash)
	}
}

func TestManifestRejectsUnsafeCommandPaths(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, command := range map[string]string{"absolute": outside, "traversal": "../outside"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeManifest(t, root, validManifest(command))
			if _, err := LoadManifest(root); err == nil {
				t.Fatalf("expected unsafe command %q to fail", command)
			}
		})
	}
	t.Run("symlink escape", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "plugin")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		writeManifest(t, root, validManifest("plugin"))
		if _, err := LoadManifest(root); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected symlink escape rejection, got %v", err)
		}
	})
}

func TestManifestLimitsAndEnvironmentValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong api", func(m map[string]any) { m["apiVersion"] = "v1" }},
		{"wrong transport", func(m map[string]any) { m["transport"] = "http" }},
		{"too many args", func(m map[string]any) { m["args"] = make([]string, MaxManifestArgs+1) }},
		{"long arg", func(m map[string]any) { m["args"] = []string{strings.Repeat("x", MaxManifestArgBytes+1)} }},
		{"sensitive env", func(m map[string]any) { m["env"] = map[string]string{"API_TOKEN": "raw-secret"} }},
		{"bad secret ref", func(m map[string]any) { m["secretRefs"] = map[string]string{"API_TOKEN": "raw-secret"} }},
		{"duplicate env key", func(m map[string]any) {
			m["env"] = map[string]string{"VALUE": "x"}
			m["secretRefs"] = map[string]string{"VALUE": "env:VALUE_SECRET"}
		}},
		{"unknown field", func(m map[string]any) { m["extra"] = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePluginCommand(t, root, "plugin")
			manifest := validManifest("plugin")
			test.mutate(manifest)
			writeManifest(t, root, manifest)
			if _, err := LoadManifest(root); err == nil {
				t.Fatal("expected invalid manifest to fail")
			}
		})
	}
}

func TestManifestTimeoutSecondsValidation(t *testing.T) {
	for _, valid := range []int{1, 600} {
		root := t.TempDir()
		writePluginCommand(t, root, "plugin")
		manifest := validManifest("plugin")
		manifest["timeoutSeconds"] = valid
		writeManifest(t, root, manifest)
		loaded, err := LoadManifest(root)
		if err != nil {
			t.Fatalf("timeoutSeconds=%d rejected: %v", valid, err)
		}
		if loaded.TimeoutSeconds != valid {
			t.Fatalf("timeoutSeconds=%d not adopted: %+v", valid, loaded)
		}
	}
	for name, value := range map[string]any{"negative": -1, "above maximum": 601, "fractional": 1.5} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writePluginCommand(t, root, "plugin")
			manifest := validManifest("plugin")
			manifest["timeoutSeconds"] = value
			writeManifest(t, root, manifest)
			if _, err := LoadManifest(root); err == nil {
				t.Fatalf("expected timeoutSeconds=%v to fail validation", value)
			}
		})
	}
}

// TestManifestHashStableWithoutTimeoutSeconds proves a manifest that omits
// timeoutSeconds keeps the exact hash produced before the field existed, so
// already-installed plugins are not forced to reinstall.
func TestManifestHashStableWithoutTimeoutSeconds(t *testing.T) {
	root := t.TempDir()
	writePluginCommand(t, root, "plugin")
	writeManifest(t, root, validManifest("plugin"))
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TimeoutSeconds != 0 {
		t.Fatalf("absent timeoutSeconds must normalize to 0: %+v", manifest)
	}
	// Canonical form predating the timeoutSeconds field.
	legacy := struct {
		APIVersion  string            `json:"apiVersion"`
		Transport   string            `json:"transport"`
		Slug        string            `json:"slug"`
		Name        string            `json:"name"`
		Version     string            `json:"version"`
		Description string            `json:"description"`
		Command     string            `json:"command"`
		Args        []string          `json:"args"`
		Env         map[string]string `json:"env"`
		SecretRefs  map[string]string `json:"secretRefs"`
	}{manifest.APIVersion, manifest.Transport, manifest.Slug, manifest.Name, manifest.Version, manifest.Description, manifest.Command, manifest.Args, manifest.Env, manifest.SecretRefs}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	if want := hex.EncodeToString(sum[:]); manifest.Hash != want {
		t.Fatalf("manifest without timeoutSeconds changed hash: got %s want %s", manifest.Hash, want)
	}
	withTimeout := validManifest("plugin")
	withTimeout["timeoutSeconds"] = 30
	writeManifest(t, root, withTimeout)
	loaded, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Hash == manifest.Hash {
		t.Fatal("a set timeoutSeconds must be covered by the canonical hash")
	}
}

func TestManifestMaximumSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ManifestFilename), []byte(strings.Repeat(" ", MaxManifestBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(root); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size rejection, got %v", err)
	}
}

func validManifest(command string) map[string]any {
	return map[string]any{"apiVersion": APIVersionV1Alpha1, "transport": TransportStdio, "slug": "demo", "name": "Demo", "version": "1.0.0", "command": command}
}

func writePluginCommand(t *testing.T, root, relative string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeManifest(t *testing.T, root string, manifest map[string]any) {
	t.Helper()
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestFilename), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
