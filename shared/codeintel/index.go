package codeintel

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// FileSymbols holds the symbols extracted from a single file.
type FileSymbols struct {
	Path    string   `json:"path"`
	Symbols []Symbol `json:"symbols"`
}

// skipDirs are directories to exclude from indexing.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	".next": true, "dist": true, "build": true, ".cache": true,
	".venv": true, "target": true, ".turbo": true, ".terraform": true,
	".yarn": true, ".pnp": true, "coverage": true, ".angular": true,
	".svelte-kit": true, ".parcel-cache": true, ".idea": true, ".vscode": true,
}

// IndexProject scans the project and extracts symbols from supported files.
// If topFiles is provided, only those files are indexed. Otherwise, all
// supported files up to maxFiles are indexed.
func (p *Parser) IndexProject(projectRoot string, topFiles []string) []FileSymbols {
	if len(topFiles) > 0 {
		return p.indexFiles(projectRoot, topFiles)
	}
	return p.indexAll(projectRoot, 100)
}

// indexFiles indexes specific files.
func (p *Parser) indexFiles(projectRoot string, files []string) []FileSymbols {
	var result []FileSymbols
	for _, relPath := range files {
		absPath := relPath
		if !filepath.IsAbs(relPath) {
			absPath = filepath.Join(projectRoot, relPath)
		}
		if !p.IsSupported(absPath) {
			continue
		}
		symbols, err := p.ExtractSymbolsFromFile(absPath)
		if err != nil {
			log.Debug().Err(err).Str("path", absPath).Msg("codeintel: skipping file")
			continue
		}
		if len(symbols) == 0 {
			continue
		}
		rel, err := filepath.Rel(projectRoot, absPath)
		if err != nil || rel == "" {
			rel = relPath
		}
		result = append(result, FileSymbols{Path: rel, Symbols: symbols})
	}
	return result
}

// indexAll walks the project and indexes all supported files.
func (p *Parser) indexAll(projectRoot string, maxFiles int) []FileSymbols {
	var result []FileSymbols
	count := 0

	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry must not abort the scan of the rest of the
			// tree, but it must not vanish silently either.
			log.Debug().Err(err).Str("path", path).Msg("codeintel: skipping unreadable path")
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= maxFiles {
			return filepath.SkipAll
		}
		if !p.IsSupported(path) {
			return nil
		}

		symbols, err := p.ExtractSymbolsFromFile(path)
		if err != nil {
			log.Debug().Err(err).Str("path", path).Msg("codeintel: skipping file")
			return nil
		}
		if len(symbols) == 0 {
			return nil
		}

		rel, relErr := filepath.Rel(projectRoot, path)
		if relErr != nil {
			log.Debug().Err(relErr).Str("path", path).Msg("codeintel: path outside project root")
			return nil
		}
		result = append(result, FileSymbols{Path: rel, Symbols: symbols})
		count++
		return nil
	})

	return result
}

// FormatIndex formats a project symbol index as a compact string suitable for
// inclusion in a system prompt.
func FormatIndex(index []FileSymbols) string {
	if len(index) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Symbol Index\n\n")

	for _, fs := range index {
		names := FormatSymbolsCompact(fs.Symbols)
		if names == "" {
			continue
		}
		fmt.Fprintf(&sb, "%s: %s\n", fs.Path, names)
	}

	return sb.String()
}

// SearchSymbols searches the index for symbols matching a query string.
func SearchSymbols(index []FileSymbols, query string) []Symbol {
	query = strings.ToLower(query)
	var matches []Symbol
	for _, fs := range index {
		for _, sym := range fs.Symbols {
			if strings.Contains(strings.ToLower(sym.Name), query) {
				matches = append(matches, sym)
			}
		}
	}
	return matches
}

// SymbolNames extracts all unique symbol names from an index.
func SymbolNames(index []FileSymbols) []string {
	seen := make(map[string]bool)
	var names []string
	for _, fs := range index {
		for _, sym := range fs.Symbols {
			if !seen[sym.Name] {
				names = append(names, sym.Name)
				seen[sym.Name] = true
			}
		}
	}
	sort.Strings(names)
	return names
}
