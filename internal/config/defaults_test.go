package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"autoto/internal/compat"
)

func TestDefaultConfig(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != CurrentConfigVersion {
		t.Fatalf("expected config version %d, got %d", CurrentConfigVersion, cfg.SchemaVersion)
	}
	if cfg.Server.Port != 16888 {
		t.Fatalf("expected default port 16888, got %d", cfg.Server.Port)
	}
	if cfg.Gateway.Enabled || cfg.Gateway.Host != "127.0.0.1" || cfg.Gateway.Port != 8788 || cfg.Gateway.MaxGlobalConcurrency != 16 || cfg.Gateway.MaxRequestBytes != 8<<20 {
		t.Fatalf("unexpected gateway defaults: %+v", cfg.Gateway)
	}
	if filepath.Base(cfg.Paths.HomeDir) != ".autoto" || filepath.Base(cfg.Paths.DatabasePath) != "autoto.db" {
		t.Fatalf("expected Autoto default paths, got %+v", cfg.Paths)
	}
	if cfg.Agent.DefaultPermissionMode == "" {
		t.Fatal("expected default permission mode")
	}
	if cfg.Agent.ReviewModel != cfg.Agent.DefaultModel {
		t.Fatalf("expected review model to default to agent model, got review=%q default=%q", cfg.Agent.ReviewModel, cfg.Agent.DefaultModel)
	}
	if cfg.Agent.ContextTokenLimit <= 0 {
		t.Fatalf("expected positive context token limit, got %d", cfg.Agent.ContextTokenLimit)
	}
	if cfg.Agent.AutoContinuationMode != "safe" || cfg.Agent.ContinuationSegmentTurns != 40 || cfg.Agent.MaxContinuations != 8 || cfg.Agent.MaxTotalTurns != 200 || cfg.Agent.MaxRunDurationMs != 3600000 || cfg.Agent.MaxRunTokens != 500000 {
		t.Fatalf("unexpected continuation defaults: %+v", cfg.Agent)
	}
	if cfg.Security.Exposed || cfg.Security.AccessPassword != "" {
		t.Fatalf("expected local security defaults, got %+v", cfg.Security)
	}
	gemini := providerByName(cfg, "gemini")
	if gemini == nil || gemini.Type != "gemini-interactions" || gemini.BaseURL != "https://generativelanguage.googleapis.com/v1beta/interactions" || gemini.Model != "gemini-2.5-pro" {
		t.Fatalf("unexpected Gemini provider preset: %+v", gemini)
	}
	provider := providerByName(cfg, "codex")
	if provider == nil {
		t.Fatal("expected native Codex provider preset")
	}
	if provider.Type != ProviderTypeCodex || provider.BaseURL != "https://chatgpt.com/backend-api/codex" || provider.Model != "gpt-5.5" || provider.APIKeyOptional {
		t.Fatalf("unexpected native Codex provider preset: %+v", *provider)
	}
}

func TestContextManagementDefaultsNormalizeAndPersist(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ContextManagement
	if got.CompactKeepTurns != 2 || got.MinPrunePercent != 30 || got.MaxPrunePercent != 80 || got.Standard.PruneStart != 95 || got.Standard.CompactStart != 99 || got.Large.PruneStart != 95 || got.Large.CompactStart != 99 {
		t.Fatalf("unexpected context management defaults: %+v", got)
	}
	if got.WindowForLimit(600000) != got.Standard || got.WindowForLimit(600001) != got.Large {
		t.Fatalf("unexpected context window classification: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	cfg.ContextManagement = ContextManagementConfig{CompactKeepTurns: 0, MinPrunePercent: 90, MaxPrunePercent: 40, Standard: ContextManagementWindowConfig{PruneStart: 101, CompactStart: 1}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContextManagement.CompactKeepTurns != 2 || loaded.ContextManagement.MinPrunePercent != 40 || loaded.ContextManagement.MaxPrunePercent != 40 || loaded.ContextManagement.Standard.PruneStart != 100 || loaded.ContextManagement.Standard.CompactStart != 1 {
		t.Fatalf("unexpected normalized persisted context settings: %+v", loaded.ContextManagement)
	}
}

func TestDefaultConfigHomeUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	for _, test := range []struct {
		name      string
		precreate bool
	}{
		{name: "fresh"},
		{name: "existing permissive directory", precreate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("USERPROFILE", home)

			appHome := filepath.Join(home, ".autoto")
			if test.precreate {
				if err := os.Mkdir(appHome, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(appHome, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(""); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(appHome)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Fatalf("default app home permissions = %04o, want 0700", got)
			}
			configInfo, err := os.Stat(filepath.Join(appHome, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := configInfo.Mode().Perm(); got != 0o600 {
				t.Fatalf("default config permissions = %04o, want 0600", got)
			}
		})
	}
}

func TestDefaultConfigHomeRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation may require elevated privileges")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	target := filepath.Join(home, "redirected")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".autoto")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(""); err == nil {
		t.Fatal("default app home symlink was accepted")
	}
}

func TestSaveDoesNotChangeExistingCustomParentPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	parent := filepath.Join(t.TempDir(), "custom")
	if err := os.Mkdir(parent, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(parent, "config.json"), Config{SchemaVersion: CurrentConfigVersion}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("custom config parent permissions = %04o, want 0750", got)
	}
}

