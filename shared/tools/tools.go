// Package tools provides local tool executors for the BujiCoder CLI.
// These tools run locally on the user's machine (file ops, terminal, search).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

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

// Registry holds available local tools. It is safe for concurrent use: MCP
// servers register tools while agent goroutines look them up.
type Registry struct {
	mu    sync.RWMutex
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
// Only .md (markdown) files can be written in plan mode. The path is cleaned
// first so that tricks like "notes.md/../main.go" cannot pass the suffix test.
func isPlanModeAllowedPath(path string) bool {
	if path == "" {
		return false
	}
	return strings.HasSuffix(strings.ToLower(filepath.Clean(path)), ".md")
}

// planModeWriteAllowed reports whether writing to requested (resolved to
// absPath) is permitted in plan mode. Both the requested path and the symlink
// target are checked, so a "notes.md" symlink pointing at a source file inside
// the workspace cannot be used to escape plan mode.
func planModeWriteAllowed(requested, absPath string) bool {
	if !isPlanModeAllowedPath(requested) {
		return false
	}
	target := absPath
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		target = resolved
	}
	return isPlanModeAllowedPath(target)
}

// pathRestricted reports whether the requested path, or the workspace-relative
// path it actually resolves to, is restricted by permissions.yaml. Checking only
// the requested spelling lets a symlink inside the workspace (link.txt -> .env)
// read or overwrite a restricted file.
func pathRestricted(perms *ProjectPermissions, workDir, requested, absPath string) bool {
	if perms.IsPathRestricted(requested) {
		return true
	}
	target := absPath
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		target = resolved
	}
	root := filepath.Clean(workDir)
	if resolvedRoot, err := filepath.EvalSymlinks(workDir); err == nil {
		root = resolvedRoot
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false // outside the workspace: safePath already rejects those
	}
	return perms.IsPathRestricted(rel)
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

