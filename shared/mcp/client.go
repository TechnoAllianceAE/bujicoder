// Package mcp provides a thin wrapper around the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk) to integrate MCP server tools
// into BujiCoder's tool registry.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

const (
	// terminateTimeout bounds how long the SDK waits for a server to exit after
	// stdin is closed before escalating to SIGTERM/SIGKILL. Shorter than the
	// SDK's 5s default so quitting the TUI is not held hostage by a hung server.
	terminateTimeout = 2 * time.Second
)

// handshakeTimeout bounds MCP server startup (spawn + initialize + tools/list).
// Without it a server that never answers on stdout blocks the caller — and
// therefore CLI startup — forever. It is a var so tests can shorten it.
var handshakeTimeout = 30 * time.Second

// ServerConfig describes how to launch an MCP server process.
type ServerConfig struct {
	Name    string   `yaml:"name"    json:"name"`
	Command string   `yaml:"command" json:"command"`
	Args    []string `yaml:"args"    json:"args"`
	Lazy    bool     `yaml:"lazy"    json:"lazy"`
}

// serverConn tracks a running MCP server connection.
type serverConn struct {
	session *gosdk.ClientSession
	cmd     *exec.Cmd
	cancel  context.CancelFunc // kills the server process
	tools   []*gosdk.Tool
}

// close shuts the session down, then makes sure the process is really gone: the
// SDK only signals the direct child, so wrappers such as `npx` leave their real
// server process (a grandchild) orphaned.
func (c *serverConn) close() {
	_ = c.session.Close()
	c.cancel()
	killProcessGroup(c.cmd)
}

// serverEntry guards lazy startup so only one goroutine starts a given server.
// A failed attempt is not cached: the next call retries instead of the entry
// being poisoned for the lifetime of the process.
type serverEntry struct {
	mu   sync.Mutex // held across the (blocking) start attempt
	conn *serverConn
}

// Manager manages multiple MCP server connections and registers their tools
// into the BujiCoder tool registry.
type Manager struct {
	mu      sync.Mutex
	conns   map[string]*serverConn  // keyed by server name (all live servers)
	entries map[string]*serverEntry // keyed by server name (lazy servers)
	configs map[string]ServerConfig
	closed  bool // set by ShutdownAll; blocks resurrection by in-flight starts
}

// NewManager creates a new MCP manager from a list of server configs.
func NewManager(configs []ServerConfig) *Manager {
	m := &Manager{
		conns:   make(map[string]*serverConn),
		entries: make(map[string]*serverEntry),
		configs: make(map[string]ServerConfig),
	}
	for _, cfg := range configs {
		m.configs[cfg.Name] = cfg
	}
	return m
}

