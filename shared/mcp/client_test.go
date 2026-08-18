package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// The test binary doubles as an MCP server: TestMain inspects helperModeEnv and,
// when set, serves MCP over stdio instead of running tests. This gives the
// manager a real subprocess to spawn, handshake with, and kill.
const (
	helperModeEnv    = "BUJI_MCP_TEST_MODE"
	helperMarkerEnv  = "BUJI_MCP_TEST_MARKER"
	helperChildPIDVa = "BUJI_MCP_TEST_CHILD_PID_FILE"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperModeEnv) {
	case "server":
		helperServe(false)
		return
	case "spawner":
		helperServe(true)
		return
	case "hang":
		// A server that never speaks the protocol: drain stdin and stay alive
		// until the parent closes it.
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	os.Exit(m.Run())
}

// helperServe runs a minimal MCP server over stdio. If spawnChild is true it
// first starts a long-lived grandchild process (the shape of `npx` wrappers)
// and records its pid, so tests can assert the grandchild is reaped too.
func helperServe(spawnChild bool) {
	// Record that a process was actually spawned, so tests can count spawns.
	if marker := os.Getenv(helperMarkerEnv); marker != "" {
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString("x")
			_ = f.Close()
		}
	}

	if spawnChild {
		child := exec.CommandContext(context.Background(), "sleep", "300")
		if err := child.Start(); err == nil {
			if p := os.Getenv(helperChildPIDVa); p != "" {
				_ = os.WriteFile(p, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
			}
		}
	}

	server := gosdk.NewServer(&gosdk.Implementation{Name: "helper", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	gosdk.AddTool(server, &gosdk.Tool{Name: "echo", Description: "echo text"},
		func(_ context.Context, _ *gosdk.CallToolRequest, in echoArgs) (*gosdk.CallToolResult, any, error) {
			return &gosdk.CallToolResult{
				Content: []gosdk.Content{&gosdk.TextContent{Text: "echo:" + in.Text}},
			}, nil, nil
		})
	_ = server.Run(context.Background(), &gosdk.StdioTransport{})
}

// helperConfig returns a ServerConfig that launches the test binary in the
// given helper mode. The mode is passed through the environment, which the
// child inherits.
func helperConfig(t *testing.T, name, mode string) ServerConfig {
	t.Helper()
	t.Setenv(helperModeEnv, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return ServerConfig{Name: name, Command: exe, Lazy: true}
}

func TestStartServerBoundsHandshake(t *testing.T) {
	// A server that never answers initialize must not block forever.
	old := handshakeTimeout
	handshakeTimeout = 750 * time.Millisecond
	defer func() { handshakeTimeout = old }()

	cfg := helperConfig(t, "hung", "hang")

	start := time.Now()
	conn, err := startServer(context.Background(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		conn.close()
		t.Fatal("expected handshake to fail against an unresponsive server")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("handshake took %v; timeout not applied", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("handshake error (acceptable): %v", err)
	}
}

func TestEnsureStartedRetriesAfterFailedStart(t *testing.T) {
	// A failed lazy start must not poison the entry: the next call retries.
	good := helperConfig(t, "srv", "server")
	bad := ServerConfig{Name: "srv", Command: "buji-nonexistent-binary-xyz", Lazy: true}

	m := NewManager([]ServerConfig{good})
	defer m.ShutdownAll()

	if _, err := m.ensureStarted(context.Background(), "srv", bad); err == nil {
		t.Fatal("expected first start to fail")
	}

	conn, err := m.ensureStarted(context.Background(), "srv", good)
	if err != nil {
		t.Fatalf("retry after failed start: %v", err)
	}
	if conn == nil || len(conn.tools) != 1 || conn.tools[0].Name != "echo" {
		t.Fatalf("unexpected tools from helper server: %+v", conn)
	}
}

func TestEnsureStartedSpawnsOnceUnderConcurrency(t *testing.T) {
	marker := t.TempDir() + "/spawns"
	cfg := helperConfig(t, "srv", "server")
	t.Setenv(helperMarkerEnv, marker)

	m := NewManager([]ServerConfig{cfg})
	defer m.ShutdownAll()

	const callers = 8
	var wg sync.WaitGroup
	conns := make([]*serverConn, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conns[i], errs[i] = m.ensureStarted(context.Background(), "srv", cfg)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if conns[i] != conns[0] {
			t.Fatalf("caller %d got a different connection: concurrent starts spawned duplicates", i)
		}
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read spawn marker: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("expected exactly 1 spawned server process, got %d", len(data))
	}
}

func TestLazyCallAfterShutdownDoesNotReuseClosedSession(t *testing.T) {
	cfg := helperConfig(t, "srv", "server")
	m := NewManager([]ServerConfig{cfg})

	if _, err := m.lazyCall(context.Background(), "srv", cfg, "echo", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("lazyCall: %v", err)
	}
	m.ShutdownAll()

	_, err := m.lazyCall(context.Background(), "srv", cfg, "echo", map[string]any{"text": "hi"})
	if err == nil {
		t.Fatal("expected an error after ShutdownAll, got success from a closed session")
	}
	if !strings.Contains(err.Error(), "shut down") {
		t.Fatalf("unexpected error after shutdown: %v", err)
	}
}

func TestLazyCallSucceedsAgainstHelperServer(t *testing.T) {
	cfg := helperConfig(t, "srv", "server")
	m := NewManager([]ServerConfig{cfg})
	defer m.ShutdownAll()

	out, err := m.lazyCall(context.Background(), "srv", cfg, "echo", map[string]any{"text": "pong"})
	if err != nil {
		t.Fatalf("lazyCall: %v", err)
	}
	if out != "echo:pong" {
		t.Fatalf("got %q, want %q", out, "echo:pong")
	}
}

func TestUniqueToolName(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir())
	stub := func(name string) *tools.Tool {
		return &tools.Tool{Name: name, Execute: func(context.Context, json.RawMessage) (string, error) {
			return name, nil
		}}
	}
	registry.Register(stub("taken"))
	registry.Register(stub("srv_both"))
	registry.Register(stub("both"))

	tests := []struct {
		name     string
		tool     string
		wantName string
		wantOK   bool
	}{
		{name: "free name used as-is", tool: "free", wantName: "free", wantOK: true},
		{name: "collision is prefixed with the server name", tool: "taken", wantName: "srv_taken", wantOK: true},
		{name: "double collision is skipped, never overwritten", tool: "both", wantName: "", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := uniqueToolName(registry, "srv", tc.tool)
			if got != tc.wantName || ok != tc.wantOK {
				t.Fatalf("uniqueToolName(%q) = (%q, %v), want (%q, %v)", tc.tool, got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

func TestStartAndRegisterKeepsRemoteNameOnCollision(t *testing.T) {
	cfg := helperConfig(t, "srv", "server")
	cfg.Lazy = false
	registry := tools.NewRegistry(t.TempDir())
	// Occupy the tool name the helper server exposes.
	registry.Register(&tools.Tool{
		Name: "echo",
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "builtin", nil
		},
	})

	m := NewManager([]ServerConfig{cfg})
	defer m.ShutdownAll()
	if err := m.startAndRegister(registry, "srv", cfg); err != nil {
		t.Fatalf("startAndRegister: %v", err)
	}

	builtin, ok := registry.Get("echo")
	if !ok {
		t.Fatal("built-in tool was removed")
	}
	if out, err := builtin.Execute(context.Background(), nil); err != nil || out != "builtin" {
		t.Fatalf("built-in tool was overwritten by the MCP tool: out=%q err=%v", out, err)
	}

	prefixed, ok := registry.Get("srv_echo")
	if !ok {
		t.Fatal("colliding MCP tool was not registered under a prefixed name")
	}
	// The remote call must still use the server's own tool name, not the
	// locally-prefixed one.
	out, err := prefixed.Execute(context.Background(), []byte(`{"text":"x"}`))
	if err != nil {
		t.Fatalf("prefixed tool execute: %v", err)
	}
	if out != "echo:x" {
		t.Fatalf("got %q, want %q", out, "echo:x")
	}
}

func TestRegisterToolsReportsAllFailuresAndKeepsGoing(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir())
	m := NewManager([]ServerConfig{
		{Name: "broken-a", Command: "buji-nonexistent-a"},
		{Name: "broken-b", Command: "buji-nonexistent-b"},
		{Name: "lazy-c", Command: "buji-nonexistent-c", Lazy: true},
	})
	defer m.ShutdownAll()

	err := m.RegisterTools(registry)
	if err == nil {
		t.Fatal("expected errors from both broken servers")
	}
	for _, want := range []string{"broken-a", "broken-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v does not mention %q: a failing server hid the others", err, want)
		}
	}
	// The lazy server after the failures still got its dispatch tool.
	if _, ok := registry.Get("mcp_lazy-c"); !ok {
		t.Fatal("lazy dispatch tool was not registered after an earlier server failed")
	}
}

func TestStatusIsOrderedByName(t *testing.T) {
	m := NewManager([]ServerConfig{
		{Name: "zeta"}, {Name: "alpha"}, {Name: "mid"},
	})
	for range 5 {
		got := m.Status()
		names := make([]string, len(got))
		for i, info := range got {
			names[i] = info.Name
		}
		if strings.Join(names, ",") != "alpha,mid,zeta" {
			t.Fatalf("Status order = %v, want sorted", names)
		}
	}
}

func TestFormatResult(t *testing.T) {
	tests := []struct {
		name   string
		result *gosdk.CallToolResult
		want   string
	}{
		{name: "nil result", result: nil, want: ""},
		{name: "no content", result: &gosdk.CallToolResult{}, want: ""},
		{
			name: "text blocks joined",
			result: &gosdk.CallToolResult{Content: []gosdk.Content{
				&gosdk.TextContent{Text: "a"}, &gosdk.TextContent{Text: "b"},
			}},
			want: "a\nb",
		},
		{
			name: "error prefix",
			result: &gosdk.CallToolResult{IsError: true, Content: []gosdk.Content{
				&gosdk.TextContent{Text: "boom"},
			}},
			want: "Error: boom",
		},
		{
			name: "embedded resource without payload does not panic",
			result: &gosdk.CallToolResult{Content: []gosdk.Content{
				&gosdk.EmbeddedResource{},
			}},
			want: "[Embedded Resource]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatResult(tc.result); got != tc.want {
				t.Fatalf("formatResult() = %q, want %q", got, tc.want)
			}
		})
	}
}
