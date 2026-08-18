package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// maxMessageBytes caps the body size accepted from a language server. A
	// buggy or hostile server advertising a huge Content-Length would otherwise
	// make us allocate that much memory in one shot.
	maxMessageBytes = 32 << 20 // 32 MiB

	// requestTimeout bounds how long we wait for a response to a request.
	requestTimeout = 5 * time.Second

	// shutdownTimeout bounds how long Close waits for the server to exit before
	// killing it.
	shutdownTimeout = 2 * time.Second
)

// errServerGone reports that the language server's stdout closed, i.e. the
// process died, so no response will ever arrive.
var errServerGone = errors.New("language server exited")

// Client is a minimal LSP JSON-RPC client that communicates over stdio.
type Client struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc // kills the server process on Close
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu        sync.Mutex // serializes operations (one request/notification at a time)
	nextID    int
	openFiles map[string]int // URI → version counter
	closed    bool

	pendMu  sync.Mutex
	pending map[int]chan json.RawMessage
	readErr error // set once readLoop stops; guarded by pendMu

	diagCh  chan publishDiagnosticsParams
	rootDir string
	source  string // e.g. "gopls"
}

// Start launches a language server process and performs the initialize handshake.
func Start(cfg *ServerConfig, rootDir string) (*Client, error) {
	// The server's lifetime is the client's, not any single request's. Cancelling
	// this context kills the process, which guarantees cleanup even if the
	// graceful shutdown path does not complete.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Stderr = os.Stderr
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}

	c := &Client{
		cmd:       cmd,
		cancel:    cancel,
		stdin:     stdin,
		stdout:    bufio.NewReaderSize(stdout, 1024*1024),
		openFiles: make(map[string]int),
		pending:   make(map[int]chan json.RawMessage),
		diagCh:    make(chan publishDiagnosticsParams, 64),
		rootDir:   rootDir,
		source:    cfg.Command,
	}

	// Start background reader for responses and notifications.
	go c.readLoop()

	// Send initialize request. A server that died on startup fails here instead
	// of being treated as healthy.
	if err := c.initialize(rootDir); err != nil {
		c.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}

	return c, nil
}

// DiagnoseFile opens or updates a file and waits for diagnostics.
// Returns only errors (severity == 1). Returns nil if no LSP errors or on timeout.
func (c *Client) DiagnoseFile(filePath, content string) []Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()

	uri := "file://" + filePath
	langID := LanguageID(filePath)

	version, opened := c.openFiles[uri]
	var err error
	if !opened {
		// Send textDocument/didOpen.
		version = 1
		c.openFiles[uri] = version
		err = c.sendNotification("textDocument/didOpen", didOpenParams{
			TextDocument: textDocumentItem{
				URI:        uri,
				LanguageID: langID,
				Version:    version,
				Text:       content,
			},
		})
	} else {
		// Send textDocument/didChange.
		version++
		c.openFiles[uri] = version
		err = c.sendNotification("textDocument/didChange", didChangeParams{
			TextDocument: versionedTextDocID{URI: uri, Version: version},
			ContentChanges: []textDocContentChange{
				{Text: content},
			},
		})
	}
	if err != nil {
		// The server is gone; waiting for diagnostics would just burn the
		// timeout on every subsequent edit.
		return nil
	}

	// Wait for diagnostics with a 3-second timeout.
	return c.waitForDiagnostics(uri, 3*time.Second)
}

// Close shuts down the language server gracefully. It is safe to call more than
// once.
func (c *Client) Close() {
	// The lock is held for the whole sequence so a concurrent DiagnoseFile
	// cannot interleave frames on stdin or reuse request ids.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true

	// Send shutdown request and exit notification (both best-effort: the server
	// may already be gone).
	_, _ = c.sendRequest("shutdown", nil)
	_ = c.sendNotification("exit", nil)

	// Closing stdin gives servers that ignore "exit" an EOF to act on, and
	// releases the pipe.
	_ = c.stdin.Close()

	// Wait briefly for the process to exit.
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		c.cancel() // SIGKILL the server
		<-done     // reap it, so no zombie is left behind
	}
	// Sweep anything the server spawned; killing the server alone would orphan
	// wrapper children.
	killProcessGroup(c.cmd)
}

// --- Internal methods ---

