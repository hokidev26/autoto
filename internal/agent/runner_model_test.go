package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func TestModelTurnUsageCalculatesTTFTAndThroughput(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	firstOutputAt := started.Add(500 * time.Millisecond)
	exact := modelTurnUsage(providers.Usage{InputTokens: 20, OutputTokens: 100, CachedInputTokens: 4, ReasoningTokens: 3}, 400, started, firstOutputAt, 2500*time.Millisecond)
	if exact.TTFTMS != 500 || exact.DurationMS != 2500 || exact.OutputTokens != 100 || exact.Estimated || exact.TokensPerSecond != 50 {
		t.Fatalf("unexpected exact turn usage: %+v", exact)
	}
	estimated := modelTurnUsage(providers.Usage{}, 20, started, firstOutputAt, 2500*time.Millisecond)
	if estimated.OutputTokens != 5 || !estimated.Estimated || estimated.TokensPerSecond != 2.5 {
		t.Fatalf("unexpected estimated turn usage: %+v", estimated)
	}
}

func TestIsTransientProviderErrorTreatsTruncatedJSONAsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		retryable bool
	}{
		{name: "truncated json", message: "unexpected end of JSON input", retryable: true},
		{name: "eof", message: "upstream EOF", retryable: true},
		{name: "authentication", message: "401 unauthorized", retryable: false},
		{name: "invalid request", message: "invalid request schema", retryable: false},
		// A wrong base URL answers 404 forever, so retrying only delays the error.
		{name: "not found", message: "cliproxyapi model request failed: 404", retryable: false},
		// 408 and 429 are 4xx that do clear on their own, so the status rule must
		// not sweep the whole 4xx range into permanent.
		{name: "request timeout", message: "openai model request failed: 408", retryable: true},
		{name: "rate limited", message: "openai model request failed: 429", retryable: true},
		// A bare 404 in free text is not a status line. Without the phrase anchor
		// this would be read as permanent and the timeout would stop retrying.
		{name: "digits outside a status line", message: "stream stalled after 404 tokens: timeout", retryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientProviderError(errors.New(tt.message)); got != tt.retryable {
				t.Fatalf("isTransientProviderError(%q) = %v, want %v", tt.message, got, tt.retryable)
			}
		})
	}
}

func TestRunModelTurnAttemptPublishesThroughputLifecycle(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := hub.Subscribe(ctx, "agent-1")
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "usage", Usage: &providers.Usage{InputTokens: 7}},
		{Type: "text", Text: "hello"},
		{Type: "usage", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 8}},
		{Type: "done", Done: true},
	}}}
	runner := &Runner{hub: hub}

	result, err, _ := runner.runModelTurnAttempt(ctx, "agent-1", "run-1", provider, "test", "", nil, nil, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.StartedAt.IsZero() || result.FirstOutputAt.IsZero() || result.CompletedAt.IsZero() || result.CompletedAt.Before(result.FirstOutputAt) || result.EstimatedOutputRunes != 5 || result.TurnUsage == nil || result.TurnUsage.OutputTokens != 8 || result.TurnUsage.Estimated {
		t.Fatalf("unexpected finalized result: %+v", result)
	}

	var lifecycle []Event
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-subscription:
			if event.Type == "model.started" || event.Type == "model.streaming" || event.Type == "model.completed" {
				lifecycle = append(lifecycle, event)
			}
			if event.Type == "model.completed" {
				goto collected
			}
		case <-deadline:
			t.Fatal("timed out waiting for model lifecycle events")
		}
	}

