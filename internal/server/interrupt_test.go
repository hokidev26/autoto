package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type interruptBlockingProvider struct {
	started chan struct{}
	once    sync.Once
}

type approvalRouteProvider struct {
	mu       sync.Mutex
	requests []providers.GenerateRequest
}

func (p *approvalRouteProvider) Name() string { return "fake" }
func (p *approvalRouteProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Tools: true, Streaming: true, ImageInput: true}
}
func (p *approvalRouteProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}
func (p *approvalRouteProvider) Generate(ctx context.Context, req providers.GenerateRequest) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	call := len(p.requests)
	p.mu.Unlock()
	out := make(chan providers.Event, 2)
	if call == 1 {
		out <- providers.Event{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "bash-route", Name: "Bash", Input: json.RawMessage(`{"command":"echo route"}`)}}
		out <- providers.Event{Type: "done", Done: true}
	} else {
		out <- providers.Event{Type: "text", Text: "approved"}
		out <- providers.Event{Type: "done", Done: true}
	}
	close(out)
	return out, nil
}

func (p *interruptBlockingProvider) Name() string { return "fake" }
func (p *interruptBlockingProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}
func (p *interruptBlockingProvider) Generate(context.Context, providers.GenerateRequest) (<-chan providers.Event, error) {
	p.once.Do(func() { close(p.started) })
	return make(chan providers.Event), nil
}

func TestApproveToolCallRouteReleasesPendingApproval(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "run"}); err != nil {
		t.Fatal(err)
	}
	provider := &approvalRouteProvider{}
	registry := providers.NewRegistry()
	registry.Register(provider)
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	hub := agentpkg.NewHub()
	runner := agentpkg.NewRunner(store, registry, toolRegistry, hub, config.AgentConfig{MaxTurns: 3})
	app := New(config.Config{}, store, runner, hub, registry)
	done := make(chan struct{})
	go func() {
		runner.Run(ctx, agent.ID)
		close(done)
	}()
	waitForToolCallStatus(t, store, agent.ID, "bash-route", "pending_approval")

	payload := []byte(`{"decision":"allow_once","reason":"route ok"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/tool-calls/bash-route/approval", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not finish after approval")
	}
	call, err := store.GetToolCallByUseID(ctx, agent.ID, "bash-route")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "completed" || call.PermissionDecisionReason != "route ok" {
		t.Fatalf("unexpected approved tool call: %+v", call)
	}
}

func waitForToolCallStatus(t *testing.T, store *db.Store, agentID, toolUseID, status string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		call, err := store.GetToolCallByUseID(context.Background(), agentID, toolUseID)
		if err == nil && call.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tool call %s status %s", toolUseID, status)
}

func TestInterruptAgentRouteCancelsActiveRun(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "wait"}); err != nil {
		t.Fatal(err)
	}
	provider := &interruptBlockingProvider{started: make(chan struct{})}
	registry := providers.NewRegistry()
	registry.Register(provider)
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	hub := agentpkg.NewHub()
	runner := agentpkg.NewRunner(store, registry, toolRegistry, hub, config.AgentConfig{MaxTurns: 2})
	app := New(config.Config{}, store, runner, hub, registry)

	done := make(chan struct{})
	go func() {
		runner.Run(ctx, agent.ID)
		close(done)
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/interrupt", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Interrupted bool `json:"interrupted"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Interrupted {
		t.Fatalf("expected interrupted response, got %+v", body)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after interrupt route")
	}
	updated, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "interrupted" {
		t.Fatalf("expected interrupted status, got %q", updated.Status)
	}
}
