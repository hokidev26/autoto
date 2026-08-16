package background

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseChildPublicReportExtractsAllowlistedJSON(t *testing.T) {
	report := parseChildPublicReport("done.\n```json\n{\"summary\":\"edited runner\",\"files\":[\"internal/agent/runner.go\"],\"result\":\"ok\",\"acceptanceCriteria\":[\"PRIVATE_ACCEPTANCE_SENTINEL\"]}\n```\n")
	if report.Summary != "edited runner" || report.Result != "ok" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.Files) != 1 || report.Files[0] != "internal/agent/runner.go" {
		t.Fatalf("unexpected files: %+v", report.Files)
	}
}

func TestParseChildPublicReportFallsBackToTruncatedProse(t *testing.T) {
	prose := strings.Repeat("child finished without structured output ", 200)
	report := parseChildPublicReport(prose)
	if report.Result != "" || len(report.Files) != 0 {
		t.Fatalf("fallback must not invent structured fields: %+v", report)
	}
	if report.Summary == "" || len(report.Summary) > maxAgentSummaryBytes {
		t.Fatalf("fallback summary was not bounded: len=%d", len(report.Summary))
	}
	if !strings.HasPrefix(prose, report.Summary) && !strings.HasPrefix(prose, strings.TrimSpace(report.Summary)) {
		t.Fatalf("fallback summary did not come from the last assistant prose: %q", report.Summary[:min(80, len(report.Summary))])
	}
}

func TestAttachChildPublicReportDoesNotLeakAcceptanceCriteria(t *testing.T) {
	const secret = "PRIVATE_ACCEPTANCE_SENTINEL"
	base, err := marshalAgentPublicResult("reviewer", 1, "child-agent", "child-run", "completed")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := attachChildPublicReport(base, parseChildPublicReport(`{"summary":"checked the diff","files":["a.go"],"result":"ok","acceptanceCriteria":["`+secret+`"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "acceptanceCriteria") {
		t.Fatalf("public result leaked acceptance criteria: %s", encoded)
	}
	var projected agentPublicResult
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Summary != "checked the diff" || projected.Result != "ok" || projected.AcceptanceCount != 1 {
		t.Fatalf("unexpected attached result: %+v", projected)
	}
}

func TestAttachChildPublicReportStaysInsideBudget(t *testing.T) {
	base, err := marshalAgentPublicResultWithDetails(
		"general", "general", "", "", "", 0, "child-agent", "child-run", "completed",
		strings.Repeat("x", 800),
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := attachChildPublicReport(base, agentResultReport{
		Summary: strings.Repeat("s", 8000),
		Files:   []string{"a.go", "b.go"},
		Result:  "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxAgentResultBytes {
		t.Fatalf("result is %d bytes, over the %d budget", len(encoded), maxAgentResultBytes)
	}
	var projected agentPublicResult
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Result != "ok" {
		t.Fatalf("structured result was dropped: %+v", projected)
	}
}
