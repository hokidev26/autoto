package agent

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/db"
	"autoto/internal/tools"
)

var (
	toolActivitySecretAssignmentPattern = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|auth(?:orization)?|bearer|cookie|password|passwd|secret|session[_-]?token|token)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	toolActivityBearerPattern           = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	toolActivitySensitiveQueryPattern   = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|auth|authorization|key|password|secret|session|token)=)[^&#\s]*`)
)

func runEventData(runID string) map[string]any {
	if runID == "" {
		return nil
	}
	return map[string]any{"runId": runID}
}

func activeRunID(active *activeRun) string {
	if active == nil {
		return ""
	}
	return active.runID
}

func mergeEventData(data map[string]any, runID string) map[string]any {
	if runID == "" {
		return data
	}
	if data == nil {
		data = make(map[string]any, 1)
	}
	data["runId"] = runID
	return data
}

func toolStartedEventData(call tools.Call, risk tools.Risk, executionDeviceID, runID string) map[string]any {
	return toolStartedEventDataWithResolution(call, risk, executionDeviceID, runID, toolPermissionResolution{Source: decisionSourceDefaultPolicy})
}

func toolStartedEventDataWithResolution(call tools.Call, risk tools.Risk, executionDeviceID, runID string, resolution toolPermissionResolution) map[string]any {
	return NewToolEventMetaBuilder(call, risk, executionDeviceID, runID).
		Decision(resolution.Decision, resolution.Source, resolution.RuleID, resolution.Scope).
		DecisionReason(resolution.Reason).
		ToEventData()
}

func toolFinishedEventDataWithResolution(call tools.Call, risk tools.Risk, executionDeviceID, runID string, result tools.Result, status string, durationMS int64, extra map[string]any, resolution toolPermissionResolution) map[string]any {
	return NewToolEventMetaBuilder(call, risk, executionDeviceID, runID).
		Decision(resolution.Decision, resolution.Source, resolution.RuleID, resolution.Scope).
		DecisionReason(resolution.Reason).
		Finished(result, status, durationMS).
		Extra(extra).
		ToEventData()
}

func approvalEventDataWithResolution(agent db.Agent, call tools.Call, risk tools.Risk, warning, reason string, expiresAt time.Time, permissionGeneration, policyGeneration int64, resolution toolPermissionResolution) map[string]any {
	data := NewToolEventMetaBuilder(call, risk, normalizedExecutionDeviceID(agent.ExecutionDeviceID), "").
		Decision(resolution.Decision, resolution.Source, resolution.RuleID, resolution.Scope).
		Approval(warning, reason, expiresAt, permissionGeneration, policyGeneration).
		ToEventData()
	// Keep the historical approval keys, but only with safe projections.
	data["input"] = data["inputJson"]
	data["command"] = ""
	if toolCommand(call.Name, call.Input) != "" {
		data["commandOmitted"] = true
	}
	data["cwd"] = agent.CWD
	data["warning"] = boundedToolEventMetaText(warning)
	data["reason"] = boundedToolEventMetaText(reason)
	return data
}

func toolEventInputJSON(input json.RawMessage) (json.RawMessage, bool) {
	return ProjectToolActivityInput("", input, maxToolEventInputBytes)
}

func ProjectToolActivityInput(toolName string, input json.RawMessage, maximum int) (json.RawMessage, bool) {
	if maximum <= 0 {
		maximum = maxToolEventInputBytes
	}
	var source map[string]any
	if len(input) == 0 || json.Unmarshal(input, &source) != nil || source == nil {
		return json.RawMessage(`{}`), len(input) > 0
	}
	priority := []string{
		"command", "file_path", "filePath", "path", "pattern", "glob", "pages", "offset", "limit", "output_mode",
		"replace_all", "replaceAll", "url", "ref_id", "selector", "mode", "max_length", "purpose", "query", "description",
		"subagent_type", "model", "reasoning_effort", "run_in_background", "workdir", "skill", "name", "args", "recency", "domains",
		"location", "start", "duration", "ticker", "market", "utc_offset", "fn", "league", "team", "opponent", "date_from",
		"date_to", "num_games", "locale", "type",
	}
	projected := make(map[string]any, len(priority)+4)
	included := make(map[string]struct{}, len(priority))
	truncated := false
	for _, key := range priority {
		value, ok := source[key]
		if !ok {
			continue
		}
		included[key] = struct{}{}
		if sensitiveToolActivityInputKey(key) {
			truncated = true
			continue
		}
		bounded, valueTruncated := projectToolActivityValue(key, value, min(maxToolEventInputStringBytes, maximum/2), 0)
		projected[key] = bounded
		encoded, err := json.Marshal(projected)
		if err != nil || len(encoded) > maximum {
			delete(projected, key)
			truncated = true
			continue
		}
		truncated = truncated || valueTruncated
	}
	for key, value := range source {
		if _, ok := included[key]; ok {
			continue
		}
		if lengthKey := omittedToolActivityLengthKey(key); lengthKey != "" {
			projected[lengthKey] = toolActivityValueBytes(value)
			truncated = true
			continue
		}
		truncated = true
	}
	encoded, err := json.Marshal(projected)
	if err != nil || len(encoded) > maximum {
		return json.RawMessage(`{}`), true
	}
	if !utf8.Valid(encoded) {
		return json.RawMessage(`{}`), true
	}
	return encoded, truncated
}

