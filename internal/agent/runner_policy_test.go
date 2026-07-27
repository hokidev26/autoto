package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func TestRunnerToolPermissionRuleDeniesBashExec(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "deny", Priority: 50, Enabled: true, Description: "deny bash exec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-rule-deny", Name: "Bash", Input: json.RawMessage(`{"command":"printf denied"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "handled"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})

	runner.Run(ctx, agent.ID)

	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-rule-deny")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "denied" || !strings.Contains(call.PermissionDecisionReason, "deny bash exec") {
		t.Fatalf("expected bash rule denial, got %+v", call)
	}
	if !requestHasToolResult(provider.request(1), "bash-rule-deny", true) {
		t.Fatalf("expected denied bash result to be fed back")
	}
}

func TestRunnerToolPermissionRuleAllowsBashExec(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "allow", Priority: 50, Enabled: true, Description: "allow bash exec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-rule-allow", Name: "Bash", Input: json.RawMessage(`{"command":"echo allowed-by-rule"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})

	runner.Run(ctx, agent.ID)

	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-rule-allow")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "completed" || !strings.Contains(call.PermissionDecisionReason, "allow bash exec") || !strings.Contains(string(call.OutputJSON), "allowed-by-rule") {
		t.Fatalf("expected bash rule allow, got %+v output=%s", call, string(call.OutputJSON))
	}
}

func TestRunnerToolPermissionRulesUsePriorityAndSkipDisabledRules(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	low, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "deny", Priority: 10, Enabled: true, Description: "low deny"})
	if err != nil {
		t.Fatal(err)
	}
	high, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "allow", Priority: 20, Enabled: true, Description: "high allow"})
	if err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	input := json.RawMessage(`{"command":"printf policy"}`)
	resolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, input)
	if resolution.Decision != toolPermissionAllow || !strings.Contains(resolution.Reason, "id="+high.ID) || !strings.Contains(resolution.Reason, "priority=20") {
		t.Fatalf("expected higher-priority allow with diagnostic record, got %+v", resolution)
	}
	high.Enabled = false
	if _, err := store.UpdateToolPermissionRule(ctx, high); err != nil {
		t.Fatal(err)
	}
	resolution = runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, input)
	if resolution.Decision != toolPermissionDeny || !strings.Contains(resolution.Reason, "id="+low.ID) {
		t.Fatalf("expected disabled high rule to be skipped in favor of low deny, got %+v", resolution)
	}
}

func TestRunnerToolPermissionRuleTieBreakUsesSpecificityThenDeny(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	wildcardDeny, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "*", ToolName: "*", Risk: "exec", Decision: "deny", Priority: 40, Enabled: true, Description: "broad deny"})
	if err != nil {
		t.Fatal(err)
	}
	exactAllow, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "allow", Priority: 40, Enabled: true, Description: "exact allow"})
	if err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	input := json.RawMessage(`{"command":"printf policy"}`)
	resolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, input)
	if resolution.Decision != toolPermissionAllow || !strings.Contains(resolution.Reason, "id="+exactAllow.ID) || strings.Contains(resolution.Reason, "id="+wildcardDeny.ID) {
		t.Fatalf("expected exact rule to beat wildcard at equal priority, got %+v", resolution)
	}
	exactDeny, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "deny", Priority: 40, Enabled: true, Description: "exact deny"})
	if err != nil {
		t.Fatal(err)
	}
	resolution = runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, input)
	if resolution.Decision != toolPermissionDeny || !strings.Contains(resolution.Reason, "id="+exactDeny.ID) {
		t.Fatalf("expected deny to win equal-priority equal-specificity tie, got %+v", resolution)
	}
}

func TestRunnerReadOnlyHardCapOverridesRulesAndSessionGrants(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "readOnly")
	defer store.Close()
	if _, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "readOnly", ToolName: "Write", Risk: "write", Decision: "allow", Priority: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	writeResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Write", tools.RiskWrite, json.RawMessage(`{"file_path":"blocked.txt","content":"no"}`))
	if writeResolution.Decision != toolPermissionDeny || !strings.Contains(writeResolution.Reason, "readOnly") {
		t.Fatalf("expected readOnly cap to deny allow rule, got %+v", writeResolution)
	}
	commandInput := json.RawMessage(`{"command":"printf blocked"}`)
	runner.approvalMu.Lock()
	runner.addSessionGrantLocked(agent.ID, sessionGrantKey("Bash", commandInput))
	runner.approvalMu.Unlock()
	execResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, commandInput)
	if execResolution.Decision != toolPermissionDeny || !strings.Contains(execResolution.Reason, "readOnly") {
		t.Fatalf("expected readOnly cap to deny session grant, got %+v", execResolution)
	}
}

