// Package tools provides local tool executors for the BujiCoder CLI.
// These tools run locally on the user's machine (file ops, terminal, search).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/TechnoAllianceAE/bujicoder/shared/codeintel"
	"github.com/TechnoAllianceAE/bujicoder/shared/contextcache"
	"github.com/TechnoAllianceAE/bujicoder/shared/lsp"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools/editmatch"
)

// Tool is a local tool executor.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any // optional JSON Schema for tool parameters (used by MCP tools)
	Execute     func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds available local tools.
type Registry struct {
	tools map[string]*Tool
}

// contextKey is an unexported type for context keys in this package.
type contextKey string

const workDirCtxKey contextKey = "tools_work_dir"
const planModeCtxKey contextKey = "tools_plan_mode"
const cacheCtxKey contextKey = "tools_context_cache"
const lspMgrCtxKey contextKey = "tools_lsp_manager"

// WithPlanMode returns a child context with plan mode enabled.
// When plan mode is active, write operations (write_file, str_replace,
// run_terminal_command) are blocked unless the target is a .md file.
func WithPlanMode(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, planModeCtxKey, enabled)
}

// IsPlanMode returns true if the context has plan mode enabled.
func IsPlanMode(ctx context.Context) bool {
	v, _ := ctx.Value(planModeCtxKey).(bool)
	return v
}

// isPlanModeAllowedPath returns true if the path is allowed in plan mode.
// Only .md (markdown) files can be written in plan mode.
func isPlanModeAllowedPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".md")
}

// WithContextCache returns a child context carrying a file content cache.
func WithContextCache(ctx context.Context, cache *contextcache.Cache) context.Context {
	return context.WithValue(ctx, cacheCtxKey, cache)
}

// getContextCache returns the context cache if set.
func getContextCache(ctx context.Context) *contextcache.Cache {
	c, _ := ctx.Value(cacheCtxKey).(*contextcache.Cache)
	return c
}

// WithLSPManager returns a child context carrying an LSP manager.
func WithLSPManager(ctx context.Context, mgr *lsp.Manager) context.Context {
	return context.WithValue(ctx, lspMgrCtxKey, mgr)
}

// getLSPManager returns the LSP manager if set.
func getLSPManager(ctx context.Context) *lsp.Manager {
	m, _ := ctx.Value(lspMgrCtxKey).(*lsp.Manager)
	return m
}

// WithWorkDir returns a child context carrying a per-request working directory.
// Tools will use this directory instead of the default one configured at startup.
func WithWorkDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, workDirCtxKey, dir)
}

// effectiveWorkDir returns the per-request work dir from ctx if set,
// otherwise falls back to the default. If the resolved directory does
// not exist, it falls back to the OS temp directory to avoid chdir errors.
func effectiveWorkDir(ctx context.Context, defaultDir string) string {
	dir := defaultDir
	if v, ok := ctx.Value(workDirCtxKey).(string); ok && v != "" {
		dir = v
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return os.TempDir()
	}
	return dir
}

// UserPromptFunc is a callback for the ask_user tool. It sends a question to
// the user and returns their answer. If nil, ask_user will return an error.
type UserPromptFunc func(question string) (string, error)

// ApprovalFunc is a callback for dangerous command approval. It sends the
// command and reason to the user and returns true if approved, false if denied.
// If nil, dangerous commands are hard-blocked (existing behaviour).
type ApprovalFunc func(command, reason string) (bool, error)

// RegistryOpts configures optional behaviour of the tool registry.
type RegistryOpts struct {
	UserPrompt  UserPromptFunc
	Approval    ApprovalFunc
	Permissions *ProjectPermissions
}