func TestCodeHarborEnvFallbacksRemainSupported(t *testing.T) {
	t.Setenv("CODEHARBOR_DEFAULT_MODEL", "legacy:default")
	t.Setenv("CODEHARBOR_SUMMARY_MODEL", "legacy:summary")
	t.Setenv("CODEHARBOR_CONTEXT_TOKEN_LIMIT", "23456")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DefaultModel != "legacy:default" || cfg.Agent.SummaryModel != "legacy:summary" || cfg.Agent.ContextTokenLimit != 23456 {
		t.Fatalf("expected legacy agent env fallbacks, got %+v", cfg.Agent)
	}
}

func TestContextTokenLimitFromEnv(t *testing.T) {
	t.Setenv("CODEHARBOR_CONTEXT_TOKEN_LIMIT", "12345")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ContextTokenLimit != 12345 {
		t.Fatalf("expected env context token limit, got %d", cfg.Agent.ContextTokenLimit)
	}
}

func TestAutotoEnvTakesPriorityOverCodeHarborEnv(t *testing.T) {
	t.Setenv("AUTOTO_DEFAULT_MODEL", "autoto:default")
	t.Setenv("CODEHARBOR_DEFAULT_MODEL", "legacy:default")
	t.Setenv("AUTOTO_SUMMARY_MODEL", "autoto:summary")
	t.Setenv("CODEHARBOR_SUMMARY_MODEL", "legacy:summary")
	t.Setenv("AUTOTO_CONTEXT_TOKEN_LIMIT", "54321")
	t.Setenv("CODEHARBOR_CONTEXT_TOKEN_LIMIT", "12345")
	t.Setenv("AUTOTO_EXPOSED", "false")
	t.Setenv("CODEHARBOR_EXPOSED", "true")
	t.Setenv("AUTOTO_ACCESS_PASSWORD", "autoto-secret")
	t.Setenv("CODEHARBOR_ACCESS_PASSWORD", "legacy-secret")
	t.Setenv("AUTOTO_REMOTE_TERMINAL", "true")
	t.Setenv("CODEHARBOR_REMOTE_TERMINAL", "false")
	t.Setenv("AUTOTO_AGENT_BACKEND_URL", "http://127.0.0.1:9000/")
	t.Setenv("CODEHARBOR_AGENT_BACKEND_URL", "http://127.0.0.1:8000/")
	t.Setenv("AUTOTO_AGENT_BACKEND_NAME", "Autoto Backend")
	t.Setenv("CODEHARBOR_AGENT_BACKEND_NAME", "Legacy Backend")
	t.Setenv("AUTOTO_AGENT_BACKEND_KIND", "cloud")
	t.Setenv("CODEHARBOR_AGENT_BACKEND_KIND", "local")
	t.Setenv("AUTOTO_AGENT_BACKEND_API_KEY", "autoto-key")
	t.Setenv("CODEHARBOR_AGENT_BACKEND_API_KEY", "legacy-key")

	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DefaultModel != "autoto:default" || cfg.Agent.SummaryModel != "autoto:summary" || cfg.Agent.ContextTokenLimit != 54321 {
		t.Fatalf("expected canonical agent env values, got %+v", cfg.Agent)
	}
	if cfg.Security.Exposed || cfg.Security.AccessPassword != "autoto-secret" || !cfg.Security.AllowRemoteTerminal {
		t.Fatalf("expected canonical security env values, got %+v", cfg.Security)
	}
	if len(cfg.Backends.Instances) != 1 {
		t.Fatalf("expected one canonical backend, got %+v", cfg.Backends.Instances)
	}
	backend := cfg.Backends.Instances[0]
	if backend.BaseURL != "http://127.0.0.1:9000" || backend.Name != "Autoto Backend" || backend.Kind != "cloud" || backend.APIKey != "autoto-key" {
		t.Fatalf("expected canonical backend env values, got %+v", backend)
	}
}

