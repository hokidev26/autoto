package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeKiroRequest(t *testing.T, req GenerateRequest) kiroConversationState {
	t.Helper()
	body, err := buildKiroRequest(req, "claude-sonnet-4-5", "arn:aws:codewhisperer:us-east-1:000000000000:profile/test")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		ConversationState kiroConversationState `json:"conversationState"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.ConversationState
}

// The Kiro wire format only has user/assistant turns, but system-role
// messages carry run-critical instructions (continuation control, pipeline
// control, spec sidecar). Dropping them silently breaks continuation and
// pipeline behavior on Kiro, so their text must ride inside the adjacent
// user turn instead.
func TestKiroRequestKeepsSystemMessages(t *testing.T) {
	state := decodeKiroRequest(t, GenerateRequest{
		SystemPrompt: "Be concise.",
		Messages: []Message{
			{Role: "user", Content: "start the task"},
			{Role: "assistant", Content: "done with step one"},
			{Role: "user", Content: "keep going"},
			{Role: "system", TurnControl: true, Content: "<side_car>continuation control: resume after message-1</side_car>"},
		},
	})
	current := kiroMessageText(state.CurrentMessage.UserInputMessage)
	if !strings.Contains(current, "keep going") || !strings.Contains(current, "continuation control: resume after message-1") {
		t.Fatalf("trailing system control must ride in the current user message: %q", current)
	}
	if !strings.HasPrefix(kiroHistoryUserText(t, state, 0), "Be concise.") {
		t.Fatalf("system prompt must still prepend the first user message: %q", kiroHistoryUserText(t, state, 0))
	}
}

// A system message between turns (context summary) prepends to the next user
// message; a trailing control after an assistant reply becomes its own user
// turn rather than vanishing.
func TestKiroRequestPlacesSystemMessagesAdjacent(t *testing.T) {
	state := decodeKiroRequest(t, GenerateRequest{
		Messages: []Message{
			{Role: "system", Content: "server context summary: earlier work happened"},
			{Role: "user", Content: "continue the task"},
			{Role: "assistant", Content: "ok"},
			{Role: "system", TurnControl: true, Content: "<side_car>progress reminder</side_car>"},
		},
	})
	first := kiroHistoryUserText(t, state, 0)
	if !strings.Contains(first, "server context summary: earlier work happened") || !strings.Contains(first, "continue the task") {
		t.Fatalf("leading system message must prepend to the next user message: %q", first)
	}
	if index := strings.Index(first, "server context summary"); index > strings.Index(first, "continue the task") {
		t.Fatalf("system text must come before the user text: %q", first)
	}
	current := kiroMessageText(state.CurrentMessage.UserInputMessage)
	if !strings.Contains(current, "progress reminder") {
		t.Fatalf("trailing control after an assistant reply must become the current user turn: %q", current)
	}
}

func kiroMessageText(message kiroUserMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, content := range message.Content {
		if text := content["text"]; text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func kiroHistoryUserText(t *testing.T, state kiroConversationState, index int) string {
	t.Helper()
	if index >= len(state.History) {
		t.Fatalf("history index %d out of range (%d turns)", index, len(state.History))
	}
	return kiroMessageText(state.History[index].UserInputMessage)
}
