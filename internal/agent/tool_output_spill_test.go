package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"autoto/internal/config"
	"autoto/internal/spill"
	"autoto/internal/tools"
)

const spillTestLimit = 2048

// newSpillTestRunner answers with the agent id too: it doubles as the spill
// owner, so every assertion about where a file landed keys off it.
func newSpillTestRunner(t *testing.T, limit int) (*Runner, *spill.Store, string) {
	t.Helper()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	t.Cleanup(func() { store.Close() })
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{ToolOutputSpillBytes: limit})
	spills, err := spill.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner.SetToolOutputSpillStore(spills)
	return runner, spills, agent.ID
}

func bashCallWithCommand(command string) tools.Call {
	return tools.Call{ID: "spill-call", Name: "Bash", Input: mustJSON(map[string]string{"command": command})}
}

func TestOversizedToolOutputIsSpilledToDiskWithABoundedPreview(t *testing.T) {
	runner, spills, agentID := newSpillTestRunner(t, spillTestLimit)
	output := "HEAD_MARKER\n" + strings.Repeat("filler line\n", 1000) + "TAIL_MARKER"

	result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output})

	if len(result.Output) > spillTestLimit {
		t.Fatalf("spilled result is %d bytes, above the %d byte cap", len(result.Output), spillTestLimit)
	}
	if !strings.Contains(result.Output, "HEAD_MARKER") || !strings.Contains(result.Output, "TAIL_MARKER") {
		t.Fatalf("preview dropped the head or the tail: %q", result.Output)
	}
	if strings.Contains(result.Output, strings.Repeat("filler line\n", 200)) {
		t.Fatal("preview carried the bulk of the output inline")
	}
	if !strings.Contains(result.Output, "Read") || !strings.Contains(result.Output, "Grep") {
		t.Fatalf("notice does not point at the retrieval tools: %q", result.Output)
	}
	path, ok := result.Meta["spillPath"].(string)
	if !ok || !strings.Contains(result.Output, path) {
		t.Fatalf("notice does not carry the spill path: meta=%+v output=%q", result.Meta, result.Output)
	}
	if !strings.HasPrefix(path, spills.Root()) {
		t.Fatalf("spill landed outside the store root: %s", path)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != output {
		t.Fatal("spill file does not hold the complete tool output")
	}
	if directory := filepath.Base(filepath.Dir(path)); directory != agentID {
		t.Fatalf("spill directory %q is not the conversation id %q", directory, agentID)
	}
}

func TestToolOutputAtOrBelowTheCapIsLeftInline(t *testing.T) {
	runner, spills, agentID := newSpillTestRunner(t, spillTestLimit)
	output := strings.Repeat("a", spillTestLimit)

	result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output})

	if result.Output != output {
		t.Fatalf("result at the cap was rewritten: %d bytes", len(result.Output))
	}
	if entries, err := os.ReadDir(spills.Root()); err != nil || len(entries) != 0 {
		t.Fatalf("a result at the cap was written to disk: entries=%d err=%v", len(entries), err)
	}
}

func TestSpillIsDisabledByAZeroCap(t *testing.T) {
	runner, _, agentID := newSpillTestRunner(t, 0)
	output := strings.Repeat("a", 100000)

	if result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output}); result.Output != output {
		t.Fatalf("spill ran with the cap disabled: %d bytes", len(result.Output))
	}
}

func TestRetrievalToolResultsAreNeverSpilled(t *testing.T) {
	runner, spills, agentID := newSpillTestRunner(t, spillTestLimit)
	output := strings.Repeat("read output\n", 1000)

	for _, toolName := range []string{"Read", "Grep", "StartPipeline", "EndPipeline"} {
		call := tools.Call{ID: "exempt", Name: toolName, Input: json.RawMessage(`{}`)}
		if result := runner.processToolResultForModel(agentID, "run-spill", call, tools.Result{Output: output}); result.Output != output {
			t.Fatalf("%s result was spilled, which sends the model back to the tool it just used", toolName)
		}
	}
	if entries, err := os.ReadDir(spills.Root()); err != nil || len(entries) != 0 {
		t.Fatalf("an exempt tool wrote a spill file: entries=%d err=%v", len(entries), err)
	}
}

