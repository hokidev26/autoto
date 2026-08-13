package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

const largeContextLimit = 600000

type ContextWindowClass string

const (
	ContextWindowStandard ContextWindowClass = "standard"
	ContextWindowLarge    ContextWindowClass = "large"
)

type ContextThresholds struct {
	PruneStartPercent   int `json:"pruneStartPercent"`
	CompactStartPercent int `json:"compactStartPercent"`
	MinPrunePercent     int `json:"minPrunePercent"`
	MaxPrunePercent     int `json:"maxPrunePercent"`
	KeepTurns           int `json:"keepTurns"`
}

type ContextTokenStatus struct {
	EstimatedTokens int                `json:"estimatedTokens"`
	LimitTokens     int                `json:"limitTokens"`
	UsagePercent    int                `json:"usagePercent"`
	WindowClass     ContextWindowClass `json:"windowClass"`
	Thresholds      ContextThresholds  `json:"thresholds"`
	CanCompact      bool               `json:"canCompact"`
	CanClear        bool               `json:"canClear"`
	LatestMessageID string             `json:"latestMessageId,omitempty"`
	MessageCount    int                `json:"messageCount"`
	PruneEnabled    bool               `json:"pruneEnabled"`
	HasSummary      bool               `json:"hasSummary"`
	Estimated       bool               `json:"estimated"`
	// EstimateBasis is "calibrated" once a real provider-reported input-token
	// count has been folded into the estimate, otherwise "heuristic".
	EstimateBasis         string `json:"estimateBasis"`
	LastActualInputTokens int64  `json:"lastActualInputTokens"`
}

const (
	contextEstimateBasisHeuristic  = "heuristic"
	contextEstimateBasisCalibrated = "calibrated"
)

func (r *Runner) SetContextManagementConfig(value config.ContextManagementConfig) {
	if r == nil {
		return
	}
	r.contextManagementMu.Lock()
	r.contextManagement = value.Normalized()
	r.contextManagementMu.Unlock()
}

func (r *Runner) ContextManagementConfig() config.ContextManagementConfig {
	if r == nil {
		return (config.ContextManagementConfig{}).Normalized()
	}
	r.contextManagementMu.RLock()
	value := r.contextManagement
	r.contextManagementMu.RUnlock()
	return value.Normalized()
}

func (r *Runner) ContextStatus(ctx context.Context, agentID string) (ContextTokenStatus, db.Agent, error) {
	if r == nil || r.store == nil {
		return ContextTokenStatus{}, db.Agent{}, errors.New("agent context store is unavailable")
	}
	agent, err := r.store.GetAgent(ctx, strings.TrimSpace(agentID))
	if err != nil {
		return ContextTokenStatus{}, db.Agent{}, err
	}
	messages, err := r.store.ListMessages(ctx, agent.ID)
	if err != nil {
		return ContextTokenStatus{}, db.Agent{}, err
	}
	agent = contextAgentForMessages(agent, messages)
	var toolSpecs []providers.ToolSpec
	if snapshot, snapshotErr := r.snapshotTools(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD}); snapshotErr == nil {
		toolSpecs = snapshot.specs
	}
	return r.contextStatusForAgent(agent, messages, toolSpecs), agent, nil
}

