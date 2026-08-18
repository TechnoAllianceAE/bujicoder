package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a language server: TestMain checks helperModeEnv
// and, when set, speaks a minimal subset of LSP over stdio instead of running
// tests. That gives the client a real subprocess to handshake with and kill.
const (
	helperModeEnv    = "BUJI_LSP_TEST_MODE"
	helperPIDFileEnv = "BUJI_LSP_TEST_CHILD_PID_FILE"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperModeEnv) {
	case "server":
		helperServe(false)
		return
	case "spawner":
		helperServe(true)
		return
	case "exit":
		// A server that dies immediately after being spawned.
		return
	}
	os.Exit(m.Run())
}

// helperServe implements just enough of LSP to answer initialize/shutdown and
// publish one error diagnostic on didOpen/didChange.
func helperServe(spawnChild bool) {
	if spawnChild {
		child := exec.CommandContext(context.Background(), "sleep", "300")
		if err := child.Start(); err == nil {
			if p := os.Getenv(helperPIDFileEnv); p != "" {
				_ = os.WriteFile(p, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			}
		}
	}

	in := bufio.NewReader(os.Stdin)
	for {
		body, err := helperReadFrame(in)
		if err != nil {
			return
		}
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
			Params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			return
		}
		switch msg.Method {
		case "initialize", "shutdown":
			if msg.ID != nil {
				helperWrite(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"helper":true}}`, *msg.ID))
			}
		case "textDocument/didOpen", "textDocument/didChange":
			helperWrite(fmt.Sprintf(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":%q,"diagnostics":[{"range":{"start":{"line":4,"character":2},"end":{"line":4,"character":6}},"severity":1,"message":"boom","source":"helper"},{"range":{"start":{"line":9,"character":0},"end":{"line":9,"character":1}},"severity":2,"message":"just a warning","source":"helper"}]}}`, msg.Params.TextDocument.URI))
		case "exit":
			return
		}
	}
}

func helperReadFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			length, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
			if err != nil {
				return nil, err
			}
		}
	}
	if length <= 0 {
		return nil, fmt.Errorf("bad length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func helperWrite(payload string) {
	fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
}

func helperServerConfig(t *testing.T, mode string) *ServerConfig {
	t.Helper()
	t.Setenv(helperModeEnv, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return &ServerConfig{Command: exe}
}

// nopWriteCloser lets tests drive a Client without a real process.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// newTestClient wires a Client to in-memory pipes. serverOut is what the fake
// server writes to the client.
func newTestClient(serverOut io.Reader) *Client {
	return &Client{
		stdin:     nopWriteCloser{io.Discard},
		stdout:    bufio.NewReader(serverOut),
		openFiles: make(map[string]int),
		pending:   make(map[int]chan json.RawMessage),
		diagCh:    make(chan publishDiagnosticsParams, 8),
	}
}

func TestReadMessageRejectsMalformedFraming(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		want  string
	}{
		{
			name:  "negative content length",
			frame: "Content-Length: -1\r\n\r\n",
			want:  "missing Content-Length",
		},
		{
			name:  "absurd content length",
			frame: "Content-Length: 2147483647\r\n\r\n",
			want:  "exceeds",
		},
		{
			name:  "non numeric content length",
			frame: "Content-Length: abc\r\n\r\n",
			want:  "invalid Content-Length",
		},
		{
			name:  "no content length header",
			frame: "X-Other: 1\r\n\r\n",
			want:  "missing Content-Length",
		},
		{
			name:  "zero length body",
			frame: "Content-Length: 0\r\n\r\n",
			want:  "empty message body",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(strings.NewReader(tc.frame))
			_, _, err := c.readMessage()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestReadMessageDecodesValidFrame(t *testing.T) {
	payload := `{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///x"}}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
	c := newTestClient(strings.NewReader(frame))

	msg, raw, err := c.readMessage()
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if msg.Method != "textDocument/publishDiagnostics" {
		t.Fatalf("method = %q", msg.Method)
	}
	if _, ok := msg.Params.(json.RawMessage); !ok {
		t.Fatalf("params were not preserved as raw JSON: %T", msg.Params)
	}
	if string(raw) != payload {
		t.Fatalf("raw body = %q", raw)
	}
}

func TestSendRequestFailsFastWhenServerDies(t *testing.T) {
	pr, pw := io.Pipe()
	c := newTestClient(pr)
	go c.readLoop()

	// The server exits without answering.
	if err := pw.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	start := time.Now()
	_, err := c.sendRequest("initialize", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a request to a dead server must fail, not report success")
	}
	if elapsed >= requestTimeout {
		t.Fatalf("sendRequest waited %v; a dead server should be detected immediately", elapsed)
	}
}

func TestSendRequestRoutesResponseByID(t *testing.T) {
	pr, pw := io.Pipe()
	c := newTestClient(pr)
	go c.readLoop()

	go func() {
		// Answer request id 1 (the first id the client allocates), after an
		// unrelated notification and an unmatched response.
		notif := `{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":3}}`
		other := `{"jsonrpc":"2.0","id":99,"result":{"ignored":true}}`
		reply := `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"real":true}}}`
		for _, payload := range []string{notif, other, reply} {
			fmt.Fprintf(pw, "Content-Length: %d\r\n\r\n%s", len(payload), payload)
		}
	}()

	raw, err := c.sendRequest("initialize", nil)
	if err != nil {
		t.Fatalf("sendRequest: %v", err)
	}
	if !strings.Contains(string(raw), `"real":true`) {
		t.Fatalf("response body not routed to the caller: %q", raw)
	}
	_ = pw.Close()
}

func TestStartRejectsServerThatDiesImmediately(t *testing.T) {
	cfg := helperServerConfig(t, "exit")

	c, err := Start(cfg, t.TempDir())
	if err == nil {
		c.Close()
		t.Fatal("Start must fail when the language server exits before the handshake completes")
	}
}

func TestStartAndDiagnoseAgainstHelperServer(t *testing.T) {
	cfg := helperServerConfig(t, "server")

	c, err := Start(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	path := t.TempDir() + "/main.go"
	diags := c.DiagnoseFile(path, "package main\n")
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1 (errors only): %+v", len(diags), diags)
	}
	if diags[0].Line != 5 || diags[0].Column != 3 || diags[0].Message != "boom" {
		t.Fatalf("unexpected diagnostic: %+v", diags[0])
	}
	if diags[0].File != path {
		t.Fatalf("file = %q, want %q", diags[0].File, path)
	}

	// A second diagnose on the same file goes through didChange and must still
	// resolve.
	if got := c.DiagnoseFile(path, "package main\n\n"); len(got) != 1 {
		t.Fatalf("second diagnose returned %d diagnostics", len(got))
	}
}

func TestCloseIsIdempotentAndReapsTheServer(t *testing.T) {
	cfg := helperServerConfig(t, "server")

	c, err := Start(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := c.cmd.Process.Pid

	c.Close()
	c.Close() // must not panic, double-Wait, or block

	if c.cmd.ProcessState == nil {
		t.Fatalf("process %d was never reaped", pid)
	}
}

func TestDiagnoseFileReturnsNilWhenServerIsGone(t *testing.T) {
	cfg := helperServerConfig(t, "server")

	c, err := Start(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c.Close()

	start := time.Now()
	if diags := c.DiagnoseFile(t.TempDir()+"/main.go", "package main\n"); diags != nil {
		t.Fatalf("expected nil diagnostics from a dead server, got %+v", diags)
	}
	// A broken pipe must be detected on the write, not by burning the full
	// diagnostics timeout on every subsequent edit.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("DiagnoseFile blocked for %v against a dead server", elapsed)
	}
}
