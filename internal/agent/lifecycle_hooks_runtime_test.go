package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/hooks"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func TestLifecycleLLMGateDenyBlocksRunBefore(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: `{"decision":"deny","reason":"policy blocked"}`},
		{Type: "done", Done: true, StopReason: "end_turn"},
	}}}
	providerRegistry := providers.NewRegistry()
	providerRegistry.Register(provider)
	runner := NewRunner(store, providerRegistry, tools.NewRegistry(), NewHub(), config.AgentConfig{})
	hook, err := store.CreateLifecycleHook(ctx, hooks.Hook{
		Name:          "deny gate",
		Enabled:       true,
		Event:         hooks.EventRunBefore,
		Scope:         hooks.Scope{Kind: hooks.ScopeGlobal},
		Mode:          hooks.ModeSync,
		FailurePolicy: hooks.FailureContinue,
		Action: hooks.Action{Kind: hooks.ActionLLM, LLM: &hooks.LLMAction{
			Model: "fake:test", Prompt: "Decide whether this run may start.", MaxOutputTokens: 64, TimeoutSeconds: 5,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runRecord, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	lifecycleRun, err := runner.ensureLifecycleRun(ctx, createdAgent.ID, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.dispatchRunLifecycle(ctx, lifecycleRun, hooks.EventRunBefore, "running", "")
	if !errors.Is(err, errLifecycleHookDenied) || !strings.Contains(err.Error(), "policy blocked") {
		t.Fatalf("LLM deny did not block run.before: %v", err)
	}
	executions, err := store.ListLifecycleHookExecutions(ctx, hook.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 || executions[0].Status != hooks.ExecutionSucceeded {
		t.Fatalf("gate execution was not persisted as a successful decision: %+v", executions)
	}
	if provider.requestCount() != 1 || len(provider.request(0).Tools) != 0 {
		t.Fatalf("LLM gate did not use an isolated no-tools request: %+v", provider.request(0))
	}
}

func TestLifecycleSyncFailurePolicyContinueAndFailRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	for _, testCase := range []struct {
		name    string
		policy  hooks.FailurePolicy
		wantErr bool
	}{
		{name: "continue", policy: hooks.FailureContinue},
		{name: "fail_run", policy: hooks.FailureFailRun, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			store, createdAgent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
			defer store.Close()
			runner := NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), NewHub(), config.AgentConfig{})
			if _, err := store.CreateLifecycleHook(ctx, hooks.Hook{
				Name:          "controlled HTTP failure",
				Enabled:       true,
				Event:         hooks.EventRunBefore,
				Scope:         hooks.Scope{Kind: hooks.ScopeGlobal},
				Mode:          hooks.ModeSync,
				FailurePolicy: testCase.policy,
				Action: hooks.Action{Kind: hooks.ActionHTTP, HTTP: &hooks.HTTPAction{
					URL: server.URL + "/hook", Method: http.MethodPost, TimeoutSeconds: 5,
				}},
			}); err != nil {
				t.Fatal(err)
			}
			runRecord, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
			if err != nil {
				t.Fatal(err)
			}
			lifecycleRun, err := runner.ensureLifecycleRun(ctx, createdAgent.ID, runRecord.ID)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.dispatchRunLifecycle(ctx, lifecycleRun, hooks.EventRunBefore, "running", "")
			if (err != nil) != testCase.wantErr {
				t.Fatalf("failure policy %s returned err=%v", testCase.policy, err)
			}
		})
	}
}

func TestLifecycleAsyncShellDoesNotBlockAndUsesToolApprovalAudit(t *testing.T) {
	ctx := context.Background()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), NewHub(), config.AgentConfig{})
	secretRefName := "AUTOTO_LIFECYCLE_SHELL_TOKEN"
	t.Setenv(secretRefName, "")
	executable := "cat"
	var args []string
	if runtime.GOOS == "windows" {
		executable = "more.com"
	}
	hook, err := store.CreateLifecycleHook(ctx, hooks.Hook{
		Name:          "async shell",
		Enabled:       true,
		Event:         hooks.EventToolAfter,
		Scope:         hooks.Scope{Kind: hooks.ScopeGlobal},
		Mode:          hooks.ModeAsync,
		FailurePolicy: hooks.FailureContinue,
		Action: hooks.Action{Kind: hooks.ActionShell, Shell: &hooks.ShellAction{
			Executable: executable, Args: args, SecretRefs: map[string]string{"AUDIT_TOKEN": "env:" + secretRefName}, TimeoutSeconds: 5, CanonicalStdinV1: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runRecord, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = runner.dispatchToolLifecycle(ctx, createdAgent.ID, runRecord.ID, hooks.EventToolAfter, tools.Call{ID: "original", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}, &tools.Result{Output: "done"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("asynchronous hook blocked tool completion for %s", elapsed)
	}

	toolUseID := waitForLifecycleApproval(t, runner, createdAgent.ID, lifecycleHookShellToolName)
	t.Setenv(secretRefName, "late-shell-secret")
	approved, err := runner.ApproveToolCall(ctx, createdAgent.ID, toolUseID, ToolApprovalDecision{Decision: "allow_once", Reason: "approved hook shell", DecidedBy: "test"})
	if err != nil || !approved {
		t.Fatalf("approve hook shell: approved=%v err=%v", approved, err)
	}
	waitForLifecycleExecutionStatus(t, store, hook.ID, hooks.ExecutionSucceeded)
	calls, err := store.ListToolCallsByRun(ctx, createdAgent.ID, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range calls {
		if call.ToolName != lifecycleHookShellToolName {
			continue
		}
		found = true
		if call.Status != "completed" {
			t.Fatalf("hook shell audit status = %q, want completed", call.Status)
		}
		persisted := string(call.InputJSON) + string(call.OutputJSON)
		if strings.Contains(persisted, secretRefName) || strings.Contains(persisted, "late-shell-secret") || strings.Contains(persisted, "env:") {
			t.Fatalf("hook shell audit leaked a secret reference or value: %s", persisted)
		}
		if !strings.Contains(string(call.OutputJSON), "output suppressed") {
			t.Fatalf("hook shell secret-bearing output was not suppressed: %s", call.OutputJSON)
		}
	}
	if !found {
		t.Fatal("hook shell action did not pass through the audited lifecycle tool gateway")
	}
}

func TestLifecycleHTTPWaitsForApprovalUsesNetworkPolicyAndRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	received := make(chan struct {
		authorization string
		body          string
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		authorization := request.Header.Get("Authorization")
		received <- struct {
			authorization string
			body          string
		}{authorization: authorization, body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization":"` + authorization + `"}`))
	}))
	defer server.Close()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), NewHub(), config.AgentConfig{})
	secretRefName := "AUTOTO_LIFECYCLE_HTTP_TOKEN"
	t.Setenv(secretRefName, "")
	hook, err := store.CreateLifecycleHook(ctx, hooks.Hook{
		Name:          "approved webhook",
		Enabled:       true,
		Event:         hooks.EventToolAfter,
		Scope:         hooks.Scope{Kind: hooks.ScopeGlobal},
		Mode:          hooks.ModeAsync,
		FailurePolicy: hooks.FailureContinue,
		Action: hooks.Action{Kind: hooks.ActionHTTP, HTTP: &hooks.HTTPAction{
			URL: server.URL + "/event", Method: http.MethodPost, SecretRefs: map[string]string{"Authorization": "env:" + secretRefName}, TimeoutSeconds: 5,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runRecord, err := store.CreateRun(ctx, db.Run{AgentID: createdAgent.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.dispatchToolLifecycle(ctx, createdAgent.ID, runRecord.ID, hooks.EventToolAfter, tools.Call{ID: "original-http", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}, &tools.Result{Output: "done"}, nil); err != nil {
		t.Fatal(err)
	}
	toolUseID := waitForLifecycleApproval(t, runner, createdAgent.ID, lifecycleHookHTTPToolName)
	select {
	case request := <-received:
		t.Fatalf("HTTP hook ran before approval: %+v", request)
	default:
	}
	t.Setenv(secretRefName, "Bearer late-http-secret")
	sessionApproved, sessionErr := runner.ApproveToolCall(ctx, createdAgent.ID, toolUseID, ToolApprovalDecision{Decision: "allow_session", Reason: "session approval must be rejected", DecidedBy: "test"})
	if sessionErr == nil || sessionApproved || !strings.Contains(sessionErr.Error(), "allow_once") {
		t.Fatalf("lifecycle HTTP accepted session approval: approved=%v err=%v", sessionApproved, sessionErr)
	}
	select {
	case request := <-received:
		t.Fatalf("HTTP hook ran after rejected session approval: %+v", request)
	default:
	}
	approved, err := runner.ApproveToolCall(ctx, createdAgent.ID, toolUseID, ToolApprovalDecision{Decision: "allow_once", Reason: "approved webhook", DecidedBy: "test"})
	if err != nil || !approved {
		t.Fatalf("approve hook HTTP: approved=%v err=%v", approved, err)
	}
	var request struct {
		authorization string
		body          string
	}
	select {
	case request = <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for approved HTTP hook")
	}
	if request.authorization != "Bearer late-http-secret" || !strings.Contains(request.body, `"schemaVersion":1`) {
		t.Fatalf("unexpected approved HTTP request: %+v", request)
	}
	waitForLifecycleExecutionStatus(t, store, hook.ID, hooks.ExecutionSucceeded)
	calls, err := store.ListToolCallsByRun(ctx, createdAgent.ID, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range calls {
		if call.ToolName != lifecycleHookHTTPToolName {
			continue
		}
		found = true
		persisted := string(call.InputJSON) + string(call.OutputJSON) + call.ErrorMessage
		for _, forbidden := range []string{secretRefName, "late-http-secret", "env:"} {
			if strings.Contains(persisted, forbidden) {
				t.Fatalf("hook HTTP audit leaked %q: %s", forbidden, persisted)
			}
		}
		if !strings.Contains(string(call.OutputJSON), `bodySuppressed\":true`) {
			t.Fatalf("hook HTTP secret-bearing response was not suppressed: %s", call.OutputJSON)
		}
		if call.Status != "completed" {
			t.Fatalf("hook HTTP audit status = %q, want completed", call.Status)
		}
	}
	if !found {
		t.Fatal("hook HTTP action did not pass through the audited lifecycle tool gateway")
	}
}

func TestLifecycleHookTestUsesAuditedToolWithoutOrdinaryRun(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	store, createdAgent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	runner := NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), NewHub(), config.AgentConfig{})
	event := hooks.Event{
		Name:    hooks.EventRunAfter,
		RunID:   "hook-test-" + db.NewID(),
		AgentID: createdAgent.ID,
		RunKind: hooks.RunKindHookTest,
	}
	result, err := runner.ExecuteHTTP(hooks.ContextWithEvent(ctx, event), hooks.HTTPRequest{
		URL:     server.URL + "/event",
		Method:  http.MethodPost,
		Body:    []byte(`{"schemaVersion":1}`),
		Timeout: 5 * time.Second,
	})
	if err != nil || result.StatusCode != http.StatusOK {
		t.Fatalf("execute isolated hook test: result=%+v err=%v", result, err)
	}
	var runCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ?`, event.RunID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("hook test created an ordinary run: %d", runCount)
	}
	var toolCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tool_calls WHERE agent_id = ? AND run_id IS NULL AND tool_name = ?`, createdAgent.ID, lifecycleHookHTTPToolName).Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if toolCount != 1 {
		t.Fatalf("isolated hook test audit count = %d, want 1", toolCount)
	}
}

func TestLifecycleHTTPDeniedForChildToolAllowlist(t *testing.T) {
	ctx := context.Background()
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, parent := newAgentTestStore(t, t.TempDir(), "bypassPermissions")
	defer store.Close()
	child, err := store.CreateAgent(ctx, db.Agent{WorklineID: parent.WorklineID, ParentAgentID: parent.ID, Type: "subagent", SubagentType: "review", Title: "Review", Model: "fake:test", PermissionMode: "bypassPermissions", Status: "idle", CWD: parent.CWD})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), NewHub(), config.AgentConfig{})
	if err := runner.RegisterChildRuntimeProfile(child.ID, ChildRoleResolution{BaseRole: "general", AllowedTools: []string{"Bash"}}); err != nil {
		t.Fatal(err)
	}
	event := hooks.Event{Name: hooks.EventRunAfter, RunID: "hook-test-" + db.NewID(), AgentID: child.ID, RunKind: hooks.RunKindHookTest}
	_, err = runner.ExecuteHTTP(hooks.ContextWithEvent(ctx, event), hooks.HTTPRequest{URL: server.URL + "/event", Method: http.MethodPost, Body: []byte(`{"schemaVersion":1}`), Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "child agent tool allowlist denies internal lifecycle tool") {
		t.Fatalf("child lifecycle HTTP was not denied by its immutable capability: %v", err)
	}
	select {
	case <-requested:
		t.Fatal("child lifecycle HTTP reached the network without exec capability")
	default:
	}
}

func TestLifecycleHTTPToolRejectsMetadataDestination(t *testing.T) {
	request := hooks.HTTPRequest{
		URL:     "https://169.254.169.254/latest/meta-data/",
		Method:  http.MethodPost,
		Body:    []byte(`{"schemaVersion":1}`),
		Timeout: time.Second,
	}
	input, err := json.Marshal(lifecycleHTTPApprovalInput(request))
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&lifecycleHookHTTPTool{request: request}).Execute(context.Background(), tools.Call{Input: input}, tools.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, "denied by network policy") {
		t.Fatalf("metadata destination was not denied: %+v", result)
	}
}

func waitForLifecycleApproval(t *testing.T, runner *Runner, agentID, toolName string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runner.approvalMu.Lock()
		for _, approval := range runner.approvals {
			if approval.AgentID == agentID && approval.ToolName == toolName {
				toolUseID := approval.ToolUseID
				runner.approvalMu.Unlock()
				return toolUseID
			}
		}
		runner.approvalMu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for lifecycle %s approval", toolName)
	return ""
}

func waitForLifecycleExecutionStatus(t *testing.T, store *db.Store, hookID string, status hooks.ExecutionStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		executions, err := store.ListLifecycleHookExecutions(context.Background(), hookID, 10)
		if err == nil && len(executions) == 1 && executions[0].Status == status {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	executions, _ := store.ListLifecycleHookExecutions(context.Background(), hookID, 10)
	t.Fatalf("timed out waiting for lifecycle execution status %s: %+v", status, executions)
}
