package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"autoto/internal/process"
)

const DefaultProtocolVersion = "2024-11-05"

type StdioConfig struct {
	Command string
	Args    []string
	CWD     string
	Env     map[string]string
	Timeout time.Duration

	// CleanEnv opts out of inheriting the parent process environment. The child
	// receives only Env plus a small set of process-launch essentials.
	CleanEnv bool
	// StderrLimit bounds captured stderr. Zero preserves the historical 64 KiB
	// default.
	StderrLimit int
	// RedactValues are exact resolved secret values removed from MCP error text.
	RedactValues []string
	// ResponseLimit bounds bytes read from stdout. Zero preserves the historical
	// unbounded protocol stream for existing MCP registrations.
	ResponseLimit int64
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type ToolCallResult struct {
	Content json.RawMessage `json:"content,omitempty"`
	IsError bool            `json:"isError,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"`
}

type Client struct {
	ctx          context.Context
	cmd          *exec.Cmd
	group        *process.Group
	stdin        io.WriteCloser
	dec          *json.Decoder
	stderr       *limitedBuffer
	cancel       context.CancelFunc
	done         chan error
	dead         chan struct{}
	redactValues []string
	mu           sync.Mutex
	nextID       int64
}

func StartStdio(ctx context.Context, cfg StdioConfig) (*Client, error) {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		return nil, errors.New("mcp command is required")
	}
	var runCtx context.Context
	var cancel context.CancelFunc
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	cmd := exec.CommandContext(runCtx, command, cfg.Args...)
	if strings.TrimSpace(cfg.CWD) != "" {
		cmd.Dir = cfg.CWD
	}
	cmd.Env = stdioEnvironment(cfg)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderrLimit := cfg.StderrLimit
	if stderrLimit <= 0 {
		stderrLimit = 64 << 10
	}
	stderr := &limitedBuffer{max: stderrLimit}
	cmd.Stderr = stderr
	group := process.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		_ = group.Close()
		cancel()
		return nil, redactError(err, cfg.RedactValues)
	}
	if err := group.Started(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = group.Close()
		cancel()
		return nil, redactError(err, cfg.RedactValues)
	}
	stdoutReader := io.Reader(stdout)
	if cfg.ResponseLimit > 0 {
		stdoutReader = io.LimitReader(stdout, cfg.ResponseLimit)
	}
	client := &Client{
		ctx: runCtx, cmd: cmd, group: group, stdin: stdin, dec: json.NewDecoder(stdoutReader), stderr: stderr,
		cancel: cancel, done: make(chan error, 1), dead: make(chan struct{}), redactValues: normalizedRedactValues(cfg.RedactValues), nextID: 1,
	}
	go func() {
		client.done <- cmd.Wait()
		close(client.dead)
	}()
	return client, nil
}

