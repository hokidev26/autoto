package agent

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/db"
	"autoto/internal/providers"
)

const maxImageGenerationEventPromptRunes = 1024

type modelTurnResult struct {
	Text                   string
	ToolCalls              []providers.ToolCall
	GeneratedImages        []providers.ImageGeneration
	ImageGenerationStarted bool
	Usage                  providers.Usage
	Dispatch               providers.DispatchInfo
	TurnUsage              *db.MessageTurnUsage
	StopReason             string
	StartedAt              time.Time
	FirstOutputAt          time.Time
	CompletedAt            time.Time
	Duration               time.Duration
	RecordAPIRequest       bool
	EstimatedOutputRunes   int64
}

func (r *Runner) runModelTurn(ctx context.Context, agentID, runID string, provider providers.Provider, model, systemPrompt string, messages []providers.Message, toolSpecs []providers.ToolSpec, reasoningEffort string, fastMode bool) (modelTurnResult, error) {
	maxRetries := r.cfg.MaxTransientRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err, retryable := r.runModelTurnAttempt(ctx, agentID, runID, provider, model, systemPrompt, messages, toolSpecs, reasoningEffort, fastMode)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil || !retryable || attempt == maxRetries {
			return result, err
		}
		backoff := modelRetryBackoff(attempt)
		slog.Warn("retrying transient provider error", "agentId", agentID, "provider", provider.Name(), "model", model, "attempt", attempt+1, "maxRetries", maxRetries, "backoff", backoff.String(), "error", err)
		select {
		case <-ctx.Done():
			return modelTurnResult{}, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return modelTurnResult{}, lastErr
}

func (r *Runner) runModelTurnAttempt(ctx context.Context, agentID, runID string, provider providers.Provider, model, systemPrompt string, messages []providers.Message, toolSpecs []providers.ToolSpec, reasoningEffort string, fastMode bool) (modelTurnResult, error, bool) {
	started := time.Now()
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	capabilities := providers.CapabilitiesFor(provider)
	if !capabilities.SupportsReasoningEffort(reasoningEffort) {
		return modelTurnResult{}, fmt.Errorf("%w: provider %q does not support requested effort %q", providers.ErrReasoningEffortUnsupported, provider.Name(), reasoningEffort), false
	}
	modelCapabilities := providers.ModelCapabilitiesFor(provider, model)
	fastModeAllowed := false
	if _, ok := provider.(providers.ModelCapabilityProvider); ok && fastMode {
		fastModeAllowed = !modelCapabilities.FastModeKnown || modelCapabilities.FastMode
	}
	requestCapabilities := capabilities
	requestCapabilities.ImageGeneration = capabilities.ImageGeneration && modelCapabilities.ImageGeneration
	requestMessages := enforceProviderMediaBudget(prepareProviderMessagesForCapabilities(messages, requestCapabilities))
	requestTools := toolSpecs
	if !capabilities.Tools {
		requestTools = nil
	}
	if limit := r.contextTokenLimit(model); limit > 0 {
		if estimated := estimateRequestTokens(systemPrompt, requestMessages, requestTools); estimated > limit {
			return modelTurnResult{}, errorsContextBudget(limit, estimated), false
		}
	}
	request := providers.GenerateRequest{Model: model, SystemPrompt: systemPrompt, Messages: requestMessages, Tools: requestTools, ReasoningEffort: reasoningEffort, FastMode: fastModeAllowed, EnableImageGeneration: capabilities.ImageGeneration && modelCapabilities.ImageGeneration, Scenario: providers.CallScenarioInternal}
	if capabilities.Reasoning {
		request.ReasoningEffort = agentReasoningEffort(ctx, r.store, agentID)
	}
	requestID := db.NewID()
	r.publish(Event{Type: "model.started", AgentID: agentID, Data: mergeEventData(map[string]any{
		"requestId": requestID,
		"provider":  provider.Name(),
		"model":     model,
		"startedAt": started.UTC().Format(time.RFC3339Nano),
	}, runID)})
	events, err := provider.Generate(attemptCtx, request)
	if err != nil {
		r.recordAPIRequest(agentID, runID, "", provider.Name(), model, "", time.Since(started), 0, providers.Usage{}, err.Error())
		return modelTurnResult{}, err, isTransientProviderError(err)
	}

	var result modelTurnResult
	var builder strings.Builder
	var firstOutputAt time.Time
	var outputRunes int64
	modelOutputStarted := false
	generatedImageIndexes := make(map[string]int)
	firstEventTimer, stopFirstEventTimer := firstEventTimeoutTimer(r.cfg.FirstTokenTimeoutMs)
	defer stopFirstEventTimer()
	markModelOutput := func(outputAt time.Time) {
		if firstOutputAt.IsZero() {
			firstOutputAt = outputAt
			stopFirstEventTimer()
		}
	}
	publishStreamingUsage := func() {
		pending := modelTurnUsage(providers.Usage{}, outputRunes, started, firstOutputAt, time.Since(started))
		r.publish(Event{Type: "model.streaming", AgentID: agentID, Data: mergeEventData(map[string]any{
			"requestId":         requestID,
			"provider":          provider.Name(),
			"model":             model,
			"firstOutputAt":     firstOutputAt.UTC().Format(time.RFC3339Nano),
			"pendingThroughput": pending,
		}, runID)})
	}
	finalize := func(record bool) modelTurnResult {
		completedAt := time.Now()
		duration := completedAt.Sub(started)
		result.Text = builder.String()
		result.StartedAt = started
		result.FirstOutputAt = firstOutputAt
		result.CompletedAt = completedAt
		result.Duration = duration
		result.RecordAPIRequest = record
		result.EstimatedOutputRunes = outputRunes
		result.TurnUsage = modelTurnUsage(result.Usage, outputRunes, started, firstOutputAt, duration)
		data := map[string]any{
			"requestId":   requestID,
			"provider":    provider.Name(),
			"model":       model,
			"startedAt":   started.UTC().Format(time.RFC3339Nano),
			"completedAt": completedAt.UTC().Format(time.RFC3339Nano),
			"throughput":  result.TurnUsage,
			"ttftMs":      result.TurnUsage.TTFTMS,
		}
		if !firstOutputAt.IsZero() {
			data["firstOutputAt"] = firstOutputAt.UTC().Format(time.RFC3339Nano)
		}
		r.publish(Event{Type: "model.completed", AgentID: agentID, Data: mergeEventData(data, runID)})
		return result
	}
	for {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			r.recordAttributedAPIRequest(agentID, runID, "", provider.Name(), model, result.Dispatch, time.Since(started), modelTurnTTFTMS(started, firstOutputAt), result.Usage, err.Error())
			if modelOutputStarted {
				return finalize(false), err, false
			}
			return modelTurnResult{}, err, false
		case <-firstEventTimer:
			err := &ProviderError{Message: fmt.Sprintf("provider first token timeout after %dms", r.cfg.FirstTokenTimeoutMs)}
			cancel()
			r.recordAttributedAPIRequest(agentID, runID, "", provider.Name(), model, result.Dispatch, time.Since(started), 0, result.Usage, err.Error())
			return modelTurnResult{}, err, true
		case event, ok := <-events:
			if !ok {
				return finalize(true), nil, false
			}
			if event.Dispatch != nil {
				result.Dispatch = *event.Dispatch
			}
			switch event.Type {
			case "text":
				if event.Text == "" {
					continue
				}
				markModelOutput(time.Now())
				modelOutputStarted = true
				outputRunes += int64(utf8.RuneCountInString(event.Text))
				builder.WriteString(event.Text)
				r.publish(Event{Type: "agent.text", AgentID: agentID, Text: event.Text, Data: mergeEventData(map[string]any{"requestId": requestID}, runID)})
				publishStreamingUsage()
			case "tool_call":
				if !capabilities.Tools {
					err := &ProviderError{Message: "provider emitted a tool call without declaring tool capability"}
					r.recordAttributedAPIRequest(agentID, runID, "", provider.Name(), model, result.Dispatch, time.Since(started), modelTurnTTFTMS(started, firstOutputAt), result.Usage, err.Error())

					return modelTurnResult{}, err, false
				}
				if event.ToolCall != nil {
					markModelOutput(time.Now())
					modelOutputStarted = true
					toolCall := normalizeProviderToolCall(*event.ToolCall)
					result.ToolCalls = append(result.ToolCalls, toolCall)
					outputRunes += estimatedToolCallOutputRunes(toolCall)
					publishStreamingUsage()
				}
			case "image_generation":
				if event.ImageGeneration == nil {
					continue
				}
				image := *event.ImageGeneration
				status, recognized := normalizeImageGenerationStatus(image.Status)
				if !recognized {
					continue
				}
				image.Status = status
				result.ImageGenerationStarted = true
				markModelOutput(time.Now())
				modelOutputStarted = true
				statusData := map[string]any{
					"requestId":    requestID,
					"generationId": strings.TrimSpace(image.GenerationID),
					"status":       status,
					"outputIndex":  image.OutputIndex,
					"partialIndex": image.PartialIndex,
				}
				if revisedPrompt := truncateRunes(image.RevisedPrompt, maxImageGenerationEventPromptRunes); revisedPrompt != "" {
					statusData["revisedPrompt"] = revisedPrompt
				}
				r.publish(Event{Type: "image_generation.status", AgentID: agentID, Data: mergeEventData(statusData, runID)})
				if status == "completed" && len(image.Data) > 0 {
					image.Data = append([]byte(nil), image.Data...)
					key := fmt.Sprintf("%s:%d", strings.TrimSpace(image.GenerationID), image.OutputIndex)
					if index, exists := generatedImageIndexes[key]; exists {
						result.GeneratedImages[index] = image
					} else {
						generatedImageIndexes[key] = len(result.GeneratedImages)
						result.GeneratedImages = append(result.GeneratedImages, image)
					}
				}
			case "usage":
				if event.Usage != nil {
					result.Usage = *event.Usage
				}
			case "error":
				err := &ProviderError{Message: event.Text}
				r.recordAttributedAPIRequest(agentID, runID, "", provider.Name(), model, result.Dispatch, time.Since(started), modelTurnTTFTMS(started, firstOutputAt), result.Usage, event.Text)
				if modelOutputStarted {
					return finalize(false), err, false
				}
				return modelTurnResult{}, err, isTransientProviderError(err)
			case "done":
				result.StopReason = event.StopReason
				return finalize(shouldRecordAPIRequest(result.StopReason)), nil, false
			}
		}
	}
}

