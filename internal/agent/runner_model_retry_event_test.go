package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/providers"
)

// The inner retry loop absorbs a transient fault inside a single segment -- a
// first-token timeout is the everyday case -- and it used to do so in silence.
// The composer had nothing to show for the backoff, so a run that was recovering
// looked identical to one that had hung. The client already renders
// agent.provider_error_retry for the continuation-level retry, so the fix is for
// this loop to say the same thing.
func collectRetryEvents(t *testing.T, subscription Subscriber, want int) []Event {
	t.Helper()
	var retries []Event
	deadline := time.After(3 * time.Second)
	for len(retries) < want {
		select {
		case event, ok := <-subscription:
			if !ok {
				t.Fatalf("event stream closed after %d retry events", len(retries))
			}
			if event.Type == "agent.provider_error_retry" {
				retries = append(retries, event)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %d retry events, saw %d", want, len(retries))
		}
	}
	return retries
}

func TestRunModelTurnAnnouncesTransientRetry(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := hub.Subscribe(ctx, "agent-1")
	// Fails transiently once, then answers. One retry, one announcement.
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "error", Text: "provider first token timeout after 60000ms"}},
		{{Type: "text", Text: "recovered"}, {Type: "done", Done: true}},
	}}
	runner := &Runner{hub: hub, cfg: config.AgentConfig{MaxTransientRetries: 2}}

	result, err := runner.runModelTurn(ctx, "agent-1", "run-1", provider, "test", "", nil, nil, "auto", false)
	if err != nil {
		t.Fatalf("the turn recovered, so it must not return an error: %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("unexpected text after retry: %q", result.Text)
	}

	retries := collectRetryEvents(t, subscription, 1)
	data := retries[0].Data
	if got, _ := data["attempt"].(int); got != 1 {
		t.Fatalf("expected the first retry to be announced as attempt 1, got %v", data["attempt"])
	}
	// The ceiling is total attempts, not the retry count: 2 retries means a first
	// try plus 2, so a reader counting "1/3" is counting the same thing the
	// setting describes.
	if got, _ := data["maxAttempts"].(int); got != 3 {
		t.Fatalf("expected 3 total attempts, got %v", data["maxAttempts"])
	}
	if got, _ := data["runId"].(string); got != "run-1" {
		t.Fatalf("the event has to name its run so the client can scope it, got %v", data["runId"])
	}
	if got, _ := data["scope"].(string); got != "model_turn" {
		t.Fatalf("expected the model_turn scope so this is distinguishable from the segment-level retry, got %v", data["scope"])
	}
	// The reason the wait exists, in the text the user reads.
	if detail, _ := data["error"].(string); !strings.Contains(detail, "first token timeout") {
		t.Fatalf("expected the provider fault in the event, got %q", detail)
	}
	if backoff, _ := data["backoffMs"].(int64); backoff <= 0 {
		t.Fatalf("expected a positive backoff so the client can say how long the wait is, got %v", data["backoffMs"])
	}
}

func TestRunModelTurnRetryOmitsCeilingWhenUnlimited(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := hub.Subscribe(ctx, "agent-1")
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "error", Text: "temporary 502"}},
		{{Type: "text", Text: "ok"}, {Type: "done", Done: true}},
	}}
	// Negative is the unlimited sentinel.
	runner := &Runner{hub: hub, cfg: config.AgentConfig{MaxTransientRetries: -1}}

	if _, err := runner.runModelTurn(ctx, "agent-1", "run-1", provider, "test", "", nil, nil, "auto", false); err != nil {
		t.Fatal(err)
	}

	retries := collectRetryEvents(t, subscription, 1)
	// There is no total to count towards, so none is claimed. Reporting 1 here is
	// what produced "retrying 5/1" on screen.
	if got, _ := retries[0].Data["maxAttempts"].(int); got != 0 {
		t.Fatalf("unlimited retries must report no ceiling, got %v", retries[0].Data["maxAttempts"])
	}
	if got, _ := retries[0].Data["attempt"].(int); got != 1 {
		t.Fatalf("the attempt number is still counted, got %v", retries[0].Data["attempt"])
	}
}

func TestRunModelTurnAnnouncesEveryRetryAndStaysSilentOnSuccess(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := hub.Subscribe(ctx, "agent-1")
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "error", Text: "temporary 503"}},
		{{Type: "error", Text: "temporary 503 again"}},
		{{Type: "text", Text: "third time"}, {Type: "done", Done: true}},
	}}
	runner := &Runner{hub: hub, cfg: config.AgentConfig{MaxTransientRetries: 3}}

	if _, err := runner.runModelTurn(ctx, "agent-1", "run-1", provider, "test", "", nil, nil, "auto", false); err != nil {
		t.Fatal(err)
	}

	retries := collectRetryEvents(t, subscription, 2)
	if first, _ := retries[0].Data["attempt"].(int); first != 1 {
		t.Fatalf("expected attempt 1 first, got %v", retries[0].Data["attempt"])
	}
	if second, _ := retries[1].Data["attempt"].(int); second != 2 {
		t.Fatalf("expected attempt 2 second, got %v", retries[1].Data["attempt"])
	}
	// Backoff grows, which is the other half of what the wait is: a client that
	// shows the same delay for every attempt understates a long recovery.
	firstBackoff, _ := retries[0].Data["backoffMs"].(int64)
	secondBackoff, _ := retries[1].Data["backoffMs"].(int64)
	if firstBackoff <= 0 || secondBackoff <= 0 {
		t.Fatalf("expected positive backoffs, got %v and %v", firstBackoff, secondBackoff)
	}

	// A turn that never failed announces nothing: the status line must not flicker
	// through "retrying" on an ordinary answer.
	quietHub := NewHub()
	quietCtx, quietCancel := context.WithCancel(context.Background())
	defer quietCancel()
	quietSubscription := quietHub.Subscribe(quietCtx, "agent-2")
	quietProvider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "text", Text: "first try"}, {Type: "done", Done: true}},
	}}
	quietRunner := &Runner{hub: quietHub, cfg: config.AgentConfig{MaxTransientRetries: 3}}
	if _, err := quietRunner.runModelTurn(quietCtx, "agent-2", "run-2", quietProvider, "test", "", nil, nil, "auto", false); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event, ok := <-quietSubscription:
			if !ok {
				return
			}
			if event.Type == "agent.provider_error_retry" {
				t.Fatal("a turn that succeeded first time must not announce a retry")
			}
			if event.Type == "model.completed" {
				return
			}
		case <-time.After(time.Second):
			return
		}
	}
}
