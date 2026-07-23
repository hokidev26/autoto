package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/integrations"
	"autoto/internal/secrets"
	"autoto/internal/tools"
)

const testBotToken = "123456:TEST_bot-token"

type fakeTelegram struct {
	server *httptest.Server

	mu           sync.Mutex
	updates      []telegramUpdate
	sent         []fakeSentMessage
	offsets      []int64
	replayAll    bool
	blockOnEmpty bool
	entered      chan struct{}
	canceled     chan struct{}
	enterOnce    sync.Once
	cancelOnce   sync.Once
}

type fakeSentMessage struct {
	ChatID string
	Text   string
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	fake := &fakeTelegram{entered: make(chan struct{}), canceled: make(chan struct{})}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeTelegram) handle(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(request.URL.Path, "/getUpdates"):
		var input struct {
			Offset int64 `json:"offset"`
		}
		if json.NewDecoder(request.Body).Decode(&input) != nil {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.offsets = append(f.offsets, input.Offset)
		updates := append([]telegramUpdate(nil), f.updates...)
		replayAll := f.replayAll
		block := f.blockOnEmpty
		f.mu.Unlock()
		if !replayAll {
			filtered := updates[:0]
			for _, update := range updates {
				if update.UpdateID >= input.Offset {
					filtered = append(filtered, update)
				}
			}
			updates = filtered
		}
		if len(updates) == 0 && block {
			f.enterOnce.Do(func() { close(f.entered) })
			<-request.Context().Done()
			f.cancelOnce.Do(func() { close(f.canceled) })
			return
		}
		if len(updates) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": updates})
	case strings.HasSuffix(request.URL.Path, "/sendMessage"):
		var input struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		if json.NewDecoder(request.Body).Decode(&input) != nil {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.sent = append(f.sent, fakeSentMessage{ChatID: input.ChatID, Text: input.Text})
		f.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	default:
		http.NotFound(writer, request)
	}
}

func (f *fakeTelegram) addUpdates(updates ...telegramUpdate) {
	f.mu.Lock()
	f.updates = append(f.updates, updates...)
	f.mu.Unlock()
}

func (f *fakeTelegram) sentMessages() []fakeSentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSentMessage(nil), f.sent...)
}

func privateTextUpdate(updateID, chatID, userID int64, text string) telegramUpdate {
	return telegramUpdate{
		UpdateID: updateID,
		Message: &telegramMessage{
			MessageID: updateID,
			Date:      time.Now().Unix(),
			Chat:      telegramChat{ID: chatID, Type: "private"},
			From:      &telegramUser{ID: userID},
			Text:      &text,
		},
	}
}

func groupTextUpdate(updateID, chatID, userID int64, text string) telegramUpdate {
	update := privateTextUpdate(updateID, chatID, userID, text)
	update.Message.Chat.Type = "group"
	return update
}

type testEnvironment struct {
	ctx        context.Context
	store      *db.Store
	agent      db.Agent
	connection db.IntegrationConnection
	resolver   *integrations.ConnectionService
	registry   *tools.Registry
}

