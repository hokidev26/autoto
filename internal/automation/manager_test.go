package automation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autoto/internal/agent"
	"autoto/internal/db"
)

type fakeScheduleRunner struct {
	mu         sync.Mutex
	runs       []db.Schedule
	dispatches []string
	err        error
	store      *db.Store
}

func (r *fakeScheduleRunner) SubmitScheduleDispatch(ctx context.Context, schedule db.Schedule, dispatchID string) (db.Run, error) {
	r.mu.Lock()
	r.runs = append(r.runs, schedule)
	r.dispatches = append(r.dispatches, dispatchID)
	err := r.err
	r.mu.Unlock()
	if err != nil {
		return db.Run{}, err
	}
	return r.store.CreateRun(ctx, db.Run{AgentID: schedule.AgentID, Status: "running", Source: "schedule", SourceID: schedule.ID, PermissionModeCap: schedule.PermissionMode, DispatchID: dispatchID, TriggerType: "scheduled"})
}

func newAutomationStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNotificationDeliveryRetriesThenDeliversAndRecordsHistory(t *testing.T) {
	ctx := context.Background()
	store := newAutomationStore(t)
	defer store.Close()

	var mu sync.Mutex
	statuses := []int{http.StatusInternalServerError, http.StatusNoContent, http.StatusBadRequest}
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		status := statuses[0]
		statuses = statuses[1:]
		mu.Unlock()
		w.WriteHeader(status)
	}))
	defer webhook.Close()
	if _, err := store.UpdateNotificationSettings(ctx, db.NotificationSettings{Enabled: true, WebhookURL: webhook.URL, NotifyOnDone: true, NotifyOnError: true, NotifyOnApproval: true}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeScheduleRunner{store: store}
	manager, err := NewManager(Config{Store: store, Runner: runner, HTTPClient: webhook.Client()})
	if err != nil {
		t.Fatal(err)
	}

	manager.Notify(ctx, agent.NotificationEvent{Event: "completed", Status: "completed"})
	if err := manager.ProcessDeliveriesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListNotificationDeliveries(ctx, db.NotificationDeliveryListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "retry_wait" || items[0].AttemptCount != 1 || items[0].LastHTTPStatus != http.StatusInternalServerError {
		t.Fatalf("expected retry history after 500, got %+v", items)
	}
	// lease_until has to be cleared alongside next_attempt_at. The claim query
	// requires (lease_until IS NULL OR lease_until <= now), and the first pass
	// left a lease in the future; without this the second pass only picks the
	// row up if the lease happens to expire first, which made this test fail
	// under load and pass in isolation.
	if _, err := store.DB().ExecContext(ctx, `UPDATE notification_deliveries SET next_attempt_at = ?, lease_until = NULL WHERE id = ?`, time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.ProcessDeliveriesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	delivered, err := store.GetNotificationDelivery(ctx, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Status != "delivered" || delivered.AttemptCount != 2 || delivered.LastHTTPStatus != http.StatusNoContent || delivered.DeliveredAt == "" {
		t.Fatalf("expected delivered history, got %+v", delivered)
	}

	manager.Notify(ctx, agent.NotificationEvent{Event: "error", Status: "error", Error: "safe failure"})
	if err := manager.ProcessDeliveriesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListNotificationDeliveries(ctx, db.NotificationDeliveryListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Status != "dead" || items[0].LastHTTPStatus != http.StatusBadRequest || items[0].AttemptCount != 1 {
		t.Fatalf("expected non-retryable 400 to dead-letter and preserve history, got %+v", items)
	}
}

func TestWebhookDefaultClientRefusesLoopbackTarget(t *testing.T) {
	ctx := context.Background()
	store := newAutomationStore(t)
	defer store.Close()

	var hits int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()
	if _, err := store.UpdateNotificationSettings(ctx, db.NotificationSettings{Enabled: true, WebhookURL: webhook.URL, NotifyOnDone: true}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeScheduleRunner{store: store}
	// No HTTPClient injected: the manager builds its own hardened public-egress
	// client, which must refuse to dial the loopback httptest server instead of
	// bouncing the run-event payload to an internal address.
	manager, err := NewManager(Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	manager.Notify(ctx, agent.NotificationEvent{Event: "completed", Status: "completed"})
	if err := manager.ProcessDeliveriesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("hardened webhook client dialed a loopback target %d time(s); SSRF guard bypassed", got)
	}
	items, err := store.ListNotificationDeliveries(ctx, db.NotificationDeliveryListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status == "delivered" {
		t.Fatalf("expected the blocked delivery to never be delivered, got %+v", items)
	}
}

func TestTelegramNotificationsStayScopedToPairedAgentAndRedactErrors(t *testing.T) {
	ctx := context.Background()
	store := newAutomationStore(t)
	defer store.Close()
	_, _, firstAgent, err := store.CreateProject(ctx, "First", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, secondAgent, err := store.CreateProject(ctx, "Second", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.CreateIntegrationConnection(ctx, db.IntegrationConnection{
		Kind: "telegram", Name: "primary", Enabled: true, Endpoint: "https://api.telegram.org",
		SettingsJSON: []byte(`{}`), SecretRefs: map[string]string{"botToken": "env:AUTOTO_TEST_TELEGRAM_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPairing, err := store.CreateChannelPairing(ctx, db.ChannelPairing{
		ConnectionID: connection.ID, AgentID: firstAgent.ID, Status: "active", ChatID: "chat-1", UserID: "user-1", CredentialRevision: 1, PairedAt: db.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateChannelPairing(ctx, db.ChannelPairing{
		ConnectionID: connection.ID, AgentID: secondAgent.ID, Status: "active", ChatID: "chat-2", UserID: "user-2", CredentialRevision: 1, PairedAt: db.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: firstAgent.ID, Status: "error", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Store: store, Runner: &fakeScheduleRunner{store: store}})
	if err != nil {
		t.Fatal(err)
	}
	manager.Notify(ctx, agent.NotificationEvent{Event: "error", AgentID: firstAgent.ID, RunID: run.ID, Status: "error", Error: "/private/workspace/.env token=secret"})
	items, err := store.ListNotificationDeliveries(ctx, db.NotificationDeliveryListOptions{SinkType: "telegram", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SinkID != firstPairing.ID {
		t.Fatalf("expected only the matching agent pairing, got %+v", items)
	}
	payload := string(items[0].PayloadJSON)
	if strings.Contains(payload, "/private/workspace") || strings.Contains(payload, "secret") || !strings.Contains(payload, "open Autoto") {
		t.Fatalf("expected generic redacted notification error, got %s", payload)
	}
}

func TestSchedulerBusySkipsWithoutReplacingRun(t *testing.T) {
	ctx := context.Background()
	store := newAutomationStore(t)
	defer store.Close()
	_, _, agentRecord, err := store.CreateProject(ctx, "Schedule", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := store.CreateSchedule(ctx, db.Schedule{
		Name: "busy", AgentID: agentRecord.ID, Expression: "@every 1m", Timezone: "UTC", Prompt: "scheduled",
		PermissionMode: "readOnly", Enabled: true, NextRunAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeScheduleRunner{store: store, err: agent.ErrAgentBusy}
	manager, err := NewManager(Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ProcessSchedulesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastOutcome != "skipped" || updated.LastRunID == "" || updated.LastError != agent.ErrAgentBusy.Error() || updated.NextRunAt == "" {
		t.Fatalf("expected busy schedule skip bookkeeping, got %+v", updated)
	}
	run, err := store.GetRun(ctx, agentRecord.ID, updated.LastRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "skipped" || run.Source != "schedule" || run.SourceID != schedule.ID || run.PermissionModeCap != "readOnly" {
		t.Fatalf("unexpected skipped schedule run: %+v", run)
	}
}

func TestManagerCloseCancelsWorkersAndWaits(t *testing.T) {
	store := newAutomationStore(t)
	defer store.Close()
	runner := &fakeScheduleRunner{store: store}
	manager, err := NewManager(Config{Store: store, Runner: runner, PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if manager.Status().Running {
		t.Fatal("manager should report stopped after Close")
	}
	if err := manager.Close(closeCtx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("repeated close failed: %v", err)
	}
}

// GetSchedule canonicalizes timestamps on read, so a lease the trigger wrote in
// RFC3339Nano (which strips trailing zeros) can never match the compare-and-swap
// predicate that later clears it. Windows clocks tick in 100ns units, so every
// real "run now" click produced a trailing-zero lease and reported
// "cannot record schedule run" for work that had already been dispatched.
func TestManualTriggerClearsTheLeaseItTook(t *testing.T) {
	ctx := context.Background()
	store := newAutomationStore(t)
	defer store.Close()
	_, _, agentRecord, err := store.CreateProject(ctx, "Schedule", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := store.CreateSchedule(ctx, db.Schedule{
		Name: "manual", AgentID: agentRecord.ID, Expression: "@every 24h", Timezone: "UTC", Prompt: "check stock",
		PermissionMode: "readOnly", Enabled: true, NextRunAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Trailing-zero nanoseconds: exactly what a Windows wall clock produces.
	fixed := time.Date(2026, 7, 31, 1, 2, 3, 400000000, time.UTC)
	manager, err := NewManager(Config{Store: store, Runner: &fakeScheduleRunner{store: store}, Clock: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.TriggerSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("manual trigger reported %v (outcome %q)", err, result.Outcome)
	}
	updated, err := store.GetSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LeaseUntil != "" {
		t.Fatalf("the lease outlived the run it guarded: %q", updated.LeaseUntil)
	}
	if updated.LastOutcome != "success" || updated.LastRunID == "" {
		t.Fatalf("expected the manual run to be recorded, got %+v", updated)
	}
}

// The environment mode is a real choice only when the schedule creates a new
// narrator. Reusing an existing agent runs wherever that agent already lives,
// so a stored mode that disagrees used to fail every run with
// "reusable agent is attached to a workline" and no way to see why.
func TestReuseNarratorRunsWhereTheAgentAlreadyLives(t *testing.T) {
	ctx := context.Background()
	store := newAutomationStore(t)
	defer store.Close()
	_, _, projectAgent, err := store.CreateProject(ctx, "Schedule", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if projectAgent.WorklineID == "" {
		t.Fatal("expected the project agent to be attached to a workline")
	}
	// Standalone + reuse against a project agent: the combination the form
	// allows and the dispatcher used to reject.
	schedule, err := store.CreateSchedule(ctx, db.Schedule{
		Name: "mismatch", AgentID: projectAgent.ID, Expression: "@every 15m", Timezone: "UTC", Prompt: "check stock",
		PermissionMode: "readOnly", EnvironmentMode: "standalone", NarratorMode: "reuse", Enabled: true,
		NextRunAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeScheduleRunner{store: store}
	manager, err := NewManager(Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.TriggerSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("manual trigger reported %v (outcome %q)", err, result.Outcome)
	}
	runner.mu.Lock()
	dispatched := append([]db.Schedule(nil), runner.runs...)
	runner.mu.Unlock()
	if len(dispatched) != 1 {
		t.Fatalf("expected one dispatch, got %d", len(dispatched))
	}
	if dispatched[0].EnvironmentMode != "workline" {
		t.Fatalf("environment should follow the agent's workline, got %q", dispatched[0].EnvironmentMode)
	}
	if dispatched[0].AgentID != projectAgent.ID {
		t.Fatalf("reuse must dispatch to the linked agent, got %q", dispatched[0].AgentID)
	}
}
