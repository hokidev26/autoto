package background

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"strings"

	"autoto/internal/db"
)

const (
	maxAgentSummaryBytes    = 1536
	maxAgentResultFiles     = 16
	maxAgentResultFileBytes = 256
)

type agentResultReport struct {
	Summary string
	Files   []string
	Result  string
}

func loadChildPublicReport(ctx context.Context, store *db.Store, childID string) agentResultReport {
	if store == nil || strings.TrimSpace(childID) == "" {
		return agentResultReport{}
	}
	messages, err := store.ListMessages(ctx, childID)
	if err != nil {
		return agentResultReport{}
	}
	return parseChildPublicReport(lastAssistantText(messages))
}

func lastAssistantText(messages []db.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i].ContentText
		}
	}
	return ""
}

func parseChildPublicReport(text string) agentResultReport {
	text = strings.TrimSpace(text)
	if text == "" {
		return agentResultReport{}
	}
	if report, ok := decodeChildPublicReport(text); ok {
		return boundChildPublicReport(report)
	}
	if unfenced, ok := unwrapJSONFence(text); ok {
		if report, ok := decodeChildPublicReport(unfenced); ok {
			return boundChildPublicReport(report)
		}
	}
	candidates := jsonObjectCandidates(text)
	for i := len(candidates) - 1; i >= 0; i-- {
		if report, ok := decodeChildPublicReport(candidates[i]); ok {
			return boundChildPublicReport(report)
		}
	}
	return boundChildPublicReport(agentResultReport{Summary: text})
}

func unwrapJSONFence(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
	if after, ok := strings.CutPrefix(strings.ToLower(rest), "json"); ok {
		rest = strings.TrimSpace(after)
	}
	end := strings.LastIndex(rest, "```")
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:end]), true
}

func jsonObjectCandidates(text string) []string {
	candidates := make([]string, 0, 4)
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil || len(value) == 0 || value[0] != '{' {
			continue
		}
		candidates = append(candidates, string(value))
	}
	return candidates
}

func decodeChildPublicReport(text string) (agentResultReport, bool) {
	text = strings.TrimSpace(text)
	if text == "" || !json.Valid([]byte(text)) {
		return agentResultReport{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return agentResultReport{}, false
	}
	report := agentResultReport{}
	if value, ok := raw["summary"]; ok {
		var summary string
		if json.Unmarshal(value, &summary) != nil {
			return agentResultReport{}, false
		}
		report.Summary = strings.TrimSpace(summary)
	}
	if value, ok := raw["files"]; ok {
		var files []string
		if json.Unmarshal(value, &files) != nil {
			return agentResultReport{}, false
		}
		report.Files = sanitizeChildReportFiles(files)
	}
	if value, ok := raw["result"]; ok {
		var result string
		if json.Unmarshal(value, &result) != nil {
			return agentResultReport{}, false
		}
		result = strings.ToLower(strings.TrimSpace(result))
		switch result {
		case "ok", "blocked", "incomplete":
			report.Result = result
		default:
			return agentResultReport{}, false
		}
	}
	if report.Summary == "" && report.Result == "" && len(report.Files) == 0 {
		return agentResultReport{}, false
	}
	return report, true
}

func sanitizeChildReportFiles(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(files), maxAgentResultFiles))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
		if file == "" || strings.ContainsRune(file, 0) {
			continue
		}
		clean := path.Clean(file)
		native := filepath.FromSlash(clean)
		if path.IsAbs(clean) || filepath.IsAbs(native) || filepath.VolumeName(native) != "" || strings.HasPrefix(clean, "../") || clean == ".." || clean == "." {
			continue
		}
		file = clean
		if _, exists := seen[file]; exists {
			continue
		}
		seen[file] = struct{}{}
		out = append(out, truncateUTF8(file, maxAgentResultFileBytes))
		if len(out) >= maxAgentResultFiles {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boundChildPublicReport(report agentResultReport) agentResultReport {
	report.Summary = truncateUTF8(report.Summary, maxAgentSummaryBytes)
	if len(report.Files) > maxAgentResultFiles {
		report.Files = report.Files[:maxAgentResultFiles]
	}
	return report
}

func attachChildPublicReport(raw json.RawMessage, report agentResultReport) (json.RawMessage, error) {
	var payload agentPublicResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("background agent result exceeds size limit")
	}
	report = boundChildPublicReport(report)
	payload.Summary = report.Summary
	payload.Files = report.Files
	payload.Result = report.Result
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("background agent result exceeds size limit")
	}
	if len(encoded) <= maxAgentResultBytes {
		return encoded, nil
	}
	payload.Files = nil
	encoded, err = json.Marshal(payload)
	if err != nil {
		return nil, errors.New("background agent result exceeds size limit")
	}
	if len(encoded) <= maxAgentResultBytes {
		return encoded, nil
	}
	payload.ChildError = ""
	encoded, err = json.Marshal(payload)
	if err != nil {
		return nil, errors.New("background agent result exceeds size limit")
	}
	if len(encoded) <= maxAgentResultBytes {
		return encoded, nil
	}
	payload.Summary = truncateUTF8(payload.Summary, 256)
	encoded, err = json.Marshal(payload)
	if err != nil || len(encoded) > maxAgentResultBytes {
		payload.Summary = ""
		payload.Result = ""
		encoded, err = json.Marshal(payload)
		if err != nil || len(encoded) > maxAgentResultBytes {
			return nil, errors.New("background agent result exceeds size limit")
		}
	}
	return encoded, nil
}
