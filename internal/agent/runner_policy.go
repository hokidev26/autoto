package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"autoto/internal/db"
	"autoto/internal/tools"
)

type toolPermissionResolution struct {
	Decision string
	Reason   string
	Warning  string
	Source   string
	RuleID   string
	Scope    string
}

const (
	toolPermissionAllow = "allow"
	toolPermissionAsk   = "ask"
	toolPermissionDeny  = "deny"
)

func (r *Runner) resolveToolPermission(ctx context.Context, agentID, mode, toolName string, risk tools.Risk, input json.RawMessage) toolPermissionResolution {
	return r.resolveToolPermissionWithSession(ctx, agentID, mode, toolName, risk, input, true)
}

func (r *Runner) resolveToolPermissionWithSession(ctx context.Context, agentID, mode, toolName string, risk tools.Risk, input json.RawMessage, allowSession bool) toolPermissionResolution {
	if risk == tools.RiskDanger {
		warning := toolRiskWarning(toolName, input)
		slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", toolPermissionDeny, "source", decisionSourceHardDangerBlock)
		return toolPermissionResolution{Decision: toolPermissionDeny, Reason: warning, Warning: warning, Source: decisionSourceHardDangerBlock}
	}
	if mode == "readOnly" && risk != tools.RiskRead {
		slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", toolPermissionDeny, "source", decisionSourceReadOnlyCap)
		return toolPermissionResolution{Decision: toolPermissionDeny, Reason: string(risk) + " risk denied by readOnly permission mode", Warning: defaultApprovalWarning(toolName, risk, input), Source: decisionSourceReadOnlyCap}
	}
	if r != nil && r.store != nil {
		rules, err := r.store.ListToolPermissionRules(ctx)
		if err != nil {
			slog.Warn("load tool permission rules failed; requiring approval", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "error", err)
			return toolPermissionResolution{Decision: toolPermissionAsk, Reason: "tool permission policy unavailable; approval required", Warning: defaultApprovalWarning(toolName, risk, input), Source: decisionSourcePolicyUnavailable}
		}
		// The store returns the deterministic policy order: priority, match
		// specificity, deny/ask/allow safety precedence, then stable age/ID.
		// The first matching rule therefore defines the persisted policy result.
		for _, rule := range rules {
			if !toolPermissionRuleMatches(rule, mode, toolName, risk) {
				continue
			}
			decision := normalizedRuleDecision(rule.Decision)
			reason := toolPermissionRuleReason(rule)
			slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", decision, "source", decisionSourceRule, "ruleId", rule.ID, "rulePriority", rule.Priority, "ruleEnabled", rule.Enabled)
			return toolPermissionResolution{Decision: decision, Reason: reason, Warning: defaultApprovalWarning(toolName, risk, input), Source: decisionSourceRule, RuleID: rule.ID}
		}
	}
	if allowSession && r.hasSessionGrant(ctx, agentID, sessionGrantKey(toolName, input)) {
		slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", toolPermissionAllow, "source", decisionSourceSessionApproval)
		return toolPermissionResolution{Decision: toolPermissionAllow, Reason: "allowed by session approval", Source: decisionSourceSessionApproval, Scope: "session"}
	}
	prefs := db.DefaultWorkflowPreferences()
	if r != nil && r.store != nil {
		loaded, err := r.store.GetWorkflowPreferences(ctx)
		if err != nil {
			slog.Warn("load workflow preferences failed; requiring approval", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "error", err)
			return toolPermissionResolution{Decision: toolPermissionAsk, Reason: "workflow preferences unavailable; approval required", Warning: defaultApprovalWarning(toolName, risk, input), Source: decisionSourceWorkflowUnavailable}
		}
		prefs = loaded
	}
	resolution := r.defaultToolPermission(ctx, agentID, mode, toolName, risk, input, prefs, allowSession)
	resolution.Source = decisionSourceDefaultPolicy
	slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", resolution.Decision, "source", resolution.Source)
	return resolution
}

