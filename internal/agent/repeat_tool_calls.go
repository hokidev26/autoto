package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"autoto/internal/config"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// repeatToolChain is one Run's consecutive-repeat streak: the identity of the
// last tracked call, how long the streak is, and the reminder the next turn owes
// the model. The reminder is parked rather than delivered immediately because
// counting happens while a tool result is being settled, and there is no request
// being built at that moment.
type repeatToolChain struct {
	agentID  string
	key      string
	count    int
	reminder *providers.Message
}

// repeatToolCallDetector owns the consecutive-repeat ladder. Live streaks stay
// on runtimeSnapshotState.repeats so closeRunRuntimeSnapshot can drop a run's
// snapshot and its chain under the same mutex.
type repeatToolCallDetector struct {
	thresholds []int
}

func repeatToolCallChainsLocked(state *runtimeSnapshotState) map[string]*repeatToolChain {
	if state == nil {
		return nil
	}
	if state.repeats == nil {
		state.repeats = make(map[string]*repeatToolChain)
	}
	return state.repeats
}

func (r *Runner) repeatToolCallChainsLocked() map[string]*repeatToolChain {
	return repeatToolCallChainsLocked(r.ensureRuntimeState())
}

// observeRepeatedToolCall advances the Run's streak by one attempt and parks a
// reminder when the streak lands on a configured threshold. It observes only: a
// call that has already run cannot be vetoed, and the point is to shape the next
// request rather than this one's result.
func (r *Runner) observeRepeatedToolCall(agentID, runID string, call tools.Call) {
	if r == nil || len(r.repeatCalls.thresholds) == 0 {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	r.repeatCalls.observe(r.ensureRuntimeState(), agentID, runID, call)
}

func (d *repeatToolCallDetector) observe(state *runtimeSnapshotState, agentID, runID string, call tools.Call) {
	if d == nil || len(d.thresholds) == 0 || state == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	arguments := canonicalToolArguments(call.Input)
	key := strings.TrimSpace(call.Name) + "\x00" + arguments

	state.mu.Lock()
	defer state.mu.Unlock()
	chains := repeatToolCallChainsLocked(state)
	chain := chains[runID]
	if chain == nil || chain.key != key {
		chain = &repeatToolChain{agentID: strings.TrimSpace(agentID), key: key}
		chains[runID] = chain
	}
	chain.count++
	if !containsThreshold(d.thresholds, chain.count) {
		return
	}
	message := repeatToolCallControlMessage(call.Name, chain.count, arguments, chain.count == d.thresholds[0])
	chain.reminder = &message
}

// repeatToolCallControl hands the parked reminder to the turn being built and
// clears it, so one streak length is reported once however many turns follow.
func (r *Runner) repeatToolCallControl(runID string) *providers.Message {
	if r == nil {
		return nil
	}
	return r.repeatCalls.control(r.ensureRuntimeState(), runID)
}

func (d *repeatToolCallDetector) control(state *runtimeSnapshotState, runID string) *providers.Message {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	chain := repeatToolCallChainsLocked(state)[strings.TrimSpace(runID)]
	if chain == nil || chain.reminder == nil {
		return nil
	}
	reminder := chain.reminder
	chain.reminder = nil
	return reminder
}

// resetRepeatToolCallChains drops an Agent's streaks when a new user message
// arrives. Repetition either side of a human interjection is not a loop, and the
// interjection is usually what redirected the work.
func (r *Runner) resetRepeatToolCallChains(agentID string) {
	if r == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	r.repeatCalls.resetAgent(r.ensureRuntimeState(), agentID)
}

func (d *repeatToolCallDetector) resetAgent(state *runtimeSnapshotState, agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	chains := repeatToolCallChainsLocked(state)
	for runID, chain := range chains {
		if chain.agentID == agentID {
			delete(chains, runID)
		}
	}
}

// canonicalToolArguments re-encodes the arguments so two calls that differ only
// in property order compare equal: encoding/json sorts object keys on the way
// out, at every depth. Arguments that will not parse are compared as the raw
// text the provider sent, which is the only identity available for them.
func canonicalToolArguments(input json.RawMessage) string {
	raw := strings.TrimSpace(string(input))
	if raw == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func containsThreshold(thresholds []int, count int) bool {
	for _, threshold := range thresholds {
		if threshold == count {
			return true
		}
	}
	return false
}

// The first threshold gets the gentle wording and the later ones name the call,
// because a model that ignored the gentle nudge needs to see exactly which call
// it is repeating. The arguments are bounded: a repeated Write or a long command
// would otherwise ride into every subsequent request in full, precisely in the
// loop this is trying to break. Detection always compares the whole string.
func repeatToolCallControlMessage(toolName string, count int, arguments string, gentle bool) providers.Message {
	toolName = strings.TrimSpace(toolName)
	if gentle {
		text := fmt.Sprintf("SERVER REPEATED TOOL CALL NOTICE (trusted): You have called %s with identical arguments %d times in a row. Read the latest result before calling it again: if the task is not finished, change the arguments or take a different approach instead of repeating the call. Do not mention this control message.", toolName, count)
		return turnControlMessage("server_repeated_tool_call", text)
	}
	bounded, truncated := truncateUTF8Bytes(arguments, config.RepeatToolCallArgumentsBytes)
	if truncated {
		bounded += " (arguments truncated)"
	}
	text := fmt.Sprintf("SERVER REPEATED TOOL CALL NOTICE (trusted): %s has now been called %d times in a row with these exact arguments:\n%s\nThe repeats are not making progress. Do not call this tool with these arguments again. Inspect the latest result and choose a different action, different arguments, or finish the task if you already have enough evidence. Do not mention this control message.", toolName, count, bounded)
	return turnControlMessage("server_repeated_tool_call", text)
}