// NewRegistry creates a tool registry with all built-in tools.
func NewRegistry(workDir string, opts ...RegistryOpts) *Registry {
	var o RegistryOpts
	if len(opts) > 0 {
		o = opts[0]
	}

	r := &Registry{tools: make(map[string]*Tool)}

	r.Register(&Tool{
		Name:        "read_files",
		Description: "Read one or more files",
		Execute:     readFiles(workDir, o.Permissions),
	})
	r.Register(&Tool{
		Name:        "write_file",
		Description: "Write content to a file",
		Execute:     writeFile(workDir, o.Permissions),
	})
	r.Register(&Tool{
		Name:        "str_replace",
		Description: "Replace a string in a file",
		Execute:     strReplace(workDir, o.Permissions),
	})
	r.Register(&Tool{
		Name:        "list_directory",
		Description: "List directory contents",
		Execute:     listDirectory(workDir),
	})
	r.Register(&Tool{
		Name:        "run_terminal_command",
		Description: "Run a terminal command",
		Execute:     runTerminalCommand(workDir, o.Approval, o.Permissions),
	})
	r.Register(&Tool{
		Name:        "glob",
		Description: "Find files matching a glob pattern",
		Execute:     globFiles(workDir),
	})
	r.Register(&Tool{
		Name:        "find_files",
		Description: "Search for files by name pattern across the project",
		Execute:     findFiles(workDir),
	})
	r.Register(&Tool{
		Name:        "code_search",
		Description: "Search file contents using regex or literal pattern",
		Execute:     codeSearch(workDir),
	})
	r.Register(&Tool{
		Name:        "web_search",
		Description: "Search the web for information using DuckDuckGo",
		Execute:     webSearch(),
	})
	r.Register(&Tool{
		Name:        "ask_user",
		Description: "Ask the user a question and wait for their response",
		Execute:     askUser(o.UserPrompt),
	})
	r.Register(&Tool{
		Name:        "propose_edit",
		Description: "Propose a string replacement edit without writing to disk. Used by implementor agents in parallel evolution mode.",
		Execute:     proposeEdit(workDir),
	})
	r.Register(&Tool{
		Name:        "propose_write_file",
		Description: "Propose writing a file without writing to disk. Used by implementor agents in parallel evolution mode.",
		Execute:     proposeWriteFile(workDir),
	})
	r.Register(&Tool{
		Name:        "todo_write",
		Description: "Set or update the task list. Items have id, task, status (pending/in_progress/done/blocked), and optional note.",
		Execute:     todoWrite(),
	})
	r.Register(&Tool{
		Name:        "todo_read",
		Description: "Read the current task list as JSON.",
		Execute:     todoRead(),
	})
	r.Register(&Tool{
		Name:        "multi_edit",
		Description: "Apply multiple str_replace edits in a single call. Edits within a file are applied sequentially.",
		Execute:     multiEdit(workDir, o.Permissions),
	})
	r.Register(&Tool{
		Name:        "apply_patch",
		Description: "Apply a unified diff/patch to the project. Supports add, update, move, and delete operations.",
		Execute:     applyPatch(workDir, o.Permissions),
	})
	r.Register(&Tool{
		Name:        "symbols",
		Description: "Extract code symbols (functions, classes, types, methods) from files. Returns a structured index of the codebase.",
		Execute:     symbols(workDir),
	})
	r.Register(&Tool{
		Name:        "structured_output",
		Description: "Validate and return structured JSON data against a provided schema. Use when producing structured plans, configs, or decisions.",
		Execute:     structuredOutput(),
	})
	r.Register(&Tool{
		Name:        "memory_read",
		Description: "Read the project memory file (BUJI.md). Contains persistent knowledge, conventions, and learnings from previous sessions.",
		Execute:     memoryRead(workDir),
	})
	r.Register(&Tool{
		Name:        "memory_write",
		Description: "Write to the project memory file (BUJI.md). Use to persist important learnings, conventions, architecture notes, or common pitfalls for future sessions. Requires 'section' (header name) and 'content' (text). Optional 'replace' (bool) to overwrite a section.",
		Execute:     memoryWrite(workDir),
	})

	return r
}

