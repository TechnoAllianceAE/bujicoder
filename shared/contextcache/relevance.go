package contextcache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RelevantFiles returns a set of file paths most relevant to the current
// session based on git changes, import graph, and working directory proximity.
// The returned paths are relative to the cache root.
func (c *Cache) RelevantFiles(focusPaths []string, maxFiles int) []string {
	if maxFiles <= 0 {
		maxFiles = 50
	}

	seen := make(map[string]bool)
	var result []string

	add := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		result = append(result, p)
	}

	// 1. Focus paths (explicitly referenced files).
	for _, p := range focusPaths {
		add(p)
	}

	// 2. Recently changed files (git diff).
	for _, p := range c.gitChangedFiles() {
		if len(result) >= maxFiles {
			break
		}
		add(p)
	}

	// 3. Import graph expansion — for each focus file, find related imports.
	for _, fp := range focusPaths {
		if len(result) >= maxFiles {
			break
		}
		c.mu.RLock()
		entry, ok := c.entries[fp]
		c.mu.RUnlock()
		if !ok {
			continue
		}
		for _, imp := range entry.Imports {
			if len(result) >= maxFiles {
				break
			}
			// Resolve import to relative paths within the project.
			for _, resolved := range c.resolveImport(imp) {
				if len(result) >= maxFiles {
					break
				}
				add(resolved)
			}
		}
	}

	// 4. Sibling files (same directory as focus files).
	for _, fp := range focusPaths {
		if len(result) >= maxFiles {
			break
		}
		dir := filepath.Dir(fp)
		absDir := filepath.Join(c.root, dir)
		entries, err := os.ReadDir(absDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if len(result) >= maxFiles {
				break
			}
			if e.IsDir() {
				continue
			}
			rel := filepath.Join(dir, e.Name())
			add(rel)
		}
	}

	return result
}

// gitChangedFiles returns files modified in the working tree and staged index.
func (c *Cache) gitChangedFiles() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "HEAD")
	cmd.Dir = c.root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}

	// Also include staged but not yet committed.
	cmd2 := exec.CommandContext(ctx, "git", "diff", "--cached", "--name-only")
	cmd2.Dir = c.root
	out2, err := cmd2.Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out2)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				files = append(files, line)
			}
		}
	}

	return files
}

// resolveImport attempts to map an import string to relative file paths in the project.
func (c *Cache) resolveImport(imp string) []string {
	// For Go: check if the import path is a subpackage of the module.
	goMod := c.goModulePath()
	if goMod != "" && strings.HasPrefix(imp, goMod) {
		rel := strings.TrimPrefix(imp, goMod)
		rel = strings.TrimPrefix(rel, "/")
		dir := filepath.Join(c.root, rel)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return listGoFiles(dir, rel)
		}
	}

	// For relative imports (JS/Python): resolve directly.
	if strings.HasPrefix(imp, ".") || strings.HasPrefix(imp, "/") {
		// Try common extensions.
		for _, ext := range []string{"", ".go", ".py", ".js", ".ts", ".tsx"} {
			candidate := imp + ext
			abs := filepath.Join(c.root, candidate)
			if _, err := os.Stat(abs); err == nil {
				return []string{candidate}
			}
		}
	}

	return nil
}

// goModulePath reads the module path from go.mod in the project root.
func (c *Cache) goModulePath() string {
	data, err := os.ReadFile(filepath.Join(c.root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

// listGoFiles returns relative paths of .go files in a directory.
func listGoFiles(dir, relDir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			files = append(files, filepath.Join(relDir, e.Name()))
		}
	}
	return files
}