func TestRunnerBypassPermissionsStillAllowsNonDangerExec(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	resolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, json.RawMessage(`{"command":"printf bypass"}`))
	if resolution.Decision != toolPermissionAllow || resolution.Reason != "allowed by bypassPermissions mode" {
		t.Fatalf("expected bypassPermissions exec compatibility, got %+v", resolution)
	}
}

func TestRunnerDisabledExecConfirmationRespectsModeCapability(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{RequireConfirmationForExec: false, RequireConfirmationForWrites: false, AllowReadOnlyByDefault: true}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	input := json.RawMessage(`{"command":"printf allowed"}`)
	allowedResolution := runner.resolveToolPermission(ctx, agent.ID, "acceptEdits", "Bash", tools.RiskExec, input)
	if allowedResolution.Decision != toolPermissionAllow {
		t.Fatalf("expected exec-capable mode to allow when confirmation is disabled, got %+v", allowedResolution)
	}
	invalidResolution := runner.resolveToolPermission(ctx, agent.ID, "invalid", "Bash", tools.RiskExec, input)
	if invalidResolution.Decision != toolPermissionDeny {
		t.Fatalf("expected invalid mode to remain denied, got %+v", invalidResolution)
	}
}

func TestRunnerWorkflowPreferenceRequiresWriteApproval(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{RequireConfirmationForExec: true, RequireConfirmationForWrites: true, AllowReadOnlyByDefault: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "write file"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "write-ask", Name: "Write", Input: json.RawMessage(`{"file_path":"out.txt","content":"hello"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	done := make(chan struct{})
	go func() { runner.Run(ctx, agent.ID); close(done) }()
	waitForPendingApproval(t, runner, agent.ID, "write-ask")
	accepted, err := runner.ApproveToolCall(ctx, agent.ID, "write-ask", ToolApprovalDecision{Decision: "allow_once", Reason: "write ok", DecidedBy: "test"})
	if err != nil || !accepted {
		t.Fatalf("write approval failed accepted=%v err=%v", accepted, err)
	}
	waitDone(t, done)
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "write-ask")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "completed" || call.PermissionDecidedBy != "test" {
		t.Fatalf("expected approved write call, got %+v", call)
	}
}

func TestRunnerWorkflowPreferenceRequiresReadApprovalAndDirectDenies(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{RequireConfirmationForExec: true, RequireConfirmationForWrites: false, AllowReadOnlyByDefault: false}); err != nil {
		t.Fatal(err)
	}
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{MaxTurns: 3})
	direct, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: "read-direct", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !direct.IsError || !strings.Contains(direct.Output, "requires approval") {
		t.Fatalf("expected direct read to be denied as approval-required, got %+v", direct)
	}

	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "read file"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "read-ask", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	runner = newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	done := make(chan struct{})
	go func() { runner.Run(ctx, agent.ID); close(done) }()
	waitForPendingApproval(t, runner, agent.ID, "read-ask")
	accepted, err := runner.ApproveToolCall(ctx, agent.ID, "read-ask", ToolApprovalDecision{Decision: "allow_once", Reason: "read ok", DecidedBy: "test"})
	if err != nil || !accepted {
		t.Fatalf("read approval failed accepted=%v err=%v", accepted, err)
	}
	waitDone(t, done)
}

func TestRunnerDangerToolIgnoresAllowRule(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	now := db.Now()
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO tool_permission_rules (id, mode, tool_name, risk, decision, priority, enabled, description, created_at, updated_at) VALUES (?, '*', 'Bash', 'danger', 'allow', 100, 1, 'legacy unsafe rule', ?, ?)`, db.NewID(), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "danger"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-danger-rule", Name: "Bash", Input: json.RawMessage(`{"command":"rm -rf /tmp/autoto-danger-test"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "blocked"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})

	runner.Run(ctx, agent.ID)

	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-danger-rule")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "denied" || call.PermissionDecidedBy != "policy" || strings.TrimSpace(call.ErrorMessage) == "" {
		t.Fatalf("expected danger command to stay denied, got %+v", call)
	}
}

func TestRunnerDangerBashIsBlockedWithoutApproval(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "delete"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-danger", Name: "Bash", Input: json.RawMessage(`{"command":"rm -rf tmp"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "blocked"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	runner.Run(ctx, agent.ID)
	if runnerPendingApprovalCount(runner) != 0 {
		t.Fatal("danger command should not create approvable pending state")
	}
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-danger")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "denied" || call.PermissionDecidedBy != "policy" {
		t.Fatalf("expected policy-denied danger command, got %+v", call)
	}
	if !requestHasToolResult(provider.request(1), "bash-danger", true) {
		t.Fatalf("expected danger block to be fed back as error")
	}
}