// Register adds a tool to the registry and reports whether it was accepted.
//
// A tool without a name or without an executor is rejected: registering it would
// hand the dispatch loop a value it would nil-dereference on the first call. A
// name that is already registered is also rejected instead of silently replacing
// the incumbent — an MCP server exposing "read_files" must not displace the
// built-in file reader. Callers that can retry under another name (e.g. MCP with
// a "<server>_<tool>" fallback) should act on the returned value; the collision
// is logged either way.
func (r *Registry) Register(t *Tool) bool {
	if t == nil || t.Name == "" || t.Execute == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		log.Warn().Str("tool", t.Name).Msg("tool name already registered; keeping the existing tool")
		return false
	}
	r.tools[t.Name] = t
	return true
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all tool names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
		if err := unmarshalArgs("read_files", args, &params); err != nil {
			return "", err
		}
		if len(params.Paths) == 0 {
			return "", fmt.Errorf("read_files: 'paths' is required and must be a non-empty array of file paths")
		}

		wd := effectiveWorkDir(ctx, workDir)
		cache := getContextCache(ctx)
		var result strings.Builder
		for _, p := range params.Paths {
			// Containment and permission checks run before any read, including
			// the cache lookup: a symlink inside the workspace pointing at a
			// restricted file must not be readable under either path.
			absPath, err := safePath(wd, p)
			if err != nil {
				fmt.Fprintf(&result, "--- %s ---\nError: %v\n\n", p, err)
				continue
			}
			if pathRestricted(perms, wd, p, absPath) {
				fmt.Fprintf(&result, "--- %s ---\nError: access denied: path is restricted by permissions.yaml\n\n", p)
				continue
			}

			// Try the context cache first (avoids redundant disk reads).
			if cache != nil {
				if content, err := cache.Get(p); err == nil {
					fmt.Fprintf(&result, "--- %s ---\n%s\n\n", p, content)
					continue
				}
			}

			data, err := os.ReadFile(absPath)
			if err != nil {
				fmt.Fprintf(&result, "--- %s ---\nError: %v\n\n", p, err)
				continue
			}
			fmt.Fprintf(&result, "--- %s ---\n%s\n\n", p, string(data))
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
		if err := unmarshalArgs("write_file", args, &params); err != nil {
			return "", err
		}
		if params.Path == "" {
			return "", fmt.Errorf("write_file: 'path' is required")
		}

		planMode := IsPlanMode(ctx)
		if planMode && !isPlanModeAllowedPath(params.Path) {
			return "", fmt.Errorf("BLOCKED (plan mode): write_file is not allowed for non-.md files in plan mode. Use propose_write_file instead.\nPath: %s", params.Path)
		}

		if perms.IsPathRestricted(params.Path) {
			return "", fmt.Errorf("access denied: path %q is restricted by permissions.yaml", params.Path)
		}

		absPath, err := safePath(effectiveWorkDir(ctx, workDir), params.Path)
		if err != nil {
			return "", err
		}
		// Re-check plan mode and path restrictions against the resolved target:
		// a symlink named "notes.md" (or one pointing at .env) must not be a
		// back door. Both checks run before any write happens.
		if planMode && !planModeWriteAllowed(params.Path, absPath) {
			return "", fmt.Errorf("BLOCKED (plan mode): %s resolves to a non-.md file", params.Path)
		}
		if pathRestricted(perms, effectiveWorkDir(ctx, workDir), params.Path, absPath) {
			return "", fmt.Errorf("access denied: path %q resolves to a path restricted by permissions.yaml", params.Path)
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return "", fmt.Errorf("create parent directory: %w", err)
		}
		if err := writeFileAtomic(absPath, []byte(params.Content), 0o644); err != nil {
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
			Path   string `json:"path"`
			OldStr string `json:"old_str"`
			NewStr string `json:"new_str"`
		}
		if err := unmarshalArgs("str_replace", args, &params); err != nil {
			return "", err
		}
		if params.Path == "" {
			return "", fmt.Errorf("str_replace: 'path' is required")
		}
		if params.OldStr == "" {
			return "", fmt.Errorf("str_replace: 'old_str' must not be empty — an empty match would insert at an arbitrary position")
		}
		if params.OldStr == params.NewStr {
			return "", fmt.Errorf("str_replace: 'old_str' and 'new_str' are identical — nothing to do")
		}

		planMode := IsPlanMode(ctx)
		if planMode && !isPlanModeAllowedPath(params.Path) {
			return "", fmt.Errorf("BLOCKED (plan mode): str_replace is not allowed for non-.md files in plan mode. Use propose_edit instead.\nPath: %s", params.Path)
		}

		if perms.IsPathRestricted(params.Path) {
			return "", fmt.Errorf("access denied: path %q is restricted by permissions.yaml", params.Path)
		}

		absPath, err := safePath(effectiveWorkDir(ctx, workDir), params.Path)
		if err != nil {
			return "", err
		}
		if planMode && !planModeWriteAllowed(params.Path, absPath) {
			return "", fmt.Errorf("BLOCKED (plan mode): %s resolves to a non-.md file", params.Path)
		}
		if pathRestricted(perms, effectiveWorkDir(ctx, workDir), params.Path, absPath) {
			return "", fmt.Errorf("access denied: path %q resolves to a path restricted by permissions.yaml", params.Path)
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", err
		}

		content := string(data)

		// Use fuzzy edit matching — tries exact first, then cascading strategies.
		// A non-unique match is reported as not found rather than guessing.
		match := editmatch.Find(content, params.OldStr)
		if match == nil {
			return "", fmt.Errorf("old_str not found or not unique in %s (tried exact + fuzzy matching); include more surrounding context to disambiguate", params.Path)
		}

		newContent := content[:match.Start] + params.NewStr + content[match.End:]
		if err := writeFileAtomic(absPath, []byte(newContent), 0o644); err != nil {
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
		if err := unmarshalArgs("list_directory", args, &params); err != nil {
			return "", err
		}
		if params.Path == "" {
			params.Path = "."
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
				fmt.Fprintf(&result, "%s/\n", entry.Name())
			} else {
				fmt.Fprintf(&result, "%s\n", entry.Name())
			}
		}
		return result.String(), nil
	}
}

// isReadOnlyCommand checks if a terminal command is safe for plan mode
// (read-only). Every segment of a chained command must be read-only, and shell
// constructs that can write files or hide the real command — redirection,
// command substitution, backgrounding — are rejected outright. Checking only
// the first segment would let "ls && rm -rf x", "cat f | sh" or
// "echo x > main.go" run in plan mode.
func isReadOnlyCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, ">`") || strings.Contains(trimmed, "$(") {
		return false
	}
	segments := splitCommandSegments(trimmed)
	if len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if !isReadOnlySegment(strings.ToLower(seg)) {
			return false
		}
	}
	return true
}

// isReadOnlySegment reports whether a single (already lower-cased) command
// segment is a known read-only invocation.
func isReadOnlySegment(lower string) bool {
	// A bare '&' survives segment splitting (which only consumes "&&"): it is
	// either backgrounding or an "&>" redirect. Both are unsafe here.
	if strings.Contains(lower, "&") {
		return false
	}
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
	return false
}

