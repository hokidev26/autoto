package background

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type discardOutputWriter struct{}

func (discardOutputWriter) Write(string, []byte) error { return nil }
func (discardOutputWriter) Truncated() bool            { return false }

type messageTestProvider struct{ reply string }

func (messageTestProvider) Name() string { return "fake" }
func (messageTestProvider) ListModels(context.Context) ([]string, error) {
	return []string{"model"}, nil
}
func (p messageTestProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	ch := make(chan providers.Event, 2)
	ch <- providers.Event{Type: "text", Text: p.reply}
	ch <- providers.Event{Type: "done", Done: true}
	close(ch)
	return ch, nil
}

func newMessageTaskFixture(t *testing.T, reply string) (*db.Store, *AgentExecutor, db.Agent, db.Agent) {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "message.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, _, sender, err := store.CreateProject(ctx, "Coordinator", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, target, err := store.CreateProject(ctx, "Weather", "", t.TempDir(), "fake:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(messageTestProvider{reply: reply})
	toolRegistry := tools.NewRegistry()
	runner := agent.NewRunner(store, registry, toolRegistry, agent.NewHub(), config.AgentConfig{MaxTurns: 2})
	executor := NewAgentExecutor(store, runner)
	executor.PollInterval = 10 * time.Millisecond
	executor.TargetBusyWait = 50 * time.Millisecond
	return store, executor, sender, target
}

