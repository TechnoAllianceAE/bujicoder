// Package lsp provides a minimal LSP client for collecting diagnostics after file edits.
// It only implements the subset of the protocol needed: initialize, textDocument/didOpen,
// textDocument/didChange, and textDocument/publishDiagnostics.
package lsp

// Diagnostic represents a single LSP diagnostic (error, warning, etc.).
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"` // "error", "warning", "info", "hint"
	Message  string `json:"message"`
	Source   string `json:"source"` // e.g., "gopls", "typescript"
}

// ServerConfig holds the command and arguments to launch a language server.
type ServerConfig struct {
	Command string
	Args    []string
}

// --- LSP JSON-RPC types ---

type jsonrpcMessage struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int        `json:"id,omitempty"`
	Method  string      `json:"method,omitempty"`
	Params  interface{} `json:"params,omitempty"`
	Result  interface{} `json:"result,omitempty"`
}

type initializeParams struct {
	ProcessID    int                `json:"processId"`
	RootURI      string             `json:"rootUri"`
	Capabilities clientCapabilities `json:"capabilities"`
}

type clientCapabilities struct {
	TextDocument textDocCapabilities `json:"textDocument"`
}

type textDocCapabilities struct {
	PublishDiagnostics publishDiagCap `json:"publishDiagnostics"`
}

type publishDiagCap struct {
	RelatedInformation bool `json:"relatedInformation"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocID    `json:"textDocument"`
	ContentChanges []textDocContentChange `json:"contentChanges"`
}

type versionedTextDocID struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type textDocContentChange struct {
	Text string `json:"text"`
}

type publishDiagnosticsParams struct {
	URI         string          `json:"uri"`
	Diagnostics []lspDiagnostic `json:"diagnostics"`
}

type lspDiagnostic struct {
	Range    lspRange `json:"range"`
	Severity int      `json:"severity"` // 1=error, 2=warning, 3=info, 4=hint
	Message  string   `json:"message"`
	Source   string   `json:"source"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
