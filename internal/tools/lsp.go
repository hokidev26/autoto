package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"autoto/internal/process"
)

const (
	// lspStartupTimeout bounds handshake work only. A cold gopls has to load the
	// build list before it answers "initialize", so this is deliberately looser
	// than a normal request.
	lspStartupTimeout = 25 * time.Second
	lspRequestTimeout = 30 * time.Second
	// lspSessionMaxLife is the hard ceiling on the child process. A session is
	// per tool call, so no gopls may outlive one Execute by more than the grace
	// period, even if every other timeout is somehow bypassed.
	lspSessionMaxLife = 3 * time.Minute
	lspShutdownGrace  = 2 * time.Second

	lspMaxMessageBytes  = 8 << 20
	lspMaxHeaderBytes   = 8 << 10
	lspMaxDidOpenBytes  = 4 << 20
	lspStderrMaxBytes   = 16 << 10
	lspOutputMaxBytes   = 20000
	lspDefaultMaxResult = 200
)

var errLSPServerNotFound = errors.New("language server binary not found")

// goplsBinaryName is a variable so tests can point discovery at a name that is
// guaranteed to be absent and exercise the not-installed path.
var goplsBinaryName = "gopls"

const goplsInstallHint = "gopls (the Go language server) was not found on PATH, so symbol navigation is unavailable. " +
	"Install it with: go install golang.org/x/tools/gopls@latest, then make sure the Go bin directory " +
	"(usually %USERPROFILE%\\go\\bin on Windows, ~/go/bin elsewhere) is on PATH. Until then use Grep for text search."

// --- JSON-RPC framing -------------------------------------------------------

type lspMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspRPCError    `json:"error,omitempty"`
}

type lspRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *lspRPCError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("language server error %d", e.Code)
	}
	return e.Message
}

// writeLSPMessage emits one Content-Length framed payload. The header and body
// go out in a single Write so two concurrent senders cannot interleave a header
// with someone else's body.
func writeLSPMessage(writer io.Writer, payload []byte) error {
	var frame bytes.Buffer
	frame.Grow(len(payload) + 32)
	frame.WriteString("Content-Length: ")
	frame.WriteString(strconv.Itoa(len(payload)))
	frame.WriteString("\r\n\r\n")
	frame.Write(payload)
	_, err := writer.Write(frame.Bytes())
	return err
}

// readLSPMessage reads one framed payload. It uses ReadSlice rather than
// ReadString so a server that never emits a newline cannot make the header
// parser allocate without bound.
func readLSPMessage(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	contentLength := -1
	headers := 0
	for {
		headers++
		if headers > 64 {
			return nil, errors.New("too many lsp headers")
		}
		line, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, fmt.Errorf("lsp header exceeds %d bytes", lspMaxHeaderBytes)
			}
			return nil, err
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			break
		}
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("malformed lsp header %q", trimmed)
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed < 0 {
			return nil, fmt.Errorf("invalid Content-Length %q", strings.TrimSpace(value))
		}
		contentLength = parsed
	}
	if contentLength < 0 {
		return nil, errors.New("lsp message is missing Content-Length")
	}
	// The stream cannot be resynchronized after skipping an oversized body, so an
	// over-limit frame kills the connection rather than being dropped.
	if maxBytes > 0 && contentLength > maxBytes {
		return nil, fmt.Errorf("lsp message of %d bytes exceeds the %d byte limit", contentLength, maxBytes)
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// --- connection -------------------------------------------------------------

// lspConn speaks JSON-RPC 2.0 over any duplex stream, which is what makes the
// framing and correlation logic testable without a real language server.
type lspConn struct {
	stream  io.ReadWriteCloser
	reader  *bufio.Reader
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *lspMessage
	failure error

	done     chan struct{}
	closeOne sync.Once
}

func newLSPConn(stream io.ReadWriteCloser) *lspConn {
	conn := &lspConn{
		stream:  stream,
		reader:  bufio.NewReaderSize(stream, lspMaxHeaderBytes),
		nextID:  1,
		pending: make(map[int64]chan *lspMessage),
		done:    make(chan struct{}),
	}
	go conn.readLoop()
	return conn
}

func (c *lspConn) readLoop() {
	for {
		payload, err := readLSPMessage(c.reader, lspMaxMessageBytes)
		if err != nil {
			c.fail(err)
			return
		}
		var message lspMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			c.fail(fmt.Errorf("invalid lsp payload: %w", err))
			return
		}
		switch {
		case message.Method != "" && len(message.ID) > 0:
			c.answerServerRequest(message)
		case message.Method != "":
			// Notifications (diagnostics, progress, log messages) need no reply.
		default:
			c.deliver(&message)
		}
	}
}

