package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictDecodeRejectsForbiddenHostFields(t *testing.T) {
	var dst struct {
		Command string `json:"command"`
	}
	err := StrictDecode(json.RawMessage(`{"command":"ls","cwd":"/tmp"}`), &dst)
	if err == nil || !strings.Contains(err.Error(), `host field "cwd"`) {
		t.Fatalf("expected forbidden host field error, got %v", err)
	}
}

func TestStrictDecodeRejectsUnknownFields(t *testing.T) {
	var dst struct {
		Command string `json:"command"`
	}
	err := StrictDecode(json.RawMessage(`{"command":"ls","extra":true}`), &dst)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}