func runTerminalCommand(workDir string, approvalFn ApprovalFunc, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Command string `json:"command"`
		}
		if err := unmarshalArgs("run_terminal_command", args, &params); err != nil {
			return "", err
		}
		if strings.TrimSpace(params.Command) == "" {
			return "", fmt.Errorf("run_terminal_command: 'command' is required")
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
		output, err := runCommandBounded(ctx, cmd, terminalCommandTimeout)
		if err != nil {
			return output, fmt.Errorf("command failed: %w\n%s", err, output)
		}
		return output, nil
	}
}

func globFiles(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Pattern string `json:"pattern"`
		}
		if err := unmarshalArgs("glob", args, &params); err != nil {
			return "", err
		}
		if params.Pattern == "" {
			return "", fmt.Errorf("glob: 'pattern' is required")
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
		if err := unmarshalArgs("find_files", args, &params); err != nil {
			return "", err
		}
		if params.Pattern == "" {
			return "", fmt.Errorf("find_files: 'pattern' is required")
		}

		wd := effectiveWorkDir(ctx, workDir)
		var results strings.Builder
		count := 0
		maxResults := 200
		var skipped []string

		lowerPattern := strings.ToLower(params.Pattern)
		walkErr := filepath.WalkDir(wd, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// An unreadable entry must not abort the whole search, but it is
				// reported so the caller knows the listing is incomplete.
				if len(skipped) < 10 {
					skipped = append(skipped, fmt.Sprintf("%s: %v", path, err))
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil //nolint:nilerr // recorded in `skipped` and reported below
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
			if strings.Contains(strings.ToLower(name), lowerPattern) {
				rel, relErr := filepath.Rel(wd, path)
				if relErr != nil {
					rel = path
				}
				results.WriteString(rel + "\n")
				count++
			}
			return nil
		})

		var notes strings.Builder
		if walkErr != nil {
			fmt.Fprintf(&notes, "\n[search incomplete: %v]\n", walkErr)
		}
		for _, s := range skipped {
			notes.WriteString("[skipped " + s + "]\n")
		}

		if count == 0 {
			return "No files found matching: " + params.Pattern + "\n" + notes.String(), nil
		}
		return results.String() + notes.String(), nil
	}
}

func codeSearch(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Pattern string `json:"pattern"`
			Glob    string `json:"glob,omitempty"` // optional file pattern filter
		}
		if err := unmarshalArgs("code_search", args, &params); err != nil {
			return "", err
		}
		if params.Pattern == "" {
			return "", fmt.Errorf("code_search: 'pattern' is required")
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
		output, err := runCommandBounded(ctx, cmd, searchCommandTimeout)
		if err == nil {
			return output, nil
		}
		if ctx.Err() != nil {
			return output, ctx.Err()
		}

		// Fallback: use grep
		cmd = exec.CommandContext(ctx, "grep", "-rn", "--max-count=50", params.Pattern, ".")
		cmd.Dir = wd
		output, err = runCommandBounded(ctx, cmd, searchCommandTimeout)
		if err != nil && output == "" {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "No matches found for: " + params.Pattern, nil
		}
		return output, nil
	}
}

func webSearch() func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Query string `json:"query"`
		}
		if err := unmarshalArgs("web_search", args, &params); err != nil {
			return "", err
		}
		if strings.TrimSpace(params.Query) == "" {
			return "", fmt.Errorf("web_search: 'query' is required")
		}

		// Use DuckDuckGo HTML lite (no API key required)
		searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(params.Query)

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
		if err := unmarshalArgs("ask_user", args, &params); err != nil {
			return "", err
		}
		if strings.TrimSpace(params.Question) == "" {
			return "", fmt.Errorf("ask_user: 'question' is required")
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
		if err := unmarshalArgs("symbols", args, &params); err != nil {
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

	// Canonicalize the target (follow symlinks). For a path that does not exist
	// yet — write_file, apply_patch add — EvalSymlinks fails, so resolve the
	// longest existing ancestor instead: otherwise a symlinked parent directory
	// pointing outside the workspace would silently pass the boundary check.
	canonicalResolved, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		canonicalResolved = resolveExistingAncestor(resolved)
	}

	if canonicalResolved != canonicalRoot &&
		!strings.HasPrefix(canonicalResolved, canonicalRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path %q is outside the project directory", path)
	}
	return resolved, nil
}

// resolveExistingAncestor canonicalizes a path that does not exist by resolving
// symlinks on its longest existing prefix and re-appending the missing
// components. filepath.EvalSymlinks fails outright on missing paths, which
// would otherwise leave a symlinked parent unresolved.
func resolveExistingAncestor(path string) string {
	cur := path
	rest := ""
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return path // reached the filesystem root, nothing resolvable
		}
		if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(resolvedParent, filepath.Base(cur), rest)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