// serverNames returns the configured server names in a stable order so that
// registration (and therefore tool-name collision resolution) is deterministic
// instead of depending on Go's randomized map iteration.
func (m *Manager) serverNames() []string {
	names := make([]string, 0, len(m.configs))
	for name := range m.configs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RegisterTools starts each non-lazy MCP server, discovers its tools,
// and registers them into the given tool registry.
// Lazy servers register tool executors that start the server on first call.
//
// A server that fails to start does not prevent the remaining servers from
// registering; all failures are returned joined together.
func (m *Manager) RegisterTools(registry *tools.Registry) error {
	var errs []error
	for _, name := range m.serverNames() {
		cfg := m.configs[name]
		if cfg.Lazy {
			// For lazy servers, we don't know tool names upfront.
			// Register a single dispatch tool that starts the server on demand.
			m.registerLazyDispatch(registry, name, cfg)
			continue
		}
		if err := m.startAndRegister(registry, name, cfg); err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// startServer spawns an MCP server and completes the handshake and tool
// discovery under a bounded timeout. On any failure the child process is
// terminated so no orphan is left behind.
func startServer(ctx context.Context, cfg ServerConfig) (*serverConn, error) {
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, handshakeTimeout)
	defer cancelHandshake()

	client := gosdk.NewClient(&gosdk.Implementation{
		Name:    "bujicoder",
		Version: "1.0.0",
	}, nil)

	// The server's lifetime is the connection's, not the handshake's or the
	// calling request's: it is torn down by serverConn.close / ShutdownAll.
	lifeCtx, cancelLife := context.WithCancel(context.Background())
	cmd := exec.CommandContext(lifeCtx, cfg.Command, cfg.Args...)
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		// Descendants keep the stdio pipes open, so kill the whole group.
		killProcessGroup(cmd)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = terminateTimeout

	transport := &gosdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: terminateTimeout,
	}

	session, err := client.Connect(handshakeCtx, transport, nil)
	if err != nil {
		// Connect closes the transport (and thus the child) on failure, but a
		// wrapper's grandchildren may survive.
		cancelLife()
		killProcessGroup(cmd)
		return nil, fmt.Errorf("connect: %w", err)
	}

	conn := &serverConn{session: session, cmd: cmd, cancel: cancelLife}

	result, err := session.ListTools(handshakeCtx, &gosdk.ListToolsParams{})
	if err != nil {
		conn.close()
		return nil, fmt.Errorf("list tools: %w", err)
	}
	conn.tools = result.Tools
	return conn, nil
}

// track records a live connection, or refuses it if the manager has already
// been shut down (in which case the connection is closed immediately so the
// child process is not leaked).
func (m *Manager) track(name string, conn *serverConn) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		conn.close()
		return fmt.Errorf("mcp manager is shut down")
	}
	m.conns[name] = conn
	m.mu.Unlock()
	return nil
}

// startAndRegister connects to an MCP server, discovers tools, and registers them.
func (m *Manager) startAndRegister(registry *tools.Registry, name string, cfg ServerConfig) error {
	conn, err := startServer(context.Background(), cfg)
	if err != nil {
		return err
	}
	if err := m.track(name, conn); err != nil {
		return err
	}

	// Register each tool individually, never clobbering an existing tool
	// (built-in or from another MCP server).
	for _, t := range conn.tools {
		localName, ok := uniqueToolName(registry, name, t.Name)
		if !ok {
			continue
		}
		registry.Register(m.wrapTool(name, localName, t))
	}

	return nil
}

// uniqueToolName resolves a tool-name collision deterministically: an
// unclaimed name is used as-is, otherwise the server name is prefixed. If both
// are taken the tool is skipped rather than silently replacing a live tool.
func uniqueToolName(registry *tools.Registry, serverName, toolName string) (string, bool) {
	if _, taken := registry.Get(toolName); !taken {
		return toolName, true
	}
	prefixed := serverName + "_" + toolName
	if _, taken := registry.Get(prefixed); !taken {
		return prefixed, true
	}
	return "", false
}

