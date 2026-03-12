// Package codeintel provides multi-language code intelligence: symbol extraction,
// project indexing, and structured code navigation. It uses go/ast for Go and
// regex-based extraction for Python, TypeScript/JavaScript, and Rust — avoiding
// CGo dependencies while providing rich code understanding.
package codeintel

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Symbol represents a code symbol (function, class, method, type, variable).
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`       // "function", "class", "method", "variable", "type", "interface"
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Signature string `json:"signature"` // first line of the symbol definition
}

// languageExtractor extracts symbols from source code of a specific language.
type languageExtractor func(content string) []Symbol

// Parser extracts symbols from source code across multiple languages.
type Parser struct {
	extractors map[string]languageExtractor // keyed by file extension
}

// NewParser creates a Parser with all supported language extractors registered.
func NewParser() *Parser {
	p := &Parser{
		extractors: make(map[string]languageExtractor),
	}
	// Go: uses go/ast for accurate parsing
	p.extractors[".go"] = extractGoSymbols
	// Python: regex-based extraction
	p.extractors[".py"] = extractPythonSymbols
	// TypeScript / JavaScript: regex-based extraction
	p.extractors[".ts"] = extractTSSymbols
	p.extractors[".tsx"] = extractTSSymbols
	p.extractors[".js"] = extractTSSymbols
	p.extractors[".jsx"] = extractTSSymbols
	// Rust: regex-based extraction
	p.extractors[".rs"] = extractRustSymbols
	return p
}

// SupportedExtensions returns the list of file extensions this parser handles.
func (p *Parser) SupportedExtensions() []string {
	exts := make([]string, 0, len(p.extractors))
	for ext := range p.extractors {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// IsSupported returns true if the given file path has a supported extension.
func (p *Parser) IsSupported(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	_, ok := p.extractors[ext]
	return ok
}

// ExtractSymbols parses source code and returns all top-level symbols.
func (p *Parser) ExtractSymbols(filePath string, content []byte) []Symbol {
	ext := strings.ToLower(filepath.Ext(filePath))
	extractor, ok := p.extractors[ext]
	if !ok {
		return nil
	}
	return extractor(string(content))
}

// ExtractSymbolsFromFile reads a file and extracts symbols.
func (p *Parser) ExtractSymbolsFromFile(filePath string) ([]Symbol, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return p.ExtractSymbols(filePath, data), nil
}

// FormatSymbols returns a human-readable representation of symbols.
func FormatSymbols(symbols []Symbol) string {
	if len(symbols) == 0 {
		return "No symbols found."
	}
	var sb strings.Builder
	for _, sym := range symbols {
		sb.WriteString(fmt.Sprintf("%s %s (L%d-%d): %s\n",
			sym.Kind, sym.Name, sym.StartLine, sym.EndLine, sym.Signature))
	}
	return sb.String()
}

// FormatSymbolsCompact returns a compact one-line-per-file summary.
func FormatSymbolsCompact(symbols []Symbol) string {
	if len(symbols) == 0 {
		return ""
	}
	names := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		prefix := ""
		switch sym.Kind {
		case "function":
			prefix = "func "
		case "method":
			prefix = "method "
		case "class":
			prefix = "class "
		case "type":
			prefix = "type "
		case "interface":
			prefix = "interface "
		}
		names = append(names, prefix+sym.Name+"()")
	}
	return strings.Join(names, ", ")
}
