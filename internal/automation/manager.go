package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"autoto/internal/agent"
	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/runtime"
	"autoto/internal/schedules"
)

const (
	defaultPollInterval  = 500 * time.Millisecond
	defaultLeaseDuration = 30 * time.Second
	webhookTimeout       = 5 * time.Second
	maxWebhookResponse   = 64 << 10
	maxPayloadBytes      = db.P2P3PayloadMaxBytes
	maxErrorRunes        = 1024
)

var ErrManagerClosed = errors.New("automation manager is closed")

type ScheduleRunner interface {
	SubmitScheduleDispatch(context.Context, db.Schedule, string) (db.Run, error)
}

type TelegramSender interface {
	Send(context.Context, string, string, string) error
}

type Config struct {
	Store          *db.Store
	Runner         ScheduleRunner
	Audit          audit.Recorder
	HTTPClient     *http.Client
	TelegramSender TelegramSender
	PollInterval   time.Duration
	LeaseDuration  time.Duration
	Clock          func() time.Time
	OnError        func(error)
}

type WorkerStatus struct {
	Running            bool   `json:"running"`
	StartedAt          string `json:"startedAt,omitempty"`
	LastDeliveryPollAt string `json:"lastDeliveryPollAt,omitempty"`
	LastSchedulePollAt string `json:"lastSchedulePollAt,omitempty"`
	LastDeliveryError  string `json:"lastDeliveryError,omitempty"`
	LastSchedulerError string `json:"lastSchedulerError,omitempty"`
}

type TriggerResult struct {
	Run     db.Run `json:"run"`
	Outcome string `json:"outcome"`
}

type Manager struct {
	store         *db.Store
	runner        ScheduleRunner
	audit         audit.Recorder
	httpClient    *http.Client
	pollInterval  time.Duration
	leaseDuration time.Duration
	clock         func() time.Time
	onError       func(error)

	senderMu sync.RWMutex
	sender   TelegramSender

	mu      sync.RWMutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	status  WorkerStatus
	done    chan struct{}
	wg      sync.WaitGroup
}

var _ runtime.Service = (*Manager)(nil)
var _ agent.Notifier = (*Manager)(nil)

func NewManager(config Config) (*Manager, error) {
	if config.Store == nil || config.Runner == nil {
		return nil, errors.New("automation manager requires store and runner")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = defaultLeaseDuration
	}
	if config.Clock == nil {
		// db.LogicalNow, not time.Now: rows are written with db.Now(), which is
		// strictly increasing and so can sit slightly ahead of the wall clock on
		// a coarse-granularity platform. Claiming with time.Now() then compares
		// two different clocks and can skip a delivery that was just enqueued.
		config.Clock = db.LogicalNow
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	cloned.Timeout = webhookTimeout
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Manager{
		store: config.Store, runner: config.Runner, audit: config.Audit, httpClient: &cloned,
		pollInterval: config.PollInterval, leaseDuration: config.LeaseDuration, clock: config.Clock,
		onError: config.OnError, sender: config.TelegramSender, done: make(chan struct{}),
	}, nil
}

func New(store *db.Store, runner ScheduleRunner, recorder audit.Recorder) (*Manager, error) {
	return NewManager(Config{Store: store, Runner: runner, Audit: recorder})
}

func (m *Manager) SetTelegramSender(sender TelegramSender) {
	if m == nil {
		return
	}
	m.senderMu.Lock()
	m.sender = sender
	m.senderMu.Unlock()
}

func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("automation manager requires context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	if m.started {
		m.mu.Unlock()
		return errors.New("automation manager already started")
	}
	workerCtx, cancel := context.WithCancel(ctx)
	m.started = true
	m.cancel = cancel
	m.status.Running = true
	m.status.StartedAt = m.now().Format(time.RFC3339Nano)
	m.mu.Unlock()

	if err := m.initializeSchedules(workerCtx); err != nil {
		cancel()
		m.mu.Lock()
		m.started = false
		m.cancel = nil
		m.status.Running = false
		m.mu.Unlock()
		return err
	}
	m.wg.Add(2)
	go m.deliveryLoop(workerCtx)
	go m.scheduleLoop(workerCtx)
	go func() {
		m.wg.Wait()
		m.mu.Lock()
		m.status.Running = false
		m.mu.Unlock()
		close(m.done)
	}()
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("automation manager requires context")
	}
	m.mu.Lock()
	if m.closed {
		done := m.done
		m.mu.Unlock()
		return wait(ctx, done)
	}
	m.closed = true
	cancel := m.cancel
	started := m.started
	if !started {
		m.status.Running = false
		close(m.done)
	}
	done := m.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return wait(ctx, done)
}

