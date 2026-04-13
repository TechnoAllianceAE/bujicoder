// Package permissions implements a 6-mode tool permission system that controls
// which tools can execute without user approval.
package permissions

import (
	"path/filepath"
	"strings"
)

// Permission modes.
const (
	ModeDefault    = "default"           // Ask for non-read-only ops
	ModeBypass     = "bypassPermissions" // Allow everything
	ModePlan       = "plan"              // Ask for everything (dry-run)
	ModeDontAsk    = "dontAsk"           // Deny all non-read-only
	ModeAcceptEdit = "acceptEdits"       // Auto-allow file edits, ask for rest
	ModeAuto       = "auto"             // Allow everything silently
)

// Rule represents a permission rule from settings.
type Rule struct {
	Source   string // "userSettings", "projectSettings", "session"
	Behavior string // "allow", "deny", "ask"
	ToolName string
	Pattern  string // Optional gitignore-style pattern
}

// Result is the outcome of a permission check.
type Result struct {
	Behavior string // "allow", "deny", "ask"
	Reason   string
}

// Checker evaluates tool permissions.
type Checker struct {
	Mode       string
	AllowRules []Rule
	DenyRules  []Rule

	// DangerousPatterns are file paths that always require confirmation.
	DangerousPatterns []string
}

// NewChecker creates a permission checker with default dangerous patterns.
func NewChecker(mode string) *Checker {
	if mode == "" {
		mode = ModeDefault
	}
	return &Checker{
		Mode: mode,
		DangerousPatterns: []string{
			"**/.env",
			"**/.env.*",
			"**/credentials*",
			"**/secrets*",
			"**/*.pem",
			"**/*.key",
			"**/id_rsa*",
			"**/.ssh/*",
			"**/settings.json",
			"**/settings.local.json",
		},
	}
}

// Check evaluates whether a tool use is permitted.
func (pc *Checker) Check(toolName string, input map[string]any, isReadOnly bool) Result {
	normalized := normalizeToolName(toolName)

	// Bypass mode: allow everything
	if pc.Mode == ModeBypass || pc.Mode == ModeAuto {
		return Result{Behavior: "allow"}
	}

	// DontAsk mode: deny everything that isn't read-only
	if pc.Mode == ModeDontAsk {
		if isReadOnly {
			return Result{Behavior: "allow"}
		}
		return Result{Behavior: "deny", Reason: "permission mode is dontAsk"}
	}

	// Plan mode: always ask
	if pc.Mode == ModePlan {
		return Result{Behavior: "ask", Reason: "plan mode — confirm before executing"}
	}

	// Check deny rules first
	for _, rule := range pc.DenyRules {
		if matchesRule(rule, normalized, input) {
			return Result{Behavior: "deny", Reason: "denied by rule: " + rule.Pattern}
		}
	}

	// Check allow rules
	for _, rule := range pc.AllowRules {
		if matchesRule(rule, normalized, input) {
			return Result{Behavior: "allow"}
		}
	}

	// Read-only tools are always allowed
	if isReadOnly {
		return Result{Behavior: "allow"}
	}

	// Check dangerous file patterns
	if filePath := extractFilePath(input); filePath != "" {
		if pc.isDangerousPath(filePath) {
			return Result{Behavior: "ask", Reason: "potentially sensitive file: " + filePath}
		}
	}

	// Check dangerous shell commands
	if normalized == "run_terminal_command" {
		if cmd, ok := input["command"].(string); ok {
			if IsDangerousCommand(cmd) {
				return Result{Behavior: "ask", Reason: "potentially destructive command"}
			}
		}
	}

	// AcceptEdits mode: allow file edits, ask for everything else
	if pc.Mode == ModeAcceptEdit {
		if normalized == "str_replace" || normalized == "write_file" || normalized == "multi_edit" || normalized == "apply_patch" {
			return Result{Behavior: "allow"}
		}
		return Result{Behavior: "ask", Reason: "acceptEdits mode only auto-allows file edits"}
	}

	// Default mode: ask for non-read-only operations
	if pc.Mode == ModeDefault {
		return Result{Behavior: "ask", Reason: "requires permission"}
	}

	return Result{Behavior: "allow"}
}