func TestWhitelistedExecMatcher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("BashTool executes cmd.exe on Windows, so POSIX command facts remain unknown")
	}
	for _, command := range []string{
		"go test ./...", "go vet ./internal/...", "go build ./...", "go version",
		"npm test", "npm run lint",
		"git status --short", "git diff --stat",
		// Any single program querying its own version or help is read-only.
		"shellcheck --version", "node --version", "python --version",
		"rustc --version", "cargo --version", "docker --version",
		"shellcheck --help", "node --help",
	} {
		if !isWhitelistedExecCommand(command) {
			t.Fatalf("expected command to be whitelisted: %s", command)
		}
	}
	for _, command := range []string{"go test ./... && rm -rf tmp", "npm run deploy", "git clean -fdx", "echo ok > file"} {
		if isWhitelistedExecCommand(command) {
			t.Fatalf("expected command not to be whitelisted: %s", command)
		}
	}
}

func TestToolPermissionResolutionSourcesAndRuleID(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	input := json.RawMessage(`{"command":"printf source"}`)
	defaultResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, input)
	if defaultResolution.Source != decisionSourceDefaultPolicy || defaultResolution.Decision != toolPermissionAsk {
		t.Fatalf("unexpected default resolution: %+v", defaultResolution)
	}
	rule, err := store.CreateToolPermissionRule(ctx, db.ToolPermissionRule{Mode: "acceptEdits", ToolName: "Bash", Risk: "exec", Decision: "allow", Priority: 50, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ruleResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskExec, input)
	if ruleResolution.Source != decisionSourceRule || ruleResolution.RuleID != rule.ID || ruleResolution.Decision != toolPermissionAllow {
		t.Fatalf("unexpected rule resolution: %+v", ruleResolution)
	}
	readOnlyResolution := runner.resolveToolPermission(ctx, agent.ID, "readOnly", "Write", tools.RiskWrite, json.RawMessage(`{"file_path":"x","content":"x"}`))
	if readOnlyResolution.Source != decisionSourceReadOnlyCap || readOnlyResolution.Decision != toolPermissionDeny {
		t.Fatalf("unexpected read-only resolution: %+v", readOnlyResolution)
	}
	dangerResolution := runner.resolveToolPermission(ctx, agent.ID, agent.PermissionMode, "Bash", tools.RiskDanger, json.RawMessage(`{"command":"rm -rf tmp"}`))
	if dangerResolution.Source != decisionSourceHardDangerBlock || dangerResolution.Decision != toolPermissionDeny {
		t.Fatalf("unexpected danger resolution: %+v", dangerResolution)
	}
}

func TestWhitelistedExecMatcherRequiresSimpleKnownCommandFacts(t *testing.T) {
	for _, command := range []string{"go test ./... | cat", "git status > out.txt", "git status &", "git status $(printf x)", "printf 'unterminated"} {
		if isWhitelistedExecCommand(command) {
			t.Fatalf("complex or unknown command must not be whitelisted: %q", command)
		}
	}
}

// TestWhitelistedVersionAndHelpQueriesAreSafe covers the cross-platform rule:
// any single program invoked with only --version, --help, -V, or -h is a
// read-only query and must skip the danger-reflection model call.
func TestWhitelistedVersionAndHelpQueriesAreSafe(t *testing.T) {
	safe := []string{
		"shellcheck --version",
		"node --version",
		"python --version",
		"rustc --version",
		"cargo --version",
		"docker --version",
		"shellcheck --help",
		"node -h",
		"go version",
	}
	for _, cmd := range safe {
		if !isWhitelistedExecCommand(cmd) {
			t.Errorf("expected --version/--help query to be whitelisted: %q", cmd)
		}
	}
	// A compound command must never be whitelisted even if it ends with --version.
	notSafe := []string{
		"node --version && rm -rf .",
		"shellcheck --version | cat",
	}
	for _, cmd := range notSafe {
		if isWhitelistedExecCommand(cmd) {
			t.Errorf("compound command must not be whitelisted: %q", cmd)
		}
	}
}