func wait(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Status() WorkerStatus {
	if m == nil {
		return WorkerStatus{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) Notify(ctx context.Context, event agent.NotificationEvent) {
	if m == nil || m.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := notificationPayload(event, m.now())
	if err != nil {
		m.report(err)
		return
	}
	settings, err := m.store.GetNotificationSettings(ctx)
	if err != nil {
		m.report(errors.New("notification settings unavailable"))
		return
	}
	if shouldEnqueueWebhook(settings, event.Event) {
		_, _, err = m.store.EnqueueNotificationDelivery(ctx, db.NotificationDelivery{
			DedupeKey: deliveryDedupe("webhook", settings.WebhookURL, event), SinkType: "webhook", SinkID: strings.TrimSpace(settings.WebhookURL),
			EventType: event.Event, AgentID: event.AgentID, RunID: event.RunID, ToolUseID: event.ToolUseID, ExecutionGeneration: event.ExecutionGeneration, PayloadJSON: payload,
		})
		if err != nil {
			m.report(errors.New("webhook notification enqueue failed"))
		}
	}
	pairings, err := m.store.ListChannelPairings(ctx, db.ChannelPairingListOptions{Status: "active", Limit: db.P2P3MaxListLimit})
	if err != nil {
		m.report(errors.New("telegram notification pairing lookup failed"))
		return
	}
	for _, pairing := range pairings {
		if event.AgentID != "" && pairing.AgentID != event.AgentID {
			continue
		}
		_, _, enqueueErr := m.store.EnqueueNotificationDelivery(ctx, db.NotificationDelivery{
			DedupeKey: deliveryDedupe("telegram", pairing.ID, event), SinkType: "telegram", SinkID: pairing.ID,
			EventType: event.Event, AgentID: event.AgentID, RunID: event.RunID, ToolUseID: event.ToolUseID, ExecutionGeneration: event.ExecutionGeneration, PayloadJSON: payload,
		})
		if enqueueErr != nil {
			m.report(errors.New("telegram notification enqueue failed"))
		}
	}
}

func (m *Manager) EnqueueTest(ctx context.Context) (db.NotificationDelivery, error) {
	if m == nil || m.store == nil {
		return db.NotificationDelivery{}, ErrManagerClosed
	}
	settings, err := m.store.GetNotificationSettings(ctx)
	if err != nil {
		return db.NotificationDelivery{}, err
	}
	if strings.TrimSpace(settings.WebhookURL) == "" {
		return db.NotificationDelivery{}, errors.New("webhookUrl is required")
	}
	now := m.now()
	payload, err := json.Marshal(map[string]any{
		"kind": "notification.test", "event": "test", "status": "test", "createdAt": now.Format(time.RFC3339Nano),
		"meta": map[string]any{"source": "Autoto"},
	})
	if err != nil {
		return db.NotificationDelivery{}, err
	}
	delivery, _, err := m.store.EnqueueNotificationDelivery(ctx, db.NotificationDelivery{
		DedupeKey: "test:" + db.NewID(), SinkType: "webhook", SinkID: strings.TrimSpace(settings.WebhookURL),
		EventType: "test", PayloadJSON: payload,
	})
	return delivery, err
}

func (m *Manager) RetryDelivery(ctx context.Context, id string) (db.NotificationDelivery, error) {
	if m == nil || m.store == nil {
		return db.NotificationDelivery{}, ErrManagerClosed
	}
	id = strings.TrimSpace(id)
	now := m.now().Format(time.RFC3339Nano)
	result, err := m.store.DB().ExecContext(ctx, `UPDATE notification_deliveries SET status = 'queued', attempt_count = 0, next_attempt_at = ?, lease_until = NULL, last_http_status = NULL, last_error = NULL, delivered_at = NULL, updated_at = ? WHERE id = ? AND status IN ('dead','delivered')`, now, now, id)
	if err != nil {
		return db.NotificationDelivery{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return db.NotificationDelivery{}, err
	}
	if affected != 1 {
		if _, getErr := m.store.GetNotificationDelivery(ctx, id); getErr != nil {
			return db.NotificationDelivery{}, getErr
		}
		return db.NotificationDelivery{}, fmt.Errorf("%w: delivery is not retryable", db.ErrConflict)
	}
	delivery, err := m.store.GetNotificationDelivery(ctx, id)
	if err == nil {
		m.recordAudit(context.WithoutCancel(ctx), audit.Event{Category: "notification", Action: "delivery.retry", Actor: "local-api", SubjectType: "notification_delivery", SubjectID: id, Outcome: "success", Risk: "low", Details: map[string]any{"sinkType": delivery.SinkType, "eventType": delivery.EventType}})
	}
	return delivery, err
}

func (m *Manager) ProcessDeliveriesOnce(ctx context.Context) error {
	now := m.now()
	claimed, err := m.store.ClaimNotificationDeliveries(ctx, now.Format(time.RFC3339Nano), now.Add(m.leaseDuration).Format(time.RFC3339Nano), 20)
	if err != nil {
		return err
	}
	for _, delivery := range claimed {
		m.processDelivery(ctx, delivery)
	}
	return nil
}

func (m *Manager) deliveryLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		m.setDeliveryPoll("")
		if err := m.ProcessDeliveriesOnce(ctx); err != nil && ctx.Err() == nil {
			m.setDeliveryPoll("delivery worker failed")
			m.report(errors.New("notification delivery worker failed"))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) processDelivery(ctx context.Context, delivery db.NotificationDelivery) {
	status, retryable, err := m.deliver(ctx, delivery)
	persistCtx := context.Background()
	if err == nil {
		if status == 0 {
			status = http.StatusOK
		}
		if markErr := m.store.MarkNotificationDeliveryDelivered(persistCtx, delivery.ID, status); markErr != nil {
			m.report(errors.New("notification delivery completion persistence failed"))
		}
		return
	}
	message := safeError(err)
	if retryable && delivery.AttemptCount < delivery.MaxAttempts {
		next := m.now().Add(deliveryBackoff(delivery.AttemptCount)).Format(time.RFC3339Nano)
		if markErr := m.store.MarkNotificationDeliveryRetry(persistCtx, delivery.ID, status, message, next); markErr != nil {
			m.report(errors.New("notification retry persistence failed"))
		}
		return
	}
	if markErr := m.store.MarkNotificationDeliveryDead(persistCtx, delivery.ID, status, message); markErr != nil {
		m.report(errors.New("notification dead-letter persistence failed"))
	}
}

func (m *Manager) deliver(ctx context.Context, delivery db.NotificationDelivery) (int, bool, error) {
	switch delivery.SinkType {
	case "webhook":
		return m.deliverWebhook(ctx, delivery)
	case "telegram":
		return m.deliverTelegram(ctx, delivery)
	default:
		return 0, false, errors.New("unsupported delivery sink")
	}
}

func (m *Manager) deliverWebhook(ctx context.Context, delivery db.NotificationDelivery) (int, bool, error) {
	requestCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, delivery.SinkID, bytes.NewReader(delivery.PayloadJSON))
	if err != nil {
		return 0, false, errors.New("invalid webhook destination")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Autoto-Webhook/2.0")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return 0, true, errors.New("webhook request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxWebhookResponse))
	status := response.StatusCode
	if status >= 200 && status < 300 {
		return status, false, nil
	}
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
		return status, true, fmt.Errorf("webhook returned retryable HTTP status %d", status)
	}
	return status, false, fmt.Errorf("webhook returned HTTP status %d", status)
}

func (m *Manager) deliverTelegram(ctx context.Context, delivery db.NotificationDelivery) (int, bool, error) {
	pairing, err := m.store.GetChannelPairing(ctx, delivery.SinkID)
	if err != nil || pairing.Status != "active" {
		return 0, false, errors.New("telegram pairing is unavailable")
	}
	m.senderMu.RLock()
	sender := m.sender
	m.senderMu.RUnlock()
	if sender == nil {
		return 0, true, errors.New("telegram sender is unavailable")
	}
	if err := sender.Send(ctx, pairing.ConnectionID, pairing.ChatID, telegramText(delivery.PayloadJSON)); err != nil {
		return 0, true, errors.New("telegram delivery failed")
	}
	return http.StatusOK, false, nil
}

func deliveryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second
	for index := 1; index < attempt; index++ {
		delay *= 2
		if delay >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return delay
}

func (m *Manager) initializeSchedules(ctx context.Context) error {
	items, err := m.store.ListSchedules(ctx, db.ScheduleListOptions{Limit: db.P2P3MaxListLimit})
	if err != nil {
		return err
	}
	for _, schedule := range items {
		if !schedule.Enabled || schedule.NextRunAt != "" {
			continue
		}
		expression, parseErr := schedules.Parse(schedule.Expression, schedule.Timezone)
		if parseErr != nil {
			continue
		}
		next, nextErr := expression.Next(m.now())
		if nextErr != nil {
			continue
		}
		schedule.NextRunAt = next.UTC().Format(time.RFC3339Nano)
		_, _ = m.store.UpdateSchedule(ctx, schedule)
	}
	return nil
}

func (m *Manager) scheduleLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		m.setSchedulePoll("")
		if err := m.ProcessSchedulesOnce(ctx); err != nil && ctx.Err() == nil {
			m.setSchedulePoll("schedule worker failed")
			m.report(errors.New("schedule worker failed"))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) ProcessSchedulesOnce(ctx context.Context) error {
	now := m.now()
	claimed, err := m.store.ClaimDueSchedules(ctx, now.Format(time.RFC3339Nano), now.Add(m.leaseDuration).Format(time.RFC3339Nano), 20)
	if err != nil {
		return err
	}
	for _, schedule := range claimed {
		_, _ = m.processSchedule(ctx, schedule)
	}
	return nil
}

func (m *Manager) TriggerSchedule(ctx context.Context, id string) (TriggerResult, error) {
	if m == nil || m.store == nil {
		return TriggerResult{}, ErrManagerClosed
	}
	now := m.now()
	leaseUntil := now.Add(m.leaseDuration).Format(time.RFC3339Nano)
	result, err := m.store.DB().ExecContext(ctx, `UPDATE schedules SET lease_until = ?, updated_at = ? WHERE id = ? AND (lease_until IS NULL OR lease_until <= ?)`, leaseUntil, now.Format(time.RFC3339Nano), strings.TrimSpace(id), now.Format(time.RFC3339Nano))
	if err != nil {
		return TriggerResult{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return TriggerResult{}, err
	}
	if affected != 1 {
		if _, getErr := m.store.GetSchedule(ctx, id); getErr != nil {
			return TriggerResult{}, getErr
		}
		return TriggerResult{Outcome: "skipped"}, fmt.Errorf("%w: schedule is already running", db.ErrConflict)
	}
	schedule, err := m.store.GetSchedule(ctx, id)
	if err != nil {
		return TriggerResult{}, err
	}
	return m.processSchedule(ctx, schedule)
}

func (m *Manager) processSchedule(ctx context.Context, schedule db.Schedule) (TriggerResult, error) {
	dispatchID, dispatchErr := scheduleDispatchID(schedule)
	if dispatchErr != nil {
		_ = m.store.ReleaseScheduleLease(context.Background(), schedule.ID, schedule.LeaseUntil)
		return TriggerResult{Outcome: "error"}, dispatchErr
	}

	expression, parseErr := schedules.Parse(schedule.Expression, schedule.Timezone)
	nextRunAt := ""
	if parseErr == nil {
		if next, nextErr := expression.Next(m.now()); nextErr == nil {
			nextRunAt = next.UTC().Format(time.RFC3339Nano)
		} else {
			parseErr = nextErr
		}
	}
	if parseErr != nil {
		run, createErr := m.terminalScheduleRun(context.Background(), schedule, schedule.AgentID, dispatchID, "error", safeError(parseErr))
		if createErr != nil {
			_ = m.store.ReleaseScheduleLease(context.Background(), schedule.ID, schedule.LeaseUntil)
			return TriggerResult{Outcome: "error"}, createErr
		}
		_, recordErr := m.store.RecordScheduleRun(context.Background(), schedule.ID, schedule.LeaseUntil, run.ID, "error", safeError(parseErr), "")
		m.auditSchedule(schedule, run, "error", safeError(parseErr))
		if recordErr != nil {
			return TriggerResult{Run: run, Outcome: "error"}, recordErr
		}
		return TriggerResult{Run: run, Outcome: "error"}, parseErr
	}

	dispatchedSchedule, prepareErr := m.prepareScheduleDispatch(ctx, schedule)
	if prepareErr != nil {
		lastError := safeError(prepareErr)
		run, createErr := m.terminalScheduleRun(context.Background(), schedule, schedule.AgentID, dispatchID, "error", lastError)
		if createErr != nil {
			_ = m.store.ReleaseScheduleLease(context.Background(), schedule.ID, schedule.LeaseUntil)
			return TriggerResult{Outcome: "failure"}, errors.Join(prepareErr, createErr)
		}
		_, recordErr := m.store.RecordScheduleRun(context.Background(), schedule.ID, schedule.LeaseUntil, run.ID, "failure", lastError, nextRunAt)
		m.auditSchedule(schedule, run, "failure", lastError)
		if recordErr != nil {
			return TriggerResult{Run: run, Outcome: "failure"}, errors.Join(prepareErr, recordErr)
		}
		return TriggerResult{Run: run, Outcome: "failure"}, prepareErr
	}

	run, err := m.runner.SubmitScheduleDispatch(ctx, dispatchedSchedule, dispatchID)
	outcome := "success"
	lastError := ""
	if errors.Is(err, agent.ErrAgentBusy) {
		outcome = "skipped"
		lastError = agent.ErrAgentBusy.Error()
		run, err = m.terminalScheduleRun(context.Background(), schedule, dispatchedSchedule.AgentID, dispatchID, "skipped", lastError)
		if err == nil {
			err = agent.ErrAgentBusy
		}
	} else if err != nil {
		outcome = "failure"
		lastError = safeError(err)
		failedRun, createErr := m.terminalScheduleRun(context.Background(), schedule, dispatchedSchedule.AgentID, dispatchID, "error", lastError)
		if createErr == nil {
			run = failedRun
		} else {
			_ = m.store.ReleaseScheduleLease(context.Background(), schedule.ID, schedule.LeaseUntil)
			return TriggerResult{Outcome: outcome}, errors.Join(err, createErr)
		}
	}
	if run.ID == "" {
		_ = m.store.ReleaseScheduleLease(context.Background(), schedule.ID, schedule.LeaseUntil)
		return TriggerResult{Outcome: outcome}, errors.New("schedule submission did not create a run")
	}
	_, recordErr := m.store.RecordScheduleRun(context.Background(), schedule.ID, schedule.LeaseUntil, run.ID, outcome, lastError, nextRunAt)
	m.auditSchedule(schedule, run, outcome, lastError)
	if recordErr != nil {
		return TriggerResult{Run: run, Outcome: outcome}, errors.Join(err, recordErr)
	}
	return TriggerResult{Run: run, Outcome: outcome}, err
}

func scheduleDispatchID(schedule db.Schedule) (string, error) {
	id := strings.TrimSpace(schedule.ID)
	leaseUntil := strings.TrimSpace(schedule.LeaseUntil)
	if id == "" || leaseUntil == "" {
		return "", errors.New("schedule dispatch requires schedule id and lease")
	}
	hash := sha256.Sum256([]byte(id + "\x00" + leaseUntil))
	return "schedule:" + hex.EncodeToString(hash[:]), nil
}

func (m *Manager) prepareScheduleDispatch(ctx context.Context, schedule db.Schedule) (db.Schedule, error) {
	if schedule.EnvironmentMode != "workline" && schedule.EnvironmentMode != "standalone" {
		return db.Schedule{}, errors.New("invalid schedule environment mode")
	}
	if schedule.NarratorMode != "reuse" && schedule.NarratorMode != "new" {
		return db.Schedule{}, errors.New("invalid schedule narrator mode")
	}
	template, err := m.store.GetAgent(ctx, schedule.AgentID)
	if err != nil {
		return db.Schedule{}, err
	}
	if schedule.NarratorMode == "reuse" {
		if schedule.EnvironmentMode == "workline" && template.WorklineID == "" {
			return db.Schedule{}, fmt.Errorf("%w: reusable agent is not attached to a workline", db.ErrConflict)
		}
		if schedule.EnvironmentMode == "standalone" && template.WorklineID != "" {
			return db.Schedule{}, fmt.Errorf("%w: reusable agent is attached to a workline", db.ErrConflict)
		}
		return schedule, nil
	}

	worklineID := template.WorklineID
	if schedule.EnvironmentMode == "workline" && worklineID == "" {
		return db.Schedule{}, fmt.Errorf("%w: template agent is not attached to a workline", db.ErrConflict)
	}
	if schedule.EnvironmentMode == "standalone" {
		worklineID = ""
	}
	title := strings.TrimSpace(schedule.Name)
	if title == "" {
		title = template.Title
	}
	created, err := m.store.CreateAgent(ctx, db.Agent{
		WorklineID:        worklineID,
		ParentAgentID:     template.ID,
		Type:              "subagent",
		SubagentType:      "scheduled",
		Title:             title,
		Model:             template.Model,
		PermissionMode:    template.PermissionMode,
		ReasoningEffort:   template.ReasoningEffort,
		ExecutionDeviceID: template.ExecutionDeviceID,
		Status:            "idle",
		CWD:               template.CWD,
	})
	if err != nil {
		return db.Schedule{}, err
	}
	schedule.AgentID = created.ID
	return schedule, nil
}

func (m *Manager) terminalScheduleRun(ctx context.Context, schedule db.Schedule, agentID, dispatchID, status, message string) (db.Run, error) {
	if existing, found, err := m.scheduleRunForDispatch(ctx, schedule.ID, dispatchID); err != nil {
		return db.Run{}, err
	} else if found {
		return existing, nil
	}
	now := m.now().Format(time.RFC3339Nano)
	return m.store.CreateRun(ctx, db.Run{
		AgentID: agentID, Status: status, CompletedAt: now, ErrorMessage: safeErrorText(message),
		Source: "schedule", SourceID: schedule.ID, PermissionModeCap: schedule.PermissionMode,
		DispatchID: dispatchID, TriggerType: "scheduled",
	})
}

func (m *Manager) scheduleRunForDispatch(ctx context.Context, scheduleID, dispatchID string) (db.Run, bool, error) {
	var runID, agentID, sourceID string
	err := m.store.DB().QueryRowContext(ctx, `SELECT id, agent_id, source_id FROM runs WHERE dispatch_id = ?`, dispatchID).Scan(&runID, &agentID, &sourceID)
	if db.IsNotFound(err) {
		return db.Run{}, false, nil
	}
	if err != nil {
		return db.Run{}, false, err
	}
	if sourceID != scheduleID {
		return db.Run{}, false, fmt.Errorf("%w: dispatch id belongs to another schedule", db.ErrConflict)
	}
	run, err := m.store.GetRun(ctx, agentID, runID)
	if err != nil {
		return db.Run{}, false, err
	}
	return run, true, nil
}

func (m *Manager) auditSchedule(schedule db.Schedule, run db.Run, outcome, lastError string) {
	details := map[string]any{
		"outcome": outcome, "permissionModeCap": schedule.PermissionMode,
		"environmentMode": schedule.EnvironmentMode, "narratorMode": schedule.NarratorMode,
		"templateAgentId": schedule.AgentID, "dispatchId": run.DispatchID,
	}
	if lastError != "" {
		details["errorClass"] = "schedule_submission"
	}
	m.recordAudit(context.Background(), audit.Event{
		Category: "schedule", Action: "schedule.run", Actor: "scheduler", AgentID: run.AgentID, RunID: run.ID,
		SubjectType: "schedule", SubjectID: schedule.ID, Outcome: auditOutcome(outcome), Risk: "low", Details: details,
	})
}

func auditOutcome(outcome string) string {
	switch outcome {
	case "success":
		return "success"
	case "skipped":
		return "denied"
	case "failure":
		return "failure"
	default:
		return "error"
	}
}

func (m *Manager) recordAudit(ctx context.Context, event audit.Event) {
	if m.audit == nil {
		return
	}
	if err := m.audit.Record(ctx, event); err != nil {
		m.report(errors.New("automation audit persistence failed"))
	}
}

func notificationPayload(event agent.NotificationEvent, now time.Time) (json.RawMessage, error) {
	payload := map[string]any{
		"kind": "run." + notificationKind(event.Event), "event": boundedText(event.Event, 96),
		"runId": boundedText(event.RunID, 128), "agentId": boundedText(event.AgentID, 128),
		"status": boundedText(event.Status, 96), "createdAt": now.UTC().Format(time.RFC3339Nano),
		"executionGeneration": event.ExecutionGeneration,
	}
	if event.TaskID != "" {
		payload["taskId"] = boundedText(event.TaskID, 128)
	}
	if event.ToolUseID != "" || event.ToolName != "" {
		payload["tool"] = map[string]any{"toolUseId": boundedText(event.ToolUseID, 256), "toolName": boundedText(event.ToolName, 128)}
	}
	if event.Error != "" {
		payload["errorMessage"] = "run failed; open Autoto for details"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxPayloadBytes {
		delete(payload, "errorMessage")
		encoded, err = json.Marshal(payload)
	}
	if err != nil || len(encoded) > maxPayloadBytes {
		return nil, errors.New("notification payload exceeds size limit")
	}
	return append(json.RawMessage(nil), encoded...), nil
}

func notificationKind(event string) string {
	switch event {
	case "approval_required", "completed", "error", "interrupted", "superseded", "task_terminal", "continuation_blocked", "budget_exhausted":
		return event
	default:
		return "event"
	}
}

func shouldEnqueueWebhook(settings db.NotificationSettings, event string) bool {
	if !settings.Enabled || strings.TrimSpace(settings.WebhookURL) == "" {
		return false
	}
	switch event {
	case "approval_required":
		return settings.NotifyOnApproval
	case "completed", "interrupted", "superseded", "task_terminal":
		return settings.NotifyOnDone
	case "error", "continuation_blocked", "budget_exhausted":
		return settings.NotifyOnError
	default:
		return false
	}
}

func deliveryDedupe(sinkType, sinkID string, event agent.NotificationEvent) string {
	version := "notification:v1"
	identity := event.RunID
	if event.ExecutionGeneration > 0 {
		version = "notification:v2"
		identity = fmt.Sprintf("%d", event.ExecutionGeneration)
	}
	value := strings.Join([]string{version, sinkType, sinkID, event.AgentID, identity, event.Event, event.TaskID, event.ToolUseID}, "\x00")
	hash := sha256.Sum256([]byte(value))
	return sinkType + ":" + hex.EncodeToString(hash[:])
}

func telegramText(raw json.RawMessage) string {
	var payload struct {
		Event   string `json:"event"`
		AgentID string `json:"agentId"`
		RunID   string `json:"runId"`
		Status  string `json:"status"`
		Error   string `json:"errorMessage"`
	}
	_ = json.Unmarshal(raw, &payload)
	parts := []string{"Autoto event: " + boundedText(payload.Event, 96)}
	if payload.Status != "" {
		parts = append(parts, "Status: "+boundedText(payload.Status, 96))
	}
	if payload.AgentID != "" {
		parts = append(parts, "Agent: "+boundedText(payload.AgentID, 128))
	}
	if payload.RunID != "" {
		parts = append(parts, "Run: "+boundedText(payload.RunID, 128))
	}
	if payload.Error != "" {
		parts = append(parts, "Error: "+safeErrorText(payload.Error))
	}
	return boundedText(strings.Join(parts, "\n"), 3500)
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return safeErrorText(err.Error())
}

func safeErrorText(value string) string {
	value = strings.TrimSpace(value)
	for _, marker := range []string{"Authorization:", "Bearer ", "bot", "token=", "access_token="} {
		if index := strings.Index(strings.ToLower(value), strings.ToLower(marker)); index >= 0 {
			value = value[:index] + "[redacted]"
		}
	}
	return boundedText(value, maxErrorRunes)
}

func boundedText(value string, maximum int) string {
	value = strings.Map(func(char rune) rune {
		if char == 0 || char == '\r' || char == '\n' || char == '\t' {
			if char == '\n' || char == '\t' {
				return ' '
			}
			return -1
		}
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, strings.TrimSpace(value))
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	if maximum <= 1 {
		return string(runes[:maximum])
	}
	return string(runes[:maximum-1]) + "…"
}

func (m *Manager) setDeliveryPoll(lastError string) {
	m.mu.Lock()
	m.status.LastDeliveryPollAt = m.now().Format(time.RFC3339Nano)
	m.status.LastDeliveryError = lastError
	m.mu.Unlock()
}

func (m *Manager) setSchedulePoll(lastError string) {
	m.mu.Lock()
	m.status.LastSchedulePollAt = m.now().Format(time.RFC3339Nano)
	m.status.LastSchedulerError = lastError
	m.mu.Unlock()
}

func (m *Manager) report(err error) {
	if err == nil {
		return
	}
	if m.onError != nil {
		m.onError(err)
		return
	}
	slog.Warn("automation worker", "error", err)
}

func (m *Manager) now() time.Time {
	return m.clock().UTC()
}