// AddAllowRule adds a session-scoped allow rule.
func (pc *Checker) AddAllowRule(toolName, pattern string) {
	pc.AllowRules = append(pc.AllowRules, Rule{
		Source:   "session",
		Behavior: "allow",
		ToolName: normalizeToolName(toolName),
		Pattern:  pattern,
	})
}

// matchesRule checks if a rule applies to a tool/input combination.
func matchesRule(rule Rule, toolName string, input map[string]any) bool {
	if rule.ToolName != "" && normalizeToolName(rule.ToolName) != toolName {
		return false
	}
	if rule.Pattern == "" {
		return true
	}

	filePath := extractFilePath(input)
	if filePath == "" {
		if cmd, ok := input["command"].(string); ok {
			return matchPattern(rule.Pattern, cmd)
		}
		return false
	}
	return matchPattern(rule.Pattern, filePath)
}

func extractFilePath(input map[string]any) string {
	if input == nil {
		return ""
	}
	if p, ok := input["file_path"].(string); ok {
		return p
	}
	if p, ok := input["path"].(string); ok {
		return p
	}
	return ""
}

func matchPattern(pattern, value string) bool {
	matched, _ := filepath.Match(pattern, filepath.Base(value))
	if matched {
		return true
	}
	matched, _ = filepath.Match(pattern, value)
	return matched
}

func (pc *Checker) isDangerousPath(path string) bool {
	base := filepath.Base(path)
	dir := filepath.Dir(path)

	for _, pattern := range pc.DangerousPatterns {
		cleanPattern := strings.TrimPrefix(pattern, "**/")

		if strings.Contains(cleanPattern, "/") {
			parts := strings.SplitN(cleanPattern, "/", 2)
			dirPattern := parts[0]
			if strings.Contains(dir, dirPattern) {
				return true
			}
			continue
		}

		if matched, _ := filepath.Match(cleanPattern, base); matched {
			return true
		}
	}
	return false
}

// IsDangerousCommand checks for destructive shell commands.
func IsDangerousCommand(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))

	dangerous := []string{
		"rm -rf", "rm -r", "rmdir",
		"git push --force", "git push -f",
		"git reset --hard",
		"git checkout .",
		"git clean -f",
		"git branch -d",
		"drop table", "drop database",
		"truncate table",
		"kill -9",
		"pkill",
		"shutdown",
		"reboot",
		"format ",
		"mkfs",
		"dd if=",
		"> /dev/",
		"chmod 777",
	}

	pipePatterns := []string{"curl", "wget"}
	pipeTargets := []string{
		"| sh", "| bash", "|sh", "|bash",
		"| /bin/sh", "| /bin/bash",
	}

	for _, d := range dangerous {
		if strings.Contains(lower, d) {
			return true
		}
	}

	for _, src := range pipePatterns {
		if strings.Contains(lower, src) {
			for _, target := range pipeTargets {
				if strings.Contains(lower, target) {
					return true
				}
			}
		}
	}

	return false
}

// normalizeToolName maps bc2-style tool names to buji-style names.
func normalizeToolName(name string) string {
	switch name {
	case "Bash", "PowerShell":
		return "run_terminal_command"
	case "Write":
		return "write_file"
	case "Edit":
		return "str_replace"
	case "Read":
		return "read_files"
	case "Glob":
		return "glob"
	case "Grep":
		return "code_search"
	case "WebFetch":
		return "web_fetch"
	case "WebSearch":
		return "web_search"
	default:
		return name
	}
}

// IsReadOnlyTool returns true if the tool only reads data.
func IsReadOnlyTool(name string) bool {
	switch normalizeToolName(name) {
	case "read_files", "glob", "code_search", "symbols", "web_fetch", "web_search",
		"shared_memory_read", "list_snapshots", "think_deeply":
		return true
	}
	return false
}
