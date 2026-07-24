package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type Dispatcher interface {
	Dispatch(context.Context, Event) ([]ExecutionResult, error)
}

type gatewayEventContextKey struct{}

func ContextWithEvent(ctx context.Context, event Event) context.Context {
	return context.WithValue(ctx, gatewayEventContextKey{}, event)
}

func EventFromContext(ctx context.Context) (Event, bool) {
	if ctx == nil {
		return Event{}, false
	}
	event, ok := ctx.Value(gatewayEventContextKey{}).(Event)
	return event, ok
}

type ShellRequest struct {
	Executable string
	Args       []string
	CWD        string
	Env        map[string]string
	SecretRefs map[string]string
	Stdin      []byte
	Timeout    time.Duration
}

type HTTPRequest struct {
	URL        string
	Method     string
	Headers    map[string]string
	SecretRefs map[string]string
	Body       []byte
	Timeout    time.Duration
}

type LLMRequest struct {
	Model           string
	Prompt          string
	EventJSON       []byte
	MaxOutputTokens int
	Timeout         time.Duration
	ResponseFormat  string
}

type GatewayResult struct {
	Output     []byte
	StatusCode int
	Metadata   map[string]string
}

type Gateway interface {
	ExecuteShell(context.Context, ShellRequest) (GatewayResult, error)
	ExecuteHTTP(context.Context, HTTPRequest) (GatewayResult, error)
	ExecuteLLM(context.Context, LLMRequest) (GatewayResult, error)
}

type ExecutionResult struct {
	HookID   string          `json:"hookId"`
	Action   ActionKind      `json:"action"`
	Output   json.RawMessage `json:"output,omitempty"`
	Gate     *GateDecision   `json:"gate,omitempty"`
	Duration time.Duration   `json:"duration"`
}

type Executor struct {
	Gateway Gateway
	Limiter *Limiter
}

func (executor Executor) Execute(ctx context.Context, hook Hook, event Event) (ExecutionResult, error) {
	if executor.Gateway == nil {
		return ExecutionResult{}, errors.New("hook gateway is unavailable")
	}
	canonical, err := NormalizeAndValidateHook(hook)
	if err != nil {
		return ExecutionResult{}, err
	}
	if !Matches(canonical, event) {
		return ExecutionResult{}, errors.New("hook does not match event")
	}
	limiter := executor.Limiter
	if limiter == nil {
		limiter = NewLimiter(4, 8)
	}
	limitedContext, release, err := limiter.Acquire(ctx)
	if err != nil {
		return ExecutionResult{}, err
	}
	defer release()
	stdin, err := CanonicalEventJSON(event)
	if err != nil {
		return ExecutionResult{}, err
	}
	started := time.Now()
	result := ExecutionResult{HookID: canonical.ID, Action: canonical.Action.Kind}
	var gatewayResult GatewayResult
	limitedContext = ContextWithEvent(limitedContext, event)
	switch canonical.Action.Kind {
	case ActionShell:
		action := canonical.Action.Shell
		gatewayResult, err = executor.Gateway.ExecuteShell(limitedContext, ShellRequest{Executable: action.Executable, Args: append([]string(nil), action.Args...), CWD: action.CWD, Env: cloneMap(action.Env), SecretRefs: cloneMap(action.SecretRefs), Stdin: stdin, Timeout: time.Duration(action.TimeoutSeconds) * time.Second})
	case ActionHTTP:
		action := canonical.Action.HTTP
		gatewayResult, err = executor.Gateway.ExecuteHTTP(limitedContext, HTTPRequest{URL: action.URL, Method: action.Method, Headers: cloneMap(action.Headers), SecretRefs: cloneMap(action.SecretRefs), Body: stdin, Timeout: time.Duration(action.TimeoutSeconds) * time.Second})
	case ActionLLM:
		action := canonical.Action.LLM
		gatewayResult, err = executor.Gateway.ExecuteLLM(limitedContext, LLMRequest{Model: action.Model, Prompt: action.Prompt, EventJSON: stdin, MaxOutputTokens: action.MaxOutputTokens, Timeout: time.Duration(action.TimeoutSeconds) * time.Second, ResponseFormat: `{"decision":"allow|deny","reason":"string?"}`})
		if err == nil {
			decision, parseErr := ParseGateDecision(gatewayResult.Output)
			if parseErr != nil {
				err = parseErr
			} else {
				result.Gate = &decision
			}
		}
	}
	result.Duration = time.Since(started)
	if err != nil {
		return result, fmt.Errorf("hook action via gateway failed: %w", err)
	}
	result.Output = RedactJSON(gatewayResult.Output)
	return result, nil
}

func CanonicalEventJSON(event Event) ([]byte, error) {
	if event.Name != EventRunBefore && event.Name != EventRunAfter && event.Name != EventToolBefore && event.Name != EventToolAfter {
		return nil, errors.New("invalid lifecycle event")
	}
	if strings.TrimSpace(event.RunID) == "" {
		return nil, errors.New("event run id is required")
	}
	occurredAt := event.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	return json.Marshal(CanonicalEventStdin{SchemaVersion: 1, EventID: event.ID, Event: event.Name, RunID: event.RunID, ProjectID: event.ProjectID, AgentID: event.AgentID, RunKind: event.RunKind, ToolName: event.ToolName, OccurredAt: occurredAt.UTC().Format(time.RFC3339Nano), Attributes: cloneMap(event.Attributes), Payload: cloneRawMap(event.Payload)})
}

func ParseGateDecision(raw []byte) (GateDecision, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decision GateDecision
	if err := decoder.Decode(&decision); err != nil {
		return GateDecision{}, errors.New("LLM gate must return strict JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return GateDecision{}, errors.New("LLM gate must return exactly one JSON object")
	}
	if decision.Decision != "allow" && decision.Decision != "deny" {
		return GateDecision{}, errors.New("LLM gate decision must be allow or deny")
	}
	decision.Reason = strings.TrimSpace(RedactText(decision.Reason))
	if len(decision.Reason) > 2000 {
		return GateDecision{}, errors.New("LLM gate reason is too long")
	}
	return decision, nil
}

type limiterDepthKey struct{}

type Limiter struct {
	maxDepth  int
	semaphore chan struct{}
	once      sync.Once
}

func NewLimiter(maxDepth, maxConcurrent int) *Limiter {
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Limiter{maxDepth: maxDepth, semaphore: make(chan struct{}, maxConcurrent)}
}

func (limiter *Limiter) Acquire(ctx context.Context) (context.Context, func(), error) {
	if limiter == nil {
		return ctx, func() {}, nil
	}
	depth, _ := ctx.Value(limiterDepthKey{}).(int)
	if depth >= limiter.maxDepth {
		return nil, nil, errors.New("hook recursion limit exceeded")
	}
	select {
	case limiter.semaphore <- struct{}{}:
		return context.WithValue(ctx, limiterDepthKey{}, depth+1), func() { <-limiter.semaphore }, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func cloneMap[T ~string](input map[string]T) map[string]T {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]T, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
func cloneRawMap(input map[string]json.RawMessage) map[string]json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		output[key] = append(json.RawMessage(nil), value...)
	}
	return output
}
