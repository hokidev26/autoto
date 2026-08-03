package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

func TestRunnerAutoExecutesToolCallsAndRecordsUsage(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "hello from tool"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "read note.txt"}); err != nil {
		t.Fatal(err)
	}

	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "usage", Usage: &providers.Usage{InputTokens: 11, OutputTokens: 3, CachedInputTokens: 2}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "tool-1", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
		{
			{Type: "usage", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 5, ReasoningTokens: 1}},
			{Type: "text", Text: "file says hello"},
			{Type: "done", Done: true, StopReason: "end_turn"},
		},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4})

	runner.Run(ctx, agent.ID)

	updated, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "idle" {
		t.Fatalf("expected idle agent, got %q", updated.Status)
	}
	if provider.requestCount() != 2 {
		t.Fatalf("expected two provider turns, got %d", provider.requestCount())
	}
	second := provider.request(1)
	if !requestHasToolResult(second, "tool-1", false) {
		t.Fatalf("expected second request to include successful tool_result, got %+v", second.Messages)
	}
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "tool-1")
	if err != nil {
		t.Fatal(err)
	}
	if call.ToolName != "Read" || call.Status != "completed" || call.MessageID == "" || call.StartedAt == "" || call.CompletedAt == "" || call.UpdatedAt == "" {
		t.Fatalf("unexpected stored tool call: %+v", call)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected user + tool_use + tool_result + final messages, got %d", len(messages))
	}
	if messages[3].Role != "assistant" || messages[3].ContentText != "file says hello" {
		t.Fatalf("unexpected final message: %+v", messages[3])
	}
	var apiCount, linkedMessageCount, inputTokens, outputTokens int64
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*), COUNT(message_id), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0) FROM api_requests WHERE agent_id = ?`, agent.ID).Scan(&apiCount, &linkedMessageCount, &inputTokens, &outputTokens); err != nil {
		t.Fatal(err)
	}
	if apiCount != 2 || linkedMessageCount != 2 || inputTokens != 18 || outputTokens != 8 {
		t.Fatalf("unexpected api request stats: count=%d linked=%d input=%d output=%d", apiCount, linkedMessageCount, inputTokens, outputTokens)
	}
}

func TestRunnerWaitsForBashApprovalAndAllowsOnce(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-1", Name: "Bash", Input: json.RawMessage(`{"command":"echo approved"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	done := make(chan struct{})
	go func() { runner.Run(ctx, agent.ID); close(done) }()
	waitForPendingApproval(t, runner, agent.ID, "bash-1")
	accepted, err := runner.ApproveToolCall(ctx, agent.ID, "bash-1", ToolApprovalDecision{Decision: "allow_once", Reason: "ok", DecidedBy: "test"})
	if err != nil || !accepted {
		t.Fatalf("approval failed accepted=%v err=%v", accepted, err)
	}
	waitDone(t, done)
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-1")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "completed" || call.PermissionDecidedBy != "test" || call.PermissionDecidedAt == "" || call.StartedAt == "" || call.CompletedAt == "" || call.UpdatedAt == "" {
		t.Fatalf("unexpected approved call: %+v", call)
	}
	if !requestHasToolResult(provider.request(1), "bash-1", false) {
		t.Fatalf("expected approved bash result to be fed back")
	}
}

func TestRunnerBashApprovalDenyFeedsErrorResult(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-deny", Name: "Bash", Input: json.RawMessage(`{"command":"printf denied"}`)}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "handled denial"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})
	done := make(chan struct{})
	go func() { runner.Run(ctx, agent.ID); close(done) }()
	waitForPendingApproval(t, runner, agent.ID, "bash-deny")
	accepted, err := runner.ApproveToolCall(ctx, agent.ID, "bash-deny", ToolApprovalDecision{Decision: "deny", Reason: "no", DecidedBy: "test"})
	if err != nil || !accepted {
		t.Fatalf("deny approval failed accepted=%v err=%v", accepted, err)
	}
	waitDone(t, done)
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-deny")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "denied" || call.PermissionDenyMessage != "no" || call.PermissionDecidedAt == "" || call.StartedAt != "" || call.CompletedAt == "" || call.UpdatedAt == "" {
		t.Fatalf("unexpected denied call: %+v", call)
	}
	if !requestHasToolResult(provider.request(1), "bash-deny", true) {
		t.Fatalf("expected denied bash result to be fed back as error")
	}
}