func TestPermissionModeWithCapNeverWidens(t *testing.T) {
	cases := []struct {
		mode string
		cap  string
		want string
	}{
		{mode: "bypassPermissions", cap: "acceptEdits", want: "acceptEdits"},
		{mode: "readOnly", cap: "acceptEdits", want: "readOnly"},
		{mode: "default", cap: "acceptEdits", want: "default"},
		{mode: "bypassPermissions", cap: "readOnly", want: "readOnly"},
	}
	for _, test := range cases {
		if got := permissionModeWithCap(test.mode, test.cap); got != test.want {
			t.Fatalf("permissionModeWithCap(%q, %q)=%q, want %q", test.mode, test.cap, got, test.want)
		}
	}
}

type normalizationSpyTool struct {
	mu            sync.Mutex
	riskInputs    []json.RawMessage
	executeInputs []json.RawMessage
}

func (*normalizationSpyTool) Name() string        { return "NormalizationSpy" }
func (*normalizationSpyTool) Description() string { return "Records normalized runner inputs." }
func (*normalizationSpyTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "integer", "default": "2.0"},
			"ratio": map[string]any{"type": "number", "minimum": 1, "maximum": 5},
		},
		"additionalProperties": true,
	}
}
func (tool *normalizationSpyTool) Risk(input json.RawMessage) tools.Risk {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.riskInputs = append(tool.riskInputs, append(json.RawMessage(nil), input...))
	return tools.RiskWrite
}
func (tool *normalizationSpyTool) Execute(_ context.Context, call tools.Call, _ tools.Env) (tools.Result, error) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.executeInputs = append(tool.executeInputs, append(json.RawMessage(nil), call.Input...))
	return tools.Result{Output: "normalized"}, nil
}
func (tool *normalizationSpyTool) inputs() ([]json.RawMessage, []json.RawMessage) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return append([]json.RawMessage(nil), tool.riskInputs...), append([]json.RawMessage(nil), tool.executeInputs...)
}

func TestRunnerUsesOneNormalizedInputForRiskApprovalAuditAndExecute(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{RequireConfirmationForExec: true, RequireConfirmationForWrites: true, AllowReadOnlyByDefault: true}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	spy := &normalizationSpyTool{}
	enableAgentTestTool(t, store, spy.Name())
	registry := tools.NewRegistry()
	registry.Register(spy)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})
	listed := runner.ListTools()
	if len(listed) != 1 || listed[0].Name != spy.Name() {
		t.Fatalf("unexpected tool metadata: %+v", listed)
	}
	call := tools.Call{ID: "normalize-shared", Name: spy.Name(), Input: json.RawMessage(`{"count":"3.0","ratio":"9","unknown":"11"}`)}
	want := `{"count":3,"ratio":5,"unknown":"11"}`

	resultCh := make(chan tools.Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, executeErr := runner.executeToolForLoop(ctx, agent.ID, run.ID, call, "")
		resultCh <- result
		errCh <- executeErr
	}()
	waitForPendingApproval(t, runner, agent.ID, call.ID)

	runner.approvalMu.Lock()
	approval := runner.approvals[approvalKey(agent.ID, call.ID)]
	approvalInput := append(json.RawMessage(nil), approval.Input...)
	approvalRisk := approval.Risk
	runner.approvalMu.Unlock()
	if string(approvalInput) != want || approvalRisk != tools.RiskWrite {
		t.Fatalf("approval did not receive normalized input: input=%s risk=%s", approvalInput, approvalRisk)
	}
	if accepted, approveErr := runner.ApproveToolCall(ctx, agent.ID, call.ID, ToolApprovalDecision{Decision: "allow_once", Reason: "normalized input checked", DecidedBy: "test"}); approveErr != nil || !accepted {
		t.Fatalf("approve normalized call: accepted=%v err=%v", accepted, approveErr)
	}
	if executeErr := <-errCh; executeErr != nil {
		t.Fatal(executeErr)
	}
	if result := <-resultCh; result.IsError || result.Output != "normalized" {
		t.Fatalf("unexpected execute result: %+v", result)
	}

	riskInputs, executeInputs := spy.inputs()
	if len(riskInputs) != 1 || string(riskInputs[0]) != want {
		t.Fatalf("risk input mismatch: %q", riskInputs)
	}
	if len(executeInputs) != 1 || string(executeInputs[0]) != want {
		t.Fatalf("execute input mismatch: %q", executeInputs)
	}
	stored, err := store.GetToolCallByUseID(ctx, agent.ID, call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "completed" || string(stored.InputJSON) != want {
		t.Fatalf("audit input mismatch: status=%s input=%s", stored.Status, stored.InputJSON)
	}
}