collected:
	if len(lifecycle) != 3 {
		t.Fatalf("expected started, streaming, completed events, got %+v", lifecycle)
	}
	if lifecycle[0].Type != "model.started" || lifecycle[1].Type != "model.streaming" || lifecycle[2].Type != "model.completed" {
		t.Fatalf("unexpected lifecycle order: %+v", lifecycle)
	}
	if startedAt, _ := lifecycle[0].Data["startedAt"].(string); startedAt == "" {
		t.Fatalf("model.started missing startedAt: %+v", lifecycle[0])
	}
	pending, ok := lifecycle[1].Data["pendingThroughput"].(*db.MessageTurnUsage)
	if !ok || pending.OutputTokens != 2 || !pending.Estimated {
		t.Fatalf("unexpected pending throughput: %#v", lifecycle[1].Data["pendingThroughput"])
	}
	throughput, ok := lifecycle[2].Data["throughput"].(*db.MessageTurnUsage)
	if !ok || throughput.OutputTokens != 8 || throughput.Estimated {
		t.Fatalf("unexpected final throughput: %#v", lifecycle[2].Data["throughput"])
	}
	if _, ok := lifecycle[2].Data["ttftMs"].(int64); !ok {
		t.Fatalf("model.completed missing ttftMs: %+v", lifecycle[2])
	}
}

func TestRunModelTurnAttemptThrottlesStreamingUsageEvents(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := hub.Subscribe(ctx, "agent-1")
	// 40 deltas well inside one 250ms throttle window: without the throttle
	// this stream produced 40 model.streaming events, one per delta.
	const deltas = 40
	turn := make([]providers.Event, 0, deltas+1)
	for i := 0; i < deltas; i++ {
		turn = append(turn, providers.Event{Type: "text", Text: "abcd"})
	}
	turn = append(turn, providers.Event{Type: "done", Done: true})
	provider := &scriptedProvider{turns: [][]providers.Event{turn}}
	runner := &Runner{hub: hub}

	if _, err, _ := runner.runModelTurnAttempt(ctx, "agent-1", "run-1", provider, "test", "", nil, nil, "auto", false); err != nil {
		t.Fatal(err)
	}

	var streaming []Event
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-subscription:
			if event.Type == "model.streaming" {
				streaming = append(streaming, event)
			}
			if event.Type == "model.completed" {
				goto collected
			}
		case <-deadline:
			t.Fatal("timed out waiting for model lifecycle events")
		}
	}

collected:
	// The first delta publishes immediately and finalize flushes the final
	// value; in-between deltas land inside the throttle window. The generous
	// ceiling only guards against a slow machine stretching the stream across
	// extra windows -- the point is that 40 deltas must not mean 40 events.
	if len(streaming) < 2 || len(streaming) > 8 {
		t.Fatalf("expected throttled streaming events (roughly first + final), got %d", len(streaming))
	}
	first, ok := streaming[0].Data["pendingThroughput"].(*db.MessageTurnUsage)
	if !ok || first.OutputTokens != 1 || !first.Estimated {
		t.Fatalf("unexpected first streaming throughput: %#v", streaming[0].Data["pendingThroughput"])
	}
	// 40 deltas x 4 runes = 160 runes, estimated at 4 runes per token. The
	// final event must carry the totals for the whole stream, not the value
	// frozen at the last unthrottled publish.
	last, ok := streaming[len(streaming)-1].Data["pendingThroughput"].(*db.MessageTurnUsage)
	if !ok || last.OutputTokens != 40 || !last.Estimated {
		t.Fatalf("unexpected final streaming throughput: %#v", streaming[len(streaming)-1].Data["pendingThroughput"])
	}
}

func TestRunModelTurnCapturesConcreteDispatch(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "dispatch", Dispatch: &providers.DispatchInfo{Provider: "actual", Model: "actual-model", CredentialID: "credential-1"}},
		{Type: "done", Done: true},
	}}}
	result, err, _ := (&Runner{}).runModelTurnAttempt(context.Background(), "agent", "run", provider, "aggregate-model", "", nil, nil, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dispatch != (providers.DispatchInfo{Provider: "actual", Model: "actual-model", CredentialID: "credential-1"}) {
		t.Fatalf("dispatch was not captured: %+v", result.Dispatch)
	}
	actualProvider, actualModel, credentialID := dispatchAttribution("aggregate:test", "aggregate-model", result.Dispatch)
	if actualProvider != "actual" || actualModel != "actual-model" || credentialID != "credential-1" {
		t.Fatalf("dispatch was not preferred: %q %q %q", actualProvider, actualModel, credentialID)
	}
}