func TestRunnerBashApprovalAllowSessionSkipsSecondPrompt(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run bash twice"}); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"command":"echo session"}`)
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-session-1", Name: "Bash", Input: input}}, {Type: "done", Done: true}},
		{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-session-2", Name: "Bash", Input: input}}, {Type: "done", Done: true}},
		{{Type: "text", Text: "done"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 4})
	done := make(chan struct{})
	go func() { runner.Run(ctx, agent.ID); close(done) }()
	waitForPendingApproval(t, runner, agent.ID, "bash-session-1")
	accepted, err := runner.ApproveToolCall(ctx, agent.ID, "bash-session-1", ToolApprovalDecision{Decision: "allow_session", Reason: "ok", DecidedBy: "test"})
	if err != nil || !accepted {
		t.Fatalf("session approval failed accepted=%v err=%v", accepted, err)
	}
	waitDone(t, done)
	if runnerPendingApprovalCount(runner) != 0 {
		t.Fatalf("expected no pending approvals, got %d", runnerPendingApprovalCount(runner))
	}
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-session-2")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "completed" || call.PermissionDecisionReason != "allowed by permission mode" && call.PermissionDecisionReason != "auto-approved by built-in exec whitelist" && call.PermissionDecisionReason != "allowed by session approval" {
		t.Fatalf("expected second session command to auto execute, got %+v", call)
	}
}

func TestRunnerDirectToolSetupFailureFinalizesAuditRow(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := writeTestFile(projectDir, "note.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	store, agent := newAgentTestStore(t, projectDir, "acceptEdits")
	defer store.Close()
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	runner := NewRunner(store, nil, registry, NewHub(), config.AgentConfig{})
	runner.SetPlanSnapshotProvider(func(context.Context, string) (db.PlanSnapshot, error) {
		return db.PlanSnapshot{}, errors.New("snapshot unavailable")
	})

	result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: "read-setup-error", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)})
	if err == nil || !strings.Contains(err.Error(), "snapshot unavailable") {
		t.Fatalf("expected snapshot setup error, got result=%+v err=%v", result, err)
	}
	if !result.IsError || !strings.Contains(result.Output, "snapshot unavailable") {
		t.Fatalf("expected surfaced setup failure result, got %+v", result)
	}
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "read-setup-error")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "error" || !strings.Contains(call.ErrorMessage, "snapshot unavailable") || call.CompletedAt == "" {
		t.Fatalf("expected terminal error audit row, got %+v", call)
	}
}

func TestRunnerDirectBackgroundToolUsesNonGitRuntimeSnapshot(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := NewRunner(store, nil, nil, NewHub(), config.AgentConfig{})
	runner.SetPlanSnapshotProvider(func(context.Context, string) (db.PlanSnapshot, error) {
		return db.PlanSnapshot{}, errors.New("workspace must be a Git repository before a plan can be approved")
	})
	runner.SetBackgroundTaskSnapshotProvider(func(context.Context, string) (db.PlanSnapshot, error) {
		return db.PlanSnapshot{PolicyGenerationSnapshot: 11, AgentGenerationSnapshot: 12, ToolCatalogDigest: "runtime-tools"}, nil
	})

	for _, toolName := range []string{"Agent", "Bash"} {
		env, err := runner.toolExecutionEnv(ctx, agent, "", toolName, nil)
		if err != nil {
			t.Fatalf("direct %s should not use the Git-strict plan snapshot: %v", toolName, err)
		}
		if env.ToolCatalogDigest != "runtime-tools" || env.WorkspaceFingerprint != "" || env.PolicyGenerationSnapshot != 11 || env.AgentGenerationSnapshot != 12 {
			t.Fatalf("unexpected direct %s runtime snapshot: %+v", toolName, env)
		}
	}
}

func TestRunnerReturnsDeniedToolResultToModel(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "readOnly")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "write file"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "tool-denied", Name: "Write", Input: json.RawMessage(`{"file_path":"x.txt","content":"x"}`)}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
		{{Type: "text", Text: "cannot write in readOnly"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 3})

	runner.Run(ctx, agent.ID)

	call, err := store.GetToolCallByUseID(ctx, agent.ID, "tool-denied")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "denied" {
		t.Fatalf("expected denied tool call, got %+v", call)
	}
	if !requestHasToolResult(provider.request(1), "tool-denied", true) {
		t.Fatalf("expected denied result to be fed back as error tool_result")
	}
}
