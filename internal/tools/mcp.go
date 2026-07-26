package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	return "List tools exposed by a registered MCP server (serverId only). Freeform command/cwd/env from the model are rejected; the host pins the process working directory to the agent workspace."
}
func (MCPListToolsTool) Schema() any { return mcpListToolsInput{} }
func (MCPListToolsTool) Risk(input json.RawMessage) Risk {
	var parsed mcpListToolsInput
	if json.Unmarshal(input, &parsed) == nil && IsManagedAutomationMCPServerID(parsed.ServerID) {
		return RiskRead
	}
	return RiskExec
}

func (MCPListToolsTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input mcpListToolsInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	cfg, err := mcpConfigFromInput(ctx, mcpServerInput{ServerID: input.ServerID, Timeout: input.Timeout}, env)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	client, cancel, err := startMCPClient(ctx, cfg)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	defer cancel()
	tools, err := client.ListTools(ctx)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	data, _ := json.MarshalIndent(tools, "", "  ")
	return Result{Output: formatMCPTools(tools), Meta: map[string]any{"tools": len(tools), "raw": string(data)}}, nil
}

func (MCPCallToolTool) Name() string { return "MCPCallTool" }
func (MCPCallToolTool) Description() string {
	return "Call a tool on a registered MCP server (serverId + toolName). Freeform command/cwd/env from the model are rejected; the host pins the process working directory to the agent workspace."
}
func (MCPCallToolTool) Schema() any { return mcpCallToolInput{} }
func (MCPCallToolTool) Risk(input json.RawMessage) Risk {
	var parsed mcpCallToolInput
	if json.Unmarshal(input, &parsed) != nil {
		return RiskExec
	}
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
	cfg, err := mcpConfigFromInput(ctx, mcpServerInput{ServerID: input.ServerID, Timeout: input.Timeout}, env)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return Result{Output: "toolName is required", IsError: true}, nil
	}
	client, cancel, err := startMCPClient(ctx, cfg)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	defer cancel()
	result, err := client.CallTool(ctx, toolName, input.Arguments)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	out := formatMCPToolResult(result)
	return Result{Output: out, IsError: result.IsError, Meta: map[string]any{"toolName": toolName, "raw": string(result.Raw)}}, nil
}

func MCPCommand(input json.RawMessage) string {
	var parsed mcpServerInput
	_ = json.Unmarshal(input, &parsed)
	if serverID := strings.TrimSpace(parsed.ServerID); serverID != "" {
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
	var parsed mcpCallToolInput
	if json.Unmarshal(input, &parsed) != nil || !IsManagedAutomationMCPServerID(parsed.ServerID) {
		return false
	}
	return (MCPCallToolTool{}).Risk(input) == RiskExec
}

func mcpConfigFromInput(ctx context.Context, input mcpServerInput, env Env) (mcp.StdioConfig, error) {
	serverID := strings.TrimSpace(input.ServerID)
	if serverID == "" {
		return mcp.StdioConfig{}, fmt.Errorf("serverId is required; freeform MCP command/cwd/env from the model are not allowed")
	}
	if env.Store == nil {
		return mcp.StdioConfig{}, fmt.Errorf("store is required for registered MCP server %q", serverID)
	}
	server, err := env.Store.GetMCPServer(ctx, serverID)
	if err != nil {
		return mcp.StdioConfig{}, err
	}
	if !server.Enabled {
		return mcp.StdioConfig{}, fmt.Errorf("mcp server %q is disabled", serverID)
	}
	command := strings.TrimSpace(server.Command)
	args := append([]string(nil), server.Args...)
	if command == "" {
		return mcp.StdioConfig{}, fmt.Errorf("registered mcp server %q has an empty command", serverID)
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
		return mcp.StdioConfig{}, fmt.Errorf("agent working directory is required to start MCP server %q", serverID)
	}
	// If the registered server configures a relative cwd, resolve it inside the agent workspace.
	if configured := strings.TrimSpace(server.CWD); configured != "" {
		resolved, resolveErr := resolveInCWD(cwd, configured)
		if resolveErr != nil {
			return mcp.StdioConfig{}, fmt.Errorf("registered mcp server %q cwd is outside the agent workspace: %w", serverID, resolveErr)
		}
		cwd = resolved
	}
	timeout := time.Duration(input.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return mcp.StdioConfig{
		Command: command,
		Args:    args,
		CWD:     cwd,
		Env:     cloneStringMap(server.Env),
		Timeout: timeout,
	}, nil
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

func startMCPClient(ctx context.Context, cfg mcp.StdioConfig) (*mcp.Client, func(), error) {
	clientCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	client, err := mcp.StartStdio(clientCtx, cfg)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	cleanup := func() {
		_ = client.Close()
		cancel()
	}
	if err := client.Initialize(clientCtx); err != nil {
		cleanup()
		return nil, nil, err
	}
	return client, cleanup, nil
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
