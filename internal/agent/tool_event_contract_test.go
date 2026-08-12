package agent

// Contract tests for the tool lifecycle event payloads.
//
// Two layers of protection against producer/consumer drift:
//
//  1. TestToolEventDataMatchesToolEventMetaJSONTags pins ToEventData() to the
//     json tags on ToolEventMeta, so the struct tags are the single source of
//     truth for key names. Renaming a tag or adding a field without updating
//     ToEventData fails here, at compile-adjacent test time.
//
//  2. TestAgentEventFixturesUpToDate writes golden fixtures consumed by the
//     frontend contract test (internal/server/static/modules/
//     agent-events-contract.test.mjs). The fixtures pin the exact wire JSON
//     plus the values the frontend must recover from it, so a rename that
//     slips past layer 1 still fails the frontend test instead of silently
//     rendering empty cards.
//
// Regenerate fixtures after an intentional payload change:
//
//	AUTOTO_UPDATE_EVENT_FIXTURES=1 go test ./internal/agent -run TestAgentEventFixturesUpToDate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/tools"
)

func fullyPopulatedToolEventBuilder() ToolEventMetaBuilder {
	call := tools.Call{ID: "contract-bash-1", Name: "Bash", Input: json.RawMessage(`{"command":"git status"}`)}
	return NewToolEventMetaBuilder(call, tools.RiskExec, "device-contract", "run-contract").
		Decision(toolPermissionDeny, decisionSourceDangerReflection, "rule-contract-1", "session").
		DecisionReason("reflection flagged this command").
		Finished(tools.Result{Output: strings.Repeat("x", maxToolResultPreviewBytes+16)}, "denied", 1200).
		Approval("contract warning", "contract reason", time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC), 3, 7).
		Extra(map[string]any{"executionMode": "foreground"})
}

// TestToolEventDataMatchesToolEventMetaJSONTags proves that marshalling the
// map from ToEventData() and marshalling ToolEventMeta directly produce the
// same JSON when every field is populated. The hand-written key strings in
// ToEventData therefore cannot drift from the struct tags without failing
// this test.
func TestToolEventDataMatchesToolEventMetaJSONTags(t *testing.T) {
	builder := fullyPopulatedToolEventBuilder()

	fromMap, err := json.Marshal(builder.ToEventData())
	if err != nil {
		t.Fatal(err)
	}
	fromStruct, err := json.Marshal(builder.meta)
	if err != nil {
		t.Fatal(err)
	}
	var mapView, structView map[string]any
	if err := json.Unmarshal(fromMap, &mapView); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fromStruct, &structView); err != nil {
		t.Fatal(err)
	}

	// Every json tag on ToolEventMeta must be exercised by the fully populated
	// builder above. A new field that stays empty here would silently escape
	// the map-vs-struct comparison, so require full coverage first.
	metaType := reflect.TypeOf(ToolEventMeta{})
	for index := 0; index < metaType.NumField(); index++ {
		tag := strings.Split(metaType.Field(index).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("ToolEventMeta.%s has no json tag; every field must name its wire key", metaType.Field(index).Name)
		}
		if _, ok := structView[tag]; !ok {
			t.Fatalf("ToolEventMeta.%s (%q) is empty in fullyPopulatedToolEventBuilder; populate it so the contract comparison covers it", metaType.Field(index).Name, tag)
		}
	}

	if !reflect.DeepEqual(mapView, structView) {
		t.Fatalf("ToEventData diverged from ToolEventMeta json tags:\nmap:    %s\nstruct: %s", fromMap, fromStruct)
	}
}

// TestToolEventDataKeepsIntentionalPresenceRules documents the two places
// where ToEventData deliberately differs from plain omitempty marshalling.
func TestToolEventDataKeepsIntentionalPresenceRules(t *testing.T) {
	call := tools.Call{ID: "contract-read-1", Name: "Read", Input: json.RawMessage(`{"file_path":"main.go"}`)}

	// runId is always emitted, even when empty, so consumers can key on it
	// without an existence check.
	minimal := NewToolEventMetaBuilder(call, tools.RiskRead, "", "").ToEventData()
	if _, ok := minimal["runId"]; !ok {
		t.Fatalf("runId must always be present: %+v", minimal)
	}

	// resultPreview accompanies any status, even when the preview is empty,
	// so a finished event always carries the pair.
	finished := NewToolEventMetaBuilder(call, tools.RiskRead, "", "run-1").Finished(tools.Result{}, "success", 5).ToEventData()
	if preview, ok := finished["resultPreview"]; !ok || preview != "" {
		t.Fatalf("resultPreview must accompany status even when empty: %+v", finished)
	}
}

const agentEventFixturesPath = "../server/static/spec/agent-events.json"

type agentEventFixture struct {
	Name      string         `json:"name"`
	EventType string         `json:"eventType"`
	Data      map[string]any `json:"data"`
	// Expect maps normalizeToolActivity() output fields (frontend names and
	// frontend-normalized values) to what the fixture data must produce.
	Expect map[string]any `json:"expect"`
}

