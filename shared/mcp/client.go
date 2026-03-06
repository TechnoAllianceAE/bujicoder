// Package mcp provides a thin wrapper around the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk) to integrate MCP server tools
// into BujiCoder's tool registry.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

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
	tools   []*gosdk.Tool
}

// serverEntry guards lazy startup so only one goroutine starts a given server.
type serverEntry struct {
	once sync.Once
	conn *serverConn
	err  error
}

// Manager manages multiple MCP server connections and registers their tools
// into the BujiCoder tool registry.
type Manager struct {
	mu      sync.Mutex
	conns   map[string]*serverConn  // keyed by server name (eager servers)
	entries map[string]*serverEntry // keyed by server name (lazy servers)
	configs map[string]ServerConfig
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

// RegisterTools starts each non-lazy MCP server, discovers its tools,
// and registers them into the given tool registry.
// Lazy servers register tool executors that start the server on first call.
func (m *Manager) RegisterTools(registry *tools.Registry) error {
	for name, cfg := range m.configs {
		if cfg.Lazy {
			// For lazy servers, we don't know tool names upfront.
			// Register a single dispatch tool that starts the server on demand.
			m.registerLazyDispatch(registry, name, cfg)
			continue
		}
		if err := m.startAndRegister(registry, name, cfg); err != nil {
			return fmt.Errorf("mcp server %q: %w", name, err)
		}
	}
	return nil
}

// startAndRegister connects to an MCP server, discovers tools, and registers them.
func (m *Manager) startAndRegister(registry *tools.Registry, name string, cfg ServerConfig) error {
	ctx := context.Background()

	client := gosdk.NewClient(&gosdk.Implementation{
		Name:    "bujicoder",
		Version: "1.0.0",
	}, nil)

	transport := &gosdk.CommandTransport{
		Command: exec.Command(cfg.Command, cfg.Args...),
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect to %q: %w", name, err)
	}

	// Discover tools
	result, err := session.ListTools(ctx, &gosdk.ListToolsParams{})
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("list tools from %q: %w", name, err)
	}

	conn := &serverConn{session: session, tools: result.Tools}
	m.mu.Lock()
	m.conns[name] = conn
	m.mu.Unlock()

	// Register each tool individually
	for _, t := range result.Tools {
		registry.Register(m.wrapTool(name, t))
	}

	return nil
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

// lazyCall starts an MCP server if needed, then calls the specified tool.
// Uses sync.Once per server to prevent duplicate process spawns from concurrent calls.
func (m *Manager) lazyCall(ctx context.Context, name string, cfg ServerConfig, toolName string, args map[string]any) (string, error) {
	// Get or create the entry (under lock), then use Once to start at most once.
	m.mu.Lock()
	entry, ok := m.entries[name]
	if !ok {
		entry = &serverEntry{}
		m.entries[name] = entry
	}
	m.mu.Unlock()

	entry.once.Do(func() {
		client := gosdk.NewClient(&gosdk.Implementation{
			Name:    "bujicoder",
			Version: "1.0.0",
		}, nil)
		transport := &gosdk.CommandTransport{
			Command: exec.Command(cfg.Command, cfg.Args...),
		}
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			entry.err = fmt.Errorf("lazy start MCP server %q: %w", name, err)
			return
		}
		toolsResult, err := session.ListTools(ctx, &gosdk.ListToolsParams{})
		if err != nil {
			_ = session.Close()
			entry.err = fmt.Errorf("list tools from %q: %w", name, err)
			return
		}
		entry.conn = &serverConn{session: session, tools: toolsResult.Tools}
		// Also store in conns so ShutdownAll cleans it up.
		m.mu.Lock()
		m.conns[name] = entry.conn
		m.mu.Unlock()
	})

	if entry.err != nil {
		return "", entry.err
	}

	result, err := entry.conn.session.CallTool(ctx, &gosdk.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("call tool %q on %q: %w", toolName, name, err)
	}

	return formatResult(result), nil
}

// wrapTool wraps an MCP tool as a BujiCoder tools.Tool.
func (m *Manager) wrapTool(serverName string, t *gosdk.Tool) *tools.Tool {
	// Convert the MCP tool's InputSchema to a map for the LLM.
	var schema map[string]any
	if t.InputSchema != nil {
		raw, err := json.Marshal(t.InputSchema)
		if err == nil {
			_ = json.Unmarshal(raw, &schema)
		}
	}

	return &tools.Tool{
		Name:        t.Name,
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
				Name:      t.Name,
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

// Status returns the status of all configured MCP servers.
func (m *Manager) Status() []ServerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	var infos []ServerInfo
	for name, cfg := range m.configs {
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
func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, conn := range m.conns {
		_ = conn.session.Close()
		delete(m.conns, name)
	}
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
