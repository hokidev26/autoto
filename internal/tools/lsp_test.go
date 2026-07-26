package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fake server ------------------------------------------------------------

type fakeLSPHandler func(method string, params json.RawMessage) (any, *lspRPCError)

type fakeLSPServer struct {
	conn    net.Conn
	reader  *bufio.Reader
	handler fakeLSPHandler

	mu       sync.Mutex
	notified []string
	methods  []string
}

func (s *fakeLSPServer) serve() {
	for {
		payload, err := readLSPMessage(s.reader, lspMaxMessageBytes)
		if err != nil {
			return
		}
		var message lspMessage
		if json.Unmarshal(payload, &message) != nil {
			return
		}
		if message.Method == "" {
			continue
		}
		s.mu.Lock()
		s.methods = append(s.methods, message.Method)
		if len(message.ID) == 0 {
			s.notified = append(s.notified, message.Method)
		}
		s.mu.Unlock()
		if len(message.ID) == 0 {
			if message.Method == "exit" {
				return
			}
			continue
		}
		reply := lspMessage{JSONRPC: "2.0", ID: message.ID}
		if message.Method == "shutdown" {
			reply.Result = json.RawMessage("null")
		} else {
			result, rpcErr := s.handler(message.Method, message.Params)
			if rpcErr != nil {
				reply.Error = rpcErr
			} else {
				encoded, err := json.Marshal(result)
				if err != nil {
					return
				}
				reply.Result = encoded
			}
		}
		encoded, err := json.Marshal(reply)
		if err != nil {
			return
		}
		if writeLSPMessage(s.conn, encoded) != nil {
			return
		}
	}
}

func (s *fakeLSPServer) sawNotification(method string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seen := range s.notified {
		if seen == method {
			return true
		}
	}
	return false
}

func newFakeSession(t *testing.T, cwd string, handler fakeLSPHandler) (*lspSession, *fakeLSPServer) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	server := &fakeLSPServer{conn: serverSide, reader: bufio.NewReaderSize(serverSide, lspMaxHeaderBytes), handler: handler}
	go server.serve()
	session := newLSPSession(newLSPConn(clientSide), cwd)
	session.timeout = 3 * time.Second
	t.Cleanup(func() {
		_ = session.conn.Close()
		_ = serverSide.Close()
	})
	return session, server
}

func newRawConn(t *testing.T) (*lspConn, net.Conn, *bufio.Reader) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	conn := newLSPConn(clientSide)
	t.Cleanup(func() {
		_ = conn.Close()
		_ = serverSide.Close()
	})
	return conn, serverSide, bufio.NewReaderSize(serverSide, lspMaxHeaderBytes)
}

func readFrame(t *testing.T, reader *bufio.Reader) lspMessage {
	t.Helper()
	payload, err := readLSPMessage(reader, lspMaxMessageBytes)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var message lspMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return message
}

