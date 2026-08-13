package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"

	"autoto/internal/providers"
	"autoto/internal/tools"
)

// prefetchedToolResult holds the outcome of a speculatively-run tool call.
// ran distinguishes "the worker actually executed this index" from the zero
// value, since only eligible indices are dispatched; the caller must fall back
// to executing any index whose ran is false itself.
type prefetchedToolResult struct {
	ran        bool
	result     tools.Result
	executeErr error
}

// maxToolPrefetchWorkers bounds the worker pool used to overlap read-risk tool
// calls within one model turn. It is deliberately small: prefetch only exists
// to hide I/O latency for a handful of read-only lookups per turn, not to
// parallelize arbitrary work.
const maxToolPrefetchWorkers = 4

// prefetchToolCallResults runs step 1 (executeToolForLoop) for eligible tool
// calls ahead of the caller's serial per-call loop, so their latency overlaps
// instead of serializing. It performs no persistence, ledger writes, publishes,
// or resumeAfterID bookkeeping — those steps (2-7) remain the caller's
// responsibility, executed strictly serially and in the model's original
// tool_call order, because out-of-order message rows or a wrong resumeAfterID
// corrupt the transcript.
//
// Eligibility is intentionally narrow: a call qualifies only if its resolved
// tool reports tools.RiskRead for that input, it is not a pipeline control
// tool, and it is not one of the agentic/exec tools (Bash, Agent, Task,
// MCPCallTool, Symbols). Any call this function cannot confidently classify is
// left ineligible. If fewer than two calls are eligible, it returns nil so the
// caller's loop runs entirely serially and unchanged — overlap only pays for
// itself when there is more than one independent read to hide.
//
// Each dispatched worker writes only its own results[index]; disjoint indices
// plus the WaitGroup barrier below (no read of results happens until after
// Wait returns) is what makes this race-free without relying on the race
// detector, which is unavailable on this host. No goroutine is dispatched once
// ctx is observed canceled, and none outlives this function: it always waits
// for every worker it started before returning.
func (r *Runner) prefetchToolCallResults(ctx context.Context, agentID, runID string, calls []providers.ToolCall, assistantMessageID string, toolset map[string]tools.Tool) []prefetchedToolResult {
	if len(calls) < 2 {
		return nil
	}
	// Read risk alone is not enough to run a call early. A read tool still asks
	// for approval when workflow preferences disable auto-allow for reads, or
	// when a rule says ask, and prefetching those would put several approval
	// prompts on screen at once for calls the user may never have wanted run.
	// Only calls that policy already resolves to a plain allow are prefetched.
	_, policy, policyErr := r.policyContext(ctx, agentID, runID)
	if policyErr != nil {
		return nil
	}
	eligible := make([]bool, len(calls))
	eligibleCount := 0
	for i, call := range calls {
		if !toolCallPrefetchEligible(call, toolset) {
			continue
		}
		decision := r.resolveToolPermission(ctx, agentID, policy.PermissionMode, strings.TrimSpace(call.Name), tools.RiskRead, call.Input)
		if decision.Decision != toolPermissionAllow {
			continue
		}
		eligible[i] = true
		eligibleCount++
	}
	if eligibleCount < 2 {
		return nil
	}

	results := make([]prefetchedToolResult, len(calls))
	sem := make(chan struct{}, maxToolPrefetchWorkers)
	var wg sync.WaitGroup
	for i, call := range calls {
		if !eligible[i] {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, call providers.ToolCall) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic anywhere in the worker body — including inside the tool's
			// own Execute — must become this slot's error result, not a crashed
			// process: nothing above this goroutine can recover it. ran is set
			// so the caller reports the error instead of re-running a call whose
			// side effects may already have happened.
			defer func() {
				if value := recover(); value != nil {
					slog.Error("tool prefetch worker panicked",
						"toolName", call.Name, "agentId", agentID, "runId", runID,
						"panic", value, "stack", string(debug.Stack()))
					results[i] = prefetchedToolResult{ran: true, executeErr: fmt.Errorf("tool %s panicked: %v", call.Name, value)}
				}
			}()
			if ctx.Err() != nil {
				return
			}
			res, err := r.executeToolForLoop(ctx, agentID, runID, tools.Call{ID: call.ID, Name: call.Name, Input: call.Input}, assistantMessageID, toolset)
			results[i] = prefetchedToolResult{ran: true, result: res, executeErr: err}
		}(i, call)
	}
	wg.Wait()
	return results
}

// toolCallPrefetchEligible decides whether a single tool call may safely run
// ahead of the serial per-call loop. Read-risk-only is required because that
// is the one risk tier with no side effect for permission resolution or the
// workspace to race against; pipeline control tools and the agentic/exec tools
// are excluded even when nominally read-risk because they interact with
// process-wide or cross-call state (the output pipeline, subprocess execution,
// nested agent/task orchestration, MCP calls, and the LSP session) that this
// prefetch pass has no way to serialize.
func toolCallPrefetchEligible(call providers.ToolCall, toolset map[string]tools.Tool) bool {
	name := strings.TrimSpace(call.Name)
	switch name {
	case "Bash", "Agent", "Task", "MCPCallTool", "Symbols":
		return false
	}
	if tools.IsToolOutputPipelineControl(name) {
		return false
	}
	if toolset == nil {
		return false
	}
	tool, ok := toolset[name]
	if !ok || tool == nil {
		return false
	}
	risk, ok := safeToolRisk(tool, call.Input)
	return ok && risk == tools.RiskRead
}

// safeToolRisk isolates the call to Tool.Risk so a panicking implementation
// (malformed input, or a future tool with a sharp edge) degrades to "cannot
// determine eligibility" rather than crashing the segment loop.
func safeToolRisk(tool tools.Tool, input json.RawMessage) (risk tools.Risk, ok bool) {
	defer func() {
		if recover() != nil {
			risk, ok = "", false
		}
	}()
	return tool.Risk(input), true
}
