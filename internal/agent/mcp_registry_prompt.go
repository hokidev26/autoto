package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	"autoto/internal/db"
)

const (
	mcpRegistryPromptMaxServers = 32
	mcpRegistryPromptMaxName    = 80
)

func (r *Runner) mcpRegistryPrompt(ctx context.Context, run db.Run) string {
	if r == nil || r.store == nil || isConversationRun(run) {
		return ""
	}
	servers, err := r.store.ListMCPServers(ctx)
	if err != nil {
		return "Registered backend MCP servers are host facts. The MCP registry could not be loaded for this run. Do not invent servers, serverIds, or connection failures."
	}
	enabled := make([]string, 0, len(servers))
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		name := sanitizeMCPRegistryName(server.Name)
		id := strings.TrimSpace(server.ID)
		if name == "" || id == "" {
			continue
		}
		enabled = append(enabled, "- name: "+name+"\n  serverId: "+id)
		if len(enabled) >= mcpRegistryPromptMaxServers {
			break
		}
	}
	var builder strings.Builder
	builder.WriteString("Registered backend MCP servers are host facts. Use MCPListTools then MCPCallTool with a listed serverId. Consecutive calls from this agent reuse the same stdio session. Do not invent servers, serverIds, or connection failures; if a server is missing from this list it is not registered or not enabled. Command, args, cwd, and env are intentionally omitted.")
	if len(enabled) == 0 {
		builder.WriteString("\nNo enabled backend MCP servers are registered.")
		return builder.String()
	}
	builder.WriteString("\nEnabled servers:\n")
	builder.WriteString(strings.Join(enabled, "\n"))
	return builder.String()
}

func sanitizeMCPRegistryName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	filtered := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if utf8.RuneCountInString(filtered) <= mcpRegistryPromptMaxName {
		return filtered
	}
	return string([]rune(filtered)[:mcpRegistryPromptMaxName])
}