func writeFrame(t *testing.T, writer net.Conn, message lspMessage) {
	t.Helper()
	message.JSONRPC = "2.0"
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	if err := writeLSPMessage(writer, encoded); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func goFixture(t *testing.T) (string, string) {
	t.Helper()
	cwd := t.TempDir()
	path := filepath.Join(cwd, "main.go")
	source := "package main\n\nfunc Greet() string { return \"hi\" }\n\nfunc main() { _ = Greet() }\n"
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd, path
}

// --- framing ----------------------------------------------------------------

func TestLSPFrameRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "empty object", payload: `{}`},
		{name: "request", payload: `{"jsonrpc":"2.0","id":1,"method":"initialize"}`},
		{name: "embedded crlf", payload: "{\"text\":\"line one\\r\\nline two\"}"},
		{name: "multibyte", payload: `{"name":"日本語のシンボル"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var buffer strings.Builder
			if err := writeLSPMessage(&buffer, []byte(test.payload)); err != nil {
				t.Fatal(err)
			}
			// The byte count in the header must be the encoded length, not the
			// rune count, or a multibyte payload desynchronizes the stream.
			if !strings.HasPrefix(buffer.String(), fmt.Sprintf("Content-Length: %d\r\n\r\n", len(test.payload))) {
				t.Fatalf("unexpected header in %q", buffer.String())
			}
			got, err := readLSPMessage(bufio.NewReaderSize(strings.NewReader(buffer.String()), lspMaxHeaderBytes), lspMaxMessageBytes)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.payload {
				t.Fatalf("round trip changed the payload: %q", string(got))
			}
		})
	}
}

func TestReadLSPMessageReadsConsecutiveFrames(t *testing.T) {
	var buffer strings.Builder
	for _, payload := range []string{`{"id":1}`, `{"id":2}`, `{"id":3}`} {
		if err := writeLSPMessage(&buffer, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	reader := bufio.NewReaderSize(strings.NewReader(buffer.String()), lspMaxHeaderBytes)
	for index := 1; index <= 3; index++ {
		payload, err := readLSPMessage(reader, lspMaxMessageBytes)
		if err != nil {
			t.Fatalf("frame %d: %v", index, err)
		}
		if want := fmt.Sprintf(`{"id":%d}`, index); string(payload) != want {
			t.Fatalf("frame %d: got %q want %q", index, string(payload), want)
		}
	}
}

func TestReadLSPMessageAcceptsAdditionalHeaders(t *testing.T) {
	stream := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\ncontent-length: 2\r\n\r\n{}"
	payload, err := readLSPMessage(bufio.NewReaderSize(strings.NewReader(stream), lspMaxHeaderBytes), lspMaxMessageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "{}" {
		t.Fatalf("unexpected payload %q", string(payload))
	}
}

func TestReadLSPMessageRejectsMalformedFrames(t *testing.T) {
	tests := []struct {
		name     string
		stream   string
		maxBytes int
		want     string
	}{
		{name: "no content length", stream: "Content-Type: x\r\n\r\n{}", want: "missing Content-Length"},
		{name: "non numeric length", stream: "Content-Length: abc\r\n\r\n{}", want: "invalid Content-Length"},
		{name: "negative length", stream: "Content-Length: -4\r\n\r\n{}", want: "invalid Content-Length"},
		{name: "header without colon", stream: "garbage\r\n\r\n{}", want: "malformed lsp header"},
		{name: "body over limit", stream: "Content-Length: 4096\r\n\r\n", maxBytes: 16, want: "exceeds the 16 byte limit"},
		{name: "truncated body", stream: "Content-Length: 32\r\n\r\nshort", want: "unexpected EOF"},
		{name: "no frame at all", stream: "", want: "EOF"},
		{name: "oversized header line", stream: "Content-Length" + strings.Repeat("x", lspMaxHeaderBytes*2) + ": 2\r\n\r\n{}", want: "lsp header exceeds"},
		{name: "header flood", stream: strings.Repeat("X-Pad: 1\r\n", 70) + "Content-Length: 2\r\n\r\n{}", want: "too many lsp headers"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maxBytes := test.maxBytes
			if maxBytes == 0 {
				maxBytes = lspMaxMessageBytes
			}
			payload, err := readLSPMessage(bufio.NewReaderSize(strings.NewReader(test.stream), lspMaxHeaderBytes), maxBytes)
			if err == nil {
				t.Fatalf("expected an error, got payload %q", string(payload))
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), test.want)
			}
		})
	}
}

// --- connection behavior ----------------------------------------------------

func TestLSPConnCorrelatesOutOfOrderResponses(t *testing.T) {
	conn, serverSide, reader := newRawConn(t)
	type answer struct {
		method string
		value  string
		err    error
	}
	answers := make(chan answer, 3)
	methods := []string{"one", "two", "three"}
	for _, method := range methods {
		go func(method string) {
			raw, err := conn.Call(context.Background(), method, map[string]any{"method": method})
			var body struct {
				Echo string `json:"echo"`
			}
			_ = json.Unmarshal(raw, &body)
			answers <- answer{method: method, value: body.Echo, err: err}
		}(method)
	}
	requests := make([]lspMessage, 0, 3)
	for range methods {
		requests = append(requests, readFrame(t, reader))
	}
	// Reply in reverse arrival order: correlation must come from the id, not from
	// the order the server chose to answer in.
	for index := len(requests) - 1; index >= 0; index-- {
		request := requests[index]
		writeFrame(t, serverSide, lspMessage{
			ID:     request.ID,
			Result: json.RawMessage(fmt.Sprintf(`{"echo":%q}`, request.Method)),
		})
	}
	seen := map[string]string{}
	for range methods {
		select {
		case got := <-answers:
			if got.err != nil {
				t.Fatalf("%s failed: %v", got.method, got.err)
			}
			seen[got.method] = got.value
		case <-time.After(5 * time.Second):
			t.Fatal("a call never completed")
		}
	}
	for _, method := range methods {
		if seen[method] != method {
			t.Fatalf("call %s received %q", method, seen[method])
		}
	}
}

func TestLSPConnIgnoresUnrelatedTraffic(t *testing.T) {
	conn, serverSide, reader := newRawConn(t)
	done := make(chan json.RawMessage, 1)
	failed := make(chan error, 1)
	go func() {
		raw, err := conn.Call(context.Background(), "textDocument/hover", nil)
		if err != nil {
			failed <- err
			return
		}
		done <- raw
	}()
	request := readFrame(t, reader)
	// Progress notifications, log spam, and a response for an id we never sent
	// must all be skipped without breaking the pending call.
	writeFrame(t, serverSide, lspMessage{Method: "$/progress", Params: json.RawMessage(`{"token":"x"}`)})
	writeFrame(t, serverSide, lspMessage{Method: "window/logMessage", Params: json.RawMessage(`{"type":3,"message":"loading"}`)})
	writeFrame(t, serverSide, lspMessage{ID: json.RawMessage("9999"), Result: json.RawMessage(`{"stale":true}`)})
	writeFrame(t, serverSide, lspMessage{ID: request.ID, Result: json.RawMessage(`{"ok":true}`)})
	select {
	case raw := <-done:
		if string(raw) != `{"ok":true}` {
			t.Fatalf("unexpected result %q", string(raw))
		}
	case err := <-failed:
		t.Fatalf("call failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated traffic blocked the pending call")
	}
}

func TestLSPConnAnswersServerRequests(t *testing.T) {
	_, serverSide, reader := newRawConn(t)
	t.Run("workspace configuration", func(t *testing.T) {
		writeFrame(t, serverSide, lspMessage{
			ID:     json.RawMessage("41"),
			Method: "workspace/configuration",
			Params: json.RawMessage(`{"items":[{"section":"gopls"},{"section":"go"}]}`),
		})
		reply := readFrame(t, reader)
		if string(reply.ID) != "41" || reply.Error != nil {
			t.Fatalf("unexpected reply: %+v", reply)
		}
		var settings []map[string]any
		if err := json.Unmarshal(reply.Result, &settings); err != nil {
			t.Fatalf("configuration reply must be an array: %v (%s)", err, string(reply.Result))
		}
		if len(settings) != 2 {
			t.Fatalf("expected one settings entry per item, got %d", len(settings))
		}
	})
	t.Run("progress create", func(t *testing.T) {
		writeFrame(t, serverSide, lspMessage{ID: json.RawMessage("42"), Method: "window/workDoneProgress/create"})
		reply := readFrame(t, reader)
		if string(reply.ID) != "42" || reply.Error != nil || string(reply.Result) != "null" {
			t.Fatalf("unexpected reply: %+v", reply)
		}
	})
	// An unanswered server request stalls gopls, so even an unsupported method
	// must produce an explicit error reply.
	t.Run("unsupported method", func(t *testing.T) {
		writeFrame(t, serverSide, lspMessage{ID: json.RawMessage("43"), Method: "window/showMessageRequest"})
		reply := readFrame(t, reader)
		if string(reply.ID) != "43" {
			t.Fatalf("reply id mismatch: %s", string(reply.ID))
		}
		if reply.Error == nil || reply.Error.Code != -32601 {
			t.Fatalf("expected a MethodNotFound error, got %+v", reply.Error)
		}
	})
}

func TestLSPConnPropagatesServerErrors(t *testing.T) {
	session, _ := newFakeSession(t, t.TempDir(), func(string, json.RawMessage) (any, *lspRPCError) {
		return nil, &lspRPCError{Code: -32603, Message: "no package for file"}
	})
	_, err := session.conn.Call(context.Background(), "textDocument/definition", nil)
	if err == nil || !strings.Contains(err.Error(), "no package for file") {
		t.Fatalf("expected the server message to survive, got %v", err)
	}
}

func TestLSPConnFailsPendingCallsWhenServerDies(t *testing.T) {
	conn, serverSide, reader := newRawConn(t)
	failed := make(chan error, 1)
	go func() {
		_, err := conn.Call(context.Background(), "workspace/symbol", nil)
		failed <- err
	}()
	readFrame(t, reader)
	_ = serverSide.Close()
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("a dead server must fail the pending call")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a dead server left the call hanging")
	}
	// A connection that already failed must reject new calls instead of hanging.
	if _, err := conn.Call(context.Background(), "workspace/symbol", nil); err == nil {
		t.Fatal("expected a call on a failed connection to fail")
	}
}

func TestLSPConnCallHonorsContextDeadline(t *testing.T) {
	conn, _, reader := newRawConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	failed := make(chan error, 1)
	go func() {
		_, err := conn.Call(ctx, "workspace/symbol", nil)
		failed <- err
	}()
	readFrame(t, reader)
	select {
	case err := <-failed:
		if err != context.DeadlineExceeded {
			t.Fatalf("expected DeadlineExceeded, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a silent server must not block forever")
	}
}

// --- paths ------------------------------------------------------------------

func TestLSPURIRoundTrip(t *testing.T) {
	paths := []string{
		filepath.Join(t.TempDir(), "main.go"),
		filepath.Join(t.TempDir(), "dir with spaces", "file name.go"),
		filepath.Join(t.TempDir(), "パッケージ", "コード.go"),
		filepath.Join(t.TempDir(), "pct%20literal.go"),
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			uri := lspURIFromPath(path)
			if !strings.HasPrefix(uri, "file:///") {
				t.Fatalf("uri must be an absolute file URL: %q", uri)
			}
			if strings.Contains(uri, " ") {
				t.Fatalf("uri must be percent-encoded: %q", uri)
			}
			got, ok := lspPathFromURI(uri)
			if !ok {
				t.Fatalf("uri %q did not parse back to a path", uri)
			}
			if got != filepath.Clean(path) {
				t.Fatalf("round trip changed the path: got %q want %q", got, filepath.Clean(path))
			}
		})
	}
}

func TestLSPPathFromURIRejectsUnusableValues(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{name: "empty", uri: ""},
		{name: "http", uri: "http://example.com/main.go"},
		{name: "no scheme", uri: "/tmp/main.go"},
		{name: "file without path", uri: "file://"},
		{name: "invalid escape", uri: "file:///%zz/main.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if path, ok := lspPathFromURI(test.uri); ok {
				t.Fatalf("expected %q to be rejected, got %q", test.uri, path)
			}
		})
	}
}

func TestLSPDisplayPath(t *testing.T) {
	cwd := t.TempDir()
	inside := filepath.Join(cwd, "internal", "tools", "lsp.go")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("package tools\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lspDisplayPath(cwd, inside); got != "internal/tools/lsp.go" {
		t.Fatalf("workspace paths must be relative and slash separated, got %q", got)
	}
	// A definition in the standard library or module cache lives outside the
	// workspace; suppressing it would make the answer useless, so it stays absolute.
	outside := filepath.Join(t.TempDir(), "goroot", "src", "fmt", "print.go")
	if got := lspDisplayPath(cwd, outside); got != filepath.ToSlash(outside) {
		t.Fatalf("external paths must stay absolute, got %q", got)
	}
}

// --- payload parsing --------------------------------------------------------

func TestParseLSPLocations(t *testing.T) {
	uri := lspURIFromPath(filepath.Join(t.TempDir(), "main.go"))
	tests := []struct {
		name      string
		raw       string
		wantCount int
		wantLine  int
	}{
		{name: "null", raw: "null"},
		{name: "empty", raw: ""},
		{name: "empty array", raw: "[]"},
		{name: "single location", raw: fmt.Sprintf(`{"uri":%q,"range":{"start":{"line":4,"character":2},"end":{"line":4,"character":8}}}`, uri), wantCount: 1, wantLine: 4},
		{name: "location array", raw: fmt.Sprintf(`[{"uri":%q,"range":{"start":{"line":7,"character":0},"end":{"line":7,"character":3}}}]`, uri), wantCount: 1, wantLine: 7},
		{name: "location link array", raw: fmt.Sprintf(`[{"targetUri":%q,"targetRange":{"start":{"line":1,"character":0},"end":{"line":9,"character":0}},"targetSelectionRange":{"start":{"line":2,"character":5},"end":{"line":2,"character":9}}}]`, uri), wantCount: 1, wantLine: 2},
		{name: "link without selection range falls back", raw: fmt.Sprintf(`[{"targetUri":%q,"targetRange":{"start":{"line":11,"character":1},"end":{"line":12,"character":1}}}]`, uri), wantCount: 1, wantLine: 11},
		{name: "unusable entries are skipped", raw: `[{"nothing":true}]`, wantCount: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locations, err := parseLSPLocations(json.RawMessage(test.raw))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(locations) != test.wantCount {
				t.Fatalf("got %d locations, want %d: %+v", len(locations), test.wantCount, locations)
			}
			if test.wantCount > 0 && locations[0].Range.Start.Line != test.wantLine {
				t.Fatalf("got line %d, want %d", locations[0].Range.Start.Line, test.wantLine)
			}
		})
	}
}

func TestRenderHoverContents(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "markup content", raw: `{"kind":"markdown","value":"func Greet() string"}`, want: "func Greet() string"},
		{name: "plain string", raw: `"func Greet() string"`, want: "func Greet() string"},
		{name: "marked string", raw: `{"language":"go","value":"func Greet() string"}`, want: "func Greet() string"},
		{name: "array", raw: `[{"language":"go","value":"func Greet() string"},"Greet returns a greeting."]`, want: "func Greet() string\n\nGreet returns a greeting."},
		{name: "null", raw: "null", want: ""},
		{name: "empty", raw: "", want: ""},
		{name: "unknown shape", raw: `12`, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renderHoverContents(json.RawMessage(test.raw)); got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
		})
	}
}

func TestLSPOutputBoundsResults(t *testing.T) {
	t.Run("line cap", func(t *testing.T) {
		output := newLSPOutput(3)
		for index := 0; index < 10; index++ {
			if !output.add(fmt.Sprintf("line %d", index)) {
				break
			}
		}
		if output.lines != 3 || !output.truncated {
			t.Fatalf("expected 3 lines and truncation, got %d lines truncated=%v", output.lines, output.truncated)
		}
		if !strings.HasSuffix(output.String(), "...[truncated]") {
			t.Fatalf("truncated output must say so: %q", output.String())
		}
	})
	t.Run("byte cap", func(t *testing.T) {
		output := newLSPOutput(0)
		line := strings.Repeat("x", 1000)
		for index := 0; index < 1000; index++ {
			if !output.add(line) {
				break
			}
		}
		if !output.truncated {
			t.Fatal("expected the byte cap to trigger")
		}
		if len(output.String()) > lspOutputMaxBytes+len("\n...[truncated]") {
			t.Fatalf("output exceeded its cap: %d bytes", len(output.String()))
		}
	})
}

// --- input validation -------------------------------------------------------

func TestParseSymbolsRequest(t *testing.T) {
	cwd, path := goFixture(t)
	if err := os.Mkdir(filepath.Join(cwd, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		input     symbolsInput
		wantError string
		check     func(*testing.T, symbolsRequest)
	}{
		{
			name:  "definition converts to zero based",
			input: symbolsInput{Action: "definition", FilePath: "main.go", Line: 5, Character: 9},
			check: func(t *testing.T, request symbolsRequest) {
				if request.position.Line != 4 || request.position.Character != 8 {
					t.Fatalf("1-based input must become 0-based protocol coordinates, got %+v", request.position)
				}
				if request.path != path {
					t.Fatalf("path was not resolved to the workspace file: %q", request.path)
				}
				if request.limit != lspDefaultMaxResult {
					t.Fatalf("expected the default limit, got %d", request.limit)
				}
			},
		},
		{
			name:  "references keeps include declaration",
			input: symbolsInput{Action: "references", FilePath: "main.go", Line: 3, Character: 6, IncludeDeclaration: true, Limit: 10},
			check: func(t *testing.T, request symbolsRequest) {
				if !request.includeDeclaration || request.limit != 10 {
					t.Fatalf("unexpected request %+v", request)
				}
			},
		},
		{
			name:  "document symbols needs no position",
			input: symbolsInput{Action: "document_symbols", FilePath: "main.go"},
			check: func(t *testing.T, request symbolsRequest) {
				if request.uri == "" || request.position != (lspPosition{}) {
					t.Fatalf("unexpected request %+v", request)
				}
			},
		},
		{
			name:  "workspace symbols needs only a query",
			input: symbolsInput{Action: "workspace_symbols", Query: "Greet"},
			check: func(t *testing.T, request symbolsRequest) {
				if request.query != "Greet" || request.path != "" {
					t.Fatalf("unexpected request %+v", request)
				}
			},
		},
		{name: "missing action", input: symbolsInput{FilePath: "main.go"}, wantError: "action is required"},
		{name: "unknown action", input: symbolsInput{Action: "rename"}, wantError: "unknown action"},
		{name: "definition without file", input: symbolsInput{Action: "definition", Line: 1, Character: 1}, wantError: "file_path is required"},
		{name: "definition without position", input: symbolsInput{Action: "definition", FilePath: "main.go"}, wantError: "line and character are required"},
		{name: "definition with zero line", input: symbolsInput{Action: "definition", FilePath: "main.go", Line: 0, Character: 3}, wantError: "line and character are required"},
		{name: "definition with query", input: symbolsInput{Action: "definition", FilePath: "main.go", Line: 1, Character: 1, Query: "Greet"}, wantError: "query is only used by workspace_symbols"},
		{name: "document symbols with position", input: symbolsInput{Action: "document_symbols", FilePath: "main.go", Line: 2, Character: 2}, wantError: "line and character are not used"},
		{name: "workspace symbols without query", input: symbolsInput{Action: "workspace_symbols"}, wantError: "query is required"},
		{name: "workspace symbols with file", input: symbolsInput{Action: "workspace_symbols", Query: "Greet", FilePath: "main.go"}, wantError: "file_path is not used"},
		{name: "path escape", input: symbolsInput{Action: "document_symbols", FilePath: "../outside.go"}, wantError: "escapes working directory"},
		{name: "sensitive path", input: symbolsInput{Action: "document_symbols", FilePath: ".git/config"}, wantError: "sensitive path"},
		{name: "missing file", input: symbolsInput{Action: "document_symbols", FilePath: "absent.go"}, wantError: "absent.go"},
		{name: "directory", input: symbolsInput{Action: "document_symbols", FilePath: "pkg"}, wantError: "is a directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := parseSymbolsRequest(test.input, cwd)
			if test.wantError != "" {
				if err == nil {
					t.Fatalf("expected an error mentioning %q, got %+v", test.wantError, request)
				}
				if !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error %q does not mention %q", err.Error(), test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.check != nil {
				test.check(t, request)
			}
		})
	}
}

func TestSymbolsToolMetadata(t *testing.T) {
	tool := SymbolsTool{}
	if tool.Name() != "Symbols" {
		t.Fatalf("unexpected name %q", tool.Name())
	}
	// The tool spawns a language server, so it must not be gated as a plain read.
	if tool.Risk(nil) != RiskExec {
		t.Fatalf("expected RiskExec, got %q", tool.Risk(nil))
	}
	if !strings.Contains(tool.Description(), "gopls") {
		t.Fatalf("the description must name the server it depends on: %q", tool.Description())
	}
	registry := NewRegistry()
	RegisterCore(registry)
	if _, ok := registry.Get("Symbols"); !ok {
		t.Fatal("Symbols must be registered as a core tool")
	}
}

func TestSymbolsToolRejectsBadCalls(t *testing.T) {
	cwd, _ := goFixture(t)
	tests := []struct {
		name  string
		cwd   string
		input string
		want  string
	}{
		{name: "unknown field", cwd: cwd, input: `{"action":"definition","file_path":"main.go","line":1,"character":1,"depth":2}`, want: "unknown field"},
		{name: "wrong type", cwd: cwd, input: `{"action":"definition","file_path":"main.go","line":"5","character":1}`, want: "cannot unmarshal"},
		{name: "host field", cwd: cwd, input: `{"action":"workspace_symbols","query":"Greet","cwd":"/etc"}`, want: "not allowed in tool input"},
		{name: "no working directory", cwd: "", input: `{"action":"workspace_symbols","query":"Greet"}`, want: "working directory is required"},
		{name: "invalid action", cwd: cwd, input: `{"action":"implementations","file_path":"main.go"}`, want: "unknown action"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := (SymbolsTool{}).Execute(context.Background(), Call{ID: "symbols", Name: "Symbols", Input: json.RawMessage(test.input)}, Env{CWD: test.cwd})
			if err != nil {
				t.Fatalf("input errors belong in the result, not the error return: %v", err)
			}
			if !result.IsError || !strings.Contains(result.Output, test.want) {
				t.Fatalf("expected an error mentioning %q, got %+v", test.want, result)
			}
		})
	}
}

// TestSymbolsToolReportsMissingServer is the degradation path that matters most:
// most machines have no gopls, and the tool must say so immediately instead of
// hanging or panicking.
func TestSymbolsToolReportsMissingServer(t *testing.T) {
	cwd, _ := goFixture(t)
	original := goplsBinaryName
	goplsBinaryName = "autoto-nonexistent-language-server"
	t.Cleanup(func() { goplsBinaryName = original })

	if _, err := lspServerCommand(goplsBinaryName); err != errLSPServerNotFound {
		t.Fatalf("expected errLSPServerNotFound, got %v", err)
	}
	start := time.Now()
	result, err := (SymbolsTool{}).Execute(context.Background(), Call{
		ID: "symbols", Name: "Symbols",
		Input: json.RawMessage(`{"action":"document_symbols","file_path":"main.go"}`),
	}, Env{CWD: cwd})
	if err != nil {
		t.Fatalf("a missing server is a tool failure, not an infrastructure error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error result, got %+v", result)
	}
	for _, want := range []string{"gopls", "go install golang.org/x/tools/gopls@latest", "PATH"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("install hint must mention %q: %q", want, result.Output)
		}
	}
	if available, _ := result.Meta["serverAvailable"].(bool); available {
		t.Fatalf("meta must report the server as unavailable: %+v", result.Meta)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("discovery failure must be immediate, took %s", elapsed)
	}
}

// --- request handling against a fake server ---------------------------------

func TestRunSymbolsRequestActions(t *testing.T) {
	cwd, path := goFixture(t)
	uri := lspURIFromPath(path)
	otherPath := filepath.Join(cwd, "helper.go")
	if err := os.WriteFile(otherPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "goroot", "fmt", "print.go")

	tests := []struct {
		name      string
		input     symbolsInput
		result    string
		wantLines []string
		wantMeta  int
	}{
		{
			name:      "definition",
			input:     symbolsInput{Action: "definition", FilePath: "main.go", Line: 5, Character: 15},
			result:    fmt.Sprintf(`{"uri":%q,"range":{"start":{"line":2,"character":5},"end":{"line":2,"character":10}}}`, uri),
			wantLines: []string{"main.go:3:6"},
			wantMeta:  1,
		},
		{
			name:   "definition in the standard library stays absolute",
			input:  symbolsInput{Action: "definition", FilePath: "main.go", Line: 5, Character: 15},
			result: fmt.Sprintf(`[{"uri":%q,"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`, lspURIFromPath(external)),
			wantLines: []string{
				filepath.ToSlash(external) + ":1:1",
			},
			wantMeta: 1,
		},
		{
			name:  "references",
			input: symbolsInput{Action: "references", FilePath: "main.go", Line: 3, Character: 6, IncludeDeclaration: true},
			result: fmt.Sprintf(`[{"uri":%q,"range":{"start":{"line":2,"character":5},"end":{"line":2,"character":10}}},{"uri":%q,"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":4}}}]`,
				uri, lspURIFromPath(otherPath)),
			wantLines: []string{"main.go:3:6", "helper.go:1:1"},
			wantMeta:  2,
		},
		{
			name:      "hover",
			input:     symbolsInput{Action: "hover", FilePath: "main.go", Line: 3, Character: 6},
			result:    `{"contents":{"kind":"markdown","value":"func Greet() string\n\nGreet returns a greeting."}}`,
			wantLines: []string{"func Greet() string", "Greet returns a greeting."},
			wantMeta:  1,
		},
		{
			name:      "document symbols hierarchy",
			input:     symbolsInput{Action: "document_symbols", FilePath: "main.go"},
			result:    `[{"name":"Greet","kind":12,"detail":"func() string","range":{"start":{"line":2,"character":0},"end":{"line":2,"character":40}},"selectionRange":{"start":{"line":2,"character":5},"end":{"line":2,"character":10}},"children":[{"name":"inner","kind":13,"range":{"start":{"line":2,"character":20},"end":{"line":2,"character":25}},"selectionRange":{"start":{"line":2,"character":20},"end":{"line":2,"character":25}}}]}]`,
			wantLines: []string{"Function Greet func() string  [3:6]", "  Variable inner  [3:21]"},
			wantMeta:  2,
		},
		{
			name:      "document symbols flat shape",
			input:     symbolsInput{Action: "document_symbols", FilePath: "main.go"},
			result:    fmt.Sprintf(`[{"name":"main","kind":12,"location":{"uri":%q,"range":{"start":{"line":4,"character":5},"end":{"line":4,"character":9}}}}]`, uri),
			wantLines: []string{"Function main  [5:6]"},
			wantMeta:  1,
		},
		{
			name:      "workspace symbols",
			input:     symbolsInput{Action: "workspace_symbols", Query: "Greet"},
			result:    fmt.Sprintf(`[{"name":"Greet","kind":12,"containerName":"main","location":{"uri":%q,"range":{"start":{"line":2,"character":5},"end":{"line":2,"character":10}}}}]`, uri),
			wantLines: []string{"Function main.Greet  main.go:3:6"},
			wantMeta:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, server := newFakeSession(t, cwd, func(string, json.RawMessage) (any, *lspRPCError) {
				return json.RawMessage(test.result), nil
			})
			request, err := parseSymbolsRequest(test.input, cwd)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runSymbolsRequest(context.Background(), session, request)
			if err != nil || result.IsError {
				t.Fatalf("request failed: result=%+v err=%v", result, err)
			}
			for _, want := range test.wantLines {
				if !strings.Contains(result.Output, want) {
					t.Fatalf("output %q does not contain %q", result.Output, want)
				}
			}
			if count, _ := result.Meta["count"].(int); count != test.wantMeta {
				t.Fatalf("expected count %d, got %+v", test.wantMeta, result.Meta)
			}
			if truncated, _ := result.Meta["truncated"].(bool); truncated {
				t.Fatalf("small results must not report truncation: %+v", result.Meta)
			}
			// Position requests must open the document first; gopls answers them
			// only for files the client has opened.
			if test.input.Action != "workspace_symbols" && !server.sawNotification("textDocument/didOpen") {
				t.Fatalf("expected a didOpen notification, saw %v", server.methods)
			}
		})
	}
}

