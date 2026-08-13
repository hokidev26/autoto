package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/db"
	"autoto/internal/mcp"
	"autoto/internal/tools"
)

type pluginTool struct {
	service     *Service
	pluginID    string
	revision    int64
	remoteName  string
	exposedName string
	description string
	schema      json.RawMessage
}

func newPluginTool(service *Service, plugin db.Plugin, stored db.PluginTool) tools.Tool {
	return &pluginTool{
		service: service, pluginID: plugin.ID, revision: plugin.Revision,
		remoteName: stored.RemoteName, exposedName: stored.ExposedName,
		description: stored.Description, schema: append(json.RawMessage(nil), stored.InputSchemaJSON...),
	}
}

func (t *pluginTool) Name() string { return t.exposedName }

func (t *pluginTool) Description() string { return t.description }

func (t *pluginTool) Schema() any { return append(json.RawMessage(nil), t.schema...) }

func (t *pluginTool) CatalogMetadata() tools.CatalogMetadata {
	return tools.CatalogMetadata{
		Domain:      "plugins",
		DisplayName: t.remoteName,
		Source:      "plugin",
		SourceID:    t.pluginID,
	}
}

func (t *pluginTool) Risk(json.RawMessage) tools.Risk { return tools.RiskExec }

func (t *pluginTool) Execute(ctx context.Context, call tools.Call, _ tools.Env) (tools.Result, error) {
	if t == nil || t.service == nil || t.service.store == nil {
		return tools.Result{}, errors.New("plugin tool is not configured")
	}
	plugin, err := t.current(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	manifest, err := validatePersistedManifest(plugin)
	if err != nil {
		return tools.Result{}, err
	}
	environment, values, err := t.service.resolveEnvironment(ctx, plugin)
	if err != nil {
		return tools.Result{}, redactPluginError(err, values)
	}
	timeout := t.service.effectiveTimeout(manifest)
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	key := poolKey{revision: plugin.Revision, manifestHash: plugin.ManifestHash, envHash: environmentFingerprint(environment)}
	client, pooled := t.service.pool.acquire(t.pluginID, key)
	if !pooled {
		// A pooled process has no process-lifetime bound: per-call deadlines
		// govern each request and the pool owns shutdown, so the spawn context
		// must survive this request while keeping its values.
		client, err = t.service.startMCP(context.WithoutCancel(ctx), t.service.pooledStdioConfig(plugin, environment, values))
		if err != nil {
			return tools.Result{}, redactPluginError(err, values)
		}
		if err := client.Initialize(opCtx); err != nil {
			_ = client.Close()
			return tools.Result{}, redactPluginError(err, values)
		}
	}
	// Revalidate immediately before delegation so adapters retained by a run
	// fail closed after disable, uninstall, or revision changes.
	if _, err := t.current(opCtx); err != nil {
		t.finish(key, client, pooled, err)
		return tools.Result{}, err
	}
	input := call.Input
	if len(input) == 0 || strings.TrimSpace(string(input)) == "" {
		input = json.RawMessage(`{}`)
	}
	result, err := client.CallTool(opCtx, t.remoteName, input)
	t.finish(key, client, pooled, err)
	if err != nil {
		return tools.Result{}, redactPluginError(err, values)
	}
	output := redactText(formatPluginToolResult(result), values)
	output, truncated := boundPluginOutput(output, t.service.outputMax)
	meta := map[string]any{"pluginId": t.pluginID, "remoteName": t.remoteName}
	if truncated {
		meta["truncated"] = true
	}
	return tools.Result{Output: output, IsError: result.IsError, Meta: meta}, nil
}

// finish returns the client to the pool. Acquired clients are released (and
// dropped on error); fresh clients are offered on success and closed on
// failure so a client that has ever errored is never reused.
func (t *pluginTool) finish(key poolKey, client MCPClient, pooled bool, callErr error) {
	if pooled {
		t.service.pool.release(t.pluginID, client, callErr)
		return
	}
	if callErr != nil {
		_ = client.Close()
		return
	}
	t.service.pool.offer(t.pluginID, key, client)
}

func (t *pluginTool) current(ctx context.Context) (db.Plugin, error) {
	plugin, err := t.service.store.GetPlugin(ctx, t.pluginID)
	if err != nil {
		return db.Plugin{}, fmt.Errorf("plugin tool unavailable: %w", err)
	}
	if !plugin.Enabled {
		return db.Plugin{}, errors.New("plugin tool unavailable: plugin is disabled")
	}
	if plugin.Revision != t.revision {
		return db.Plugin{}, errors.New("plugin tool unavailable: plugin revision changed")
	}
	stored, err := t.service.store.ListPluginTools(ctx, plugin.ID)
	if err != nil {
		return db.Plugin{}, fmt.Errorf("plugin tool unavailable: %w", err)
	}
	for _, candidate := range stored {
		if candidate.ExposedName == t.exposedName && candidate.RemoteName == t.remoteName && string(candidate.InputSchemaJSON) == string(t.schema) {
			return plugin, nil
		}
	}
	return db.Plugin{}, errors.New("plugin tool unavailable: tool snapshot changed")
}

func formatPluginToolResult(result mcp.ToolCallResult) string {
	if len(result.Content) == 0 || strings.TrimSpace(string(result.Content)) == "null" {
		return string(result.Raw)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Content, &blocks); err == nil {
		texts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				texts = append(texts, strings.TrimSpace(block.Text))
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return string(result.Content)
}

func boundPluginOutput(output string, limit int) (string, bool) {
	if limit <= 0 || len(output) <= limit {
		return output, false
	}
	const marker = "\n...[truncated]"
	keep := limit - len(marker)
	if keep < 0 {
		keep = 0
	}
	return strings.ToValidUTF8(output[:keep], "") + marker, true
}

var _ tools.Tool = (*pluginTool)(nil)
var _ tools.CatalogMetadataProvider = (*pluginTool)(nil)
