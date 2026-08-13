package agent

import (
	"fmt"
	"log/slog"
	"strings"

	"autoto/internal/spill"
	"autoto/internal/tools"
)

// spillPreviewSeparator marks the cut between the head and the tail so the two
// halves do not read as one continuous line. Its cost is reserved
// unconditionally, which spends a few bytes of the budget on a preview that
// turns out to have no tail but keeps the reservation independent of the split.
const spillPreviewSeparator = "\n...\n"

// Tools whose results are never spilled. Read and Grep are the retrieval path
// the spill notice points at, so spilling their output would answer a Read with
// an instruction to Read again. EndPipeline is a retrieval too: the model
// already chose its own max_chars there, and replacing that with a file path
// would discard the filtering it asked for.
func spillExemptTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "Read", "Grep":
		return true
	}
	return tools.IsToolOutputPipelineControl(name)
}

func (r *Runner) SetToolOutputSpillStore(store *spill.Store) {
	if r == nil {
		return
	}
	r.spillMu.Lock()
	r.spillStore = store
	r.spillMu.Unlock()
}

func (r *Runner) toolOutputSpillStore() *spill.Store {
	if r == nil {
		return nil
	}
	r.spillMu.RLock()
	defer r.spillMu.RUnlock()
	return r.spillStore
}

// spillToolResultForModel keeps an oversized result out of the request while
// leaving the whole of it reachable on disk. Every failure path returns the
// original result: a store that is missing, a conversation the store refuses,
// or a write that fails must never turn a tool call that succeeded into an
// error, or hide output that was about to be delivered inline.
func (r *Runner) spillToolResultForModel(agentID string, call tools.Call, result tools.Result) tools.Result {
	if r == nil {
		return result
	}
	limit := r.cfg.ToolOutputSpillBytes
	total := len(result.Output)
	if limit <= 0 || total <= limit || spillExemptTool(call.Name) {
		return result
	}
	store := r.toolOutputSpillStore()
	if store == nil {
		return result
	}
	path, err := store.Save(agentID, call.Name, result.Output)
	if err != nil {
		slog.Warn("spill oversized tool output failed; keeping the inline result",
			"agentId", agentID, "toolName", call.Name, "bytes", total, "error", err)
		return result
	}
	replaced, ok := spillReplacement(result.Output, total, path, limit)
	if !ok {
		// The notice alone does not fit the cap, which takes either a tiny cap or
		// a very long spill root. Keeping the inline result is the lesser evil: a
		// replacement larger than the cap breaks the one guarantee this policy
		// makes. The file just written is a harmless orphan the retention sweep
		// collects.
		slog.Warn("spill notice exceeds the tool output cap; keeping the inline result",
			"agentId", agentID, "toolName", call.Name, "bytes", total)
		return result
	}
	return tools.Result{Output: replaced, IsError: result.IsError, Meta: map[string]any{
		"spillPath":  path,
		"spillBytes": total,
	}}
}

// spillReplacement builds the head/tail preview plus notice, or reports that no
// within-cap replacement exists. The notice's cost is reserved inside the cap
// rather than appended to a full-budget preview, so the replacement of a result
// that only just crossed the threshold cannot come out larger than the original.
// The reservation prices the notice at the worst-case omission count, whose
// digits bound the real count's.
func spillReplacement(output string, total int, path string, limit int) (string, bool) {
	budget := limit - len(spillNotice(total, total, path)) - len("\n\n") - len(spillPreviewSeparator)
	if budget < 0 {
		budget = 0
	}
	head, tail := spillPreview(output, budget)
	preview := head
	if tail != "" {
		preview += spillPreviewSeparator + tail
	}
	notice := spillNotice(total-len(head)-len(tail), total, path)
	replaced := notice
	if preview != "" {
		replaced = preview + "\n\n" + notice
	}
	if len(replaced) > limit {
		return "", false
	}
	return replaced, true
}

// spillPreview splits the byte budget between the start and the end of the
// output. Invalid UTF-8 is repaired first: a Bash result carrying raw bytes
// would otherwise make the rune-safe truncation back off to nothing. Head and
// tail cannot overlap because their combined length stays under the budget,
// which is itself under the length of an output large enough to spill.
func spillPreview(output string, budget int) (head, tail string) {
	if budget <= 0 {
		return "", ""
	}
	output = strings.ToValidUTF8(output, "\uFFFD")
	if len(output) <= budget {
		return output, ""
	}
	head, _ = truncateUTF8Bytes(output, (budget+1)/2)
	return head, tailUTF8Bytes(output, budget-len(head))
}

func spillNotice(omitted, total int, path string) string {
	return fmt.Sprintf("(%d of %d bytes omitted. The full tool output is stored at %s -- page through it with Read using offset and line_limit, or search it with Grep.)", omitted, total, path)
}
