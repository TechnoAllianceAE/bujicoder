// Package smartctx provides smart context assembly for the agent runtime.
// Instead of dumping a flat file tree, it ranks files by relevance to the
// current task using keyword matching, git changes, symbol references,
// and import proximity.
package smartctx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileRelevance holds a file's relevance score and the reasons it scored.
type FileRelevance struct {
	Path    string   `json:"path"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
}

// Scoring weights for different relevance signals.
const (
	WeightKeywordMatch = 3.0 // File name or path matches a query keyword
	WeightGitChanged   = 5.0 // File appears in git diff (uncommitted changes)
	WeightRecentlyMod  = 2.0 // File was recently modified (git log)
	WeightSymbolMatch  = 4.0 // File contains symbols referenced in the query
	WeightImportProx   = 1.5 // File is imported by a changed file
	MaxRankedFiles     = 50  // Maximum files to return
)

// skipDirs are directories to exclude from ranking.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	".next": true, "dist": true, "build": true, ".cache": true,
	".venv": true, "target": true, ".turbo": true, ".terraform": true,
	".yarn": true, ".pnp": true, "coverage": true, ".angular": true,
	".svelte-kit": true, ".parcel-cache": true, ".idea": true, ".vscode": true,
}

// RankFiles scores all project files by relevance to the given query and
// optional symbol names. Returns the top N files sorted by score descending.
func RankFiles(projectRoot, query string, symbolNames []string) []FileRelevance {
	if projectRoot == "" {
		return nil
	}

	keywords := ExtractKeywords(query)
	changedFiles := getGitChangedFiles(projectRoot)
	recentFiles := getRecentlyModifiedFiles(projectRoot)

	// Build a lookup of symbol names for fast matching.
	symbolSet := make(map[string]bool, len(symbolNames))
	for _, s := range symbolNames {
		symbolSet[strings.ToLower(s)] = true
	}

	scores := make(map[string]*FileRelevance)

	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(projectRoot, path)
		if err != nil || rel == "." {
			return nil
		}

		fr := &FileRelevance{Path: rel}

		// 1. Keyword match: file name or path contains query keywords.
		for _, kw := range keywords {
			if strings.Contains(strings.ToLower(rel), kw) {
				fr.Score += WeightKeywordMatch
				fr.Reasons = append(fr.Reasons, "name_match:"+kw)
				break // One keyword match is enough
			}
		}

		// 2. Git changed files (uncommitted changes).
		if changedFiles[rel] {
			fr.Score += WeightGitChanged
			fr.Reasons = append(fr.Reasons, "git_changed")
		}

		// 3. Recently modified files.
		if recentFiles[rel] {
			fr.Score += WeightRecentlyMod
			fr.Reasons = append(fr.Reasons, "recently_modified")
		}

		// 4. Symbol match: file base name matches known symbols.
		baseName := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		baseLower := strings.ToLower(baseName)
		if symbolSet[baseLower] {
			fr.Score += WeightSymbolMatch
			fr.Reasons = append(fr.Reasons, "symbol_match:"+baseName)
		}

		// Only include files that scored above zero.
		if fr.Score > 0 {
			scores[rel] = fr
		}

		return nil
	})

	// 5. Import proximity: files imported by changed files get a boost.
	for changedFile := range changedFiles {
		imports := extractImportPaths(projectRoot, changedFile)
		for _, imp := range imports {
			if fr, ok := scores[imp]; ok {
				fr.Score += WeightImportProx
				fr.Reasons = append(fr.Reasons, "import_proximity")
			}
		}
	}

	// Sort by score descending and cap.
	ranked := make([]FileRelevance, 0, len(scores))
	for _, fr := range scores {
		ranked = append(ranked, *fr)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Path < ranked[j].Path
	})

	if len(ranked) > MaxRankedFiles {
		ranked = ranked[:MaxRankedFiles]
	}

	return ranked
}

// FormatRankedFiles returns a formatted string of ranked files for system prompt inclusion.
func FormatRankedFiles(ranked []FileRelevance) string {
	if len(ranked) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Relevant Files (ranked by relevance)\n\n")

	for _, fr := range ranked {
		reasons := strings.Join(fr.Reasons, ", ")
		sb.WriteString(fmt.Sprintf("[%.1f] %s (%s)\n", fr.Score, fr.Path, reasons))
	}

	return sb.String()
}

// getGitChangedFiles returns a set of files with uncommitted changes.
func getGitChangedFiles(root string) map[string]bool {
	result := make(map[string]bool)

	// Get modified/added tracked files.
	output := runGitCmd(root, "diff", "--name-only", "HEAD")
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result[line] = true
		}
	}

	// Get untracked files.
	untrackedOutput := runGitCmd(root, "ls-files", "--others", "--exclude-standard")
	for _, line := range strings.Split(untrackedOutput, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result[line] = true
		}
	}

	// Get staged but not yet committed files.
	stagedOutput := runGitCmd(root, "diff", "--name-only", "--cached")
	for _, line := range strings.Split(stagedOutput, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result[line] = true
		}
	}

	return result
}

// getRecentlyModifiedFiles returns files modified in the last 5 commits.
func getRecentlyModifiedFiles(root string) map[string]bool {
	result := make(map[string]bool)
	output := runGitCmd(root, "log", "--oneline", "--name-only", "-5", "--diff-filter=ACRM")
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip commit message lines (they contain spaces).
		if strings.Contains(line, " ") {
			continue
		}
		// If the line looks like a file path, add it.
		if strings.Contains(line, ".") || strings.Contains(line, "/") {
			result[line] = true
		}
	}
	return result
}

// extractImportPaths reads a file and extracts imported file paths.
// Returns relative paths that might match other project files.
func extractImportPaths(root, relPath string) []string {
	absPath := filepath.Join(root, relPath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}

	content := string(data)
	var imports []string
	ext := strings.ToLower(filepath.Ext(relPath))

	switch ext {
	case ".ts", ".tsx", ".js", ".jsx":
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, "from") && !strings.Contains(line, "require") {
				continue
			}
			for _, q := range []byte{'\'', '"'} {
				start := strings.IndexByte(line, q)
				if start < 0 {
					continue
				}
				end := strings.IndexByte(line[start+1:], q)
				if end < 0 {
					continue
				}
				impPath := line[start+1 : start+1+end]
				if strings.HasPrefix(impPath, ".") {
					dir := filepath.Dir(relPath)
					resolved := filepath.Join(dir, impPath)
					for _, tryExt := range []string{".ts", ".tsx", ".js", ".jsx", "/index.ts", "/index.js"} {
						candidate := resolved + tryExt
						if _, err := os.Stat(filepath.Join(root, candidate)); err == nil {
							imports = append(imports, candidate)
						}
					}
				}
				break
			}
		}

	case ".py":
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "from .") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					modPath := strings.TrimPrefix(parts[1], ".")
					modFile := strings.ReplaceAll(modPath, ".", "/") + ".py"
					dir := filepath.Dir(relPath)
					imports = append(imports, filepath.Join(dir, modFile))
				}
			}
		}
	}

	return imports
}

// runGitCmd runs a git command with a timeout.
func runGitCmd(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