// answerServerRequest keeps the server unblocked. gopls waits for a reply to
// every request it sends; leaving one unanswered stalls the requests we care
// about, so unknown methods get an explicit MethodNotFound rather than silence.
func (c *lspConn) answerServerRequest(request lspMessage) {
	reply := lspMessage{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "window/workDoneProgress/create", "client/registerCapability", "client/unregisterCapability":
		reply.Result = json.RawMessage("null")
	case "workspace/configuration":
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(request.Params, &params)
		settings := make([]map[string]any, len(params.Items))
		for index := range settings {
			settings[index] = map[string]any{}
		}
		encoded, err := json.Marshal(settings)
		if err != nil {
			encoded = json.RawMessage("[]")
		}
		reply.Result = encoded
	default:
		reply.Error = &lspRPCError{Code: -32601, Message: "method not supported by this client: " + request.Method}
	}
	_ = c.send(reply)
}

func (c *lspConn) deliver(message *lspMessage) {
	id, err := strconv.ParseInt(strings.TrimSpace(string(message.ID)), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	waiter, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		waiter <- message
	}
}

// fail is terminal: the read loop has stopped, so every waiter is woken and no
// later delivery can race with the channel closes here.
func (c *lspConn) fail(err error) {
	c.mu.Lock()
	if c.failure == nil {
		c.failure = err
	}
	waiters := c.pending
	c.pending = make(map[int64]chan *lspMessage)
	c.mu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
	}
	c.closeOne.Do(func() { close(c.done) })
}

func (c *lspConn) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return c.failure
	}
	return errors.New("language server connection closed")
}

func (c *lspConn) send(message lspMessage) error {
	message.JSONRPC = "2.0"
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeLSPMessage(c.stream, payload)
}

func (c *lspConn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	encoded, err := encodeLSPParams(params)
	if err != nil {
		return nil, err
	}
	waiter := make(chan *lspMessage, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, c.err()
	default:
	}
	id := c.nextID
	c.nextID++
	c.pending[id] = waiter
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.send(lspMessage{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: encoded}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, c.err()
	case message, ok := <-waiter:
		if !ok || message == nil {
			return nil, c.err()
		}
		if message.Error != nil {
			return nil, fmt.Errorf("%s failed: %s", method, message.Error.Error())
		}
		return message.Result, nil
	}
}

func (c *lspConn) Notify(method string, params any) error {
	encoded, err := encodeLSPParams(params)
	if err != nil {
		return err
	}
	return c.send(lspMessage{Method: method, Params: encoded})
}

func (c *lspConn) Close() error {
	c.closeOne.Do(func() { close(c.done) })
	return c.stream.Close()
}

func encodeLSPParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}

// --- server process ---------------------------------------------------------

// lspProcess adapts a child process's stdio to io.ReadWriteCloser so the
// connection layer never has to know a process is involved.
type lspProcess struct {
	cmd      *exec.Cmd
	group    *process.Group
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   *lspStderrBuffer
	done     chan error
	finished chan struct{}
	cancel   context.CancelFunc
	stopOne  sync.Once
}

func (p *lspProcess) Read(buffer []byte) (int, error)  { return p.stdout.Read(buffer) }
func (p *lspProcess) Write(buffer []byte) (int, error) { return p.stdin.Write(buffer) }

// Close terminates the whole process tree. process.Group owns the kill so a
// gopls that spawned helpers does not leak on Windows.
func (p *lspProcess) Close() error {
	p.stopOne.Do(func() {
		_ = p.stdin.Close()
		_ = p.group.Terminate(p.cmd, p.done, lspShutdownGrace)
		_ = p.group.Close()
		_ = p.stdout.Close()
		p.cancel()
	})
	return nil
}

type lspStderrBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lspStderrBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := lspStderrMaxBytes - b.buffer.Len(); remaining > 0 {
		if remaining > len(payload) {
			remaining = len(payload)
		}
		b.buffer.Write(payload[:remaining])
	}
	return len(payload), nil
}

func (b *lspStderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.buffer.String())
}

// lspServerCommand is the discovery seam: it reports the resolved binary or
// errLSPServerNotFound so callers can render an install hint instead of hanging.
func lspServerCommand(name string) (string, error) {
	binary, err := exec.LookPath(name)
	if err != nil {
		return "", errLSPServerNotFound
	}
	return binary, nil
}

func startLSPProcess(ctx context.Context, binary, cwd string, args []string) (*lspProcess, error) {
	runCtx, cancel := context.WithTimeout(ctx, lspSessionMaxLife)
	// Plain Command, not CommandContext: process.Group must own the tree kill,
	// because CommandContext only signals the direct child.
	cmd := exec.Command(binary, args...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, err
	}
	stderr := &lspStderrBuffer{}
	cmd.Stderr = stderr
	group := process.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		_ = group.Close()
		cancel()
		return nil, err
	}
	if err := group.Started(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = group.Close()
		cancel()
		return nil, err
	}
	proc := &lspProcess{
		cmd: cmd, group: group, stdin: stdin, stdout: stdout, stderr: stderr,
		done: make(chan error, 1), finished: make(chan struct{}), cancel: cancel,
	}
	go func() {
		proc.done <- cmd.Wait()
		close(proc.finished)
	}()
	// Watchdog: caller cancellation or the lifetime ceiling kills the tree even
	// if no request is in flight to notice.
	go func() {
		select {
		case <-runCtx.Done():
			_ = proc.Close()
		case <-proc.finished:
			cancel()
		}
	}()
	return proc, nil
}

// --- session ----------------------------------------------------------------

type lspSession struct {
	conn *lspConn
	proc *lspProcess
	cwd  string
	// timeout bounds a single request. It is a field rather than a constant so
	// tests can assert timeout behavior without waiting out the real budget.
	timeout time.Duration
	opened  map[string]bool
}

func newLSPSession(conn *lspConn, cwd string) *lspSession {
	return &lspSession{conn: conn, cwd: cwd, timeout: lspRequestTimeout, opened: make(map[string]bool)}
}

func startGoplsSession(ctx context.Context, cwd string) (*lspSession, error) {
	binary, err := lspServerCommand(goplsBinaryName)
	if err != nil {
		return nil, err
	}
	proc, err := startLSPProcess(ctx, binary, cwd, []string{"serve"})
	if err != nil {
		return nil, err
	}
	session := newLSPSession(newLSPConn(proc), cwd)
	session.proc = proc
	initCtx, cancel := context.WithTimeout(ctx, lspStartupTimeout)
	defer cancel()
	if err := session.initialize(initCtx); err != nil {
		session.close()
		if detail := proc.stderr.String(); detail != "" {
			return nil, fmt.Errorf("%w; stderr: %s", err, detail)
		}
		return nil, err
	}
	return session, nil
}

func (s *lspSession) initialize(ctx context.Context) error {
	root, err := filepath.Abs(s.cwd)
	if err != nil {
		return err
	}
	rootURI := lspURIFromPath(root)
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"clientInfo": map[string]any{
			"name":    "Autoto",
			"version": "0.1",
		},
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"workspaceFolders": true,
				"symbol":           map[string]any{"dynamicRegistration": false},
				"configuration":    true,
			},
			"textDocument": map[string]any{
				"synchronization": map[string]any{"dynamicRegistration": false},
				"definition":      map[string]any{"linkSupport": true},
				"references":      map[string]any{"dynamicRegistration": false},
				"hover":           map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
				"documentSymbol":  map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
		},
		"workspaceFolders": []map[string]any{{"uri": rootURI, "name": filepath.Base(root)}},
	}
	if _, err := s.conn.Call(ctx, "initialize", params); err != nil {
		return err
	}
	return s.conn.Notify("initialized", map[string]any{})
}

