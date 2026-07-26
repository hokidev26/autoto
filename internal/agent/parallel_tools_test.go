package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// testOrderLog records the relative order in which concurrent goroutines reach
// a point of interest. Appends are serialized under a mutex, so the resulting
// slice is a trustworthy happens-before ordering even though the recorders run
// on different goroutines.
type testOrderLog struct {
	mu    sync.Mutex
	order []string
}

func (l *testOrderLog) record(entry string) {
	l.mu.Lock()
	l.order = append(l.order, entry)
	l.mu.Unlock()
}

func (l *testOrderLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.order...)
}

func (l *testOrderLog) indexOf(entry string) int {
	for i, e := range l.snapshot() {
		if e == entry {
			return i
		}
	}
	return -1
}

// orderedProbeTool is a minimal tools.Tool whose Execute logs a "<name>:start"
// entry immediately, optionally signals a buffered "started" channel so a test
// can wait for concurrent dispatch to actually happen, then optionally blocks
// on a release channel (or ctx cancellation) before logging "<name>:done" and
// returning. A nil release channel makes it a non-blocking tool.
type orderedProbeTool struct {
	name    string
	risk    tools.Risk
	log     *testOrderLog
	started chan<- string
	release <-chan struct{}
}

func (t orderedProbeTool) Name() string        { return t.name }
func (t orderedProbeTool) Description() string { return "prefetch ordering probe tool" }
func (t orderedProbeTool) Schema() any {
	return map[string]any{"type": "object", "additionalProperties": true}
}
func (t orderedProbeTool) Risk(json.RawMessage) tools.Risk { return t.risk }

func (t orderedProbeTool) Execute(ctx context.Context, call tools.Call, env tools.Env) (tools.Result, error) {
	if t.log != nil {
		t.log.record(t.name + ":start")
	}
	if t.started != nil {
		select {
		case t.started <- t.name:
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		}
	}
	if t.release != nil {
		select {
		case <-t.release:
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		}
	}
	if t.log != nil {
		t.log.record(t.name + ":done")
	}
	return tools.Result{Output: t.name + "-output"}, nil
}

// newPrefetchTestFixture builds the minimal store/agent/run scaffolding the
// segment loop needs, mirroring the pattern used by the other
// runContinuationSegment-level tests in continuation_test.go.
func newPrefetchTestFixture(t *testing.T, provider providers.Provider, toolRegistry *tools.Registry) (*db.Store, db.Agent, db.Run) {
	t.Helper()
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	trigger, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "exercise prefetch"})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, TriggerMessageID: trigger.ID, Status: "running", ExecutionMode: db.RunExecutionModeExecute})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.AssignMessageRun(ctx, agent.ID, trigger.ID, run.ID); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, agent, run
}

func newPrefetchTestRunner(store *db.Store, provider providers.Provider, toolRegistry *tools.Registry) *Runner {
	providerRegistry := providers.NewRegistry()
	providerRegistry.Register(provider)
	return NewRunner(store, providerRegistry, toolRegistry, NewHub(), config.AgentConfig{})
}

