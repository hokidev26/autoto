package agent

import (
	"strings"
	"testing"
	"time"
)

// Publish holds a hub-wide lock, so it caches each event's encoded size instead
// of re-marshalling on every eviction, and drops a batch of evicted events in one
// reslice instead of shifting the slice per eviction. Both are accounting changes,
// so what needs pinning is that the ring still holds the newest events and still
// respects both budgets.
func TestHubRingEvictsOldestAndKeepsBudgets(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	hub := NewHubWithConfig(HubConfig{
		RingSize:    3,
		ReplayLimit: 100,
		Clock:       func() time.Time { return now },
		NewSession:  sequenceSessions(),
	})

	for _, name := range []string{"one", "two", "three", "four", "five"} {
		hub.Publish(Event{Type: name, AgentID: "agent-1"})
	}

	hub.mu.Lock()
	current := hub.streams["agent-1"]
	got := make([]string, 0, len(current.ring))
	ringBytes := current.ringBytes
	for _, entry := range current.ring {
		got = append(got, entry.event.Type)
	}
	hub.mu.Unlock()

	if len(got) != 3 || got[0] != "three" || got[1] != "four" || got[2] != "five" {
		t.Fatalf("ring must keep the newest RingSize events in order, got %v", got)
	}
	if ringBytes <= 0 {
		t.Fatalf("ring byte accounting must stay positive, got %d", ringBytes)
	}
}

// A single large event can force several evictions at once. That batch used to
// shift the whole slice per eviction; it now drops them together, and the byte
// budget still has to be respected afterwards.
func TestHubRingEvictsABatchToHonourTheByteBudget(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	// MaxEventBytes has to be set too: the constructor floors RingBytes at
	// MaxEventBytes so a single event always fits, which would otherwise raise this
	// budget to the 32KB default and never evict at all.
	hub := NewHubWithConfig(HubConfig{
		RingSize:      64,
		RingBytes:     4096,
		MaxEventBytes: 4096,
		ReplayLimit:   100,
		Clock:         func() time.Time { return now },
		NewSession:    sequenceSessions(),
	})

	for i := 0; i < 12; i++ {
		hub.Publish(Event{Type: "small", AgentID: "agent-1", Text: strings.Repeat("a", 256)})
	}
	// Large enough that several of the small events have to go to make room.
	hub.Publish(Event{Type: "large", AgentID: "agent-1", Text: strings.Repeat("b", 3000)})

	hub.mu.Lock()
	current := hub.streams["agent-1"]
	ringBytes := current.ringBytes
	count := len(current.ring)
	newest := ""
	if count > 0 {
		newest = current.ring[count-1].event.Type
	}
	// The cached sizes must still add up to the running total, or eviction would
	// drift away from the real budget over time.
	sum := 0
	for _, entry := range current.ring {
		sum += entry.size
	}
	hub.mu.Unlock()

	if newest != "large" {
		t.Fatalf("the newest event must survive eviction, got %q", newest)
	}
	if ringBytes > 4096 {
		t.Fatalf("ring byte budget exceeded: %d", ringBytes)
	}
	if sum != ringBytes {
		t.Fatalf("cached sizes drifted from the running total: sum %d, tracked %d", sum, ringBytes)
	}
	if count == 0 {
		t.Fatal("ring must not be emptied by a single large event")
	}
}

// The envelope estimate is allowed to be an upper bound, but never an
// underestimate: an event that is reported as fitting must actually fit, or an
// oversized event would reach subscribers unbounded.
func TestHubEnvelopeGrowthIsAnUpperBound(t *testing.T) {
	events := []Event{
		{Type: "plain", AgentID: "agent-1"},
		{Type: "text", AgentID: "agent-1", Text: strings.Repeat("x", 512)},
		{Type: "data", AgentID: "agent-1", Data: map[string]any{"k": strings.Repeat("y", 512)}},
		{Type: "cjk", AgentID: "agent-1", Text: strings.Repeat("測試", 256)},
	}
	for _, event := range events {
		before := hubEventSize(event)
		stamped := event
		stamped.Protocol = ProtocolVersion
		stamped.StreamSession = "session-abcdef0123456789"
		stamped.Sequence = 18446744073709551615
		stamped.CreatedAt = "2025-01-01T00:00:00.000000001Z"
		actual := hubEventSize(stamped)
		if predicted := before + hubEventEnvelopeMaxGrowth(stamped); predicted < actual {
			t.Fatalf("envelope growth underestimated for %q: predicted %d, actual %d", event.Type, predicted, actual)
		}
	}
}

func BenchmarkHubPublish(b *testing.B) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	hub := NewHubWithConfig(HubConfig{
		RingSize:   256,
		RingBytes:  1 << 20,
		Clock:      func() time.Time { return now },
		NewSession: sequenceSessions(),
	})
	event := Event{Type: "tool.output", AgentID: "agent-1", Text: strings.Repeat("a", 512)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Publish(event)
	}
}

// The eviction path is the part that was quadratic, so this keeps the ring
// saturated and forces an eviction on every publish.
func BenchmarkHubPublishWithEviction(b *testing.B) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	hub := NewHubWithConfig(HubConfig{
		RingSize:   64,
		RingBytes:  32 << 10,
		Clock:      func() time.Time { return now },
		NewSession: sequenceSessions(),
	})
	event := Event{Type: "tool.output", AgentID: "agent-1", Text: strings.Repeat("a", 1024)}
	for i := 0; i < 128; i++ {
		hub.Publish(event)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Publish(event)
	}
}
