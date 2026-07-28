package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"autoto/internal/compat"
)

// Version is the product version string. Release builds override it with:
//
//	-ldflags "-X autoto/internal/config.Version=..."
//
// Keep this a package-level var (not const) so the Go linker can rewrite it.
var Version = "0.1.0-dev"

// Commit is the short git commit hash of the build. Empty in local/dev builds;
// release builds inject it with:
//
//	-ldflags "-X autoto/internal/config.Commit=$(git rev-parse --short HEAD)"
//
// When set, it is shown in the startup banner as "Autoto <version> (<commit>)".
var Commit = ""

const CurrentConfigVersion = 3

type Config struct {
	SchemaVersion     int                     `json:"version"`
	Server            ServerConfig            `json:"server"`
	Gateway           GatewayConfig           `json:"gateway"`
	Paths             PathsConfig             `json:"paths"`
	Agent             AgentConfig             `json:"agent"`
	ContextManagement ContextManagementConfig `json:"contextManagement"`
	Auth              AuthConfig              `json:"auth"`
	Security          SecurityConfig          `json:"security"`
	Providers         ProvidersConfig         `json:"providers"`
	Backends          BackendsConfig          `json:"backends"`
}

type ServerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type GatewayConfig struct {
	Enabled              bool   `json:"enabled"`
	AllowRemote          bool   `json:"allowRemote"`
	Host                 string `json:"host"`
	Port                 int    `json:"port"`
	MaxGlobalConcurrency int    `json:"maxGlobalConcurrency"`
	MaxRequestBytes      int64  `json:"maxRequestBytes"`
}

type PathsConfig struct {
	HomeDir           string `json:"homeDir"`
	DatabasePath      string `json:"databasePath"`
	DefaultProjectDir string `json:"defaultProjectDir"`
}

type ContextManagementWindowConfig struct {
	PruneStart   int `json:"pruneStart"`
	CompactStart int `json:"compactStart"`
}

type ContextManagementConfig struct {
	CompactKeepTurns int                           `json:"compactKeepTurns"`
	MaxPrunePercent  int                           `json:"maxPrunePercent"`
	MinPrunePercent  int                           `json:"minPrunePercent"`
	Standard         ContextManagementWindowConfig `json:"standard"`
	Large            ContextManagementWindowConfig `json:"large"`
}

type AgentConfig struct {
	DefaultModel string `json:"defaultModel"`
	SummaryModel string `json:"summaryModel"`
	ReviewModel  string `json:"reviewModel"`
	// SafetyModel judges whether a risky action may proceed. It falls back to
	// SummaryModel when unset, but is separate because the summary model is
	// routinely pointed at a small cheap model for titles and compaction, and
	// that silently downgrades the safety gate to the same tier.
	SafetyModel              string              `json:"safetyModel,omitempty"`
	SubagentModels           map[string]string   `json:"subagentModels,omitempty"`
	SubagentModelPools       map[string][]string `json:"subagentModelPools,omitempty"`
	DefaultPermissionMode    string              `json:"defaultPermissionMode"`
	DefaultStartInPlanMode   bool                `json:"defaultStartInPlanMode"`
	MaxTurns                 int                 `json:"maxTurns"`
	ContextTokenLimit        int                 `json:"contextTokenLimit"`
	FirstTokenTimeoutMs      int                 `json:"firstTokenTimeoutMs"`
	MaxTransientRetries      int                 `json:"maxTransientRetries"`
	AutoContinuationMode     string              `json:"autoContinuationMode"`
	ContinuationSegmentTurns int                 `json:"continuationSegmentTurns"`
	MaxContinuations         int                 `json:"maxContinuations"`
	MaxTotalTurns            int                 `json:"maxTotalTurns"`
	MaxRunDurationMs         int64               `json:"maxRunDurationMs"`
	MaxRunTokens             int64               `json:"maxRunTokens"`
}

type AuthConfig struct {
	JWTSecret        string         `json:"jwtSecret"`
	RegistrationOpen bool           `json:"registrationOpen"`
	OAuthApp         OAuthAppConfig `json:"oauthApp"`
}

type SecurityConfig struct {
	Exposed bool `json:"exposed"`
	// AccessPassword is an environment-only compatibility input. It is never
	// written to disk; durable credentials use AccessPasswordHash.
	AccessPassword          string `json:"accessPassword,omitempty"`
	AccessPasswordHash      string `json:"accessPasswordHash,omitempty"`
	AllowRemoteFullAccess   bool   `json:"allowRemoteFullAccess,omitempty"`
	DefaultRemoteAccessMode string `json:"defaultRemoteAccessMode,omitempty"`
	AllowRemoteNativePicker bool   `json:"allowRemoteNativePicker,omitempty"`
	CredentialRevision      int64  `json:"credentialRevision,omitempty"`
	// AllowRemoteTerminal is retained only to read older configurations. New
	// remote terminal access is granted exclusively by a full remote session.
	AllowRemoteTerminal bool `json:"allowRemoteTerminal,omitempty"`
}

type ProvidersConfig struct {
	Instances        []ProviderConfig        `json:"instances"`
	OpenAICompatible *OpenAICompatibleConfig `json:"openaiCompatible,omitempty"`
}

const ProviderProfileCLIProxyAPI = "cliproxyapi"
const ProviderTypeCodex = "codex"
const ProviderModelContextTokenLimitMax = 10_000_000

const (
	ProviderOriginBuiltin = "builtin"
	ProviderOriginCustom  = "custom"
)

type ProviderRequestHeader struct {
	Name  string `json:"name"`
	Value string `json:"-"`
}

type ProviderModelConfig struct {
	Name              string `json:"name"`
	ContextTokenLimit int    `json:"contextTokenLimit"`
	ImageGeneration   bool   `json:"imageGeneration,omitempty"`
}