func (r *Runner) defaultToolPermission(ctx context.Context, agentID, mode, toolName string, risk tools.Risk, input json.RawMessage, prefs db.WorkflowPreferences, allowSession bool) toolPermissionResolution {
	switch risk {
	case tools.RiskRead:
		if !prefs.AllowReadOnlyByDefault {
			return toolPermissionResolution{Decision: toolPermissionAsk, Reason: "read risk requires approval by workflow preferences", Warning: defaultApprovalWarning(toolName, risk, input)}
		}
		if allowed(mode, toolName, risk) {
			return toolPermissionResolution{Decision: toolPermissionAllow, Reason: "allowed by permission mode"}
		}
	case tools.RiskWrite:
		if mode == "readOnly" {
			return toolPermissionResolution{Decision: toolPermissionDeny, Reason: "write risk denied by readOnly permission mode"}
		}
		if prefs.RequireConfirmationForWrites {
			return toolPermissionResolution{Decision: toolPermissionAsk, Reason: "write risk requires approval by workflow preferences", Warning: defaultApprovalWarning(toolName, risk, input)}
		}
		if allowed(mode, toolName, risk) {
			return toolPermissionResolution{Decision: toolPermissionAllow, Reason: "allowed by permission mode"}
		}
	case tools.RiskExec:
		if mode == "bypassPermissions" {
			return toolPermissionResolution{Decision: toolPermissionAllow, Reason: "allowed by bypassPermissions mode"}
		}
		if !prefs.RequireConfirmationForExec && execPermittedByMode(mode) {
			return toolPermissionResolution{Decision: toolPermissionAllow, Reason: "exec risk allowed by workflow preferences"}
		}
		if r.canAutoExecuteTool(ctx, agentID, mode, toolName, risk, input, allowSession) {
			return toolPermissionResolution{Decision: toolPermissionAllow, Reason: autoApprovalReason(toolName, input)}
		}
		if approvalRequired(mode, toolName, risk) {
			return toolPermissionResolution{Decision: toolPermissionAsk, Reason: defaultApprovalReason(risk), Warning: defaultApprovalWarning(toolName, risk, input)}
		}
	}
	return toolPermissionResolution{Decision: toolPermissionDeny, Reason: "tool call denied by permission mode"}
}

func toolPermissionRuleMatches(rule db.ToolPermissionRule, mode, toolName string, risk tools.Risk) bool {
	if !rule.Enabled {
		return false
	}
	return wildcardMatch(rule.Mode, mode) && wildcardMatch(rule.ToolName, toolName) && wildcardMatch(rule.Risk, string(risk))
}

func normalizedRuleDecision(decision string) string {
	switch strings.TrimSpace(decision) {
	case toolPermissionAllow, toolPermissionAsk, toolPermissionDeny:
		return strings.TrimSpace(decision)
	default:
		return toolPermissionAsk
	}
}

func wildcardMatch(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	return pattern == value
}

func toolPermissionRuleReason(rule db.ToolPermissionRule) string {
	prefix := fmt.Sprintf("tool permission rule matched (id=%s, priority=%d, decision=%s)", rule.ID, rule.Priority, normalizedRuleDecision(rule.Decision))
	if strings.TrimSpace(rule.Description) != "" {
		return prefix + ": " + strings.TrimSpace(rule.Description)
	}
	return prefix
}

func defaultApprovalReason(risk tools.Risk) string {
	switch risk {
	case tools.RiskRead:
		return "read risk requires approval"
	case tools.RiskWrite:
		return "write risk requires approval"
	case tools.RiskExec:
		return "exec risk requires approval"
	default:
		return "tool risk requires approval"
	}
}

