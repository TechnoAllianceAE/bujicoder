package lsp

import (
	"fmt"
	"sync"
)

// Manager manages LSP client connections, one per language server.
// It lazily starts servers on first file edit of each language.
type Manager struct {
	clients map[string]*Client // keyed by server command (e.g. "gopls")
	rootDir string
	mu      sync.Mutex
}

// NewManager creates a new LSP manager for the given project root.
func NewManager(rootDir string) *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		rootDir: rootDir,
	}
}

// Diagnose runs LSP diagnostics on a file after it has been written.
// Returns nil silently if no LSP server is available for the file type (graceful degradation).
// Only returns error-severity diagnostics.
func (m *Manager) Diagnose(filePath, content string) []Diagnostic {
	cfg, ok := DetectServer(filePath)
	if !ok {
		return nil // no LSP available for this language
	}

	m.mu.Lock()
	client, exists := m.clients[cfg.Command]
	if !exists {
		var err error
		client, err = Start(cfg, m.rootDir)
		if err != nil {
			m.mu.Unlock()
			return nil // silently degrade
		}
		m.clients[cfg.Command] = client
	}
	m.mu.Unlock()

	return client.DiagnoseFile(filePath, content)
}

// CloseAll shuts down all running language servers.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, client := range m.clients {
		client.Close()
	}
	m.clients = make(map[string]*Client)
}

// FormatDiagnostics formats diagnostics for inclusion in tool results.
// Returns empty string if no errors. Caps at maxErrors to avoid flooding the LLM context.
func FormatDiagnostics(diags []Diagnostic, maxErrors int) string {
	if len(diags) == 0 {
		return ""
	}

	if maxErrors <= 0 {
		maxErrors = 10
	}

	result := "\n\nSyntax errors detected after edit:\n"
	for i, d := range diags {
		if i >= maxErrors {
			result += fmt.Sprintf("  ... and %d more errors\n", len(diags)-maxErrors)
			break
		}
		result += fmt.Sprintf("  %s:%d:%d: %s\n", d.File, d.Line, d.Column, d.Message)
	}
	return result
}
