package providers

import (
	"encoding/json"
	"testing"
)

// The Anthropic Messages API requires every tool_use block to be answered by a
// tool_result block; an unanswered call is rejected with a 400. Unlike the Codex
// adapter, this adapter has no pairing repair of its own: anthropicContentBlocks
// passes tool_use straight through.
//
// That is deliberate now. Pairing is repaired once on the shared path in
// internal/agent (repairToolCallPairing) rather than in each adapter, so this
// test pins the boundary contract instead of duplicating the repair here: given
// paired input the adapter emits paired output, and it never silently discards
// half of a pair.
//
// If someone later moves pairing responsibility back into the adapters, the first
// assertion below still describes what this one has to produce.
func TestAnthropicContentBlocksPreservesToolPairing(t *testing.T) {
	assistantBlocks := []ContentBlock{
		{Type: "tool_use", ToolUseID: "call-1", ToolName: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`)},
	}
	userBlocks := []ContentBlock{
		{Type: "tool_result", ToolUseID: "call-1", ToolName: "Read", Output: "contents"},
	}

	assistantOut := anthropicContentBlocks(assistantBlocks, true, "claude-sonnet-4-6")
	userOut := anthropicContentBlocks(userBlocks, false, "claude-sonnet-4-6")

	if len(assistantOut) != 1 {
		t.Fatalf("expected the tool_use block to survive, got %d blocks", len(assistantOut))
	}
	if len(userOut) != 1 {
		t.Fatalf("expected the tool_result block to survive, got %d blocks", len(userOut))
	}
}

// TestAnthropicContentBlocksDropsIncompleteToolBlocks documents the adapter's
// existing guard: a block missing the ids Anthropic requires is omitted rather
// than sent as a malformed request.
//
// This is why pairing must be repaired upstream. The adapter can only drop what
// it cannot represent; it has no way to invent the missing counterpart, so an
// unpaired call would otherwise reach the API and fail the whole request.
func TestAnthropicContentBlocksDropsIncompleteToolBlocks(t *testing.T) {
	cases := map[string][]ContentBlock{
		"tool_use without id":    {{Type: "tool_use", ToolName: "Read", Input: json.RawMessage(`{}`)}},
		"tool_use without name":  {{Type: "tool_use", ToolUseID: "call-1", Input: json.RawMessage(`{}`)}},
		"tool_result without id": {{Type: "tool_result", ToolName: "Read", Output: "x"}},
	}
	for name, blocks := range cases {
		t.Run(name, func(t *testing.T) {
			if out := anthropicContentBlocks(blocks, true, "claude-sonnet-4-6"); len(out) != 0 {
				t.Fatalf("expected an unrepresentable block to be dropped, got %d blocks", len(out))
			}
		})
	}
}