func TestSecurityConfigFromEnv(t *testing.T) {
	t.Setenv("CODEHARBOR_EXPOSED", "true")
	t.Setenv("CODEHARBOR_ACCESS_PASSWORD", "remote-secret")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Security.Exposed || cfg.Security.AccessPassword != "remote-secret" || cfg.Security.AllowRemoteTerminal {
		t.Fatalf("expected security env overrides without remote terminal, got %+v", cfg.Security)
	}
	t.Setenv("CODEHARBOR_REMOTE_TERMINAL", "true")
	cfg, err = Default()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Security.AllowRemoteTerminal {
		t.Fatalf("expected remote terminal env override, got %+v", cfg.Security)
	}
}

func TestPersistedAccessPasswordHashTakesPrecedenceOverEnvironment(t *testing.T) {
	t.Setenv("AUTOTO_ACCESS_PASSWORD", "environment-secret")
	t.Setenv("CODEHARBOR_ACCESS_PASSWORD", "legacy-environment-secret")
	hash, err := HashAccessPassword("stored-secret")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Config{Security: SecurityConfig{AccessPasswordHash: hash}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.AccessPassword != "" || !VerifyAccessPassword(cfg.Security.AccessPasswordHash, "stored-secret") {
		t.Fatalf("expected persisted hash to remain authoritative, got %+v", cfg.Security)
	}
	if VerifyAccessPassword(cfg.Security.AccessPasswordHash, "environment-secret") {
		t.Fatal("environment password must not replace the persisted local password")
	}
}

func TestLoadBackfillsLegacyConfigVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "server": {"host": "127.0.0.1", "port": 9000},
  "paths": {"homeDir": "/tmp/codeharbor", "databasePath": "/tmp/codeharbor/db.sqlite", "defaultProjectDir": "/tmp/codeharbor/projects"},
  "agent": {"defaultModel": "openai:test", "summaryModel": "openai:test", "defaultPermissionMode": "acceptEdits", "maxTurns": 3, "contextTokenLimit": 1000},
  "auth": {"registrationOpen": true},
  "providers": {"instances": []},
  "backends": {"instances": []}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != CurrentConfigVersion {
		t.Fatalf("expected legacy config version backfill to %d, got %d", CurrentConfigVersion, cfg.SchemaVersion)
	}
	if cfg.Server.Port != 9000 {
		t.Fatalf("expected loaded legacy server port, got %d", cfg.Server.Port)
	}
}

func TestLoadMigratesLegacyConfigToCanonicalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	legacyDir := filepath.Join(home, ".codeharbor")
	legacyPath := filepath.Join(legacyDir, "config.json")
	legacyDatabasePath := filepath.Join(legacyDir, "codeharbor.db")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyData, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"server":  map[string]any{"host": "127.0.0.1", "port": 9091},
		"paths":   map[string]any{"homeDir": legacyDir, "databasePath": legacyDatabasePath, "defaultProjectDir": filepath.Join(home, "projects")},
		"agent":   map[string]any{"defaultModel": "openai:legacy", "summaryModel": "openai:legacy", "defaultPermissionMode": "acceptEdits", "maxTurns": 3, "contextTokenLimit": 1000},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	legacyData = append(legacyData, '\n')
	if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	canonicalPath, err := ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, report, err := LoadWithReport(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasLegacy(report, "~/.codeharbor/config.json") {
		t.Fatalf("expected copied legacy config report, got %+v", report.Usages)
	}
	if cfg.Server.Port != 9091 || cfg.Paths.DatabasePath != legacyDatabasePath {
		t.Fatalf("expected migrated legacy values and database path, got %+v", cfg)
	}
	canonicalData, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonicalData) != string(legacyData) {
		t.Fatalf("expected byte-for-byte config copy, got %q", canonicalData)
	}
	if _, err := os.Stat(legacyDatabasePath); !os.IsNotExist(err) {
		t.Fatalf("migration must not create or move the legacy database, stat err=%v", err)
	}
	if legacyAfter, err := os.ReadFile(legacyPath); err != nil || string(legacyAfter) != string(legacyData) {
		t.Fatalf("expected legacy config to remain unchanged, err=%v", err)
	}
}