func (r *Runner) contextStatusForAgent(agent db.Agent, messages []db.Message, toolSpecs []providers.ToolSpec) ContextTokenStatus {
	agent = contextAgentForMessages(agent, messages)
	limit := r.contextTokenLimit(agent.Model)
	request := providerMessagesForContextWithKeep(agent, messages, r.ContextManagementConfig().CompactKeepTurns)
	estimated := estimateRequestTokens(agent.SystemPrompt, request, toolSpecs)
	// The status path loads messages without binary data, so image costs are
	// supplemented from stored metadata to match what a turn would send.
	estimated += estimateContextImageMetadataTokens(messages, agent.PruneBoundaryMessageID)
	basis := contextEstimateBasisHeuristic
	var lastActual int64
	if ratio, sample, ok := r.contextCalibrationRatio(agent.ID, agent.Model); ok {
		estimated = calibrateContextTokens(estimated, ratio)
		basis = contextEstimateBasisCalibrated
		lastActual = sample.ActualTokens
	}
	usage := 0
	if limit > 0 {
		usage = estimated * 100 / limit
	}
	cfg := r.ContextManagementConfig()
	window := cfg.WindowForLimit(limit)
	class := ContextWindowStandard
	if limit > largeContextLimit {
		class = ContextWindowLarge
	}
	compacted := messagesStartAfterBoundary(messages, agent.PruneBoundaryMessageID)
	canCompact := strings.TrimSpace(agent.Status) != "running" && len(selectContextTurnCandidates(messages, agent.PruneBoundaryMessageID, cfg.CompactKeepTurns)) > 0
	latest := ""
	if len(messages) > 0 {
		latest = messages[len(messages)-1].ID
	}
	return ContextTokenStatus{
		EstimatedTokens:       estimated,
		LimitTokens:           limit,
		UsagePercent:          usage,
		WindowClass:           class,
		Thresholds:            ContextThresholds{PruneStartPercent: window.PruneStart, CompactStartPercent: window.CompactStart, MinPrunePercent: cfg.MinPrunePercent, MaxPrunePercent: cfg.MaxPrunePercent, KeepTurns: cfg.CompactKeepTurns},
		CanCompact:            canCompact,
		CanClear:              len(messages) > 0 && (compacted < len(messages) || strings.TrimSpace(agent.ContextSummary) != ""),
		LatestMessageID:       latest,
		MessageCount:          len(messages),
		PruneEnabled:          agent.PruneEnabled,
		HasSummary:            strings.TrimSpace(agent.ContextSummary) != "",
		Estimated:             true,
		EstimateBasis:         basis,
		LastActualInputTokens: lastActual,
	}
}

func contextMessageStartsTurn(message db.Message) bool {
	return strings.EqualFold(strings.TrimSpace(message.Role), "user") && strings.TrimSpace(message.ParentToolID) == ""
}

func contextBoundaryStart(messages []db.Message, boundaryID string) (int, bool) {
	boundaryID = strings.TrimSpace(boundaryID)
	if boundaryID == "" {
		return 0, true
	}
	for i, message := range messages {
		if message.ID == boundaryID {
			return i + 1, true
		}
	}
	return 0, false
}

func contextAgentForMessages(agent db.Agent, messages []db.Message) db.Agent {
	if strings.TrimSpace(agent.PruneBoundaryMessageID) != "" {
		if _, ok := contextBoundaryStart(messages, agent.PruneBoundaryMessageID); !ok {
			// A stale boundary must never cause a persisted summary to be sent
			// together with the entire raw transcript.
			agent.ContextSummary = ""
			agent.PruneBoundaryMessageID = ""
			agent.PrunedPercent = 0
		}
	}
	return agent
}

func contextRecentTurnsStart(messages []db.Message, start, keepTurns int) int {
	if keepTurns <= 0 {
		keepTurns = 2
	}
	turnStarts := make([]int, 0)
	for i := start; i < len(messages); i++ {
		if contextMessageStartsTurn(messages[i]) {
			turnStarts = append(turnStarts, i)
		}
	}
	if len(turnStarts) <= keepTurns {
		return start
	}
	return turnStarts[len(turnStarts)-keepTurns]
}

