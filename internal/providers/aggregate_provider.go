package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

const (
	aggregateProviderPrefix   = "aggregate"
	AggregateStrategyPriority = "priority"
)

// AggregateDefinition is a runtime snapshot of an ordered model aggregate.
// Strategy and Mode are aliases; Mode is accepted for storage-facing adapters.
type AggregateDefinition struct {
	Name     string   `json:"name"`
	Strategy string   `json:"strategy,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Members  []string `json:"members"`
}

// AggregateSource resolves the latest aggregate definition for a name.
// Implementations must preserve member order.
type AggregateSource interface {
	ResolveAggregate(ctx context.Context, name string) (AggregateDefinition, error)
}

// AggregateSourceFunc adapts a function into an AggregateSource.
type AggregateSourceFunc func(ctx context.Context, name string) (AggregateDefinition, error)

func (f AggregateSourceFunc) ResolveAggregate(ctx context.Context, name string) (AggregateDefinition, error) {
	return f(ctx, name)
}

// AggregateProvider dynamically resolves an ordered set of complete
// provider:model references for every Generate call.
type AggregateProvider struct {
	registry        *Registry
	name            string
	fixedDefinition *AggregateDefinition
}

func newAggregateProvider(registry *Registry, name string) *AggregateProvider {
	return &AggregateProvider{registry: registry, name: strings.TrimSpace(name)}
}

// ResolveAggregateSnapshot creates an aggregate provider pinned to one validated
// definition. It is used at security boundaries that must dispatch exactly the
// members they already authorized rather than reloading a mutable source later.
func (r *Registry) ResolveAggregateSnapshot(definition AggregateDefinition) (Provider, error) {
	if r == nil {
		return nil, errors.New("aggregate provider registry is unavailable")
	}
	validated, err := validateAggregateDefinition(definition.Name, definition)
	if err != nil {
		return nil, err
	}
	validated.Members = append([]string(nil), validated.Members...)
	return &AggregateProvider{registry: r, name: validated.Name, fixedDefinition: &validated}, nil
}

func (p *AggregateProvider) Name() string {
	return aggregateProviderPrefix + ":" + p.name
}

func (p *AggregateProvider) Capabilities() Capabilities {
	// Tools and images are safe to accept because each candidate request is pruned
	// against that candidate's declared capabilities before dispatch. Reasoning
	// effort cannot be pruned equivalently: an unsupported concrete effort causes
	// adapters to reject the request. Advertise only the intersection that every
	// currently resolvable candidate can accept.
	capabilities := Capabilities{Tools: true, Streaming: true, ImageInput: true, ImageGeneration: true, NativeReasoningBlocks: true}
	definition, err := p.loadDefinition(context.Background())
	if err != nil {
		return capabilities
	}

	var shared []string
	for _, member := range definition.Members {
		candidate, _, err := p.resolveCandidate(member)
		if err != nil {
			return capabilities
		}
		candidateCapabilities := CapabilitiesFor(candidate)
		if !candidateCapabilities.ReasoningEffort || len(candidateCapabilities.ReasoningEfforts) == 0 {
			return capabilities
		}
		if shared == nil {
			shared = append([]string(nil), candidateCapabilities.ReasoningEfforts...)
			continue
		}
		shared = intersectReasoningEfforts(shared, candidateCapabilities.ReasoningEfforts)
		if len(shared) == 0 {
			return capabilities
		}
	}
	if len(shared) > 0 {
		capabilities.ReasoningEffort = true
		capabilities.ReasoningEfforts = shared
	}
	return capabilities
}

func intersectReasoningEfforts(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, effort := range right {
		rightSet[effort] = struct{}{}
	}
	shared := make([]string, 0, len(left))
	for _, effort := range left {
		if _, ok := rightSet[effort]; ok {
			shared = append(shared, effort)
		}
	}
	return shared
}

func (p *AggregateProvider) ListModels(ctx context.Context) ([]string, error) {
	definition, err := p.loadDefinition(ctx)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), definition.Members...), nil
}

func (p *AggregateProvider) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	definition, err := p.loadDefinition(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		for index, member := range definition.Members {
			candidate, model, resolveErr := p.resolveCandidate(member)
			canFallback := index+1 < len(definition.Members)
			if resolveErr != nil {
				if canFallback && aggregateFallbackAllowed(ctx, resolveErr) {
					continue
				}
				emitAggregateEvent(ctx, out, Event{Type: "error", Text: resolveErr.Error()})
				return
			}

			candidateReq := aggregateRequestForCapabilities(req, CapabilitiesFor(candidate), ModelCapabilitiesFor(candidate, model))
			candidateReq.Model = model
			events, generateErr := candidate.Generate(ctx, candidateReq)
			if generateErr != nil {
				if canFallback && aggregateFallbackAllowed(ctx, generateErr) {
					continue
				}
				emitAggregateEvent(ctx, out, Event{Type: "error", Text: generateErr.Error()})
				return
			}
			if events == nil {
				nilEventsErr := providerUnavailableError(candidate.Name(), "Generate returned no event stream")
				if canFallback {
					continue
				}
				emitAggregateEvent(ctx, out, Event{Type: "error", Text: nilEventsErr.Error()})
				return
			}
			if consumeAggregateCandidate(ctx, out, events, canFallback) {
				continue
			}
			return
		}
	}()
	return out, nil
}

func (p *AggregateProvider) loadDefinition(ctx context.Context) (AggregateDefinition, error) {
	if p == nil || p.registry == nil {
		return AggregateDefinition{}, errors.New("aggregate provider registry is unavailable")
	}
	if p.fixedDefinition != nil {
		definition := *p.fixedDefinition
		definition.Members = append([]string(nil), p.fixedDefinition.Members...)
		return definition, nil
	}
	source := p.registry.aggregateSourceSnapshot()
	if source == nil {
		return AggregateDefinition{}, errors.New("aggregate provider source is not configured")
	}
	definition, err := source.ResolveAggregate(ctx, p.name)
	if err != nil {
		return AggregateDefinition{}, fmt.Errorf("resolve aggregate %q: %w", p.name, err)
	}
	return validateAggregateDefinition(p.name, definition)
}

func validateAggregateDefinition(requestedName string, definition AggregateDefinition) (AggregateDefinition, error) {
	requestedName = strings.TrimSpace(requestedName)
	if requestedName == "" {
		return AggregateDefinition{}, errors.New("aggregate name must not be empty")
	}
	if len(requestedName) > 120 {
		return AggregateDefinition{}, errors.New("aggregate name exceeds 120 bytes")
	}
	definition.Name = strings.TrimSpace(definition.Name)
	if definition.Name == "" {
		definition.Name = requestedName
	}
	if definition.Name != requestedName {
		return AggregateDefinition{}, fmt.Errorf("aggregate source returned name %q for requested aggregate %q", definition.Name, requestedName)
	}

	strategy := strings.ToLower(strings.TrimSpace(definition.Strategy))
	mode := strings.ToLower(strings.TrimSpace(definition.Mode))
	if strategy != "" && mode != "" && strategy != mode {
		return AggregateDefinition{}, fmt.Errorf("aggregate %q has conflicting strategy %q and mode %q", requestedName, definition.Strategy, definition.Mode)
	}
	if strategy == "" {
		strategy = mode
	}
	if strategy == "" {
		strategy = AggregateStrategyPriority
	}
	if strategy != AggregateStrategyPriority {
		return AggregateDefinition{}, fmt.Errorf("aggregate %q has unsupported strategy %q", requestedName, strategy)
	}
	definition.Strategy = strategy
	definition.Mode = strategy

	if len(definition.Members) == 0 {
		return AggregateDefinition{}, fmt.Errorf("aggregate %q has no members", requestedName)
	}
	members := make([]string, 0, len(definition.Members))
	seen := make(map[string]struct{}, len(definition.Members))
	for index, member := range definition.Members {
		providerName, modelName, canonical, err := parseAggregateMember(member)
		if err != nil {
			return AggregateDefinition{}, fmt.Errorf("aggregate %q member %d: %w", requestedName, index, err)
		}
		if strings.EqualFold(providerName, aggregateProviderPrefix) {
			return AggregateDefinition{}, fmt.Errorf("aggregate %q member %d: nested aggregates are not allowed", requestedName, index)
		}
		if _, duplicate := seen[canonical]; duplicate {
			return AggregateDefinition{}, fmt.Errorf("aggregate %q contains duplicate member %q", requestedName, canonical)
		}
		seen[canonical] = struct{}{}
		members = append(members, providerName+":"+modelName)
	}
	definition.Members = members
	return definition, nil
}

func parseAggregateMember(member string) (providerName, modelName, canonical string, err error) {
	member = strings.TrimSpace(member)
	if member == "" {
		return "", "", "", errors.New("member must not be empty")
	}
	if len(member) > 256 {
		return "", "", "", errors.New("member exceeds 256 bytes")
	}
	parts := strings.SplitN(member, ":", 2)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("member %q must be a complete provider:model reference", member)
	}
	providerName = strings.TrimSpace(parts[0])
	modelName = strings.TrimSpace(parts[1])
	if providerName == "" || modelName == "" {
		return "", "", "", fmt.Errorf("member %q must be a complete provider:model reference", member)
	}
	canonical = providerName + ":" + modelName
	return providerName, modelName, canonical, nil
}

func (p *AggregateProvider) resolveCandidate(member string) (Provider, string, error) {
	providerName, modelName, _, err := parseAggregateMember(member)
	if err != nil {
		return nil, "", err
	}
	if strings.EqualFold(providerName, aggregateProviderPrefix) {
		return nil, "", errors.New("nested aggregates are not allowed")
	}
	provider, ok := p.registry.Get(providerName)
	if !ok {
		return nil, "", providerUnavailableError(providerName, "provider is not registered")
	}
	return provider, modelName, nil
}

func aggregateRequestForCapabilities(req GenerateRequest, capabilities Capabilities, modelCapabilities ModelCapabilities) GenerateRequest {
	if !capabilities.Tools {
		req.Tools = nil
	}
	if !modelCapabilities.FastMode {
		req.FastMode = false
	}
	if !capabilities.ImageGeneration || !modelCapabilities.ImageGeneration {
		req.EnableImageGeneration = false
	}
	// Aggregate definitions can change between catalog reads and dispatch. Keep
	// a direct aggregate caller safe as well by downgrading a now-unsupported
	// concrete effort to auto for this candidate; the aggregate's advertised
	// capabilities still expose only the stricter shared set.
	if !capabilities.SupportsReasoningEffort(req.ReasoningEffort) {
		req.ReasoningEffort = "auto"
		req.ReasoningBudgetTokens = 0
	}
	if capabilities.Tools && capabilities.ImageInput && capabilities.ImageGeneration && capabilities.NativeReasoningBlocks {
		return req
	}
	messages := make([]Message, len(req.Messages))
	for index, message := range req.Messages {
		messages[index] = message
		if len(message.Blocks) == 0 {
			continue
		}
		blocks := make([]ContentBlock, 0, len(message.Blocks))
		for _, block := range message.Blocks {
			switch block.Type {
			case ContentBlockTypeThinking, ContentBlockTypeRedactedThinking:
				if capabilities.NativeReasoningBlocks {
					blocks = append(blocks, block)
				}
			case "image":
				if capabilities.ImageInput {
					blocks = append(blocks, block)
					continue
				}
				name := strings.TrimSpace(block.Filename)
				if name == "" {
					name = "image"
				}
				blocks = append(blocks, ContentBlock{Type: "text", Text: fmt.Sprintf("[图片附件 %s 未发送：当前 Provider 不支持原生图片输入。]", name)})
			case "tool_use":
				if capabilities.Tools {
					blocks = append(blocks, block)
					continue
				}
				blocks = append(blocks, ContentBlock{Type: "text", Text: fmt.Sprintf("[历史工具调用 %s 未作为结构化工具消息发送。]", strings.TrimSpace(block.ToolName))})
			case "tool_result":
				if capabilities.Tools {
					blocks = append(blocks, block)
					continue
				}
				blocks = append(blocks, ContentBlock{Type: "text", Text: fmt.Sprintf("[历史工具结果 %s]\n%s", strings.TrimSpace(block.ToolName), block.Output)})
			case "image_generation":
				if capabilities.ImageGeneration && modelCapabilities.ImageGeneration && len(block.Data) > 0 {
					blocks = append(blocks, block)
					continue
				}
				blocks = append(blocks, ContentBlock{Type: "text", Text: "[历史图片生成结果未作为结构化消息发送。]"})
			default:
				block.Data = nil
				blocks = append(blocks, block)
			}
		}
		messages[index].Blocks = blocks
		if content := strings.TrimSpace(contentBlocksText(blocks)); content != "" {
			messages[index].Content = content
		}
	}
	req.Messages = messages
	return req
}

func consumeAggregateCandidate(ctx context.Context, out chan<- Event, events <-chan Event, canFallback bool) bool {
	pending := make([]Event, 0, 2)
	outputStarted := false
	flushPending := func() bool {
		for _, event := range pending {
			if !emitAggregateEvent(ctx, out, event) {
				return false
			}
		}
		pending = pending[:0]
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return false
		case event, ok := <-events:
			if !ok {
				flushPending()
				return false
			}
			switch event.Type {
			case "reasoning":
				// Forwarded but deliberately kept out of outputStarted: reasoning is
				// not an answer, so a candidate that only reasoned before failing must
				// still be allowed to fall back to the next account.
				if !emitAggregateEvent(ctx, out, event) {
					return false
				}
			case "text", "tool_call", "image_generation", "content_block":
				if !flushPending() {
					return false
				}
				outputStarted = true
				if !emitAggregateEvent(ctx, out, event) {
					return false
				}
			case "error":
				err := errors.New(strings.TrimSpace(event.Text))
				if canFallback && !outputStarted && aggregateFallbackAllowed(ctx, err) {
					return true
				}
				if !flushPending() {
					return false
				}
				emitAggregateEvent(ctx, out, event)
				return false
			case "done":
				if !flushPending() {
					return false
				}
				emitAggregateEvent(ctx, out, event)
				return false
			default:
				if outputStarted {
					if !emitAggregateEvent(ctx, out, event) {
						return false
					}
					continue
				}
				pending = append(pending, event)
			}
		}
	}
}

func emitAggregateEvent(ctx context.Context, out chan<- Event, event Event) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

var aggregateHTTPStatusPattern = regexp.MustCompile(`(^|[^0-9])([45][0-9]{2})([^0-9]|$)`)

func aggregateFallbackAllowed(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrProviderUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	if strings.Contains(message, "canceled") || strings.Contains(message, "cancelled") || strings.Contains(message, "unauthorized") || strings.Contains(message, "forbidden") || strings.Contains(message, "bad request") || strings.Contains(message, "invalid_request") || strings.Contains(message, "invalid request") {
		return false
	}
	if match := aggregateHTTPStatusPattern.FindStringSubmatch(message); len(match) > 2 {
		status := match[2]
		if status == "400" || status == "401" || status == "403" {
			return false
		}
		if status == "429" || strings.HasPrefix(status, "5") {
			return true
		}
		return false
	}
	for _, marker := range []string{
		"rate limit", "too many requests", "quota", "unavailable", "overloaded", "capacity",
		"timeout", "timed out", "deadline exceeded", "connection reset", "connection refused",
		"connection aborted", "broken pipe", "unexpected eof", " eof", "dial tcp", "no such host",
		"network error", "network is unreachable", "temporary network", "transport error", "i/o timeout", "tls handshake timeout",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