type ProviderConfig struct {
	Name                  string                  `json:"name"`
	Type                  string                  `json:"type"`
	Profile               string                  `json:"profile,omitempty"`
	BaseURL               string                  `json:"baseUrl,omitempty"`
	APIKey                string                  `json:"apiKey,omitempty"`
	Model                 string                  `json:"model"`
	Models                []ProviderModelConfig   `json:"models,omitempty"`
	MaxTokens             int64                   `json:"maxTokens,omitempty"`
	APIKeyOptional        bool                    `json:"apiKeyOptional,omitempty"`
	ImageInput            bool                    `json:"imageInput,omitempty"`
	GatewayEnabled        bool                    `json:"gatewayEnabled,omitempty"`
	ProxyURL              string                  `json:"proxyUrl,omitempty"`
	UserAgent             string                  `json:"userAgent,omitempty"`
	RequestHeaders        []ProviderRequestHeader `json:"requestHeaders,omitempty"`
	InsecureSkipTLSVerify bool                    `json:"insecureSkipTLSVerify,omitempty"`
	// SecretRevision coordinates crash-safe Provider API key updates between
	// config.json and the encrypted SQLite secret vault. It contains no secret.
	SecretRevision int64 `json:"secretRevision,omitempty"`
	// TransportSecretRevision independently coordinates encrypted proxy
	// credentials and request-header values.
	TransportSecretRevision int64 `json:"transportSecretRevision,omitempty"`
	// Disabled is persisted instead of Enabled so configs written before this
	// field existed remain enabled after an upgrade.
	Disabled bool `json:"disabled,omitempty"`

	ClientVersion                  string `json:"-"`
	InstallationID                 string `json:"-"`
	CredentialStorePath            string `json:"-"`
	CodexAllowInsecureTestEndpoint bool   `json:"-"`
	CodexRefreshURLForTest         string `json:"-"`
	CodexUsageURL                  string `json:"-"`
	// APIKeySource is runtime-only provenance used to keep environment values
	// ahead of the encrypted database vault without exposing either value.
	APIKeySource         string `json:"-"`
	ProxyUsername        string `json:"-"`
	ProxyPassword        string `json:"-"`
	ProxyAuthSource      string `json:"-"`
	RequestHeadersSource string `json:"-"`
}

type OpenAICompatibleConfig = ProviderConfig

type BackendsConfig struct {
	Instances []BackendConfig `json:"instances"`
}

type BackendConfig struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey,omitempty"`
	Active  bool   `json:"active,omitempty"`
}

type ProviderSummary struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	Profile        string `json:"profile,omitempty"`
	BaseURL        string `json:"baseUrl,omitempty"`
	Model          string `json:"model"`
	MaxTokens      int64  `json:"maxTokens,omitempty"`
	Configured     bool   `json:"configured"`
	APIKeyOptional bool   `json:"apiKeyOptional,omitempty"`
	GatewayEnabled bool   `json:"gatewayEnabled"`
	Enabled        bool   `json:"enabled"`
	Origin         string `json:"origin"`
}

func Default() (Config, error) {
	cfg, _, err := DefaultWithReport()
	return cfg, err
}

func DefaultWithReport() (Config, compat.Report, error) {
	var report compat.Report
	cfg, err := defaultWithReport(&report)
	return cfg, report, err
}

func defaultWithReport(report *compat.Report) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	appHome := filepath.Join(home, ".autoto")
	defaultModel := firstEnvFallback(report, "AUTOTO_DEFAULT_MODEL", "CODEHARBOR_DEFAULT_MODEL")
	if defaultModel == "" {
		defaultModel = "openai:gpt-4.1-mini"
	}
	summaryModel := firstEnvFallback(report, "AUTOTO_SUMMARY_MODEL", "CODEHARBOR_SUMMARY_MODEL")
	if summaryModel == "" {
		summaryModel = defaultModel
	}
	// Left empty on purpose when unset: the runner falls back to the summary
	// model, so an existing config keeps its current behaviour, and only an
	// explicit value moves the safety gate onto its own model.
	safetyModel := firstEnvFallback(report, "AUTOTO_SAFETY_MODEL", "CODEHARBOR_SAFETY_MODEL")
	return Config{
		SchemaVersion: CurrentConfigVersion,
		Server:        ServerConfig{Host: "localhost", Port: 16888},
		Gateway: GatewayConfig{
			Enabled:              false,
			AllowRemote:          false,
			Host:                 "127.0.0.1",
			Port:                 8788,
			MaxGlobalConcurrency: 16,
			MaxRequestBytes:      8 << 20,
		},
		Paths: PathsConfig{
			HomeDir:           appHome,
			DatabasePath:      filepath.Join(appHome, "autoto.db"),
			DefaultProjectDir: filepath.Join(home, "projects"),
		},
		ContextManagement: ContextManagementConfig{
			CompactKeepTurns: 2,
			MaxPrunePercent:  80,
			MinPrunePercent:  30,
			Standard:         ContextManagementWindowConfig{PruneStart: 95, CompactStart: 99},
			Large:            ContextManagementWindowConfig{PruneStart: 95, CompactStart: 99},
		},
		Agent: AgentConfig{
			DefaultModel:             defaultModel,
			SummaryModel:             summaryModel,
			ReviewModel:              defaultModel,
			SafetyModel:              safetyModel,
			DefaultPermissionMode:    "acceptEdits",
			DefaultStartInPlanMode:   false,
			MaxTurns:                 200,
			ContextTokenLimit:        getenvIntFallbackReported(report, []string{"AUTOTO_CONTEXT_TOKEN_LIMIT", "CODEHARBOR_CONTEXT_TOKEN_LIMIT"}, 120000),
			FirstTokenTimeoutMs:      60000,
			MaxTransientRetries:      10,
			AutoContinuationMode:     "safe",
			ContinuationSegmentTurns: 40,
			// -1 across the cross-segment budgets: no ceiling by default. See
			// normalizeAgent for why maxContinuations is included.
			MaxContinuations:  -1,
			MaxTotalTurns:     -1,
			MaxRunDurationMs:  -1,
			MaxRunTokens:      -1,
		},
		Auth: AuthConfig{
			RegistrationOpen: true,
			OAuthApp: OAuthAppConfig{
				ClientSecretEnv: "AUTOTO_OIDC_CLIENT_SECRET",
				RedirectURL:     "http://localhost:16888/app/auth/callback",
				AutoProvision:   true,
				SessionTTLHours: 8,
			},
		},
		Security: SecurityConfig{
			Exposed:                 getenvBoolFallbackReported(report, []string{"AUTOTO_EXPOSED", "CODEHARBOR_EXPOSED"}, false),
			AccessPassword:          firstEnvFallback(report, "AUTOTO_ACCESS_PASSWORD", "CODEHARBOR_ACCESS_PASSWORD"),
			AllowRemoteFullAccess:   getenvBoolFallbackReported(report, []string{"AUTOTO_ALLOW_REMOTE_FULL_ACCESS", "CODEHARBOR_ALLOW_REMOTE_FULL_ACCESS"}, false),
			DefaultRemoteAccessMode: firstEnvFallback(report, "AUTOTO_DEFAULT_REMOTE_ACCESS_MODE", "CODEHARBOR_DEFAULT_REMOTE_ACCESS_MODE"),
			AllowRemoteNativePicker: getenvBoolFallbackReported(report, []string{"AUTOTO_ALLOW_REMOTE_NATIVE_PICKER", "CODEHARBOR_ALLOW_REMOTE_NATIVE_PICKER"}, false),
			CredentialRevision:      1,
			AllowRemoteTerminal:     getenvBoolFallbackReported(report, []string{"AUTOTO_REMOTE_TERMINAL", "CODEHARBOR_REMOTE_TERMINAL"}, false),
		},
		Providers: ProvidersConfig{Instances: []ProviderConfig{
			{
				Name:   "openai",
				Type:   "openai",
				APIKey: os.Getenv("OPENAI_API_KEY"),
				Model:  getenv("OPENAI_MODEL", "gpt-4.1-mini"),
			},
			{
				Name:      "anthropic",
				Type:      "anthropic",
				APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
				Model:     getenv("ANTHROPIC_MODEL", "claude-sonnet-4-5"),
				MaxTokens: 4096,
			},
			defaultGeminiProvider(),
			defaultGrokProvider(),
			defaultKimiProvider(),
			defaultKiroProvider(),
			defaultCodexProvider(),
			{
				Name:    "openai-compatible",
				Type:    "openai-compatible",
				BaseURL: getenv("OPENAI_COMPATIBLE_BASE_URL", getenv("OPENAI_BASE_URL", "https://api.openai.com/v1")),
				APIKey:  getenv("OPENAI_COMPATIBLE_API_KEY", os.Getenv("OPENAI_API_KEY")),
				Model:   getenv("OPENAI_COMPATIBLE_MODEL", getenv("OPENAI_MODEL", "gpt-4.1-mini")),
			},
		}},
		Backends: BackendsConfig{Instances: defaultBackendsFromEnv(report)},
	}, nil
}