// TestPrefetchToolCallsPreserveOrderDespiteOutOfOrderCompletion verifies the
// central correctness property: when the model requests several eligible
// (read-risk) tool calls in one turn, they run concurrently -- proven here by
// releasing them in the reverse of their call order and watching them finish
// in that reverse order -- but the durable message rows the segment loop
// writes still land in the model's original tool_call order.
func TestPrefetchToolCallsPreserveOrderDespiteOutOfOrderCompletion(t *testing.T) {
	log := &testOrderLog{}
	started := make(chan string, 3)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	releaseC := make(chan struct{})

	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(orderedProbeTool{name: "PrefetchA", risk: tools.RiskRead, log: log, started: started, release: releaseA})
	toolRegistry.Register(orderedProbeTool{name: "PrefetchB", risk: tools.RiskRead, log: log, started: started, release: releaseB})
	toolRegistry.Register(orderedProbeTool{name: "PrefetchC", risk: tools.RiskRead, log: log, started: started, release: releaseC})

	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-a", Name: "PrefetchA", Input: json.RawMessage(`{}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-b", Name: "PrefetchB", Input: json.RawMessage(`{}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-c", Name: "PrefetchC", Input: json.RawMessage(`{}`)}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
		{{Type: "text", Text: "all reads complete"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}

	store, agent, run := newPrefetchTestFixture(t, provider, toolRegistry)
	defer store.Close()
	runner := newPrefetchTestRunner(store, provider, toolRegistry)

	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 2, maxTotalTurns: 2, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}

	type segmentResult struct {
		outcome segmentOutcome
		err     error
	}
	resultCh := make(chan segmentResult, 1)
	go func() {
		outcome, err := runner.runContinuationSegment(context.Background(), state, 0)
		resultCh <- segmentResult{outcome: outcome, err: err}
	}()

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for tool %d to start; started so far=%v", i, seen)
		}
	}
	if !seen["PrefetchA"] || !seen["PrefetchB"] || !seen["PrefetchC"] {
		t.Fatalf("expected all three eligible tool calls to be dispatched concurrently, got %v", seen)
	}

	// Release in the reverse of call order so completion order is C, B, A --
	// the opposite of the model's original tool_call order (A, B, C).
	close(releaseC)
	time.Sleep(20 * time.Millisecond)
	close(releaseB)
	time.Sleep(20 * time.Millisecond)
	close(releaseA)

	var result segmentResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for segment to finish")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.outcome.disposition != segmentComplete {
		t.Fatalf("unexpected outcome: %+v", result.outcome)
	}

	entries := log.snapshot()
	doneA, doneB, doneC := -1, -1, -1
	for i, e := range entries {
		switch e {
		case "PrefetchA:done":
			doneA = i
		case "PrefetchB:done":
			doneB = i
		case "PrefetchC:done":
			doneC = i
		}
	}
	if doneA < 0 || doneB < 0 || doneC < 0 || !(doneC < doneB && doneB < doneA) {
		t.Fatalf("expected completion order C, B, A (proving real concurrency), got log=%v", entries)
	}

	messages, err := store.ListMessages(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toolResultOrder []string
	for _, message := range messages {
		switch message.ParentToolID {
		case "call-a", "call-b", "call-c":
			toolResultOrder = append(toolResultOrder, message.ParentToolID)
		}
	}
	want := []string{"call-a", "call-b", "call-c"}
	if len(toolResultOrder) != len(want) {
		t.Fatalf("expected 3 tool result messages, got %v", toolResultOrder)
	}
	for i := range want {
		if toolResultOrder[i] != want[i] {
			t.Fatalf("durable tool result messages out of order: got %v want %v", toolResultOrder, want)
		}
	}
}

// TestPrefetchToolCallsMixedRiskRunsIneligibleCallsSerially verifies that a
// turn mixing read-risk (eligible) and exec-risk (ineligible) calls still
// produces correct, in-order results, and that the exec-risk call is never
// started while the read-risk calls are still in flight -- i.e. it truly runs
// on the unchanged serial path rather than being swept into the worker pool.
func TestPrefetchToolCallsMixedRiskRunsIneligibleCallsSerially(t *testing.T) {
	log := &testOrderLog{}
	started := make(chan string, 2)
	releaseX := make(chan struct{})
	releaseY := make(chan struct{})

	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(orderedProbeTool{name: "ReadX", risk: tools.RiskRead, log: log, started: started, release: releaseX})
	toolRegistry.Register(orderedProbeTool{name: "ReadY", risk: tools.RiskRead, log: log, started: started, release: releaseY})
	toolRegistry.Register(orderedProbeTool{name: "ExecZ", risk: tools.RiskExec, log: log})

	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-x", Name: "ReadX", Input: json.RawMessage(`{}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-z", Name: "ExecZ", Input: json.RawMessage(`{}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-y", Name: "ReadY", Input: json.RawMessage(`{}`)}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
		{{Type: "text", Text: "mixed turn complete"}, {Type: "done", Done: true, StopReason: "end_turn"}},
	}}

	store, agent, run := newPrefetchTestFixture(t, provider, toolRegistry)
	defer store.Close()
	runner := newPrefetchTestRunner(store, provider, toolRegistry)

	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 2, maxTotalTurns: 2, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}

	type segmentResult struct {
		outcome segmentOutcome
		err     error
	}
	resultCh := make(chan segmentResult, 1)
	go func() {
		outcome, err := runner.runContinuationSegment(context.Background(), state, 0)
		resultCh <- segmentResult{outcome: outcome, err: err}
	}()

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for read tool %d to start; started so far=%v", i, seen)
		}
	}
	if !seen["ReadX"] || !seen["ReadY"] {
		t.Fatalf("expected both read-risk calls to be dispatched concurrently, got %v", seen)
	}

	// While both reads are still blocked, the exec-risk call must not have run.
	// It cannot have: the exec-risk call is ineligible for prefetch, so the
	// serial loop that would reach it is itself blocked behind the prefetch
	// pass's WaitGroup barrier, which cannot return until both reads unblock.
	if log.indexOf("ExecZ:start") >= 0 {
		t.Fatal("exec-risk call ran while read-risk calls were still pending; it must stay on the serial path")
	}

	close(releaseX)
	close(releaseY)

	// The exec-risk call still goes through the unmodified, non-bypassed
	// permission gate -- under acceptEdits an exec-risk call asks for human
	// approval, exactly as it would without prefetch in the picture.
	waitForPendingApproval(t, runner, agent.ID, "call-z")
	if accepted, err := runner.ApproveToolCall(context.Background(), agent.ID, "call-z", ToolApprovalDecision{Decision: "allow_once", Reason: "test approval", DecidedBy: "test"}); err != nil || !accepted {
		t.Fatalf("failed to approve exec-risk call: accepted=%v err=%v", accepted, err)
	}

	var result segmentResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for segment to finish")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.outcome.disposition != segmentComplete {
		t.Fatalf("unexpected outcome: %+v", result.outcome)
	}

	entries := log.snapshot()
	execStart := -1
	readXDone, readYDone := -1, -1
	for i, e := range entries {
		switch e {
		case "ExecZ:start":
			execStart = i
		case "ReadX:done":
			readXDone = i
		case "ReadY:done":
			readYDone = i
		}
	}
	if execStart < 0 || readXDone < 0 || readYDone < 0 || execStart < readXDone || execStart < readYDone {
		t.Fatalf("exec-risk call did not run strictly after both read-risk calls completed: log=%v", entries)
	}

	messages, err := store.ListMessages(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var toolResultOrder []string
	for _, message := range messages {
		switch message.ParentToolID {
		case "call-x", "call-z", "call-y":
			toolResultOrder = append(toolResultOrder, message.ParentToolID)
		}
	}
	want := []string{"call-x", "call-z", "call-y"}
	if len(toolResultOrder) != len(want) {
		t.Fatalf("expected 3 tool result messages, got %v", toolResultOrder)
	}
	for i := range want {
		if toolResultOrder[i] != want[i] {
			t.Fatalf("durable tool result messages out of order: got %v want %v", toolResultOrder, want)
		}
	}
}

