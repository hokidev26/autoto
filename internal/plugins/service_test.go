package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/mcp"
	"autoto/internal/secrets"
	"autoto/internal/tools"
)

type resolverFunc func(context.Context, secrets.Ref) (string, error)

func (f resolverFunc) Resolve(ctx context.Context, ref secrets.Ref) (string, error) {
	return f(ctx, ref)
}

type fakeMCPClient struct {
	tools      []mcp.Tool
	initErr    error
	listErr    error
	callResult mcp.ToolCallResult
	callErr    error
	onCall     func()

	mu            sync.Mutex
	inits         int
	calls         int
	closes        int
	callDeadlines []time.Time
}

func (f *fakeMCPClient) Initialize(context.Context) error {
	f.mu.Lock()
	f.inits++
	f.mu.Unlock()
	return f.initErr
}
func (f *fakeMCPClient) ListTools(context.Context) ([]mcp.Tool, error) {
	return append([]mcp.Tool(nil), f.tools...), f.listErr
}
func (f *fakeMCPClient) CallTool(ctx context.Context, _ string, _ json.RawMessage) (mcp.ToolCallResult, error) {
	f.mu.Lock()
	f.calls++
	deadline, _ := ctx.Deadline()
	f.callDeadlines = append(f.callDeadlines, deadline)
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall()
	}
	return f.callResult, f.callErr
}
func (f *fakeMCPClient) Close() error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
	return nil
}

func (f *fakeMCPClient) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func (f *fakeMCPClient) lastCallDeadline() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.callDeadlines) == 0 {
		return time.Time{}
	}
	return f.callDeadlines[len(f.callDeadlines)-1]
}

type recordingStarter struct {
	mu        sync.Mutex
	clients   []*fakeMCPClient
	started   []*fakeMCPClient
	configs   []mcp.StdioConfig
	deadlines []time.Time
}

func (s *recordingStarter) start(ctx context.Context, cfg mcp.StdioConfig) (MCPClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, deadline)
	s.configs = append(s.configs, cfg)
	if len(s.clients) == 0 {
		return nil, errors.New("no fake MCP client")
	}
	client := s.clients[0]
	s.clients = s.clients[1:]
	s.started = append(s.started, client)
	return client, nil
}

func (s *recordingStarter) launchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.configs)
}

func TestServiceEnableDynamicToolSecurityLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := writePluginFixture(t, "demo", map[string]string{"MODE": "test"}, map[string]string{"TOKEN": "env:PLUGIN_TOKEN"})
	const secret = "resolved-plugin-secret"
	schema := json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false,"$defs":{"tag":{"type":"string"}}}`)
	starter := &recordingStarter{clients: []*fakeMCPClient{
		{tools: []mcp.Tool{{Name: "echo", Description: "Echo text", InputSchema: schema}}},
		{callResult: mcp.ToolCallResult{Content: json.RawMessage(`[{"type":"text","text":"value=resolved-plugin-secret"}]`)}},
	}}
	service := NewService(store, resolverFunc(func(_ context.Context, ref secrets.Ref) (string, error) {
		if ref.Name != "PLUGIN_TOKEN" {
			return "", errors.New("unexpected ref")
		}
		return secret, nil
	}), WithMCPStarter(starter.start))

	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Enabled || installed.Status != "disabled" || installed.SecretRefs["TOKEN"] != "env:PLUGIN_TOKEN" {
		t.Fatalf("install must persist disabled references only: %+v", installed)
	}
	if _, err := service.Discover(ctx, installed.ID); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled plugin discovery executed code: %v", err)
	}
	if len(starter.configs) != 0 {
		t.Fatalf("disabled plugin discovery started MCP: %+v", starter.configs)
	}
	enabled, err := service.Enable(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Status != "healthy" {
		t.Fatalf("unexpected enabled plugin: %+v", enabled)
	}
	if len(starter.configs) != 1 || !starter.configs[0].CleanEnv || starter.configs[0].Env["TOKEN"] != secret || starter.configs[0].Env["MODE"] != "test" {
		t.Fatalf("plugin MCP did not use isolated resolved environment: %+v", starter.configs)
	}
	if _, leaked := starter.configs[0].Env["AUTOTO_UNRELATED_PARENT_SECRET"]; leaked {
		t.Fatal("plugin config copied unrelated parent environment")
	}

	dynamic, err := service.ListTools(ctx, tools.ResolutionContext{AgentID: "agent-1", CWD: t.TempDir()})
	if err != nil || len(dynamic) != 1 {
		t.Fatalf("unexpected dynamic tools: %+v err=%v", dynamic, err)
	}
	adapter := dynamic[0]
	if adapter.Name() != "plugin__demo__echo" || adapter.Risk(nil) != tools.RiskExec {
		t.Fatalf("unexpected adapter identity/risk: %s %s", adapter.Name(), adapter.Risk(nil))
	}
	var gotSchema, wantSchema any
	if err := json.Unmarshal(adapter.Schema().(json.RawMessage), &gotSchema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schema, &wantSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSchema, wantSchema) {
		t.Fatalf("native schema was not preserved:\n got %v\nwant %v", gotSchema, wantSchema)
	}
	result, err := adapter.Execute(ctx, tools.Call{ID: "call-1", Name: adapter.Name(), Input: json.RawMessage(`{"message":"hi"}`)}, tools.Env{AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, secret) || !strings.Contains(result.Output, "[REDACTED]") {
		t.Fatalf("plugin output leaked resolved secret: %+v", result)
	}

	if _, err := service.Disable(ctx, installed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Execute(ctx, tools.Call{Name: adapter.Name(), Input: json.RawMessage(`{}`)}, tools.Env{}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("retained adapter did not fail closed after disable: %v", err)
	}
	if err := service.Uninstall(ctx, installed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Execute(ctx, tools.Call{Name: adapter.Name(), Input: json.RawMessage(`{}`)}, tools.Env{}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("retained adapter did not fail closed after uninstall: %v", err)
	}
}

func TestBoundPluginOutput(t *testing.T) {
	output, truncated := boundPluginOutput(strings.Repeat("x", 100), 24)
	if !truncated || len(output) > 24 || !strings.HasSuffix(output, "...[truncated]") {
		t.Fatalf("plugin output was not bounded: len=%d truncated=%v output=%q", len(output), truncated, output)
	}
}

func TestServiceEnableFailureStaysDisabledAndRejectsNameCollision(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := writePluginFixture(t, "collision", nil, nil)
	starter := &recordingStarter{clients: []*fakeMCPClient{{tools: []mcp.Tool{
		{Name: "foo/bar", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "foo.bar", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}}}
	service := NewService(store, nil, WithMCPStarter(starter.start))
	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(ctx, installed.ID); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected normalized name collision, got %v", err)
	}
	stored, err := store.GetPlugin(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled || stored.Status != "error" {
		t.Fatalf("failed enable must remain disabled: %+v", stored)
	}
	storedTools, err := store.ListPluginTools(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedTools) != 0 {
		t.Fatalf("failed enable persisted partial tool snapshot: %+v", storedTools)
	}
}

func TestPluginAdapterRejectsRevisionChange(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := writePluginFixture(t, "revision", nil, nil)
	starter := &recordingStarter{clients: []*fakeMCPClient{{tools: []mcp.Tool{{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}}}}}
	service := NewService(store, nil, WithMCPStarter(starter.start))
	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(ctx, installed.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListTools(ctx, tools.ResolutionContext{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list adapters: %+v %v", listed, err)
	}
	current, err := store.GetPlugin(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePluginStatus(ctx, current.ID, current.Status, true, db.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := listed[0].Execute(ctx, tools.Call{Name: listed[0].Name(), Input: json.RawMessage(`{}`)}, tools.Env{}); err == nil || !strings.Contains(err.Error(), "revision changed") {
		t.Fatalf("stale adapter did not reject revision change: %v", err)
	}
}

func TestServicePerPluginTimeoutAppliedToDiscoveryExecuteAndHealth(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	writePluginCommand(t, root, "server")
	manifest := validManifest("server")
	manifest["slug"] = "timed"
	manifest["timeoutSeconds"] = 45
	writeManifest(t, root, manifest)
	ping := []mcp.Tool{{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	starter := &recordingStarter{clients: []*fakeMCPClient{
		{tools: ping},
		{callResult: mcp.ToolCallResult{Content: json.RawMessage(`[{"type":"text","text":"pong"}]`)}},
		{tools: ping},
	}}
	service := NewService(store, nil, WithMCPStarter(starter.start))
	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(ctx, installed.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListTools(ctx, tools.ResolutionContext{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list adapters: %+v %v", listed, err)
	}
	if _, err := listed[0].Execute(ctx, tools.Call{Name: listed[0].Name(), Input: json.RawMessage(`{}`)}, tools.Env{}); err != nil {
		t.Fatal(err)
	}
	health := service.Health(ctx, installed.ID)
	if !health.Healthy || health.ToolCount != 1 {
		t.Fatalf("unexpected health for enabled plugin: %+v", health)
	}
	if len(starter.configs) != 3 {
		t.Fatalf("expected 3 launches (enable, execute, health), got %d", len(starter.configs))
	}
	// Discovery launches (enable, health) are ephemeral: the per-plugin
	// timeout bounds the whole process lifetime and the spawn context.
	for _, index := range []int{0, 2} {
		if starter.configs[index].Timeout != 45*time.Second {
			t.Fatalf("launch %d StdioConfig.Timeout = %s, want 45s", index, starter.configs[index].Timeout)
		}
		// With the 20s default the deadline could never exceed 20s from now.
		if remaining := time.Until(starter.deadlines[index]); remaining <= DefaultPluginTimeout {
			t.Fatalf("launch %d context deadline %s does not reflect per-plugin timeout", index, remaining)
		}
	}
	// The execute launch is pooled: no process-lifetime bound, an enlarged
	// stdout budget, and the per-plugin timeout applied per call instead.
	execute := starter.configs[1]
	if execute.Timeout != 0 {
		t.Fatalf("pooled execute launch must not carry a process-lifetime timeout: %s", execute.Timeout)
	}
	if execute.ResponseLimit != DefaultPluginResponseLimit*pooledResponseBudgetFactor {
		t.Fatalf("pooled execute launch response budget = %d", execute.ResponseLimit)
	}
	if !starter.deadlines[1].IsZero() {
		t.Fatalf("pooled spawn context must not inherit the request deadline: %s", starter.deadlines[1])
	}
	executeClient := starter.started[1]
	if remaining := time.Until(executeClient.lastCallDeadline()); remaining <= DefaultPluginTimeout {
		t.Fatalf("per-call deadline %s does not reflect per-plugin timeout", remaining)
	}
}

func TestServiceDefaultTimeoutWhenManifestOmitsTimeoutSeconds(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := writePluginFixture(t, "default-timeout", nil, nil)
	starter := &recordingStarter{clients: []*fakeMCPClient{{tools: []mcp.Tool{{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}}}}}
	service := NewService(store, nil, WithMCPStarter(starter.start))
	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(ctx, installed.ID); err != nil {
		t.Fatal(err)
	}
	if len(starter.configs) != 1 || starter.configs[0].Timeout != DefaultPluginTimeout {
		t.Fatalf("manifest without timeoutSeconds must use the service default: %+v", starter.configs)
	}
}

func TestServiceUpdateAdoptsManifestAndLeavesDisabled(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := writePluginFixture(t, "updateme", map[string]string{"MODE": "one"}, nil)
	starter := &recordingStarter{clients: []*fakeMCPClient{{tools: []mcp.Tool{{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}}}}}
	service := NewService(store, nil, WithMCPStarter(starter.start))
	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := service.Enable(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, map[string]any{
		"apiVersion": APIVersionV1Alpha1, "transport": TransportStdio,
		"slug": "updateme", "name": "Updated Plugin", "version": "2.0.0",
		"description": "updated", "command": "server",
		"args": []string{"--fast"}, "env": map[string]string{"MODE": "two"},
		"timeoutSeconds": 90,
	})
	expected, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.Status != "disabled" || updated.LastError != "" {
		t.Fatalf("update must leave the plugin disabled: %+v", updated)
	}
	if updated.Name != "Updated Plugin" || updated.Version != "2.0.0" || updated.Description != "updated" {
		t.Fatalf("manifest fields were not adopted: %+v", updated)
	}
	if len(updated.Args) != 1 || updated.Args[0] != "--fast" || updated.Env["MODE"] != "two" {
		t.Fatalf("args/env were not adopted: %+v", updated)
	}
	if updated.ManifestHash != expected.Hash || updated.ManifestHash == enabled.ManifestHash {
		t.Fatalf("manifest hash was not adopted: %+v", updated)
	}
	if updated.Revision <= enabled.Revision {
		t.Fatalf("update did not bump revision: %d <= %d", updated.Revision, enabled.Revision)
	}
	storedTools, err := store.ListPluginTools(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(storedTools) != 0 {
		t.Fatalf("update did not clear the tool snapshot: %+v", storedTools)
	}
	// A no-op update (hash unchanged) still succeeds and stays disabled.
	again, err := service.Update(ctx, installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Enabled || again.Status != "disabled" || again.ManifestHash != expected.Hash || again.Revision <= updated.Revision {
		t.Fatalf("unexpected no-op update result: %+v", again)
	}
}

func TestServiceUpdateRejectsInvalidManifestAndSlugConflict(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store, nil, WithMCPStarter((&recordingStarter{}).start))
	alphaRoot := writePluginFixture(t, "alpha", nil, nil)
	if _, err := service.Install(ctx, alphaRoot); err != nil {
		t.Fatal(err)
	}
	betaRoot := writePluginFixture(t, "beta", nil, nil)
	beta, err := service.Install(ctx, betaRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(betaRoot, ManifestFilename), []byte(`{invalid`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, beta.ID); err == nil {
		t.Fatal("expected invalid manifest to fail update")
	}
	writeManifest(t, betaRoot, map[string]any{
		"apiVersion": APIVersionV1Alpha1, "transport": TransportStdio,
		"slug": "alpha", "name": "Beta", "version": "1.0.0", "command": "server",
	})
	if _, err := service.Update(ctx, beta.ID); !db.IsConflict(err) {
		t.Fatalf("expected slug conflict, got %v", err)
	}
	stored, err := store.GetPlugin(ctx, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Slug != "beta" || stored.Revision != beta.Revision || stored.ManifestHash != beta.ManifestHash {
		t.Fatalf("failed update mutated stored plugin: %+v", stored)
	}
	if _, err := service.Update(ctx, "missing"); !db.IsNotFound(err) {
		t.Fatalf("expected unknown plugin not found, got %v", err)
	}
}

func TestServiceHealthDisabledPluginNeverLaunches(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := writePluginFixture(t, "sleepy", nil, nil)
	starter := &recordingStarter{}
	service := NewService(store, nil, WithMCPStarter(starter.start))
	installed, err := service.Install(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	health := service.Health(ctx, installed.ID)
	if health.Healthy || health.Error != "plugin is disabled" || health.PluginID != installed.ID || health.CheckedAt == "" || health.ToolCount != 0 {
		t.Fatalf("unexpected disabled plugin health: %+v", health)
	}
	missing := service.Health(ctx, "missing")
	if missing.Healthy || missing.Error == "" {
		t.Fatalf("unexpected unknown plugin health: %+v", missing)
	}
	if len(starter.configs) != 0 {
		t.Fatalf("health check launched a disabled or unknown plugin: %+v", starter.configs)
	}
}

func writePluginFixture(t *testing.T, slug string, env, refs map[string]string) string {
	t.Helper()
	root := t.TempDir()
	command := filepath.Join(root, "server")
	if err := os.WriteFile(command, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"apiVersion": APIVersionV1Alpha1, "transport": TransportStdio,
		"slug": slug, "name": "Plugin " + slug, "version": "1.0.0", "command": "server",
		"env": env, "secretRefs": refs,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
