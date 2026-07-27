package providers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type aggregateTestProvider struct {
	name      string
	caps      Capabilities
	modelCaps map[string]ModelCapabilities
	generate  func(GenerateRequest) ([]Event, error)

	mu       sync.Mutex
	requests []GenerateRequest
}

func (p *aggregateTestProvider) Name() string { return p.name }

func (p *aggregateTestProvider) Capabilities() Capabilities { return p.caps }

func (p *aggregateTestProvider) ModelCapabilities(model string) ModelCapabilities {
	return p.modelCaps[model]
}

func (p *aggregateTestProvider) ListModels(context.Context) ([]string, error) { return nil, nil }

func (p *aggregateTestProvider) Generate(_ context.Context, req GenerateRequest) (<-chan Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	var events []Event
	var err error
	if p.generate != nil {
		events, err = p.generate(req)
	}
	if err != nil {
		return nil, err
	}
	out := make(chan Event, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out, nil
}

func (p *aggregateTestProvider) requestSnapshot() []GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]GenerateRequest(nil), p.requests...)
}

type aggregateSequenceSource struct {
	mu          sync.Mutex
	definitions []AggregateDefinition
	calls       int
}

func (s *aggregateSequenceSource) ResolveAggregate(_ context.Context, name string) (AggregateDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.definitions) == 0 {
		return AggregateDefinition{}, errors.New("source unavailable")
	}
	index := s.calls
	if index >= len(s.definitions) {
		index = len(s.definitions) - 1
	}
	s.calls++
	definition := s.definitions[index]
	if definition.Name == "" {
		definition.Name = name
	}
	return definition, nil
}

func TestAggregateProviderFallsBackAndReloadsSourceEveryGenerate(t *testing.T) {
	primary := &aggregateTestProvider{name: "primary", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
		return []Event{{Type: "error", Text: "upstream returned 503 Service Unavailable"}}, nil
	}}
	secondary := &aggregateTestProvider{name: "secondary", caps: Capabilities{Streaming: true}, generate: func(req GenerateRequest) ([]Event, error) {
		return []Event{{Type: "text", Text: req.Model}, {Type: "done", Done: true}}, nil
	}}
	source := &aggregateSequenceSource{definitions: []AggregateDefinition{
		{Members: []string{"primary:model-a", "secondary:model-b"}},
		{Members: []string{"secondary:model-c"}},
	}}
	registry := NewRegistry()
	registry.Register(primary)
	registry.Register(secondary)
	registry.SetAggregateSource(source)

	provider, model, err := registry.Resolve("aggregate:fast")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "aggregate:fast" || model != "fast" {
		t.Fatalf("unexpected aggregate resolution: provider=%q model=%q", provider.Name(), model)
	}

	first := collectAggregateEvents(t, provider, GenerateRequest{})
	if got := aggregateEventText(first); got != "model-b" {
		t.Fatalf("expected fallback model output, got %q from %+v", got, first)
	}
	second := collectAggregateEvents(t, provider, GenerateRequest{})
	if got := aggregateEventText(second); got != "model-c" {
		t.Fatalf("expected refreshed aggregate definition, got %q from %+v", got, second)
	}
	if source.calls != 2 {
		t.Fatalf("expected source to be read for each Generate, got %d calls", source.calls)
	}
	if requests := primary.requestSnapshot(); len(requests) != 1 || requests[0].Model != "model-a" {
		t.Fatalf("unexpected primary requests: %+v", requests)
	}
	if requests := secondary.requestSnapshot(); len(requests) != 2 || requests[0].Model != "model-b" || requests[1].Model != "model-c" {
		t.Fatalf("unexpected secondary requests: %+v", requests)
	}
}

