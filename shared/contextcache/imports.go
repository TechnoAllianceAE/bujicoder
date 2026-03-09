package contextcache

import (
	"bufio"
	"strings"
)

// extractImports parses import statements from file content based on language.
func extractImports(content, lang string) []string {
	switch lang {
	case "go":
		return extractGoImports(content)
	case "python":
		return extractPythonImports(content)
	case "javascript", "typescript":
		return extractJSImports(content)
	default:
		return nil
	}
}

// extractGoImports parses Go import statements.
func extractGoImports(content string) []string {
	var imports []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	inBlock := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "import (") {
			inBlock = true
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			// Extract the import path from a quoted string.
			imp := extractQuoted(line)
			if imp != "" {
				imports = append(imports, imp)
			}
			continue
		}
		if strings.HasPrefix(line, "import ") {
			imp := extractQuoted(line)
			if imp != "" {
				imports = append(imports, imp)
			}
		}
		// Stop scanning past the package/import header.
		if strings.HasPrefix(line, "func ") || strings.HasPrefix(line, "type ") || strings.HasPrefix(line, "var ") || strings.HasPrefix(line, "const ") {
			break
		}
	}
	return imports
}

// extractPythonImports parses Python import and from...import statements.
func extractPythonImports(content string) []string {
	var imports []string
	scanner := bufio.NewScanner(strings.NewReader(content))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "import ") {
			mod := strings.TrimPrefix(line, "import ")
			mod = strings.SplitN(mod, " ", 2)[0] // strip "as" alias
			mod = strings.TrimRight(mod, ",")
			if mod != "" {
				imports = append(imports, mod)
			}
		} else if strings.HasPrefix(line, "from ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				imports = append(imports, parts[1])
			}
		}
	}
	return imports
}

// extractJSImports parses JavaScript/TypeScript import and require statements.
func extractJSImports(content string) []string {
	var imports []string
	scanner := bufio.NewScanner(strings.NewReader(content))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// import ... from "..."
		if strings.HasPrefix(line, "import ") {
			if idx := strings.Index(line, "from "); idx > 0 {
				spec := strings.TrimSpace(line[idx+5:])
				imp := extractQuoted(spec)
				if imp != "" {
					imports = append(imports, imp)
				}
			}
			continue
		}

		// const x = require("...")
		if strings.Contains(line, "require(") {
			start := strings.Index(line, "require(")
			if start >= 0 {
				rest := line[start+8:]
				imp := extractQuoted(rest)
				if imp != "" {
					imports = append(imports, imp)
				}
			}
		}
	}
	return imports
}

// extractQuoted returns the first quoted string from s (single or double quotes).
func extractQuoted(s string) string {
	for _, q := range []byte{'"', '\''} {
		start := strings.IndexByte(s, q)
		if start < 0 {
			continue
		}
		end := strings.IndexByte(s[start+1:], q)
		if end < 0 {
			continue
		}
		return s[start+1 : start+1+end]
	}
	return ""
}
