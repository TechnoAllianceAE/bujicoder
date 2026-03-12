package lsp

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// languageServers maps file extensions to known language server configurations.
var languageServers = map[string]struct {
	command string
	args    []string
	langID  string
}{
	".go":   {"gopls", []string{"serve"}, "go"},
	".ts":   {"typescript-language-server", []string{"--stdio"}, "typescript"},
	".tsx":  {"typescript-language-server", []string{"--stdio"}, "typescriptreact"},
	".js":   {"typescript-language-server", []string{"--stdio"}, "javascript"},
	".jsx":  {"typescript-language-server", []string{"--stdio"}, "javascriptreact"},
	".py":   {"pylsp", nil, "python"},
	".rs":   {"rust-analyzer", nil, "rust"},
	".rb":   {"solargraph", []string{"stdio"}, "ruby"},
	".java": {"jdtls", nil, "java"},
}

// DetectServer returns the language server config for the given file, if available.
func DetectServer(filePath string) (*ServerConfig, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	entry, ok := languageServers[ext]
	if !ok {
		return nil, false
	}

	// Check if the server binary is installed.
	if _, err := exec.LookPath(entry.command); err != nil {
		return nil, false
	}

	return &ServerConfig{
		Command: entry.command,
		Args:    entry.args,
	}, true
}

// LanguageID returns the LSP language identifier for a file extension.
func LanguageID(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if entry, ok := languageServers[ext]; ok {
		return entry.langID
	}
	return "plaintext"
}