func TestLoadExplicitPathDoesNotMigrateLegacyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	legacyDir := filepath.Join(home, ".codeharbor")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "config.json"), []byte(`{"server":{"port":9091}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	explicitPath := filepath.Join(home, "custom", "config.json")
	cfg, err := Load(explicitPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 16888 {
		t.Fatalf("expected explicit path to use new defaults, got port %d", cfg.Server.Port)
	}
	if _, err := os.Stat(explicitPath); err != nil {
		t.Fatalf("expected explicit config to be created: %v", err)
	}
}

func TestNormalizeGatewayConfigBounds(t *testing.T) {
	defaults := normalizeGatewayConfig(GatewayConfig{})
	if defaults.Host != "127.0.0.1" || defaults.Port != 8788 || defaults.MaxGlobalConcurrency != 16 || defaults.MaxRequestBytes != 8<<20 {
		t.Fatalf("unexpected gateway fallback: %+v", defaults)
	}
	bounded := normalizeGatewayConfig(GatewayConfig{Host: " 0.0.0.0 ", Port: 70000, MaxGlobalConcurrency: 5000, MaxRequestBytes: 1})
	if bounded.Host != "0.0.0.0" || bounded.Port != 8788 || bounded.MaxGlobalConcurrency != 1024 || bounded.MaxRequestBytes != 1<<10 {
		t.Fatalf("unexpected gateway bounds: %+v", bounded)
	}
}

func TestNormalizeAgentConfigDefaultsReviewModelToDefaultModel(t *testing.T) {
	got := normalizeAgentConfig(AgentConfig{DefaultModel: " openai:review-target ", ReviewModel: "   "})
	if got.DefaultModel != "openai:review-target" || got.ReviewModel != "openai:review-target" {
		t.Fatalf("expected trimmed default model fallback for reviewer, got %+v", got)
	}
}

func TestNormalizeAgentConfigContinuationBounds(t *testing.T) {
	got := normalizeAgentConfig(AgentConfig{
		AutoContinuationMode:     "unexpected",
		ContinuationSegmentTurns: 5000,
		MaxContinuations:         100,
		MaxTotalTurns:            12,
		MaxRunDurationMs:         10,
		MaxRunTokens:             10,
	})
	if got.AutoContinuationMode != "safe" || got.ContinuationSegmentTurns != 12 || got.MaxContinuations != 64 || got.MaxTotalTurns != 12 || got.MaxRunDurationMs != 1000 || got.MaxRunTokens != 1000 {
		t.Fatalf("unexpected normalized continuation bounds: %+v", got)
	}
	off := normalizeAgentConfig(AgentConfig{AutoContinuationMode: " OFF ", MaxContinuations: -1})
	if off.AutoContinuationMode != "off" || off.MaxContinuations != 0 {
		t.Fatalf("expected explicit off and zero continuation budget, got %+v", off)
	}
}

func TestMigrateConfigBackfillsLegacyVersion(t *testing.T) {
	cfg := migrateConfig(Config{})
	if cfg.SchemaVersion != CurrentConfigVersion {
		t.Fatalf("expected legacy config to migrate to %d, got %d", CurrentConfigVersion, cfg.SchemaVersion)
	}
}

func TestMigrateConfigKeepsFutureVersion(t *testing.T) {
	cfg := migrateConfig(Config{SchemaVersion: CurrentConfigVersion + 1})
	if cfg.SchemaVersion != CurrentConfigVersion+1 {
		t.Fatalf("expected future config version to be preserved, got %d", cfg.SchemaVersion)
	}
}

func TestDefaultBackendsFromEnv(t *testing.T) {
	t.Setenv("CODEHARBOR_AGENT_BACKEND_URL", "http://127.0.0.1:8000/")
	t.Setenv("CODEHARBOR_AGENT_BACKEND_API_KEY", "secret")

	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Backends.Instances) != 1 {
		t.Fatalf("expected one backend, got %d", len(cfg.Backends.Instances))
	}
	backend := cfg.Backends.Instances[0]
	if backend.BaseURL != "http://127.0.0.1:8000" || backend.APIKey != "secret" || !backend.Active {
		t.Fatalf("unexpected backend seed: %+v", backend)
	}
}

func TestLoadWritesDefaultConfigWithoutEnvSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("GEMINI_API_KEY", "gemini-secret")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "compatible-secret")
	t.Setenv("CLIPROXYAPI_API_KEY", "cliproxy-secret")
	t.Setenv("CODEHARBOR_AGENT_BACKEND_URL", "http://127.0.0.1:8000")
	t.Setenv("CODEHARBOR_AGENT_BACKEND_API_KEY", "backend-secret")
	t.Setenv("CODEHARBOR_ACCESS_PASSWORD", "remote-access-secret")

	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	expectedRuntimeKeys := map[string]string{
		"openai":            "openai-secret",
		"anthropic":         "anthropic-secret",
		"gemini":            "gemini-secret",
		"cliproxyapi":       "cliproxy-secret",
		"openai-compatible": "compatible-secret",
	}
	for _, provider := range cfg.Providers.Instances {
		if expected, ok := expectedRuntimeKeys[provider.Name]; ok && provider.APIKey != expected {
			t.Fatalf("expected runtime config to keep %s env secret, got %q", provider.Name, provider.APIKey)
		}
	}
	if len(cfg.Backends.Instances) != 1 || cfg.Backends.Instances[0].APIKey != "backend-secret" {
		t.Fatal("expected runtime config to keep backend env secret")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"openai-secret", "anthropic-secret", "gemini-secret", "cliproxy-secret", "compatible-secret", "backend-secret", "remote-access-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("persisted config contains secret %q", secret)
		}
	}

	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.SchemaVersion != CurrentConfigVersion {
		t.Fatalf("expected persisted config version %d, got %d", CurrentConfigVersion, persisted.SchemaVersion)
	}
	for _, provider := range persisted.Providers.Instances {
		if provider.APIKey != "" {
			t.Fatalf("expected persisted provider api key to be empty for %s", provider.Name)
		}
	}
	for _, backend := range persisted.Backends.Instances {
		if backend.APIKey != "" {
			t.Fatalf("expected persisted backend api key to be empty for %s", backend.Name)
		}
	}
	if persisted.Security.AccessPassword != "" {
		t.Fatal("expected persisted remote access password to be empty")
	}
}

func TestProviderProxyURLWithoutCredentialsFailsClosedForMalformedUserinfo(t *testing.T) {
	if got := providerProxyURLWithoutCredentials("proxy-user:proxy-pass@proxy.internal:7890"); got != "" {
		t.Fatalf("malformed proxy userinfo was retained: %q", got)
	}
}

func TestProviderConfigSaveNeverPersistsProxyCredentialsOrHeaderValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{Providers: ProvidersConfig{Instances: []ProviderConfig{{
		Name:     "relay",
		Type:     "openai-compatible",
		BaseURL:  "https://relay.example/v1",
		Model:    "relay-model",
		ProxyURL: "http://proxy-user:proxy-pass@127.0.0.1:7890",
		RequestHeaders: []ProviderRequestHeader{{
			Name:  "X-Tenant",
			Value: "tenant-secret",
		}},
	}}}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"proxy-user", "proxy-pass", "tenant-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("persisted provider config leaked %q: %s", secret, data)
		}
	}
	if !strings.Contains(string(data), `"proxyUrl": "http://127.0.0.1:7890"`) || !strings.Contains(string(data), `"name": "X-Tenant"`) {
		t.Fatalf("non-secret transport metadata was not preserved: %s", data)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := providerByName(loaded, "relay")
	if provider == nil || provider.ProxyURL != "http://127.0.0.1:7890" || len(provider.RequestHeaders) != 1 || provider.RequestHeaders[0].Value != "" {
		t.Fatalf("unexpected reloaded provider transport metadata: %+v", provider)
	}
}

func TestProviderDisabledStateIsBackwardCompatibleAndSummaryIsServerDerived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"instances":[{"name":"openai","type":"openai","model":"gpt-test"},{"name":"relay","type":"openai-compatible","baseUrl":"http://127.0.0.1:8080/v1","model":"relay-test","disabled":true}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	openAI := providerByName(cfg, "openai")
	relay := providerByName(cfg, "relay")
	if openAI == nil || openAI.Disabled {
		t.Fatalf("legacy provider without disabled must remain enabled: %+v", openAI)
	}
	if relay == nil || !relay.Disabled {
		t.Fatalf("expected disabled provider state to load: %+v", relay)
	}
	if got := openAI.Summary(); !got.Enabled || got.Origin != ProviderOriginBuiltin {
		t.Fatalf("unexpected built-in summary: %+v", got)
	}
	if got := relay.Summary(); got.Enabled || got.Origin != ProviderOriginCustom {
		t.Fatalf("unexpected custom summary: %+v", got)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"disabled": true`) {
		t.Fatalf("disabled state was not persisted: %s", data)
	}
	if strings.Contains(string(data), `"origin"`) {
		t.Fatalf("provider origin must be server-derived, not persisted: %s", data)
	}
}