func TestSpillFailureKeepsTheInlineResult(t *testing.T) {
	runner, spills, agentID := newSpillTestRunner(t, spillTestLimit)
	// Occupying the conversation's directory path with a file makes every save
	// for this conversation fail the way a permission or disk fault would.
	if err := os.WriteFile(filepath.Join(spills.Root(), agentID), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := strings.Repeat("important output\n", 1000)

	result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output})

	if result.Output != output {
		t.Fatal("a spill failure hid the tool output instead of keeping it inline")
	}
	if result.IsError {
		t.Fatal("a spill failure turned a successful tool result into an error")
	}
}

func TestSpillWithoutAStoreKeepsTheInlineResult(t *testing.T) {
	runner, _, agentID := newSpillTestRunner(t, spillTestLimit)
	runner.SetToolOutputSpillStore(nil)
	output := strings.Repeat("important output\n", 1000)

	if result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output}); result.Output != output {
		t.Fatal("a missing spill store hid the tool output")
	}
}

func TestSpilledErrorResultStaysAnError(t *testing.T) {
	runner, _, agentID := newSpillTestRunner(t, spillTestLimit)
	output := strings.Repeat("compiler error\n", 1000)

	result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("build"), tools.Result{Output: output, IsError: true})

	if !result.IsError || len(result.Output) > spillTestLimit {
		t.Fatalf("spilled error result lost its error flag or its bound: %+v", result)
	}
}

func TestSpillPreviewNeverSplitsARune(t *testing.T) {
	runner, _, agentID := newSpillTestRunner(t, spillTestLimit)
	output := strings.Repeat("\u4e16\u754c", 4000)

	result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output})

	if !utf8.ValidString(result.Output) {
		t.Fatalf("spill preview split a multi-byte rune: %q", result.Output)
	}
	if len(result.Output) > spillTestLimit {
		t.Fatalf("spilled result is %d bytes, above the %d byte cap", len(result.Output), spillTestLimit)
	}
}

func TestSpillLeavesInvalidUTF8ResultsReadable(t *testing.T) {
	runner, _, agentID := newSpillTestRunner(t, spillTestLimit)
	output := "START" + strings.Repeat("\xff\xfe", 4000) + "END"

	result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output})

	if !utf8.ValidString(result.Output) {
		t.Fatal("spill preview passed raw invalid UTF-8 to the model")
	}
	if !strings.Contains(result.Output, "START") || !strings.Contains(result.Output, "END") {
		t.Fatalf("preview of a binary-ish result kept neither end: %q", result.Output)
	}
}

func TestAnActivePipelineCapturesInsteadOfSpilling(t *testing.T) {
	runner, spills, agentID := newSpillTestRunner(t, spillTestLimit)
	scope := toolOutputPipelineScope(agentID, "run-spill")
	if started := runner.toolOutputPipeline.Start(scope, tools.ToolOutputPipelineStartOptions{}); started.IsError {
		t.Fatalf("could not start the pipeline: %+v", started)
	}
	output := strings.Repeat("captured line\n", 1000)

	result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output})

	if !strings.Contains(result.Output, "Captured as p1") {
		t.Fatalf("pipeline capture did not run first: %q", result.Output)
	}
	if _, spilled := result.Meta["spillPath"]; spilled {
		t.Fatal("spill rewrote a pipeline capture preview, which points the model away from EndPipeline")
	}
	if entries, err := os.ReadDir(spills.Root()); err != nil || len(entries) != 0 {
		t.Fatalf("a captured result was also spilled: entries=%d err=%v", len(entries), err)
	}
}

func TestSpillKeepsTheInlineResultWhenTheNoticeCannotFit(t *testing.T) {
	runner, _, agentID := newSpillTestRunner(t, config.MinToolOutputSpillBytes)
	runner.cfg.ToolOutputSpillBytes = 64
	output := strings.Repeat("a", 200)

	if result := runner.processToolResultForModel(agentID, "run-spill", bashCallWithCommand("dump"), tools.Result{Output: output}); result.Output != output {
		t.Fatalf("a replacement larger than the cap was delivered: %d bytes for a %d byte cap", len(result.Output), 64)
	}
}