// TestPrefetchToolCallsSingleEligibleCallStaysOnSerialPath pins the exact
// threshold the design specifies: with fewer than two eligible calls, nothing
// changes -- prefetchToolCallResults must return nil so the caller's loop
// falls through entirely to the pre-existing serial executeToolForLoop call.
func TestPrefetchToolCallsSingleEligibleCallStaysOnSerialPath(t *testing.T) {
	// A real store and agent are required: prefetch resolves the run's policy
	// before deciding anything, so an empty Runner would return nil for every
	// case and the eligibility logic below would never actually be exercised.
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	store, agent, run := newPrefetchTestFixture(t, &scriptedProvider{}, registry)
	defer store.Close()
	runner := newPrefetchTestRunner(store, &scriptedProvider{}, registry)
	ctx := context.Background()
	toolset := map[string]tools.Tool{"Read": tools.ReadTool{}, "Bash": tools.BashTool{}}

	calls := []providers.ToolCall{{ID: "only", Name: "Read", Input: json.RawMessage(`{}`)}}
	if got := runner.prefetchToolCallResults(ctx, agent.ID, run.ID, calls, "assistant-1", toolset); got != nil {
		t.Fatalf("expected nil prefetch results for a single eligible call, got %+v", got)
	}

	// Also pin the zero- and mixed-ineligible cases: still nil, never a
	// partially-populated slice the caller could misread as "ran".
	if got := runner.prefetchToolCallResults(ctx, agent.ID, run.ID, nil, "assistant-1", toolset); got != nil {
		t.Fatalf("expected nil prefetch results for no calls, got %+v", got)
	}
	mixed := []providers.ToolCall{
		{ID: "only-read", Name: "Read", Input: json.RawMessage(`{}`)},
		{ID: "only-exec", Name: "Bash", Input: json.RawMessage(`{"command":"echo hi"}`)},
	}
	if got := runner.prefetchToolCallResults(ctx, agent.ID, run.ID, mixed, "assistant-1", toolset); got != nil {
		t.Fatalf("expected nil prefetch results when only one call is eligible, got %+v", got)
	}

	// Two eligible reads DO prefetch, which proves the nil results above come
	// from the eligibility threshold rather than from an inert fixture.
	twoReads := []providers.ToolCall{
		{ID: "read-a", Name: "Read", Input: json.RawMessage(`{"file_path":"a.txt"}`)},
		{ID: "read-b", Name: "Read", Input: json.RawMessage(`{"file_path":"b.txt"}`)},
	}
	got := runner.prefetchToolCallResults(ctx, agent.ID, run.ID, twoReads, "assistant-1", toolset)
	if got == nil || !got[0].ran || !got[1].ran {
		t.Fatalf("expected both eligible reads to be prefetched, got %+v", got)
	}
}

