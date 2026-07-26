package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type e2eProvider struct {
	mu       sync.Mutex
	requests []providers.GenerateRequest
}

func (p *e2eProvider) Name() string { return "fake" }

func (p *e2eProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Tools: true, Streaming: true, ImageInput: true}
}

func (p *e2eProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}

func (p *e2eProvider) Generate(_ context.Context, req providers.GenerateRequest) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	turn := len(p.requests)
	p.mu.Unlock()

	out := make(chan providers.Event, 3)
	if turn == 1 {
		out <- providers.Event{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "e2e-bash", Name: "Bash", Input: json.RawMessage(`{"command":"echo e2e-approved"}`)}}
		out <- providers.Event{Type: "done", Done: true, StopReason: "tool_use"}
	} else {
		out <- providers.Event{Type: "text", Text: "workflow complete"}
		out <- providers.Event{Type: "usage", Usage: &providers.Usage{InputTokens: 100, CachedInputTokens: 10, OutputTokens: 20}}
		out <- providers.Event{Type: "done", Done: true, StopReason: "end_turn"}
	}
	close(out)
	return out, nil
}

func (p *e2eProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *e2eProvider) request(index int) providers.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[index]
}

type captureNotifier struct {
	events chan agentpkg.NotificationEvent
}

func (n captureNotifier) Notify(_ context.Context, event agentpkg.NotificationEvent) {
	n.events <- event
}

func TestEndToEndMessageWebSocketApprovalToolExecutionAndPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	projectDir := t.TempDir()
	_, _, agent, err := store.CreateProject(ctx, "E2E", "", projectDir, "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	provider := &e2eProvider{}
	providerRegistry := providers.NewRegistry()
	providerRegistry.Register(provider)
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	hub := agentpkg.NewHub()
	runner := agentpkg.NewRunner(store, providerRegistry, toolRegistry, hub, config.AgentConfig{MaxTurns: 4})
	notifications := make(chan agentpkg.NotificationEvent, 4)
	runner.SetNotifier(captureNotifier{events: notifications})
	app := New(config.Config{}, store, runner, hub, providerRegistry)

	httpServer := httptest.NewServer(app.Routes())
	defer httpServer.Close()

	conn := dialAgentWebSocket(t, ctx, httpServer.URL, app.localToken, agent.ID)
	defer conn.CloseNow()
	readConnectedWebSocketEvent(t, ctx, conn)

	postJSON(t, ctx, httpServer.Client(), httpServer.URL+"/api/agents/"+agent.ID+"/messages", app.localToken, httpServer.URL, map[string]string{"text": "run the approved command"})

	approved := false
	sawToolOutput := false
	sawToolFinished := false
	sawCompletion := false
	for !(approved && sawToolOutput && sawToolFinished && sawCompletion) {
		event := readAgentWebSocketEvent(t, ctx, conn)
		switch event.Type {
		case "tool.approval_required":
			if event.Data["toolUseId"] != "e2e-bash" || event.Data["toolName"] != "Bash" {
				t.Fatalf("unexpected approval event: %+v", event)
			}
			if !approved {
				postJSON(t, ctx, httpServer.Client(), httpServer.URL+"/api/agents/"+agent.ID+"/tool-calls/e2e-bash/approval", app.localToken, httpServer.URL, map[string]string{"decision": "allow_once", "reason": "e2e approval"})
				approved = true
			}
		case "tool.output":
			if event.Data["toolUseId"] == "e2e-bash" && strings.Contains(event.Text, "e2e-approved") {
				sawToolOutput = true
			}
		case "tool.finished":
			if event.Data["toolUseId"] == "e2e-bash" && event.Data["status"] == "completed" {
				sawToolFinished = true
			}
		case "message.created":
			if strings.Contains(event.Text, "workflow complete") {
				sawCompletion = true
			}
		case "agent.error":
			t.Fatalf("unexpected agent error event: %+v", event)
		}
	}

	waitForAgentIdle(t, store, agent.ID)
	assertE2ENotifications(t, notifications)
	if got := provider.requestCount(); got != 2 {
		t.Fatalf("expected provider to be called twice, got %d", got)
	}
	if !requestHasE2EToolResult(provider.request(1)) {
		t.Fatalf("second provider request did not include approved tool result: %+v", provider.request(1).Messages)
	}

	call, err := store.GetToolCallByUseID(ctx, agent.ID, "e2e-bash")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != "completed" || call.PermissionDecisionReason != "e2e approval" || !strings.Contains(string(call.OutputJSON), "e2e-approved") {
		t.Fatalf("unexpected persisted tool call: %+v output=%s", call, string(call.OutputJSON))
	}

	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !messagesContain(messages, "assistant", "workflow complete") || !messagesContain(messages, "user", "e2e-approved") {
		t.Fatalf("expected assistant completion and tool result messages, got %+v", messages)
	}

	var apiRequests int
	var outputTokens int64
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(output_tokens), 0) FROM api_requests WHERE agent_id = ?`, agent.ID).Scan(&apiRequests, &outputTokens); err != nil {
		t.Fatal(err)
	}
	if apiRequests != 2 || outputTokens != 20 {
		t.Fatalf("expected two persisted provider requests with usage, got count=%d outputTokens=%d", apiRequests, outputTokens)
	}
}

type agentWSEvent struct {
	Type string         `json:"type"`
	Text string         `json:"text"`
	Data map[string]any `json:"data"`
}

func assertE2ENotifications(t *testing.T, notifications <-chan agentpkg.NotificationEvent) {
	t.Helper()
	sawApproval := false
	sawCompleted := false
	deadline := time.After(2 * time.Second)
	for !(sawApproval && sawCompleted) {
		select {
		case event := <-notifications:
			if event.Event == "approval_required" && event.ToolUseID == "e2e-bash" {
				sawApproval = true
			}
			if event.Event == "completed" {
				sawCompleted = true
			}
		case <-deadline:
			t.Fatalf("expected approval and completed notifications, got approval=%v completed=%v", sawApproval, sawCompleted)
		}
	}
}

func dialAgentWebSocket(t *testing.T, ctx context.Context, baseURL, token, agentID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws/agent?id=" + url.QueryEscape(agentID) + "&token=" + url.QueryEscape(token)
	header := http.Header{}
	header.Set("Origin", baseURL)
	conn, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if response != nil {
			t.Fatalf("websocket dial failed with status %d: %v", response.StatusCode, err)
		}
		t.Fatalf("websocket dial failed: %v", err)
	}
	return conn
}

func readConnectedWebSocketEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type           string `json:"type"`
		LatestSequence int64  `json:"latestSequence"`
	}
	if err := json.Unmarshal(data, &event); err != nil || event.Type != "connected" {
		t.Fatalf("expected connected event, got %s (err=%v)", string(data), err)
	}
}

func readAgentWebSocketEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) agentWSEvent {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event agentWSEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("decode websocket event %s: %v", string(data), err)
	}
	return event
}

func postJSON(t *testing.T, ctx context.Context, client *http.Client, endpoint, token, origin string, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set(localTokenHeader, token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s returned %d: %s", endpoint, response.StatusCode, string(body))
	}
}

func requestHasE2EToolResult(req providers.GenerateRequest) bool {
	for _, message := range req.Messages {
		for _, block := range message.Blocks {
			if block.Type == "tool_result" && block.ToolUseID == "e2e-bash" && strings.Contains(block.Output, "e2e-approved") {
				return true
			}
		}
	}
	return false
}

func messagesContain(messages []db.Message, role, text string) bool {
	for _, message := range messages {
		if message.Role == role && strings.Contains(message.ContentText, text) {
			return true
		}
	}
	return false
}