func selectContextTurnCandidates(messages []db.Message, boundaryID string, keepTurns int) []db.Message {
	start := messagesStartAfterBoundary(messages, boundaryID)
	if start >= len(messages) {
		return nil
	}
	if keepTurns <= 0 {
		keepTurns = 2
	}
	turnStarts := make([]int, 0)
	for i := start; i < len(messages); i++ {
		if contextMessageStartsTurn(messages[i]) {
			turnStarts = append(turnStarts, i)
		}
	}
	if len(turnStarts) <= keepTurns {
		return nil
	}
	end := turnStarts[len(turnStarts)-keepTurns]
	for end < len(messages) && !contextMessageStartsTurn(messages[end]) {
		end++
	}
	if end <= start {
		return nil
	}
	return append([]db.Message(nil), messages[start:end]...)
}

func selectManualContextCandidates(messages []db.Message, boundaryID string, cfg config.ContextManagementConfig) []db.Message {
	return selectContextTurnCandidates(messages, boundaryID, cfg.CompactKeepTurns)
}

// selectContextCandidatesThroughMessage picks every message from the current
// boundary through the target, then extends the cut to the end of the turn the
// target sits in: a boundary inside a turn would resume the raw transcript on
// an unmatched tool result.
func selectContextCandidatesThroughMessage(messages []db.Message, boundaryID, throughID string) ([]db.Message, error) {
	start := messagesStartAfterBoundary(messages, boundaryID)
	target := -1
	for i, message := range messages {
		if message.ID == throughID {
			if message.SupersededAt != "" {
				return nil, fmt.Errorf("%w: compaction requires a message that is still current", db.ErrConflict)
			}
			target = i
			break
		}
	}
	if target < 0 {
		return nil, fmt.Errorf("%w: compaction target message was not found", db.ErrConflict)
	}
	if target < start {
		// Already behind the boundary: everything up to it is compacted.
		return nil, nil
	}
	end := target + 1
	for end < len(messages) && !contextMessageStartsTurn(messages[end]) {
		end++
	}
	if end <= start {
		return nil, nil
	}
	return append([]db.Message(nil), messages[start:end]...), nil
}

func contextCandidateBoundary(candidates []db.Message) string {
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1].ID
}

type contextPayloadPruneCandidate struct {
	messageIndex int
	blockIndex   int
	replacement  providers.ContentBlock
	savings      int
}

func progressivelyPruneContextToolPayloads(messages []providers.Message, eligible []bool, cfg config.ContextManagementConfig, desiredReduction, quantum int) []providers.Message {
	cfg = cfg.Normalized()
	candidates := make([]contextPayloadPruneCandidate, 0)
	totalSavings := 0
	for messageIndex, message := range messages {
		if messageIndex >= len(eligible) || !eligible[messageIndex] {
			continue
		}
		for blockIndex, block := range message.Blocks {
			replacement := block
			switch block.Type {
			case "tool_use":
				if len(block.Input) == 0 {
					continue
				}
				replacement.Input = json.RawMessage(`{"_autotoCompacted":true}`)
			case "tool_result":
				if strings.TrimSpace(block.Output) == "" {
					continue
				}
				replacement.Output = compactToolResultOutput(block.ToolName)
			default:
				continue
			}
			savings := estimateBlockTokens(block) - estimateBlockTokens(replacement)
			if savings <= 0 {
				continue
			}
			candidates = append(candidates, contextPayloadPruneCandidate{messageIndex: messageIndex, blockIndex: blockIndex, replacement: replacement, savings: savings})
			totalSavings += savings
		}
	}
	if totalSavings <= 0 {
		return messages
	}
	minTarget := (totalSavings*cfg.MinPrunePercent + 99) / 100
	maxTarget := totalSavings * cfg.MaxPrunePercent / 100
	if maxTarget < minTarget {
		maxTarget = minTarget
	}
	target := desiredReduction
	if target < minTarget {
		target = minTarget
	}
	// Quantize upward so the pruned set stays identical across consecutive
	// turns. desiredReduction grows a little every turn; without quantization
	// each turn prunes slightly more, the rendered history diverges mid-prefix,
	// and provider prompt caches are invalidated on every request. Rounding up
	// prunes somewhat more than strictly needed but keeps the request prefix
	// byte-stable until the target crosses the next quantum step. MaxPrunePercent
	// still caps the result.
	if quantum > 0 && target > 0 {
		target = (target + quantum - 1) / quantum * quantum
	}
	if target > maxTarget {
		target = maxTarget
	}
	if target <= 0 {
		return messages
	}
	out := append([]providers.Message(nil), messages...)
	cloned := make(map[int]bool)
	saved := 0
	for _, candidate := range candidates {
		if !cloned[candidate.messageIndex] {
			out[candidate.messageIndex].Blocks = append([]providers.ContentBlock(nil), out[candidate.messageIndex].Blocks...)
			cloned[candidate.messageIndex] = true
		}
		out[candidate.messageIndex].Blocks[candidate.blockIndex] = candidate.replacement
		if content := strings.TrimSpace(contextMessageContent(out[candidate.messageIndex].Blocks)); content != "" {
			out[candidate.messageIndex].Content = content
		}
		saved += candidate.savings
		if saved >= target {
			break
		}
	}
	return out
}