// Register adds a tool to the registry.
func (r *Registry) Register(t *Tool) {
	r.tools[t.Name] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List returns all tool names.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// --- Tool implementations ---

func readFiles(workDir string, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Paths []string `json:"paths"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		wd := effectiveWorkDir(ctx, workDir)
		cache := getContextCache(ctx)
		var result strings.Builder
		for _, p := range params.Paths {
			if perms.IsPathRestricted(p) {
				result.WriteString(fmt.Sprintf("--- %s ---\nError: access denied: path is restricted by permissions.yaml\n\n", p))
				continue
			}

			// Try the context cache first (avoids redundant disk reads).
			if cache != nil {
				if content, err := cache.Get(p); err == nil {
					result.WriteString(fmt.Sprintf("--- %s ---\n%s\n\n", p, content))
					continue
				}
			}

			absPath, err := safePath(wd, p)
			if err != nil {
				result.WriteString(fmt.Sprintf("--- %s ---\nError: %v\n\n", p, err))
				continue
			}
			data, err := os.ReadFile(absPath)
			if err != nil {
				result.WriteString(fmt.Sprintf("--- %s ---\nError: %v\n\n", p, err))
				continue
			}
			result.WriteString(fmt.Sprintf("--- %s ---\n%s\n\n", p, string(data)))
		}
		return result.String(), nil
	}
}

func writeFile(workDir string, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		if IsPlanMode(ctx) && !isPlanModeAllowedPath(params.Path) {
			return "", fmt.Errorf("BLOCKED (plan mode): write_file is not allowed for non-.md files in plan mode. Use propose_write_file instead.\nPath: %s", params.Path)
		}

		if perms.IsPathRestricted(params.Path) {
			return "", fmt.Errorf("access denied: path %q is restricted by permissions.yaml", params.Path)
		}

		absPath, err := safePath(effectiveWorkDir(ctx, workDir), params.Path)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(absPath, []byte(params.Content), 0o644); err != nil {
			return "", err
		}
		// Invalidate cache for the written file.
		if cache := getContextCache(ctx); cache != nil {
			cache.Invalidate(params.Path)
		}
		result := fmt.Sprintf("Wrote %d bytes to %s", len(params.Content), params.Path)
		// Run LSP diagnostics if available.
		if mgr := getLSPManager(ctx); mgr != nil {
			if diags := mgr.Diagnose(absPath, params.Content); len(diags) > 0 {
				result += lsp.FormatDiagnostics(diags, 10)
			}
		}
		return result, nil
	}
}

func strReplace(workDir string, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Path      string `json:"path"`
			OldStr    string `json:"old_str"`
			NewStr    string `json:"new_str"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		if IsPlanMode(ctx) && !isPlanModeAllowedPath(params.Path) {
			return "", fmt.Errorf("BLOCKED (plan mode): str_replace is not allowed for non-.md files in plan mode. Use propose_edit instead.\nPath: %s", params.Path)
		}

		if perms.IsPathRestricted(params.Path) {
			return "", fmt.Errorf("access denied: path %q is restricted by permissions.yaml", params.Path)
		}

		absPath, err := safePath(effectiveWorkDir(ctx, workDir), params.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", err
		}

		content := string(data)

		// Use fuzzy edit matching — tries exact first, then cascading strategies.
		match := editmatch.Find(content, params.OldStr)
		if match == nil {
			return "", fmt.Errorf("old_str not found in %s (tried exact + fuzzy matching)", params.Path)
		}

		newContent := content[:match.Start] + params.NewStr + content[match.End:]
		if err := os.WriteFile(absPath, []byte(newContent), 0o644); err != nil {
			return "", err
		}
		// Invalidate cache for the edited file.
		if cache := getContextCache(ctx); cache != nil {
			cache.Invalidate(params.Path)
		}
		result := "Replacement applied"
		if match.Strategy != "exact" {
			result = fmt.Sprintf("Replacement applied (fuzzy match: %s)", match.Strategy)
		}
		// Run LSP diagnostics if available.
		if mgr := getLSPManager(ctx); mgr != nil {
			if diags := mgr.Diagnose(absPath, newContent); len(diags) > 0 {
				result += lsp.FormatDiagnostics(diags, 10)
			}
		}
		return result, nil
	}
}

func listDirectory(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		absPath, err := safePath(effectiveWorkDir(ctx, workDir), params.Path)
		if err != nil {
			return "", err
		}
		entries, err := os.ReadDir(absPath)
		if err != nil {
			return "", err
		}

		var result strings.Builder
		for _, entry := range entries {
			if entry.IsDir() {
				result.WriteString(fmt.Sprintf("%s/\n", entry.Name()))
			} else {
				result.WriteString(fmt.Sprintf("%s\n", entry.Name()))
			}
		}
		return result.String(), nil
	}
}

// isReadOnlyCommand checks if a terminal command is safe for plan mode (read-only).
func isReadOnlyCommand(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	// Allow common read-only commands
	readOnlyPrefixes := []string{
		"ls", "cat", "head", "tail", "less", "more", "wc",
		"find", "grep", "rg", "ag", "ack",
		"git status", "git log", "git diff", "git show", "git branch",
		"git remote", "git tag", "git stash list",
		"pwd", "echo", "which", "whereis", "whoami",
		"tree", "file", "stat", "du", "df",
		"go vet", "go doc", "go list",
		"npm list", "npm ls", "npm info",
		"python --version", "node --version", "go version",
	}
	for _, prefix := range readOnlyPrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") || strings.HasPrefix(lower, prefix+"\t") {
			return true
		}
	}
	// Allow piped read-only commands if the first command is read-only
	if idx := strings.Index(lower, "|"); idx > 0 {
		first := strings.TrimSpace(lower[:idx])
		return isReadOnlyCommand(first)
	}
	return false
}