// registerLazyDispatch registers a dispatch tool for a lazy MCP server.
func (m *Manager) registerLazyDispatch(registry *tools.Registry, name string, cfg ServerConfig) {
	registry.Register(&tools.Tool{
		Name:        fmt.Sprintf("mcp_%s", name),
		Description: fmt.Sprintf("Dispatch a tool call to the %q MCP server. Pass {\"tool\": \"<name>\", \"arguments\": {...}}. The server starts lazily on first call.", name),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var params struct {
				Tool      string         `json:"tool"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			return m.lazyCall(ctx, name, cfg, params.Tool, params.Arguments)
		},
	})
}

// ensureStarted returns the running connection for a lazy server, starting it
// if needed. Concurrent callers are serialized so at most one process is
// spawned, and a failed start is retried by the next caller.
func (m *Manager) ensureStarted(ctx context.Context, name string, cfg ServerConfig) (*serverConn, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("mcp manager is shut down")
	}
	entry, ok := m.entries[name]
	if !ok {
		entry = &serverEntry{}
		m.entries[name] = entry
	}
	m.mu.Unlock()

	// m.mu is deliberately not held here: starting a server blocks on a child
	// process, and holding the manager lock would stall Status/ShutdownAll.
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.conn != nil {
		return entry.conn, nil
	}

	conn, err := startServer(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("lazy start MCP server %q: %w", name, err)
	}
	if err := m.track(name, conn); err != nil {
		return nil, err
	}
	entry.conn = conn
	return conn, nil
}

// lazyCall starts an MCP server if needed, then calls the specified tool.
func (m *Manager) lazyCall(ctx context.Context, name string, cfg ServerConfig, toolName string, args map[string]any) (string, error) {
	conn, err := m.ensureStarted(ctx, name, cfg)
	if err != nil {
		return "", err
	}

	result, err := conn.session.CallTool(ctx, &gosdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("call tool %q on %q: %w", toolName, name, err)
	}

	return formatResult(result), nil
}

// wrapTool wraps an MCP tool as a BujiCoder tools.Tool. localName is the name
// the tool is registered under, which may differ from the remote tool name when
// a collision was resolved.
func (m *Manager) wrapTool(serverName, localName string, t *gosdk.Tool) *tools.Tool {
	// Convert the MCP tool's InputSchema to a map for the LLM.
	var schema map[string]any
	if t.InputSchema != nil {
		raw, err := json.Marshal(t.InputSchema)
		if err == nil {
			_ = json.Unmarshal(raw, &schema)
		}
	}

	remoteName := t.Name
	return &tools.Tool{
		Name:        localName,
		Description: t.Description,
		InputSchema: schema,
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			m.mu.Lock()
			conn, ok := m.conns[serverName]
			m.mu.Unlock()
			if !ok {
				return "", fmt.Errorf("MCP server %q not running", serverName)
			}

			var argsMap map[string]any
			if err := json.Unmarshal(args, &argsMap); err != nil {
				return "", fmt.Errorf("parse tool args: %w", err)
			}

			result, err := conn.session.CallTool(ctx, &gosdk.CallToolParams{
				Name:      remoteName,
				Arguments: argsMap,
			})
			if err != nil {
				return "", err
			}

			return formatResult(result), nil
		},
	}
}

// ServerInfo describes the status of a single MCP server for display purposes.
type ServerInfo struct {
	Name    string
	Command string
	Args    []string
	Lazy    bool
	Running bool
	Tools   []string // tool names discovered from the server
}

// Status returns the status of all configured MCP servers, ordered by name.
func (m *Manager) Status() []ServerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]ServerInfo, 0, len(m.configs))
	for _, name := range m.serverNames() {
		cfg := m.configs[name]
		info := ServerInfo{
			Name:    name,
			Command: cfg.Command,
			Args:    cfg.Args,
			Lazy:    cfg.Lazy,
		}
		if conn, ok := m.conns[name]; ok {
			info.Running = true
			for _, t := range conn.tools {
				info.Tools = append(info.Tools, t.Name)
			}
		}
		infos = append(infos, info)
	}
	return infos
}

// ShutdownAll gracefully shuts down all running MCP server connections.
// Servers are closed concurrently so one unresponsive server cannot serialize
// the shutdown of the others.
func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	m.closed = true
	conns := make([]*serverConn, 0, len(m.conns))
	for name, conn := range m.conns {
		conns = append(conns, conn)
		delete(m.conns, name)
	}
	// Drop lazy entries so a later call cannot reuse a closed session; a fresh
	// entry would be created, but m.closed rejects the start.
	m.entries = make(map[string]*serverEntry)
	m.mu.Unlock()

	// Closing blocks on child processes, so it happens outside the lock.
	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(c *serverConn) {
			defer wg.Done()
			c.close()
		}(conn)
	}
	wg.Wait()
}

// formatResult converts an MCP CallToolResult into a plain text string.
func formatResult(result *gosdk.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, block := range result.Content {
		switch c := block.(type) {
		case *gosdk.TextContent:
			parts = append(parts, c.Text)
		case *gosdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[Image: %s]", c.MIMEType))
		case *gosdk.EmbeddedResource:
			if c.Resource != nil {
				parts = append(parts, fmt.Sprintf("[Embedded Resource: %s]", c.Resource.URI))
			} else {
				parts = append(parts, "[Embedded Resource]")
			}
		default:
			parts = append(parts, fmt.Sprintf("[%T content]", block))
		}
	}
	text := strings.Join(parts, "\n")
	if result.IsError {
		text = "Error: " + text
	}
	return text
}
