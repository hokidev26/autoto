package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type scriptedProvider struct {
	mu            sync.Mutex
	requests      []providers.GenerateRequest
	turns         [][]providers.Event
	onGenerate    func(int)
	reasoning     bool
	fastModels    map[string]bool
	contextLimits map[string]int
}

func (p *scriptedProvider) Name() string { return "fake" }

func (p *scriptedProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Tools: true, Streaming: true, ImageInput: true, Reasoning: p.reasoning}
}

func (p *scriptedProvider) ModelCapabilities(model string) providers.ModelCapabilities {
	return providers.ModelCapabilities{FastMode: p.fastModels[model], FastModeKnown: true, ContextTokenLimit: p.contextLimits[model]}
}

func (p *scriptedProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}

func (p *scriptedProvider) Generate(ctx context.Context, req providers.GenerateRequest) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	idx := len(p.requests) - 1
	var events []providers.Event
	if idx < len(p.turns) {
		events = append([]providers.Event(nil), p.turns[idx]...)
	}
	hook := p.onGenerate
	p.mu.Unlock()
	if hook != nil {
		hook(idx)
	}
	out := make(chan providers.Event, len(events))
	go func() {
		defer close(out)
		for _, event := range events {
			select {
			case <-ctx.Done():
				return
			case out <- event:
			}
		}
	}()
	return out, nil
}

func (p *scriptedProvider) request(index int) providers.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[index]
}

