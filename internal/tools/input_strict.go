package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ForbiddenHostInputFields must never be supplied by the model for core tools.
// Host identity and workspace binding are injected exclusively via Env.
var ForbiddenHostInputFields = []string{
	// Host identity / workspace binding — never accepted from model input.
	// Note: run_id/runId is intentionally allowed for tools like ContextAsk that
	// reference a target child run, not the host agent identity.
	"agentId", "agent_id",
	"bookId", "book_id",
	"sessionId", "session_id",
	"bookRoot", "book_root",
	"projectId", "project_id",
	"worklineId", "workline_id",
	"cwd", "workingDirectory", "working_directory",
}

// StrictDecode unmarshals tool input with unknown fields rejected (closed-world).
func StrictDecode(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := rejectForbiddenHostFields(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after tool input object")
	}
	return nil
}

func rejectForbiddenHostFields(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		// Non-object inputs are handled by the target decoder.
		return nil
	}
	forbidden := make(map[string]struct{}, len(ForbiddenHostInputFields))
	for _, key := range ForbiddenHostInputFields {
		forbidden[strings.ToLower(key)] = struct{}{}
	}
	for present := range object {
		if _, ok := forbidden[strings.ToLower(present)]; ok {
			return fmt.Errorf("host field %q is not allowed in tool input; it is injected by the runtime", present)
		}
	}
	return nil
}