func TestAggregateProviderUsesReplacementSourceAfterResolve(t *testing.T) {
	candidate := &aggregateTestProvider{name: "candidate", caps: Capabilities{Streaming: true}, generate: func(req GenerateRequest) ([]Event, error) {
		return []Event{{Type: "text", Text: req.Model}, {Type: "done", Done: true}}, nil
	}}
	registry := NewRegistry()
	registry.Register(candidate)
	registry.SetAggregateSource(AggregateSourceFunc(func(context.Context, string) (AggregateDefinition, error) {
		return AggregateDefinition{Members: []string{"candidate:old"}}, nil
	}))
	provider, _, err := registry.Resolve("aggregate:dynamic")
	if err != nil {
		t.Fatal(err)
	}
	registry.SetAggregateSource(AggregateSourceFunc(func(context.Context, string) (AggregateDefinition, error) {
		return AggregateDefinition{Members: []string{"candidate:new"}}, nil
	}))
	if got := aggregateEventText(collectAggregateEvents(t, provider, GenerateRequest{})); got != "new" {
		t.Fatalf("resolved aggregate did not use replacement source: %q", got)
	}
}

func TestAggregateProviderDoesNotFallbackAfterOutput(t *testing.T) {
	primary := &aggregateTestProvider{name: "primary", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
		return []Event{{Type: "text", Text: "partial"}, {Type: "error", Text: "temporary 503 after output"}}, nil
	}}
	secondary := &aggregateTestProvider{name: "secondary", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
		return []Event{{Type: "text", Text: "must-not-run"}, {Type: "done", Done: true}}, nil
	}}
	provider := resolvedAggregateForTest(t, []Provider{primary, secondary}, []string{"primary:model-a", "secondary:model-b"})
	events := collectAggregateEvents(t, provider, GenerateRequest{})
	if aggregateEventText(events) != "partial" {
		t.Fatalf("unexpected aggregate output: %+v", events)
	}
	if !aggregateEventsContainError(events, "503") {
		t.Fatalf("expected original post-output error, got %+v", events)
	}
	if len(secondary.requestSnapshot()) != 0 {
		t.Fatalf("aggregate switched candidates after output: %+v", secondary.requestSnapshot())
	}
}

func TestAggregateProviderDoesNotFallbackOnNonRetryableErrors(t *testing.T) {
	for _, message := range []string{
		"400 Bad Request",
		"401 Unauthorized",
		"403 Forbidden",
		"context canceled",
	} {
		t.Run(strings.ReplaceAll(message, " ", "_"), func(t *testing.T) {
			primary := &aggregateTestProvider{name: "primary", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
				return []Event{{Type: "error", Text: message}}, nil
			}}
			secondary := &aggregateTestProvider{name: "secondary", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
				return []Event{{Type: "text", Text: "must-not-run"}}, nil
			}}
			provider := resolvedAggregateForTest(t, []Provider{primary, secondary}, []string{"primary:model-a", "secondary:model-b"})
			events := collectAggregateEvents(t, provider, GenerateRequest{})
			if !aggregateEventsContainError(events, message) {
				t.Fatalf("expected non-retryable error %q, got %+v", message, events)
			}
			if len(secondary.requestSnapshot()) != 0 {
				t.Fatalf("aggregate fell back for non-retryable error %q", message)
			}
		})
	}
}