func (p *scriptedProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

type blockingProvider struct {
	started chan struct{}
	once    sync.Once
}

func (p *blockingProvider) Name() string { return "fake" }

func (p *blockingProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}

func (p *blockingProvider) Generate(ctx context.Context, req providers.GenerateRequest) (<-chan providers.Event, error) {
	p.once.Do(func() { close(p.started) })
	return make(chan providers.Event), nil
}

func newCheckpointTestRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Autoto Test"}, {"config", "user.email", "test@example.com"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeTestFile(repo, "tracked.txt", "base\n"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "initial"}} {
		if _, err := runCheckpointGit(ctx, repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func newAgentTestStore(t *testing.T, projectDir, permissionMode string) (*db.Store, db.Agent) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(context.Background(), db.ToolExecutionGroupSchemaSQL()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(context.Background(), "Demo", "", projectDir, "fake:test", permissionMode)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, agent
}

func enableAgentTestTool(t *testing.T, store *db.Store, toolName string) {
	t.Helper()
	if _, err := store.SetToolAvailabilityRuleCAS(context.Background(), db.ToolAvailabilityTarget{Scope: db.ToolAvailabilityScopeGlobal}, toolName, db.ToolAvailabilityEnabled, 0, "test"); err != nil {
		t.Fatal(err)
	}
}

func newAgentTestRunner(store *db.Store, provider providers.Provider, cfg config.AgentConfig) *Runner {
	registry := providers.NewRegistry()
	registry.Register(provider)
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	return NewRunner(store, registry, toolRegistry, NewHub(), cfg)
}

// disableReflectionForTest turns the danger-reflection gate off for the
// duration of a test that is not exercising the reflection code path. Tests
// that test static permission policy or lifecycle behaviour should call this so
// they do not hang waiting for approval when the scripted provider has no
// reflection turns queued.
func disableReflectionForTest(t *testing.T, store *db.Store) {
	t.Helper()
	ctx := context.Background()
	prefs, err := store.GetWorkflowPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefs.DangerReflectionLevel = "off"
	if _, err := store.UpdateWorkflowPreferences(ctx, prefs); err != nil {
		t.Fatal(err)
	}
}

func waitForPendingApproval(t *testing.T, runner *Runner, agentID, toolUseID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runner.approvalMu.Lock()
		approval := runner.approvals[approvalKey(agentID, toolUseID)]
		runner.approvalMu.Unlock()
		if approval != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for approval %s", toolUseID)
}

func runnerPendingApprovalCount(runner *Runner) int {
	runner.approvalMu.Lock()
	defer runner.approvalMu.Unlock()
	return len(runner.approvals)
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner")
	}
}

// waitForRunSettled blocks until a run started by Submit* has fully finished.
//
// Submit* starts the run in a goroutine bound to context.Background(), so a test
// that returns early leaves that goroutine writing to the store and the project
// directory while t.TempDir() cleanup is already removing them — the source of
// intermittent "directory not empty" cleanup failures and cross-test interference.
//
// Waiting for a terminal run status alone is not enough: executeRegisteredRun
// still closes the tool-output pipeline, unregisters the run, and resumes ready
// background continuations after the status is written. So this also waits for
// the runner to release the agent's active run. The run row is created
// synchronously by Submit* and only reaches a terminal status from inside the
// registered run, so observing "terminal status, then no active run" cannot pass
// before the goroutine has actually started.
func waitForRunSettled(t *testing.T, store *db.Store, runner *Runner, agentID, runID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	lastStatus := ""
	for {
		settled := runID == ""
		if !settled {
			run, err := store.GetRun(context.Background(), agentID, runID)
			if err != nil {
				t.Fatalf("get run %s: %v", runID, err)
			}
			lastStatus = run.Status
			switch run.Status {
			case "completed", "error", "failed", "interrupted", "superseded", "denied":
				settled = true
			}
		}
		if settled {
			runner.runMu.Lock()
			idle := runner.running[agentID] == nil
			runner.runMu.Unlock()
			if idle {
				return
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for run %s to settle, last status %q", runID, lastStatus)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func requestHasToolResult(req providers.GenerateRequest, toolUseID string, isError bool) bool {
	for _, message := range req.Messages {
		for _, block := range message.Blocks {
			if block.Type == "tool_result" && block.ToolUseID == toolUseID && block.IsError == isError {
				return true
			}
		}
	}
	return false
}

func toolResultOutput(messages []providers.Message, toolUseID string) string {
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.Type == "tool_result" && block.ToolUseID == toolUseID {
				return block.Output
			}
		}
	}
	return ""
}

func requestHasSystemText(req providers.GenerateRequest, text string) bool {
	return requestHasRoleText(req, "system", text)
}

func requestHasRoleText(req providers.GenerateRequest, role, text string) bool {
	for _, message := range req.Messages {
		if message.Role != role {
			continue
		}
		if strings.Contains(message.Content, text) {
			return true
		}
		for _, block := range message.Blocks {
			if strings.Contains(block.Text, text) || strings.Contains(block.Output, text) {
				return true
			}
		}
	}
	return false
}

func requestHasUserText(req providers.GenerateRequest, text string) bool {
	for _, message := range req.Messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Blocks {
			if block.Type == "text" && strings.Contains(block.Text, text) {
				return true
			}
		}
	}
	return false
}

func requestUntrustedUserContext(req providers.GenerateRequest, source string) (string, bool) {
	opening := `<untrusted_context source="` + source + `">`
	for _, message := range req.Messages {
		if message.Role != "user" {
			continue
		}
		candidates := []string{message.Content}
		for _, block := range message.Blocks {
			if block.Type == "text" {
				candidates = append(candidates, block.Text)
			}
		}
		for _, candidate := range candidates {
			if strings.Contains(candidate, opening) && strings.Contains(candidate, "</untrusted_context>") {
				return candidate, true
			}
		}
	}
	return "", false
}

func requestHasUntrustedUserContext(req providers.GenerateRequest, source, text string) bool {
	contextText, ok := requestUntrustedUserContext(req, source)
	return ok && strings.Contains(contextText, text)
}

func waitForAgentStatus(t *testing.T, store *db.Store, agentID, status string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		agent, err := store.GetAgent(context.Background(), agentID)
		if err != nil {
			t.Fatal(err)
		}
		if agent.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for agent status %q", status)
}

func writeTestFile(root, name, content string) error {
	result, err := (tools.WriteTool{}).Execute(context.Background(), tools.Call{ID: "setup", Name: "Write", Input: mustJSON(map[string]string{"file_path": name, "content": content})}, tools.Env{CWD: root})
	if err != nil {
		return err
	}
	if result.IsError {
		return errors.New(result.Output)
	}
	return nil
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
