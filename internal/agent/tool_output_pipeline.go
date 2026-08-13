package agent

import (
	"strings"

	"autoto/internal/providers"
	"autoto/internal/tools"
)

func toolOutputPipelineScope(agentID, runID string) tools.ToolOutputPipelineScope {
	return tools.ToolOutputPipelineScope{AgentID: strings.TrimSpace(agentID), RunID: strings.TrimSpace(runID)}
}

// processToolResultForModel is the single point every tool result passes on its
// way to the model, whatever produced it: a real execution, a policy denial, or
// a Go error the loop wrapped. Repeat detection counts here for exactly that
// reason -- a model hammering a call that keeps being refused is the loop most
// worth breaking, and a denial never reaches an executor.
//
// The pipeline runs before the spill policy and the two never both act. An
// active pipeline has already replaced the output with a short preview, so it
// is far below the spill threshold, and spilling a preview would send the model
// to Read a file instead of to EndPipeline. With no pipeline active -- the
// default -- ProcessResult returns the result untouched and spill is the only
// thing shaping it.
func (r *Runner) processToolResultForModel(agentID, runID string, call tools.Call, raw tools.Result) tools.Result {
	if r == nil {
		return raw
	}
	r.observeRepeatedToolCall(agentID, runID, call)
	result := raw
	if r.toolOutputPipeline != nil {
		result = r.toolOutputPipeline.ProcessResult(toolOutputPipelineScope(agentID, runID), call, result)
	}
	return r.spillToolResultForModel(agentID, call, result)
}

func (r *Runner) toolOutputPipelineActive(agentID, runID string) bool {
	return r != nil && r.toolOutputPipeline != nil && r.toolOutputPipeline.IsActive(toolOutputPipelineScope(agentID, runID))
}

func (r *Runner) toolOutputPipelineControl(agentID, runID string) *providers.Message {
	if !r.toolOutputPipelineActive(agentID, runID) {
		return nil
	}
	text := "SERVER TOOL OUTPUT PIPELINE CONTROL (trusted): A tool output pipeline is active for this Run. Before giving a final answer, call EndPipeline to retrieve the filtered captures, or call EndPipeline with discard=true if none are needed. Do not answer from previews alone."
	message := turnControlMessage("server_tool_output_pipeline_control", text)
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
