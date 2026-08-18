package codeintel

// extractPythonSymbols extracts Python symbols using regex patterns.
func extractPythonSymbols(content string) []Symbol {
	return extractWithPatterns(content, pythonPatterns)
}

var pythonPatterns = []symbolPattern{
	pat("class", `^class\s+(\w+)\s*[\(:]`),
	pat("function", `^def\s+(\w+)\s*\(`),
	pat("method", `^\s{4}def\s+(\w+)\s*\(\s*self`),
	pat("function", `^async\s+def\s+(\w+)\s*\(`),
	pat("variable", `^(\w+)\s*:\s*\w+\s*=`),
	pat("variable", `^([A-Z_][A-Z0-9_]+)\s*=`),
}