func runTerminalCommand(workDir string, approvalFn ApprovalFunc, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		// Plan mode: only allow read-only commands.
		if IsPlanMode(ctx) && !isReadOnlyCommand(params.Command) {
			return "", fmt.Errorf("BLOCKED (plan mode): only read-only commands are allowed in plan mode.\nCommand: %s", params.Command)
		}

		// 1. Check permissions.yaml command rules (first match wins).
		if action := perms.CheckCommand(params.Command); action != "" {
			switch action {
			case ActionAllow:
				// Explicitly allowed — but still block critical threats for safety.
				if v := AnalyzeCommand(params.Command); v.Blocked {
					return "", fmt.Errorf("BLOCKED [%s]: %s\nThis command was blocked even though permissions.yaml allows it — critical threats are always blocked.\nCommand: %s",
						v.Level, v.Reason, params.Command)
				}
				goto execute
			case ActionDeny:
				return "", fmt.Errorf("BLOCKED by permissions.yaml: command matches a deny rule.\nCommand: %s", params.Command)
			case ActionAsk:
				// Fall through to security analysis + approval flow.
			}
		}

		// 2. Comprehensive security analysis.
		{
			verdict := AnalyzeCommand(params.Command)

			if verdict.Blocked {
				// Critical threats are always blocked regardless of mode.
				return "", fmt.Errorf("BLOCKED [%s]: %s\nThis command was not executed.\nCommand: %s",
					verdict.Level, verdict.Reason, params.Command)
			}

			if verdict.NeedsApproval {
				// 3. Apply permission mode.
				if perms != nil && perms.Mode == ModeYolo {
					// Yolo mode: auto-approve.
					goto execute
				}
				if perms != nil && perms.Mode == ModeStrict {
					return "", fmt.Errorf("BLOCKED (strict mode) [%s]: %s\nThis command was not executed.\nCommand: %s",
						verdict.Level, verdict.Reason, params.Command)
				}

				// Ask mode (default).
				if approvalFn == nil {
					return "", fmt.Errorf("BLOCKED [%s]: %s\nThis command was not executed.\nPlease inform the user and let them run it manually.\nCommand: %s",
						verdict.Level, verdict.Reason, params.Command)
				}
				approved, err := approvalFn(params.Command, verdict.Reason)
				if err != nil {
					return "", fmt.Errorf("approval error: %w", err)
				}
				if !approved {
					return "", fmt.Errorf("DENIED: user declined to run this command.\nCommand: %s\nReason: %s",
						params.Command, verdict.Reason)
				}
			}
		}

	execute:
		cmd := exec.CommandContext(ctx, "sh", "-c", params.Command)
		cmd.Dir = effectiveWorkDir(ctx, workDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return string(output), fmt.Errorf("command failed: %w\n%s", err, string(output))
		}
		return string(output), nil
	}
}

func globFiles(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		wd := effectiveWorkDir(ctx, workDir)
		// Resolve pattern relative to workDir.
		absPattern := params.Pattern
		if !filepath.IsAbs(params.Pattern) {
			absPattern = filepath.Join(wd, params.Pattern)
		}
		absPattern = filepath.Clean(absPattern)

		// Canonicalize workDir for boundary check.
		canonicalRoot, err := filepath.EvalSymlinks(wd)
		if err != nil {
			canonicalRoot = filepath.Clean(wd)
		}

		matches, err := filepath.Glob(absPattern)
		if err != nil {
			return "", err
		}

		var result strings.Builder
		for _, m := range matches {
			// Canonicalize each match and filter to workDir boundary.
			cm, err := filepath.EvalSymlinks(m)
			if err != nil {
				cm = filepath.Clean(m)
			}
			if cm != canonicalRoot && !strings.HasPrefix(cm, canonicalRoot+string(filepath.Separator)) {
				continue
			}
			rel, _ := filepath.Rel(wd, m)
			result.WriteString(rel + "\n")
		}
		return result.String(), nil
	}
}

func findFiles(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		wd := effectiveWorkDir(ctx, workDir)
		var results strings.Builder
		count := 0
		maxResults := 200

		_ = filepath.WalkDir(wd, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip errors
			}
			if count >= maxResults {
				return filepath.SkipAll
			}

			// Skip common ignored directories
			name := d.Name()
			if d.IsDir() {
				switch name {
				case ".git", "node_modules", "__pycache__", ".next", "vendor", "dist", "build":
					return filepath.SkipDir
				}
				return nil
			}

			// Check if name matches the pattern (case-insensitive substring match)
			lowerName := strings.ToLower(name)
			lowerPattern := strings.ToLower(params.Pattern)
			if strings.Contains(lowerName, lowerPattern) {
				rel, _ := filepath.Rel(wd, path)
				results.WriteString(rel + "\n")
				count++
			}
			return nil
		})

		if count == 0 {
			return "No files found matching: " + params.Pattern, nil
		}
		return results.String(), nil
	}
}

