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
		var result strings.Builder
		for _, p := range params.Paths {
			if perms.IsPathRestricted(p) {
				result.WriteString(fmt.Sprintf("--- %s ---\nError: access denied: path is restricted by .bujicoderrc\n\n", p))
				continue
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

		if perms.IsPathRestricted(params.Path) {
			return "", fmt.Errorf("access denied: path %q is restricted by .bujicoderrc", params.Path)
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
		return fmt.Sprintf("Wrote %d bytes to %s", len(params.Content), params.Path), nil
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

		if perms.IsPathRestricted(params.Path) {
			return "", fmt.Errorf("access denied: path %q is restricted by .bujicoderrc", params.Path)
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
		if !strings.Contains(content, params.OldStr) {
			return "", fmt.Errorf("old_str not found in %s", params.Path)
		}

		newContent := strings.Replace(content, params.OldStr, params.NewStr, 1)
		if err := os.WriteFile(absPath, []byte(newContent), 0o644); err != nil {
			return "", err
		}
		return "Replacement applied", nil
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

// isDangerousCommand checks if a shell command is destructive or attempts to
// access paths outside the project directory. Returns true and a reason if blocked.
func isDangerousCommand(cmd string) (bool, string) {
	lower := strings.ToLower(strings.TrimSpace(cmd))

	// Block access to sensitive absolute paths outside the project.
	sensitivePatterns := []string{
		"/etc/", "/etc/passwd", "/etc/shadow",
		"~/.ssh", "$HOME/.ssh", "${HOME}/.ssh",
		"~/.aws", "$HOME/.aws", "${HOME}/.aws",
		"~/.gnupg", "$HOME/.gnupg", "${HOME}/.gnupg",
		"~/.config", "$HOME/.config", "${HOME}/.config",
	}
	for _, pat := range sensitivePatterns {
		if strings.Contains(lower, pat) {
			return true, fmt.Sprintf("access to %s is restricted for security", pat)
		}
	}

	// Git push (any variant)
	if strings.Contains(lower, "git push") {
		return true, "git push requires user confirmation"
	}
	// Git force/destructive operations
	if strings.Contains(lower, "git reset") && strings.Contains(lower, "--hard") {
		return true, "git reset --hard is destructive and requires user confirmation"
	}
	if strings.Contains(lower, "git clean") && (strings.Contains(lower, "-f") || strings.Contains(lower, "--force")) {
		return true, "git clean requires user confirmation"
	}
	if strings.Contains(lower, "git checkout") && strings.Contains(lower, "-- .") {
		return true, "discarding all changes requires user confirmation"
	}
	if strings.Contains(lower, "git restore .") || strings.Contains(lower, "git restore --staged .") {
		return true, "restoring all files requires user confirmation"
	}
	// -D is case-sensitive (uppercase D = force delete) — check original cmd
	if strings.Contains(lower, "git branch") && strings.Contains(cmd, "-D") {
		return true, "force-deleting a branch requires user confirmation"
	}
	// Destructive file operations
	if strings.Contains(lower, "rm ") && (strings.Contains(lower, "-rf") || strings.Contains(lower, "-r ") || strings.Contains(lower, " -fr")) {
		return true, "recursive delete requires user confirmation"
	}

	return false, ""
}

func runTerminalCommand(workDir string, approvalFn ApprovalFunc, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		// 1. Check .bujicoderrc command rules (first match wins).
		if action := perms.CheckCommand(params.Command); action != "" {
			switch action {
			case ActionAllow:
				// Explicitly allowed — skip all further checks.
				goto execute
			case ActionDeny:
				return "", fmt.Errorf("BLOCKED by .bujicoderrc: command matches a deny rule.\nCommand: %s", params.Command)
			case ActionAsk:
				// Fall through to dangerous-command check + approval flow.
			}
		}

		// 2. Check isDangerousCommand (hardcoded list).
		if blocked, reason := isDangerousCommand(params.Command); blocked {
			// 3. Apply permission mode.
			if perms != nil && perms.Mode == ModeYolo {
				// Yolo mode: auto-approve dangerous commands.
				goto execute
			}
			if perms != nil && perms.Mode == ModeStrict {
				return "", fmt.Errorf("BLOCKED (strict mode): %s. This command was not executed.\nCommand: %s", reason, params.Command)
			}

			// Ask mode (default).
			if approvalFn == nil {
				return "", fmt.Errorf("BLOCKED: %s. This command was not executed.\nPlease inform the user and let them run it manually.\nCommand: %s", reason, params.Command)
			}
			approved, err := approvalFn(params.Command, reason)
			if err != nil {
				return "", fmt.Errorf("approval error: %w", err)
			}
			if !approved {
				return "", fmt.Errorf("DENIED: user declined to run this command.\nCommand: %s\nReason: %s", params.Command, reason)
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

// safePath resolves a path relative to workDir and ensures the result stays
// within workDir. Returns an error if the path escapes the boundary.
func safePath(workDir, path string) (string, error) {
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