func TestRunnerRejectsForbiddenHostFieldsBeforeRiskApprovalOrExecute(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	spy := &normalizationSpyTool{}
	enableAgentTestTool(t, store, spy.Name())
	registry := tools.NewRegistry()
	registry.Register(spy)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})
	input := json.RawMessage(`{"cwd":"C:/escape","count":"3.0"}`)
	result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: "normalize-host-field", Name: spy.Name(), Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, "host field") {
		t.Fatalf("expected forbidden host field result, got %+v", result)
	}
	if runnerPendingApprovalCount(runner) != 0 {
		t.Fatal("forbidden host field must not create approval state")
	}
	riskInputs, executeInputs := spy.inputs()
	if len(riskInputs) != 0 || len(executeInputs) != 0 {
		t.Fatalf("forbidden host field reached risk or execute: risk=%q execute=%q", riskInputs, executeInputs)
	}
	stored, err := store.GetToolCallByUseID(ctx, agent.ID, "normalize-host-field")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "error" || string(stored.InputJSON) != string(input) {
		t.Fatalf("forbidden input was not audited intact: status=%s input=%q", stored.Status, stored.InputJSON)
	}
}

func TestRunnerLoopRejectsForbiddenHostFieldsBeforeRiskApprovalOrExecute(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	spy := &normalizationSpyTool{}
	enableAgentTestTool(t, store, spy.Name())
	registry := tools.NewRegistry()
	registry.Register(spy)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})
	listed, err := runner.ListToolsForAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != spy.Name() {
		t.Fatalf("unexpected agent tool metadata: %+v", listed)
	}
	input := json.RawMessage(`{"agent_id":"attacker","count":"3.0"}`)
	result, err := runner.executeToolForLoop(ctx, agent.ID, run.ID, tools.Call{ID: "normalize-loop-host-field", Name: spy.Name(), Input: input}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, "host field") {
		t.Fatalf("expected forbidden host field result, got %+v", result)
	}
	if runnerPendingApprovalCount(runner) != 0 {
		t.Fatal("forbidden loop input must not create approval state")
	}
	riskInputs, executeInputs := spy.inputs()
	if len(riskInputs) != 0 || len(executeInputs) != 0 {
		t.Fatalf("forbidden loop input reached risk or execute: risk=%q execute=%q", riskInputs, executeInputs)
	}
	stored, err := store.GetToolCallByUseID(ctx, agent.ID, "normalize-loop-host-field")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "error" || string(stored.InputJSON) != string(input) {
		t.Fatalf("forbidden loop input was not audited intact: status=%s input=%q", stored.Status, stored.InputJSON)
	}
}

func TestRunnerRejectsInvalidJSONWithoutRiskOrEmptyObjectFallback(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	spy := &normalizationSpyTool{}
	enableAgentTestTool(t, store, spy.Name())
	registry := tools.NewRegistry()
	registry.Register(spy)
	runner := NewRunner(store, providers.NewRegistry(), registry, NewHub(), config.AgentConfig{})
	invalid := json.RawMessage(`{"count":`)
	result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: "normalize-invalid", Name: spy.Name(), Input: invalid})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, "invalid tool input") {
		t.Fatalf("expected invalid input result, got %+v", result)
	}
	riskInputs, executeInputs := spy.inputs()
	if len(riskInputs) != 0 || len(executeInputs) != 0 {
		t.Fatalf("invalid JSON reached risk or execute: risk=%q execute=%q", riskInputs, executeInputs)
	}
	stored, err := store.GetToolCallByUseID(ctx, agent.ID, "normalize-invalid")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "error" || string(stored.InputJSON) != string(invalid) || string(stored.InputJSON) == `{}` {
		t.Fatalf("invalid JSON was not audited intact: status=%s input=%q", stored.Status, stored.InputJSON)
	}
}