func codeSearch(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Pattern string `json:"pattern"`
			Glob    string `json:"glob,omitempty"` // optional file pattern filter
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		wd := effectiveWorkDir(ctx, workDir)

		// Try ripgrep first (fast)
		rgArgs := []string{
			"--max-count", "50",
			"--line-number",
			"--no-heading",
			"--color", "never",
			"-e", params.Pattern,
		}
		if params.Glob != "" {
			rgArgs = append(rgArgs, "--glob", params.Glob)
		}
		rgArgs = append(rgArgs, ".")

		cmd := exec.CommandContext(ctx, "rg", rgArgs...)
		cmd.Dir = wd
		output, err := cmd.CombinedOutput()
		if err == nil {
			return string(output), nil
		}

		// Fallback: use grep
		grepArgs := []string{"-rn", "--max-count=50", params.Pattern, "."}
		cmd = exec.CommandContext(ctx, "grep", grepArgs...)
		cmd.Dir = wd
		output, err = cmd.CombinedOutput()
		if err != nil && len(output) == 0 {
			return "No matches found for: " + params.Pattern, nil
		}
		return string(output), nil
	}
}

func webSearch() func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		// Use DuckDuckGo HTML lite (no API key required)
		searchURL := "https://html.duckduckgo.com/html/?q=" + strings.ReplaceAll(params.Query, " ", "+")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
		if err != nil {
			return "", fmt.Errorf("create search request: %w", err)
		}
		req.Header.Set("User-Agent", "BujiCoder/1.0 (CLI)")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("web search: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if err != nil {
			return "", fmt.Errorf("read response: %w", err)
		}

		// Extract text content from HTML (simple extraction)
		content := string(body)
		// Remove HTML tags (basic)
		var result strings.Builder
		inTag := false
		for _, c := range content {
			if c == '<' {
				inTag = true
				continue
			}
			if c == '>' {
				inTag = false
				result.WriteRune(' ')
				continue
			}
			if !inTag {
				result.WriteRune(c)
			}
		}

		// Truncate to reasonable length
		text := result.String()
		if len(text) > 4000 {
			text = text[:4000] + "...\n[truncated]"
		}
		return text, nil
	}
}

func askUser(promptFn UserPromptFunc) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		if promptFn == nil {
			return "", fmt.Errorf("ask_user is not available in this context")
		}

		return promptFn(params.Question)
	}
}

func symbols(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Paths []string `json:"paths"` // optional: specific file paths to analyze
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		wd := effectiveWorkDir(ctx, workDir)
		parser := codeintel.NewParser()

		if len(params.Paths) > 0 {
			// Index specific files.
			index := parser.IndexProject(wd, params.Paths)
			if len(index) == 0 {
				return "No symbols found in the specified files.", nil
			}
			return codeintel.FormatIndex(index), nil
		}

		// Index the entire project (up to 100 files).
		index := parser.IndexProject(wd, nil)
		if len(index) == 0 {
			return "No supported source files found in the project.", nil
		}
		return codeintel.FormatIndex(index), nil
	}
}

// safePath resolves a path relative to workDir and ensures the result stays
// within workDir. Returns an error if the path escapes the boundary.
func safePath(workDir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("access denied: empty path")
	}
	// Reject null bytes which can bypass path checks in some OS calls.
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("access denied: path contains null byte")
	}
	// Reject excessively long paths (Windows MAX_PATH=260, but be generous).
	if len(path) > 4096 {
		return "", fmt.Errorf("access denied: path exceeds maximum length")
	}

	resolved := path
	if !filepath.IsAbs(path) {
		resolved = filepath.Join(workDir, path)
	}
	resolved = filepath.Clean(resolved)

	// Canonicalize the workDir for comparison (follow symlinks).
	canonicalRoot, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		canonicalRoot = filepath.Clean(workDir)
	}

	// Canonicalize the target if it exists (follow symlinks).
	canonicalResolved, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// File may not exist yet (write_file), check the cleaned path.
		canonicalResolved = resolved
	}

	if canonicalResolved != canonicalRoot &&
		!strings.HasPrefix(canonicalResolved, canonicalRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path %q is outside the project directory", path)
	}
	return resolved, nil
}
