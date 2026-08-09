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
	managedApprovalRequired := managedAutomationMCPApprovalRequired(toolName, risk, input)
	// A command that static analysis marked serious-but-recoverable, or could not
	// classify at all, normally has to reach a human, above stored rules so a
	// wildcard allow rule cannot turn it into a silent execution.
	//
	// bypassPermissions is the one exception, because it is the user explicitly
	// choosing "allow everything" for this conversation. Keeping a mandatory
	// prompt there made the setting mean something other than its label, and the
	// prompts were unavoidable for ordinary work such as running a helper script.
	// The unrecoverable tier above is unaffected: it is a hard deny, not a
	// prompt, so bypassPermissions never reaches it.
	reviewRequired, reviewWarning := false, ""
	if mode != "bypassPermissions" {
		reviewRequired, reviewWarning = execRequiresHumanReview(toolName, risk, input)
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
			if (managedApprovalRequired || reviewRequired) && decision == toolPermissionAllow {
				break
			}
			warning := defaultApprovalWarning(toolName, risk, input)
			scope := ""
			if managedApprovalRequired && decision == toolPermissionAsk {
				warning = managedAutomationMCPApprovalWarning()
				scope = "once"
			}
			if reviewRequired && decision == toolPermissionAsk && strings.TrimSpace(reviewWarning) != "" {
				warning = reviewWarning
			}
			slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", decision, "source", decisionSourceRule, "ruleId", rule.ID, "rulePriority", rule.Priority, "ruleEnabled", rule.Enabled)
			return toolPermissionResolution{Decision: decision, Reason: reason, Warning: warning, Source: decisionSourceRule, RuleID: rule.ID, Scope: scope}
		}
	}
	if managedApprovalRequired {
		reason := "managed browser automation action requires one-time human approval"
		warning := managedAutomationMCPApprovalWarning()
		slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", toolPermissionAsk, "source", decisionSourceDefaultPolicy, "scope", "once")
		return toolPermissionResolution{Decision: toolPermissionAsk, Reason: reason, Warning: warning, Source: decisionSourceDefaultPolicy, Scope: "once"}
	}
	// A session grant is an explicit human approval of this exact command, so it
	// still satisfies the review requirement.
	if allowSession && r.hasSessionGrant(ctx, agentID, sessionGrantKey(toolName, input)) {
		slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", toolPermissionAllow, "source", decisionSourceSessionApproval)
		return toolPermissionResolution{Decision: toolPermissionAllow, Reason: "allowed by session approval", Source: decisionSourceSessionApproval, Scope: "session"}
	}
	if reviewRequired {
		slog.Info("tool permission decision", "agentId", agentID, "mode", mode, "toolName", toolName, "risk", risk, "decision", toolPermissionAsk, "source", decisionSourceCommandReview)
		return toolPermissionResolution{Decision: toolPermissionAsk, Reason: "command requires human review before execution", Warning: reviewWarning, Source: decisionSourceCommandReview, Scope: "once"}
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
		// bypassPermissions is an explicit user choice to allow every exec call in
		// this conversation, so it is honored before the review gate below.
		if mode == "bypassPermissions" {
			return toolPermissionResolution{Decision: toolPermissionAllow, Reason: "allowed by bypassPermissions mode"}
		}
		// Defense in depth for every other mode: a future direct caller must not
		// be able to reach the unconditional allows below with a command that
		// static analysis flagged or could not classify.
		if required, warning := execRequiresHumanReview(toolName, risk, input); required {
			return toolPermissionResolution{Decision: toolPermissionAsk, Reason: "command requires human review before execution", Warning: warning}
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

func managedAutomationMCPApprovalRequired(toolName string, risk tools.Risk, input json.RawMessage) bool {
	return toolName == "MCPCallTool" && risk == tools.RiskExec && tools.ManagedAutomationMCPCallRequiresApproval(input)
}

func managedAutomationMCPApprovalWarning() string {
	return "This managed browser automation call may click, type, navigate, upload, execute scripts, write screenshot files, or cause other side effects and requires one-time human approval."
}

func sessionGrantKey(toolName string, input json.RawMessage) string {
	if managedAutomationMCPApprovalRequired(toolName, tools.RiskExec, input) {
		// The approval UI may still present its generic session choice, but no
		// reusable grant is ever created for managed automation side effects.
		return ""
	}
	if toolName == "Bash" {
		return toolName + ":" + sessionGrantCommandKey(tools.BashCommand(input))
	}
	return toolName + ":" + strings.TrimSpace(string(input))
}

// scriptFileExtensions are the interpreted-file suffixes whose trailing
// arguments are parameters to an already-approved script rather than a
// different action.
var scriptFileExtensions = []string{".ps1", ".psm1", ".sh", ".bash", ".bat", ".cmd", ".py", ".rb", ".pl", ".mjs", ".cjs", ".js", ".ts"}

// sessionGrantCommandKey builds the identity a session grant is recorded
// against. For a script-file invocation it stops at the script path, so
// re-running the same script with different parameters reuses the approval the
// human already gave instead of prompting on every call.
//
// The boundary stays narrow on purpose: the interpreter, the script path, and
// any compound prefix all remain part of the key, so a different script, a
// different interpreter, or extra chained commands still require a fresh
// approval. Anything that is not a recognizable script invocation keeps the
// full command as its key.
func sessionGrantCommandKey(command string) string {
	normalized := normalizeShellCommand(command)
	if normalized == "" {
		return ""
	}
	fields := strings.Fields(normalized)
	for index, field := range fields {
		if !isScriptFileToken(field) {
			continue
		}
		return strings.Join(fields[:index+1], " ")
	}
	return normalized
}

func isScriptFileToken(token string) bool {
	trimmed := strings.ToLower(strings.Trim(token, `"'`))
	if trimmed == "" {
		return false
	}
	for _, ext := range scriptFileExtensions {
		if strings.HasSuffix(trimmed, ext) {
			return true
		}
	}
	return false
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

// execRequiresHumanReview reports whether an exec-risk call must always reach a
// human, regardless of permission mode or stored rules. It covers the two cases
// where silent execution would be unjustifiable: a command classified as
// serious-but-recoverable, and a command static analysis could not classify at
// all. Returning the structured warning keeps the approval prompt explanatory.
func execRequiresHumanReview(toolName string, risk tools.Risk, input json.RawMessage) (bool, string) {
	if risk != tools.RiskExec || toolName != "Bash" {
		return false, ""
	}
	command := tools.BashCommand(input)
	if strings.TrimSpace(command) == "" {
		return false, ""
	}
	facts := tools.AnalyzeBashCommand(command)
	if !facts.NeedsApproval() {
		return false, ""
	}
	warning := tools.CommandApprovalWarning(facts)
	if strings.TrimSpace(warning) == "" {
		warning = defaultApprovalWarning(toolName, risk, input)
	}
	return true, warning
}

func isWhitelistedExecCommand(command string) bool {
	command = strings.TrimSpace(command)
	facts := tools.AnalyzeBashCommand(command)
	if command == "" || facts.NeedsApproval() || facts.Compound || facts.Pipeline || facts.Redirection || facts.Substitution || facts.Background || facts.CommandCount != 1 {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	// Any single program invoked with only --version or --help is a read-only
	// query that cannot mutate anything. Paying a model call per `shellcheck
	// --version` or `node --version` adds latency with no safety benefit.
	if len(fields) == 2 && oneOf(fields[1], "--version", "--help", "-V", "-h", "version") {
		return true
	}
	switch fields[0] {
	case "go":
		return len(fields) >= 2 && oneOf(fields[1], "test", "vet", "build", "version") &&
			operandsAreSafe(fields[2:], goSafeFlags)
	case "npm":
		return len(fields) == 2 && fields[1] == "test" || len(fields) == 3 && fields[1] == "run" && oneOf(fields[2], "test", "build", "lint", "check")
	case "pnpm", "yarn", "bun":
		return len(fields) == 2 && oneOf(fields[1], "test", "build", "lint", "check")
	case "git":
		return len(fields) >= 2 && oneOf(fields[1], "status", "diff", "log", "show") &&
			operandsAreSafe(fields[2:], gitSafeFlags)
	default:
		return false
	}
}

// The subcommand alone does not make an invocation read-only. `go` and `git`
// both accept flags that hand execution to another program or write a file,
// which turns an allowlisted verb into an arbitrary-code or arbitrary-write
// primitive:
//
//	go test -exec <cmd>      runs <cmd> instead of the test binary
//	go build -toolexec <cmd> runs <cmd> for every compiler invocation
//	go vet -vettool <bin>    runs <bin> as the analysis tool
//	git diff --output=<path> writes to <path>, anywhere on disk
//	git diff --ext-diff      runs the repository's configured external differ
//
// Matching on the verb and ignoring the rest let all of those skip both danger
// reflection and, through canAutoExecuteTool, the approval prompt itself.
//
// These are allowlists rather than denylists on purpose. A denylist has to
// predict the next flag Go or Git adds; an allowlist that fails closed does not.
// Being wrong here costs one model call, not a broken command, because a
// rejected invocation is still reviewed and can still run.
var goSafeFlags = map[string]struct{}{
	"-v": {}, "-race": {}, "-short": {}, "-count": {}, "-run": {}, "-bench": {},
	"-timeout": {}, "-tags": {}, "-cover": {}, "-covermode": {}, "-coverprofile": {},
	"-json": {}, "-failfast": {}, "-shuffle": {}, "-parallel": {}, "-vet": {},
	"-o": {}, "-mod": {}, "-C": {},
}

var gitSafeFlags = map[string]struct{}{
	"--short": {}, "--stat": {}, "--numstat": {}, "--shortstat": {}, "--oneline": {},
	"--name-only": {}, "--name-status": {}, "--cached": {}, "--staged": {}, "--porcelain": {},
	"--graph": {}, "--decorate": {}, "--no-color": {}, "--color": {}, "--branch": {},
	"-p": {}, "-s": {}, "-n": {}, "-1": {}, "-2": {}, "-3": {}, "--": {},
}

// operandsAreSafe reports whether every argument after the subcommand is either
// a plain operand (a path, a package pattern, a flag value) or a flag on the
// allowlist. `--flag=value` is split first, so `--stat` passes while
// `--output=/etc/passwd` does not.
func operandsAreSafe(operands []string, safeFlags map[string]struct{}) bool {
	for _, operand := range operands {
		if !strings.HasPrefix(operand, "-") {
			continue
		}
		name := operand
		if index := strings.Index(name, "="); index >= 0 {
			name = name[:index]
		}
		if _, ok := safeFlags[name]; !ok {
			return false
		}
	}
	return true
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