func claimedMessageTask(t *testing.T, store *db.Store, sender db.Agent, targetID, prompt string) db.BackgroundTask {
	t.Helper()
	ctx := context.Background()
	payload, err := json.Marshal(agentPayload{Prompt: prompt, TargetAgentID: targetID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBackgroundTask(ctx, db.BackgroundTask{
		OwnerAgentID: sender.ID, Kind: db.BackgroundTaskKindAgent, PayloadJSON: payload,
		PublicSummaryJSON: json.RawMessage(`{"targetAgentId":"` + targetID + `"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimQueuedBackgroundTask(ctx, db.BackgroundTaskClaimOptions{WorkerInstanceID: "test-worker"})
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}

func TestParseAgentPayloadTargetAgentIDRules(t *testing.T) {
	payload, err := parseAgentPayload(json.RawMessage(`{"prompt":"總結結論","targetAgentId":"agent-b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.TargetAgentID != "agent-b" || payload.SubagentType != "" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	rejected := []string{
		`{"prompt":"x","targetAgentId":"agent-b","subagentType":"general"}`,
		`{"prompt":"x","targetAgentId":"agent-b","model":"fake:model"}`,
		`{"prompt":"x","targetAgentId":"agent-b","workdir":"sub"}`,
		`{"prompt":"x","targetAgentId":"agent-b","reasoningEffort":"high"}`,
		`{"prompt":"x","targetAgentId":"agent-b","acceptanceCriteria":["done"]}`,
		`{"prompt":"x","targetAgentId":"` + strings.Repeat("a", 129) + `"}`,
	}
	for _, raw := range rejected {
		if _, err := parseAgentPayload(json.RawMessage(raw)); err == nil {
			t.Fatalf("payload was accepted but must be rejected: %s", raw)
		}
	}
}

func TestNarrowestPermissionCapClampsWithoutWidening(t *testing.T) {
	tests := []struct {
		target  string
		sender  string
		want    string
		wantErr bool
	}{
		{target: "acceptEdits", sender: "", want: "acceptEdits"},
		{target: "acceptEdits", sender: "readOnly", want: "readOnly"},
		{target: "readOnly", sender: "acceptEdits", want: "readOnly"},
		{target: "bypassPermissions", sender: "", want: "bypassPermissions"},
		{target: "bypassPermissions", sender: "acceptEdits", want: "acceptEdits"},
		{target: "default", sender: "bypassPermissions", want: "acceptEdits"},
		{target: "readOnly", sender: "bypassPermissions", want: "readOnly"},
		{target: "invalid", sender: "", wantErr: true},
		{target: "acceptEdits", sender: "invalid", wantErr: true},
	}
	for _, test := range tests {
		got, err := narrowestPermissionCap(test.target, test.sender)
		if test.wantErr {
			if err == nil {
				t.Fatalf("narrowestPermissionCap(%q, %q) = %q, want error", test.target, test.sender, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("narrowestPermissionCap(%q, %q) = %q, %v; want %q", test.target, test.sender, got, err, test.want)
		}
	}
}

func TestMessagePublicResultDropsOversizedReplyInsteadOfFailing(t *testing.T) {
	result, err := marshalMessagePublicResultWithReply("agent-b", "Weather", "run-1", "completed", strings.Repeat("答", 4000), false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) > maxAgentResultBytes {
		t.Fatalf("result is %d bytes, over the %d budget", len(result), maxAgentResultBytes)
	}
	var projected messagePublicResult
	if err := json.Unmarshal(result, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Reply != "" || !projected.ReplyTruncated {
		t.Fatalf("oversized reply was not dropped with a truncation marker: %+v", projected)
	}
}

func TestExecuteSendMessageRejectsInvalidTargets(t *testing.T) {
	ctx := context.Background()
	store, executor, sender, target := newMessageTaskFixture(t, "ok")
	child, err := store.CreateAgent(ctx, db.Agent{
		WorklineID: target.WorklineID, ParentAgentID: target.ID, Type: "subagent", SubagentType: "general",
		Title: "child", Model: target.Model, PermissionMode: "readOnly", Status: "idle", CWD: target.CWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := db.BackgroundTask{ID: "task-x", OwnerAgentID: sender.ID, Kind: db.BackgroundTaskKindAgent, Revision: 1}

	tests := []struct {
		name     string
		owner    db.Agent
		targetID string
		wantCode string
	}{
		{name: "self", owner: sender, targetID: sender.ID, wantCode: "message_target_rejected"},
		{name: "subagent target", owner: sender, targetID: child.ID, wantCode: "message_target_rejected"},
		{name: "missing target", owner: sender, targetID: "missing", wantCode: "message_target_unavailable"},
		{name: "subagent owner", owner: child, targetID: target.ID, wantCode: "message_owner_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := executor.executeSendMessage(ctx, task, test.owner, agentPayload{Prompt: "hi", TargetAgentID: test.targetID}, discardOutputWriter{})
			if err == nil || result.ErrorCode != test.wantCode {
				t.Fatalf("errorCode = %q err = %v, want %q", result.ErrorCode, err, test.wantCode)
			}
		})
	}
}

func TestExecuteSendMessageRejectsArchivedTargetAndDirectCycle(t *testing.T) {
	ctx := context.Background()
	store, executor, sender, target := newMessageTaskFixture(t, "ok")
	task := db.BackgroundTask{ID: "task-x", OwnerAgentID: sender.ID, Kind: db.BackgroundTaskKindAgent, Revision: 1}

	archived := true
	if _, err := store.UpdateAgentNavigationState(ctx, target.ID, nil, &archived); err != nil {
		t.Fatal(err)
	}
	result, err := executor.executeSendMessage(ctx, task, sender, agentPayload{Prompt: "hi", TargetAgentID: target.ID}, discardOutputWriter{})
	if err == nil || result.ErrorCode != "message_target_rejected" {
		t.Fatalf("archived target: errorCode = %q err = %v", result.ErrorCode, err)
	}
	restored := false
	if _, err := store.UpdateAgentNavigationState(ctx, target.ID, nil, &restored); err != nil {
		t.Fatal(err)
	}

	// The target already has an active message task pointed back at the sender.
	reversePayload, err := json.Marshal(agentPayload{Prompt: "ping", TargetAgentID: sender.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBackgroundTask(ctx, db.BackgroundTask{
		OwnerAgentID: target.ID, Kind: db.BackgroundTaskKindAgent, PayloadJSON: reversePayload,
		PublicSummaryJSON: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	result, err = executor.executeSendMessage(ctx, task, sender, agentPayload{Prompt: "hi", TargetAgentID: target.ID}, discardOutputWriter{})
	if err != nil || result.ErrorCode != "" {
		t.Fatalf("direct cycle should be informational: errorCode = %q err = %v", result.ErrorCode, err)
	}
	var projected messagePublicResult
	if err := json.Unmarshal(result.JSON, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Status != "already_in_progress" || !strings.Contains(projected.TargetError, "reply will return automatically") {
		t.Fatalf("unexpected informational cycle result: %+v", projected)
	}
}

func TestExecuteSendMessageDeliversPromptAndReportsReply(t *testing.T) {
	ctx := context.Background()
	store, executor, sender, target := newMessageTaskFixture(t, "台北明天多雲時晴,降雨機率 20%。")
	const prompt = "請總結你查到的台北天氣結論"
	claimed := claimedMessageTask(t, store, sender, target.ID, prompt)

	result, err := executor.Execute(ctx, claimed, discardOutputWriter{})
	if err != nil {
		t.Fatalf("execute failed: result=%+v err=%v", result, err)
	}
	var projected messagePublicResult
	if err := json.Unmarshal(result.JSON, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Status != "completed" || projected.TargetAgentID != target.ID {
		t.Fatalf("unexpected result: %+v", projected)
	}
	if !strings.Contains(projected.Reply, "降雨機率") {
		t.Fatalf("reply missing target answer: %+v", projected)
	}

	attached, err := store.GetBackgroundTask(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attached.ChildAgentID != target.ID || attached.ChildRunID == "" || attached.ChildRunID != projected.RunID {
		t.Fatalf("task child linkage missing: %+v", attached)
	}

	run, err := store.GetRunByID(ctx, projected.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.AgentID != target.ID || run.Source != "internal" || run.SourceID != claimed.ID || run.Status != "completed" {
		t.Fatalf("unexpected target run: %+v", run)
	}

	messages, err := store.ListMessages(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	sawPrompt, sawReply := false, false
	for _, message := range messages {
		if message.Role == "user" && strings.Contains(message.ContentText, prompt) {
			sawPrompt = true
		}
		if message.Role == "assistant" && strings.Contains(message.ContentText, "降雨機率") {
			sawReply = true
		}
	}
	if !sawPrompt || !sawReply {
		t.Fatalf("target transcript missing prompt/reply: prompt=%v reply=%v", sawPrompt, sawReply)
	}
}

func TestExecuteSendMessageFailsClearlyWhenTargetStaysBusy(t *testing.T) {
	ctx := context.Background()
	store, executor, sender, target := newMessageTaskFixture(t, "ok")
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO runs (id, agent_id, status, created_at, updated_at) VALUES (?, ?, 'running', ?, ?)`, db.NewID(), target.ID, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	claimed := claimedMessageTask(t, store, sender, target.ID, "hello")

	result, err := executor.Execute(ctx, claimed, discardOutputWriter{})
	if err == nil || result.ErrorCode != "target_agent_busy" {
		t.Fatalf("busy target: errorCode = %q err = %v", result.ErrorCode, err)
	}
}