func normalizeImageGenerationStatus(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "added":
		return "added", true
	case "in_progress":
		return "in_progress", true
	case "generating":
		return "generating", true
	case "partial", "partial_image":
		return "partial", true
	case "completed":
		return "completed", true
	default:
		return "", false
	}
}

func estimatedToolCallOutputRunes(call providers.ToolCall) int64 {
	return int64(utf8.RuneCountInString(call.Name) + utf8.RuneCount(call.Input))
}

func modelTurnUsage(usage providers.Usage, outputRunes int64, started, firstOutputAt time.Time, duration time.Duration) *db.MessageTurnUsage {
	durationMS := duration.Milliseconds()
	if duration > 0 && durationMS == 0 {
		durationMS = 1
	}
	if durationMS < 0 {
		durationMS = 0
	}
	ttftMS := modelTurnTTFTMS(started, firstOutputAt)
	if ttftMS > durationMS {
		ttftMS = durationMS
	}
	outputTokens := usage.OutputTokens
	estimated := false
	if outputTokens <= 0 && outputRunes > 0 {
		outputTokens = (outputRunes + 3) / 4
		estimated = true
	}
	generationDuration := time.Duration(0)
	if !started.IsZero() && !firstOutputAt.IsZero() && !firstOutputAt.Before(started) {
		elapsedToFirstOutput := firstOutputAt.Sub(started)
		if duration > elapsedToFirstOutput {
			generationDuration = duration - elapsedToFirstOutput
		}
	}
	tokensPerSecond := 0.0
	if outputTokens > 0 && generationDuration > 0 {
		tokensPerSecond = float64(outputTokens) / generationDuration.Seconds()
		if tokensPerSecond > 1_000_000 {
			tokensPerSecond = 1_000_000
		}
	}
	return &db.MessageTurnUsage{
		InputTokens:       maxInt64(usage.InputTokens, 0),
		OutputTokens:      maxInt64(outputTokens, 0),
		CachedInputTokens: maxInt64(usage.CachedInputTokens, 0),
		ReasoningTokens:   maxInt64(usage.ReasoningTokens, 0),
		TTFTMS:            ttftMS,
		DurationMS:        durationMS,
		TokensPerSecond:   tokensPerSecond,
		Estimated:         estimated,
	}
}

