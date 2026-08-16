package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStartStdioCleanEnvironmentDoesNotInheritSecrets(t *testing.T) {
	t.Setenv("AUTOTO_MCP_PARENT_SECRET", "must-not-leak")
	client, err := StartStdio(context.Background(), StdioConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestStdioHelperProcess"},
		CleanEnv: true,
		Env: map[string]string{
			"AUTOTO_MCP_STDIO_HELPER": "1",
			"AUTOTO_MCP_EXPLICIT":     "allowed",
		},
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	data, err := client.Call(context.Background(), "env/read", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Parent   string `json:"parent"`
		Explicit string `json:"explicit"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Parent != "" {
		t.Fatalf("clean MCP environment leaked parent secret %q", got.Parent)
	}
	if got.Explicit != "allowed" {
		t.Fatalf("explicit environment missing: %+v", got)
	}
	if got.Path == "" {
		t.Fatal("clean MCP environment must retain PATH")
	}
}

func TestStartStdioLegacyEnvironmentStillInherits(t *testing.T) {
	t.Setenv("AUTOTO_MCP_PARENT_SECRET", "legacy-value")
	client, err := StartStdio(context.Background(), StdioConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestStdioHelperProcess"},
		Env:     map[string]string{"AUTOTO_MCP_STDIO_HELPER": "1"},
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	data, err := client.Call(context.Background(), "env/read", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "legacy-value") {
		t.Fatalf("legacy environment no longer inherits parent values: %s", data)
	}
}

func TestStartStdioTimeoutAndExactSecretRedaction(t *testing.T) {
	const secret = "resolved-secret-value"
	client, err := StartStdio(context.Background(), StdioConfig{
		Command:      os.Args[0],
		Args:         []string{"-test.run=TestStdioHelperProcess"},
		CleanEnv:     true,
		Env:          map[string]string{"AUTOTO_MCP_STDIO_HELPER": "1", "AUTOTO_MCP_ERROR_SECRET": secret},
		Timeout:      time.Second,
		RedactValues: []string{secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Call(context.Background(), "secret/error", map[string]any{})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected exact secret redaction, got %v", err)
	}

	timed, err := StartStdio(context.Background(), StdioConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestStdioHelperProcess"},
		CleanEnv: true,
		Env:      map[string]string{"AUTOTO_MCP_STDIO_HELPER": "1"},
		Timeout:  50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer timed.Close()
	_, err = timed.Call(context.Background(), "hang", map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected configured timeout, got %v", err)
	}
}

func TestNormalizeToolArgumentsKeepsJSONObjects(t *testing.T) {
	object, err := normalizeToolArguments(json.RawMessage(`{"type":"url","url":"https://www.youtube.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(object) != `{"type":"url","url":"https://www.youtube.com"}` {
		t.Fatalf("object arguments were rewritten: %s", object)
	}
	unwrapped, err := normalizeToolArguments(json.RawMessage(`"{\"type\":\"url\",\"url\":\"https://www.youtube.com\"}"`))
	if err != nil {
		t.Fatal(err)
	}
	if string(unwrapped) != `{"type":"url","url":"https://www.youtube.com"}` {
		t.Fatalf("stringified arguments were not unwrapped: %s", unwrapped)
	}
	empty, err := normalizeToolArguments(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(empty) != `{}` {
		t.Fatalf("empty arguments should become {}, got %s", empty)
	}
	if _, err := normalizeToolArguments(json.RawMessage(`["https://www.youtube.com"]`)); err == nil {
		t.Fatal("array arguments must be rejected")
	}
}

func TestCallToolSendsArgumentsAsJSONObject(t *testing.T) {
	client, err := StartStdio(context.Background(), StdioConfig{
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestStdioHelperProcess"},
		CleanEnv: true,
		Env:      map[string]string{"AUTOTO_MCP_STDIO_HELPER": "1"},
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.CallTool(context.Background(), "navigate_page", json.RawMessage(`{"type":"url","url":"https://www.youtube.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Raw), `"arguments":{"type":"url","url":"https://www.youtube.com"}`) {
		t.Fatalf("tools/call did not send an arguments object: %s", result.Raw)
	}
	stringified, err := client.CallTool(context.Background(), "navigate_page", json.RawMessage(`"{\"type\":\"url\",\"url\":\"https://www.youtube.com\"}"`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stringified.Raw), `"arguments":{"type":"url","url":"https://www.youtube.com"}`) {
		t.Fatalf("stringified arguments were forwarded as a string: %s", stringified.Raw)
	}
}

func TestLimitedBufferHonorsConfiguredLimit(t *testing.T) {
	buffer := &limitedBuffer{max: 5}
	if n, err := buffer.Write([]byte("123456789")); err != nil || n != 9 {
		t.Fatalf("unexpected write n=%d err=%v", n, err)
	}
	if got := buffer.String(); got != "12345" {
		t.Fatalf("unexpected limited stderr %q", got)
	}
}

func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv("AUTOTO_MCP_STDIO_HELPER") != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if decoder.Decode(&request) != nil {
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		switch request.Method {
		case "tools/call":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": json.RawMessage(request.Params)})
		case "env/read":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]string{
					"parent":   os.Getenv("AUTOTO_MCP_PARENT_SECRET"),
					"explicit": os.Getenv("AUTOTO_MCP_EXPLICIT"),
					"path":     os.Getenv("PATH"),
				},
			})
		case "secret/error":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"error":   map[string]any{"code": -32000, "message": "failure: " + os.Getenv("AUTOTO_MCP_ERROR_SECRET")},
			})
		case "hang":
			time.Sleep(10 * time.Second)
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}})
		}
	}
}