func (c *Client) initialize(rootDir string) error {
	params := initializeParams{
		ProcessID: os.Getpid(),
		RootURI:   "file://" + rootDir,
		Capabilities: clientCapabilities{
			TextDocument: textDocCapabilities{
				PublishDiagnostics: publishDiagCap{RelatedInformation: true},
			},
		},
	}

	if _, err := c.sendRequest("initialize", params); err != nil {
		return err
	}

	// Send initialized notification.
	return c.sendNotification("initialized", struct{}{})
}

// sendRequest writes a request and waits for the matching response. It fails
// fast if the server has died and gives up after requestTimeout.
func (c *Client) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID

	reply := make(chan json.RawMessage, 1)
	c.pendMu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.pendMu.Unlock()
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	c.pending[id] = reply
	c.pendMu.Unlock()

	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}
	if err := c.writeMessage(msg); err != nil {
		return nil, err
	}

	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()
	select {
	case result, ok := <-reply:
		if !ok {
			// readLoop closed the channel: the server exited.
			c.pendMu.Lock()
			err := c.readErr
			c.pendMu.Unlock()
			if err == nil {
				err = errServerGone
			}
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		return result, nil
	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for response to %s", method)
	}
}

func (c *Client) sendNotification(method string, params interface{}) error {
	return c.writeMessage(jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (c *Client) writeMessage(msg jsonrpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

func (c *Client) readLoop() {
	for {
		msg, raw, err := c.readMessage()
		if err != nil {
			// The server died or sent something unreadable. Release every
			// waiter so requests fail instead of hanging until their timeout.
			c.pendMu.Lock()
			if c.readErr == nil {
				c.readErr = fmt.Errorf("%w: %w", errServerGone, err)
			}
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.pendMu.Unlock()
			return
		}

		// Route responses to their waiting request.
		if msg.Method == "" && msg.ID != nil {
			c.pendMu.Lock()
			ch, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.pendMu.Unlock()
			if ok {
				ch <- raw
			}
			continue
		}

		// Check for publishDiagnostics notification.
		if msg.Method == "textDocument/publishDiagnostics" {
			var params publishDiagnosticsParams
			if data, ok := msg.Params.(json.RawMessage); ok {
				if json.Unmarshal(data, &params) == nil {
					select {
					case c.diagCh <- params:
					default: // drop if buffer full
					}
				}
			}
		}
	}
}

// readMessage reads one framed JSON-RPC message, returning the decoded envelope
// and the raw message body.
func (c *Client) readMessage() (*jsonrpcMessage, json.RawMessage, error) {
	// Read headers until empty line.
	contentLength := -1
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid Content-Length %q: %w", val, err)
			}
			contentLength = n
		}
	}

	// A negative length would panic in make(); an absurd one would exhaust
	// memory. Both are rejected instead.
	if contentLength < 0 {
		return nil, nil, fmt.Errorf("missing Content-Length")
	}
	if contentLength == 0 {
		return nil, nil, fmt.Errorf("empty message body")
	}
	if contentLength > maxMessageBytes {
		return nil, nil, fmt.Errorf("message body of %d bytes exceeds the %d byte limit", contentLength, maxMessageBytes)
	}

	// Read body.
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.stdout, body); err != nil {
		return nil, nil, err
	}

	var msg jsonrpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, nil, err
	}

	// Preserve raw params for notifications.
	var raw struct {
		Params json.RawMessage `json:"params"`
	}
	// body already unmarshalled above, so this cannot fail for a different reason.
	if err := json.Unmarshal(body, &raw); err == nil && raw.Params != nil {
		msg.Params = raw.Params
	}

	return &msg, body, nil
}

func (c *Client) waitForDiagnostics(uri string, timeout time.Duration) []Diagnostic {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case diag := <-c.diagCh:
			if diag.URI != uri {
				continue // wrong file, keep waiting
			}
			var errs []Diagnostic
			for _, d := range diag.Diagnostics {
				if d.Severity != 1 { // only errors
					continue
				}
				errs = append(errs, Diagnostic{
					File:     strings.TrimPrefix(diag.URI, "file://"),
					Line:     d.Range.Start.Line + 1, // LSP is 0-based
					Column:   d.Range.Start.Character + 1,
					Severity: "error",
					Message:  d.Message,
					Source:   d.Source,
				})
			}
			return errs

		case <-timer.C:
			return nil // timeout — no errors found
		}
	}
}