func newTestEnvironment(t *testing.T) testEnvironment {
	t.Helper()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, _, agentRecord, err := store.CreateProject(ctx, "Agent Alpha", "", t.TempDir(), "test-model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE agents SET status = 'running', system_prompt = 'TOP SECRET SYSTEM', cwd = '/very/secret/path' WHERE id = ?`, agentRecord.ID); err != nil {
		t.Fatal(err)
	}
	agentRecord, err = store.GetAgent(ctx, agentRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.CreateIntegrationConnection(ctx, db.IntegrationConnection{
		Kind: TelegramKind, Name: "primary", Enabled: true,
		SecretRefs: map[string]string{"botToken": "env:TEST_TELEGRAM_BOT_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := integrations.NewConnectionService(store, secrets.EnvResolver{LookupEnv: func(name string) (string, bool) {
		if name != "TEST_TELEGRAM_BOT_TOKEN" {
			return "", false
		}
		return testBotToken, true
	}})
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	return testEnvironment{ctx: ctx, store: store, agent: agentRecord, connection: connection, resolver: resolver, registry: registry}
}

func (environment testEnvironment) managerConfig(fake *fakeTelegram, approvals ApprovalService) Config {
	return Config{
		Store: environment.store, Connections: environment.resolver, Approvals: approvals, Tools: environment.registry,
		APIBase: fake.server.URL, HTTPClient: fake.server.Client(), RefreshInterval: 20 * time.Millisecond,
		LongPollTimeout: time.Second, RequestTimeout: 2 * time.Second, RetryDelay: 10 * time.Millisecond,
	}
}

func startManager(t *testing.T, config Config) *Manager {
	t.Helper()
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	return manager
}

func createPendingPairing(t *testing.T, environment testEnvironment, code string) db.ChannelPairing {
	t.Helper()
	hash := sha256.Sum256([]byte(code))
	pairing, err := environment.store.CreateChannelPairing(environment.ctx, db.ChannelPairing{
		ConnectionID: environment.connection.ID,
		AgentID:      environment.agent.ID,
		CodeHash:     hex.EncodeToString(hash[:]),
		ExpiresAt:    time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pairing
}

func createActivePairing(t *testing.T, environment testEnvironment, chatID, userID string) db.ChannelPairing {
	t.Helper()
	pairing, err := environment.store.CreateChannelPairing(environment.ctx, db.ChannelPairing{
		ConnectionID:       environment.connection.ID,
		AgentID:            environment.agent.ID,
		Status:             "active",
		ChatID:             chatID,
		UserID:             userID,
		CredentialRevision: telegramCredentialRevision(testBotToken),
		PairedAt:           db.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pairing
}

func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func waitCursor(t *testing.T, environment testEnvironment, want int64) {
	t.Helper()
	waitFor(t, func() bool {
		cursor, err := environment.store.GetChannelCursor(environment.ctx, environment.connection.ID)
		return err == nil && cursor.Offset >= want
	}, "telegram cursor did not advance")
}

func TestTelegramPairingPrivateOnlyAndUnpairedSilentAudit(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	pending := createPendingPairing(t, environment, "PAIR-CODE")
	manager := startManager(t, environment.managerConfig(fake, &approvalRecorder{}))

	fake.addUpdates(privateTextUpdate(1, 1001, 2001, "/pair PAIR-CODE"))
	waitFor(t, func() bool { return len(fake.sentMessages()) == 1 }, "pairing response was not sent")
	paired, err := environment.store.GetChannelPairing(environment.ctx, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paired.Status != "active" || paired.ChatID != "1001" || paired.UserID != "2001" || paired.AgentID != environment.agent.ID {
		t.Fatalf("unexpected active pairing: %+v", paired)
	}
	if paired.CredentialRevision != telegramCredentialRevision(testBotToken) {
		t.Fatalf("unexpected credential revision: %d", paired.CredentialRevision)
	}

	fake.addUpdates(
		privateTextUpdate(2, 9001, 9002, "/status"),
		groupTextUpdate(3, 1001, 2001, "/status"),
	)
	waitCursor(t, environment, 4)
	waitFor(t, func() bool {
		audits, err := environment.store.ListAutomationAuditEvents(environment.ctx, 20, 0)
		if err != nil {
			return false
		}
		for _, event := range audits {
			if event.Action == "telegram.unpaired_message" && event.Actor == "channel:telegram:9002" {
				return true
			}
		}
		return false
	}, "missing unpaired-message audit")
	messages := fake.sentMessages()
	if len(messages) != 1 || messages[0].Text != "Pairing complete." {
		t.Fatalf("unpaired or group message produced a response: %+v", messages)
	}
	statuses := manager.ListStatuses()
	if len(statuses) != 1 || statuses[0].ConnectionID != environment.connection.ID || !statuses[0].Running {
		t.Fatalf("unexpected manager statuses: %+v", statuses)
	}
}

func TestTelegramPairingFailuresLockPendingPairing(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	pending := createPendingPairing(t, environment, "RIGHT-CODE")
	startManager(t, environment.managerConfig(fake, &approvalRecorder{}))
	for updateID := int64(1); updateID <= 5; updateID++ {
		fake.addUpdates(privateTextUpdate(updateID, 3001, 3002, "/pair WRONG-CODE"))
	}
	waitCursor(t, environment, 6)
	var got db.ChannelPairing
	waitFor(t, func() bool {
		current, err := environment.store.GetChannelPairing(environment.ctx, pending.ID)
		if err != nil {
			return false
		}
		got = current
		return got.FailedAttempts == db.DefaultPairingMaxFailedAttempts && got.LockedUntil != ""
	}, "pending pairing was not locked")
	if messages := fake.sentMessages(); len(messages) != 0 {
		t.Fatalf("failed pairing attempts must remain silent, got %+v", messages)
	}
	lockedUntil, err := time.Parse(time.RFC3339Nano, got.LockedUntil)
	if err != nil || !lockedUntil.After(time.Now()) {
		t.Fatalf("invalid pairing lock time: %q", got.LockedUntil)
	}
}

func TestTelegramCursorPersistsAndReplayIsIdempotent(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	fake.replayAll = true
	createActivePairing(t, environment, "4001", "4002")
	fake.addUpdates(privateTextUpdate(10, 4001, 4002, "/status"))

	manager := startManager(t, environment.managerConfig(fake, &approvalRecorder{}))
	waitCursor(t, environment, 11)
	waitFor(t, func() bool { return len(fake.sentMessages()) == 1 }, "first status response missing")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := manager.Close(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()

	second, err := NewManager(environment.managerConfig(fake, &approvalRecorder{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = second.Close(ctx)
	})
	time.Sleep(100 * time.Millisecond)
	events, err := environment.store.ListChannelEvents(environment.ctx, db.ChannelEventListOptions{ConnectionID: environment.connection.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ExternalEventID != "10" {
		t.Fatalf("replayed update was written more than once: %+v", events)
	}
	if len(fake.sentMessages()) != 1 {
		t.Fatalf("replayed update executed more than once: %+v", fake.sentMessages())
	}
}

func TestTelegramStatusIsMinimalAndRedacted(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	createActivePairing(t, environment, "5001", "5002")
	if _, err := environment.store.CreateRun(environment.ctx, db.Run{AgentID: environment.agent.ID, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.store.AddToolCall(environment.ctx, db.ToolCall{
		AgentID: environment.agent.ID, ToolUseID: "pending-status", ToolName: "Read",
		InputJSON: json.RawMessage(`{"file_path":"PRIVATE TOOL INPUT"}`), Status: "pending_approval",
	}); err != nil {
		t.Fatal(err)
	}
	startManager(t, environment.managerConfig(fake, &approvalRecorder{}))
	fake.addUpdates(privateTextUpdate(1, 5001, 5002, "/status"))
	waitFor(t, func() bool { return len(fake.sentMessages()) == 1 }, "status response missing")
	text := fake.sentMessages()[0].Text
	for _, expected := range []string{"Agent: Agent Alpha", "Status: running", "Recent run: completed", "Pending approvals: 1"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("status response missing %q: %s", expected, text)
		}
	}
	for _, forbidden := range []string{"/very/secret/path", "TOP SECRET SYSTEM", "PRIVATE TOOL INPUT", "file_path"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, text)
		}
	}
}

type recordedApproval struct {
	agentID   string
	toolUseID string
	decision  ApprovalDecision
}

type approvalRecorder struct {
	mu    sync.Mutex
	calls []recordedApproval
	order *[]string
}

func (r *approvalRecorder) ApproveToolCall(_ context.Context, agentID, toolUseID string, decision ApprovalDecision) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedApproval{agentID: agentID, toolUseID: toolUseID, decision: decision})
	if r.order != nil {
		*r.order = append(*r.order, "approval:"+toolUseID)
	}
	return true, nil
}

func (r *approvalRecorder) snapshot() []recordedApproval {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedApproval(nil), r.calls...)
}

func TestTelegramApproveDenyDangerAndPersistedGenerations(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	createActivePairing(t, environment, "6001", "6002")
	calls := []db.ToolCall{
		{AgentID: environment.agent.ID, RunID: "", ToolUseID: "read-approve", ToolName: "Read", InputJSON: json.RawMessage(`{"file_path":"README.md"}`), Status: "pending_approval", PermissionGeneration: 7, PolicyGeneration: 9},
		{AgentID: environment.agent.ID, RunID: "", ToolUseID: "write-deny", ToolName: "Write", InputJSON: json.RawMessage(`{"file_path":"x","content":"y"}`), Status: "pending_approval", PermissionGeneration: 11, PolicyGeneration: 13},
		{AgentID: environment.agent.ID, RunID: "", ToolUseID: "bash-danger", ToolName: "Bash", InputJSON: json.RawMessage(`{"command":"rm -rf tmp"}`), Status: "pending_approval", PermissionGeneration: 17, PolicyGeneration: 19},
	}
	for _, call := range calls {
		if _, err := environment.store.AddToolCall(environment.ctx, call); err != nil {
			t.Fatal(err)
		}
	}
	order := make([]string, 0)
	approvals := &approvalRecorder{order: &order}
	config := environment.managerConfig(fake, approvals)
	config.Audit = func(ctx context.Context, event db.AutomationAuditEvent) error {
		order = append(order, "audit:"+event.SubjectID)
		_, err := environment.store.AddAutomationAuditEvent(ctx, event)
		return err
	}
	startManager(t, config)
	fake.addUpdates(
		privateTextUpdate(1, 6001, 6002, "/approve read-approve"),
		privateTextUpdate(2, 6001, 6002, "/deny write-deny not now"),
		privateTextUpdate(3, 6001, 6002, "/approve bash-danger"),
	)
	waitFor(t, func() bool { return len(approvals.snapshot()) == 3 }, "approval decisions were not delivered")
	got := approvals.snapshot()
	assertDecision := func(index int, toolUseID, decision string, permissionGeneration, policyGeneration int64) {
		t.Helper()
		call := got[index]
		if call.agentID != environment.agent.ID || call.toolUseID != toolUseID || call.decision.Decision != decision {
			t.Fatalf("unexpected decision %d: %+v", index, call)
		}
		if call.decision.PermissionGeneration != permissionGeneration || call.decision.PolicyGeneration != policyGeneration {
			t.Fatalf("decision did not use persisted generations: %+v", call.decision)
		}
		if call.decision.DecidedBy != "channel:telegram:6002" || call.decision.Decision == "allow_session" {
			t.Fatalf("unsafe decision metadata: %+v", call.decision)
		}
	}
	assertDecision(0, "read-approve", "allow_once", 7, 9)
	assertDecision(1, "write-deny", "deny", 11, 13)
	assertDecision(2, "bash-danger", "deny", 17, 19)
	if got[1].decision.Reason != "not now" || !strings.Contains(got[2].decision.Reason, "cannot be approved") {
		t.Fatalf("unexpected denial reasons: %+v", got)
	}
	wantOrder := []string{
		"audit:read-approve", "approval:read-approve",
		"audit:write-deny", "approval:write-deny",
		"audit:bash-danger", "approval:bash-danger",
	}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("approval occurred before audit: got %v want %v", order, wantOrder)
	}
	audits, err := environment.store.ListAutomationAuditEvents(environment.ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range audits {
		encoded := string(event.DetailsJSON)
		if strings.Contains(encoded, "README.md") || strings.Contains(encoded, "rm -rf") || strings.Contains(encoded, "file_path") || strings.Contains(encoded, "command") {
			t.Fatalf("audit leaked tool input: %s", encoded)
		}
	}
}

func TestTelegramAuditFailureFailsClosed(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	createActivePairing(t, environment, "7001", "7002")
	if _, err := environment.store.AddToolCall(environment.ctx, db.ToolCall{
		AgentID: environment.agent.ID, ToolUseID: "audit-fail", ToolName: "Read",
		InputJSON: json.RawMessage(`{"file_path":"README.md"}`), Status: "pending_approval", PermissionGeneration: 3, PolicyGeneration: 5,
	}); err != nil {
		t.Fatal(err)
	}
	approvals := &approvalRecorder{}
	config := environment.managerConfig(fake, approvals)
	config.Audit = func(context.Context, db.AutomationAuditEvent) error { return errors.New("storage unavailable") }
	startManager(t, config)
	fake.addUpdates(privateTextUpdate(1, 7001, 7002, "/approve audit-fail"))
	waitFor(t, func() bool { return len(fake.sentMessages()) == 1 }, "fail-closed response missing")
	if len(approvals.snapshot()) != 0 {
		t.Fatalf("approval executed despite audit failure: %+v", approvals.snapshot())
	}
	if fake.sentMessages()[0].Text != "Decision was not applied." {
		t.Fatalf("unexpected fail-closed response: %+v", fake.sentMessages())
	}
	events, err := environment.store.ListChannelEvents(environment.ctx, db.ChannelEventListOptions{ConnectionID: environment.connection.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ToolUseID != "audit-fail" || events[0].ProcessedAt == "" {
		t.Fatalf("channel event was not recorded before the failed audit: %+v", events)
	}
}

func TestTelegramPerPairingRateLimit(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	pairing := createActivePairing(t, environment, "8001", "8002")
	startManager(t, environment.managerConfig(fake, &approvalRecorder{}))
	updates := make([]telegramUpdate, 0, 11)
	for updateID := int64(1); updateID <= 11; updateID++ {
		updates = append(updates, privateTextUpdate(updateID, 8001, 8002, "/status"))
	}
	fake.addUpdates(updates...)
	waitCursor(t, environment, 12)
	waitFor(t, func() bool { return len(fake.sentMessages()) == 11 }, "rate-limit responses missing")
	statusCount, rateLimited := 0, 0
	for _, message := range fake.sentMessages() {
		if strings.HasPrefix(message.Text, "Agent: ") {
			statusCount++
		}
		if strings.Contains(message.Text, "Rate limit exceeded") {
			rateLimited++
		}
	}
	if statusCount != 10 || rateLimited != 1 {
		t.Fatalf("unexpected rate-limit behavior: status=%d limited=%d messages=%+v", statusCount, rateLimited, fake.sentMessages())
	}
	audits, err := environment.store.ListAutomationAuditEvents(environment.ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audits {
		if event.Action == "telegram.rate_limited" && event.SubjectID == pairing.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("missing rate-limit audit")
	}
}

func TestTelegramCloseCancelsLongPollingAndWaits(t *testing.T) {
	environment := newTestEnvironment(t)
	fake := newFakeTelegram(t)
	fake.blockOnEmpty = true
	manager, err := NewManager(environment.managerConfig(fake, &approvalRecorder{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("long poll did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.canceled:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel the long poll")
	}
}

func TestTelegramErrorsDoNotLeakTokenURLOrMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "ORIGINAL MESSAGE BODY", http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := newTelegramClient(testBotToken, server.URL, server.Client(), time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.getUpdates(context.Background(), 0)
	if err == nil {
		t.Fatal("expected telegram request error")
	}
	message := err.Error()
	for _, forbidden := range []string{testBotToken, server.URL, "ORIGINAL MESSAGE BODY", "/bot"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("error leaked %q: %s", forbidden, message)
		}
	}
}
