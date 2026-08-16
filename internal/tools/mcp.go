package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/mcp"
)

type MCPListToolsTool struct{}
type MCPCallToolTool struct{}

const ManagedAutomationMCPServerIDPrefix = "optional-automation-"

var managedAutomationMCPReadTools = map[string]map[string]struct{}{
	ManagedAutomationMCPServerID("playwright-mcp"): {
		"browser_console_messages": {},
		"browser_network_requests": {},
		"browser_snapshot":         {},
		"browser_take_screenshot":  {},
	},
	ManagedAutomationMCPServerID("chrome-devtools-mcp"): {
		"get_console_message":         {},
		"get_network_request":         {},
		"list_console_messages":       {},
		"list_network_requests":       {},
		"list_pages":                  {},
		"performance_analyze_insight": {},
		"take_screenshot":             {},
		"take_snapshot":               {},
	},
}

type mcpServerInput struct {
	ServerID string `json:"serverId,omitempty"`
	Timeout  int    `json:"timeout,omitempty"`
}

type mcpListToolsInput struct {
	ServerID string `json:"serverId"`
	Timeout  int    `json:"timeout,omitempty"`
}

type mcpCallToolInput struct {
	ServerID  string          `json:"serverId"`
	Timeout   int             `json:"timeout,omitempty"`
	ToolName  string          `json:"toolName"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func (MCPListToolsTool) Name() string { return "MCPListTools" }
func (MCPListToolsTool) Description() string {
	return "List tools exposed by a registered MCP server (serverId only). Consecutive calls to the same serverId from this agent reuse the same stdio session so browser pages and other server-side state persist. Freeform command/cwd/env from the model are rejected; the host pins the process working directory to the agent workspace."
}
func (MCPListToolsTool) Schema() any { return mcpListToolsInput{} }

func mcpListToolsInputFrom(raw json.RawMessage) (mcpListToolsInput, bool) {
	var parsed mcpListToolsInput
	if json.Unmarshal(raw, &parsed) != nil {
		return parsed, false
	}
	return parsed, true
}

func (MCPListToolsTool) Risk(input json.RawMessage) Risk {
	parsed, ok := mcpListToolsInputFrom(input)
	if ok && IsManagedAutomationMCPServerID(parsed.ServerID) {
		return RiskRead
	}
	return RiskExec
}

func (MCPListToolsTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input mcpListToolsInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	var listed []mcp.Tool
	if err := withMCPSession(ctx, env, mcpServerInput{ServerID: input.ServerID, Timeout: input.Timeout}, func(opCtx context.Context, client mcp.SessionHandle) error {
		var listErr error
		listed, listErr = client.ListTools(opCtx)
		return listErr
	}); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	data, _ := json.MarshalIndent(listed, "", "  ")
	return Result{Output: formatMCPTools(listed), Meta: map[string]any{"tools": len(listed), "raw": string(data)}}, nil
}

func (MCPCallToolTool) Name() string { return "MCPCallTool" }
func (MCPCallToolTool) Description() string {
	return "Call a tool on a registered MCP server (serverId + toolName). Consecutive calls to the same serverId from this agent reuse the same stdio session so browser pages and other server-side state persist. Freeform command/cwd/env from the model are rejected; the host pins the process working directory to the agent workspace."
}
func (MCPCallToolTool) Schema() any { return mcpCallToolInput{} }

func mcpCallToolInputFrom(raw json.RawMessage) (mcpCallToolInput, bool) {
	var parsed mcpCallToolInput
	if json.Unmarshal(raw, &parsed) != nil {
		return parsed, false
	}
	return parsed, true
}

func mcpCallToolRisk(parsed mcpCallToolInput) Risk {
	allowed, managed := managedAutomationMCPReadTools[strings.TrimSpace(parsed.ServerID)]
	if !managed {
		return RiskExec
	}
	toolName := strings.TrimSpace(parsed.ToolName)
	if _, ok := allowed[toolName]; ok && !managedAutomationMCPScreenshotWritesFile(toolName, parsed.Arguments) {
		return RiskRead
	}
	return RiskExec
}

func (MCPCallToolTool) Risk(input json.RawMessage) Risk {
	parsed, ok := mcpCallToolInputFrom(input)
	if !ok {
		return RiskExec
	}
	return mcpCallToolRisk(parsed)
}

func managedAutomationMCPScreenshotWritesFile(toolName string, arguments json.RawMessage) bool {
	if toolName != "browser_take_screenshot" && toolName != "take_screenshot" {
		return false
	}
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return false
	}
	var values map[string]any
	if json.Unmarshal(arguments, &values) != nil {
		return true
	}
	for _, key := range []string{"filename", "filePath", "path", "outputPath"} {
		if value, ok := values[key]; ok && strings.TrimSpace(fmt.Sprint(value)) != "" {
			return true
		}
	}
	return false
}

func (MCPCallToolTool) SessionApprovalAllowedForInput(input json.RawMessage) bool {
	return !ManagedAutomationMCPCallRequiresApproval(input)
}

func (MCPCallToolTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input mcpCallToolInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return Result{Output: "toolName is required", IsError: true}, nil
	}
	var result mcp.ToolCallResult
	if err := withMCPSession(ctx, env, mcpServerInput{ServerID: input.ServerID, Timeout: input.Timeout}, func(opCtx context.Context, client mcp.SessionHandle) error {
		var callErr error
		result, callErr = client.CallTool(opCtx, toolName, input.Arguments)
		return callErr
	}); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	out := formatMCPToolResult(result)
	return Result{Output: out, IsError: result.IsError, Meta: map[string]any{"toolName": toolName, "raw": string(result.Raw)}}, nil
}

func mcpServerInputFrom(raw json.RawMessage) mcpServerInput {
	var parsed mcpServerInput
	_ = json.Unmarshal(raw, &parsed)
	return parsed
}

func MCPCommand(input json.RawMessage) string {
	if serverID := strings.TrimSpace(mcpServerInputFrom(input).ServerID); serverID != "" {
		return "mcp server " + serverID
	}
	return "mcp server"
}

func ManagedAutomationMCPServerID(catalogID string) string {
	return ManagedAutomationMCPServerIDPrefix + strings.TrimSpace(catalogID)
}

func IsManagedAutomationMCPServerID(serverID string) bool {
	_, ok := managedAutomationMCPReadTools[strings.TrimSpace(serverID)]
	return ok
}

// ManagedAutomationMCPCallRequiresApproval identifies side-effecting or unknown
// calls on the two stable, backend-managed browser automation server IDs.
func ManagedAutomationMCPCallRequiresApproval(input json.RawMessage) bool {
	parsed, ok := mcpCallToolInputFrom(input)
	if !ok || !IsManagedAutomationMCPServerID(parsed.ServerID) {
		return false
	}
	return mcpCallToolRisk(parsed) == RiskExec
}

type mcpResolvedSession struct {
	Server      db.MCPServer
	Config      mcp.StdioConfig
	CallTimeout time.Duration
	Slot        string
	Key         string
}

func mcpSessionPool(env Env) *mcp.ProcessPool {
	if env.MCPSessions != nil {
		return env.MCPSessions
	}
	return mcp.DefaultSessionPool()
}

func resolveMCPSession(ctx context.Context, input mcpServerInput, env Env) (mcpResolvedSession, error) {
	serverID := strings.TrimSpace(input.ServerID)
	if serverID == "" {
		return mcpResolvedSession{}, errors.New("serverId is required; freeform MCP command/cwd/env from the model are not allowed")
	}
	if env.Store == nil {
		return mcpResolvedSession{}, fmt.Errorf("store is required for registered MCP server %q", serverID)
	}
	server, err := env.Store.GetMCPServer(ctx, serverID)
	if err != nil {
		if db.IsNotFound(err) {
			mcpSessionPool(env).InvalidateServer(serverID)
		}
		return mcpResolvedSession{}, err
	}
	if !server.Enabled {
		mcpSessionPool(env).InvalidateServer(serverID)
		return mcpResolvedSession{}, fmt.Errorf("mcp server %q is disabled", serverID)
	}
	command := strings.TrimSpace(server.Command)
	args := append([]string(nil), server.Args...)
	if command == "" {
		return mcpResolvedSession{}, fmt.Errorf("registered mcp server %q has an empty command", serverID)
	}
	if len(args) == 0 {
		parts := strings.Fields(command)
		if len(parts) > 1 {
			command = parts[0]
			args = parts[1:]
		}
	}
	// Host-pinned workspace: never honor model-supplied cwd. Prefer the agent CWD.
	cwd := strings.TrimSpace(env.CWD)
	if cwd == "" {
		return mcpResolvedSession{}, fmt.Errorf("agent working directory is required to start MCP server %q", serverID)
	}
	if configured := strings.TrimSpace(server.CWD); configured != "" {
		resolved, resolveErr := resolveInCWD(cwd, configured)
		if resolveErr != nil {
			return mcpResolvedSession{}, fmt.Errorf("registered mcp server %q cwd is outside the agent workspace: %w", serverID, resolveErr)
		}
		cwd = resolved
	}
	timeout := time.Duration(input.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return mcpResolvedSession{
		Server: server,
		Config: mcp.StdioConfig{
			Command: command,
			Args:    args,
			CWD:     cwd,
			Env:     cloneStringMap(server.Env),
		},
		CallTimeout: timeout,
		Slot:        mcp.SessionSlot(server.ID, env.AgentID, cwd),
		Key:         mcp.LaunchFingerprint(command, args, cwd, server.Env),
	}, nil
}

func revalidateMCPSession(ctx context.Context, env Env, resolved mcpResolvedSession) error {
	current, err := resolveMCPSession(ctx, mcpServerInput{ServerID: resolved.Server.ID}, env)
	if err != nil {
		return err
	}
	if current.Key != resolved.Key || current.Slot != resolved.Slot {
		return fmt.Errorf("mcp server %q launch configuration changed", resolved.Server.ID)
	}
	return nil
}

func startPooledMCPClient(ctx context.Context, resolved mcpResolvedSession) (mcp.SessionHandle, error) {
	client, err := mcp.StartStdio(context.WithoutCancel(ctx), resolved.Config)
	if err != nil {
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(ctx, resolved.CallTimeout)
	defer cancel()
	if err := client.Initialize(initCtx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func finishMCPSession(pool *mcp.ProcessPool, slot, key string, client mcp.SessionHandle, pooled bool, callErr error) {
	if client == nil {
		return
	}
	if pool == nil {
		_ = client.Close()
		return
	}
	if pooled {
		pool.Release(slot, client, callErr)
		return
	}
	if callErr != nil {
		_ = client.Close()
		return
	}
	pool.Offer(slot, key, client)
}

func withMCPSession(ctx context.Context, env Env, input mcpServerInput, fn func(context.Context, mcp.SessionHandle) error) error {
	resolved, err := resolveMCPSession(ctx, input, env)
	if err != nil {
		return err
	}
	pool := mcpSessionPool(env)
	client, pooled := pool.Acquire(resolved.Slot, resolved.Key)
	if !pooled {
		client, err = startPooledMCPClient(ctx, resolved)
		if err != nil {
			return err
		}
	}
	if err := revalidateMCPSession(ctx, env, resolved); err != nil {
		finishMCPSession(pool, resolved.Slot, resolved.Key, client, pooled, err)
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, resolved.CallTimeout)
	defer cancel()
	callErr := fn(opCtx, client)
	finishMCPSession(pool, resolved.Slot, resolved.Key, client, pooled, callErr)
	return callErr
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func formatMCPTools(tools []mcp.Tool) string {
	if len(tools) == 0 {
		return "No MCP tools returned."
	}
	var builder strings.Builder
	builder.WriteString("MCP tools:\n")
	for i, tool := range tools {
		builder.WriteString(fmt.Sprintf("\n%d. %s", i+1, tool.Name))
		if strings.TrimSpace(tool.Description) != "" {
			builder.WriteString(" — ")
			builder.WriteString(strings.TrimSpace(tool.Description))
		}
		if len(tool.InputSchema) > 0 {
			builder.WriteString("\n   inputSchema: ")
			builder.WriteString(string(tool.InputSchema))
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func formatMCPToolResult(result mcp.ToolCallResult) string {
	if len(result.Content) == 0 || strings.TrimSpace(string(result.Content)) == "null" {
		if len(result.Raw) > 0 {
			return string(result.Raw)
		}
		return ""
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
