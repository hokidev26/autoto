// Command echo-plugin is a minimal MCP stdio server used as the Autoto example
// plugin. It is intentionally dependency-free: it reads JSON-RPC 2.0 messages
// from stdin, writes responses to stdout, and exposes a single "echo" tool.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// request is an incoming JSON-RPC 2.0 message. A message without an id is a
// notification and must not be answered.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// callParams matches the MCP tools/call request parameters.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func main() {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var req request
		if err := decoder.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return // clean shutdown when the host closes stdin
			}
			fmt.Fprintln(os.Stderr, "echo-plugin: decode request:", err)
			os.Exit(1)
		}
		if len(req.ID) == 0 {
			// Notification, e.g. notifications/initialized: nothing to send back.
			continue
		}
		result, callErr := handle(req)
		reply := response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: callErr}
		if err := encoder.Encode(reply); err != nil {
			fmt.Fprintln(os.Stderr, "echo-plugin: write response:", err)
			os.Exit(1)
		}
	}
}

func handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": "echo-plugin", "version": "0.1.0"},
		}, nil
	case "tools/list":
		return map[string]any{"tools": []map[string]any{{
			"name":        "echo",
			"description": "Echoes the provided text back to the caller.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "description": "Text to echo back"},
				},
				"required": []string{"text"},
			},
		}}}, nil
	case "tools/call":
		return handleCall(req.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func handleCall(params json.RawMessage) (any, *rpcError) {
	var call callParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid tools/call params"}
		}
	}
	if call.Name != "echo" {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + call.Name}
	}
	var args struct {
		Text string `json:"text"`
	}
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid echo arguments"}
		}
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": "echo: " + args.Text}},
	}, nil
}
