package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"autoto/internal/db"
	"autoto/internal/providers"
)

// The tool execution ledger and the assistant message that produced it are two
// durable records of the same event. Recovery reads both, so they can disagree:
// a partially applied write, a hand-edited database, or a schema downgrade can
// leave a ledger item that does not correspond to any tool call the model
// actually made.
//
// Re-dispatching from a ledger that disagrees with its assistant message would
// run a tool with arguments belonging to a different call. So the pair is
// validated first, and every way it can be inconsistent has a name. A named
// reason is refused, never repaired: these states cannot be produced by the
// normal single-writer path, so "fixing" one would be inventing history.
type LedgerCorruptionReason string

const (
	// LedgerItemCountMismatch means the ledger and the message disagree on how
	// many tool calls the turn contained.
	LedgerItemCountMismatch LedgerCorruptionReason = "item_count_mismatch"
	// LedgerOrdinalOutOfRange means an item's ordinal addresses a tool call the
	// assistant message does not have.
	LedgerOrdinalOutOfRange LedgerCorruptionReason = "ordinal_out_of_range"
	// LedgerToolCallMismatch means the item at an ordinal names a different tool
	// call than the message does. This is the check that makes argument recovery
	// safe to trust.
	LedgerToolCallMismatch LedgerCorruptionReason = "tool_call_mismatch"
	// LedgerDuplicateOrdinal means two items claim the same position.
	LedgerDuplicateOrdinal LedgerCorruptionReason = "duplicate_ordinal"
	// LedgerMissingAssistantCall means the message carries no tool calls at all
	// while the ledger says it should.
	LedgerMissingAssistantCall LedgerCorruptionReason = "missing_assistant_call"
)

// LedgerCorruption is the refusal. It carries the machine-readable reason plus
// human-readable detail, so a caller can branch on the reason and an operator
// can read the detail.
type LedgerCorruption struct {
	Reason  LedgerCorruptionReason
	Message string
}

func (e *LedgerCorruption) Error() string {
	return fmt.Sprintf("tool execution ledger corruption (%s): %s", e.Reason, e.Message)
}

func ledgerCorrupt(reason LedgerCorruptionReason, format string, args ...any) *LedgerCorruption {
	return &LedgerCorruption{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// assistantToolCalls extracts the tool calls from an assistant message in the
// order the model emitted them. Ordinal is defined as the index into this slice,
// which is how the ledger assigned it when the group was created.
func assistantToolCalls(message db.Message) []providers.ContentBlock {
	blocks := contentBlocksFromMessage(message)
	calls := make([]providers.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "tool_use" {
			calls = append(calls, block)
		}
	}
	return calls
}

// ValidateToolExecutionLedger checks one group against its assistant message.
//
// It is pure: it reads no state and mutates nothing, so it can be tested
// directly against constructed inputs rather than only through a live recovery.
func ValidateToolExecutionLedger(group db.ToolExecutionGroup, assistant db.Message) error {
	calls := assistantToolCalls(assistant)
	if len(group.Items) > 0 && len(calls) == 0 {
		return ledgerCorrupt(LedgerMissingAssistantCall,
			"group %s has %d item(s) but assistant message %s carries no tool calls",
			group.ID, len(group.Items), assistant.ID)
	}
	if group.ExpectedCount != len(calls) {
		return ledgerCorrupt(LedgerItemCountMismatch,
			"group %s expects %d call(s) but assistant message %s carries %d",
			group.ID, group.ExpectedCount, assistant.ID, len(calls))
	}

	seen := make(map[int]struct{}, len(group.Items))
	for _, item := range group.Items {
		if _, exists := seen[item.Ordinal]; exists {
			return ledgerCorrupt(LedgerDuplicateOrdinal,
				"group %s has two items at ordinal %d", group.ID, item.Ordinal)
		}
		seen[item.Ordinal] = struct{}{}

		if item.Ordinal < 0 || item.Ordinal >= len(calls) {
			return ledgerCorrupt(LedgerOrdinalOutOfRange,
				"group %s item %s has ordinal %d outside the %d call(s) in message %s",
				group.ID, item.ToolUseID, item.Ordinal, len(calls), assistant.ID)
		}

		call := calls[item.Ordinal]
		if strings.TrimSpace(call.ToolUseID) != strings.TrimSpace(item.ToolUseID) {
			return ledgerCorrupt(LedgerToolCallMismatch,
				"group %s ordinal %d is %q in the ledger but %q in message %s",
				group.ID, item.Ordinal, item.ToolUseID, call.ToolUseID, assistant.ID)
		}
		if strings.TrimSpace(call.ToolName) != strings.TrimSpace(item.ToolName) {
			return ledgerCorrupt(LedgerToolCallMismatch,
				"group %s ordinal %d names tool %q in the ledger but %q in message %s",
				group.ID, item.Ordinal, item.ToolName, call.ToolName, assistant.ID)
		}
	}
	return nil
}

// RecoverToolCallInput returns the durable arguments for one ledger item.
//
// The arguments were never stored on the ledger row; they live in the assistant
// message that requested the call. That is deliberate: one copy means the two
// cannot drift, and no additional row can leak a Write payload or a Bash command
// into a second table.
//
// The ledger/message pair is validated before the input is returned, so a caller
// cannot receive arguments that belong to a different call.
func RecoverToolCallInput(group db.ToolExecutionGroup, assistant db.Message, toolUseID string) (json.RawMessage, error) {
	if err := ValidateToolExecutionLedger(group, assistant); err != nil {
		return nil, err
	}
	toolUseID = strings.TrimSpace(toolUseID)
	calls := assistantToolCalls(assistant)
	for _, item := range group.Items {
		if strings.TrimSpace(item.ToolUseID) != toolUseID {
			continue
		}
		// Validation above already proved the ordinal addresses this exact call.
		return calls[item.Ordinal].Input, nil
	}
	return nil, ledgerCorrupt(LedgerToolCallMismatch,
		"group %s has no item for tool use id %q", group.ID, toolUseID)
}
