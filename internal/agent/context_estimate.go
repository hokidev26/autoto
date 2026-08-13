package agent

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"autoto/internal/db"
	"autoto/internal/media"
	"autoto/internal/providers"
)

// Context estimate calibration. The char-based heuristic in estimateTextTokens
// systematically drifts from real tokenizers (CJK-heavy history can be off by
// 30-50%), so every finished model turn feeds back the provider-reported input
// token count for the request we just estimated. Later estimates for the same
// conversation and model are scaled by the measured ratio, which both the
// usage panel and the prune/compact gates consume, so compaction fires on
// measured reality instead of a fixed guess. Samples live in memory only: a
// restart just re-learns from the next turn.
const (
	// Ratios outside this band are treated as measurement noise (e.g. provider
	// miscounts, empty-history turns) rather than tokenizer truth.
	contextCalibrationMinRatio = 0.5
	contextCalibrationMaxRatio = 2.0
	// Below this estimate size fixed per-request overhead dominates the ratio,
	// so tiny turns must not steer later multi-thousand-token decisions.
	contextCalibrationMinSampleTokens = 512
	contextCalibrationMaxEntries      = 1024
)

type contextCalibrationSample struct {
	Model           string
	EstimatedTokens int
	ActualTokens    int64
	At              time.Time
}

// contextCalibrator stores the latest estimate-vs-actual input-token pair per
// conversation. Its mutex is private and is never nested with Runner locks.
type contextCalibrator struct {
	mu      sync.RWMutex
	samples map[string]contextCalibrationSample
}

func (c *contextCalibrator) record(agentID, model string, estimatedTokens int, usage providers.Usage) {
	agentID = strings.TrimSpace(agentID)
	model = strings.TrimSpace(model)
	actual := usage.InputTokens
	if agentID == "" || model == "" || estimatedTokens < contextCalibrationMinSampleTokens || actual <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.samples == nil {
		c.samples = make(map[string]contextCalibrationSample)
	}
	if _, exists := c.samples[agentID]; !exists && len(c.samples) >= contextCalibrationMaxEntries {
		evictOldestContextCalibration(c.samples)
	}
	c.samples[agentID] = contextCalibrationSample{
		Model:           model,
		EstimatedTokens: estimatedTokens,
		ActualTokens:    actual,
		At:              time.Now(),
	}
}

func (c *contextCalibrator) ratio(agentID, model string) (float64, contextCalibrationSample, bool) {
	c.mu.RLock()
	sample, ok := c.samples[strings.TrimSpace(agentID)]
	c.mu.RUnlock()
	if !ok || sample.EstimatedTokens <= 0 || sample.ActualTokens <= 0 {
		return 0, contextCalibrationSample{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(model), sample.Model) {
		return 0, contextCalibrationSample{}, false
	}
	measured := float64(sample.ActualTokens) / float64(sample.EstimatedTokens)
	if measured < contextCalibrationMinRatio {
		measured = contextCalibrationMinRatio
	}
	if measured > contextCalibrationMaxRatio {
		measured = contextCalibrationMaxRatio
	}
	return measured, sample, true
}

// recordContextCalibration stores the estimate/actual pair for one completed
// turn. Adapters report InputTokens as the whole prompt with CachedInputTokens
// as the subset served from cache, so InputTokens alone is the request size;
// adding the cached share on top double-counted every warm-cache turn, drove
// the ratio into its 2.0 clamp, and halved the effective window.
func (r *Runner) recordContextCalibration(agentID, model string, estimatedTokens int, usage providers.Usage) {
	if r == nil {
		return
	}
	r.usage.calibrator.record(agentID, model, estimatedTokens, usage)
}

func evictOldestContextCalibration(samples map[string]contextCalibrationSample) {
	oldestKey := ""
	var oldestAt time.Time
	for key, sample := range samples {
		if oldestKey == "" || sample.At.Before(oldestAt) {
			oldestKey = key
			oldestAt = sample.At
		}
	}
	if oldestKey != "" {
		delete(samples, oldestKey)
	}
}

// contextCalibrationRatio returns the clamped actual/estimated ratio for the
// conversation, but only while the conversation still runs the model the
// sample was measured against: a model switch changes the tokenizer, so the
// old ratio is evidence about the wrong vocabulary.
func (r *Runner) contextCalibrationRatio(agentID, model string) (float64, contextCalibrationSample, bool) {
	if r == nil {
		return 0, contextCalibrationSample{}, false
	}
	return r.usage.calibrator.ratio(agentID, model)
}

func calibrateContextTokens(estimated int, ratio float64) int {
	if estimated <= 0 || ratio <= 0 {
		return estimated
	}
	return int(float64(estimated)*ratio + 0.5)
}

// effectiveContextTokenLimit converts a calibration ratio into an adjusted
// window so every existing raw-estimate comparison keeps working: comparing
// rawEstimate against limit/ratio is equivalent to comparing rawEstimate*ratio
// against limit. The floor guards against a pathological sample shrinking the
// window into uselessness.
func effectiveContextTokenLimit(limit int, ratio float64, calibrated bool) int {
	if !calibrated || ratio <= 0 || limit <= 0 {
		return limit
	}
	effective := int(float64(limit) / ratio)
	if effective < 4096 {
		effective = 4096
	}
	return effective
}

// estimateContextImageMetadataTokens supplements the status-path estimate with
// image costs that only the turn path can see directly. ListMessages returns
// attachments and generated images as metadata without binary data, so the
// block estimator counts neither transport bytes nor visual tokens for them;
// the turn path (ListMessagesWithAttachmentData plus hydration) counts both
// and the panel silently underestimated image-heavy conversations. Guarded on
// absent binary data so it can never double-count a hydrated block.
func estimateContextImageMetadataTokens(messages []db.Message, boundaryID string) int {
	start := messagesStartAfterBoundary(messages, boundaryID)
	total := 0
	for i := start; i < len(messages); i++ {
		if messages[i].SupersededAt != "" {
			continue
		}
		for _, attachment := range messages[i].Attachments {
			if attachment.Kind != "image" || len(attachment.ModelData) > 0 || len(attachment.Data) > 0 {
				continue
			}
			if strings.TrimSpace(attachment.ProcessingStatus) == media.ProcessingRejected {
				continue
			}
			total += estimateImageTokens(attachment.Width, attachment.Height)
			if attachment.SizeBytes > 0 {
				total += int((attachment.SizeBytes + 3071) / 3072)
			}
		}
		for _, image := range messages[i].GeneratedImages {
			if image.Status != "ready" {
				continue
			}
			total += estimateImageTokens(image.Width, image.Height)
			if image.ByteSize > 0 {
				total += int((image.ByteSize + 3071) / 3072)
			}
		}
	}
	return total
}

// estimateToolSpecsTokens serializes the specs once. Tool specs are constant
// across a turn, but estimateRequestTokens used to re-marshal the whole set on
// every call and managedContextForTurn asks for an estimate up to six times per
// turn, so long tool catalogs paid the JSON cost repeatedly.
func estimateToolSpecsTokens(toolSpecs []providers.ToolSpec) int {
	if len(toolSpecs) == 0 {
		return 0
	}
	data, err := json.Marshal(toolSpecs)
	if err != nil {
		return 0
	}
	return estimateTextTokens(string(data))
}

func estimateRequestTokensWithSpecTokens(systemPrompt string, messages []providers.Message, specTokens int) int {
	total := estimateTextTokens(systemPrompt) + specTokens
	for _, message := range messages {
		total += estimateMessageTokens(message)
	}
	return total
}
