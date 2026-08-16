package review

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/providers"
)

type scriptedProvider struct {
	mu       sync.Mutex
	requests []providers.GenerateRequest
	events   []providers.Event
	block    bool
}

func (p *scriptedProvider) Name() string { return "reviewer" }
func (p *scriptedProvider) ListModels(context.Context) ([]string, error) {
	return []string{"model"}, nil
}
func (p *scriptedProvider) Generate(ctx context.Context, request providers.GenerateRequest) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	events := append([]providers.Event(nil), p.events...)
	block := p.block
	p.mu.Unlock()
	out := make(chan providers.Event, len(events))
	go func() {
		defer close(out)
		if block {
			<-ctx.Done()
			return
		}
		for _, event := range events {
			select {
			case <-ctx.Done():
				return
			case out <- event:
			}
		}
	}()
	return out, nil
}
func (p *scriptedProvider) request() providers.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[0]
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func testDraft() PlanDraft {
	return PlanDraft{
		Goal:        "Implement the review boundary",
		Assumptions: []string{"Provider registry is configured"},
		Steps:       []string{"Add isolated reviewer"},
		Risks:       []string{"Malformed provider response"},
		Tests:       []string{"Run package tests"},
		Rollback:    []string{"Revert the isolated package"},
	}
}

func testService(provider *scriptedProvider, timeout time.Duration) *Service {
	registry := providers.NewRegistry()
	registry.Register(provider)
	return NewServiceWithConfig(registry, Config{Model: "reviewer:model", Timeout: timeout})
}

func TestServiceReviewerIDUsesConfiguredModelOrUnavailableFallback(t *testing.T) {
	if got := (*Service)(nil).ReviewerID(); got != "system:reviewer-unavailable" {
		t.Fatalf("nil reviewer identity = %q", got)
	}
	if got := NewServiceWithConfig(nil, Config{Model: " reviewer:model "}).ReviewerID(); got != "model:reviewer:model" {
		t.Fatalf("configured reviewer identity = %q", got)
	}
}