func ResolvePath(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return path, nil
	}
	cfg, err := Default()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg.Paths.HomeDir, "config.json"), nil
}

func Load(path string) (Config, error) {
	cfg, _, err := LoadWithReport(path)
	return cfg, err
}

func LoadWithReport(path string) (Config, compat.Report, error) {
	var report compat.Report
	cfg, err := defaultWithReport(&report)
	if err != nil {
		return Config{}, report, err
	}
	path, err = ResolvePath(path)
	if err != nil {
		return Config{}, report, err
	}
	legacyPath, err := legacyConfigPath()
	if err != nil {
		return Config{}, report, err
	}
	explicitLegacyPath := filepath.Clean(path) == filepath.Clean(legacyPath)
	if err := ensureConfigParent(path); err != nil {
		return Config{}, report, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if isCanonicalConfigPath(path, cfg) {
				if copied, copyErr := copyLegacyConfig(legacyPath, path); copyErr != nil {
					return Config{}, report, copyErr
				} else if copied {
					report.Add(legacyConfigUsage("copied"))
					data, err = os.ReadFile(path)
					if err != nil {
						return Config{}, report, err
					}
					goto decode
				}
			}
			if writeErr := writeDefaultConfig(path, cfg); writeErr != nil {
				return Config{}, report, writeErr
			}
			if explicitLegacyPath {
				report.Add(legacyConfigUsage("loaded"))
			}
			return cfg, report, nil
		}
		return Config{}, report, err
	}

decode:
	filterOverriddenDefaultUsages(&report, data)
	if len(data) == 0 {
		if explicitLegacyPath {
			report.Add(legacyConfigUsage("loaded"))
		}
		return cfg, report, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, report, fmt.Errorf("parse config %s: %w", path, err)
	}
	migratedSecurityPassword, err := migrateLegacySecurityPassword(&cfg, data)
	if err != nil {
		return Config{}, report, fmt.Errorf("migrate security credential in %s: %w", path, err)
	}
	if migratedSecurityPassword {
		if err := persistMigratedSecurityPassword(path, data, cfg.Security.AccessPasswordHash); err != nil {
			return Config{}, report, fmt.Errorf("persist migrated security credential in %s: %w", path, err)
		}
	}
	cfg = normalizeConfigWithReport(cfg, &report)
	if explicitLegacyPath {
		report.Add(legacyConfigUsage("loaded"))
	}
	return cfg, report, nil
}

func ensureConfigParent(path string) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o755)
	privateDefaultHome := false
	if home, err := os.UserHomeDir(); err == nil {
		privateDefaultHome = filepath.Clean(dir) == filepath.Clean(filepath.Join(home, ".autoto"))
		if privateDefaultHome {
			mode = 0o700
		}
	}
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	if !privateDefaultHome {
		return nil
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("default Autoto home %s must be a real directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return nil
}

func isCanonicalConfigPath(path string, cfg Config) bool {
	canonical := filepath.Join(cfg.Paths.HomeDir, "config.json")
	return filepath.Clean(path) == filepath.Clean(canonical)
}

func legacyConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codeharbor", "config.json"), nil
}

func legacyConfigUsage(operation string) compat.Usage {
	return compat.Usage{
		Key:         "config:" + operation + ":~/.codeharbor/config.json",
		Legacy:      "~/.codeharbor/config.json",
		Replacement: "~/.autoto/config.json",
		Kind:        operation,
	}
}

