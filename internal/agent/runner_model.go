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

// streamingUsagePublishInterval throttles the per-text-delta model.streaming
// events. At 50-100 deltas per second an unconditional publish doubled the hub
// traffic -- every event is JSON-marshalled under the hub-wide lock, serialized
// again per WebSocket connection, and pushed into the replay ring where it
// crowds out events worth replaying. 250ms keeps the throughput readout feeling
// live (4 updates/s is well above what a human can read off a counter) while
// cutting the model.streaming volume by ~95% at those rates; 500ms would only
// halve the remaining trickle but double how stale the readout can get.
const streamingUsagePublishInterval = 250 * time.Millisecond

// Reasoning holds the model's own running commentary for the turn, when the
// provider exposes a readable one. It is never fed back into the next request:
// it exists so the activity list can say why a step happened.
type modelTurnResult struct {
	Text                   string
	Reasoning              string
	ResponseBlocks         []providers.ContentBlock
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
	// Read the live ceiling rather than the frozen config, so saving the setting
	// applies to the next turn instead of the next restart. A negative value is
	// the unlimited sentinel, which used to be clamped to zero here -- turning
	// "keep trying" into "never retry", the exact opposite.
	maxRetries := r.MaxTransientRetries()
	unlimited := maxRetries < 0
	var lastErr error
	for attempt := 0; unlimited || attempt <= maxRetries; attempt++ {
		result, err, retryable := r.runModelTurnAttempt(ctx, agentID, runID, provider, model, systemPrompt, messages, toolSpecs, reasoningEffort, fastMode)
		if err == nil {
			return result, nil
		}
		lastErr = err
		// Unlimited never exhausts its attempts, so only cancellation or a
		// permanent error ends it. Stating that here rather than relying on
		// attempt never reaching -1 keeps the exit visible next to the entry.
		exhausted := !unlimited && attempt == maxRetries
		if ctx.Err() != nil || !retryable || exhausted {
			return result, err
		}
		backoff := modelRetryBackoff(attempt)
		slog.Warn("retrying transient provider error", "agentId", agentID, "provider", provider.Name(), "model", model, "attempt", attempt+1, "maxRetries", maxRetries, "backoff", backoff.String(), "error", err)
		// Said out loud, not only logged. This loop owns the retries that happen
		// *inside* one segment -- a first-token timeout is the common one -- and it
		// used to wait here silently, so the composer showed an idle conversation
		// for the whole backoff and the run read as hung. The continuation-level
		// retry already publishes this event and the client already renders it, so
		// the same shape reaches the same handler.
		//
		// maxAttempts is 0 when the ceiling is the unlimited sentinel: there is no
		// total to count towards, and the client treats a missing total as "say
		// retrying without a fraction" rather than inventing one.
		retryCeiling := 0
		if !unlimited {
			retryCeiling = maxRetries + 1
		}
		r.publish(Event{Type: "agent.provider_error_retry", AgentID: agentID, Data: mergeEventData(map[string]any{
			"attempt":     attempt + 1,
			"maxAttempts": retryCeiling,
			"provider":    provider.Name(),
			"model":       model,
			"backoffMs":   backoff.Milliseconds(),
			"error":       boundedProviderErrorText(err),
			"scope":       "model_turn",
		}, runID)})
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
	modelCapabilities := providers.ModelCapabilitiesFor(provider, model)
	// Judged against the model rather than the provider: the strongest Codex
	// levels are served by some models only.
	if !providers.CapabilitiesForModel(capabilities, modelCapabilities).SupportsReasoningEffort(reasoningEffort) {
		return modelTurnResult{}, fmt.Errorf("%w: provider %q does not support requested effort %q for model %q", providers.ErrReasoningEffortUnsupported, provider.Name(), reasoningEffort, model), false
	}
	fastModeAllowed := false
	if _, ok := provider.(providers.ModelCapabilityProvider); ok && fastMode {
		fastModeAllowed = !modelCapabilities.FastModeKnown || modelCapabilities.FastMode
	}
	requestCapabilities := capabilities
	requestCapabilities.ImageGeneration = capabilities.ImageGeneration && modelCapabilities.ImageGeneration
	// Repair tool-call pairing before anything rewrites the blocks. A run killed
	// mid-tool leaves a tool_use with no tool_result, which providers reject
	// outright, and prepareProviderMessagesForCapabilities may turn these blocks
	// into plain text further down. Doing it here means every provider is covered
	// by one implementation instead of each adapter remembering to handle it.
	requestMessages := enforceProviderMediaBudget(prepareProviderMessagesForCapabilities(repairToolCallPairing(messages), requestCapabilities))
	requestTools := toolSpecs
	if !capabilities.Tools {
		requestTools = nil
	}
	// Ask about the window using the provider this turn is actually calling. The
	// bare model name alone would resolve to the default provider and measure the
	// turn against that adapter's protocol default instead of this model's
	// configured limit.
	qualifiedModel := modelWithProvider(provider.Name(), model)
	if limit, origin := r.contextTokenLimitWithOrigin(qualifiedModel); limit > 0 {
		if estimated := estimateRequestTokens(systemPrompt, requestMessages, requestTools); estimated > limit {
			return modelTurnResult{}, annotateContextBudgetError(errorsContextBudget(limit, estimated), qualifiedModel, origin), false
		}
	}
	request := providers.GenerateRequest{Model: model, SystemPrompt: systemPrompt, Messages: requestMessages, Tools: requestTools, ReasoningEffort: reasoningEffort, FastMode: fastModeAllowed, EnableImageGeneration: capabilities.ImageGeneration && modelCapabilities.ImageGeneration, Scenario: providers.CallScenarioInternal}
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
		return modelTurnResult{}, err, r.isTransientProviderError(err)
	}

	var result modelTurnResult
	var builder strings.Builder
	// Kept apart from builder so reasoning never leaks into the assistant
	// message that is sent back to the model on the next turn.
	var reasoning strings.Builder
	// One preview per tool call id. A nil entry records that the tool has no
	// streamable argument, so the lookup is not repeated per fragment.
	var inputPreviews map[string]*toolInputStreamPreview
	publishToolInputDelta := func(id, name string, preview *toolInputStreamPreview, chunk string) {
		data := map[string]any{
			"requestId": requestID,
			"toolUseId": id,
			"toolName":  name,
			"field":     preview.Field(),
		}
		// Snapshot previews (Bash) resend the whole redacted text each time
		// instead of appending, because redaction is only reliable on the
		// complete accumulated command.
		if preview.SnapshotMode() {
			data["replace"] = true
		}
		// The file path is worth naming the card early, while the content is
		// still streaming; it is reported exactly once.
		if path, ok := preview.FilePath(); ok {
			data["inputJson"] = map[string]any{"file_path": path}
		}
		if chunk == "" && data["inputJson"] == nil {
			return
		}
		r.publish(Event{Type: "tool.input_delta", AgentID: agentID, Text: chunk, Data: mergeEventData(data, runID)})
	}
	var firstOutputAt time.Time
	var outputRunes int64
	modelOutputStarted := false
	generatedImageIndexes := make(map[string]int)
	firstEventTimer, stopFirstEventTimer := firstEventTimeoutTimer(r.cfg.FirstTokenTimeoutMs)
	defer stopFirstEventTimer()
	idleTimer := newStreamIdleTimer(r.cfg.StreamIdleTimeoutMs)
	defer idleTimer.stop()
	markModelOutput := func(outputAt time.Time) {
		if firstOutputAt.IsZero() {
			firstOutputAt = outputAt
			stopFirstEventTimer()
		}
	}
	// Throttle state is local to this attempt on purpose: each stream starts
	// with a fresh window, so the first delta always publishes immediately and
	// nothing leaks across runs.
	var streamingUsagePublishedAt time.Time
	streamingUsagePending := false
	publishStreamingUsage := func() {
		streamingUsagePublishedAt = time.Now()
		streamingUsagePending = false
		pending := modelTurnUsage(providers.Usage{}, outputRunes, started, firstOutputAt, time.Since(started))
		r.publish(Event{Type: "model.streaming", AgentID: agentID, Data: mergeEventData(map[string]any{
			"requestId":         requestID,
			"provider":          provider.Name(),
			"model":             model,
			"firstOutputAt":     firstOutputAt.UTC().Format(time.RFC3339Nano),
			"pendingThroughput": pending,
		}, runID)})
	}
	// Only the per-text-delta path goes through this throttle; the tool_call
	// path keeps publishing directly because tool calls arrive at human scale,
	// not token scale. A suppressed update is remembered so finalize can flush
	// the accurate final numbers instead of leaving the UI on a stale snapshot.
	throttledPublishStreamingUsage := func() {
		if !streamingUsagePublishedAt.IsZero() && time.Since(streamingUsagePublishedAt) < streamingUsagePublishInterval {
			streamingUsagePending = true
			return
		}
		publishStreamingUsage()
	}
	finalize := func(record bool) modelTurnResult {
		if streamingUsagePending {
			publishStreamingUsage()
		}
		completedAt := time.Now()
		duration := completedAt.Sub(started)
		result.Text = builder.String()
		result.Reasoning = reasoning.String()
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
		case <-idleTimer.expired():
			// A stream that opened and then went silent is indistinguishable from
			// one that is still working, so without this the turn waited on the
			// event channel for as long as the provider kept the connection open.
			// That wait is what a wedged agent looked like from the outside.
			err := &ProviderError{Message: fmt.Sprintf("provider stream idle timeout after %dms", r.cfg.StreamIdleTimeoutMs)}
			cancel()
			r.recordAttributedAPIRequest(agentID, runID, "", provider.Name(), model, result.Dispatch, time.Since(started), modelTurnTTFTMS(started, firstOutputAt), result.Usage, err.Error())
			// Matches the mid-stream provider error above: partial output is kept
			// and the turn is not retried, because a retry would duplicate what
			// the user has already seen.
			if modelOutputStarted {
				return finalize(false), err, false
			}
			return modelTurnResult{}, err, true
		case event, ok := <-events:
			if !ok {
				return finalize(true), nil, false
			}
			idleTimer.observe()
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
				throttledPublishStreamingUsage()
			case "reasoning":
				// Advisory only. It is not appended to the answer, does not count
				// toward outputRunes, and does not set modelOutputStarted -- a turn
				// that only reasoned has produced nothing and must stay retryable.
				// It does mark first output, because the user can see it arrive.
				if event.Text == "" {
					continue
				}
				markModelOutput(time.Now())
				reasoning.WriteString(event.Text)
				r.publish(Event{Type: "agent.reasoning", AgentID: agentID, Text: event.Text, Data: mergeEventData(map[string]any{"requestId": requestID}, runID)})
			case "content_block":
				if event.ContentBlock == nil {
					continue
				}
				markModelOutput(time.Now())
				modelOutputStarted = true
				block := *event.ContentBlock
				block.Data = append([]byte(nil), block.Data...)
				block.Input = append([]byte(nil), block.Input...)
				block.ProviderState = append([]byte(nil), block.ProviderState...)
				result.ResponseBlocks = append(result.ResponseBlocks, block)
			case "tool_call_delta":
				// A live preview of the arguments the model is composing. Like
				// reasoning it marks first output but not modelOutputStarted: a turn
				// that died mid-arguments produced nothing and must stay retryable.
				if !capabilities.Tools || event.ToolCall == nil || event.Text == "" {
					continue
				}
				toolUseID := strings.TrimSpace(event.ToolCall.ID)
				if toolUseID == "" {
					continue
				}
				preview, tracked := inputPreviews[toolUseID]
				if !tracked {
					preview = newToolInputStreamPreview(event.ToolCall.Name)
					if inputPreviews == nil {
						inputPreviews = make(map[string]*toolInputStreamPreview)
					}
					inputPreviews[toolUseID] = preview
				}
				if preview == nil {
					continue
				}
				markModelOutput(time.Now())
				chunk := preview.Feed(event.Text)
				if preview.SnapshotMode() {
					chunk, _ = preview.SnapshotText(time.Now(), false)
				}
				publishToolInputDelta(toolUseID, event.ToolCall.Name, preview, chunk)
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
					// Whatever the delta stream did not cover is flushed now, so
					// providers without argument streaming still produce a preview
					// and streamed previews always end complete.
					preview, tracked := inputPreviews[toolCall.ID]
					if !tracked {
						preview = newToolInputStreamPreview(toolCall.Name)
						if inputPreviews == nil {
							inputPreviews = make(map[string]*toolInputStreamPreview)
						}
						inputPreviews[toolCall.ID] = preview
					}
					if preview != nil {
						publishToolInputDelta(toolCall.ID, toolCall.Name, preview, preview.Finalize(toolCall.Input))
					}
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
				return modelTurnResult{}, err, r.isTransientProviderError(err)
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

// streamIdleTimer bounds the gap between provider stream events. It is separate
// from firstEventTimeoutTimer because the two answer different questions: that
// one asks whether the provider ever started, this one asks whether it is still
// going. A zero timeout disables the guard, which is what every caller that does
// not configure one gets.
type streamIdleTimer struct {
	timeout time.Duration
	timer   *time.Timer
}

func newStreamIdleTimer(timeoutMS int) *streamIdleTimer {
	if timeoutMS <= 0 {
		return &streamIdleTimer{}
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	return &streamIdleTimer{timeout: timeout, timer: time.NewTimer(timeout)}
}

func (t *streamIdleTimer) expired() <-chan time.Time {
	if t == nil || t.timer == nil {
		return nil
	}
	return t.timer.C
}

// observe restarts the window because the stream just proved it is alive. Any
// event counts, including usage metadata: the question here is whether the
// connection is still producing, not whether it produced anything useful.
func (t *streamIdleTimer) observe() {
	if t == nil || t.timer == nil {
		return
	}
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
	t.timer.Reset(t.timeout)
}

func (t *streamIdleTimer) stop() {
	if t == nil || t.timer == nil {
		return
	}
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
	}
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

type ProviderError struct{ Message string }

func (e *ProviderError) Error() string { return e.Message }
