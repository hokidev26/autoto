package providers

import (
	"context"
	"strings"
	"testing"
)

// collectReasoning drains a provider event channel into the reasoning text and
// the answer text, so each case can assert they never bleed into each other.
func collectReasoning(t *testing.T, events <-chan Event) (reasoning string, text string) {
	t.Helper()
	var reasoningParts, textParts []string
	for event := range events {
		switch event.Type {
		case "reasoning":
			reasoningParts = append(reasoningParts, event.Text)
		case "text":
			textParts = append(textParts, event.Text)
		}
	}
	return strings.Join(reasoningParts, ""), strings.Join(textParts, "")
}

func TestGrokResponsesStreamSeparatesReasoningFromAnswer(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Checking "}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"the styles."}`,
		`data: {"type":"response.output_text.delta","delta":"Done."}`,
		`data: [DONE]`,
		``,
	}, "\n")

	out := make(chan Event, 16)
	outcome := handleGrokResponsesStream(context.Background(), out, strings.NewReader(stream), func() bool { return true }, "grok")
	close(out)

	reasoning, text := collectReasoning(t, out)
	if reasoning != "Checking the styles." {
		t.Fatalf("unexpected reasoning: %q", reasoning)
	}
	if text != "Done." {
		t.Fatalf("unexpected answer text: %q", text)
	}
	if !outcome.emittedContent {
		t.Fatal("an answer delta must still count as emitted content")
	}
}

func TestGrokReasoningAloneIsNotEmittedContent(t *testing.T) {
	// A turn that only reasoned has produced no answer, so the retry and
	// account-fallback paths must still treat it as empty.
	stream := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Thinking."}`,
		`data: [DONE]`,
		``,
	}, "\n")

	out := make(chan Event, 8)
	outcome := handleGrokResponsesStream(context.Background(), out, strings.NewReader(stream), func() bool { return true }, "grok")
	close(out)

	reasoning, text := collectReasoning(t, out)
	if reasoning != "Thinking." {
		t.Fatalf("unexpected reasoning: %q", reasoning)
	}
	if text != "" {
		t.Fatalf("reasoning must not surface as answer text, got %q", text)
	}
	if outcome.emittedContent {
		t.Fatal("reasoning alone must not count as emitted content")
	}
}

func TestOpenAIReasoningSummaryDeltaReadsRawFrames(t *testing.T) {
	if got := openAIReasoningSummaryDelta(`{"type":"response.reasoning_summary_text.delta","delta":"Planning."}`); got != "Planning." {
		t.Fatalf("unexpected delta: %q", got)
	}
	if got := openAIReasoningSummaryDelta("not json"); got != "" {
		t.Fatalf("malformed frames must yield nothing, got %q", got)
	}
	if got := openAIReasoningSummaryDelta(""); got != "" {
		t.Fatalf("empty frames must yield nothing, got %q", got)
	}
}

// TestGeminiCloudCodeToolTurnReportsToolStopReason pins the fix for the failure
// the user hit: "你好" answered fine, and the next message -- the first one that
// made the model call a tool -- died with `provider returned unsafe tool stop
// reason "stop"`. Cloud Code reports STOP for tool turns too, so the adapter has
// to say what the turn actually was.
func TestGeminiCloudCodeToolTurnReportsToolStopReason(t *testing.T) {
	toolTurn := strings.Join([]string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	out := make(chan Event, 16)
	consumeGeminiCloudCodeAttempt(context.Background(), out, strings.NewReader(toolTurn), func() bool { return true }, "gemini-oauth")
	close(out)

	var stopReason string
	sawCall := false
	for event := range out {
		if event.Type == "tool_call" {
			sawCall = true
		}
		if event.Done {
			stopReason = event.StopReason
		}
	}
	if !sawCall {
		t.Fatal("the function call must still reach the runner")
	}
	if stopReason != "tool_use" {
		t.Fatalf("a turn carrying tool calls must report a tool stop reason, got %q", stopReason)
	}
}

func TestGeminiCloudCodeTextTurnKeepsPlainStopReason(t *testing.T) {
	// The rewrite must be narrow: a turn with no tool call still ends in "stop",
	// otherwise the runner would keep looping waiting for tool results.
	textTurn := strings.Join([]string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"Greeting back."},{"text":"你好"}]},"finishReason":"STOP"}]}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	out := make(chan Event, 16)
	consumeGeminiCloudCodeAttempt(context.Background(), out, strings.NewReader(textTurn), func() bool { return true }, "gemini-oauth")
	close(out)

	var stopReason string
	var reasoning, text []string
	for event := range out {
		switch {
		case event.Done:
			stopReason = event.StopReason
		case event.Type == "reasoning":
			reasoning = append(reasoning, event.Text)
		case event.Type == "text":
			text = append(text, event.Text)
		}
	}
	if stopReason != "stop" {
		t.Fatalf("a text-only turn must still stop, got %q", stopReason)
	}
	if strings.Join(reasoning, "") != "Greeting back." {
		t.Fatalf("thought parts must surface as reasoning, got %q", strings.Join(reasoning, ""))
	}
	if strings.Join(text, "") != "你好" {
		t.Fatalf("thought parts must not leak into the answer, got %q", strings.Join(text, ""))
	}
}