func TestRunSymbolsRequestReportsEmptyAnswers(t *testing.T) {
	cwd, _ := goFixture(t)
	tests := []struct {
		name   string
		input  symbolsInput
		result string
		want   string
	}{
		{name: "definition", input: symbolsInput{Action: "definition", FilePath: "main.go", Line: 1, Character: 1}, result: "null", want: "No definition found at main.go:1:1."},
		{name: "references", input: symbolsInput{Action: "references", FilePath: "main.go", Line: 1, Character: 1}, result: "[]", want: "No reference found at main.go:1:1."},
		{name: "hover", input: symbolsInput{Action: "hover", FilePath: "main.go", Line: 1, Character: 1}, result: "null", want: "No hover information at main.go:1:1."},
		{name: "document symbols", input: symbolsInput{Action: "document_symbols", FilePath: "main.go"}, result: "[]", want: "No symbols in main.go."},
		{name: "workspace symbols", input: symbolsInput{Action: "workspace_symbols", Query: "Nope"}, result: "null", want: `No workspace symbols match "Nope".`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, _ := newFakeSession(t, cwd, func(string, json.RawMessage) (any, *lspRPCError) {
				return json.RawMessage(test.result), nil
			})
			request, err := parseSymbolsRequest(test.input, cwd)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runSymbolsRequest(context.Background(), session, request)
			if err != nil {
				t.Fatal(err)
			}
			// An empty answer is a fact about the code, not a tool failure.
			if result.IsError {
				t.Fatalf("an empty result must not be an error: %+v", result)
			}
			if result.Output != test.want {
				t.Fatalf("got %q want %q", result.Output, test.want)
			}
		})
	}
}

