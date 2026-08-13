package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"autoto/internal/db"
	"autoto/internal/mcp"
	"autoto/internal/secrets"
	"autoto/internal/tools"
)

const (
	MaxPluginTools              = 64
	MaxPluginToolDescription    = 2 << 10
	MaxPluginToolSchemaTotal    = 256 << 10
	DefaultPluginTimeout        = 20 * time.Second
	DefaultPluginStderrLimit    = 64 << 10
	DefaultPluginResponseLimit  = 1 << 20
	DefaultPluginOutputMaxBytes = 256 << 10
)

type MCPClient interface {
	Initialize(context.Context) error
	ListTools(context.Context) ([]mcp.Tool, error)
	CallTool(context.Context, string, json.RawMessage) (mcp.ToolCallResult, error)
	Close() error
}

type MCPStarter func(context.Context, mcp.StdioConfig) (MCPClient, error)
type Option func(*Service)

func WithMCPStarter(starter MCPStarter) Option {
	return func(service *Service) {
		if starter != nil {
			service.startMCP = starter
		}
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(service *Service) {
		if timeout > 0 {
			service.timeout = timeout
		}
	}
}

// WithPoolIdleTTL overrides how long an idle pooled plugin process stays warm
// before it is closed. Intended for tests.
func WithPoolIdleTTL(ttl time.Duration) Option {
	return func(service *Service) {
		if ttl > 0 {
			service.pool = newProcessPool(ttl)
		}
	}
}

type Service struct {
	lifecycleMu sync.Mutex
	store       *db.Store
	resolver    secrets.Resolver
	startMCP    MCPStarter
	pool        *processPool
	timeout     time.Duration
	outputMax   int
	stderrMax   int
	responseMax int64
}

func NewService(store *db.Store, resolver secrets.Resolver, options ...Option) *Service {
	service := &Service{
		store: store, resolver: resolver,
		startMCP: func(ctx context.Context, cfg mcp.StdioConfig) (MCPClient, error) { return mcp.StartStdio(ctx, cfg) },
		pool:     newProcessPool(defaultPoolIdleTTL),
		timeout:  DefaultPluginTimeout, outputMax: DefaultPluginOutputMaxBytes,
		stderrMax: DefaultPluginStderrLimit, responseMax: DefaultPluginResponseLimit,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

// Close terminates every pooled plugin process. The service remains usable;
// later tool calls simply fall back to fresh per-call processes.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.pool.close()
}

type Health struct {
	PluginID  string `json:"pluginId"`
	Healthy   bool   `json:"healthy"`
	ToolCount int    `json:"toolCount"`
	CheckedAt string `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

func (s *Service) Install(ctx context.Context, rootPath string) (db.Plugin, error) {
	if s == nil || s.store == nil {
		return db.Plugin{}, errors.New("plugin store is unavailable")
	}
	manifest, err := LoadManifest(rootPath)
	if err != nil {
		return db.Plugin{}, err
	}
	return s.store.CreatePlugin(ctx, db.Plugin{
		Slug: manifest.Slug, Name: manifest.Name, Version: manifest.Version, Description: manifest.Description,
		ManifestVersion: manifest.APIVersion, RootPath: manifest.RootPath, Command: manifest.Command,
		Args: cloneStrings(manifest.Args), Env: cloneStringMap(manifest.Env), SecretRefs: cloneStringMap(manifest.SecretRefs),
		Enabled: false, Status: "disabled", ManifestHash: manifest.Hash,
	})
}

func (s *Service) List(ctx context.Context) ([]db.Plugin, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("plugin store is unavailable")
	}
	return s.store.ListPlugins(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (db.Plugin, error) {
	if s == nil || s.store == nil {
		return db.Plugin{}, errors.New("plugin store is unavailable")
	}
	return s.store.GetPlugin(ctx, id)
}

func (s *Service) Enable(ctx context.Context, id string) (db.Plugin, error) {
	if s == nil || s.store == nil {
		return db.Plugin{}, errors.New("plugin store is unavailable")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	// Invalidate retained adapters and any warm process before resolving
	// secrets or launching code.
	plugin, err := s.store.UpdatePluginStatus(ctx, id, "enabling", false, db.Now(), "")
	if err != nil {
		return db.Plugin{}, err
	}
	s.pool.invalidate(id)
	pluginTools, checkedAt, values, err := s.discover(ctx, plugin)
	if err != nil {
		err = redactPluginError(err, values)
		_, _ = s.store.UpdatePluginStatus(context.Background(), id, "error", false, db.Now(), boundedText(err.Error(), 4096))
		return db.Plugin{}, err
	}
	snapshot, err := s.store.EnablePluginWithTools(ctx, id, pluginTools, checkedAt)
	if err != nil {
		err = redactPluginError(err, values)
		_, _ = s.store.UpdatePluginStatus(context.Background(), id, "error", false, db.Now(), boundedText(err.Error(), 4096))
		return db.Plugin{}, err
	}
	return snapshot.Plugin, nil
}

func (s *Service) Disable(ctx context.Context, id string) (db.Plugin, error) {
	if s == nil || s.store == nil {
		return db.Plugin{}, errors.New("plugin store is unavailable")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	plugin, err := s.store.UpdatePluginStatus(ctx, id, "disabled", false, db.Now(), "")
	if err != nil {
		return db.Plugin{}, err
	}
	s.pool.invalidate(id)
	return plugin, nil
}

// Discover refreshes the stored snapshot while preserving the plugin's current
// enabled state. Enable uses the atomic store operation instead.
func (s *Service) Discover(ctx context.Context, id string) ([]db.PluginTool, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("plugin store is unavailable")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	plugin, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !plugin.Enabled {
		return nil, errors.New("plugin is disabled")
	}
	pluginTools, checkedAt, values, err := s.discover(ctx, plugin)
	if err != nil {
		err = redactPluginError(err, values)
		_, _ = s.store.UpdatePluginStatus(context.Background(), id, "error", false, db.Now(), boundedText(err.Error(), 4096))
		return nil, err
	}
	stored, err := s.store.ReplacePluginTools(ctx, id, pluginTools)
	if err != nil {
		return nil, redactPluginError(err, values)
	}
	status := "ready"
	if plugin.Enabled {
		status = "healthy"
	}
	if _, err := s.store.UpdatePluginStatus(ctx, id, status, plugin.Enabled, checkedAt, ""); err != nil {
		return nil, err
	}
	s.pool.invalidate(id)
	return stored, nil
}

// Update re-reads the manifest from the plugin's stored root path and adopts
// it, clearing the tool snapshot. The result is always disabled — even when
// the plugin was enabled or the manifest is unchanged — because a manifest
// change may alter the code being executed, so the user must re-confirm by
// re-enabling.
func (s *Service) Update(ctx context.Context, id string) (db.Plugin, error) {
	if s == nil || s.store == nil {
		return db.Plugin{}, errors.New("plugin store is unavailable")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	current, err := s.store.GetPlugin(ctx, id)
	if err != nil {
		return db.Plugin{}, err
	}
	manifest, err := LoadManifest(current.RootPath)
	if err != nil {
		return db.Plugin{}, err
	}
	updated, err := s.store.ReplacePluginManifest(ctx, db.Plugin{
		ID: current.ID, Slug: manifest.Slug, Name: manifest.Name, Version: manifest.Version, Description: manifest.Description,
		ManifestVersion: manifest.APIVersion, RootPath: manifest.RootPath, Command: manifest.Command,
		Args: cloneStrings(manifest.Args), Env: cloneStringMap(manifest.Env), SecretRefs: cloneStringMap(manifest.SecretRefs),
		Enabled: false, Status: "disabled", ManifestHash: manifest.Hash,
	})
	if err != nil {
		return db.Plugin{}, err
	}
	s.pool.invalidate(id)
	return updated, nil
}

func (s *Service) Uninstall(ctx context.Context, id string) error {
	if s == nil || s.store == nil {
		return errors.New("plugin store is unavailable")
	}
	if _, err := s.Disable(ctx, id); err != nil && !db.IsNotFound(err) {
		return err
	}
	// Disable already invalidated the pooled process; repeat defensively for
	// the tolerated not-found path so no warm process can outlive the row.
	s.pool.invalidate(id)
	return s.store.DeletePlugin(ctx, id)
}

func (s *Service) Health(ctx context.Context, id string) Health {
	health := Health{PluginID: id, CheckedAt: db.Now()}
	plugin, err := s.Get(ctx, id)
	if err != nil {
		health.Error = err.Error()
		return health
	}
	// A disabled plugin must never execute: report without launching anything.
	if !plugin.Enabled {
		health.Error = "plugin is disabled"
		return health
	}
	remote, _, values, err := s.discoverRemote(ctx, plugin)
	if err != nil {
		health.Error = err.Error()
		return health
	}
	if _, err := validateDiscoveredTools(plugin, remote, values); err != nil {
		health.Error = err.Error()
		return health
	}
	health.Healthy, health.ToolCount = true, len(remote)
	return health
}

func (s *Service) ConfiguredEnvironment(ctx context.Context, plugin db.Plugin) (map[string]bool, error) {
	configured := make(map[string]bool, len(plugin.Env)+len(plugin.SecretRefs))
	for key := range plugin.Env {
		configured[key] = true
	}
	for key, value := range plugin.SecretRefs {
		configured[key] = false
		if s.resolver == nil {
			continue
		}
		ref, err := secrets.ParseRef(value)
		if err != nil {
			return nil, fmt.Errorf("invalid stored plugin secret reference for %q", key)
		}
		secret, err := s.resolver.Resolve(ctx, ref)
		configured[key] = err == nil && secret != ""
	}
	return configured, nil
}

// HasTool reports whether name is present in the current enabled plugin tool snapshot.
func (s *Service) HasTool(ctx context.Context, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	listed, err := s.ListTools(ctx, tools.ResolutionContext{})
	if err != nil {
		return false, err
	}
	for _, tool := range listed {
		if tool != nil && tool.Name() == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) ListTools(ctx context.Context, scope tools.ResolutionContext) ([]tools.Tool, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("plugin store is unavailable")
	}
	// Agent-scoped resolution without a workspace binding hides product/plugin tools.
	// Process-wide inventory (empty AgentID, e.g. HasTool) still lists enabled plugins.
	if strings.TrimSpace(scope.AgentID) != "" && strings.TrimSpace(scope.CWD) == "" {
		return []tools.Tool{}, nil
	}
	enabled, err := s.store.ListEnabledPluginsWithTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tools.Tool, 0)
	seen := make(map[string]struct{})
	for _, item := range enabled {
		for _, stored := range item.Tools {
			key := strings.ToLower(stored.ExposedName)
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("duplicate enabled plugin tool name: %s", stored.ExposedName)
			}
			seen[key] = struct{}{}
			out = append(out, newPluginTool(s, item.Plugin, stored))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (s *Service) ResolveTool(ctx context.Context, scope tools.ResolutionContext, name string) (tools.Tool, error) {
	listed, err := s.ListTools(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, tool := range listed {
		if tool.Name() == name {
			return tool, nil
		}
	}
	return nil, errors.New("tool not found: " + strings.TrimSpace(name))
}

func (s *Service) discover(ctx context.Context, plugin db.Plugin) ([]db.PluginTool, string, []string, error) {
	remote, checkedAt, values, err := s.discoverRemote(ctx, plugin)
	if err != nil {
		return nil, "", values, err
	}
	pluginTools, err := validateDiscoveredTools(plugin, remote, values)
	return pluginTools, checkedAt, values, err
}

func (s *Service) discoverRemote(ctx context.Context, plugin db.Plugin) ([]mcp.Tool, string, []string, error) {
	manifest, err := validatePersistedManifest(plugin)
	if err != nil {
		return nil, "", nil, err
	}
	environment, values, err := s.resolveEnvironment(ctx, plugin)
	if err != nil {
		return nil, "", values, err
	}
	timeout := s.effectiveTimeout(manifest)
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := s.startMCP(opCtx, s.stdioConfig(plugin, environment, values, timeout))
	if err != nil {
		return nil, "", values, redactPluginError(err, values)
	}
	defer client.Close()
	if err := client.Initialize(opCtx); err != nil {
		return nil, "", values, redactPluginError(err, values)
	}
	remote, err := client.ListTools(opCtx)
	if err != nil {
		return nil, "", values, redactPluginError(err, values)
	}
	return remote, db.Now(), values, nil
}

func (s *Service) resolveEnvironment(ctx context.Context, plugin db.Plugin) (map[string]string, []string, error) {
	environment := cloneStringMap(plugin.Env)
	values := make([]string, 0, len(plugin.SecretRefs))
	if len(plugin.SecretRefs) > 0 && s.resolver == nil {
		return nil, values, errors.New("plugin secret resolver is not configured")
	}
	keys := make([]string, 0, len(plugin.SecretRefs))
	for key := range plugin.SecretRefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ref, err := secrets.ParseRef(plugin.SecretRefs[key])
		if err != nil {
			return nil, values, fmt.Errorf("invalid stored plugin secret reference for %q", key)
		}
		value, err := s.resolver.Resolve(ctx, ref)
		if err != nil {
			return nil, values, fmt.Errorf("resolve plugin secret %q: %w", key, err)
		}
		environment[key] = value
		if value != "" {
			values = append(values, value)
		}
	}
	return environment, values, nil
}

// effectiveTimeout returns the per-plugin timeout from the freshly loaded,
// hash-validated manifest, falling back to the service default when the
// manifest does not set one. The timeout is never read from the database.
func (s *Service) effectiveTimeout(manifest Manifest) time.Duration {
	if manifest.TimeoutSeconds > 0 {
		return time.Duration(manifest.TimeoutSeconds) * time.Second
	}
	return s.timeout
}

func (s *Service) stdioConfig(plugin db.Plugin, environment map[string]string, values []string, timeout time.Duration) mcp.StdioConfig {
	return mcp.StdioConfig{
		Command: filepath.Join(plugin.RootPath, filepath.FromSlash(plugin.Command)), Args: cloneStrings(plugin.Args), CWD: plugin.RootPath,
		Env: environment, CleanEnv: true, Timeout: timeout, StderrLimit: s.stderrMax,
		ResponseLimit: s.responseMax, RedactValues: cloneStrings(values),
	}
}

// pooledStdioConfig configures a reusable plugin process: no process-lifetime
// timeout (per-call context deadlines govern each request and the pool owns
// shutdown) and a stdout budget sized for many calls. maxPooledCalls recycles
// the process long before that budget can starve a call.
func (s *Service) pooledStdioConfig(plugin db.Plugin, environment map[string]string, values []string) mcp.StdioConfig {
	cfg := s.stdioConfig(plugin, environment, values, 0)
	if cfg.ResponseLimit > 0 {
		cfg.ResponseLimit *= pooledResponseBudgetFactor
	}
	return cfg
}

func validateDiscoveredTools(plugin db.Plugin, remote []mcp.Tool, secretValues []string) ([]db.PluginTool, error) {
	if len(remote) > MaxPluginTools {
		return nil, fmt.Errorf("plugin exposes %d tools; maximum is %d", len(remote), MaxPluginTools)
	}
	seenRemote := make(map[string]struct{}, len(remote))
	seenExposed := make(map[string]struct{}, len(remote))
	totalSchema := 0
	checkedAt := db.Now()
	out := make([]db.PluginTool, 0, len(remote))
	for _, item := range remote {
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" || len(item.Name) > 128 || !utf8.ValidString(item.Name) || strings.ContainsRune(item.Name, 0) {
			return nil, errors.New("plugin tool has invalid remote name")
		}
		if containsConfiguredSecret(item.Name, secretValues) {
			return nil, errors.New("plugin tool metadata contains a configured secret")
		}
		remoteKey := strings.ToLower(item.Name)
		if _, duplicate := seenRemote[remoteKey]; duplicate {
			return nil, fmt.Errorf("plugin returned duplicate tool name %q", item.Name)
		}
		seenRemote[remoteKey] = struct{}{}
		description := strings.TrimSpace(item.Description)
		if containsConfiguredSecret(description, secretValues) || containsConfiguredSecret(string(item.InputSchema), secretValues) {
			return nil, errors.New("plugin tool metadata contains a configured secret")
		}
		if len(description) > MaxPluginToolDescription || !utf8.ValidString(description) || strings.ContainsRune(description, 0) {
			return nil, fmt.Errorf("plugin tool %q description exceeds %d bytes", item.Name, MaxPluginToolDescription)
		}
		schema, err := normalizeToolSchema(item.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("plugin tool %q input schema: %w", item.Name, err)
		}
		totalSchema += len(schema)
		if totalSchema > MaxPluginToolSchemaTotal {
			return nil, fmt.Errorf("plugin tool schemas exceed %d bytes", MaxPluginToolSchemaTotal)
		}
		exposed, err := exposedToolName(plugin.Slug, item.Name)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(exposed)
		if _, duplicate := seenExposed[key]; duplicate {
			return nil, fmt.Errorf("plugin tool naming collision for %q", exposed)
		}
		seenExposed[key] = struct{}{}
		out = append(out, db.PluginTool{PluginID: plugin.ID, RemoteName: item.Name, ExposedName: exposed, Description: description, InputSchemaJSON: schema, DiscoveredAt: checkedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExposedName < out[j].ExposedName })
	return out, nil
}

func normalizeToolSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		raw = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if !json.Valid(raw) {
		return nil, errors.New("must be valid JSON")
	}
	var schema map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil || schema == nil {
		return nil, errors.New("must be a JSON object")
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 64<<10 {
		return nil, errors.New("exceeds 64 KiB per-tool limit")
	}
	return encoded, nil
}

func exposedToolName(slug, remote string) (string, error) {
	var component strings.Builder
	lastUnderscore := false
	for _, char := range strings.ToLower(strings.TrimSpace(remote)) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-':
			component.WriteRune(char)
			lastUnderscore = false
		case char == '_':
			if !lastUnderscore {
				component.WriteByte('_')
				lastUnderscore = true
			}
		default:
			if component.Len() > 0 && !lastUnderscore {
				component.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	name := "plugin__" + slug + "__" + strings.Trim(component.String(), "_")
	if strings.HasSuffix(name, "__") || len(name) > 192 {
		return "", fmt.Errorf("plugin tool %q cannot be exposed safely", remote)
	}
	return name, nil
}

func validatePersistedManifest(plugin db.Plugin) (Manifest, error) {
	manifest, err := LoadManifest(plugin.RootPath)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Hash != plugin.ManifestHash || manifest.Slug != plugin.Slug || manifest.Command != plugin.Command {
		return Manifest{}, errors.New("plugin manifest changed; reinstall or update the plugin before enabling")
	}
	return manifest, nil
}

func containsConfiguredSecret(text string, values []string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func cloneStrings(input []string) []string { return append([]string(nil), input...) }
func cloneStringMap(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
func redactText(text string, values []string) string {
	sorted := cloneStrings(values)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, value := range sorted {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	return text
}
func redactPluginError(err error, values []string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactText(err.Error(), values))
}
func boundedText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return strings.ToValidUTF8(value[:max], "")
}

var _ tools.ToolSource = (*Service)(nil)
var _ tools.Resolver = (*Service)(nil)