func TestProviderRuntimeIdentityIsNotSerialized(t *testing.T) {
	provider := ProviderConfig{
		Name:           "openai",
		Type:           "openai",
		Model:          "gpt-5",
		ClientVersion:  "1.2.3",
		InstallationID: "123e4567-e89b-42d3-a456-426614174000",
	}
	encoded, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"clientVersion", "installationId", "1.2.3", provider.InstallationID} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("runtime identity leaked into provider JSON: %s", text)
		}
	}

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Config{SchemaVersion: CurrentConfigVersion, Providers: ProvidersConfig{Instances: []ProviderConfig{provider}}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"clientVersion", "installationId", "1.2.3", provider.InstallationID} {
		if strings.Contains(string(persisted), forbidden) {
			t.Fatalf("runtime identity leaked into saved config: %s", persisted)
		}
	}
}

func TestNormalizeProvidersDerivesLegacyCLIProxyProfile(t *testing.T) {
	providers := normalizeProviders(ProvidersConfig{Instances: []ProviderConfig{{
		Name: "cliproxyapi",
		Type: "openai-compatible",
	}}})
	if len(providers.Instances) != 1 || providers.Instances[0].Profile != ProviderProfileCLIProxyAPI {
		t.Fatalf("expected legacy profile derivation, got %+v", providers.Instances)
	}
}

