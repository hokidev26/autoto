package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func TestHubProtocolEnvelopeAndReplay(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	hub := NewHubWithConfig(HubConfig{
		RingSize:    4,
		ReplayLimit: 3,
		Clock:       func() time.Time { return now },
		NewSession:  sequenceSessions(),
	})

	hub.Publish(Event{Type: "one", AgentID: "agent-1"})
	hub.Publish(Event{Type: "two", AgentID: "agent-1"})
	initial := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})
	if initial.StreamSession == "" || initial.LatestSequence != 2 {
		t.Fatalf("unexpected initial stream: %+v", initial)
	}

	sub := hub.SubscribeProtocol(t.Context(), SubscribeOptions{
		AgentID:       "agent-1",
		StreamSession: initial.StreamSession,
		After:         1,
		HasAfter:      true,
	})
	if sub.Reason != "" {
		t.Fatalf("unexpected resync: %q", sub.Reason)
	}
	if len(sub.Replay) != 1 || sub.Replay[0].Type != "two" || sub.Replay[0].Sequence != 2 {
		t.Fatalf("unexpected replay: %+v", sub.Replay)
	}
	if sub.Replay[0].Protocol != ProtocolVersion || sub.Replay[0].StreamSession != initial.StreamSession {
		t.Fatalf("missing protocol envelope: %+v", sub.Replay[0])
	}

	hub.Publish(Event{Type: "three", AgentID: "agent-1"})
	select {
	case event := <-sub.Events:
		if event.Sequence != 3 || event.Type != "three" {
			t.Fatalf("unexpected live event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
}

func TestHubToolInputPreviewSnapshots(t *testing.T) {
	hub := NewHub()
	hub.Publish(Event{Type: "tool.input_delta", AgentID: "agent-1", Text: "hello ", Data: map[string]any{"toolUseId": "write-1"}})
	hub.Publish(Event{Type: "tool.input_delta", AgentID: "agent-1", Text: "world", Data: map[string]any{"toolUseId": "write-1"}})
	// Snapshot-style previews replace what was accumulated before them.
	hub.Publish(Event{Type: "tool.input_delta", AgentID: "agent-1", Text: "curl -v", Data: map[string]any{"toolUseId": "bash-1", "replace": true}})
	hub.Publish(Event{Type: "tool.input_delta", AgentID: "agent-1", Text: "curl -v https://example.com", Data: map[string]any{"toolUseId": "bash-1", "replace": true}})
	// Output events must stay out of the input previews and vice versa.
	hub.Publish(Event{Type: "tool.output", AgentID: "agent-1", Text: "ok", Data: map[string]any{"toolUseId": "bash-1"}})

	previews := hub.ToolInputPreviewSnapshots("agent-1")
	if previews["write-1"].Text != "hello world" {
		t.Fatalf("append preview = %+v", previews["write-1"])
	}
	if previews["bash-1"].Text != "curl -v https://example.com" {
		t.Fatalf("replace preview = %+v", previews["bash-1"])
	}
	outputs := hub.ToolOutputSnapshots("agent-1")
	if outputs["bash-1"].Text != "ok" {
		t.Fatalf("output snapshot = %+v", outputs["bash-1"])
	}
	if _, ok := outputs["write-1"]; ok {
		t.Fatal("input deltas must not leak into output snapshots")
	}
}

func TestHubProviderRetrySnapshotStaysAfterRingEviction(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{RingSize: 3, NewSession: sequenceSessions()})
	hub.Publish(Event{
		Type:    "agent.provider_error_retry",
		AgentID: "agent-1",
		Data: map[string]any{
			"attempt":     2,
			"maxAttempts": 4,
			"backoffMs":   1500,
			"scope":       "model_turn",
			"runId":       "run-9",
			"error":       "SECRET_PROVIDER_RETRY_DETAIL",
		},
	})
	got := hub.ProviderRetrySnapshot("agent-1")
	if got == nil || got.Attempt != 2 || got.MaxAttempts != 4 || got.BackoffMs != 1500 || got.Scope != "model_turn" || got.RunID != "run-9" {
		t.Fatalf("retry snapshot = %+v", got)
	}

	for i := 0; i < 8; i++ {
		hub.Publish(Event{Type: "agent.text", AgentID: "agent-1", Text: fmt.Sprintf("noise-%d", i)})
	}
	got = hub.ProviderRetrySnapshot("agent-1")
	if got == nil || got.Attempt != 2 || got.MaxAttempts != 4 {
		t.Fatalf("retry snapshot must survive ring eviction, got %+v", got)
	}

	hub.Publish(Event{Type: "model.started", AgentID: "agent-1", Data: map[string]any{"runId": "run-9"}})
	if hub.ProviderRetrySnapshot("agent-1") != nil {
		t.Fatal("model.started must clear the retry snapshot")
	}
}

func TestHubResyncReasonsAreExplicit(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		RingSize:         3,
		ReplayLimit:      2,
		SubscriberBuffer: 1,
		NewSession:       sequenceSessions(),
	})
	for i := 0; i < 4; i++ {
		hub.Publish(Event{Type: fmt.Sprintf("event-%d", i), AgentID: "agent-1"})
	}
	current := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})

	mismatch := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1", StreamSession: "wrong", After: 4, HasAfter: true})
	if mismatch.Reason != ResyncSessionMismatch || len(mismatch.Replay) != 0 || mismatch.Events != nil {
		t.Fatalf("expected session mismatch without partial replay, got %+v", mismatch)
	}
	expired := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1", StreamSession: current.StreamSession, After: 0, HasAfter: true})
	if expired.Reason != ResyncCursorExpired || len(expired.Replay) != 0 || expired.Events != nil {
		t.Fatalf("expected cursor expiry without partial replay, got %+v", expired)
	}
	limited := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1", StreamSession: current.StreamSession, After: 1, HasAfter: true})
	if limited.Reason != ResyncReplayLimit || len(limited.Replay) != 0 || limited.Events != nil {
		t.Fatalf("expected replay-limit resync without partial replay, got %+v", limited)
	}

	overrun := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})
	hub.Publish(Event{Type: "five", AgentID: "agent-1"})
	hub.Publish(Event{Type: "six", AgentID: "agent-1"})
	select {
	case reason := <-overrun.Resync:
		if reason != ResyncSubscriberOverrun {
			t.Fatalf("expected subscriber overrun, got %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscriber overrun")
	}
}