func TestAggregateProviderReasoningCapabilitiesAreSafeIntersection(t *testing.T) {
	tests := []struct {
		name       string
		candidates []Provider
		members    []string
		want       []string
	}{
		{
			name: "all support legacy values",
			candidates: []Provider{
				&aggregateTestProvider{name: "first", caps: Capabilities{ReasoningEffort: true}},
				&aggregateTestProvider{name: "second", caps: Capabilities{ReasoningEffort: true}},
			},
			members: []string{"first:model", "second:model"},
			want:    []string{"low", "medium", "high"},
		},
		{
			name: "mixed support exposes only shared values",
			candidates: []Provider{
				&aggregateTestProvider{name: "first", caps: Capabilities{ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}}},
				&aggregateTestProvider{name: "second", caps: Capabilities{ReasoningEfforts: []string{"medium", "high"}}},
			},
			members: []string{"first:model", "second:model"},
			want:    []string{"medium", "high"},
		},
		{
			name: "no member supports reasoning",
			candidates: []Provider{
				&aggregateTestProvider{name: "first", caps: Capabilities{}},
				&aggregateTestProvider{name: "second", caps: Capabilities{}},
			},
			members: []string{"first:model", "second:model"},
			want:    nil,
		},
		{
			name: "xhigh remains when every member supports it",
			candidates: []Provider{
				&aggregateTestProvider{name: "first", caps: Capabilities{ReasoningEfforts: []string{"low", "xhigh"}}},
				&aggregateTestProvider{name: "second", caps: Capabilities{ReasoningEfforts: []string{"medium", "low", "xhigh"}}},
			},
			members: []string{"first:model", "second:model"},
			want:    []string{"low", "xhigh"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := resolvedAggregateForTest(t, test.candidates, test.members)
			capabilities := CapabilitiesFor(provider)
			if got := capabilities.ReasoningEfforts; !equalStringSlices(got, test.want) {
				t.Fatalf("unexpected reasoning efforts: got=%v want=%v capabilities=%+v", got, test.want, capabilities)
			}
			if capabilities.ReasoningEffort != (len(test.want) > 0) {
				t.Fatalf("unexpected reasoning capability flag: %+v", capabilities)
			}
		})
	}
}

func TestAggregateProviderPrunesToolsAndImagesPerCandidate(t *testing.T) {
	candidate := &aggregateTestProvider{name: "text-only", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
		return []Event{{Type: "done", Done: true}}, nil
	}}
	provider := resolvedAggregateForTest(t, []Provider{candidate}, []string{"text-only:model"})
	caps := CapabilitiesFor(provider)
	if !caps.Tools || !caps.Streaming || !caps.ImageInput || caps.ReasoningEffort {
		t.Fatalf("unexpected safe aggregate capabilities: %+v", caps)
	}
	collectAggregateEvents(t, provider, GenerateRequest{
		Tools: []ToolSpec{{Name: "Read"}},
		Messages: []Message{{Role: "user", Blocks: []ContentBlock{
			{Type: "text", Text: "inspect"},
			{Type: "image", Filename: "screen.png", Data: []byte{1, 2, 3}},
			{Type: "tool_use", ToolUseID: "call-1", ToolName: "Read"},
			{Type: "tool_result", ToolUseID: "call-1", ToolName: "Read", Output: "ok"},
		}}},
	})
	requests := candidate.requestSnapshot()
	if len(requests) != 1 {
		t.Fatalf("expected one candidate request, got %+v", requests)
	}
	request := requests[0]
	if len(request.Tools) != 0 {
		t.Fatalf("tools were not pruned: %+v", request.Tools)
	}
	if len(request.Messages) != 1 {
		t.Fatalf("unexpected messages: %+v", request.Messages)
	}
	for _, block := range request.Messages[0].Blocks {
		if block.Type == "image" || block.Type == "tool_use" || block.Type == "tool_result" || len(block.Data) != 0 {
			t.Fatalf("candidate received unsupported structured block: %+v", block)
		}
	}
	for _, marker := range []string{"screen.png", "历史工具调用", "历史工具结果"} {
		if !strings.Contains(request.Messages[0].Content, marker) {
			t.Fatalf("expected pruned message content to contain %q: %q", marker, request.Messages[0].Content)
		}
	}
}

