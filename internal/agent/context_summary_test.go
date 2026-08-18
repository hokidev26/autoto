package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

// summaryTestMessage builds a candidate whose rendered line is ~1900 CJK runes,
// so a handful of them overflow the floored 8,000-token segment budget.
func summaryTestMessage(index int) db.Message {
	return db.Message{Role: "user", ContentText: fmt.Sprintf("MSG%02d", index) + strings.Repeat("句", 1894)}
}

func TestSummaryInputBudgetTokensFollowsSummaryModelWindow(t *testing.T) {
	provider := &scriptedProvider{contextLimits: map[string]int{"big": 100000, "tiny": 1000}}
	runner := newAgentTestRunner(nil, provider, config.AgentConfig{SummaryModel: "fake:big"})
	overhead := estimateTextTokens(summaryPromptInstructions) + estimateTextTokens(summarySystemPrompt)
	want := 100000*summaryInputWindowPercent/100 - overhead
	if got := runner.summaryInputBudgetTokens(""); got != want {
		t.Fatalf("window-driven budget = %d, want %d", got, want)
	}
	existing := strings.Repeat("界", 500)
	if got := runner.summaryInputBudgetTokens(existing); got != want-500 {
		t.Fatalf("budget must subtract the carried summary share: got %d, want %d", got, want-500)
	}
	tiny := newAgentTestRunner(nil, &scriptedProvider{contextLimits: map[string]int{"tiny": 1000}}, config.AgentConfig{SummaryModel: "fake:tiny"})
	if got := tiny.summaryInputBudgetTokens(""); got != minSummaryInputTokens {
		t.Fatalf("a tiny window must floor at %d tokens, got %d", minSummaryInputTokens, got)
	}
}

func TestSummarizeOldestMessagesRollsSegmentsAndCarriesPriorOutput(t *testing.T) {
	provider := &scriptedProvider{
		contextLimits: map[string]int{"small": 1000},
		turns: [][]providers.Event{
			{{Type: "text", Text: "segment one digest"}, {Type: "done", Done: true}},
			{{Type: "text", Text: "final rolled summary"}, {Type: "done", Done: true}},
		},
	}
	runner := newAgentTestRunner(nil, provider, config.AgentConfig{SummaryModel: "fake:small"})
	candidates := []db.Message{{Role: "assistant", ContentJSON: mustJSON([]providers.ContentBlock{
		{Type: "text", Text: "MSG01" + strings.Repeat("句", 1894)},
		{Type: "tool_use", ToolUseID: "t-prov", ToolName: "Read", Input: mustJSON(map[string]string{"file_path": "notes.md"})},
	})}}
	for i := 2; i <= 5; i++ {
		candidates = append(candidates, summaryTestMessage(i))
	}

	summary := runner.summarizeOldestMessages(context.Background(), db.Agent{ID: "conv-1"}, candidates)

	if got := provider.requestCount(); got != 2 {
		t.Fatalf("expected material to roll across 2 model calls, got %d", got)
	}
	first := provider.request(0).Messages[0].Content
	second := provider.request(1).Messages[0].Content
	if !strings.Contains(first, "MSG01") || !strings.Contains(first, "MSG04") || strings.Contains(first, "MSG05") {
		t.Fatalf("first segment must hold only the messages that fit the budget: %q", truncateRunes(first, 200))
	}
	if strings.Contains(first, "Existing summary:") {
		t.Fatalf("first segment must not invent a carried summary: %q", truncateRunes(first, 200))
	}
	if !strings.Contains(second, "Existing summary:\nsegment one digest") {
		t.Fatalf("second segment must carry the first segment's output as the existing summary: %q", truncateRunes(second, 400))
	}
	if !strings.Contains(second, "MSG05") || strings.Contains(second, "MSG01") {
		t.Fatalf("second segment must hold only the remaining messages: %q", truncateRunes(second, 400))
	}
	if !strings.HasPrefix(summary, "final rolled summary") {
		t.Fatalf("final summary must be the last segment's output, got %q", truncateRunes(summary, 200))
	}
	if !strings.Contains(summary, "Files touched in compacted history:") || !strings.Contains(summary, "read: notes.md") {
		t.Fatalf("file provenance must be appended to the rolled summary, got %q", summary)
	}
}