func TestHubReclaimsIdleStreamsAndBoundsStreamCount(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	hub := NewHubWithConfig(HubConfig{
		MaxStreams:  2,
		IdleTimeout: 15 * time.Minute,
		Clock:       func() time.Time { return now },
		NewSession:  sequenceSessions(),
	})
	hub.Publish(Event{Type: "one", AgentID: "agent-1"})
	hub.Publish(Event{Type: "one", AgentID: "agent-2"})
	hub.Publish(Event{Type: "one", AgentID: "agent-3"})
	if len(hub.streams) != 2 {
		t.Fatalf("expected bounded streams, got %d", len(hub.streams))
	}

	now = now.Add(15 * time.Minute)
	hub.CollectGarbage()
	if len(hub.streams) != 0 {
		t.Fatalf("expected idle streams to be reclaimed, got %d", len(hub.streams))
	}
}

func sequenceSessions() func() string {
	var next int
	return func() string {
		next++
		return fmt.Sprintf("session-%d", next)
	}
}

func TestSubscribeCompatibilityIsRealtimeOnly(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{NewSession: sequenceSessions()})
	hub.Publish(Event{Type: "before", AgentID: "agent-1"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := hub.Subscribe(ctx, "agent-1")
	select {
	case event := <-sub:
		t.Fatalf("unexpected replay through compatibility subscriber: %+v", event)
	default:
	}
	hub.Publish(Event{Type: "after", AgentID: "agent-1"})
	select {
	case event := <-sub:
		if event.Type != "after" {
			t.Fatalf("unexpected compatibility event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for compatibility event")
	}
}

func TestHubFanoutDoesNotHoldHubLock(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{NewSession: sequenceSessions()})
	sub := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})
	if sub.Reason != "" {
		t.Fatalf("subscribe failed: %q", sub.Reason)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	hub.testFanoutStall = func() {
		close(started)
		<-release
	}
	t.Cleanup(func() {
		hub.testFanoutStall = nil
		releaseOnce.Do(func() { close(release) })
	})

	published := make(chan struct{})
	go func() {
		hub.Publish(Event{Type: "one", AgentID: "agent-1"})
		close(published)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fan-out stall")
	}

	finished := make(chan StreamWatermark, 1)
	go func() {
		finished <- hub.Watermark("agent-1")
	}()
	select {
	case watermark := <-finished:
		if watermark.LatestSequence != 1 {
			t.Fatalf("watermark during fan-out = %+v", watermark)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watermark blocked by subscriber fan-out; hub lock still held")
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish did not finish after stall was released")
	}
	select {
	case event := <-sub.Events:
		if event.Sequence != 1 || event.Type != "one" {
			t.Fatalf("unexpected live event: %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live event after stall")
	}
}

func TestHubConcurrentPublishPreservesPerSubscriberOrder(t *testing.T) {
	const publishers = 4
	const perPublisher = 100
	const subscribers = 3
	total := publishers * perPublisher
	hub := NewHubWithConfig(HubConfig{
		SubscriberBuffer: total,
		NewSession:       sequenceSessions(),
	})
	subs := make([]*Subscription, subscribers)
	for i := 0; i < subscribers; i++ {
		subs[i] = hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})
		if subs[i].Reason != "" {
			t.Fatalf("subscribe %d failed: %q", i, subs[i].Reason)
		}
	}

	var wg sync.WaitGroup
	for p := 0; p < publishers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perPublisher; i++ {
				hub.Publish(Event{Type: "n", AgentID: "agent-1"})
			}
		}()
	}
	wg.Wait()

	for si, sub := range subs {
		var last uint64
		for n := 0; n < total; n++ {
			select {
			case event := <-sub.Events:
				if event.Sequence != last+1 {
					t.Fatalf("subscriber %d: got sequence %d after %d", si, event.Sequence, last)
				}
				last = event.Sequence
			case reason := <-sub.Resync:
				t.Fatalf("subscriber %d resynced at %d: %q", si, last, reason)
			case <-time.After(2 * time.Second):
				t.Fatalf("subscriber %d timed out after sequence %d", si, last)
			}
		}
	}
}

