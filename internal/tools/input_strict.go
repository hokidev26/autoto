package tools

import (
	"bytes"
	"encoding/json"
	"errors"
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

var forbiddenHostInputFieldSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(ForbiddenHostInputFields))
	for _, key := range ForbiddenHostInputFields {
		set[strings.ToLower(key)] = struct{}{}
	}
	return set
}()

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
		return errors.New("trailing data after tool input object")
	}
	return nil
}

func rejectForbiddenHostFields(raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		// Non-object and malformed inputs are handled by the target decoder.
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil
		}
		if _, found := forbiddenHostInputFieldSet[strings.ToLower(key)]; found {
			return fmt.Errorf("host field %q is not allowed in tool input; it is injected by the runtime", key)
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil
		}
	}
	return nil
}