func modelTurnTTFTMS(started, firstOutputAt time.Time) int64 {
	if started.IsZero() || firstOutputAt.IsZero() || firstOutputAt.Before(started) {
		return 0
	}
	ttftMS := firstOutputAt.Sub(started).Milliseconds()
	if firstOutputAt.After(started) && ttftMS == 0 {
		return 1
	}
	return ttftMS
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func firstEventTimeoutTimer(timeoutMS int) (<-chan time.Time, func()) {
	if timeoutMS <= 0 {
		return nil, func() {}
	}
	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	return timer.C, stop
}

const (
	modelRetryBaseDelay = 250 * time.Millisecond
	// A 2s ceiling is far too low for provider rate limits, which commonly need
	// tens of seconds before a retry can succeed. Retrying faster than that just
	// burns the remaining attempts before the limit clears.
	modelRetryMaxDelay = 30 * time.Second
)

// modelRetryBackoff returns the delay before a retry attempt. It applies full
// jitter: concurrent runners that hit the same rate limit must not retry in
// lockstep, which would reproduce the burst that triggered the limit.
func modelRetryBackoff(attempt int) time.Duration {
	return jitteredBackoff(attempt, rand.Int63n)
}

func jitteredBackoff(attempt int, randomInt63n func(int64) int64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := modelRetryBaseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= modelRetryMaxDelay {
			delay = modelRetryMaxDelay
			break
		}
	}
	if delay <= 0 {
		return modelRetryBaseDelay
	}
	// Full jitter over [base, delay] keeps a minimum spacing while spreading
	// retries across the window.
	spread := int64(delay - modelRetryBaseDelay)
	if spread <= 0 {
		return delay
	}
	return modelRetryBaseDelay + time.Duration(randomInt63n(spread+1))
}

