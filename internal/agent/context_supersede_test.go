package agent

import (
	"testing"

	"autoto/internal/db"
)

// The supersede flag is only meaningful if the model stops seeing those turns.
// Without this the correction re-runs against the very history it retired.
func TestProviderContextSkipsSupersededMessages(t *testing.T) {
	messages := []db.Message{
		{ID: "1", Role: "user", ContentText: "first question"},
		{ID: "2", Role: "assistant", ContentText: "first answer"},
		{ID: "3", Role: "user", ContentText: "withdrawn question", SupersededAt: "2026-07-27T00:00:00Z"},
		{ID: "4", Role: "assistant", ContentText: "answer to a withdrawn question", SupersededAt: "2026-07-27T00:00:00Z"},
		{ID: "5", Role: "user", ContentText: "corrected question"},
	}

	built, eligible := providerMessagesForContextPlan(db.Agent{}, messages, 10)
	if len(built) != len(eligible) {
		t.Fatalf("built %d messages but %d eligibility flags", len(built), len(eligible))
	}
	texts := make([]string, 0, len(built))
	for _, message := range built {
		texts = append(texts, message.Content)
	}
	want := []string{"first question", "first answer", "corrected question"}
	if len(texts) != len(want) {
		t.Fatalf("provider context = %v, want %v", texts, want)
	}
	for i, expected := range want {
		if texts[i] != expected {
			t.Fatalf("provider context = %v, want %v", texts, want)
		}
	}
}

// Nothing superseded means nothing is dropped, so an ordinary conversation is
// untouched by the filter.
func TestProviderContextKeepsEveryLiveMessage(t *testing.T) {
	messages := []db.Message{
		{ID: "1", Role: "user", ContentText: "a"},
		{ID: "2", Role: "assistant", ContentText: "b"},
		{ID: "3", Role: "user", ContentText: "c"},
	}
	built, _ := providerMessagesForContextPlan(db.Agent{}, messages, 10)
	if len(built) != 3 {
		t.Fatalf("expected all 3 messages, got %d", len(built))
	}
}