func TestServiceReviewUsesNoToolsAndParsesExactVerdict(t *testing.T) {
	provider := &scriptedProvider{events: []providers.Event{
		{Type: "text", Text: `{"verdict":"needs_human",`},
		{Type: "text", Text: `"reason":"The rollback needs an owner."}`},
		{Type: "done", Done: true},
	}}
	result, err := testService(provider, time.Second).Review(context.Background(), Request{Subject: "Review generated plan", Draft: testDraft()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictNeedsHuman || result.Reason != "The rollback needs an owner." {
		t.Fatalf("unexpected review result: %+v", result)
	}
	request := provider.request()
	if request.Tools != nil || len(request.Tools) != 0 {
		t.Fatalf("reviewer must not receive tools: %+v", request.Tools)
	}
	if request.SystemPrompt != reviewerSystemPrompt || len(request.Messages) != 1 {
		t.Fatalf("unexpected reviewer request: %+v", request)
	}
}

func TestReviewerSystemPromptRequiresNeedsHumanWhenGoalWasNotAchieved(t *testing.T) {
	if !strings.Contains(reviewerSystemPrompt, "isolated plan reviewer") {
		t.Fatal("reviewer prompt must keep the isolated reviewer identity")
	}
	if !strings.Contains(reviewerSystemPrompt, `"verdict":"pass|needs_human|block_recommended|unavailable"`) {
		t.Fatal("reviewer prompt must keep the closed verdict protocol")
	}
	if !strings.Contains(reviewerSystemPrompt, "needs_human") || !strings.Contains(reviewerSystemPrompt, "goal") {
		t.Fatal("reviewer prompt must mention needs_human and the stated goal")
	}
	if !strings.Contains(reviewerSystemPrompt, "was not achieved") {
		t.Fatal("reviewer prompt must require needs_human when the stated goal was not achieved")
	}
	if !strings.Contains(reviewerSystemPrompt, "must not authorize execution") {
		t.Fatal("reviewer prompt must still forbid authorizing execution")
	}
}

func TestServiceReviewFailsClosedForInvalidProtocol(t *testing.T) {
	for _, event := range []providers.Event{
		{Type: "text", Text: `{"verdict":"pass","reason":"ok","extra":true}`},
		{Type: "tool_call", ToolCall: &providers.ToolCall{Name: "Write"}},
	} {
		t.Run(event.Type, func(t *testing.T) {
			provider := &scriptedProvider{events: []providers.Event{event, {Type: "done", Done: true}}}
			result, err := testService(provider, time.Second).Review(context.Background(), Request{Subject: "Review generated plan", Draft: testDraft()})
			if err == nil {
				t.Fatal("expected fail-closed error")
			}
			if result.Verdict != VerdictUnavailable {
				t.Fatalf("invalid reviewer protocol must be unavailable, got %+v", result)
			}
		})
	}
}

func TestServiceReviewFailsClosedOnTimeout(t *testing.T) {
	provider := &scriptedProvider{block: true}
	result, err := testService(provider, 10*time.Millisecond).Review(context.Background(), Request{Subject: "Review generated plan", Draft: testDraft()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if result.Verdict != VerdictUnavailable {
		t.Fatalf("timeout must be unavailable, got %+v", result)
	}
}

func TestParsePlanDraftRequiresExactStructuredShape(t *testing.T) {
	valid := `{"goal":"Ship plan mode","assumptions":["DB API exists"],"steps":["Persist draft"],"risks":["Invalid output"],"tests":["go test"],"rollback":["Revert"]}`
	draft, err := ParsePlanDraft(valid)
	if err != nil || draft.Goal != "Ship plan mode" || len(draft.Steps) != 1 {
		t.Fatalf("valid plan rejected: draft=%+v err=%v", draft, err)
	}
	for _, invalid := range []string{
		`{"goal":"x","assumptions":[],"steps":[],"risks":[],"tests":[]}`,
		`{"goal":"x","assumptions":[],"steps":[],"risks":[],"tests":[],"rollback":[],"extra":true}`,
		`{"goal":"x","goal":"y","assumptions":[],"steps":[],"risks":[],"tests":[],"rollback":[]}`,
		`not-json`,
	} {
		if _, err := ParsePlanDraft(invalid); err == nil {
			t.Fatalf("invalid plan was accepted: %s", invalid)
		}
	}
}

func TestCandidateModelsSkipsBlankAndDuplicates(t *testing.T) {
	got := CandidateModels(" reviewer:a ", "", "reviewer:a", "reviewer:b", "  ")
	if len(got) != 2 || got[0] != "reviewer:a" || got[1] != "reviewer:b" {
		t.Fatalf("unexpected candidates: %+v", got)
	}
}

func TestReviewWithCandidatesFallsBackAfterResolveFailure(t *testing.T) {
	provider := &scriptedProvider{events: []providers.Event{
		{Type: "text", Text: `{"verdict":"pass","reason":"conversation model reviewed the plan."}`},
		{Type: "done", Done: true},
	}}
	registry := providers.NewRegistry()
	registry.Register(provider)
	result, reviewerID := ReviewWithCandidates(context.Background(), registry, []string{"missing:review", "reviewer:model"}, Request{Subject: "Review generated plan", Draft: testDraft()})
	if result.Verdict != VerdictPass || reviewerID != "model:reviewer:model" {
		t.Fatalf("resolve failure must fall back to the next model: result=%+v id=%s", result, reviewerID)
	}
	if provider.callCount() != 1 {
		t.Fatalf("expected one generate after the unresolvable candidate, got %d", provider.callCount())
	}
}

func TestReviewWithCandidatesFallsBackWhenProviderIsUnavailable(t *testing.T) {
	provider := &unavailableThenReadyProvider{ready: []providers.Event{
		{Type: "text", Text: `{"verdict":"pass","reason":"conversation model reviewed the plan."}`},
		{Type: "done", Done: true},
	}}
	registry := providers.NewRegistry()
	registry.Register(provider)
	result, reviewerID := ReviewWithCandidates(context.Background(), registry, []string{"reviewer:dedicated", "reviewer:model"}, Request{Subject: "Review generated plan", Draft: testDraft()})
	if result.Verdict != VerdictPass || reviewerID != "model:reviewer:model" {
		t.Fatalf("unconfigured provider must fall back to the next model: result=%+v id=%s", result, reviewerID)
	}
	if provider.calls != 2 {
		t.Fatalf("expected dedicated then conversation generate, got %d", provider.calls)
	}
}

func TestReviewWithCandidatesDoesNotFallBackOnMalformedJSON(t *testing.T) {
	provider := &scriptedProvider{events: []providers.Event{
		{Type: "text", Text: "not-json"},
		{Type: "done", Done: true},
	}}
	registry := providers.NewRegistry()
	registry.Register(provider)
	result, reviewerID := ReviewWithCandidates(context.Background(), registry, []string{"reviewer:broken", "reviewer:model"}, Request{Subject: "Review generated plan", Draft: testDraft()})
	if result.Verdict != VerdictUnavailable || result.Reason != "reviewer returned invalid JSON" || reviewerID != "model:reviewer:broken" {
		t.Fatalf("malformed JSON must stay fail-closed on the first model: result=%+v id=%s", result, reviewerID)
	}
	if provider.callCount() != 1 {
		t.Fatalf("must not try the next candidate after a protocol failure, got %d", provider.callCount())
	}
}

type unavailableThenReadyProvider struct {
	calls int
	ready []providers.Event
}

func (p *unavailableThenReadyProvider) Name() string { return "reviewer" }
func (p *unavailableThenReadyProvider) ListModels(context.Context) ([]string, error) {
	return []string{"dedicated", "model"}, nil
}
func (p *unavailableThenReadyProvider) Generate(_ context.Context, _ providers.GenerateRequest) (<-chan providers.Event, error) {
	p.calls++
	if p.calls == 1 {
		return nil, providers.ErrProviderUnavailable
	}
	out := make(chan providers.Event, len(p.ready))
	for _, event := range p.ready {
		out <- event
	}
	close(out)
	return out, nil
}