func filterOverriddenDefaultUsages(report *compat.Report, data []byte) {
	if report == nil || len(data) == 0 {
		return
	}
	var raw struct {
		Agent    map[string]json.RawMessage `json:"agent"`
		Backends map[string]json.RawMessage `json:"backends"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return
	}
	remove := map[string]bool{}
	_, hasDefaultModel := raw.Agent["defaultModel"]
	_, hasSummaryModel := raw.Agent["summaryModel"]
	if hasDefaultModel && (hasSummaryModel || os.Getenv("AUTOTO_SUMMARY_MODEL") != "" || os.Getenv("CODEHARBOR_SUMMARY_MODEL") != "") {
		remove[envUsageKey("CODEHARBOR_DEFAULT_MODEL")] = true
	}
	if hasSummaryModel {
		remove[envUsageKey("CODEHARBOR_SUMMARY_MODEL")] = true
	}
	if _, ok := raw.Agent["contextTokenLimit"]; ok {
		remove[envUsageKey("CODEHARBOR_CONTEXT_TOKEN_LIMIT")] = true
	}
	if _, ok := raw.Backends["instances"]; ok {
		for _, name := range []string{
			"CODEHARBOR_AGENT_BACKEND_URL",
			"CODEHARBOR_AGENT_BACKEND_NAME",
			"CODEHARBOR_AGENT_BACKEND_KIND",
			"CODEHARBOR_AGENT_BACKEND_API_KEY",
		} {
			remove[envUsageKey(name)] = true
		}
	}
	if len(remove) == 0 {
		return
	}
	filtered := report.Usages[:0]
	for _, usage := range report.Usages {
		if !remove[usage.Key] {
			filtered = append(filtered, usage)
		}
	}
	report.Usages = filtered
}

func copyLegacyConfig(sourcePath, destinationPath string) (bool, error) {
	linkInfo, err := os.Lstat(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !linkInfo.Mode().IsRegular() {
		return false, fmt.Errorf("legacy config %s is not a regular file", sourcePath)
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return false, err
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(linkInfo, info) {
		return false, fmt.Errorf("legacy config %s changed while opening", sourcePath)
	}

	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return true, nil
		}
		return false, err
	}
	complete := false
	defer func() {
		_ = destination.Close()
		if !complete {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := io.Copy(destination, source); err != nil {
		return false, err
	}
	if err := destination.Sync(); err != nil {
		return false, err
	}
	if err := destination.Close(); err != nil {
		return false, err
	}
	complete = true
	return true, nil
}

func normalizeConfig(cfg Config) Config {
	return normalizeConfigWithReport(cfg, nil)
}

func normalizeConfigWithReport(cfg Config, report *compat.Report) Config {
	cfg = migrateConfig(cfg)
	cfg.Gateway = normalizeGatewayConfig(cfg.Gateway)
	cfg.ContextManagement = normalizeContextManagementConfig(cfg.ContextManagement)
	cfg.Agent = normalizeAgentConfig(cfg.Agent)
	cfg.Auth.OAuthApp = cfg.Auth.OAuthApp.Normalized()
	cfg.Security = normalizeSecurityConfig(cfg.Security)
	applySecurityEnvOverrides(&cfg.Security, report)
	cfg.Security = normalizeSecurityConfig(cfg.Security)

	cfg.Providers = ensureNativeBuiltinProviders(normalizeProviders(cfg.Providers))
	cfg.Backends = normalizeBackends(cfg.Backends)
	return cfg
}

func migrateConfig(cfg Config) Config {
	if cfg.SchemaVersion < CurrentConfigVersion {
		cfg.SchemaVersion = CurrentConfigVersion
	}
	return cfg
}

func normalizeGatewayConfig(gateway GatewayConfig) GatewayConfig {
	gateway.Host = normalizeGatewayHost(gateway.Host, gateway.AllowRemote)
	if gateway.Port <= 0 || gateway.Port > 65535 {
		gateway.Port = 8788
	}
	if gateway.MaxGlobalConcurrency <= 0 {
		gateway.MaxGlobalConcurrency = 16
	} else if gateway.MaxGlobalConcurrency > 1024 {
		gateway.MaxGlobalConcurrency = 1024
	}
	if gateway.MaxRequestBytes <= 0 {
		gateway.MaxRequestBytes = 8 << 20
	} else if gateway.MaxRequestBytes < 1<<10 {
		gateway.MaxRequestBytes = 1 << 10
	} else if gateway.MaxRequestBytes > 64<<20 {
		gateway.MaxRequestBytes = 64 << 20
	}
	return gateway
}

func normalizeGatewayHost(raw string, allowRemote bool) string {
	const loopbackFallback = "127.0.0.1"
	if !utf8.ValidString(raw) {
		return loopbackFallback
	}
	for _, char := range raw {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return loopbackFallback
		}
	}

	host := strings.TrimSpace(raw)
	if host == "" {
		return loopbackFallback
	}
	if strings.EqualFold(host, "localhost") {
		return "localhost"
	}
	if host == "*" {
		if allowRemote {
			return "0.0.0.0"
		}
		return loopbackFallback
	}
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if len(host) < 3 || host[0] != '[' || host[len(host)-1] != ']' {
			return loopbackFallback
		}
		host = host[1 : len(host)-1]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return loopbackFallback
	}
	if !allowRemote && !ip.IsLoopback() {
		return loopbackFallback
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.String()
}

func normalizeContextManagementConfig(value ContextManagementConfig) ContextManagementConfig {
	if value.CompactKeepTurns <= 0 {
		value.CompactKeepTurns = 2
	}
	if value.CompactKeepTurns > 100 {
		value.CompactKeepTurns = 100
	}
	if value.MaxPrunePercent <= 0 {
		value.MaxPrunePercent = 80
	}
	if value.MaxPrunePercent > 100 {
		value.MaxPrunePercent = 100
	}
	if value.MinPrunePercent <= 0 {
		value.MinPrunePercent = 30
	}
	if value.MinPrunePercent > value.MaxPrunePercent {
		value.MinPrunePercent = value.MaxPrunePercent
	}
	normalizeWindow := func(window ContextManagementWindowConfig) ContextManagementWindowConfig {
		if window.PruneStart <= 0 {
			window.PruneStart = 95
		}
		if window.PruneStart > 100 {
			window.PruneStart = 100
		}
		if window.CompactStart <= 0 {
			window.CompactStart = 99
		}
		if window.CompactStart > 100 {
			window.CompactStart = 100
		}
		return window
	}
	value.Standard = normalizeWindow(value.Standard)
	value.Large = normalizeWindow(value.Large)
	return value
}

func (c ContextManagementConfig) Normalized() ContextManagementConfig {
	return normalizeContextManagementConfig(c)
}

func (c ContextManagementConfig) Validate() error {
	if c.CompactKeepTurns < 1 || c.CompactKeepTurns > 100 {
		return errors.New("compactKeepTurns must be between 1 and 100")
	}
	if c.MinPrunePercent < 1 || c.MinPrunePercent > 100 {
		return errors.New("minPrunePercent must be between 1 and 100")
	}
	if c.MaxPrunePercent < 1 || c.MaxPrunePercent > 100 {
		return errors.New("maxPrunePercent must be between 1 and 100")
	}
	if c.MinPrunePercent > c.MaxPrunePercent {
		return errors.New("minPrunePercent must not exceed maxPrunePercent")
	}
	validateWindow := func(name string, window ContextManagementWindowConfig) error {
		if window.PruneStart < 1 || window.PruneStart > 100 {
			return fmt.Errorf("%s.pruneStart must be between 1 and 100", name)
		}
		if window.CompactStart < 1 || window.CompactStart > 100 {
			return fmt.Errorf("%s.compactStart must be between 1 and 100", name)
		}
		return nil
	}
	if err := validateWindow("standard", c.Standard); err != nil {
		return err
	}
	return validateWindow("large", c.Large)
}

func (c ContextManagementConfig) WindowForLimit(limit int) ContextManagementWindowConfig {
	c = normalizeContextManagementConfig(c)
	if limit > 600000 {
		return c.Large
	}
	return c.Standard
}

func normalizeAgentConfig(agent AgentConfig) AgentConfig {
	agent.DefaultModel = strings.TrimSpace(agent.DefaultModel)
	agent.SummaryModel = strings.TrimSpace(agent.SummaryModel)
	agent.ReviewModel = strings.TrimSpace(agent.ReviewModel)
	if agent.ReviewModel == "" {
		agent.ReviewModel = agent.DefaultModel
	}
	agent.SubagentModels = normalizeSubagentModels(agent.SubagentModels)
	agent.SubagentModelPools = normalizeSubagentModelPools(agent.SubagentModelPools)
	agent.AutoContinuationMode = strings.ToLower(strings.TrimSpace(agent.AutoContinuationMode))
	if agent.AutoContinuationMode != "off" && agent.AutoContinuationMode != "safe" {
		agent.AutoContinuationMode = "safe"
	}
	if agent.ContinuationSegmentTurns <= 0 {
		agent.ContinuationSegmentTurns = 40
	}
	if agent.ContinuationSegmentTurns > 1000 {
		agent.ContinuationSegmentTurns = 1000
	}
	// The four cross-segment budgets below default to -1 (no ceiling). A long
	// /goal run legitimately outlasts any fixed number, and a run that is making
	// progress should not stop because of a limit the user never chose. Callers
	// who want a ceiling set one in Settings > Execution budget.
	//
	// MaxContinuations is capped alongside them rather than kept at 8: with
	// segmentTurns x maxContinuations being an effective turn ceiling of its
	// own, leaving it at 8 would stop a run near turn 320 and report
	// max_continuations instead — the same unexplained interruption with a
	// different label. Disabling continuation entirely is expressed by
	// autoContinuationMode "off", not by a zero count.
	if agent.MaxContinuations <= 0 {
		agent.MaxContinuations = -1
	} else if agent.MaxContinuations > 64 {
		agent.MaxContinuations = 64
	}
	if agent.MaxTotalTurns == 0 {
		agent.MaxTotalTurns = -1
	} else if agent.MaxTotalTurns > 10000 {
		agent.MaxTotalTurns = 10000
	} else if agent.MaxTotalTurns < 0 {
		agent.MaxTotalTurns = -1
	}
	if agent.MaxTotalTurns > 0 && agent.ContinuationSegmentTurns > agent.MaxTotalTurns {
		agent.ContinuationSegmentTurns = agent.MaxTotalTurns
	}
	if agent.MaxRunDurationMs == 0 {
		agent.MaxRunDurationMs = -1
	} else if agent.MaxRunDurationMs < 0 {
		agent.MaxRunDurationMs = -1
	} else if agent.MaxRunDurationMs < 1000 {
		agent.MaxRunDurationMs = 1000
	} else if agent.MaxRunDurationMs > 86400000 {
		agent.MaxRunDurationMs = 86400000
	}
	if agent.MaxRunTokens == 0 {
		agent.MaxRunTokens = -1
	} else if agent.MaxRunTokens < 0 {
		agent.MaxRunTokens = -1
	} else if agent.MaxRunTokens < 1000 {
		agent.MaxRunTokens = 1000
	} else if agent.MaxRunTokens > 10000000 {
		agent.MaxRunTokens = 10000000
	}
	return agent
}

func normalizeSubagentModels(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(values))
	for role, model := range values {
		role = strings.ToLower(strings.TrimSpace(role))
		model = strings.TrimSpace(model)
		if role != "" && model != "" {
			normalized[role] = model
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeSubagentModelPools(values map[string][]string) map[string][]string {
	if len(values) == 0 {
		return nil
	}
	normalized := make(map[string][]string, len(values))
	for role, models := range values {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" {
			continue
		}
		seen := make(map[string]struct{}, len(models))
		pool := make([]string, 0, len(models))
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, exists := seen[model]; exists {
				continue
			}
			seen[model] = struct{}{}
			pool = append(pool, model)
		}
		if len(pool) > 0 {
			normalized[role] = pool
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func applySecurityEnvOverrides(security *SecurityConfig, report *compat.Report) {
	if value, ok := lookupBoolEnvFallbackReported(report, "AUTOTO_EXPOSED", "CODEHARBOR_EXPOSED"); ok {
		security.Exposed = value
	}
	if strings.TrimSpace(security.AccessPasswordHash) != "" {
		// A host-local password rotation persists a hash. Once present, it is the
		// durable authority and must not be shadowed by a stale startup env value.
		security.AccessPassword = ""
	} else if value := firstEnvFallback(report, "AUTOTO_ACCESS_PASSWORD", "CODEHARBOR_ACCESS_PASSWORD"); value != "" {
		// Environment credentials seed the initial password but are never saved
		// by sanitizeConfigForDisk. A localhost rotation can replace them with a
		// durable hash for subsequent restarts.
		security.AccessPassword = value
	}
	if value, ok := lookupBoolEnvFallbackReported(report, "AUTOTO_ALLOW_REMOTE_FULL_ACCESS", "CODEHARBOR_ALLOW_REMOTE_FULL_ACCESS"); ok {
		security.AllowRemoteFullAccess = value
	}
	if value := firstEnvFallback(report, "AUTOTO_DEFAULT_REMOTE_ACCESS_MODE", "CODEHARBOR_DEFAULT_REMOTE_ACCESS_MODE"); value != "" {
		security.DefaultRemoteAccessMode = value
	}
	if value, ok := lookupBoolEnvFallbackReported(report, "AUTOTO_ALLOW_REMOTE_NATIVE_PICKER", "CODEHARBOR_ALLOW_REMOTE_NATIVE_PICKER"); ok {
		security.AllowRemoteNativePicker = value
	}
	if value, ok := lookupBoolEnvFallbackReported(report, "AUTOTO_REMOTE_TERMINAL", "CODEHARBOR_REMOTE_TERMINAL"); ok {
		security.AllowRemoteTerminal = value
	}
}

func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

func (c Config) GatewayAddr() string {
	return net.JoinHostPort(c.Gateway.Host, strconv.Itoa(c.Gateway.Port))
}

func (p ProviderConfig) IsConfigured() bool {
	switch p.Type {
	case ProviderTypeCodex, ProviderTypeGemini, ProviderTypeGrok, ProviderTypeKimi, ProviderTypeKiro:
		return false
	default:
		return p.APIKey != "" || p.APIKeyOptional
	}
}

func (p ProviderConfig) Summary() ProviderSummary {
	return ProviderSummary{
		Name:           p.Name,
		Type:           p.Type,
		Profile:        p.Profile,
		BaseURL:        p.BaseURL,
		Model:          p.Model,
		MaxTokens:      p.MaxTokens,
		Configured:     p.IsConfigured(),
		APIKeyOptional: p.APIKeyOptional,
		GatewayEnabled: p.GatewayEnabled,
		Enabled:        !p.Disabled,
		Origin:         ProviderOriginForName(p.Name),
	}
}

// ProviderOriginForName determines provider ownership on the server. API
// clients never supply this value, so a custom provider cannot self-identify
// as built in to bypass lifecycle restrictions.
func ProviderOriginForName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai", "anthropic", ProviderTypeCodex, "ollama", "openai-compatible", ProviderProfileCLIProxyAPI, ProviderTypeGemini, ProviderTypeGrok, ProviderTypeKimi, ProviderTypeKiro:
		return ProviderOriginBuiltin
	default:
		return ProviderOriginCustom
	}
}

func IsBuiltinProviderName(name string) bool {
	return ProviderOriginForName(name) == ProviderOriginBuiltin
}

func (p ProvidersConfig) Summaries() []ProviderSummary {
	providers := normalizeProviders(p).Instances
	out := make([]ProviderSummary, 0, len(providers))
	for _, provider := range providers {
		out = append(out, provider.Summary())
	}
	return out
}

func providerProxyURLWithoutCredentials(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil {
		if parsed.User != nil {
			parsed.User = nil
			return parsed.String()
		}
		if parsed.Host != "" || !strings.Contains(raw, "@") {
			return parsed.String()
		}
	}
	// Fail closed even for malformed legacy values: never let a userinfo segment
	// survive an ordinary config save.
	if scheme := strings.Index(raw, "://"); scheme >= 0 {
		authorityStart := scheme + 3
		authorityEnd := len(raw)
		for _, separator := range []string{"/", "?", "#"} {
			if index := strings.Index(raw[authorityStart:], separator); index >= 0 && authorityStart+index < authorityEnd {
				authorityEnd = authorityStart + index
			}
		}
		if at := strings.LastIndex(raw[authorityStart:authorityEnd], "@"); at >= 0 {
			return raw[:authorityStart] + raw[authorityStart+at+1:]
		}
	}
	if strings.Contains(raw, "@") {
		return ""
	}
	return raw
}

func normalizeProviders(p ProvidersConfig) ProvidersConfig {
	if p.OpenAICompatible != nil {
		legacy := *p.OpenAICompatible
		if legacy.Name == "" {
			legacy.Name = "openai-compatible"
		}
		if legacy.Type == "" {
			legacy.Type = "openai-compatible"
		}
		p.Instances = upsertProvider(p.Instances, legacy)
		p.OpenAICompatible = nil
	}
	for i := range p.Instances {
		provider := &p.Instances[i]
		provider.Name = strings.TrimSpace(provider.Name)
		provider.Type = strings.TrimSpace(provider.Type)
		provider.Profile = strings.TrimSpace(provider.Profile)
		provider.ProxyURL = providerProxyURLWithoutCredentials(provider.ProxyURL)
		provider.UserAgent = strings.TrimSpace(provider.UserAgent)
		if len(provider.RequestHeaders) > 0 {
			provider.RequestHeaders = append([]ProviderRequestHeader(nil), provider.RequestHeaders...)
			for headerIndex := range provider.RequestHeaders {
				provider.RequestHeaders[headerIndex].Name = strings.TrimSpace(provider.RequestHeaders[headerIndex].Name)
			}
		}
		if provider.Type == "" {
			provider.Type = provider.Name
		}
		if provider.Name == "" {
			provider.Name = provider.Type
		}
		if provider.Profile == "" {
			provider.Profile = legacyProviderProfile(provider.Name)
		}
		applyProviderEnvDefaults(provider)
		provider.Model = strings.TrimSpace(provider.Model)
		provider.Models = NormalizeProviderModels(provider.Models, provider.Model)
		if strings.EqualFold(provider.Profile, ProviderProfileCLIProxyAPI) {
			provider.GatewayEnabled = false
		}
	}
	return p
}

// legacyProviderProfile is the single compatibility boundary for historic provider names.
func legacyProviderProfile(name string) string {
	if strings.EqualFold(strings.TrimSpace(name), ProviderProfileCLIProxyAPI) {
		return ProviderProfileCLIProxyAPI
	}
	return ""
}

// NormalizeProviderModels trims names, removes duplicates, bounds context limits,
// and keeps the configured default model addressable for legacy configurations.
func NormalizeProviderModels(models []ProviderModelConfig, defaultModel string) []ProviderModelConfig {
	defaultModel = strings.TrimSpace(defaultModel)
	seen := make(map[string]struct{}, len(models)+1)
	normalized := make([]ProviderModelConfig, 0, len(models)+1)
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		limit := model.ContextTokenLimit
		if limit < 0 {
			limit = 0
		} else if limit > ProviderModelContextTokenLimitMax {
			limit = ProviderModelContextTokenLimitMax
		}
		seen[name] = struct{}{}
		normalized = append(normalized, ProviderModelConfig{Name: name, ContextTokenLimit: limit, ImageGeneration: model.ImageGeneration})
	}
	if defaultModel != "" {
		if _, exists := seen[defaultModel]; !exists {
			normalized = append(normalized, ProviderModelConfig{Name: defaultModel})
		}
	}
	return normalized
}

func (p ProviderConfig) ModelContextTokenLimit(model string) int {
	model = strings.TrimSpace(model)
	for _, configured := range p.Models {
		if strings.TrimSpace(configured.Name) == model && configured.ContextTokenLimit > 0 {
			return configured.ContextTokenLimit
		}
	}
	return 0
}

func (p ProviderConfig) ModelImageGeneration(model string) (enabled bool, known bool) {
	model = strings.TrimSpace(model)
	for _, configured := range p.Models {
		if strings.TrimSpace(configured.Name) == model {
			return configured.ImageGeneration, true
		}
	}
	return false, false
}

// NormalizeProviderConfig applies the same compatibility defaults used when loading config.
func NormalizeProviderConfig(provider ProviderConfig) ProviderConfig {
	return normalizeProviders(ProvidersConfig{Instances: []ProviderConfig{provider}}).Instances[0]
}

func applyProviderEnvDefaults(provider *ProviderConfig) {
	if applyNativeProviderDefaults(provider) {
		return
	}
	if provider.Profile == ProviderProfileCLIProxyAPI {
		applyCLIProxyAPIEnvDefaults(provider)
		return
	}
	if provider.Name == "groq" {
		applyGroqEnvDefaults(provider)
		return
	}
	switch provider.Type {
	case ProviderTypeCodex:
		provider.Name = "codex"
		if provider.BaseURL == "" {
			provider.BaseURL = "https://chatgpt.com/backend-api/codex"
		}
		if provider.Model == "" {
			provider.Model = getenv("CODEX_MODEL", "gpt-5.5")
		}
	case "openai":
		if provider.APIKey == "" {
			provider.APIKey = os.Getenv("OPENAI_API_KEY")
		}
		if provider.Model == "" {
			provider.Model = getenv("OPENAI_MODEL", "gpt-4.1-mini")
		}
	case "anthropic":
		if provider.APIKey == "" {
			provider.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if provider.Model == "" {
			provider.Model = getenv("ANTHROPIC_MODEL", "claude-sonnet-4-5")
		}
		if provider.MaxTokens <= 0 {
			provider.MaxTokens = 4096
		}
	case "openai-compatible":
		if provider.BaseURL == "" {
			provider.BaseURL = getenv("OPENAI_COMPATIBLE_BASE_URL", getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"))
		}
		if provider.APIKey == "" {
			provider.APIKey = getenv("OPENAI_COMPATIBLE_API_KEY", os.Getenv("OPENAI_API_KEY"))
		}
		if provider.Model == "" {
			provider.Model = getenv("OPENAI_COMPATIBLE_MODEL", getenv("OPENAI_MODEL", "gpt-4.1-mini"))
		}
	case "gemini-interactions":
		if provider.BaseURL == "" {
			provider.BaseURL = getenv("GEMINI_INTERACTIONS_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/interactions")
		}
		if provider.APIKey == "" {
			provider.APIKey = os.Getenv("GEMINI_API_KEY")
		}
		if provider.Model == "" {
			provider.Model = getenv("GEMINI_MODEL", "gemini-2.5-pro")
		}
	}
}

func defaultCodexProvider() ProviderConfig {
	provider := ProviderConfig{Name: "codex", Type: ProviderTypeCodex}
	applyProviderEnvDefaults(&provider)
	return provider
}

func applyGroqEnvDefaults(provider *ProviderConfig) {
	provider.Name = "groq"
	provider.Type = "openai-compatible"
	if provider.BaseURL == "" {
		provider.BaseURL = "https://api.groq.com/openai/v1"
	}
	if provider.APIKey == "" {
		provider.APIKey = os.Getenv("GROQ_API_KEY")
	}
	if provider.Model == "" {
		provider.Model = getenv("GROQ_MODEL", "openai/gpt-oss-20b")
	}
}

func applyCLIProxyAPIEnvDefaults(provider *ProviderConfig) {
	provider.Profile = ProviderProfileCLIProxyAPI
	if provider.Name == "" {
		provider.Name = "cliproxyapi"
	}
	provider.Type = "openai-compatible"
	provider.APIKeyOptional = true
	if provider.BaseURL == "" {
		provider.BaseURL = firstEnv("CLIPROXYAPI_BASE_URL", "CLIPROXY_BASE_URL", "CLI_PROXY_API_BASE_URL", "CPA_BASE_URL")
	}
	if provider.BaseURL == "" {
		provider.BaseURL = "http://127.0.0.1:8317/v1"
	}
	if provider.APIKey == "" {
		provider.APIKey = firstEnv("CLIPROXYAPI_API_KEY", "CLIPROXY_API_KEY", "CLI_PROXY_API_KEY", "CPA_API_KEY")
	}
	if provider.Model == "" {
		provider.Model = firstEnv("CLIPROXYAPI_MODEL", "CLIPROXY_MODEL", "CLI_PROXY_MODEL", "CPA_MODEL")
	}
	if provider.Model == "" {
		provider.Model = "gpt-5.5"
	}
}

func upsertProvider(providers []ProviderConfig, provider ProviderConfig) []ProviderConfig {
	for i, existing := range providers {
		if existing.Name == provider.Name {
			providers[i] = provider
			return providers
		}
	}
	return append(providers, provider)
}

func Save(path string, cfg Config) error {
	path, err := ResolvePath(path)
	if err != nil {
		return err
	}
	if cfg.SchemaVersion > CurrentConfigVersion {
		return fmt.Errorf("config schema version %d is newer than supported version %d", cfg.SchemaVersion, CurrentConfigVersion)
	}
	if err := ensureConfigParent(path); err != nil {
		return err
	}
	cfg, err = preserveSecurityEnvOverrides(path, cfg)
	if err != nil {
		return err
	}
	return writeDefaultConfig(path, cfg)
}

// preserveSecurityEnvOverrides keeps security settings supplied by the
// environment out of ordinary saves. When a config already exists, its durable
// values win; when it does not, the zero values are the secure defaults.
func preserveSecurityEnvOverrides(path string, cfg Config) (Config, error) {
	_, exposedFromEnv := lookupBoolEnvFallback("AUTOTO_EXPOSED", "CODEHARBOR_EXPOSED")
	_, fullAccessFromEnv := lookupBoolEnvFallback("AUTOTO_ALLOW_REMOTE_FULL_ACCESS", "CODEHARBOR_ALLOW_REMOTE_FULL_ACCESS")
	accessModeFromEnv := firstEnv("AUTOTO_DEFAULT_REMOTE_ACCESS_MODE", "CODEHARBOR_DEFAULT_REMOTE_ACCESS_MODE") != ""
	_, nativePickerFromEnv := lookupBoolEnvFallback("AUTOTO_ALLOW_REMOTE_NATIVE_PICKER", "CODEHARBOR_ALLOW_REMOTE_NATIVE_PICKER")
	_, terminalFromEnv := lookupBoolEnvFallback("AUTOTO_REMOTE_TERMINAL", "CODEHARBOR_REMOTE_TERMINAL")
	if !exposedFromEnv && !fullAccessFromEnv && !accessModeFromEnv && !nativePickerFromEnv && !terminalFromEnv {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	var persisted struct {
		Security SecurityConfig `json:"security"`
	}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
	} else if err := json.Unmarshal(data, &persisted); err != nil {
		return Config{}, fmt.Errorf("parse existing config before save: %w", err)
	}

	if exposedFromEnv {
		cfg.Security.Exposed = persisted.Security.Exposed
	}
	if fullAccessFromEnv {
		cfg.Security.AllowRemoteFullAccess = persisted.Security.AllowRemoteFullAccess
	}
	if accessModeFromEnv {
		cfg.Security.DefaultRemoteAccessMode = persisted.Security.DefaultRemoteAccessMode
	}
	if nativePickerFromEnv {
		cfg.Security.AllowRemoteNativePicker = persisted.Security.AllowRemoteNativePicker
	}
	if terminalFromEnv {
		cfg.Security.AllowRemoteTerminal = persisted.Security.AllowRemoteTerminal
	}
	return cfg, nil
}

func writeDefaultConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(sanitizeConfigForDisk(cfg), "", "  ")
	if err != nil {
		return err
	}
	return writeConfigAtomically(path, append(data, '\n'))
}

func writeConfigAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	completed := false
	defer func() {
		if !completed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	completed = true

	directory, err := os.Open(dir)
	if err != nil {
		// Rename already succeeded; parent-dir fsync is best-effort durability.
		return nil
	}
	defer directory.Close()
	// Windows rejects Sync on directory handles ("Access is denied").
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func sanitizeConfigForDisk(cfg Config) Config {
	cfg = migrateConfig(cfg)
	cfg.Gateway = normalizeGatewayConfig(cfg.Gateway)
	cfg.ContextManagement = normalizeContextManagementConfig(cfg.ContextManagement)
	cfg.Agent = normalizeAgentConfig(cfg.Agent)
	cfg.Auth.OAuthApp = cfg.Auth.OAuthApp.Normalized()
	cfg.Security = normalizeSecurityConfig(cfg.Security)
	cfg.Providers = normalizeProviders(cfg.Providers)
	cfg.Backends = normalizeBackends(cfg.Backends)
	cfg.Auth.JWTSecret = ""
	cfg.Security.AccessPassword = ""
	if len(cfg.Providers.Instances) > 0 {
		cfg.Providers.Instances = append([]ProviderConfig(nil), cfg.Providers.Instances...)
		for i := range cfg.Providers.Instances {
			cfg.Providers.Instances[i].APIKey = ""
		}
	}
	if cfg.Providers.OpenAICompatible != nil {
		legacy := *cfg.Providers.OpenAICompatible
		legacy.APIKey = ""
		cfg.Providers.OpenAICompatible = &legacy
	}
	if len(cfg.Backends.Instances) > 0 {
		cfg.Backends.Instances = append([]BackendConfig(nil), cfg.Backends.Instances...)
		for i := range cfg.Backends.Instances {
			cfg.Backends.Instances[i].APIKey = ""
		}
	}
	return cfg
}

func defaultBackendsFromEnv(report *compat.Report) []BackendConfig {
	baseURL := firstEnvFallback(report, "AUTOTO_AGENT_BACKEND_URL", "CODEHARBOR_AGENT_BACKEND_URL", "OPENHANDS_AGENT_SERVER_URL", "AGENT_SERVER_URL")
	if baseURL == "" {
		return nil
	}
	name := firstEnvFallback(report, "AUTOTO_AGENT_BACKEND_NAME", "CODEHARBOR_AGENT_BACKEND_NAME")
	if name == "" {
		name = "Local Agent Server"
	}
	kind := firstEnvFallback(report, "AUTOTO_AGENT_BACKEND_KIND", "CODEHARBOR_AGENT_BACKEND_KIND")
	if kind == "" {
		kind = "local"
	}
	return []BackendConfig{
		{
			Name:    name,
			Kind:    kind,
			BaseURL: normalizeURL(baseURL),
			APIKey:  firstEnvFallback(report, "AUTOTO_AGENT_BACKEND_API_KEY", "CODEHARBOR_AGENT_BACKEND_API_KEY", "OPENHANDS_SESSION_API_KEY", "AGENT_SERVER_API_KEY"),
			Active:  true,
		},
	}
}

func normalizeBackends(backends BackendsConfig) BackendsConfig {
	for i := range backends.Instances {
		backend := &backends.Instances[i]
		backend.Name = strings.TrimSpace(backend.Name)
		backend.Kind = strings.TrimSpace(backend.Kind)
		if backend.Kind == "" {
			backend.Kind = "local"
		}
		if backend.Kind != "cloud" {
			backend.Kind = "local"
		}
		backend.BaseURL = normalizeURL(backend.BaseURL)
	}
	return backends
}

func normalizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(trimmed, "/")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}