func TestRunSymbolsRequestBoundsLargeResults(t *testing.T) {
	cwd, path := goFixture(t)
	uri := lspURIFromPath(path)
	locations := make([]string, 0, 5000)
	for index := 0; index < 5000; index++ {
		locations = append(locations, fmt.Sprintf(`{"uri":%q,"range":{"start":{"line":%d,"character":0},"end":{"line":%d,"character":4}}}`, uri, index, index))
	}
	session, _ := newFakeSession(t, cwd, func(string, json.RawMessage) (any, *lspRPCError) {
		return json.RawMessage("[" + strings.Join(locations, ",") + "]"), nil
	})
	request, err := parseSymbolsRequest(symbolsInput{Action: "references", FilePath: "main.go", Line: 3, Character: 6}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runSymbolsRequest(context.Background(), session, request)
	if err != nil || result.IsError {
		t.Fatalf("request failed: result=%+v err=%v", result, err)
	}
	if lines := strings.Count(result.Output, "\n") + 1; lines > lspDefaultMaxResult+1 {
		t.Fatalf("output was not capped: %d lines", lines)
	}
	if len(result.Output) > lspOutputMaxBytes+len("\n...[truncated]") {
		t.Fatalf("output exceeded the byte cap: %d bytes", len(result.Output))
	}
	if !strings.HasSuffix(result.Output, "...[truncated]") {
		t.Fatalf("capped output must announce truncation: %q", result.Output[max(0, len(result.Output)-80):])
	}
	if truncated, _ := result.Meta["truncated"].(bool); !truncated {
		t.Fatalf("meta must report truncation: %+v", result.Meta)
	}
}

func TestRunSymbolsRequestHandlesServerFailures(t *testing.T) {
	cwd, _ := goFixture(t)
	t.Run("server error", func(t *testing.T) {
		session, _ := newFakeSession(t, cwd, func(string, json.RawMessage) (any, *lspRPCError) {
			return nil, &lspRPCError{Code: -32603, Message: "no metadata for file"}
		})
		request, err := parseSymbolsRequest(symbolsInput{Action: "definition", FilePath: "main.go", Line: 3, Character: 6}, cwd)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runSymbolsRequest(context.Background(), session, request)
		if err != nil {
			t.Fatalf("a server-side failure belongs in the result: %v", err)
		}
		if !result.IsError || !strings.Contains(result.Output, "no metadata for file") {
			t.Fatalf("unexpected result %+v", result)
		}
	})
	t.Run("unreadable payload", func(t *testing.T) {
		session, _ := newFakeSession(t, cwd, func(string, json.RawMessage) (any, *lspRPCError) {
			return json.RawMessage(`{"contents":42}`), nil
		})
		request, err := parseSymbolsRequest(symbolsInput{Action: "document_symbols", FilePath: "main.go"}, cwd)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runSymbolsRequest(context.Background(), session, request)
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || !strings.Contains(result.Output, "unreadable") {
			t.Fatalf("unexpected result %+v", result)
		}
	})
	t.Run("silent server times out", func(t *testing.T) {
		clientSide, serverSide := net.Pipe()
		t.Cleanup(func() { _ = serverSide.Close() })
		// Drain requests but never answer, which is what a wedged server looks like.
		go func() {
			reader := bufio.NewReaderSize(serverSide, lspMaxHeaderBytes)
			for {
				if _, err := readLSPMessage(reader, lspMaxMessageBytes); err != nil {
					return
				}
			}
		}()
		session := newLSPSession(newLSPConn(clientSide), cwd)
		session.timeout = 150 * time.Millisecond
		request, err := parseSymbolsRequest(symbolsInput{Action: "workspace_symbols", Query: "Greet"}, cwd)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		result, err := runSymbolsRequest(context.Background(), session, request)
		if err != nil {
			t.Fatalf("a request timeout belongs in the result: %v", err)
		}
		if !result.IsError || !strings.Contains(result.Output, "did not answer in time") {
			t.Fatalf("unexpected result %+v", result)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("the request timeout did not fire promptly, took %s", elapsed)
		}
	})
	t.Run("cancelled context returns an error", func(t *testing.T) {
		session, _ := newFakeSession(t, cwd, func(string, json.RawMessage) (any, *lspRPCError) {
			return json.RawMessage("null"), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request, err := parseSymbolsRequest(symbolsInput{Action: "workspace_symbols", Query: "Greet"}, cwd)
		if err != nil {
			t.Fatal(err)
		}
		// Cancellation is the agent loop stopping, so it must surface as an error
		// return rather than a result the model would try to interpret.
		if _, err := runSymbolsRequest(ctx, session, request); err == nil {
			t.Fatal("expected the cancellation to be returned as an error")
		}
	})
}

func TestLSPSessionCloseSendsShutdownHandshake(t *testing.T) {
	session, server := newFakeSession(t, t.TempDir(), func(string, json.RawMessage) (any, *lspRPCError) {
		return json.RawMessage("null"), nil
	})
	session.close()
	deadline := time.After(3 * time.Second)
	for {
		server.mu.Lock()
		methods := append([]string(nil), server.methods...)
		server.mu.Unlock()
		if len(methods) >= 2 && methods[0] == "shutdown" && methods[1] == "exit" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("expected shutdown then exit, saw %v", methods)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// --- real child process -----------------------------------------------------

// longRunningCommand returns a process that stays alive and quiet, which is all
// the lifecycle tests need: they assert the process is reaped, not what it says.
func longRunningCommand(t *testing.T) (string, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		binary, err := exec.LookPath("cmd")
		if err != nil {
			t.Skipf("cmd is unavailable: %v", err)
		}
		return binary, []string{"/C", "ping", "-n", "60", "127.0.0.1"}
	}
	binary, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh is unavailable: %v", err)
	}
	return binary, []string{"-c", "sleep 60"}
}

func TestLSPProcessCloseTerminatesServer(t *testing.T) {
	binary, args := longRunningCommand(t)
	proc, err := startLSPProcess(context.Background(), binary, t.TempDir(), args)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	_ = proc.Close()
	select {
	case <-proc.finished:
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not reap the server process")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Close took %s", elapsed)
	}
	if proc.cmd.ProcessState == nil {
		t.Fatal("the process was never waited on")
	}
	// Close must be safe to call twice: the tool defers it and the watchdog may
	// have already run.
	_ = proc.Close()
}

func TestLSPProcessTerminatesOnContextCancellation(t *testing.T) {
	binary, args := longRunningCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	proc, err := startLSPProcess(ctx, binary, t.TempDir(), args)
	if err != nil {
		cancel()
		t.Fatalf("start: %v", err)
	}
	cancel()
	select {
	case <-proc.finished:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelling the context left the server process running")
	}
}

func TestStartGoplsSessionReportsStartupFailure(t *testing.T) {
	binary, args := longRunningCommand(t)
	original := goplsBinaryName
	goplsBinaryName = binary
	t.Cleanup(func() { goplsBinaryName = original })
	// The command exists but never speaks LSP. Rather than assert on the real
	// startup budget, drive the handshake directly with a short deadline.
	proc, err := startLSPProcess(context.Background(), binary, t.TempDir(), args)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer proc.Close()
	session := newLSPSession(newLSPConn(proc), t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := session.initialize(ctx); err == nil {
		t.Fatal("a server that never answers initialize must fail the handshake")
	}
}

// TestSymbolsToolAgainstRealGopls is the only test that needs the real server.
// It also needs the go command, because gopls loads the build list to answer.
func TestSymbolsToolAgainstRealGopls(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skipf("gopls is not installed: %v", err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("the go command is not on PATH, so gopls cannot load a package: %v", err)
	}
	cwd := t.TempDir()
	files := map[string]string{
		"go.mod":  "module symbolsfixture\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc Greet() string {\n\treturn \"hi\"\n}\n\nfunc main() {\n\t_ = Greet()\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := (SymbolsTool{}).Execute(ctx, Call{
		ID: "symbols", Name: "Symbols",
		Input: json.RawMessage(`{"action":"definition","file_path":"main.go","line":8,"character":7}`),
	}, Env{CWD: cwd})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("gopls reported an error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "main.go:3:6") {
		t.Fatalf("expected the definition of Greet at main.go:3:6, got %q", result.Output)
	}
}