func TestHubOverrunOfOneSubscriberDoesNotStarveAnother(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{
		SubscriberBuffer: 1,
		NewSession:       sequenceSessions(),
	})
	slow := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})
	fast := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})
	if slow.Reason != "" || fast.Reason != "" {
		t.Fatalf("subscribe failed: slow=%q fast=%q", slow.Reason, fast.Reason)
	}

	hub.Publish(Event{Type: "one", AgentID: "agent-1"})
	select {
	case event := <-fast.Events:
		if event.Type != "one" {
			t.Fatalf("fast subscriber first event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fast subscriber missed the first event")
	}
	hub.Publish(Event{Type: "two", AgentID: "agent-1"})

	select {
	case reason := <-slow.Resync:
		if reason != ResyncSubscriberOverrun {
			t.Fatalf("slow subscriber reason = %q", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow subscriber was not marked overrun")
	}

	select {
	case event := <-fast.Events:
		if event.Type != "two" || event.Sequence != 2 {
			t.Fatalf("fast subscriber second event = %+v", event)
		}
	case reason := <-fast.Resync:
		t.Fatalf("fast subscriber resynced: %q", reason)
	case <-time.After(2 * time.Second):
		t.Fatal("fast subscriber starved after the slow subscriber overran")
	}
}

func TestGenerateStreamSessionRandFailureReturnsError(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, errors.New("rand unavailable") }

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("generateStreamSession panicked: %v", recovered)
		}
	}()
	id, err := generateStreamSession()
	if err == nil || id != "" {
		t.Fatalf("generateStreamSession() = %q, %v", id, err)
	}
	if randomStreamSession() != "" {
		t.Fatal("randomStreamSession must fail closed with an empty id")
	}
}

func TestHubSessionAllocationFailureDoesNotPanic(t *testing.T) {
	hub := NewHubWithConfig(HubConfig{NewSession: func() string { return "" }})
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Publish panicked: %v", recovered)
		}
	}()
	hub.Publish(Event{Type: "one", AgentID: "agent-1"})
	hub.mu.Lock()
	streams := len(hub.streams)
	hub.mu.Unlock()
	if streams != 0 {
		t.Fatalf("failed session allocation created %d streams", streams)
	}
	sub := hub.SubscribeProtocol(t.Context(), SubscribeOptions{AgentID: "agent-1"})
	if sub.Reason != ResyncStreamEvicted {
		t.Fatalf("subscribe reason = %q, want %q", sub.Reason, ResyncStreamEvicted)
	}
}

func TestHubSequenceWrapSessionFailureLeavesStreamIntact(t *testing.T) {
	var calls int
	hub := NewHubWithConfig(HubConfig{NewSession: func() string {
		calls++
		if calls == 1 {
			return "session-1"
		}
		return ""
	}})
	hub.Publish(Event{Type: "one", AgentID: "agent-1"})
	hub.mu.Lock()
	current := hub.streams["agent-1"]
	current.sequence = math.MaxUint64
	session := current.session
	ringLen := len(current.ring)
	hub.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Publish panicked on wrap: %v", recovered)
		}
	}()
	hub.Publish(Event{Type: "wrap", AgentID: "agent-1"})

	hub.mu.Lock()
	defer hub.mu.Unlock()
	current = hub.streams["agent-1"]
	if current.session != session || current.sequence != math.MaxUint64 {
		t.Fatalf("wrap failure mutated stream session=%q seq=%d", current.session, current.sequence)
	}
	if len(current.ring) != ringLen {
		t.Fatalf("wrap failure dropped the ring: %d -> %d", ringLen, len(current.ring))
	}
}