func defaultApprovalWarning(toolName string, risk tools.Risk, input json.RawMessage) string {
	if risk == tools.RiskExec {
		if toolName == "Bash" {
			return "Bash 命令将访问本地 shell，请确认命令安全后再允许。"
		}
		return "该工具会启动本地进程或外部工具，请确认安全后再允许。"
	}
	if risk == tools.RiskWrite {
		return "该工具会修改本地工作区文件，请确认变更范围后再允许。"
	}
	if risk == tools.RiskRead {
		return "该只读工具被当前工作流策略要求人工批准。"
	}
	return toolRiskWarning(toolName, input)
}

func (r *Runner) canAutoExecuteTool(ctx context.Context, agentID, mode, toolName string, risk tools.Risk, input json.RawMessage, allowSession bool) bool {
	if allowed(mode, toolName, risk) {
		return true
	}
	if risk != tools.RiskExec {
		return false
	}
	if mode != "acceptEdits" && mode != "default" && mode != "dontAsk" {
		return false
	}
	if toolName == "Bash" && isWhitelistedExecCommand(tools.BashCommand(input)) {
		return true
	}
	return allowSession && r.hasSessionGrant(ctx, agentID, sessionGrantKey(toolName, input))
}

func execPermittedByMode(mode string) bool {
	switch mode {
	case "bypassPermissions", "acceptEdits", "default", "dontAsk":
		return true
	default:
		return false
	}
}

func approvalRequired(mode, toolName string, risk tools.Risk) bool {
	if risk != tools.RiskExec {
		return false
	}
	switch mode {
	case "acceptEdits", "default", "dontAsk":
		return true
	default:
		return false
	}
}

func sessionGrantKey(toolName string, input json.RawMessage) string {
	if toolName == "Bash" {
		return toolName + ":" + normalizeShellCommand(tools.BashCommand(input))
	}
	return toolName + ":" + strings.TrimSpace(string(input))
}

func autoApprovalReason(toolName string, input json.RawMessage) string {
	if toolName == "Bash" && isWhitelistedExecCommand(tools.BashCommand(input)) {
		return "auto-approved by built-in exec whitelist"
	}
	return "allowed by permission mode"
}

func autoApprovalReasonWithPolicy(toolName string, input json.RawMessage, reason string) string {
	if strings.TrimSpace(reason) != "" {
		return strings.TrimSpace(reason)
	}
	return autoApprovalReason(toolName, input)
}

func isWhitelistedExecCommand(command string) bool {
	command = strings.TrimSpace(command)
	facts := tools.AnalyzeBashCommand(command)
	if command == "" || !facts.ParseKnown || len(facts.Dangerous) > 0 || facts.Compound || facts.Pipeline || facts.Redirection || facts.Substitution || facts.Background || facts.CommandCount != 1 {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "go":
		return len(fields) >= 2 && oneOf(fields[1], "test", "vet", "build")
	case "npm":
		return len(fields) == 2 && fields[1] == "test" || len(fields) == 3 && fields[1] == "run" && oneOf(fields[2], "test", "build", "lint", "check")
	case "pnpm", "yarn", "bun":
		return len(fields) == 2 && oneOf(fields[1], "test", "build", "lint", "check")
	case "git":
		return len(fields) >= 2 && oneOf(fields[1], "status", "diff", "log", "show")
	default:
		return false
	}
}

func normalizeShellCommand(command string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(command)), " ")
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func permissionModeWithCap(mode, cap string) string {
	mode = strings.TrimSpace(mode)
	switch strings.TrimSpace(cap) {
	case "readOnly":
		return "readOnly"
	case "acceptEdits":
		if mode == "bypassPermissions" {
			return "acceptEdits"
		}
	}
	return mode
}

func allowed(mode, toolName string, risk tools.Risk) bool {
	if risk == tools.RiskDanger {
		return false
	}
	switch mode {
	case "readOnly":
		return risk == tools.RiskRead
	case "bypassPermissions":
		return true
	case "acceptEdits", "default", "dontAsk":
		return risk == tools.RiskRead || risk == tools.RiskWrite
	default:
		return toolName == "Read" || toolName == "Glob" || toolName == "Grep"
	}
}