func TestToolCallCountsAsFirstOutputAndEstimatedRunes(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "tool-1", Name: "Read", Input: json.RawMessage(`{"path":"文档.txt"}`)}},
		{Type: "done", Done: true},
	}}}
	runner := &Runner{}
	result, err, _ := runner.runModelTurnAttempt(context.Background(), "agent", "run", provider, "test", "", nil, nil, "auto", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.FirstOutputAt.IsZero() || result.EstimatedOutputRunes <= 0 || result.TurnUsage == nil || !result.TurnUsage.Estimated || result.TurnUsage.OutputTokens <= 0 {
		t.Fatalf("tool call should produce TTFT and estimated output usage: %+v", result)
	}
}

func TestRunModelTurnForwardsFastModeOnlyForSupportedModel(t *testing.T) {
	provider := &scriptedProvider{
		fastModels: map[string]bool{"test": true},
		turns:      [][]providers.Event{{{Type: "done", Done: true}}},
	}
	runner := &Runner{cfg: config.AgentConfig{}}
	if _, err, _ := runner.runModelTurnAttempt(context.Background(), "agent", "run", provider, "test", "", nil, nil, "auto", true); err != nil {
		t.Fatal(err)
	}
	if !provider.request(0).FastMode {
		t.Fatalf("supported model did not receive Fast mode: %+v", provider.request(0))
	}

	provider.fastModels = map[string]bool{}
	provider.turns = append(provider.turns, []providers.Event{{Type: "done", Done: true}})
	if _, err, _ := runner.runModelTurnAttempt(context.Background(), "agent", "run", provider, "basic", "", nil, nil, "auto", true); err != nil {
		t.Fatal(err)
	}
	if provider.request(1).FastMode {
		t.Fatalf("unsupported model received Fast mode: %+v", provider.request(1))
	}
}

type reasoningScriptedProvider struct {
	*scriptedProvider
	supported        bool
	reasoningEfforts []string
}

func (p *reasoningScriptedProvider) Capabilities() providers.Capabilities {
	capabilities := p.scriptedProvider.Capabilities()
	capabilities.ReasoningEffort = p.supported
	capabilities.ReasoningEfforts = append([]string(nil), p.reasoningEfforts...)
	return capabilities
}

func TestRunnerUsesRuntimeDefaultReasoningEffort(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	settings, err := store.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	effort := "high"
	if _, err := store.UpdateRuntimeSettings(ctx, db.RuntimeSettingsPatch{DefaultReasoningEffort: &effort, ExpectedRevision: settings.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "think"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: "done"},
		{Type: "done", Done: true, StopReason: "end_turn"},
	}}}
	runner := newAgentTestRunner(store, &reasoningScriptedProvider{scriptedProvider: provider, supported: true}, config.AgentConfig{MaxTurns: 1})
	if err := runner.run(ctx, agent.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := provider.request(0).ReasoningEffort; got != "high" {
		t.Fatalf("expected runtime default reasoning effort, got %q", got)
	}
}

func TestRunnerAgentReasoningEffortOverridesRuntimeDefault(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	settings, err := store.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaultEffort := "high"
	if _, err := store.UpdateRuntimeSettings(ctx, db.RuntimeSettingsPatch{DefaultReasoningEffort: &defaultEffort, ExpectedRevision: settings.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgentReasoningEffort(ctx, agent.ID, "low"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "think briefly"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: "done"},
		{Type: "done", Done: true, StopReason: "end_turn"},
	}}}
	runner := newAgentTestRunner(store, &reasoningScriptedProvider{scriptedProvider: provider, supported: true}, config.AgentConfig{MaxTurns: 1})
	if err := runner.run(ctx, agent.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := provider.request(0).ReasoningEffort; got != "low" {
		t.Fatalf("expected agent reasoning effort override, got %q", got)
	}
}