// close performs the LSP shutdown/exit handshake, then kills whatever is left.
// It uses its own context because cleanup must still run when the caller's
// context is already cancelled.
func (s *lspSession) close() {
	if s == nil || s.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspShutdownGrace)
	if _, err := s.conn.Call(ctx, "shutdown", nil); err == nil {
		_ = s.conn.Notify("exit", nil)
	}
	cancel()
	_ = s.conn.Close()
	if s.proc != nil {
		_ = s.proc.Close()
	}
}

// openDocument sends didOpen before position requests. gopls answers reliably
// only for documents the client has opened, and an unopened file yields a
// "no file for uri" error instead of a location.
func (s *lspSession) openDocument(path string) error {
	uri := lspURIFromPath(path)
	if s.opened[uri] {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > lspMaxDidOpenBytes {
		// Too large to ship over the wire; gopls falls back to the on-disk copy.
		s.opened[uri] = true
		return nil
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	err = s.conn.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": lspLanguageID(path),
			"version":    1,
			"text":       string(text),
		},
	})
	if err != nil {
		return err
	}
	s.opened[uri] = true
	return nil
}

func (s *lspSession) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	timeout := s.timeout
	if timeout <= 0 {
		timeout = lspRequestTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.conn.Call(callCtx, method, params)
}

func lspLanguageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".mod":
		return "go.mod"
	case ".sum":
		return "go.sum"
	default:
		return "go"
	}
}

// --- URI handling -----------------------------------------------------------

func lspURIFromPath(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	uri := url.URL{Scheme: "file", Path: slashed}
	return uri.String()
}

func lspPathFromURI(uri string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || !strings.EqualFold(parsed.Scheme, "file") {
		return "", false
	}
	path := parsed.Path
	if path == "" {
		return "", false
	}
	// A Windows URI carries the drive letter in the first path segment
	// (file:///C:/x -> /C:/x). The check is unconditional because such a path
	// only ever originates from a Windows-shaped URI.
	if trimmed := strings.TrimPrefix(path, "/"); len(trimmed) > 1 && trimmed[1] == ':' {
		path = trimmed
	}
	if parsed.Host != "" {
		path = "//" + parsed.Host + path
	}
	return filepath.Clean(filepath.FromSlash(path)), true
}

// lspDisplayPath renders a result location. Locations inside the workspace are
// shown relative to it; anything else (the standard library, the module cache)
// is shown absolute, because a definition that lives outside the workspace is
// the correct answer and must not be suppressed.
func lspDisplayPath(cwd, path string) string {
	if resolved, err := resolveInCWD(cwd, path); err == nil {
		if base, absErr := filepath.Abs(cwd); absErr == nil {
			if rel, relErr := filepath.Rel(base, resolved); relErr == nil {
				return filepath.ToSlash(rel)
			}
		}
	}
	return filepath.ToSlash(path)
}

// --- protocol payloads ------------------------------------------------------

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type lspLocationLink struct {
	TargetURI            string   `json:"targetUri"`
	TargetRange          lspRange `json:"targetRange"`
	TargetSelectionRange lspRange `json:"targetSelectionRange"`
}

type lspDocumentSymbol struct {
	Name           string              `json:"name"`
	Detail         string              `json:"detail"`
	Kind           int                 `json:"kind"`
	Range          lspRange            `json:"range"`
	SelectionRange lspRange            `json:"selectionRange"`
	Children       []lspDocumentSymbol `json:"children"`
	// Location is only present on the flat SymbolInformation shape, which older
	// servers return instead of a hierarchy.
	Location      *lspLocation `json:"location"`
	ContainerName string       `json:"containerName"`
}

// parseLSPLocations accepts all three shapes a definition response may take:
// a single Location, an array of Location, or an array of LocationLink.
func parseLSPLocations(raw json.RawMessage) ([]lspLocation, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var elements []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &elements); err != nil {
			return nil, err
		}
		locations := make([]lspLocation, 0, len(elements))
		for _, element := range elements {
			if location, ok := parseLSPLocationValue(element); ok {
				locations = append(locations, location)
			}
		}
		return locations, nil
	}
	if location, ok := parseLSPLocationValue(json.RawMessage(trimmed)); ok {
		return []lspLocation{location}, nil
	}
	return nil, errors.New("unrecognized location payload")
}

