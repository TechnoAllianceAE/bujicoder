package codeintel

// extractRustSymbols extracts Rust symbols using regex patterns.
func extractRustSymbols(content string) []Symbol {
	return extractWithPatterns(content, rustPatterns)
}

var rustPatterns = []symbolPattern{
	// Struct definitions
	pat("type", `^(?:pub\s+)?struct\s+(\w+)`),
	// Enum definitions
	pat("type", `^(?:pub\s+)?enum\s+(\w+)`),
	// Trait definitions (like interfaces)
	pat("interface", `^(?:pub\s+)?trait\s+(\w+)`),
	// Free functions
	pat("function", `^(?:pub\s+)?(?:async\s+)?fn\s+(\w+)`),
	// Methods in impl blocks
	pat("method", `^\s+(?:pub\s+)?(?:async\s+)?fn\s+(\w+)`),
	// Type aliases
	pat("type", `^(?:pub\s+)?type\s+(\w+)`),
	// Constants
	pat("variable", `^(?:pub\s+)?const\s+(\w+)`),
	// Static variables
	pat("variable", `^(?:pub\s+)?static\s+(?:mut\s+)?(\w+)`),
}
