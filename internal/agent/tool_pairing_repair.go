package agent

import (
	"fmt"
	"strings"

	"autoto/internal/providers"
)

// Every provider that accepts structured tool calls requires each tool_use to be
// answered by exactly one tool_result. Anthropic rejects an unanswered call with
// a 400, and the others are no more forgiving.
//
// Autoto can persist a conversation that violates this, through no fault of the
// provider layer. A run killed while a tool was executing records the tool_use
// and never records the result. Startup recovery then aborts the group, which
// makes the ledger consistent but does not add the missing result: the abort
// reason is durable, and the model never sees it. A correction can also supersede
// the message holding one half of a pair.
//
// The Codex adapter already repaired this for itself
// (repairCodexToolCallPairing). Doing it per adapter means every new provider
// starts out broken until someone remembers, so the repair belongs on the shared
// path that all of them go through. This runs in runModelTurnAttempt, so no
// adapter can skip it and no future adapter has to know it exists.
//
// The two halves are treated asymmetrically on purpose:
//
//   - An unanswered call gets a synthetic error result. Dropping the call instead
//     would erase an action the assistant actually took, and the model would
//     re-plan against a history it never lived.
//   - An unmatched result is dropped. It refers to a call the model cannot see,
//     so it is unintelligible, and inventing a call to justify it would be
//     fabricating a request that was never made.
const orphanedToolResultOutput = "This tool call did not complete: Autoto stopped before a result was recorded. No result is available. Re-issue the call if you still need it."

// repairToolCallPairing returns messages whose tool_use and tool_result blocks
// are fully paired. Input is not mutated; only messages needing a change are
// rebuilt.
//
// It is intentionally a pure function over the provider message slice so it can
// be tested directly and reasoned about without a live run.
func repairToolCallPairing(messages []providers.Message) []providers.Message {
	answered, requested := toolPairingIndex(messages)

	// Nothing to do is the overwhelmingly common case, so detect it before
	// allocating anything.
	if !toolPairingNeedsRepair(messages, answered, requested) {
		return messages
	}

	out := make([]providers.Message, 0, len(messages)+1)
	for _, message := range messages {
		if len(message.Blocks) == 0 {
			out = append(out, message)
			continue
		}

		blocks := make([]providers.ContentBlock, 0, len(message.Blocks))
		var synthesized []providers.ContentBlock
		for _, block := range message.Blocks {
			switch block.Type {
			case "tool_use":
				blocks = append(blocks, block)
				id := strings.TrimSpace(block.ToolUseID)
				if id == "" || answered[id] {
					continue
				}
				// Answer it immediately after the call. Providers accept the result
				// in the following message; emitting it here keeps the pairing
				// adjacent and order-independent for adapters that scan linearly.
				synthesized = append(synthesized, providers.ContentBlock{
					Type:      "tool_result",
					ToolUseID: id,
					ToolName:  block.ToolName,
					Output:    orphanedToolResultOutput,
					IsError:   true,
				})
			case "tool_result":
				id := strings.TrimSpace(block.ToolUseID)
				if id == "" || !requested[id] {
					// Unmatched result: drop it rather than describe a call the model
					// never made.
					continue
				}
				blocks = append(blocks, block)
			default:
				blocks = append(blocks, block)
			}
		}

		message.Blocks = blocks
		if content := strings.TrimSpace(contextMessageContent(blocks)); content != "" {
			message.Content = content
		}
		// A message that held only unmatched results now holds nothing, and an
		// empty message is itself invalid for some providers.
		if len(blocks) == 0 && strings.TrimSpace(message.Content) == "" {
			if len(synthesized) == 0 {
				continue
			}
		}
		out = append(out, message)

		if len(synthesized) > 0 {
			out = append(out, syntheticToolResultMessage(synthesized))
		}
	}
	return out
}

// syntheticToolResultMessage wraps repair results in the user-role message shape
// that real tool results already use, so adapters cannot distinguish a repaired
// turn from an ordinary one by message shape.
func syntheticToolResultMessage(blocks []providers.ContentBlock) providers.Message {
	names := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if name := strings.TrimSpace(block.ToolName); name != "" {
			names = append(names, name)
		}
	}
	text := orphanedToolResultOutput
	if len(names) > 0 {
		text = fmt.Sprintf("%s (%s)", orphanedToolResultOutput, strings.Join(names, ", "))
	}
	return providers.Message{Role: "user", Content: text, Blocks: blocks}
}

// toolPairingIndex records which calls were answered and which results have a
// call. Both directions are needed because the two orphan kinds are handled
// differently.
func toolPairingIndex(messages []providers.Message) (answered, requested map[string]bool) {
	answered = make(map[string]bool)
	requested = make(map[string]bool)
	for _, message := range messages {
		for _, block := range message.Blocks {
			id := strings.TrimSpace(block.ToolUseID)
			if id == "" {
				continue
			}
			switch block.Type {
			case "tool_use":
				requested[id] = true
			case "tool_result":
				answered[id] = true
			}
		}
	}
	return answered, requested
}

// toolPairingNeedsRepair reports whether any block is unpaired.
func toolPairingNeedsRepair(messages []providers.Message, answered, requested map[string]bool) bool {
	for _, message := range messages {
		for _, block := range message.Blocks {
			id := strings.TrimSpace(block.ToolUseID)
			switch block.Type {
			case "tool_use":
				if id == "" || !answered[id] {
					return true
				}
			case "tool_result":
				if id == "" || !requested[id] {
					return true
				}
			}
		}
	}
	return false
}
