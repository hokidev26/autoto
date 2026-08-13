package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

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
	if cfg.Gateway.Enabled || cfg.Gateway.AllowRemote || cfg.Gateway.Host != "127.0.0.1" || cfg.Gateway.Port != 8788 || cfg.Gateway.MaxGlobalConcurrency != 16 || cfg.Gateway.MaxRequestBytes != 8<<20 {
		t.Fatalf("unexpected gateway defaults: %+v", cfg.Gateway)
	}
	if cfg.Background.WorkerCount != 8 || cfg.Background.PerAgentLimit != 4 || cfg.Background.AllowNestedSubagents || cfg.Background.MaxSubagentDepth != 2 {
		t.Fatalf("unexpected background defaults: %+v", cfg.Background)
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
	// The cross-segment budgets default to -1 (no ceiling): a long run should not
	// stop because of a limit the user never chose. Settings > Execution budget
	// is where a ceiling gets imposed.
	//
	// Segment turns is one of them. It kept an assertion of 40 from before the
	// budgets became opt-in, which is why this test was red: normalizeAgentConfig
	// coerces 0 to -1 precisely so "unset" and "no ceiling" are the same value.
	if cfg.Agent.AutoContinuationMode != "safe" || cfg.Agent.ContinuationSegmentTurns != -1 {
		t.Fatalf("unexpected continuation defaults: %+v", cfg.Agent)
	}
	if cfg.Agent.MaxContinuations != -1 || cfg.Agent.MaxTotalTurns != -1 || cfg.Agent.MaxRunDurationMs != -1 || cfg.Agent.MaxRunTokens != -1 {
		t.Fatalf("continuation budgets should default to unlimited: %+v", cfg.Agent)
	}
	if cfg.Security.Exposed || cfg.Security.AccessPassword != "" {
		t.Fatalf("expected local security defaults, got %+v", cfg.Security)
	}
	gemini := providerByName(cfg, ProviderTypeGemini)
	if gemini == nil || gemini.Type != ProviderTypeGemini || gemini.BaseURL != "https://cloudcode-pa.googleapis.com" || gemini.Model != "gemini-3-flash" || gemini.ModelContextTokenLimit("gemini-3-flash") != 1048576 {
		t.Fatalf("unexpected Gemini provider preset: %+v", gemini)
	}
	grok := providerByName(cfg, ProviderTypeGrok)
	if grok == nil || grok.Type != ProviderTypeGrok || grok.BaseURL != "https://cli-chat-proxy.grok.com/v1" || grok.Model != "grok-4.5" || grok.ModelContextTokenLimit("grok-4.5") != 500000 {
		t.Fatalf("unexpected Grok provider preset: %+v", grok)
	}
	kimi := providerByName(cfg, ProviderTypeKimi)
	if kimi == nil || kimi.Type != ProviderTypeKimi || kimi.BaseURL != "https://api.kimi.com/coding" || kimi.Model != "kimi-k2.7-code" || kimi.ModelContextTokenLimit("kimi-k2.7-code") != 262144 {
		t.Fatalf("unexpected Kimi provider preset: %+v", kimi)
	}
	provider := providerByName(cfg, "codex")
	if provider == nil {
		t.Fatal("expected native Codex provider preset")
	}
	if provider.Type != ProviderTypeCodex || provider.BaseURL != "https://chatgpt.com/backend-api/codex" || provider.Model != "gpt-5.5" || provider.APIKeyOptional {
		t.Fatalf("unexpected native Codex provider preset: %+v", *provider)
	}
}

