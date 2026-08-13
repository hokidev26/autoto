package background

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"autoto/internal/db"
	"autoto/internal/runtime"
	"autoto/internal/tools"
)

type managerState uint8

const (
	managerNew managerState = iota
	managerRunning
	managerClosing
	managerClosed
)

type activeTask struct {
	cancel context.CancelFunc
}

type Manager struct {
	store   *db.Store
	options Options

	mu           sync.Mutex
	state        managerState
	ctx          context.Context
	cancel       context.CancelFunc
	executors    map[string]Executor
	validator    TaskValidator
	eventHook    TaskEventHook
	terminalHook TerminalHook
	active       map[string]activeTask
	waiters      map[string]chan struct{}

	claimMu        sync.Mutex
	runningTotal   int
	runningByAgent map[string]int
	startedWorkers int
	wake           chan struct{}
	workers        sync.WaitGroup
}

var _ runtime.Service = (*Manager)(nil)

func NewManager(store *db.Store, options Options) *Manager {
	options = options.withDefaults()
	if strings.TrimSpace(options.WorkerInstanceID) == "" {
		options.WorkerInstanceID = db.NewID()
	}
	return &Manager{
		store:          store,
		options:        options,
		executors:      make(map[string]Executor),
		active:         make(map[string]activeTask),
		waiters:        make(map[string]chan struct{}),
		runningByAgent: make(map[string]int),
		wake:           make(chan struct{}, 1),
	}
}

