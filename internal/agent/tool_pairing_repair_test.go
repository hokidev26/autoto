package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"autoto/internal/providers"
)

func toolUseBlock(id, name string) providers.ContentBlock {
	return providers.ContentBlock{Type: "tool_use", ToolUseID: id, ToolName: name, Input: json.RawMessage(`{}`)}
}

func toolResultBlock(id, name, output string) providers.ContentBlock {
	return providers.ContentBlock{Type: "tool_result", ToolUseID: id, ToolName: name, Output: output}
}

// pairingIndexOf collects call and result ids from a repaired slice so tests can
// assert on pairing without depending on message layout.
func pairingIndexOf(messages []providers.Message) (calls, results map[string]int) {
	calls = map[string]int{}
	results = map[string]int{}
	for _, message := range messages {
		for _, block := range message.Blocks {
			switch block.Type {
			case "tool_use":
				calls[block.ToolUseID]++
			case "tool_result":
				results[block.ToolUseID]++
			}
		}
	}
	return calls, results
}

// assertFullyPaired is the invariant every provider depends on.
func assertFullyPaired(t *testing.T, messages []providers.Message) {
	t.Helper()
	calls, results := pairingIndexOf(messages)
	for id, count := range calls {
		if count != 1 {
			t.Fatalf("call %q appears %d times, want exactly 1", id, count)
		}
		if results[id] != 1 {
			t.Fatalf("call %q has %d results, want exactly 1", id, results[id])
		}
	}
	for id := range results {
		if calls[id] != 1 {
			t.Fatalf("result %q has no matching call", id)
		}
	}
}

// TestRepairToolCallPairingLeavesHealthyHistoryUntouched is the no-op guarantee.
// The repair runs on every model turn, so it must not perturb ordinary
// conversations or allocate when nothing is wrong.
func TestRepairToolCallPairingLeavesHealthyHistoryUntouched(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "read it", Blocks: []providers.ContentBlock{{Type: "text", Text: "read it"}}},
		{Role: "assistant", Blocks: []providers.ContentBlock{toolUseBlock("call-1", "Read")}},
		{Role: "user", Blocks: []providers.ContentBlock{toolResultBlock("call-1", "Read", "file contents")}},
		{Role: "assistant", Blocks: []providers.ContentBlock{{Type: "text", Text: "done"}}},
	}
	repaired := repairToolCallPairing(messages)
	if len(repaired) != len(messages) {
		t.Fatalf("healthy history changed length: %d to %d", len(messages), len(repaired))
	}
	// Same backing array means no work was done at all.
	if &repaired[0] != &messages[0] {
		t.Fatal("healthy history was needlessly rebuilt")
	}
	assertFullyPaired(t, repaired)
}

// TestRepairToolCallPairingAnswersAnInterruptedCall is the bug this fixes: a run
// killed mid-tool records the call and never the result, and providers reject the
// unanswered call outright.
func TestRepairToolCallPairingAnswersAnInterruptedCall(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "write it", Blocks: []providers.ContentBlock{{Type: "text", Text: "write it"}}},
		{Role: "assistant", Blocks: []providers.ContentBlock{toolUseBlock("call-killed", "Write")}},
		// Process restarted here. No tool_result was ever persisted.
	}
	repaired := repairToolCallPairing(messages)
	assertFullyPaired(t, repaired)

	_, results := pairingIndexOf(repaired)
	if results["call-killed"] != 1 {
		t.Fatalf("interrupted call was not answered: %+v", repaired)
	}

	// The synthetic answer must be an error, so the model treats it as a failure
	// rather than an empty success.
	var found bool
	for _, message := range repaired {
		for _, block := range message.Blocks {
			if block.Type == "tool_result" && block.ToolUseID == "call-killed" {
				found = true
				if !block.IsError {
					t.Fatal("synthetic result must be marked as an error")
				}
				if !strings.Contains(block.Output, "did not complete") {
					t.Fatalf("synthetic result does not explain itself: %q", block.Output)
				}
			}
		}
	}
	if !found {
		t.Fatal("no synthetic result block was produced")
	}
}

// TestRepairToolCallPairingAnswersOnlyTheMissingHalf covers the partial case:
// one call in a parallel group finished and one did not. The completed result
// must be preserved exactly.
func TestRepairToolCallPairingAnswersOnlyTheMissingHalf(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", Blocks: []providers.ContentBlock{
			toolUseBlock("call-done", "Read"),
			toolUseBlock("call-lost", "Read"),
		}},
		{Role: "user", Blocks: []providers.ContentBlock{toolResultBlock("call-done", "Read", "real output")}},
	}
	repaired := repairToolCallPairing(messages)
	assertFullyPaired(t, repaired)

	for _, message := range repaired {
		for _, block := range message.Blocks {
			if block.Type != "tool_result" || block.ToolUseID != "call-done" {
				continue
			}
			if block.Output != "real output" {
				t.Fatalf("a completed result was altered: %q", block.Output)
			}
			if block.IsError {
				t.Fatal("a completed result was marked as an error")
			}
		}
	}
}