// Alive reports whether the stdio process is still running. The pool uses this
// to drop idle clients that exited between calls without consuming Wait.
func (c *Client) Alive() bool {
	if c == nil || c.dead == nil {
		return false
	}
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

func stdioEnvironment(cfg StdioConfig) []string {
	values := make(map[string]string)
	if cfg.CleanEnv {
		for _, entry := range os.Environ() {
			key, value, ok := strings.Cut(entry, "=")
			if ok && essentialEnvironmentKey(key) {
				values[key] = value
			}
		}
	} else {
		for _, entry := range os.Environ() {
			key, value, ok := strings.Cut(entry, "=")
			if ok && key != "" {
				values[key] = value
			}
		}
	}
	for rawKey, value := range cfg.Env {
		key := strings.TrimSpace(rawKey)
		if key != "" && !strings.ContainsAny(key, "=\x00") {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func essentialEnvironmentKey(key string) bool {
	switch key {
	case "PATH", "HOME", "TMPDIR", "TEMP", "TMP", "LANG", "LANGUAGE":
		return true
	default:
		return strings.HasPrefix(key, "LC_")
	}
}

func normalizedRedactValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func redactText(text string, values []string) string {
	for _, value := range values {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	return text
}

func redactError(err error, values []string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactText(err.Error(), normalizedRedactValues(values)))
}

func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": DefaultProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "Autoto", "version": "0.1"},
	}
	if _, err := c.Call(ctx, "initialize", params); err != nil {
		return err
	}
	return c.Notify(ctx, "notifications/initialized", map[string]any{})
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	data, err := c.Call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var body struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	return body.Tools, nil
}

const maxMCPArgumentUnwrap = 4

func normalizeToolArguments(arguments json.RawMessage) (json.RawMessage, error) {
	current := bytes.TrimSpace(arguments)
	for depth := 0; depth <= maxMCPArgumentUnwrap; depth++ {
		if len(current) == 0 || string(current) == "null" {
			return json.RawMessage(`{}`), nil
		}
		if current[0] == '{' {
			var object map[string]any
			if err := json.Unmarshal(current, &object); err != nil {
				return nil, fmt.Errorf("mcp arguments must be a JSON object: %w", err)
			}
			return append(json.RawMessage(nil), current...), nil
		}
		if current[0] != '"' {
			return nil, errors.New("mcp arguments must be a JSON object")
		}
		var encoded string
		if err := json.Unmarshal(current, &encoded); err != nil {
			return nil, fmt.Errorf("mcp arguments must be a JSON object: %w", err)
		}
		current = bytes.TrimSpace([]byte(encoded))
	}
	return nil, errors.New("mcp arguments must be a JSON object")
}

func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (ToolCallResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolCallResult{}, errors.New("mcp tool name is required")
	}
	args, err := normalizeToolArguments(arguments)
	if err != nil {
		return ToolCallResult{}, err
	}
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: name, Arguments: args}
	data, err := c.Call(ctx, "tools/call", params)
	if err != nil {
		return ToolCallResult{}, err
	}
	var result ToolCallResult
	result.Raw = append(json.RawMessage(nil), data...)
	var body struct {
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"isError"`
	}
	if err := json.Unmarshal(data, &body); err == nil {
		result.Content = body.Content
		result.IsError = body.IsError
	}
	return result, nil
}

func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	method = strings.TrimSpace(method)
	if method == "" {
		return nil, errors.New("mcp method is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	request := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if err := json.NewEncoder(c.stdin).Encode(request); err != nil {
		return nil, err
	}
	for {
		var response rpcResponse
		if err := decodeWithContext(ctx, c.dec, &response); err != nil {
			return nil, c.withProcessError(err)
		}
		if response.ID == nil || *response.ID != id {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("mcp %s failed: %s", method, redactText(response.Error.Message, c.redactValues))
		}
		return response.Result, nil
	}
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	method = strings.TrimSpace(method)
	if method == "" {
		return errors.New("mcp method is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	request := rpcNotification{JSONRPC: "2.0", Method: method, Params: params}
	if err := json.NewEncoder(c.stdin).Encode(request); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	_ = c.stdin.Close()
	c.cancel()
	select {
	case err := <-c.done:
		if c.group != nil {
			_ = c.group.Close()
		}
		return err
	case <-time.After(2 * time.Second):
		if c.group != nil {
			_ = c.group.Kill(c.cmd)
			_ = c.group.Close()
		} else if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		return context.DeadlineExceeded
	}
}

func (c *Client) withProcessError(err error) error {
	if c.ctx != nil && c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	select {
	case procErr := <-c.done:
		stderr := redactText(strings.TrimSpace(c.stderr.String()), c.redactValues)
		procText := redactText(fmt.Sprint(procErr), c.redactValues)
		base := redactText(err.Error(), c.redactValues)
		if stderr != "" {
			return fmt.Errorf("%s; process exited: %s; stderr: %s", base, procText, stderr)
		}
		return fmt.Errorf("%s; process exited: %s", base, procText)
	default:
		return redactError(err, c.redactValues)
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func decodeWithContext(ctx context.Context, dec *json.Decoder, dst any) error {
	type result struct{ err error }
	ch := make(chan result, 1)
	go func() { ch <- result{err: dec.Decode(dst)} }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-ch:
		return result.err
	}
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 || b.buf.Len() < b.max {
		remaining := b.max - b.buf.Len()
		if remaining <= 0 || remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