type agentEventFixtureFile struct {
	Comment          string              `json:"$comment"`
	ToolEventVersion int                 `json:"toolEventVersion"`
	Fixtures         []agentEventFixture `json:"fixtures"`
}

// pinnedCommandFacts replaces the analyzer output in Bash fixtures. The real
// analyzer branches on the host OS (POSIX AST vs cmd.exe scanner), so its
// output is not stable across machines; the contract being pinned is the JSON
// shape of tools.CommandFacts, not the analyzer verdict for one command.
func pinnedCommandFacts() tools.CommandFacts {
	return tools.CommandFacts{
		ParseKnown:   true,
		Program:      "rm",
		CommandCount: 1,
		Effects:      []string{"filesystem-delete"},
		Dangerous:    []string{"file-delete"},
	}
}

func pinnedCommandFactsExpect() map[string]any {
	return map[string]any{
		"parseKnown":   true,
		"program":      "rm",
		"subcommand":   "",
		"commandCount": 1,
		"compound":     false,
		"pipeline":     false,
		"redirection":  false,
		"substitution": false,
		"background":   false,
		"effects":      []string{"filesystem-delete"},
		"dangerous":    []string{"file-delete"},
		"sensitive":    []string{},
	}
}

func buildAgentEventFixtures(t *testing.T) agentEventFixtureFile {
	t.Helper()
	const runID = "run-fixture-1"
	agentRow := db.Agent{ID: "agent-fixture-1", CWD: "C:/repo", ExecutionDeviceID: "local"}

	readCall := tools.Call{ID: "fixture-read-1", Name: "Read", Input: json.RawMessage(`{"file_path":"C:/repo/main.go","limit":120}`)}
	started := toolStartedEventDataWithResolution(readCall, tools.RiskRead, "local", runID,
		toolPermissionResolution{Decision: toolPermissionAllow, Source: decisionSourceRule, RuleID: "rule-read-source", Scope: "session"})

	writeCall := tools.Call{ID: "fixture-write-2", Name: "Write", Input: json.RawMessage(`{"file_path":"C:/repo/notes.md","content":"hello fixtures"}`)}
	finishedOK := toolFinishedEventDataWithResolution(writeCall, tools.RiskWrite, "local", runID,
		tools.Result{Output: "wrote C:/repo/notes.md"}, "success", 1234, map[string]any{"executionMode": "foreground"},
		toolPermissionResolution{Decision: toolPermissionAllow, Source: decisionSourceDefaultPolicy})

	bashCall := tools.Call{ID: "fixture-bash-3", Name: "Bash", Input: json.RawMessage(`{"command":"rm -rf /tmp/cache"}`)}
	finishedDenied := toolFinishedEventDataWithResolution(bashCall, tools.RiskExec, "local", runID,
		tools.Result{Output: "blocked before execution", IsError: true}, "denied", 0, nil,
		toolPermissionResolution{Decision: toolPermissionDeny, Reason: "reflection flagged this command as destructive", Source: decisionSourceDangerReflection, Scope: "tool_call"})
	finishedDenied["commandFacts"] = pinnedCommandFacts()

	hardBlockWarning := "This deletes files recursively and cannot be undone."
	hardBlock := mergeEventData(approvalEventDataWithResolution(agentRow, bashCall, tools.RiskDanger, hardBlockWarning, "danger", time.Time{}, 0, 0,
		toolPermissionResolution{Decision: toolPermissionDeny, Reason: hardBlockWarning, Warning: hardBlockWarning, Source: decisionSourceHardDangerBlock}), runID)
	hardBlock["commandFacts"] = pinnedCommandFacts()

	approvalExpires := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	reflectionApproval := mergeEventData(approvalEventDataWithResolution(agentRow, writeCall, tools.RiskWrite, "reflection wants human confirmation", "write outside workspace", approvalExpires, 3, 7,
		toolPermissionResolution{Decision: toolPermissionAsk, Source: decisionSourceDangerReflection}), runID)

	return agentEventFixtureFile{
		Comment: "Generated by TestAgentEventFixturesUpToDate (internal/agent/tool_event_contract_test.go). " +
			"Do not edit by hand; regenerate with: AUTOTO_UPDATE_EVENT_FIXTURES=1 go test ./internal/agent -run TestAgentEventFixturesUpToDate",
		ToolEventVersion: toolEventVersion,
		Fixtures: []agentEventFixture{
			{
				Name:      "tool.started allowed by rule",
				EventType: "tool.started",
				Data:      started,
				Expect: map[string]any{
					"toolUseId":         "fixture-read-1",
					"toolName":          "Read",
					"risk":              string(tools.RiskRead),
					"runId":             runID,
					"eventVersion":      toolEventVersion,
					"executionDeviceId": "local",
					// No status on started events; the frontend defaults to running.
					"status":         "running",
					"decision":       toolPermissionAllow,
					"decisionSource": decisionSourceRule,
					"ruleId":         "rule-read-source",
					"decisionScope":  "session",
					"truncated":      false,
					"inputJson":      map[string]any{"file_path": "C:/repo/main.go", "limit": 120},
					"resultPreview":  "",
				},
			},
			{
				Name:      "tool.finished success",
				EventType: "tool.finished",
				Data:      finishedOK,
				Expect: map[string]any{
					"toolUseId":    "fixture-write-2",
					"toolName":     "Write",
					"risk":         string(tools.RiskWrite),
					"runId":        runID,
					"eventVersion": toolEventVersion,
					// "success" normalizes to the frontend's canonical "completed".
					"status":         "completed",
					"durationMs":     1234,
					"decision":       toolPermissionAllow,
					"decisionSource": decisionSourceDefaultPolicy,
					"resultPreview":  "wrote C:/repo/notes.md",
					// content is projected away and replaced by a byte count,
					// which marks the input as truncated.
					"truncated": true,
					"inputJson": map[string]any{"file_path": "C:/repo/notes.md", "contentBytes": len("hello fixtures")},
				},
			},
			{
				Name:      "tool.finished denied by danger reflection",
				EventType: "tool.finished",
				Data:      finishedDenied,
				Expect: map[string]any{
					"toolUseId":                "fixture-bash-3",
					"toolName":                 "Bash",
					"risk":                     string(tools.RiskExec),
					"runId":                    runID,
					"status":                   "denied",
					"decision":                 toolPermissionDeny,
					"decisionSource":           decisionSourceDangerReflection,
					"decisionScope":            "tool_call",
					"permissionDecisionReason": "reflection flagged this command as destructive",
					"resultPreview":            "blocked before execution",
					// Bash events never carry the raw command, only a marker.
					"inputJson":    map[string]any{"commandPresent": true},
					"truncated":    true,
					"commandFacts": pinnedCommandFactsExpect(),
				},
			},
			{
				Name:      "tool.approval_required hard danger block",
				EventType: "tool.approval_required",
				Data:      hardBlock,
				Expect: map[string]any{
					"toolUseId":                "fixture-bash-3",
					"toolName":                 "Bash",
					"risk":                     string(tools.RiskDanger),
					"runId":                    runID,
					"decision":                 toolPermissionDeny,
					"decisionSource":           decisionSourceHardDangerBlock,
					"permissionDecisionReason": "danger",
					"inputJson":                map[string]any{"commandPresent": true},
					"commandFacts":             pinnedCommandFactsExpect(),
				},
			},
			{
				Name:      "tool.approval_required danger reflection confirmation",
				EventType: "tool.approval_required",
				Data:      reflectionApproval,
				Expect: map[string]any{
					"toolUseId":                "fixture-write-2",
					"toolName":                 "Write",
					"risk":                     string(tools.RiskWrite),
					"runId":                    runID,
					"decision":                 toolPermissionAsk,
					"decisionSource":           decisionSourceDangerReflection,
					"permissionDecisionReason": "write outside workspace",
					"status":                   "running",
				},
			},
		},
	}
}