func isTransientProviderError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	for _, marker := range []string{"401", "403", "unauthorized", "forbidden", "invalid_request", "invalid request", "invalid schema", "context canceled"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	for _, marker := range []string{"408", "409", "425", "429", "500", "502", "503", "504", "rate limit", "too many requests", "temporar", "timeout", "timed out", "deadline exceeded", "eof", "connection reset", "server error", "service unavailable", "bad gateway", "gateway timeout"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func shouldRecordAPIRequest(stopReason string) bool {
	return stopReason != "not_configured"
}

func (r *Runner) recordCompletedModelTurn(agentID, runID, messageID, providerName, model string, result modelTurnResult) {
	if !result.RecordAPIRequest {
		return
	}
	ttftMS := int64(0)
	if result.TurnUsage != nil {
		ttftMS = result.TurnUsage.TTFTMS
	}
	r.recordAttributedAPIRequest(agentID, runID, messageID, providerName, model, result.Dispatch, result.Duration, ttftMS, result.Usage, "")
}

func (r *Runner) recordAttributedAPIRequest(agentID, runID, messageID, providerName, model string, dispatch providers.DispatchInfo, duration time.Duration, ttftMS int64, usage providers.Usage, errorMessage string) {
	actualProvider, actualModel, credentialID := dispatchAttribution(providerName, model, dispatch)
	r.recordAPIRequest(agentID, runID, messageID, actualProvider, actualModel, credentialID, duration, ttftMS, usage, errorMessage)
}

func dispatchAttribution(providerName, model string, dispatch providers.DispatchInfo) (string, string, string) {
	if actual := strings.TrimSpace(dispatch.Provider); actual != "" {
		providerName = actual
	}
	if actual := strings.TrimSpace(dispatch.Model); actual != "" {
		model = actual
	}
	return providerName, model, strings.TrimSpace(dispatch.CredentialID)
}

func (r *Runner) recordAPIRequest(agentID, runID, messageID, providerName, model, credentialID string, duration time.Duration, ttftMS int64, usage providers.Usage, errorMessage string) {
	if r.store == nil {
		return
	}
	durationMS := duration.Milliseconds()
	if duration > 0 && durationMS == 0 {
		durationMS = 1
	}
	if durationMS < 0 {
		durationMS = 0
	}
	if ttftMS < 0 {
		ttftMS = 0
	}
	if ttftMS > durationMS {
		ttftMS = durationMS
	}
	request := db.APIRequest{
		AgentID:           agentID,
		RunID:             runID,
		MessageID:         messageID,
		Kind:              "model",
		Provider:          providerName,
		CredentialID:      strings.TrimSpace(credentialID),
		Model:             model,
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		TTFTMS:            ttftMS,
		DurationMS:        durationMS,
		CostUSD:           estimateUsageCostUSD(providerName, model, usage),
		ErrorMessage:      errorMessage,
	}
	_, err := r.store.AddAPIRequest(context.Background(), request)
	if err != nil {
		slog.Warn("record api request failed", "agentId", agentID, "error", err)
	}
}

func agentReasoningEffort(ctx context.Context, store *db.Store, agentID string) string {
	if store == nil {
		return ""
	}
	agent, err := store.GetAgent(ctx, agentID)
	if err != nil {
		return ""
	}
	return agent.ReasoningEffort
}

type ProviderError struct{ Message string }

func (e *ProviderError) Error() string { return e.Message }
