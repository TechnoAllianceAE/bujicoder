package tools

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PermissionAction represents what to do when a rule matches.
type PermissionAction string

const (
	ActionAllow PermissionAction = "allow"
	ActionAsk   PermissionAction = "ask"
	ActionDeny  PermissionAction = "deny"
)

// PermissionMode controls the default behaviour for dangerous commands.
type PermissionMode string

const (
	ModeAsk    PermissionMode = "ask"    // prompt user (default)
	ModeYolo   PermissionMode = "yolo"   // auto-approve everything
	ModeStrict PermissionMode = "strict" // hard-block all dangerous commands
)

// CommandRule is a pattern → action mapping for terminal commands.
type CommandRule struct {
	Pattern string           `yaml:"pattern"`
	Action  PermissionAction `yaml:"action"`
}

// ProjectPermissions holds the parsed .bujicoderrc configuration.
type ProjectPermissions struct {
	Mode            PermissionMode            `yaml:"mode"`
	Tools           map[string]PermissionAction `yaml:"tools"`
	Commands        []CommandRule             `yaml:"commands"`
	RestrictedPaths []string                  `yaml:"restricted_paths"`

	// sourceFile is the path from which this config was loaded (for display).
	sourceFile string
}

// SourceFile returns the path from which the config was loaded.
func (p *ProjectPermissions) SourceFile() string {
	if p == nil {
		return ""
	}
	return p.sourceFile
}

// CommandRuleCount returns the number of command rules.
func (p *ProjectPermissions) CommandRuleCount() int {
	if p == nil {
		return 0
	}
	return len(p.Commands)
}

// LoadProjectPermissions searches for a .bujicoderrc file starting from dir
// and walking up to the git root (or filesystem root). Returns nil if no file
// is found.
func LoadProjectPermissions(dir string) *ProjectPermissions {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}

	for {
		candidate := filepath.Join(dir, ".bujicoderrc")
		data, err := os.ReadFile(candidate)
		if err == nil {
			perms := &ProjectPermissions{
				Mode: ModeAsk, // default
			}
			if err := yaml.Unmarshal(data, perms); err != nil {
				return nil // malformed file — treat as absent
			}
			perms.sourceFile = candidate
			// Normalise mode
			switch perms.Mode {
			case ModeAsk, ModeYolo, ModeStrict:
				// valid
			default:
				perms.Mode = ModeAsk
			}
			return perms
		}

		// Stop at git root or filesystem root.
		if isGitRoot(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // filesystem root
		}
		dir = parent
	}
	return nil
}

// CheckCommand matches a terminal command against the command rules.
// Returns the action of the first matching rule, or "" if no rule matches.
func (p *ProjectPermissions) CheckCommand(cmd string) PermissionAction {
	if p == nil {
		return ""
	}
	trimmed := strings.TrimSpace(cmd)
	for _, rule := range p.Commands {
		if matchPattern(rule.Pattern, trimmed) {
			return rule.Action
		}
	}
	return ""
}

// CheckToolPermission returns the permission action for a given tool name.
// Returns "" if no tool-level override is set.
func (p *ProjectPermissions) CheckToolPermission(toolName string) PermissionAction {
	if p == nil || p.Tools == nil {
		return ""
	}
	action, ok := p.Tools[toolName]
	if !ok {
		return ""
	}
	return action
}

// IsPathRestricted checks whether a relative path matches any restricted path
// pattern. Paths should be relative to the project root.
func (p *ProjectPermissions) IsPathRestricted(path string) bool {
	if p == nil {
		return false
	}
	// Normalise to forward slashes for matching.
	normalized := filepath.ToSlash(filepath.Clean(path))
	for _, pattern := range p.RestrictedPaths {
		pattern = filepath.ToSlash(pattern)
		if matchPattern(pattern, normalized) {
			return true
		}
		// Also match against the basename for simple patterns like ".env".
		base := filepath.Base(normalized)
		if !strings.Contains(pattern, "/") && matchPattern(pattern, base) {
			return true
		}
	}
	return false
}

// matchPattern does simple glob-style matching.
// For command patterns: * matches any characters (including /).
// For path patterns: ** matches across directory separators.
func matchPattern(pattern, value string) bool {
	if strings.Contains(pattern, "**") {
		return matchDoubleGlob(pattern, value)
	}
	return simpleGlob(pattern, value)
}

// simpleGlob matches a pattern where * means "any characters" (including /).
// This is intentionally different from filepath.Match where * does not match /.
func simpleGlob(pattern, value string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Skip consecutive stars.
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true // trailing * matches everything
			}
			// Try matching the rest of the pattern at every position.
			for i := 0; i <= len(value); i++ {
				if simpleGlob(pattern, value[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(value) == 0 {
				return false
			}
			pattern = pattern[1:]
			value = value[1:]
		default:
			if len(value) == 0 || pattern[0] != value[0] {
				return false
			}
			pattern = pattern[1:]
			value = value[1:]
		}
	}
	return len(value) == 0
}

// matchDoubleGlob handles ** patterns that match any path segments.
func matchDoubleGlob(pattern, value string) bool {
	// Replace ** with a single * and use simpleGlob which already
	// matches across separators.
	collapsed := strings.ReplaceAll(pattern, "**", "*")
	// Collapse multiple consecutive stars.
	for strings.Contains(collapsed, "**") {
		collapsed = strings.ReplaceAll(collapsed, "**", "*")
	}
	return simpleGlob(collapsed, value)
}

// isGitRoot checks whether dir contains a .git directory or file.
func isGitRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