func projectToolActivityValue(key string, value any, stringLimit, depth int) (any, bool) {
	if depth >= 3 {
		return nil, true
	}
	switch typed := value.(type) {
	case string:
		text := RedactToolActivityText(typed)
		if strings.EqualFold(key, "url") {
			text = sanitizeToolActivityURL(text)
		}
		return boundedToolEventString(text, stringLimit)
	case nil, bool, float64, json.Number:
		return typed, false
	case []any:
		limit := min(len(typed), 16)
		result := make([]any, 0, limit)
		truncated := len(typed) > limit
		for _, item := range typed[:limit] {
			bounded, itemTruncated := projectToolActivityValue(key, item, min(stringLimit, 512), depth+1)
			result = append(result, bounded)
			truncated = truncated || itemTruncated
		}
		return result, truncated
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for nestedKey := range typed {
			keys = append(keys, nestedKey)
		}
		sort.Strings(keys)
		limit := min(len(keys), 16)
		result := make(map[string]any, limit)
		truncated := len(keys) > limit
		for _, nestedKey := range keys[:limit] {
			if sensitiveToolActivityInputKey(nestedKey) {
				truncated = true
				continue
			}
			bounded, itemTruncated := projectToolActivityValue(nestedKey, typed[nestedKey], min(stringLimit, 512), depth+1)
			result[nestedKey] = bounded
			truncated = truncated || itemTruncated
		}
		return result, truncated
	default:
		return nil, true
	}
}

func sensitiveToolActivityInputKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(strings.TrimSpace(key)))
	for _, fragment := range []string{"apikey", "authorization", "cookie", "credential", "env", "header", "password", "passwd", "secret", "token"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func omittedToolActivityLengthKey(key string) string {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(strings.TrimSpace(key)))
	switch normalized {
	case "content":
		return "contentBytes"
	case "oldstring":
		return "oldStringBytes"
	case "newstring":
		return "newStringBytes"
	case "prompt":
		return "promptBytes"
	case "body":
		return "bodyBytes"
	default:
		return ""
	}
}

func toolActivityValueBytes(value any) int {
	if text, ok := value.(string); ok {
		return len(text)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func sanitizeToolActivityURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return toolActivitySensitiveQueryPattern.ReplaceAllString(value, `${1}[redacted]`)
	}
	parsed.User = nil
	query := parsed.Query()
	for key, values := range query {
		for index := range values {
			values[index] = "[redacted]"
		}
		query[key] = values
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func RedactToolActivityText(value string) string {
	value = sanitizedToolText(value)
	value = toolActivityBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = toolActivitySecretAssignmentPattern.ReplaceAllString(value, `${1}[redacted]`)
	return toolActivitySensitiveQueryPattern.ReplaceAllString(value, `${1}[redacted]`)
}

func boundedToolEventString(value string, limit int) (string, bool) {
	value = sanitizedToolText(value)
	if len(value) <= limit {
		return value, false
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}

func normalizedExecutionDeviceID(executionDeviceID string) string {
	if executionDeviceID = strings.TrimSpace(executionDeviceID); executionDeviceID == "" {
		return "local"
	}
	return executionDeviceID
}

// sanitizedToolText makes arbitrary child-process output safe to persist. The
// trusted store accepts valid UTF-8 with no NUL, and ToValidUTF8 alone does not
// satisfy that: U+0000 is a legal code point, so it survives repair untouched.
//
// A UTF-16 writer is the common source. Every ASCII character it emits carries a
// trailing zero byte, so a single such command used to fill the preview with NUL,
// the store rejected the row, and the whole run died on the tool ledger write
// instead of merely losing a readable preview.
//
// NUL is dropped rather than replaced. Substituting U+FFFD for each one would
// triple the byte count of UTF-16 output while adding nothing readable, and the
// digest and byte count are taken from the untouched output, so audit fidelity
// does not depend on what the preview keeps.
func sanitizedToolText(value string) string {
	value = strings.ToValidUTF8(value, "\ufffd")
	if !strings.ContainsRune(value, 0) {
		return value
	}
	return strings.ReplaceAll(value, "\x00", "")
}

func boundedToolResultPreview(output string) (string, bool) {
	output = sanitizedToolText(output)
	if len(output) <= maxToolResultPreviewBytes {
		return output, false
	}
	end := maxToolResultPreviewBytes
	for end > 0 && !utf8.ValidString(output[:end]) {
		end--
	}
	return output[:end], true
}

func toolCommand(toolName string, input json.RawMessage) string {
	switch toolName {
	case "Bash":
		return tools.BashCommand(input)
	case "MCPListTools", "MCPCallTool":
		return tools.MCPCommand(input)
	default:
		return ""
	}
}

func toolRiskWarning(toolName string, input json.RawMessage) string {
	if toolName == "Bash" {
		return tools.BashDangerWarning(tools.BashCommand(input))
	}
	return "tool risk is blocked by policy"
}

func dangerBlockedMessage(warning string) string {
	if strings.TrimSpace(warning) == "" {
		warning = "dangerous tool call blocked by policy"
	}
	return warning
}

func normalizeToolCall(call tools.Call) tools.Call {
	if call.ID == "" {
		call.ID = db.NewID()
	}
	return call
}

func (r *Runner) publish(event Event) {
	if r.hub == nil {
		return
	}
	if event.Data != nil && event.Data["executionGeneration"] == nil && r.store != nil && terminalAgentEvent(event.Type) {
		if runID, _ := event.Data["runId"].(string); strings.TrimSpace(runID) != "" {
			if run, err := r.store.GetRunByID(context.Background(), runID); err == nil {
				event.Data["executionGeneration"] = run.ExecutionGeneration
				event.Data["status"] = run.Status
			}
		}
	}
	r.hub.Publish(event)
}

func terminalAgentEvent(eventType string) bool {
	switch eventType {
	case "agent.done", "agent.error", "agent.interrupted":
		return true
	default:
		return false
	}
}