func TestAggregateProviderFiltersNativeReasoningBlocksPerCandidate(t *testing.T) {
	native := &aggregateTestProvider{name: "native", caps: Capabilities{Streaming: true, NativeReasoningBlocks: true, ReasoningEfforts: []string{"low", "medium", "high"}}, generate: func(GenerateRequest) ([]Event, error) {
		return []Event{{Type: "done", Done: true}}, nil
	}}
	plain := &aggregateTestProvider{name: "plain", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
		return []Event{{Type: "done", Done: true}}, nil
	}}
	request := GenerateRequest{
		ReasoningEffort:       "high",
		ReasoningBudgetTokens: 4096,
		Messages: []Message{{Role: "assistant", Blocks: []ContentBlock{
			{Type: ContentBlockTypeThinking, ReasoningText: "private chain", ProviderState: []byte(`{"signature":"sig"}`)},
			{Type: ContentBlockTypeRedactedThinking, ProviderState: []byte(`{"data":"cipher"}`)},
			{Type: "text", Text: "answer"},
		}}},
	}
	collectAggregateEvents(t, resolvedAggregateForTest(t, []Provider{native}, []string{"native:model"}), request)
	collectAggregateEvents(t, resolvedAggregateForTest(t, []Provider{plain}, []string{"plain:model"}), request)
	if requests := native.requestSnapshot(); len(requests) != 1 || requests[0].ReasoningEffort != "high" || requests[0].ReasoningBudgetTokens != 4096 || len(requests[0].Messages[0].Blocks) != 3 {
		t.Fatalf("native candidate lost reasoning replay state: %+v", requests)
	}
	if requests := plain.requestSnapshot(); len(requests) != 1 || requests[0].ReasoningEffort != "auto" || requests[0].ReasoningBudgetTokens != 0 || len(requests[0].Messages[0].Blocks) != 1 || requests[0].Messages[0].Blocks[0].Type != "text" {
		t.Fatalf("plain candidate received native reasoning state: %+v", requests)
	}
}

func TestAggregateProviderFiltersImageGenerationPerCandidateModel(t *testing.T) {
	enabled := &aggregateTestProvider{
		name:      "enabled",
		caps:      Capabilities{Streaming: true, ImageGeneration: true},
		modelCaps: map[string]ModelCapabilities{"image-model": {ImageGeneration: true, ImageGenerationKnown: true}},
		generate: func(GenerateRequest) ([]Event, error) {
			return []Event{{Type: "done", Done: true}}, nil
		},
	}
	disabled := &aggregateTestProvider{
		name:      "disabled",
		caps:      Capabilities{Streaming: true, ImageGeneration: true},
		modelCaps: map[string]ModelCapabilities{"text-model": {ImageGenerationKnown: true}},
		generate: func(GenerateRequest) ([]Event, error) {
			return []Event{{Type: "done", Done: true}}, nil
		},
	}
	request := GenerateRequest{
		EnableImageGeneration: true,
		Messages:              []Message{{Role: "assistant", Blocks: []ContentBlock{{Type: "image_generation", GenerationID: "ig-history", Status: "completed", Data: []byte("png")}}}},
	}
	collectAggregateEvents(t, resolvedAggregateForTest(t, []Provider{enabled}, []string{"enabled:image-model"}), request)
	collectAggregateEvents(t, resolvedAggregateForTest(t, []Provider{disabled}, []string{"disabled:text-model"}), request)
	if requests := enabled.requestSnapshot(); len(requests) != 1 || !requests[0].EnableImageGeneration || len(requests[0].Messages) != 1 || len(requests[0].Messages[0].Blocks) != 1 || requests[0].Messages[0].Blocks[0].Type != "image_generation" {
		t.Fatalf("enabled model lost explicit image generation request or history: %+v", requests)
	}
	if requests := disabled.requestSnapshot(); len(requests) != 1 || requests[0].EnableImageGeneration || len(requests[0].Messages) != 1 || len(requests[0].Messages[0].Blocks) != 1 || requests[0].Messages[0].Blocks[0].Type != "text" || len(requests[0].Messages[0].Blocks[0].Data) != 0 {
		t.Fatalf("disabled model received image generation request or history: %+v", requests)
	}
}