func validateContextExpectedLatest(messages []db.Message, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return errors.New("expectedLatestMessageId is required")
	}
	if len(messages) == 0 || messages[len(messages)-1].ID != expected {
		return fmt.Errorf("%w: latest message changed", db.ErrConflict)
	}
	return nil
}

func (r *Runner) ContextStatusForEvent(agent db.Agent, messages []db.Message) map[string]any {
	return r.contextUpdatedData(agent, messages, nil)
}

func (r *Runner) contextUpdatedData(agent db.Agent, messages []db.Message, toolSpecs []providers.ToolSpec) map[string]any {
	status := r.contextStatusForAgent(agent, messages, toolSpecs)
	return map[string]any{
		"entityGeneration":      agent.EntityGeneration,
		"messageCount":          status.MessageCount,
		"prunedPercent":         agent.PrunedPercent,
		"pruneEnabled":          agent.PruneEnabled,
		"estimatedTokens":       status.EstimatedTokens,
		"limitTokens":           status.LimitTokens,
		"usagePercent":          status.UsagePercent,
		"windowClass":           status.WindowClass,
		"thresholds":            status.Thresholds,
		"canCompact":            status.CanCompact,
		"canClear":              status.CanClear,
		"latestMessageId":       status.LatestMessageID,
		"hasSummary":            status.HasSummary,
		"estimated":             true,
		"estimateBasis":         status.EstimateBasis,
		"lastActualInputTokens": status.LastActualInputTokens,
	}
}

func (r *Runner) ClearAgentContext(ctx context.Context, agentID string, expectedEntityGeneration int64, expectedLatestMessageID string) (db.Agent, error) {
	if r == nil || r.store == nil {
		return db.Agent{}, errors.New("agent context store is unavailable")
	}
	agentID = strings.TrimSpace(agentID)
	if err := r.beginContextCompaction(ctx, agentID); err != nil {
		return db.Agent{}, err
	}
	defer r.finishContextCompaction(agentID)
	agent, err := r.store.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if agent.EntityGeneration != expectedEntityGeneration {
		return db.Agent{}, fmt.Errorf("%w: agent settings changed", db.ErrConflict)
	}
	messages, err := r.store.ListMessages(ctx, agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if err := validateContextExpectedLatest(messages, expectedLatestMessageID); err != nil {
		return db.Agent{}, err
	}
	updated, err := r.store.ClearAgentContext(ctx, agentID, expectedEntityGeneration, expectedLatestMessageID)
	if err != nil {
		return db.Agent{}, err
	}
	messages, _ = r.store.ListMessages(ctx, agentID)
	data := r.contextUpdatedData(updated, messages, nil)
	data["cleared"] = true
	r.publish(Event{Type: "context.updated", AgentID: agentID, Data: data})
	return updated, nil
}