func parseLSPLocationValue(raw json.RawMessage) (lspLocation, bool) {
	var location lspLocation
	if err := json.Unmarshal(raw, &location); err == nil && strings.TrimSpace(location.URI) != "" {
		return location, true
	}
	var link lspLocationLink
	if err := json.Unmarshal(raw, &link); err == nil && strings.TrimSpace(link.TargetURI) != "" {
		target := link.TargetSelectionRange
		if target == (lspRange{}) {
			target = link.TargetRange
		}
		return lspLocation{URI: link.TargetURI, Range: target}, true
	}
	return lspLocation{}, false
}

// renderHoverContents flattens every legal Hover.contents shape: MarkupContent,
// a bare string, a {language,value} pair, or an array of those.
func renderHoverContents(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal([]byte(trimmed), &text); err == nil {
		return strings.TrimSpace(text)
	}
	var object struct {
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal([]byte(trimmed), &object); err == nil && object.Value != "" {
		return strings.TrimSpace(object.Value)
	}
	var elements []json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &elements); err == nil {
		parts := make([]string, 0, len(elements))
		for _, element := range elements {
			if part := renderHoverContents(element); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

var lspSymbolKinds = map[int]string{
	1: "File", 2: "Module", 3: "Namespace", 4: "Package", 5: "Class", 6: "Method",
	7: "Property", 8: "Field", 9: "Constructor", 10: "Enum", 11: "Interface",
	12: "Function", 13: "Variable", 14: "Constant", 15: "String", 16: "Number",
	17: "Boolean", 18: "Array", 19: "Object", 20: "Key", 21: "Null",
	22: "EnumMember", 23: "Struct", 24: "Event", 25: "Operator", 26: "TypeParameter",
}

func lspSymbolKindName(kind int) string {
	if name, ok := lspSymbolKinds[kind]; ok {
		return name
	}
	return "Symbol"
}

// --- bounded output ---------------------------------------------------------

type lspOutput struct {
	builder   strings.Builder
	lines     int
	maxLines  int
	maxBytes  int
	truncated bool
}

func newLSPOutput(maxLines int) *lspOutput {
	return &lspOutput{maxLines: maxLines, maxBytes: lspOutputMaxBytes}
}

// add reports whether the caller should keep producing lines.
func (o *lspOutput) add(line string) bool {
	if o.truncated {
		return false
	}
	if o.maxLines > 0 && o.lines >= o.maxLines {
		o.truncated = true
		return false
	}
	if o.builder.Len()+len(line)+1 > o.maxBytes {
		o.truncated = true
		return false
	}
	if o.lines > 0 {
		o.builder.WriteByte('\n')
	}
	o.builder.WriteString(line)
	o.lines++
	return true
}

func (o *lspOutput) String() string {
	text := o.builder.String()
	if o.truncated {
		if text != "" {
			text += "\n"
		}
		text += "...[truncated]"
	}
	return text
}

// --- tool -------------------------------------------------------------------

type SymbolsTool struct{}

type symbolsInput struct {
	Action             string `json:"action" jsonschema:"enum=definition|references|hover|document_symbols|workspace_symbols" desc:"definition jumps to where the symbol under the cursor is declared. references lists every use of it. hover returns its type and documentation. document_symbols outlines one file. workspace_symbols searches the whole workspace by name."`
	FilePath           string `json:"file_path,omitempty" desc:"File containing the cursor position, absolute or relative to the working directory. Required for definition, references, hover, and document_symbols; ignored for workspace_symbols."`
	Line               int    `json:"line,omitempty" jsonschema:"minimum=1" desc:"1-based line of the symbol, matching the line numbers Read and Grep report. Required for definition, references, and hover."`
	Character          int    `json:"character,omitempty" jsonschema:"minimum=1" desc:"1-based column of the symbol on that line, counted in UTF-16 code units as the language server does. Point at any character inside the identifier. Required for definition, references, and hover."`
	Query              string `json:"query,omitempty" jsonschema:"minLength=1,maxLength=200" desc:"Symbol name or fragment to search for across the workspace. Required for workspace_symbols and rejected for the other actions."`
	IncludeDeclaration bool   `json:"include_declaration,omitempty" desc:"For references, include the declaration itself alongside the uses. Defaults to false."`
	Limit              int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=500" desc:"Maximum result lines to return. Defaults to 200. Output is also capped by total size."`
}

func (SymbolsTool) Name() string { return "Symbols" }
func (SymbolsTool) Description() string {
	return "Navigate code by symbol instead of by text: find where a symbol is defined, list every reference to it, show its type and documentation, outline a file, or search the workspace by symbol name. " +
		"Currently supports Go only, via the gopls language server, which must be installed and on PATH. Prefer this over Grep whenever the question is about a symbol rather than a string."
}
func (SymbolsTool) Schema() any { return symbolsInput{} }

// Risk is RiskExec, not RiskRead: answering a query spawns a local language
// server process, so it belongs behind the same gate as other process launches.
func (SymbolsTool) Risk(json.RawMessage) Risk { return RiskExec }

type symbolsRequest struct {
	action             string
	path               string
	uri                string
	position           lspPosition
	query              string
	includeDeclaration bool
	limit              int
}

// parseSymbolsRequest validates input and resolves paths before anything is
// started, so a bad call never pays for a language server launch.
func parseSymbolsRequest(input symbolsInput, cwd string) (symbolsRequest, error) {
	request := symbolsRequest{
		action:             strings.TrimSpace(input.Action),
		query:              strings.TrimSpace(input.Query),
		includeDeclaration: input.IncludeDeclaration,
		limit:              input.Limit,
	}
	if request.limit <= 0 || request.limit > 500 {
		request.limit = lspDefaultMaxResult
	}
	needsPosition := false
	switch request.action {
	case "definition", "references", "hover":
		needsPosition = true
	case "document_symbols":
	case "workspace_symbols":
		if request.query == "" {
			return symbolsRequest{}, errors.New("query is required for workspace_symbols")
		}
		if strings.TrimSpace(input.FilePath) != "" {
			return symbolsRequest{}, errors.New("file_path is not used by workspace_symbols")
		}
		return request, nil
	case "":
		return symbolsRequest{}, errors.New("action is required: definition, references, hover, document_symbols, or workspace_symbols")
	default:
		return symbolsRequest{}, fmt.Errorf("unknown action %q: use definition, references, hover, document_symbols, or workspace_symbols", request.action)
	}
	if request.query != "" {
		return symbolsRequest{}, fmt.Errorf("query is only used by workspace_symbols, not %s", request.action)
	}
	if strings.TrimSpace(input.FilePath) == "" {
		return symbolsRequest{}, fmt.Errorf("file_path is required for %s", request.action)
	}
	path, err := resolveInCWD(cwd, input.FilePath)
	if err != nil {
		return symbolsRequest{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return symbolsRequest{}, err
	}
	if info.IsDir() {
		return symbolsRequest{}, fmt.Errorf("file_path is a directory: %s", input.FilePath)
	}
	request.path = path
	request.uri = lspURIFromPath(path)
	if !needsPosition {
		if input.Line != 0 || input.Character != 0 {
			return symbolsRequest{}, fmt.Errorf("line and character are not used by %s", request.action)
		}
		return request, nil
	}
	if input.Line < 1 || input.Character < 1 {
		return symbolsRequest{}, fmt.Errorf("line and character are required for %s and are 1-based", request.action)
	}
	// The protocol is 0-based; the tool surface is 1-based to match Read and Grep.
	request.position = lspPosition{Line: input.Line - 1, Character: input.Character - 1}
	return request, nil
}

func (SymbolsTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input symbolsInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(env.CWD) == "" {
		return Result{Output: "a working directory is required for symbol navigation", IsError: true}, nil
	}
	request, err := parseSymbolsRequest(input, env.CWD)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	session, err := startGoplsSession(ctx, env.CWD)
	if err != nil {
		if errors.Is(err, errLSPServerNotFound) {
			return Result{Output: goplsInstallHint, IsError: true, Meta: map[string]any{"serverAvailable": false}}, nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{Output: "the Go language server could not be started: " + err.Error(), IsError: true, Meta: map[string]any{"serverAvailable": false}}, nil
	}
	defer session.close()
	return runSymbolsRequest(ctx, session, request)
}

// runSymbolsRequest is the seam the tests drive: it needs only a session, so a
// fake server on an in-memory pipe exercises every action end to end.
func runSymbolsRequest(ctx context.Context, session *lspSession, request symbolsRequest) (Result, error) {
	// Check cancellation before issuing anything. A cancelled context means the
	// agent loop is already stopping, so there is no point starting a request —
	// and without this the outcome depends on whether the server's reply or the
	// cancellation wins a select, which is nondeterministic under load.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	switch request.action {
	case "definition":
		return symbolsLocationResult(ctx, session, request, "textDocument/definition", nil)
	case "references":
		extra := map[string]any{"context": map[string]any{"includeDeclaration": request.includeDeclaration}}
		return symbolsLocationResult(ctx, session, request, "textDocument/references", extra)
	case "hover":
		return symbolsHoverResult(ctx, session, request)
	case "document_symbols":
		return symbolsDocumentResult(ctx, session, request)
	case "workspace_symbols":
		return symbolsWorkspaceResult(ctx, session, request)
	default:
		return Result{Output: fmt.Sprintf("unknown action %q", request.action), IsError: true}, nil
	}
}

func symbolsPositionParams(request symbolsRequest, extra map[string]any) map[string]any {
	params := map[string]any{
		"textDocument": map[string]any{"uri": request.uri},
		"position":     map[string]any{"line": request.position.Line, "character": request.position.Character},
	}
	for key, value := range extra {
		params[key] = value
	}
	return params
}

func symbolsLocationResult(ctx context.Context, session *lspSession, request symbolsRequest, method string, extra map[string]any) (Result, error) {
	if err := session.openDocument(request.path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	raw, err := session.request(ctx, method, symbolsPositionParams(request, extra))
	if err != nil {
		return symbolsRequestError(ctx, err)
	}
	locations, err := parseLSPLocations(raw)
	if err != nil {
		return Result{Output: "the language server returned an unreadable response: " + err.Error(), IsError: true}, nil
	}
	if len(locations) == 0 {
		label := "definition"
		if request.action == "references" {
			label = "reference"
		}
		return Result{
			Output: fmt.Sprintf("No %s found at %s:%d:%d.", label, lspDisplayPath(session.cwd, request.path), request.position.Line+1, request.position.Character+1),
			Meta:   map[string]any{"action": request.action, "count": 0, "truncated": false},
		}, nil
	}
	output := newLSPOutput(request.limit)
	for _, location := range locations {
		path, ok := lspPathFromURI(location.URI)
		if !ok {
			continue
		}
		line := fmt.Sprintf("%s:%d:%d", lspDisplayPath(session.cwd, path), location.Range.Start.Line+1, location.Range.Start.Character+1)
		if !output.add(line) {
			break
		}
	}
	return Result{
		Output: output.String(),
		Meta:   map[string]any{"action": request.action, "count": len(locations), "truncated": output.truncated},
	}, nil
}

func symbolsHoverResult(ctx context.Context, session *lspSession, request symbolsRequest) (Result, error) {
	if err := session.openDocument(request.path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	raw, err := session.request(ctx, "textDocument/hover", symbolsPositionParams(request, nil))
	if err != nil {
		return symbolsRequestError(ctx, err)
	}
	var hover struct {
		Contents json.RawMessage `json:"contents"`
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &hover); err != nil {
			return Result{Output: "the language server returned an unreadable hover response: " + err.Error(), IsError: true}, nil
		}
	}
	text := renderHoverContents(hover.Contents)
	if text == "" {
		return Result{
			Output: fmt.Sprintf("No hover information at %s:%d:%d.", lspDisplayPath(session.cwd, request.path), request.position.Line+1, request.position.Character+1),
			Meta:   map[string]any{"action": request.action, "count": 0, "truncated": false},
		}, nil
	}
	bounded, cut := truncate(text, lspOutputMaxBytes)
	return Result{Output: bounded, Meta: map[string]any{"action": request.action, "count": 1, "truncated": cut}}, nil
}

func symbolsDocumentResult(ctx context.Context, session *lspSession, request symbolsRequest) (Result, error) {
	if err := session.openDocument(request.path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	raw, err := session.request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": request.uri},
	})
	if err != nil {
		return symbolsRequestError(ctx, err)
	}
	var symbols []lspDocumentSymbol
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &symbols); err != nil {
			return Result{Output: "the language server returned an unreadable symbol list: " + err.Error(), IsError: true}, nil
		}
	}
	if len(symbols) == 0 {
		return Result{
			Output: fmt.Sprintf("No symbols in %s.", lspDisplayPath(session.cwd, request.path)),
			Meta:   map[string]any{"action": request.action, "count": 0, "truncated": false},
		}, nil
	}
	output := newLSPOutput(request.limit)
	count := writeDocumentSymbols(output, symbols, 0)
	return Result{
		Output: output.String(),
		Meta:   map[string]any{"action": request.action, "count": count, "truncated": output.truncated},
	}, nil
}

// writeDocumentSymbols renders both response shapes: the hierarchical
// DocumentSymbol tree and the flat SymbolInformation list, which carries its
// position in Location instead of Range.
func writeDocumentSymbols(output *lspOutput, symbols []lspDocumentSymbol, depth int) int {
	count := 0
	for _, symbol := range symbols {
		position := symbol.SelectionRange.Start
		if position == (lspPosition{}) {
			position = symbol.Range.Start
		}
		if symbol.Location != nil {
			position = symbol.Location.Range.Start
		}
		line := fmt.Sprintf("%s%s %s", strings.Repeat("  ", depth), lspSymbolKindName(symbol.Kind), symbol.Name)
		if detail := strings.TrimSpace(symbol.Detail); detail != "" {
			line += " " + detail
		}
		line += fmt.Sprintf("  [%d:%d]", position.Line+1, position.Character+1)
		if !output.add(line) {
			return count
		}
		count++
		if len(symbol.Children) > 0 {
			count += writeDocumentSymbols(output, symbol.Children, depth+1)
			if output.truncated {
				return count
			}
		}
	}
	return count
}

func symbolsWorkspaceResult(ctx context.Context, session *lspSession, request symbolsRequest) (Result, error) {
	raw, err := session.request(ctx, "workspace/symbol", map[string]any{"query": request.query})
	if err != nil {
		return symbolsRequestError(ctx, err)
	}
	var symbols []lspDocumentSymbol
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &symbols); err != nil {
			return Result{Output: "the language server returned an unreadable symbol list: " + err.Error(), IsError: true}, nil
		}
	}
	if len(symbols) == 0 {
		return Result{
			Output: fmt.Sprintf("No workspace symbols match %q.", request.query),
			Meta:   map[string]any{"action": request.action, "count": 0, "truncated": false},
		}, nil
	}
	output := newLSPOutput(request.limit)
	for _, symbol := range symbols {
		name := symbol.Name
		if container := strings.TrimSpace(symbol.ContainerName); container != "" {
			name = container + "." + name
		}
		line := fmt.Sprintf("%s %s", lspSymbolKindName(symbol.Kind), name)
		if symbol.Location != nil {
			if path, ok := lspPathFromURI(symbol.Location.URI); ok {
				line += fmt.Sprintf("  %s:%d:%d", lspDisplayPath(session.cwd, path), symbol.Location.Range.Start.Line+1, symbol.Location.Range.Start.Character+1)
			}
		}
		if !output.add(line) {
			break
		}
	}
	return Result{
		Output: output.String(),
		Meta:   map[string]any{"action": request.action, "count": len(symbols), "truncated": output.truncated},
	}, nil
}

// symbolsRequestError separates a caller cancellation, which the agent loop
// must see as an error return, from a server-side failure, which is a normal
// tool-level failure the model can react to.
func symbolsRequestError(ctx context.Context, err error) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Result{Output: "the language server did not answer in time; it may still be indexing this workspace", IsError: true}, nil
	}
	return Result{Output: err.Error(), IsError: true}, nil
}