func TestNormalizeProvidersPreservesExplicitProfile(t *testing.T) {
	providers := normalizeProviders(ProvidersConfig{Instances: []ProviderConfig{{
		Name:    "local-codex",
		Type:    "openai-compatible",
		Profile: ProviderProfileCLIProxyAPI,
	}}})
	if len(providers.Instances) != 1 || providers.Instances[0].Profile != ProviderProfileCLIProxyAPI {
		t.Fatalf("expected explicit profile to remain intact, got %+v", providers.Instances)
	}
}

func TestNormalizeProvidersClearsOAuthGatewayEligibility(t *testing.T) {
	providers := normalizeProviders(ProvidersConfig{Instances: []ProviderConfig{
		{Name: "codex", Type: "CoDeX", GatewayEnabled: true},
		{Name: "proxy", Type: "openai-compatible", Profile: ProviderProfileCLIProxyAPI, GatewayEnabled: true},
		{Name: "relay", Type: "openai-compatible", GatewayEnabled: true},
	}})
	if providers.Instances[0].GatewayEnabled || providers.Instances[1].GatewayEnabled || !providers.Instances[2].GatewayEnabled {
		t.Fatalf("unexpected normalized Gateway eligibility: %+v", providers.Instances)
	}
}

func TestNormalizeProviderModelsSupportsLegacyDefaultsAndBoundsLimits(t *testing.T) {
	provider := NormalizeProviderConfig(ProviderConfig{
		Name:  "relay",
		Type:  "openai-compatible",
		Model: " default-model ",
		Models: []ProviderModelConfig{
			{Name: " model-a ", ContextTokenLimit: -1},
			{Name: "model-a", ContextTokenLimit: 9000},
			{Name: "model-b", ContextTokenLimit: ProviderModelContextTokenLimitMax + 1},
			{Name: "   ", ContextTokenLimit: 100},
		},
	})
	if provider.Model != "default-model" {
		t.Fatalf("default model was not normalized: %q", provider.Model)
	}
	want := []ProviderModelConfig{
		{Name: "model-a", ContextTokenLimit: 0},
		{Name: "model-b", ContextTokenLimit: ProviderModelContextTokenLimitMax},
		{Name: "default-model", ContextTokenLimit: 0},
	}
	if len(provider.Models) != len(want) {
		t.Fatalf("unexpected normalized models: %+v", provider.Models)
	}
	for i := range want {
		if provider.Models[i] != want[i] {
			t.Fatalf("model %d = %+v, want %+v", i, provider.Models[i], want[i])
		}
	}

	legacy := NormalizeProviderConfig(ProviderConfig{Name: "legacy", Type: "openai-compatible", Model: "legacy-default"})
	if len(legacy.Models) != 1 || legacy.Models[0].Name != "legacy-default" || legacy.Models[0].ContextTokenLimit != 0 {
		t.Fatalf("legacy provider default was not added to models: %+v", legacy.Models)
	}
}

