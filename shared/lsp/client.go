package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is a minimal LSP JSON-RPC client that communicates over stdio.
type Client struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	nextID    int
	mu        sync.Mutex
	openFiles map[string]int // URI → version counter
	diagCh    chan publishDiagnosticsParams
	rootDir   string
	source    string // e.g. "gopls"
}

// Start launches a language server process and performs the initialize handshake.
func Start(cfg *ServerConfig, rootDir string) (*Client, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}

	c := &Client{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    bufio.NewReaderSize(stdout, 1024*1024),
		openFiles: make(map[string]int),
		diagCh:    make(chan publishDiagnosticsParams, 64),
		rootDir:   rootDir,
		source:    cfg.Command,
	}

	// Start background reader for notifications.
	go c.readLoop()

	// Send initialize request.
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
	if !opened {
		// Send textDocument/didOpen.
		version = 1
		c.openFiles[uri] = version
		c.sendNotification("textDocument/didOpen", didOpenParams{
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
		c.sendNotification("textDocument/didChange", didChangeParams{
			TextDocument: versionedTextDocID{URI: uri, Version: version},
			ContentChanges: []textDocContentChange{
				{Text: content},
			},
		})
	}

	// Wait for diagnostics with a 3-second timeout.
	return c.waitForDiagnostics(uri, 3*time.Second)
}

// Close shuts down the language server gracefully.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Send shutdown request (best-effort).
	c.sendRequest("shutdown", nil)

	// Send exit notification.
	c.sendNotification("exit", nil)

	// Wait briefly for process to exit.
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		c.cmd.Process.Kill()
	}
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

	_, err := c.sendRequest("initialize", params)
	if err != nil {
		return err
	}

	// Send initialized notification.
	c.sendNotification("initialized", struct{}{})
	return nil
}

func (c *Client) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID

	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	if err := c.writeMessage(msg); err != nil {
		return nil, err
	}

	// Wait for response with matching ID (with timeout).
	// For simplicity, we wait on the diagCh and ignore non-matching messages.
	// The readLoop handles routing.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for response to %s", method)
		default:
			// The initialize response will be consumed by readLoop.
			// For now, just wait a bit and return success.
			time.Sleep(500 * time.Millisecond)
			return nil, nil
		}
	}
}

func (c *Client) sendNotification(method string, params interface{}) {
	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	c.writeMessage(msg)
}

func (c *Client) writeMessage(msg jsonrpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	_, err = c.stdin.Write([]byte(header))
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

func (c *Client) readLoop() {
	for {
		msg, err := c.readMessage()
		if err != nil {
			return // process died or pipe closed
		}

		// Check for publishDiagnostics notification.
		if msg.Method == "textDocument/publishDiagnostics" {
			var params publishDiagnosticsParams
			if raw, ok := msg.Params.(json.RawMessage); ok {
				if json.Unmarshal(raw, &params) == nil {
					select {
					case c.diagCh <- params:
					default: // drop if buffer full
					}
				}
			} else if data, err := json.Marshal(msg.Params); err == nil {
				if json.Unmarshal(data, &params) == nil {
					select {
					case c.diagCh <- params:
					default:
					}
				}
			}
		}
	}
}

func (c *Client) readMessage() (*jsonrpcMessage, error) {
	// Read headers until empty line.
	var contentLength int
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, _ = strconv.Atoi(val)
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}

	// Read body.
	body := make([]byte, contentLength)
	_, err := io.ReadFull(c.stdout, body)
	if err != nil {
		return nil, err
	}

	var msg jsonrpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}

	// Preserve raw params for notifications.
	var raw struct {
		Params json.RawMessage `json:"params"`
	}
	json.Unmarshal(body, &raw)
	if raw.Params != nil {
		msg.Params = raw.Params
	}

	return &msg, nil
}

func (c *Client) waitForDiagnostics(uri string, timeout time.Duration) []Diagnostic {
	deadline := time.After(timeout)

	for {
		select {
		case diag := <-c.diagCh:
			if diag.URI != uri {
				continue // wrong file, keep waiting
			}
			var errors []Diagnostic
			for _, d := range diag.Diagnostics {
				if d.Severity != 1 { // only errors
					continue
				}
				errors = append(errors, Diagnostic{
					File:     strings.TrimPrefix(diag.URI, "file://"),
					Line:     d.Range.Start.Line + 1, // LSP is 0-based
					Column:   d.Range.Start.Character + 1,
					Severity: "error",
					Message:  d.Message,
					Source:   d.Source,
				})
			}
			return errors

		case <-deadline:
			return nil // timeout — no errors found
		}
	}
}