// TestPrefetchSkipsReadsThatStillNeedApproval covers the gate that risk alone
// does not provide. A read tool still prompts when workflow preferences turn
// off auto-allow for reads, and prefetching those would put several approval
// prompts on screen at once for calls the user may never approve.
func TestPrefetchSkipsReadsThatStillNeedApproval(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterCore(registry)
	store, agent, run := newPrefetchTestFixture(t, &scriptedProvider{}, registry)
	defer store.Close()
	runner := newPrefetchTestRunner(store, &scriptedProvider{}, registry)
	ctx := context.Background()
	toolset := map[string]tools.Tool{"Read": tools.ReadTool{}}
	calls := []providers.ToolCall{
		{ID: "read-a", Name: "Read", Input: json.RawMessage(`{"file_path":"a.txt"}`)},
		{ID: "read-b", Name: "Read", Input: json.RawMessage(`{"file_path":"b.txt"}`)},
	}

	if got := runner.prefetchToolCallResults(ctx, agent.ID, run.ID, calls, "assistant-1", toolset); got == nil {
		t.Fatal("reads that auto-allow should be prefetched")
	}

	if _, err := store.UpdateWorkflowPreferences(ctx, db.WorkflowPreferences{AllowReadOnlyByDefault: false}); err != nil {
		t.Fatal(err)
	}
	if got := runner.prefetchToolCallResults(ctx, agent.ID, run.ID, calls, "assistant-1", toolset); got != nil {
		t.Fatalf("reads that require approval must not be prefetched, got %+v", got)
	}
}

// TestPrefetchToolCallsCancellationDoesNotHangOrLeakGoroutines exercises the
// ctx-cancellation path required of the prefetch pass: workers dispatched
// before cancellation must still return promptly once ctx is canceled, the
// wg.Wait() barrier must not hang, and no goroutine may survive past the call.
func TestPrefetchToolCallsCancellationDoesNotHangOrLeakGoroutines(t *testing.T) {
	baseline := goroutineCountSettled(t)

	log := &testOrderLog{}
	started := make(chan string, 3)
	// A release channel that is never closed: these tools only ever unblock
	// via ctx cancellation, modeling a real read-risk tool (e.g. a subprocess
	// or HTTP call) that honors ctx.
	block := make(chan struct{})

	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(orderedProbeTool{name: "CancelA", risk: tools.RiskRead, log: log, started: started, release: block})
	toolRegistry.Register(orderedProbeTool{name: "CancelB", risk: tools.RiskRead, log: log, started: started, release: block})
	toolRegistry.Register(orderedProbeTool{name: "CancelC", risk: tools.RiskRead, log: log, started: started, release: block})

	provider := &scriptedProvider{turns: [][]providers.Event{
		{
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-a", Name: "CancelA", Input: json.RawMessage(`{}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-b", Name: "CancelB", Input: json.RawMessage(`{}`)}},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-c", Name: "CancelC", Input: json.RawMessage(`{}`)}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
	}}

	store, _, run := newPrefetchTestFixture(t, provider, toolRegistry)
	defer store.Close()
	runner := newPrefetchTestRunner(store, provider, toolRegistry)

	state := continuationRunState{run: run, limits: continuationLimits{segmentTurns: 2, maxTotalTurns: 2, maxTokens: 10000}, deadline: time.Now().Add(time.Minute)}

	segmentCtx, cancel := context.WithCancel(context.Background())

	type segmentResult struct {
		outcome segmentOutcome
		err     error
	}
	resultCh := make(chan segmentResult, 1)
	go func() {
		outcome, err := runner.runContinuationSegment(segmentCtx, state, 0)
		resultCh <- segmentResult{outcome: outcome, err: err}
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for tool %d to start", i)
		}
	}

	cancel()

	select {
	case result := <-resultCh:
		if result.err == nil {
			t.Fatalf("expected the segment to surface an error on cancellation, got outcome=%+v", result.outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("segment did not return after ctx cancellation; prefetch barrier appears to have hung")
	}

	after := goroutineCountSettled(t)
	if after > baseline+2 {
		t.Fatalf("goroutines appear to have leaked from the prefetch pool: baseline=%d after=%d", baseline, after)
	}
}

// goroutineCountSettled samples runtime.NumGoroutine() repeatedly until it
// stabilizes (or a deadline passes), since goroutine teardown after a channel
// send/cancel is not instantaneous.
func goroutineCountSettled(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	last := runtime.NumGoroutine()
	stable := 0
	for time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
		current := runtime.NumGoroutine()
		if current == last {
			stable++
			if stable >= 5 {
				return current
			}
		} else {
			stable = 0
			last = current
		}
	}
	return last
}
