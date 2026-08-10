package agent

import (
	"fmt"
	"strings"

	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// replayClassForCall derives the durable replay class for one tool call from the
// run's frozen tool snapshot.
//
// The snapshot is the only trustworthy source at this point. Recovery runs after
// a restart, by which time plugins may have been disabled, MCP servers removed,
// or the catalog otherwise changed, so a class derived then would describe a
// different tool than the one that actually started. Deriving at start time and
// persisting the answer keeps the decision bound to the call it describes.
//
// Any failure to resolve collapses to tools.ReplayNever, preserving the previous
// behaviour of never treating an interrupted call as repeatable.
func (r *Runner) replayClassForCall(runID string, call providers.ToolCall) tools.ReplayClass {
	if r == nil {
		return tools.ReplayNever
	}
	snapshot, ok := r.runRuntimeSnapshot(strings.TrimSpace(runID))
	if !ok || snapshot == nil || snapshot.tools.tools == nil {
		return tools.ReplayNever
	}
	tool, ok := snapshot.tools.tools[strings.TrimSpace(call.Name)]
	if !ok || tool == nil {
		return tools.ReplayNever
	}
	return tools.ClassifyReplay(tool, call.Input)
}

// interruptedGroupSummary counts how an unsettled group's non-terminal items are
// classified, so recovery can explain itself instead of emitting one opaque
// reason for every interrupted group.
//
// This deliberately does not decide to re-dispatch anything. The ledger persists
// tool_use_id and tool_name but not the effective arguments, and the run's tool
// snapshot is process-local, so after a restart there is nothing to replay from.
// The persisted class is therefore used to tell the model which calls are safe to
// re-issue, not to silently repeat them.
type interruptedGroupSummary struct {
	Pending      int
	Replayable   int
	Unreplayable int
}

// summarizeInterruptedGroup classifies the non-terminal items of one group.
// An item counts as replayable only when its persisted class is exactly "safe";
// the store re-narrows that value on read, so a hand-edited or downgraded row
// cannot widen it here.
func summarizeInterruptedGroup(group db.ToolExecutionGroup) interruptedGroupSummary {
	summary := interruptedGroupSummary{}
	for _, item := range group.Items {
		if item.Status != db.ToolExecutionItemStatusPending {
			continue
		}
		summary.Pending++
		if item.ReplayClass == db.ToolExecutionReplaySafe {
			summary.Replayable++
			continue
		}
		summary.Unreplayable++
	}
	return summary
}

// interruptedAbortReason renders the durable abort reason for one interrupted
// group. The wording distinguishes the two cases so a later reader of the ledger
// can tell whether work was lost that could safely have been redone.
func interruptedAbortReason(summary interruptedGroupSummary) string {
	if summary.Pending == 0 {
		return "process restarted while tool execution group was incomplete"
	}
	if summary.Unreplayable == 0 {
		return fmt.Sprintf(
			"process restarted with %d incomplete tool call(s), all replay-safe; the model must re-issue them",
			summary.Replayable,
		)
	}
	if summary.Replayable == 0 {
		return fmt.Sprintf(
			"process restarted with %d incomplete tool call(s) that are not safe to repeat",
			summary.Unreplayable,
		)
	}
	return fmt.Sprintf(
		"process restarted with %d incomplete tool call(s): %d replay-safe, %d not safe to repeat",
		summary.Pending, summary.Replayable, summary.Unreplayable,
	)
}
