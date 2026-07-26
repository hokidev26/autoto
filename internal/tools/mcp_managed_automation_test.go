package tools

import (
	"encoding/json"
	"testing"
)

func TestManagedAutomationMCPRiskClassification(t *testing.T) {
	playwrightID := ManagedAutomationMCPServerID("playwright-mcp")
	chromeID := ManagedAutomationMCPServerID("chrome-devtools-mcp")
	if !IsManagedAutomationMCPServerID(playwrightID) || !IsManagedAutomationMCPServerID(chromeID) || IsManagedAutomationMCPServerID("user-mcp") {
		t.Fatal("managed automation MCP id classification is incorrect")
	}

	listTool := MCPListToolsTool{}
	if risk := listTool.Risk(json.RawMessage(`{"serverId":"` + playwrightID + `"}`)); risk != RiskRead {
		t.Fatalf("managed MCP discovery should be read risk, got %s", risk)
	}
	if risk := listTool.Risk(json.RawMessage(`{"serverId":"user-mcp"}`)); risk != RiskExec {
		t.Fatalf("ordinary MCP discovery compatibility should stay exec risk, got %s", risk)
	}
	if risk := listTool.Risk(json.RawMessage(`{"serverId":`)); risk != RiskExec {
		t.Fatalf("malformed discovery input must fail closed, got %s", risk)
	}

	callTool := MCPCallToolTool{}
	for _, testCase := range []struct {
		name     string
		input    string
		wantRisk Risk
		wantOnce bool
	}{
		{name: "playwright snapshot", input: `{"serverId":"` + playwrightID + `","toolName":"browser_snapshot"}`, wantRisk: RiskRead},
		{name: "playwright screenshot", input: `{"serverId":"` + playwrightID + `","toolName":"browser_take_screenshot"}`, wantRisk: RiskRead},
		{name: "playwright screenshot file", input: `{"serverId":"` + playwrightID + `","toolName":"browser_take_screenshot","arguments":{"filename":"capture.png"}}`, wantRisk: RiskExec, wantOnce: true},
		{name: "chrome screenshot file", input: `{"serverId":"` + chromeID + `","toolName":"take_screenshot","arguments":{"filePath":"capture.png"}}`, wantRisk: RiskExec, wantOnce: true},
		{name: "chrome pages", input: `{"serverId":"` + chromeID + `","toolName":"list_pages"}`, wantRisk: RiskRead},
		{name: "chrome performance", input: `{"serverId":"` + chromeID + `","toolName":"performance_analyze_insight"}`, wantRisk: RiskRead},
		{name: "playwright click", input: `{"serverId":"` + playwrightID + `","toolName":"browser_click"}`, wantRisk: RiskExec, wantOnce: true},
		{name: "playwright navigate", input: `{"serverId":"` + playwrightID + `","toolName":"browser_navigate"}`, wantRisk: RiskExec, wantOnce: true},
		{name: "chrome evaluate", input: `{"serverId":"` + chromeID + `","toolName":"evaluate_script"}`, wantRisk: RiskExec, wantOnce: true},
		{name: "managed unknown", input: `{"serverId":"` + chromeID + `","toolName":"future_unknown_tool"}`, wantRisk: RiskExec, wantOnce: true},
		{name: "ordinary same tool name", input: `{"serverId":"user-mcp","toolName":"take_snapshot"}`, wantRisk: RiskExec},
		{name: "malformed", input: `{"serverId":`, wantRisk: RiskExec},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := json.RawMessage(testCase.input)
			if risk := callTool.Risk(input); risk != testCase.wantRisk {
				t.Fatalf("risk=%s want=%s", risk, testCase.wantRisk)
			}
			if requiresApproval := ManagedAutomationMCPCallRequiresApproval(input); requiresApproval != testCase.wantOnce {
				t.Fatalf("one-time approval=%v want=%v", requiresApproval, testCase.wantOnce)
			}
			if allowed := callTool.SessionApprovalAllowedForInput(input); allowed == testCase.wantOnce {
				t.Fatalf("session approval allowed=%v for wantOnce=%v", allowed, testCase.wantOnce)
			}
		})
	}
}

func TestManagedAutomationMCPServerIDDoesNotAcceptArbitraryCatalogIDs(t *testing.T) {
	for _, id := range []string{"", "unknown", "../playwright-mcp", ManagedAutomationMCPServerID("playwright-mcp") + "-suffix"} {
		if IsManagedAutomationMCPServerID(id) {
			t.Fatalf("unexpected managed server id acceptance: %q", id)
		}
	}
}
