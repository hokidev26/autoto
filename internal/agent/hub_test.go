package agent

import (
	"context"
	"fmt"
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