func TestProviderModelConfigJSONContract(t *testing.T) {
	encoded, err := json.Marshal(ProviderConfig{Model: "model-a", Models: []ProviderModelConfig{{Name: "model-a", ContextTokenLimit: 123456, ImageGeneration: true}}, MaxTokens: 789})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"models":[{"name":"model-a","contextTokenLimit":123456,"imageGeneration":true}]`, `"maxTokens":789`} {
		if !strings.Contains(text, required) {
			t.Fatalf("provider model JSON contract missing %s: %s", required, text)
		}
	}
	withoutImageGeneration, err := json.Marshal(ProviderModelConfig{Name: "model-b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutImageGeneration), "imageGeneration") {
		t.Fatalf("default false image generation flag must remain omitted: %s", withoutImageGeneration)
	}
}

func TestNormalizeProviderModelsPreservesImageGeneration(t *testing.T) {
	models := NormalizeProviderModels([]ProviderModelConfig{
		{Name: " enabled ", ContextTokenLimit: 1000, ImageGeneration: true},
		{Name: "enabled", ContextTokenLimit: 2000},
		{Name: "disabled", ImageGeneration: false},
	}, "default")
	if len(models) != 3 || models[0] != (ProviderModelConfig{Name: "enabled", ContextTokenLimit: 1000, ImageGeneration: true}) || models[1] != (ProviderModelConfig{Name: "disabled"}) || models[2] != (ProviderModelConfig{Name: "default"}) {
		t.Fatalf("unexpected normalized image generation configuration: %+v", models)
	}
	provider := ProviderConfig{Models: models}
	if enabled, known := provider.ModelImageGeneration("enabled"); !known || !enabled {
		t.Fatalf("enabled model capability was not retained: enabled=%v known=%v", enabled, known)
	}
	if enabled, known := provider.ModelImageGeneration("disabled"); !known || enabled {
		t.Fatalf("explicit false model capability was not retained: enabled=%v known=%v", enabled, known)
	}
	if enabled, known := provider.ModelImageGeneration("unknown"); known || enabled {
		t.Fatalf("unknown model capability was inferred: enabled=%v known=%v", enabled, known)
	}
}

func TestDefaultWithReportTracksOnlyEffectiveLegacyFallbacks(t *testing.T) {
	for _, name := range []string{
		"AUTOTO_DEFAULT_MODEL",
		"CODEHARBOR_DEFAULT_MODEL",
		"AUTOTO_SUMMARY_MODEL",
		"CODEHARBOR_SUMMARY_MODEL",
	} {
		t.Setenv(name, "")
	}
	const secretValue = "legacy-secret-model"
	t.Setenv("CODEHARBOR_DEFAULT_MODEL", secretValue)

	cfg, report, err := DefaultWithReport()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DefaultModel != secretValue || cfg.Agent.SummaryModel != secretValue {
		t.Fatalf("expected effective legacy fallback, got %+v", cfg.Agent)
	}
	if len(report.Usages) != 1 || report.Usages[0].Legacy != "CODEHARBOR_DEFAULT_MODEL" || report.Usages[0].Replacement != "AUTOTO_DEFAULT_MODEL" {
		t.Fatalf("unexpected legacy report: %+v", report.Usages)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretValue) {
		t.Fatalf("legacy report leaked fallback value: %s", encoded)
	}
}

func TestDefaultWithReportCanonicalEnvSuppressesLegacyReport(t *testing.T) {
	t.Setenv("AUTOTO_DEFAULT_MODEL", "canonical-model")
	t.Setenv("CODEHARBOR_DEFAULT_MODEL", "legacy-secret-model")

	cfg, report, err := DefaultWithReport()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DefaultModel != "canonical-model" {
		t.Fatalf("expected canonical value, got %q", cfg.Agent.DefaultModel)
	}
	if reportHasLegacy(report, "CODEHARBOR_DEFAULT_MODEL") {
		t.Fatalf("canonical env must suppress legacy report: %+v", report.Usages)
	}
}

func TestDefaultWithReportInvalidLegacyEnvIsNotReported(t *testing.T) {
	t.Setenv("AUTOTO_EXPOSED", "")
	t.Setenv("CODEHARBOR_EXPOSED", "not-a-bool")

	cfg, report, err := DefaultWithReport()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.Exposed {
		t.Fatal("invalid legacy bool must not become effective")
	}
	if reportHasLegacy(report, "CODEHARBOR_EXPOSED") {
		t.Fatalf("invalid legacy env must not be reported: %+v", report.Usages)
	}
}

func TestLoadWithReportFiltersConfigOverriddenLegacyDefaults(t *testing.T) {
	t.Setenv("AUTOTO_DEFAULT_MODEL", "")
	t.Setenv("CODEHARBOR_DEFAULT_MODEL", "legacy-secret-model")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"defaultModel":"canonical:file","summaryModel":"canonical:summary"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := LoadWithReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DefaultModel != "canonical:file" || cfg.Agent.SummaryModel != "canonical:summary" {
		t.Fatalf("expected config values, got %+v", cfg.Agent)
	}
	if reportHasLegacy(report, "CODEHARBOR_DEFAULT_MODEL") {
		t.Fatalf("config-overridden fallback must not be reported: %+v", report.Usages)
	}
}

func TestLoadWithReportTracksExplicitLegacyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	legacyPath := filepath.Join(home, ".codeharbor", "config.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"server":{"port":9092}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, report, err := LoadWithReport(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9092 {
		t.Fatalf("expected explicitly loaded legacy config, got port %d", cfg.Server.Port)
	}
	if !reportHasLegacy(report, "~/.codeharbor/config.json") {
		t.Fatalf("expected explicit legacy config report, got %+v", report.Usages)
	}
}

func TestLoadWithReportTracksNewConfigCreatedAtExplicitLegacyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	legacyPath := filepath.Join(home, ".codeharbor", "config.json")

	cfg, report, err := LoadWithReport(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 16888 {
		t.Fatalf("expected default config at explicit legacy path, got port %d", cfg.Server.Port)
	}
	if !reportHasLegacy(report, "~/.codeharbor/config.json") {
		t.Fatalf("expected explicit legacy path usage report, got %+v", report.Usages)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("expected config to be written at explicit legacy path: %v", err)
	}
}

func TestLoadMigratesLegacyAccessPasswordAndRemovesPlaintextFromDisk(t *testing.T) {
	t.Setenv("AUTOTO_ACCESS_PASSWORD", "")
	t.Setenv("CODEHARBOR_ACCESS_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "config.json")
	legacyPassword := "Legacy-Remote-Password-9!"
	input := `{
  "security": {"exposed": true, "accessPassword": "` + legacyPassword + `"},
  "providers": {"instances": [{"name": "custom", "type": "openai-compatible", "apiKey": "preserve-existing-value", "model": "test"}]},
  "unknownExtension": {"enabled": true}
}`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.AccessPassword != "" || !VerifyAccessPassword(cfg.Security.AccessPasswordHash, legacyPassword) {
		t.Fatalf("expected in-memory hash-only credential, got %+v", cfg.Security)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(persisted)
	if strings.Contains(text, legacyPassword) || strings.Contains(text, `"accessPassword":`) {
		t.Fatalf("legacy plaintext credential remained on disk: %s", text)
	}
	if !strings.Contains(text, `"accessPasswordHash"`) || !strings.Contains(text, `"apiKey": "preserve-existing-value"`) || !strings.Contains(text, `"unknownExtension"`) {
		t.Fatalf("security-only migration did not preserve unrelated config fields: %s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected migrated config mode 0600, got %o", info.Mode().Perm())
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAccessPassword(reloaded.Security.AccessPasswordHash, legacyPassword) {
		t.Fatal("persisted migrated hash did not verify after reload")
	}
}

func reportHasLegacy(report compat.Report, legacy string) bool {
	for _, usage := range report.Usages {
		if usage.Legacy == legacy {
			return true
		}
	}
	return false
}

func providerByName(cfg Config, name string) *ProviderConfig {
	for i := range cfg.Providers.Instances {
		if cfg.Providers.Instances[i].Name == name {
			return &cfg.Providers.Instances[i]
		}
	}
	return nil
}
