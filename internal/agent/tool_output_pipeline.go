package agent

import (
	"strings"

	"autoto/internal/providers"
	"autoto/internal/tools"
)

func toolOutputPipelineScope(agentID, runID string) tools.ToolOutputPipelineScope {
	return tools.ToolOutputPipelineScope{AgentID: strings.TrimSpace(agentID), RunID: strings.TrimSpace(runID)}
}

func (r *Runner) processToolResultForModel(agentID, runID string, call tools.Call, raw tools.Result) tools.Result {
	if r == nil || r.toolOutputPipeline == nil {
		return raw
	}
	return r.toolOutputPipeline.ProcessResult(toolOutputPipelineScope(agentID, runID), call, raw)
}

func (r *Runner) toolOutputPipelineActive(agentID, runID string) bool {
	return r != nil && r.toolOutputPipeline != nil && r.toolOutputPipeline.IsActive(toolOutputPipelineScope(agentID, runID))
}

func (r *Runner) toolOutputPipelineControl(agentID, runID string) *providers.Message {
	if !r.toolOutputPipelineActive(agentID, runID) {
		return nil
	}
	text := "SERVER TOOL OUTPUT PIPELINE CONTROL (trusted): A tool output pipeline is active for this Run. Before giving a final answer, call EndPipeline to retrieve the filtered captures, or call EndPipeline with discard=true if none are needed. Do not answer from previews alone."
	message := providers.Message{Role: "system", Content: text, Blocks: []providers.ContentBlock{{Type: "text", Text: text, Kind: "server_tool_output_pipeline_control"}}}
	return &message
}

func (r *Runner) closeToolOutputPipelineRun(agentID, runID string) {
	if r == nil {
		return
	}
	// Danger reflection verdicts are Run-scoped too, so they retire on exactly
	// the same lifecycle edges as the pipeline captures.
	r.closeReflectionCacheRun(agentID, runID)
	if r.toolOutputPipeline == nil {
		return
	}
	r.toolOutputPipeline.CloseRun(toolOutputPipelineScope(agentID, runID))
}

func (r *Runner) closeToolOutputPipelineAgent(agentID string) {
	if r == nil {
		return
	}
	r.clearReflectionCache(strings.TrimSpace(agentID))
	if r.toolOutputPipeline == nil {
		return
	}
	r.toolOutputPipeline.CloseAgent(strings.TrimSpace(agentID))
}
