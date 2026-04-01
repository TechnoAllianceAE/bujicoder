package agentruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/TechnoAllianceAE/bujicoder/shared/codeintel"
	"github.com/TechnoAllianceAE/bujicoder/shared/smartctx"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// buildDynamicContext gathers project context (file tree, git changes, system info,
// knowledge files, symbol index, and smart-ranked files) and returns a prompt
// section to append to the system prompt.
func buildDynamicContext(projectRoot string, userQuery ...string) string {
	if projectRoot == "" {
		return ""
	}

	// Resolve to absolute path so the LLM knows the full path for terminal commands.
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		absRoot = projectRoot
	}

	var sections []string

	sections = append(sections, fmt.Sprintf("# Project Root\n\n%s", absRoot))

	if tree := buildFileTree(projectRoot); tree != "" {
		sections = append(sections, "# Project File Tree\n\n"+tree)
	}

	if git := buildGitChanges(projectRoot); git != "" {
		sections = append(sections, "# Initial Git Changes\n\n"+git)
	}

	sections = append(sections, buildSystemInfo())

	if knowledge := readKnowledgeFiles(projectRoot); knowledge != "" {
		sections = append(sections, "# Knowledge Files\n\n"+knowledge)
	}

	// Load persistent project memory (BUJI.md).
	// Cap at 6000 bytes (same as knowledge files) to prevent unbounded context consumption.
	if memory := tools.ReadProjectMemory(projectRoot); memory != "" {
		if len(memory) > 6000 {
			memory = memory[:6000] + "\n... (truncated)"
		}
		sections = append(sections, "# Project Memory (BUJI.md)\n\n"+memory)
	}

	// Symbol index: extract top-level symbols from project files for code intelligence.
	parser := codeintel.NewParser()
	symbolIndex := parser.IndexProject(projectRoot, nil)
	if formatted := codeintel.FormatIndex(symbolIndex); formatted != "" {
		sections = append(sections, "# Code Intelligence\n\n"+formatted)
	}

	// Smart context: rank files by relevance to the user's query.
	query := ""
	if len(userQuery) > 0 {
		query = userQuery[0]
	}
	if query != "" {
		symbolNames := codeintel.SymbolNames(symbolIndex)
		ranked := smartctx.RankFiles(projectRoot, query, symbolNames)
		if formatted := smartctx.FormatRankedFiles(ranked); formatted != "" {
			sections = append(sections, "# Smart Context\n\n"+formatted)
		}
	}

	return strings.Join(sections, "\n\n")
}

// skipDirs contains directory names to exclude from the file tree.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
	".next": true, "dist": true, "build": true, ".cache": true,
	".venv": true, "target": true, ".turbo": true, ".terraform": true,
	".yarn": true, ".pnp": true, "coverage": true, ".angular": true,
	".svelte-kit": true, ".parcel-cache": true, ".idea": true, ".vscode": true,
	".github": true,
}

// buildFileTree generates a truncated file tree of the project.
func buildFileTree(root string) string {
	var lines []string
	maxFiles := 200
	count := 0
	truncated := false

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}

		name := d.Name()
		if d.IsDir() && skipDirs[name] {
			return filepath.SkipDir
		}

		if count >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}

		depth := strings.Count(rel, string(os.PathSeparator))
		indent := strings.Repeat("  ", depth)
		if d.IsDir() {
			lines = append(lines, indent+name+"/")
		} else {
			lines = append(lines, indent+name)
			count++
		}
		return nil
	})

	if len(lines) == 0 {
		return ""
	}

	result := "<project_file_tree>\n" + strings.Join(lines, "\n") + "\n</project_file_tree>"
	if truncated {
		result += "\n\nNote: File tree truncated to 200 files."
	}
	return result
}

// buildGitChanges runs git status and diff in the project directory.
func buildGitChanges(root string) string {
	var sections []string

	status := runGitCmd(root, "status", "--short", "-b")
	if status != "" {
		sections = append(sections, "<git_status>\n"+status+"\n</git_status>")
	}

	diffStat := runGitCmd(root, "diff", "--stat", "HEAD")
	if diffStat != "" {
		sections = append(sections, "<git_diff_stat>\n"+diffStat+"\n</git_diff_stat>")
	}

	shortDiff := runGitCmd(root, "diff", "HEAD", "--no-color")
	if len(shortDiff) > 4000 {
		shortDiff = shortDiff[:4000] + "\n... (truncated)"
	}
	if shortDiff != "" {
		sections = append(sections, "<git_diff>\n"+shortDiff+"\n</git_diff>")
	}

	recentCommits := runGitCmd(root, "log", "--oneline", "-5")
	if recentCommits != "" {
		sections = append(sections, "<git_recent_commits>\n"+recentCommits+"\n</git_recent_commits>")
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

// buildSystemInfo returns OS and shell information.
func buildSystemInfo() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "unknown"
	} else {
		shell = filepath.Base(shell)
	}

	return fmt.Sprintf("# System Info\n\nOperating System: %s\nArchitecture: %s\nShell: %s",
		runtime.GOOS, runtime.GOARCH, shell)
}

// readKnowledgeFiles reads knowledge.md, CLAUDE.md, and *.knowledge.md files from the project root.
// If a bujicoder/ subdirectory exists and contains knowledge files, those take priority over
// identically-named files in the root (so bujicoder/knowledge.md is preferred over ./knowledge.md).
func readKnowledgeFiles(root string) string {
	var sections []string
	loaded := make(map[string]bool) // track filenames already loaded (by base name)

	// If a bujicoder/ subdirectory exists, read its knowledge files first (higher priority).
	bujiDir := filepath.Join(root, "bujicoder")
	if info, err := os.Stat(bujiDir); err == nil && info.IsDir() {
		sections, loaded = readKnowledgeFilesFrom(bujiDir, sections, loaded)
	}

	// Read from the project root, skipping any filenames already loaded from bujicoder/.
	sections, _ = readKnowledgeFilesFrom(root, sections, loaded)

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

// readKnowledgeFilesFrom reads well-known knowledge files and *.knowledge.md globs from dir,
// skipping any base names present in the loaded set. Returns updated sections and loaded map.
func readKnowledgeFilesFrom(dir string, sections []string, loaded map[string]bool) ([]string, map[string]bool) {
	names := []string{"knowledge.md", "CLAUDE.md"}
	for _, name := range names {
		if loaded[name] {
			continue
		}
		path := filepath.Join(dir, name)
		if content := readAndTruncate(path, 6000); content != "" {
			sections = append(sections, fmt.Sprintf("## %s\n\n```\n%s\n```", name, content))
			loaded[name] = true
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "*.knowledge.md"))
	for _, m := range matches {
		base := filepath.Base(m)
		if base == "knowledge.md" || loaded[base] {
			continue
		}
		if content := readAndTruncate(m, 6000); content != "" {
			sections = append(sections, fmt.Sprintf("## %s\n\n```\n%s\n```", base, content))
			loaded[base] = true
		}
	}

	return sections, loaded
}

// readAndTruncate reads a file and truncates to maxLen bytes.
func readAndTruncate(path string, maxLen int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	if len(content) > maxLen {
		content = content[:maxLen] + "\n... (truncated)"
	}
	return content
}

// gitCmdTimeout is the maximum time to wait for a git command.
const gitCmdTimeout = 5 * time.Second

// runGitCmd runs a git command in the given directory with a timeout and returns stdout.
func runGitCmd(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