func (manager *Manager) RegisterExecutor(kind string, executor Executor) error {
	kind = strings.TrimSpace(kind)
	if executor == nil {
		return ErrNilExecutor
	}
	if kind == "" {
		return errors.New("background executor kind is required")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.state == managerClosing || manager.state == managerClosed {
		return ErrClosed
	}
	if _, exists := manager.executors[kind]; exists {
		return fmt.Errorf("%w: %s", ErrExecutorExists, kind)
	}
	manager.executors[kind] = executor
	return nil
}

func (manager *Manager) SetTerminalHook(hook TerminalHook) {
	manager.mu.Lock()
	manager.terminalHook = hook
	manager.mu.Unlock()
}

func (manager *Manager) SetValidator(validator TaskValidator) {
	manager.mu.Lock()
	manager.validator = validator
	manager.mu.Unlock()
}

func (manager *Manager) SetEventHook(hook TaskEventHook) {
	manager.mu.Lock()
	manager.eventHook = hook
	manager.mu.Unlock()
}

func (manager *Manager) Start(ctx context.Context) error {
	manager.mu.Lock()
	if manager.state != managerNew {
		manager.mu.Unlock()
		return ErrAlreadyStarted
	}
	if manager.store == nil {
		manager.mu.Unlock()
		return errors.New("background manager store is nil")
	}
	root, cancel := context.WithCancel(context.Background())
	manager.ctx = root
	manager.cancel = cancel
	manager.mu.Unlock()

	if _, err := manager.store.ReconcileBackgroundTasksAfterRestart(ctx, manager.options.WorkerInstanceID); err != nil {
		cancel()
		return fmt.Errorf("reconcile background tasks: %w", err)
	}

	manager.mu.Lock()
	if manager.state != managerNew {
		manager.mu.Unlock()
		cancel()
		return ErrAlreadyStarted
	}
	manager.state = managerRunning
	manager.startWorkersLocked(root, manager.options.WorkerCount)
	manager.mu.Unlock()
	manager.signalWake()
	return nil
}

func (manager *Manager) startWorkersLocked(ctx context.Context, target int) {
	if target <= manager.startedWorkers {
		return
	}
	for worker := manager.startedWorkers; worker < target; worker++ {
		manager.workers.Add(1)
		go manager.worker(ctx)
	}
	manager.startedWorkers = target
}

func validateRuntimeSettings(settings tools.BackgroundRuntimeSettings) error {
	if settings.WorkerCount < 1 || settings.WorkerCount > 16 {
		return errors.New("workerCount must be between 1 and 16")
	}
	if settings.PerAgentLimit < 1 || settings.PerAgentLimit > 8 {
		return errors.New("perAgentLimit must be between 1 and 8")
	}
	if settings.MaxSubagentDepth < 2 || settings.MaxSubagentDepth > 4 {
		return errors.New("maxSubagentDepth must be between 2 and 4")
	}
	return nil
}

func (manager *Manager) BackgroundRuntimeSettings() tools.BackgroundRuntimeSettings {
	if manager == nil {
		return tools.BackgroundRuntimeSettings{WorkerCount: 8, PerAgentLimit: 4, MaxSubagentDepth: 2}
	}
	manager.claimMu.Lock()
	defer manager.claimMu.Unlock()
	return tools.BackgroundRuntimeSettings{
		WorkerCount:          manager.options.WorkerCount,
		PerAgentLimit:        manager.options.PerAgentLimit,
		AllowNestedSubagents: manager.options.AllowNestedSubagents,
		MaxSubagentDepth:     manager.options.MaxSubagentDepth,
	}
}

func (manager *Manager) UpdateBackgroundRuntimeSettings(settings tools.BackgroundRuntimeSettings) (tools.BackgroundRuntimeSettings, error) {
	if manager == nil {
		return tools.BackgroundRuntimeSettings{}, ErrClosed
	}
	if err := validateRuntimeSettings(settings); err != nil {
		return tools.BackgroundRuntimeSettings{}, err
	}

	manager.mu.Lock()
	if manager.state == managerClosing || manager.state == managerClosed {
		manager.mu.Unlock()
		return tools.BackgroundRuntimeSettings{}, ErrClosed
	}
	ctx := manager.ctx
	state := manager.state
	manager.claimMu.Lock()
	manager.options.WorkerCount = settings.WorkerCount
	manager.options.PerAgentLimit = settings.PerAgentLimit
	manager.options.AllowNestedSubagents = settings.AllowNestedSubagents
	manager.options.MaxSubagentDepth = settings.MaxSubagentDepth
	manager.claimMu.Unlock()
	if state == managerRunning {
		manager.startWorkersLocked(ctx, settings.WorkerCount)
	}
	manager.mu.Unlock()
	manager.signalWake()
	return manager.BackgroundRuntimeSettings(), nil
}

func (manager *Manager) Close(ctx context.Context) error {
	manager.mu.Lock()
	switch manager.state {
	case managerClosed:
		manager.mu.Unlock()
		return nil
	case managerNew:
		manager.state = managerClosed
		manager.mu.Unlock()
		return nil
	case managerClosing:
		manager.mu.Unlock()
		return manager.waitWorkers(ctx)
	}
	manager.state = managerClosing
	cancel := manager.cancel
	active := make([]context.CancelFunc, 0, len(manager.active))
	for _, task := range manager.active {
		active = append(active, task.cancel)
	}
	manager.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, cancelTask := range active {
		cancelTask()
	}
	if err := manager.waitWorkers(ctx); err != nil {
		return err
	}
	manager.mu.Lock()
	manager.state = managerClosed
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) waitWorkers(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		manager.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) Submit(ctx context.Context, task db.BackgroundTask) (db.BackgroundTask, error) {
	manager.mu.Lock()
	closed := manager.state == managerClosing || manager.state == managerClosed
	manager.mu.Unlock()
	if closed {
		return db.BackgroundTask{}, ErrClosed
	}
	created, err := manager.store.CreateBackgroundTask(ctx, task)
	if err != nil {
		return db.BackgroundTask{}, err
	}
	manager.notifyEvent("created", created)
	manager.signalWake()
	return created, nil
}

func (manager *Manager) Get(ctx context.Context, taskID string) (db.BackgroundTask, error) {
	return manager.store.GetBackgroundTask(ctx, taskID)
}

func (manager *Manager) List(ctx context.Context, options db.BackgroundTaskListOptions) ([]db.BackgroundTask, error) {
	return manager.store.ListBackgroundTasks(ctx, options)
}

func (manager *Manager) ListOutput(ctx context.Context, taskID string, afterSequence int64, byteLimit int) (db.BackgroundTaskOutputPage, error) {
	return manager.store.ListBackgroundTaskOutput(ctx, taskID, afterSequence, byteLimit)
}

func (manager *Manager) Wait(ctx context.Context, taskID string) (db.BackgroundTask, error) {
	for {
		// Register the waiter before reading the status. A task that reaches
		// a terminal state between a read and a later registration has
		// already fired its one notify, so a waiter registered afterwards
		// would only wake when the fallback timer elapses -- tolerable at
		// 100ms, but the fallback is now deliberately long.
		signal := manager.waitSignal(taskID)
		task, err := manager.store.GetBackgroundTask(ctx, taskID)
		if err != nil {
			manager.notify(taskID)
			return db.BackgroundTask{}, err
		}
		if task.Terminal() {
			// A terminal task receives no further notifies, so drop the
			// waiter entry registered above instead of leaking it. Concurrent
			// waiters woken by this re-check the status and return too.
			manager.notify(taskID)
			return task, nil
		}
		timer := time.NewTimer(manager.options.PollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return db.BackgroundTask{}, ctx.Err()
		case <-signal:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (manager *Manager) Cancel(ctx context.Context, taskID string) (db.BackgroundTask, error) {
	task, err := manager.store.RequestBackgroundTaskCancel(ctx, taskID)
	if err != nil {
		return db.BackgroundTask{}, err
	}
	manager.mu.Lock()
	active, running := manager.active[taskID]
	manager.mu.Unlock()
	if running {
		active.cancel()
	}
	if task.Terminal() {
		manager.notifyTerminal(task)
	} else {
		manager.notifyEvent("status", task)
		manager.notify(task.ID)
	}
	return task, nil
}

func (manager *Manager) worker(ctx context.Context) {
	defer manager.workers.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		task, err := manager.claim(ctx)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if !manager.waitForWork(ctx) {
					return
				}
				continue
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			if !manager.waitForWork(ctx) {
				return
			}
			continue
		}
		// The wake channel holds at most one pending signal, so a burst of
		// submissions can collapse into a single wakeup. Re-arm it after a
		// successful claim so sibling workers keep draining the queue instead
		// of sitting out the long fallback timer in waitForWork.
		manager.signalWake()
		manager.notifyEvent("status", task)
		manager.execute(ctx, task)
	}
}

func (manager *Manager) claim(ctx context.Context) (db.BackgroundTask, error) {
	manager.claimMu.Lock()
	defer manager.claimMu.Unlock()
	if manager.runningTotal >= manager.options.WorkerCount {
		return db.BackgroundTask{}, sql.ErrNoRows
	}
	excluded := make([]string, 0)
	for agentID, count := range manager.runningByAgent {
		if count >= manager.options.PerAgentLimit {
			excluded = append(excluded, agentID)
		}
	}
	task, err := manager.store.ClaimQueuedBackgroundTask(ctx, db.BackgroundTaskClaimOptions{
		WorkerInstanceID:     manager.options.WorkerInstanceID,
		ExcludeOwnerAgentIDs: excluded,
	})
	if err != nil {
		return db.BackgroundTask{}, err
	}
	manager.runningTotal++
	manager.runningByAgent[task.OwnerAgentID]++
	return task, nil
}

func (manager *Manager) execute(parent context.Context, claimed db.BackgroundTask) {
	ctx, cancel := context.WithCancel(parent)
	manager.mu.Lock()
	manager.active[claimed.ID] = activeTask{cancel: cancel}
	executor := manager.executors[claimed.Kind]
	validator := manager.validator
	manager.mu.Unlock()
	defer func() {
		cancel()
		manager.mu.Lock()
		delete(manager.active, claimed.ID)
		manager.mu.Unlock()
		manager.claimMu.Lock()
		if manager.runningTotal > 0 {
			manager.runningTotal--
		}
		manager.runningByAgent[claimed.OwnerAgentID]--
		if manager.runningByAgent[claimed.OwnerAgentID] <= 0 {
			delete(manager.runningByAgent, claimed.OwnerAgentID)
		}
		manager.claimMu.Unlock()
		manager.signalWake()
	}()

	outputTask := claimed
	output := newPersistentOutputWriter(ctx, manager.store, claimed.ID, manager.options.OutputLimitBytes, manager.options.OutputChunkBytes, func(appended db.BackgroundTaskOutputAppendResult) {
		outputTask.LastOutputSequence = appended.LastSequence
		outputTask.OutputBytes = appended.OutputBytes
		outputTask.OutputTruncated = appended.Truncated
		manager.notifyEvent("output", outputTask)
	})
	var result Result
	var executeErr error
	if validator != nil {
		if err := validator(ctx, claimed); err != nil {
			executeErr = fmt.Errorf("background task safety validation failed: %w", err)
			result.ErrorCode = "safety_snapshot_invalid"
			if coded, ok := err.(interface{ ErrorCode() string }); ok {
				if code := strings.TrimSpace(coded.ErrorCode()); code != "" {
					result.ErrorCode = code
				}
			}
		}
	}
	if executeErr == nil && executor == nil {
		executeErr = fmt.Errorf("%w: %s", ErrUnknownExecutor, claimed.Kind)
		result.ErrorCode = "executor_not_registered"
	} else if executeErr == nil {
		result, executeErr = executor.Execute(ctx, claimed, output)
	}
	manager.finish(claimed.ID, result, executeErr, parent.Err() != nil)
}

func (manager *Manager) finish(taskID string, result Result, executeErr error, shuttingDown bool) {
	ctx := context.Background()
	current, err := manager.store.GetBackgroundTaskForExecution(ctx, taskID)
	if err != nil {
		manager.notify(taskID)
		return
	}
	if current.Terminal() {
		manager.notifyTerminal(current)
		return
	}
	status := db.BackgroundTaskStatusSucceeded
	errorCode := strings.TrimSpace(result.ErrorCode)
	errorMessage := ""
	if current.Status == db.BackgroundTaskStatusCancelRequested {
		status = db.BackgroundTaskStatusCanceled
		errorCode = "canceled"
		errorMessage = "background task canceled"
	} else if shuttingDown {
		status = db.BackgroundTaskStatusInterrupted
		errorCode = "manager_shutdown"
		errorMessage = "background manager stopped"
	} else if executeErr != nil {
		status = db.BackgroundTaskStatusFailed
		if errorCode == "" {
			errorCode = "executor_error"
		}
		errorMessage = truncateUTF8(executeErr.Error(), 4096)
	}
	resultJSON, normalizeErr := boundedResultJSON(result.JSON)
	if normalizeErr != nil {
		status = db.BackgroundTaskStatusFailed
		errorCode = "invalid_result"
		errorMessage = normalizeErr.Error()
		resultJSON = json.RawMessage(`{}`)
	}
	updated, err := manager.store.TransitionBackgroundTask(ctx, taskID, db.BackgroundTaskTransition{
		ExpectedRevision: current.Revision,
		FromStatuses:     []string{db.BackgroundTaskStatusRunning, db.BackgroundTaskStatusCancelRequested},
		Status:           status,
		ResultJSON:       resultJSON,
		ErrorCode:        truncateUTF8(errorCode, 128),
		ErrorMessage:     truncateUTF8(errorMessage, 4096),
		ExitCode:         result.ExitCode,
	})
	if err != nil {
		if latest, getErr := manager.store.GetBackgroundTask(ctx, taskID); getErr == nil && latest.Terminal() {
			manager.notifyTerminal(latest)
			return
		}
		manager.notify(taskID)
		return
	}
	manager.notifyTerminal(updated)
}

// waitForWork parks an idle worker until signalWake fires. The timer is only a
// safety net for a lost wakeup (see the PollInterval default in types.go), so
// its length costs latency only when the event path has already failed.
func (manager *Manager) waitForWork(ctx context.Context) bool {
	timer := time.NewTimer(manager.options.PollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-manager.wake:
		return true
	case <-timer.C:
		return true
	}
}

func (manager *Manager) signalWake() {
	select {
	case manager.wake <- struct{}{}:
	default:
	}
}

func (manager *Manager) waitSignal(taskID string) <-chan struct{} {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	signal := manager.waiters[taskID]
	if signal == nil {
		signal = make(chan struct{})
		manager.waiters[taskID] = signal
	}
	return signal
}

func (manager *Manager) notify(taskID string) {
	manager.mu.Lock()
	if signal := manager.waiters[taskID]; signal != nil {
		close(signal)
		delete(manager.waiters, taskID)
	}
	manager.mu.Unlock()
}

func (manager *Manager) notifyEvent(event string, task db.BackgroundTask) {
	manager.mu.Lock()
	hook := manager.eventHook
	manager.mu.Unlock()
	if hook == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		hook(context.Background(), event, task)
	}()
}

func (manager *Manager) notifyTerminal(task db.BackgroundTask) {
	manager.notify(task.ID)
	manager.notifyEvent("completed", task)
	manager.mu.Lock()
	hook := manager.terminalHook
	manager.mu.Unlock()
	if hook == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		hook(context.Background(), task)
	}()
}

func boundedResultJSON(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(trimmed) > 32768 || !json.Valid(trimmed) {
		return nil, errors.New("background executor result must be valid JSON no larger than 32768 bytes")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("background executor result must be valid JSON")
	}
	switch value.(type) {
	case map[string]any, []any:
	default:
		return nil, errors.New("background executor result must be a JSON object or array")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 32768 {
		return nil, errors.New("background executor result exceeds 32768 bytes")
	}
	return encoded, nil
}

func truncateUTF8(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