func TestAggregateProviderTreatsImageGenerationAsStartedOutput(t *testing.T) {
	primary := &aggregateTestProvider{
		name:      "primary",
		caps:      Capabilities{Streaming: true, ImageGeneration: true},
		modelCaps: map[string]ModelCapabilities{"model-a": {ImageGeneration: true, ImageGenerationKnown: true}},
		generate: func(GenerateRequest) ([]Event, error) {
			return []Event{
				{Type: "image_generation", ImageGeneration: &ImageGeneration{GenerationID: "ig_1", Status: "generating"}},
				{Type: "error", Text: "temporary 503 after image output"},
			}, nil
		},
	}
	secondary := &aggregateTestProvider{name: "secondary", caps: Capabilities{Streaming: true}, generate: func(GenerateRequest) ([]Event, error) {
		return []Event{{Type: "text", Text: "must-not-run"}}, nil
	}}
	events := collectAggregateEvents(t, resolvedAggregateForTest(t, []Provider{primary, secondary}, []string{"primary:model-a", "secondary:model-b"}), GenerateRequest{EnableImageGeneration: true})
	if len(events) != 2 || events[0].Type != "image_generation" || !aggregateEventsContainError(events, "503") {
		t.Fatalf("unexpected aggregate image output sequence: %+v", events)
	}
	if len(secondary.requestSnapshot()) != 0 {
		t.Fatalf("aggregate fell back after image generation output: %+v", secondary.requestSnapshot())
	}
}

func TestAggregateSourceDefinitionsAreValidated(t *testing.T) {
	cases := []struct {
		name       string
		definition AggregateDefinition
		want       string
	}{
		{name: "no-members", definition: AggregateDefinition{}, want: "no members"},
		{name: "empty-member", definition: AggregateDefinition{Members: []string{"primary:model", " "}}, want: "must not be empty"},
		{name: "incomplete-member", definition: AggregateDefinition{Members: []string{"model-only"}}, want: "provider:model"},
		{name: "nested", definition: AggregateDefinition{Members: []string{"aggregate:other"}}, want: "nested aggregates"},
		{name: "duplicate", definition: AggregateDefinition{Members: []string{"primary:model", " primary : model "}}, want: "duplicate"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			registry.SetAggregateSource(AggregateSourceFunc(func(context.Context, string) (AggregateDefinition, error) {
				return test.definition, nil
			}))
			provider, _, err := registry.Resolve("aggregate:test")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Generate(context.Background(), GenerateRequest{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected validation error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestAggregateProviderPreservesGatewayScenarioAndConcreteDispatch(t *testing.T) {
	candidate := &aggregateTestProvider{name: "member", caps: Capabilities{Streaming: true}, generate: func(req GenerateRequest) ([]Event, error) {
		if req.EffectiveScenario() != CallScenarioGateway {
			t.Fatalf("aggregate changed scenario: %+v", req)
		}
		return []Event{newDispatchEvent("member", req.Model, "credential-1"), {Type: "done", Done: true}}, nil
	}}
	provider := resolvedAggregateForTest(t, []Provider{candidate}, []string{"member:model-a"})
	events := collectAggregateEvents(t, provider, GenerateRequest{Scenario: CallScenarioGateway})
	if len(events) != 2 || events[0].Dispatch == nil || *events[0].Dispatch != (DispatchInfo{Provider: "member", Model: "model-a", CredentialID: "credential-1"}) {
		t.Fatalf("aggregate did not propagate concrete dispatch: %+v", events)
	}
}

func resolvedAggregateForTest(t *testing.T, candidates []Provider, members []string) Provider {
	t.Helper()
	registry := NewRegistry()
	for _, candidate := range candidates {
		registry.Register(candidate)
	}
	registry.SetAggregateSource(AggregateSourceFunc(func(context.Context, string) (AggregateDefinition, error) {
		return AggregateDefinition{Members: members}, nil
	}))
	provider, _, err := registry.Resolve("aggregate:test")
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func collectAggregateEvents(t *testing.T, provider Provider, req GenerateRequest) []Event {
	t.Helper()
	events, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var collected []Event
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

func aggregateEventText(events []Event) string {
	var text strings.Builder
	for _, event := range events {
		if event.Type == "text" {
			text.WriteString(event.Text)
		}
	}
	return text.String()
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func aggregateEventsContainError(events []Event, fragment string) bool {
	for _, event := range events {
		if event.Type == "error" && strings.Contains(event.Text, fragment) {
			return true
		}
	}
	return false
}