func TestRunnerRejectsUnsupportedExplicitReasoningEffort(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateAgentReasoningEffort(ctx, agent.ID, "medium"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "think"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{}
	runner := newAgentTestRunner(store, &reasoningScriptedProvider{scriptedProvider: provider, supported: false}, config.AgentConfig{MaxTurns: 1})
	err := runner.run(ctx, agent.ID, "")
	if !errors.Is(err, providers.ErrReasoningEffortUnsupported) || !strings.Contains(err.Error(), "fake") || !strings.Contains(err.Error(), "medium") {
		t.Fatalf("expected explicit unsupported reasoning error, got %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatalf("unsupported reasoning request reached provider %d times", provider.requestCount())
	}
}

func TestRunnerUsesXHighWhenProviderDeclaresIt(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateAgentReasoningEffort(ctx, agent.ID, "xhigh"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "think deeply"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: "done"},
		{Type: "done", Done: true, StopReason: "end_turn"},
	}}}
	runner := newAgentTestRunner(store, &reasoningScriptedProvider{
		scriptedProvider: provider,
		supported:        true,
		reasoningEfforts: []string{"low", "medium", "high", "xhigh"},
	}, config.AgentConfig{MaxTurns: 1})
	if err := runner.run(ctx, agent.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := provider.request(0).ReasoningEffort; got != "xhigh" {
		t.Fatalf("expected xhigh request, got %q", got)
	}
}