func TestSummaryModelForPrefersAgentOverride(t *testing.T) {
	runner := NewRunner(nil, nil, nil, nil, config.AgentConfig{SummaryModel: "fake:host"})
	if got := runner.summaryModelFor(db.Agent{}); got != "fake:host" {
		t.Fatalf("empty agent must inherit host summary model, got %q", got)
	}
	if got := runner.summaryModelFor(db.Agent{SummaryModel: " fake:override "}); got != "fake:override" {
		t.Fatalf("agent override must win, got %q", got)
	}
}

func TestSummarizeOldestMessagesUsesAgentSummaryModelOverride(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "text", Text: "override summary"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(nil, provider, config.AgentConfig{SummaryModel: "fake:host"})
	summary := runner.summarizeOldestMessages(context.Background(), db.Agent{ID: "conv-1", SummaryModel: "fake:override"}, []db.Message{
		{Role: "user", ContentText: "alpha task"},
	})
	if summary != "override summary" {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if got := provider.requestCount(); got != 1 {
		t.Fatalf("expected one summary call, got %d", got)
	}
	if provider.request(0).Model != "override" {
		t.Fatalf("compaction must use the conversation override, got %q", provider.request(0).Model)
	}
}

func TestSummarizeWithModelSmallMaterialUsesSingleCall(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.Event{
		{{Type: "text", Text: "compact summary"}, {Type: "done", Done: true}},
	}}
	runner := newAgentTestRunner(nil, provider, config.AgentConfig{SummaryModel: "fake:test"})
	candidates := []db.Message{
		{Role: "user", ContentText: "alpha task"},
		{Role: "assistant", ContentText: "beta decision"},
		{Role: "user", ContentText: "gamma outcome"},
	}

	summary, err := runner.summarizeWithModel(context.Background(), "prior summary", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "compact summary" {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if got := provider.requestCount(); got != 1 {
		t.Fatalf("small material must stay a single call, got %d", got)
	}
	prompt := provider.request(0).Messages[0].Content
	for _, want := range []string{summaryPromptInstructions, "Existing summary:\nprior summary", "alpha task", "beta decision", "gamma outcome"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("single-segment prompt is missing %q: %q", want, prompt)
		}
	}
}

func TestSummarizeOldestMessagesFallsBackWhenASegmentFails(t *testing.T) {
	provider := &scriptedProvider{
		contextLimits: map[string]int{"small": 1000},
		turns: [][]providers.Event{
			{{Type: "text", Text: "segment one digest"}, {Type: "done", Done: true}},
			{{Type: "error", Text: "segment exploded"}},
		},
	}
	runner := newAgentTestRunner(nil, provider, config.AgentConfig{SummaryModel: "fake:small"})
	candidates := make([]db.Message, 0, 5)
	for i := 1; i <= 5; i++ {
		candidates = append(candidates, summaryTestMessage(i))
	}

	summary := runner.summarizeOldestMessages(context.Background(), db.Agent{ID: "conv-2"}, candidates)

	if got := provider.requestCount(); got != 2 {
		t.Fatalf("expected the failing second segment to have been attempted, got %d calls", got)
	}
	if !strings.HasPrefix(summary, "Older conversation summary (local fallback)") {
		t.Fatalf("a failed segment must fall back to the deterministic summary, got %q", truncateRunes(summary, 200))
	}
}

func TestBoundedMemoryOwnedEntriesGetHigherCapThanGlobal(t *testing.T) {
	exact := strings.Repeat("留", memoryOwnedContentMaxRunes)
	memories := []db.Memory{
		{ID: "owned-exact", AgentID: "conv-1", Content: exact},
		{ID: "owned-over", AgentID: "conv-1", Content: strings.Repeat("截", memoryOwnedContentMaxRunes+1)},
		{ID: "global-over", Content: strings.Repeat("全", memoryContentMaxRunes+1)},
	}
	rendered, ledgerIDs := boundedMemorySystemContext(memories)
	if !strings.Contains(rendered, exact) {
		t.Fatal("an owned memory of exactly the owned cap must be injected whole")
	}
	if !strings.Contains(rendered, strings.Repeat("截", memoryOwnedContentMaxRunes-1)+"…") || strings.Contains(rendered, strings.Repeat("截", memoryOwnedContentMaxRunes)) {
		t.Fatalf("an owned memory one rune over the cap must be truncated to %d runes", memoryOwnedContentMaxRunes)
	}
	if !strings.Contains(rendered, strings.Repeat("全", memoryContentMaxRunes-1)+"…") || strings.Contains(rendered, strings.Repeat("全", memoryContentMaxRunes)) {
		t.Fatalf("a global memory must keep the %d-rune cap", memoryContentMaxRunes)
	}
	if len(ledgerIDs) != 1 || ledgerIDs[0] != "global-over" {
		t.Fatalf("only the global memory belongs in the ledger, got %v", ledgerIDs)
	}
	if got := renderedMemoryCount(memories); got != 3 {
		t.Fatalf("rendered count must match the injected entries, got %d", got)
	}
}

func TestBoundedMemoryOwnedTotalBudgetDegradesLaterEntries(t *testing.T) {
	memories := []db.Memory{
		{ID: "owned-1", AgentID: "conv-1", Content: strings.Repeat("一", memoryOwnedContentMaxRunes)},
		{ID: "owned-2", AgentID: "conv-1", Content: strings.Repeat("二", 5000)},
		{ID: "global-mid", Content: "global marker entry"},
		{ID: "owned-3", AgentID: "conv-1", Content: strings.Repeat("三", memoryOwnedContentMaxRunes)},
		{ID: "owned-4", AgentID: "conv-1", Content: strings.Repeat("四", memoryOwnedContentMaxRunes)},
	}
	rendered, ledgerIDs := boundedMemorySystemContext(memories)
	if !strings.Contains(rendered, strings.Repeat("一", memoryOwnedContentMaxRunes)) || !strings.Contains(rendered, strings.Repeat("二", 5000)) {
		t.Fatal("owned entries inside the total budget must be injected whole")
	}
	if !strings.Contains(rendered, "global marker entry") {
		t.Fatal("a global entry must not be affected by the owned budget")
	}
	// 8000 + 5000 leaves 3000 runes of owned budget, so the third owned entry
	// is cut to the remainder.
	if !strings.Contains(rendered, strings.Repeat("三", 2999)+"…") || strings.Contains(rendered, strings.Repeat("三", 3000)) {
		t.Fatal("the owned entry crossing the total budget must be cut to the remaining allowance")
	}
	// The budget is now exhausted, so later owned entries fall back to the
	// global cap instead of vanishing.
	if !strings.Contains(rendered, strings.Repeat("四", memoryContentMaxRunes-1)+"…") || strings.Contains(rendered, strings.Repeat("四", memoryContentMaxRunes)) {
		t.Fatalf("owned entries past the exhausted budget must fall back to the %d-rune cap", memoryContentMaxRunes)
	}
	if len(ledgerIDs) != 1 || ledgerIDs[0] != "global-mid" {
		t.Fatalf("owned entries must stay out of the ledger, got %v", ledgerIDs)
	}
	if got := renderedMemoryCount(memories); got != 5 {
		t.Fatalf("rendered count must match the injected entries, got %d", got)
	}
	if got := strings.Count(rendered, "----- MEMORY ENTRY -----"); got != 4 {
		t.Fatalf("expected 5 entries separated by 4 markers, got %d markers", got)
	}
}