func TestNativeBuiltinProvidersSurviveLegacyNameCollision(t *testing.T) {
	cfg := normalizeConfig(Config{Providers: ProvidersConfig{Instances: []ProviderConfig{
		{Name: ProviderTypeGemini, Type: ProviderTypeGeminiInteractions, Model: "gemini-2.5-pro"},
	}}})
	legacy := providerByName(cfg, ProviderTypeGemini)
	if legacy == nil || legacy.Type != ProviderTypeGeminiInteractions {
		t.Fatalf("legacy Gemini Interactions provider changed unexpectedly: %+v", legacy)
	}
	native := providerByName(cfg, "gemini-oauth")
	if native == nil || native.Type != ProviderTypeGemini || native.Model != "gemini-3-flash" {
		t.Fatalf("native Gemini provider was not seeded under a collision-free name: %+v", native)
	}
	if providerByName(cfg, ProviderTypeGrok) == nil || providerByName(cfg, ProviderTypeKimi) == nil {
		t.Fatal("Grok or Kimi native provider was not seeded")
	}

	second := normalizeConfig(cfg)
	count := 0
	for _, provider := range second.Providers.Instances {
		if provider.Type == ProviderTypeGemini {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("native Gemini provider seeding was not idempotent: count=%d providers=%+v", count, second.Providers.Instances)
	}
}

func TestContextManagementDefaultsNormalizeAndPersist(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ContextManagement
	if got.CompactKeepTurns != 2 || got.MinPrunePercent != 30 || got.MaxPrunePercent != 80 || got.Standard.PruneStart != 80 || got.Standard.CompactStart != 90 || got.Large.PruneStart != 85 || got.Large.CompactStart != 92 {
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

func TestContextManagementMigratesLegacyDefaultThresholds(t *testing.T) {
	legacy := ContextManagementWindowConfig{PruneStart: 95, CompactStart: 99}

	// A pre-v4 config still carrying the shipped 95/99 defaults picks up the
	// safer thresholds on load.
	migrated := normalizeConfig(Config{SchemaVersion: 3, ContextManagement: ContextManagementConfig{Standard: legacy, Large: legacy}})
	if migrated.ContextManagement.Standard != (ContextManagementWindowConfig{PruneStart: 80, CompactStart: 90}) {
		t.Fatalf("legacy standard window was not migrated: %+v", migrated.ContextManagement.Standard)
	}
	if migrated.ContextManagement.Large != (ContextManagementWindowConfig{PruneStart: 85, CompactStart: 92}) {
		t.Fatalf("legacy large window was not migrated: %+v", migrated.ContextManagement.Large)
	}

	// A pre-v4 config with any other value is a user's own choice and is kept.
	custom := ContextManagementWindowConfig{PruneStart: 70, CompactStart: 99}
	kept := normalizeConfig(Config{SchemaVersion: 3, ContextManagement: ContextManagementConfig{Standard: custom, Large: custom}})
	if kept.ContextManagement.Standard != custom || kept.ContextManagement.Large != custom {
		t.Fatalf("user thresholds must survive migration: %+v", kept.ContextManagement)
	}

	// A v4 config that deliberately chose 95/99 is never rewritten again.
	deliberate := normalizeConfig(Config{SchemaVersion: 4, ContextManagement: ContextManagementConfig{Standard: legacy, Large: legacy}})
	if deliberate.ContextManagement.Standard != legacy || deliberate.ContextManagement.Large != legacy {
		t.Fatalf("post-migration thresholds must be kept verbatim: %+v", deliberate.ContextManagement)
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
			setTestHome(t, home)
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
	setTestHome(t, home)
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

func TestToolOutputSpillAndRepeatDefaultsNormalizeAndPersist(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ToolOutputSpillBytes != DefaultToolOutputSpillBytes {
		t.Fatalf("unexpected spill default: %d", cfg.Agent.ToolOutputSpillBytes)
	}
	if got := cfg.Agent.RepeatToolCallThresholds; len(got) != 3 || got[0] != 3 || got[1] != 5 || got[2] != 8 {
		t.Fatalf("unexpected repeat ladder default: %v", got)
	}

	// An existing config that predates both settings keeps the shipped defaults,
	// because Load unmarshals over Default rather than over a zero value.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":4,"agent":{"maxTurns":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agent.ToolOutputSpillBytes != DefaultToolOutputSpillBytes || len(loaded.Agent.RepeatToolCallThresholds) != 3 {
		t.Fatalf("an older config lost the shipped defaults: %+v", loaded.Agent)
	}
}

func TestNormalizeAgentConfigBoundsToolOutputSpillBytes(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int
		want  int
	}{
		{name: "zero disables", value: 0, want: 0},
		{name: "negative disables", value: -1, want: 0},
		{name: "below floor", value: 10, want: MinToolOutputSpillBytes},
		{name: "kept", value: DefaultToolOutputSpillBytes, want: DefaultToolOutputSpillBytes},
		{name: "above ceiling", value: MaxToolOutputSpillBytes * 2, want: MaxToolOutputSpillBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeAgentConfig(AgentConfig{ToolOutputSpillBytes: test.value}).ToolOutputSpillBytes; got != test.want {
				t.Fatalf("normalized %d to %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestNormalizeRepeatToolCallThresholds(t *testing.T) {
	for _, test := range []struct {
		name  string
		value []int
		want  []int
	}{
		{name: "empty disables", value: nil, want: nil},
		{name: "sorted and deduplicated", value: []int{8, 3, 5, 3}, want: []int{3, 5, 8}},
		{name: "values below two dropped", value: []int{1, 0, -4, 4}, want: []int{4}},
		{name: "all rejected disables", value: []int{1, 0}, want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeRepeatToolCallThresholds(test.value)
			if len(got) != len(test.want) {
				t.Fatalf("normalized %v to %v, want %v", test.value, got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("normalized %v to %v, want %v", test.value, got, test.want)
				}
			}
		})
	}
}

func TestContextTokenLimitFromEnv(t *testing.T) {
	t.Setenv("AUTOTO_CONTEXT_TOKEN_LIMIT", "12345")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ContextTokenLimit != 12345 {
		t.Fatalf("expected env context token limit, got %d", cfg.Agent.ContextTokenLimit)
	}
}

func TestSecurityConfigFromEnv(t *testing.T) {
	t.Setenv("AUTOTO_EXPOSED", "true")
	t.Setenv("AUTOTO_ACCESS_PASSWORD", "remote-secret")
	t.Setenv("AUTOTO_REMOTE_TERMINAL", "false")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Security.Exposed || cfg.Security.AccessPassword != "remote-secret" || cfg.Security.AllowRemoteTerminal {
		t.Fatalf("expected security env overrides without remote terminal, got %+v", cfg.Security)
	}
	t.Setenv("AUTOTO_REMOTE_TERMINAL", "true")
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
  "paths": {"homeDir": "/tmp/autoto", "databasePath": "/tmp/autoto/db.sqlite", "defaultProjectDir": "/tmp/autoto/projects"},
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

func TestNormalizeGatewayConfigBounds(t *testing.T) {
	defaults := normalizeGatewayConfig(GatewayConfig{})
	if defaults.Host != "127.0.0.1" || defaults.Port != 8788 || defaults.MaxGlobalConcurrency != 16 || defaults.MaxRequestBytes != 8<<20 {
		t.Fatalf("unexpected gateway fallback: %+v", defaults)
	}
	bounded := normalizeGatewayConfig(GatewayConfig{AllowRemote: true, Host: " 0.0.0.0 ", Port: 70000, MaxGlobalConcurrency: 5000, MaxRequestBytes: 1})
	if !bounded.AllowRemote || bounded.Host != "0.0.0.0" || bounded.Port != 8788 || bounded.MaxGlobalConcurrency != 1024 || bounded.MaxRequestBytes != 1<<10 {
		t.Fatalf("unexpected gateway bounds: %+v", bounded)
	}
}

func TestNormalizeGatewayHostRequiresExplicitRemoteAccess(t *testing.T) {
	invalidUTF8 := string([]byte{0xff, 0xfe})
	tests := []struct {
		name        string
		allowRemote bool
		host        string
		want        string
	}{
		{name: "default local", host: "", want: "127.0.0.1"},
		{name: "localhost", host: " LocalHost ", want: "localhost"},
		{name: "ipv4 loopback", host: "127.23.45.67", want: "127.23.45.67"},
		{name: "ipv6 loopback", host: "[::1]", want: "::1"},
		{name: "remote ip denied", host: "192.0.2.10", want: "127.0.0.1"},
		{name: "ipv4 wildcard denied", host: "0.0.0.0", want: "127.0.0.1"},
		{name: "ipv6 wildcard denied", host: "::", want: "127.0.0.1"},
		{name: "remote ip allowed", allowRemote: true, host: "192.0.2.10", want: "192.0.2.10"},
		{name: "ipv6 allowed", allowRemote: true, host: "[2001:db8::10]", want: "2001:db8::10"},
		{name: "ipv4 wildcard allowed", allowRemote: true, host: "*", want: "0.0.0.0"},
		{name: "ipv6 wildcard allowed", allowRemote: true, host: "::", want: "::"},
		{name: "url rejected", allowRemote: true, host: "https://127.0.0.1", want: "127.0.0.1"},
		{name: "host and port rejected", allowRemote: true, host: "127.0.0.1:8788", want: "127.0.0.1"},
		{name: "dns name rejected", allowRemote: true, host: "gateway.example", want: "127.0.0.1"},
		{name: "control character rejected", allowRemote: true, host: "localhost\n", want: "127.0.0.1"},
		{name: "format character rejected", allowRemote: true, host: "local\u200bhost", want: "127.0.0.1"},
		{name: "invalid utf8 rejected", allowRemote: true, host: invalidUTF8, want: "127.0.0.1"},
		{name: "malformed brackets rejected", allowRemote: true, host: "[::1", want: "127.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeGatewayConfig(GatewayConfig{AllowRemote: test.allowRemote, Host: test.host})
			if got.Host != test.want {
				t.Fatalf("normalized host = %q, want %q", got.Host, test.want)
			}
		})
	}
}

func TestGatewayConfigV2JSONAndIPv6Address(t *testing.T) {
	encoded, err := json.Marshal(GatewayConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"allowRemote":false`) {
		t.Fatalf("gateway JSON is missing allowRemote: %s", encoded)
	}
	cfg := Config{Gateway: GatewayConfig{Host: "::1", Port: 8788}}
	if got := cfg.GatewayAddr(); got != "[::1]:8788" {
		t.Fatalf("IPv6 gateway address = %q, want %q", got, "[::1]:8788")
	}
}

func TestLoadMigratesV1GatewayToSafeBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"gateway":{"enabled":true,"host":"0.0.0.0","port":9000,"maxGlobalConcurrency":32,"maxRequestBytes":4096}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SchemaVersion != CurrentConfigVersion || cfg.Gateway.AllowRemote || cfg.Gateway.Host != "127.0.0.1" || cfg.Gateway.Port != 9000 || cfg.Gateway.MaxGlobalConcurrency != 32 || cfg.Gateway.MaxRequestBytes != 4096 {
		t.Fatalf("unexpected v1 gateway migration: version=%d gateway=%+v", cfg.SchemaVersion, cfg.Gateway)
	}

	cfg.Gateway.Host = "https://192.0.2.10"
	cfg.Gateway.AllowRemote = true
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err := json.Unmarshal(persisted, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.SchemaVersion != CurrentConfigVersion || !saved.Gateway.AllowRemote || saved.Gateway.Host != "127.0.0.1" {
		t.Fatalf("unsafe gateway was not normalized on save: %s", persisted)
	}
}

func TestNormalizeBackgroundConfigBounds(t *testing.T) {
	defaults := normalizeBackgroundConfig(BackgroundConfig{})
	if defaults.WorkerCount != 8 || defaults.PerAgentLimit != 4 || defaults.AllowNestedSubagents || defaults.MaxSubagentDepth != 2 {
		t.Fatalf("unexpected normalized background defaults: %+v", defaults)
	}
	bounded := normalizeBackgroundConfig(BackgroundConfig{WorkerCount: 99, PerAgentLimit: 99, AllowNestedSubagents: true, MaxSubagentDepth: 99})
	if bounded.WorkerCount != 16 || bounded.PerAgentLimit != 8 || !bounded.AllowNestedSubagents || bounded.MaxSubagentDepth != 4 {
		t.Fatalf("unexpected normalized background bounds: %+v", bounded)
	}
	if err := (BackgroundConfig{WorkerCount: 0, PerAgentLimit: 4, MaxSubagentDepth: 2}).Validate(); err == nil {
		t.Fatal("expected invalid worker count to be rejected")
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
	// Disabling continuation is expressed by the mode, not by a zero count: a
	// negative count now means "no ceiling" like the other budgets.
	off := normalizeAgentConfig(AgentConfig{AutoContinuationMode: " OFF ", MaxContinuations: -1})
	if off.AutoContinuationMode != "off" || off.MaxContinuations != -1 {
		t.Fatalf("expected explicit off mode and unlimited continuation budget, got %+v", off)
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
	t.Setenv("AUTOTO_AGENT_BACKEND_URL", "http://127.0.0.1:8000/")
	t.Setenv("AUTOTO_AGENT_BACKEND_API_KEY", "secret")
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
	t.Setenv("AUTOTO_AGENT_BACKEND_URL", "http://127.0.0.1:8000")
	t.Setenv("AUTOTO_AGENT_BACKEND_API_KEY", "backend-secret")
	t.Setenv("AUTOTO_ACCESS_PASSWORD", "remote-access-secret")

	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	expectedRuntimeKeys := map[string]string{
		"openai":            "openai-secret",
		"anthropic":         "anthropic-secret",
		"cliproxyapi":       "cliproxy-secret",
		"openai-compatible": "compatible-secret",
	}
	for _, provider := range cfg.Providers.Instances {
		if expected, ok := expectedRuntimeKeys[provider.Name]; ok && provider.APIKey != expected {
			t.Fatalf("expected runtime config to keep %s env secret, got %q", provider.Name, provider.APIKey)
		}
	}
	if gemini := providerByName(cfg, ProviderTypeGemini); gemini == nil || gemini.APIKey != "" {
		t.Fatalf("native Gemini preset must ignore GEMINI_API_KEY, got %+v", gemini)
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

func TestProviderImageInputCapabilityRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"instances":[{"name":"legacy-relay","type":"openai-compatible","baseUrl":"http://127.0.0.1:8080/v1","model":"legacy-model"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if provider := providerByName(legacy, "legacy-relay"); provider == nil || provider.ImageInput {
		t.Fatalf("legacy compatible provider must default image input off: %+v", provider)
	}

	cfg := Config{SchemaVersion: CurrentConfigVersion, Providers: ProvidersConfig{Instances: []ProviderConfig{{
		Name:       "image-relay",
		Type:       "openai-compatible",
		BaseURL:    "http://127.0.0.1:8081/v1",
		Model:      "vision-model",
		ImageInput: true,
	}}}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"imageInput": true`) {
		t.Fatalf("explicit image input capability was not persisted: %s", persisted)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if provider := providerByName(loaded, "image-relay"); provider == nil || !provider.ImageInput {
		t.Fatalf("explicit image input capability was not restored: %+v", provider)
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

func TestNormalizeProvidersClearsCLIProxyGatewayEligibility(t *testing.T) {
	providers := normalizeProviders(ProvidersConfig{Instances: []ProviderConfig{
		{Name: "codex", Type: "CoDeX", GatewayEnabled: true},
		{Name: "proxy", Type: "openai-compatible", Profile: ProviderProfileCLIProxyAPI, GatewayEnabled: true},
		{Name: "relay", Type: "openai-compatible", GatewayEnabled: true},
	}})
	// Codex may now be shared (kept enabled); only CLI-proxy OAuth is cleared.
	if !providers.Instances[0].GatewayEnabled || providers.Instances[1].GatewayEnabled || !providers.Instances[2].GatewayEnabled {
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
	encoded, err := json.Marshal(ProviderConfig{Model: "model-a", Models: []ProviderModelConfig{{Name: "model-a", ContextTokenLimit: 123456}}, MaxTokens: 789})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"models":[{"name":"model-a","contextTokenLimit":123456}]`, `"maxTokens":789`} {
		if !strings.Contains(text, required) {
			t.Fatalf("provider model JSON contract missing %s: %s", required, text)
		}
	}
}

func TestLoadMigratesLegacyAccessPasswordAndRemovesPlaintextFromDisk(t *testing.T) {
	t.Setenv("AUTOTO_ACCESS_PASSWORD", "")
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

func providerByName(cfg Config, name string) *ProviderConfig {
	for i := range cfg.Providers.Instances {
		if cfg.Providers.Instances[i].Name == name {
			return &cfg.Providers.Instances[i]
		}
	}
	return nil
}