func TestAgentEventFixturesUpToDate(t *testing.T) {
	fixtures := buildAgentEventFixtures(t)
	generated, err := json.MarshalIndent(fixtures, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	generated = append(generated, '\n')

	if os.Getenv("AUTOTO_UPDATE_EVENT_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(agentEventFixturesPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(agentEventFixturesPath, generated, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", agentEventFixturesPath, len(generated))
		return
	}

	stored, err := os.ReadFile(agentEventFixturesPath)
	if err != nil {
		t.Fatalf("read fixtures: %v\nregenerate with: AUTOTO_UPDATE_EVENT_FIXTURES=1 go test ./internal/agent -run TestAgentEventFixturesUpToDate", err)
	}
	if normalizeFixtureText(stored) != normalizeFixtureText(generated) {
		t.Fatalf("agent event fixtures are stale; the event payload changed.\n"+
			"If the change is intentional, regenerate with: AUTOTO_UPDATE_EVENT_FIXTURES=1 go test ./internal/agent -run TestAgentEventFixturesUpToDate\n"+
			"and re-run the frontend contract test (agent-events-contract.test.mjs) to confirm consumers still understand the payload.\n%s",
			fixtureDiffHint(normalizeFixtureText(stored), normalizeFixtureText(generated)))
	}
}

func normalizeFixtureText(data []byte) string {
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func fixtureDiffHint(stored, generated string) string {
	storedLines := strings.Split(stored, "\n")
	generatedLines := strings.Split(generated, "\n")
	for index := 0; index < len(storedLines) || index < len(generatedLines); index++ {
		storedLine, generatedLine := "", ""
		if index < len(storedLines) {
			storedLine = storedLines[index]
		}
		if index < len(generatedLines) {
			generatedLine = generatedLines[index]
		}
		if storedLine != generatedLine {
			return fmt.Sprintf("first difference at line %d:\nstored:    %s\ngenerated: %s", index+1, storedLine, generatedLine)
		}
	}
	return "files differ only in length"
}