// TestRepairToolCallPairingDropsAnUnmatchedResult is the asymmetric half. A
// result whose call is gone refers to something the model cannot see, so it is
// removed rather than justified with an invented call.
func TestRepairToolCallPairingDropsAnUnmatchedResult(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", Blocks: []providers.ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "user", Blocks: []providers.ContentBlock{toolResultBlock("call-vanished", "Read", "stale")}},
	}
	repaired := repairToolCallPairing(messages)
	assertFullyPaired(t, repaired)

	calls, results := pairingIndexOf(repaired)
	if len(results) != 0 {
		t.Fatalf("unmatched result was kept: %+v", repaired)
	}
	if len(calls) != 0 {
		t.Fatalf("a call was invented to justify the result: %+v", repaired)
	}
}

// TestRepairToolCallPairingHandlesBlankToolUseIDs guards the degenerate case. A
// block with no id cannot be paired with anything, so it must not be silently
// treated as answered.
func TestRepairToolCallPairingHandlesBlankToolUseIDs(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", Blocks: []providers.ContentBlock{
			{Type: "tool_use", ToolUseID: "  ", ToolName: "Read", Input: json.RawMessage(`{}`)},
		}},
		{Role: "user", Blocks: []providers.ContentBlock{
			{Type: "tool_result", ToolUseID: "", ToolName: "Read", Output: "orphan"},
		}},
	}
	repaired := repairToolCallPairing(messages)
	// The unusable result is dropped; the idless call cannot be answered but must
	// not produce a result with an empty id either.
	for _, message := range repaired {
		for _, block := range message.Blocks {
			if block.Type == "tool_result" && strings.TrimSpace(block.ToolUseID) == "" {
				t.Fatalf("produced a tool_result with no id: %+v", repaired)
			}
		}
	}
}

// TestRepairToolCallPairingIsIdempotent matters because the repair runs on every
// turn, including turns whose history already contains a previous repair.
func TestRepairToolCallPairingIsIdempotent(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", Blocks: []providers.ContentBlock{toolUseBlock("call-1", "Read")}},
	}
	once := repairToolCallPairing(messages)
	twice := repairToolCallPairing(once)
	assertFullyPaired(t, twice)

	if len(once) != len(twice) {
		t.Fatalf("second repair changed the message count: %d to %d", len(once), len(twice))
	}
	_, results := pairingIndexOf(twice)
	if results["call-1"] != 1 {
		t.Fatalf("repeated repair duplicated the synthetic result: %+v", twice)
	}
}

// TestRepairToolCallPairingSurvivesCapabilityDowngrade is the ordering guarantee.
// The repair runs before blocks are rewritten for providers without structured
// tool support, so a downgraded conversation is still coherent.
func TestRepairToolCallPairingSurvivesCapabilityDowngrade(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", Blocks: []providers.ContentBlock{toolUseBlock("call-killed", "Write")}},
	}
	repaired := repairToolCallPairing(messages)
	downgraded := prepareProviderMessagesForCapabilities(repaired, providers.Capabilities{Tools: false})

	var mentionsOutcome bool
	for _, message := range downgraded {
		for _, block := range message.Blocks {
			if strings.Contains(block.Text, "did not complete") || strings.Contains(block.Output, "did not complete") {
				mentionsOutcome = true
			}
		}
		if strings.Contains(message.Content, "did not complete") {
			mentionsOutcome = true
		}
	}
	if !mentionsOutcome {
		t.Fatalf("the interrupted call's outcome was lost in downgrade: %+v", downgraded)
	}
}

// TestRepairToolCallPairingCoversEveryProviderShape is the point of doing this on
// the shared path. Whatever a provider's capabilities are, the messages reaching
// it are fully paired, so a new adapter is correct without knowing this exists.
func TestRepairToolCallPairingCoversEveryProviderShape(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", Blocks: []providers.ContentBlock{
			toolUseBlock("call-a", "Read"),
			toolUseBlock("call-b", "Write"),
		}},
		{Role: "user", Blocks: []providers.ContentBlock{toolResultBlock("call-a", "Read", "ok")}},
		{Role: "user", Blocks: []providers.ContentBlock{toolResultBlock("call-ghost", "Grep", "stale")}},
	}
	repaired := repairToolCallPairing(messages)

	for _, capabilities := range []providers.Capabilities{
		{Tools: true},
		{Tools: false},
		{Tools: true, ImageGeneration: true},
	} {
		prepared := prepareProviderMessagesForCapabilities(repaired, capabilities)
		if capabilities.Tools {
			assertFullyPaired(t, prepared)
		}
		for _, message := range prepared {
			for _, block := range message.Blocks {
				if block.Type == "tool_result" && block.ToolUseID == "call-ghost" {
					t.Fatalf("the ghost result reached a provider: %+v", prepared)
				}
			}
		}
	}
}