func TestRunnerRejectsXHighForLegacyReasoningCapability(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateAgentReasoningEffort(ctx, agent.ID, "xhigh"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "think deeply"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{}
	runner := newAgentTestRunner(store, &reasoningScriptedProvider{scriptedProvider: provider, supported: true}, config.AgentConfig{MaxTurns: 1})
	err := runner.run(ctx, agent.ID, "")
	if !errors.Is(err, providers.ErrReasoningEffortUnsupported) || !strings.Contains(err.Error(), "xhigh") {
		t.Fatalf("expected xhigh unsupported error, got %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatalf("unsupported xhigh reached provider %d times", provider.requestCount())
	}
}

func TestEstimateUsageCostUSD(t *testing.T) {
	openAICost := estimateUsageCostUSD("openai", "gpt-4.1-mini", providers.Usage{InputTokens: 1_000_000, CachedInputTokens: 250_000, OutputTokens: 100_000})
	if openAICost < 0.4849 || openAICost > 0.4851 {
		t.Fatalf("unexpected OpenAI cost: %.6f", openAICost)
	}
	gpt55Cost := estimateUsageCostUSD("openai", "gpt-5.5", providers.Usage{InputTokens: 1_000_000, CachedInputTokens: 100_000, OutputTokens: 100_000})
	if gpt55Cost < 6.7999 || gpt55Cost > 6.8001 {
		t.Fatalf("unexpected OpenAI GPT-5.5 cost: %.6f", gpt55Cost)
	}
	anthropicCost := estimateUsageCostUSD("anthropic", "claude-sonnet-4-5", providers.Usage{InputTokens: 1_000_000, CachedInputTokens: 100_000, OutputTokens: 100_000})
	if anthropicCost < 4.2299 || anthropicCost > 4.2301 {
		t.Fatalf("unexpected Anthropic cost: %.6f", anthropicCost)
	}
	opusCost := estimateUsageCostUSD("anthropic", "claude-opus-4-1", providers.Usage{InputTokens: 1_000_000, CachedInputTokens: 100_000, OutputTokens: 100_000})
	if opusCost < 21.1499 || opusCost > 21.1501 {
		t.Fatalf("unexpected Anthropic Opus cost: %.6f", opusCost)
	}
	opus45Cost := estimateUsageCostUSD("anthropic", "claude-opus-4-5", providers.Usage{InputTokens: 1_000_000, CachedInputTokens: 100_000, OutputTokens: 100_000})
	if opus45Cost < 7.0499 || opus45Cost > 7.0501 {
		t.Fatalf("unexpected Anthropic Opus 4.5 cost: %.6f", opus45Cost)
	}
	if got := estimateUsageCostUSD("unknown", "custom-model", providers.Usage{InputTokens: 1_000_000}); got != 0 {
		t.Fatalf("expected unknown model cost to be 0, got %.6f", got)
	}
}

func TestRunnerRecordsEstimatedCostAndCredentialAttribution(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	provider := &scriptedProvider{}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{})
	runner.recordAPIRequest(agent.ID, "", "", "openai", "gpt-4.1-mini", "credential-1", time.Millisecond, 1, providers.Usage{InputTokens: 1_000_000, OutputTokens: 100_000}, "")
	var cost float64
	var ttftMS int64
	var credentialID string
	if err := store.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_usd),0), COALESCE(MAX(ttft_ms),0), COALESCE(MAX(credential_id),'') FROM api_requests WHERE agent_id = ?`, agent.ID).Scan(&cost, &ttftMS, &credentialID); err != nil {
		t.Fatal(err)
	}
	if cost < 0.5599 || cost > 0.5601 {
		t.Fatalf("expected estimated cost to be stored, got %.6f", cost)
	}
	if ttftMS != 1 {
		t.Fatalf("expected ttft to be stored, got %dms", ttftMS)
	}
	if credentialID != "credential-1" {
		t.Fatalf("expected credential attribution to be stored, got %q", credentialID)
	}
}

func TestRunnerRetriesTransientProviderErrorBeforeOutput(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "error", Text: "temporary 500 from provider"}},
		{{Type: "text", Text: "recovered"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1, MaxTransientRetries: 1})

	if err := runner.run(ctx, agent.ID, ""); err != nil {
		t.Fatal(err)
	}
	if provider.requestCount() != 2 {
		t.Fatalf("expected retry to make two provider requests, got %d", provider.requestCount())
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].ContentText != "recovered" {
		t.Fatalf("expected recovered assistant message, got %+v", messages)
	}
}

func TestRunnerDoesNotRetryAfterPartialProviderOutput(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: "partial"},
		{Type: "error", Text: "temporary 500 after text"},
	}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1, MaxTransientRetries: 1})

	err := runner.run(ctx, agent.ID, "")
	if err == nil || !strings.Contains(err.Error(), "temporary 500") {
		t.Fatalf("expected provider error without retry, got %v", err)
	}
	if provider.requestCount() != 1 {
		t.Fatalf("expected no retry after partial output, got %d requests", provider.requestCount())
	}
}

func TestRunnerFailsOnFirstTokenTimeout(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &blockingProvider{started: make(chan struct{})}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1, FirstTokenTimeoutMs: 10})

	err := runner.run(ctx, agent.ID, "")
	if err == nil || !strings.Contains(err.Error(), "first token timeout") {
		t.Fatalf("expected first token timeout error, got %v", err)
	}
}

func TestUsageMetadataDoesNotSatisfyFirstTokenTimeout(t *testing.T) {
	provider := &usageThenBlockingProvider{}
	runner := &Runner{cfg: config.AgentConfig{FirstTokenTimeoutMs: 10}}
	_, err, retryable := runner.runModelTurnAttempt(context.Background(), "agent", "run", provider, "test", "", nil, nil, "auto", false)
	if err == nil || !strings.Contains(err.Error(), "first token timeout") || !retryable {
		t.Fatalf("usage metadata must not satisfy first-token timeout: err=%v retryable=%v", err, retryable)
	}
}

func TestRunnerSkipsAPIRequestForNotConfiguredProviderNotice(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{
		{Type: "text", Text: "provider is not configured"},
		{Type: "done", Done: true, StopReason: "not_configured"},
	}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})

	runner.Run(ctx, agent.ID)

	var count int64
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_requests WHERE agent_id = ?`, agent.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no api_requests for local not_configured notice, got %d", count)
	}
}

func TestRunnerPassesPersistedReasoningEffortToSupportingProvider(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.UpdateAgentReasoningEffort(ctx, agent.ID, "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{reasoning: true, turns: [][]providers.Event{{{Type: "text", Text: "done"}, {Type: "done", Done: true}}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
	runner.Run(ctx, agent.ID)
	if provider.requestCount() != 1 || provider.request(0).ReasoningEffort != "high" {
		t.Fatalf("expected persisted reasoning effort in provider request, got %+v", provider.request(0))
	}
}

type usageThenBlockingProvider struct{}

func (p *usageThenBlockingProvider) Name() string { return "fake" }

func (p *usageThenBlockingProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}

func (p *usageThenBlockingProvider) Generate(ctx context.Context, req providers.GenerateRequest) (<-chan providers.Event, error) {
	out := make(chan providers.Event, 1)
	out <- providers.Event{Type: "usage", Usage: &providers.Usage{InputTokens: 5}}
	return out, nil
}
