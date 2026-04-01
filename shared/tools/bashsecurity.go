package tools

import (
	"fmt"
	"strings"
)

// ThreatLevel classifies the severity of a command's risk.
type ThreatLevel int

const (
	ThreatNone     ThreatLevel = iota // Safe command
	ThreatLow                         // Mildly risky (e.g. writing to a file)
	ThreatMedium                      // Potentially destructive (e.g. rm without -r)
	ThreatHigh                        // Destructive (e.g. rm -rf, git push --force)
	ThreatCritical                    // Extremely dangerous (e.g. dd, mkfs, > /dev/sda)
)

// SecurityVerdict holds the analysis result for a shell command.
type SecurityVerdict struct {
	Level    ThreatLevel
	Reason   string
	Command  string
	Blocked  bool   // Whether this should be blocked outright
	NeedsApproval bool // Whether this needs user confirmation
}

// String returns a human-readable description of the threat level.
func (t ThreatLevel) String() string {
	switch t {
	case ThreatNone:
		return "safe"
	case ThreatLow:
		return "low"
	case ThreatMedium:
		return "medium"
	case ThreatHigh:
		return "high"
	case ThreatCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// destructivePattern defines a pattern to match and its associated threat.
type destructivePattern struct {
	// match is a function that checks if a command matches this pattern.
	match  func(cmd, lower string) bool
	level  ThreatLevel
	reason string
}

// securityPatterns defines all the destructive command patterns, ordered by severity.
var securityPatterns = []destructivePattern{
	// === CRITICAL: Meta-execution patterns (must be checked first) ===
	{
		match: func(_, lower string) bool {
			// Detect command substitution: $(...) and backticks
			return strings.Contains(lower, "$(") || strings.Contains(lower, "`")
		},
		level:  ThreatMedium,
		reason: "command substitution detected — embedded commands may bypass security analysis",
	},
	{
		match: func(_, lower string) bool {
			// Detect indirect execution: eval, bash -c, sh -c, python -c, perl -e, ruby -e
			return hasCommand(lower, "eval") ||
				(hasCommand(lower, "bash") && strings.Contains(lower, " -c")) ||
				(hasCommand(lower, "sh") && strings.Contains(lower, " -c")) ||
				(hasCommand(lower, "python") && strings.Contains(lower, " -c")) ||
				(hasCommand(lower, "python3") && strings.Contains(lower, " -c")) ||
				(hasCommand(lower, "perl") && strings.Contains(lower, " -e")) ||
				(hasCommand(lower, "ruby") && strings.Contains(lower, " -e"))
		},
		level:  ThreatHigh,
		reason: "indirect code execution — the actual command is hidden from security analysis",
	},
	{
		match: func(_, lower string) bool {
			// Detect find -exec / -execdir (execution vector that hides the real command)
			return hasCommand(lower, "find") && (strings.Contains(lower, "-exec") || strings.Contains(lower, "-execdir"))
		},
		level:  ThreatMedium,
		reason: "find with -exec/-execdir — executes commands on matched files",
	},

	// === CRITICAL: System-level destruction ===
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "mkfs") || strings.Contains(lower, "mkfs.")
		},
		level:  ThreatCritical,
		reason: "mkfs formats disk partitions — will destroy all data",
	},
	{
		match:  func(_, lower string) bool { return hasCommand(lower, "dd") && strings.Contains(lower, "of=/dev/") },
		level:  ThreatCritical,
		reason: "dd writing to block device — will overwrite raw disk",
	},
	{
		match: func(_, lower string) bool {
			return strings.Contains(lower, "> /dev/sd") || strings.Contains(lower, "> /dev/nvme") ||
				strings.Contains(lower, "> /dev/hd")
		},
		level:  ThreatCritical,
		reason: "redirect to block device — will overwrite raw disk",
	},
	{
		match:  func(_, lower string) bool { return hasCommand(lower, ":(){ :|:& };:") || strings.Contains(lower, "fork bomb") },
		level:  ThreatCritical,
		reason: "fork bomb — will crash the system",
	},
	{
		match: func(_, lower string) bool {
			// Match "rm -rf /" but not "rm -rf /tmp/foo" — the target must be / or /*
			return strings.Contains(lower, "rm -rf /") && !strings.Contains(lower, "rm -rf /.")  &&
				(strings.HasSuffix(strings.TrimSpace(lower), "rm -rf /") ||
					strings.Contains(lower, "rm -rf /*") ||
					strings.Contains(lower, "rm -rf / "))
		},
		level:  ThreatCritical,
		reason: "rm -rf / — will destroy the entire filesystem",
	},
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "chmod") && (strings.Contains(lower, "777") || strings.Contains(lower, "a+rwx")) &&
				(strings.Contains(lower, " /") || strings.Contains(lower, " -R"))
		},
		level:  ThreatCritical,
		reason: "recursive chmod on system paths — will break system permissions",
	},

	// === HIGH: Destructive but recoverable ===
	{
		match: func(_, lower string) bool {
			hasRm := hasCommand(lower, "rm") || strings.Contains(lower, "xargs rm")
			return hasRm && (strings.Contains(lower, "-rf") || strings.Contains(lower, "-r ") ||
				strings.Contains(lower, " -fr") || strings.Contains(lower, "-Rf"))
		},
		level:  ThreatHigh,
		reason: "recursive delete — removes directory trees permanently",
	},
	{
		match: func(_, lower string) bool {
			return strings.Contains(lower, "git push") && (strings.Contains(lower, "--force") || strings.Contains(lower, " -f"))
		},
		level:  ThreatHigh,
		reason: "force push — will overwrite remote history, potentially losing others' work",
	},
	{
		match: func(_, lower string) bool {
			return strings.Contains(lower, "git reset") && strings.Contains(lower, "--hard")
		},
		level:  ThreatHigh,
		reason: "git reset --hard — discards all uncommitted changes permanently",
	},
	{
		match: func(_, lower string) bool {
			return strings.Contains(lower, "git clean") && (strings.Contains(lower, "-f") || strings.Contains(lower, "--force"))
		},
		level:  ThreatHigh,
		reason: "git clean -f — deletes untracked files permanently",
	},
	{
		match: func(_, lower string) bool {
			return strings.Contains(lower, "git checkout") && strings.Contains(lower, "-- .")
		},
		level:  ThreatHigh,
		reason: "git checkout -- . — discards all working directory changes",
	},
	{
		match:  func(_, lower string) bool { return strings.Contains(lower, "git restore .") },
		level:  ThreatHigh,
		reason: "git restore . — discards all working directory changes",
	},
	{
		match: func(cmd, lower string) bool {
			return strings.Contains(lower, "git branch") && strings.Contains(cmd, "-D")
		},
		level:  ThreatHigh,
		reason: "force-deleting a git branch — may lose unmerged work",
	},
	{
		match: func(_, lower string) bool {
			return hasAnySQL(lower, "drop table", "drop database", "truncate table", "delete from") &&
				!strings.Contains(lower, "where")
		},
		level:  ThreatHigh,
		reason: "destructive SQL operation — data loss risk",
	},
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "kill") && strings.Contains(lower, "-9")
		},
		level:  ThreatHigh,
		reason: "kill -9 — forcefully terminates process without cleanup",
	},
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "pkill") || hasCommand(lower, "killall")
		},
		level:  ThreatHigh,
		reason: "pattern-based process killing — may terminate unintended processes",
	},

	// === MEDIUM: Needs caution ===
	{
		match:  func(_, lower string) bool { return strings.Contains(lower, "git push") },
		level:  ThreatMedium,
		reason: "git push — publishes local commits to remote repository",
	},
	{
		match:  func(_, lower string) bool { return hasCommand(lower, "rm") && !strings.Contains(lower, "-r") },
		level:  ThreatMedium,
		reason: "file deletion",
	},
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "mv") && (strings.Contains(lower, " /") || strings.Contains(lower, " ~/"))
		},
		level:  ThreatMedium,
		reason: "moving files to/from system paths",
	},
	{
		match:  func(_, lower string) bool { return hasCommand(lower, "chmod") },
		level:  ThreatMedium,
		reason: "changing file permissions",
	},
	{
		match:  func(_, lower string) bool { return hasCommand(lower, "chown") },
		level:  ThreatMedium,
		reason: "changing file ownership",
	},
	{
		match: func(_, lower string) bool {
			hasDownloader := strings.Contains(lower, "curl") || strings.Contains(lower, "wget")
			hasPipeToShell := strings.Contains(lower, "| sh") || strings.Contains(lower, "| bash") || strings.Contains(lower, "| zsh")
			return hasDownloader && hasPipeToShell
		},
		level:  ThreatMedium,
		reason: "piping remote content to shell — executes untrusted code",
	},
	{
		match: func(_, lower string) bool {
			return (hasCommand(lower, "npm") || hasCommand(lower, "pip") || hasCommand(lower, "gem") ||
				hasCommand(lower, "cargo")) && strings.Contains(lower, "install") && strings.Contains(lower, "-g")
		},
		level:  ThreatMedium,
		reason: "global package installation — affects system-wide environment",
	},
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "docker") && (strings.Contains(lower, "rm") || strings.Contains(lower, "prune") ||
				strings.Contains(lower, "stop") || strings.Contains(lower, "kill"))
		},
		level:  ThreatMedium,
		reason: "Docker container/image management — may remove running containers",
	},
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "systemctl") && (strings.Contains(lower, "stop") ||
				strings.Contains(lower, "restart") || strings.Contains(lower, "disable"))
		},
		level:  ThreatMedium,
		reason: "managing system services",
	},

	// === LOW: Watch but allow ===
	{
		match: func(_, lower string) bool {
			return hasCommand(lower, "npm") && strings.Contains(lower, "install") && !strings.Contains(lower, "-g")
		},
		level:  ThreatLow,
		reason: "installing packages locally",
	},

	// === SENSITIVE PATH ACCESS ===
	{
		match: func(_, lower string) bool {
			return containsAnySensitivePath(lower)
		},
		level:  ThreatHigh,
		reason: "accessing sensitive system paths (SSH keys, credentials, etc.)",
	},
}

// AnalyzeCommand performs comprehensive security analysis on a shell command.
// It checks against all known destructive patterns and returns a verdict.
func AnalyzeCommand(cmd string) SecurityVerdict {
	trimmed := strings.TrimSpace(cmd)
	lower := strings.ToLower(trimmed)

	// Always check the full command first (catches cross-segment patterns like "xargs rm -rf").
	fullVerdict := analyzeSingleCommand(trimmed, lower)
	if fullVerdict.Level > ThreatNone {
		return fullVerdict
	}

	// Split into segments respecting quoted strings, then check each segment.
	segments := splitCommandSegments(trimmed)
	if len(segments) > 1 {
		var worst SecurityVerdict
		for _, seg := range segments {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			v := analyzeSingleCommand(seg, strings.ToLower(seg))
			if v.Level > worst.Level {
				worst = v
			}
		}
		if worst.Level > ThreatNone {
			worst.Command = trimmed
			return worst
		}
	}

	return SecurityVerdict{Level: ThreatNone, Command: trimmed}
}

// splitCommandSegments splits a command string on shell operators (|, &&, ||, ;, \n)
// while respecting single and double quoted strings to avoid false splits.
func splitCommandSegments(cmd string) []string {
	var segments []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	runes := []rune(cmd)

	for i := 0; i < len(runes); i++ {
		c := runes[i]

		// Track quote state.
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			current.WriteRune(c)
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			current.WriteRune(c)
			continue
		}

		// Only split when outside quotes.
		if !inSingle && !inDouble {
			// Newline is a command separator.
			if c == '\n' {
				segments = append(segments, current.String())
				current.Reset()
				continue
			}
			// Semicolon.
			if c == ';' {
				segments = append(segments, current.String())
				current.Reset()
				continue
			}
			// Pipe: | (but not ||)
			if c == '|' {
				if i+1 < len(runes) && runes[i+1] == '|' {
					// || — split and skip both chars.
					segments = append(segments, current.String())
					current.Reset()
					i++ // skip second |
					continue
				}
				segments = append(segments, current.String())
				current.Reset()
				continue
			}
			// &&
			if c == '&' && i+1 < len(runes) && runes[i+1] == '&' {
				segments = append(segments, current.String())
				current.Reset()
				i++ // skip second &
				continue
			}
		}

		current.WriteRune(c)
	}

	if current.Len() > 0 {
		segments = append(segments, current.String())
	}

	return segments
}

// analyzeSingleCommand checks a single command (no pipes/chains) against patterns.
func analyzeSingleCommand(cmd, lower string) SecurityVerdict {
	for _, pattern := range securityPatterns {
		if pattern.match(cmd, lower) {
			return SecurityVerdict{
				Level:         pattern.level,
				Reason:        pattern.reason,
				Command:       cmd,
				Blocked:       pattern.level >= ThreatCritical,
				NeedsApproval: pattern.level >= ThreatMedium,
			}
		}
	}
	return SecurityVerdict{Level: ThreatNone, Command: cmd}
}

// hasCommand checks if a command string starts with or contains the given command name.
func hasCommand(lower, cmd string) bool {
	// Direct start
	if lower == cmd || strings.HasPrefix(lower, cmd+" ") || strings.HasPrefix(lower, cmd+"\t") {
		return true
	}
	// After sudo
	if strings.HasPrefix(lower, "sudo "+cmd) || strings.HasPrefix(lower, "sudo\t"+cmd) {
		return true
	}
	// After env vars or path
	if strings.Contains(lower, "/"+cmd+" ") || strings.Contains(lower, "/"+cmd+"\t") {
		return true
	}
	return false
}

// hasAnySQL checks if the command contains any SQL destructive statements.
func hasAnySQL(lower string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// sensitivePathPatterns lists paths that should never be accessed by an AI agent.
var sensitivePathPatterns = []string{
	"/etc/passwd", "/etc/shadow", "/etc/sudoers",
	"~/.ssh", "$HOME/.ssh", "${HOME}/.ssh",
	"~/.aws", "$HOME/.aws", "${HOME}/.aws",
	"~/.gnupg", "$HOME/.gnupg", "${HOME}/.gnupg",
	"~/.config/gcloud", "$HOME/.config/gcloud",
	"~/.kube", "$HOME/.kube",
	"~/.docker/config.json",
	"~/.netrc", "$HOME/.netrc",
	"~/.npmrc",
	"/etc/ssl/private",
}

// containsAnySensitivePath checks if the command references sensitive paths.
func containsAnySensitivePath(lower string) bool {
	for _, pat := range sensitivePathPatterns {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

// FormatVerdict returns a user-friendly message for a security verdict.
func FormatVerdict(v SecurityVerdict) string {
	if v.Level == ThreatNone {
		return ""
	}

	prefix := "⚠️"
	if v.Level >= ThreatHigh {
		prefix = "🛑"
	}
	if v.Level >= ThreatCritical {
		prefix = "🚫"
	}

	return fmt.Sprintf("%s [%s] %s\n   Command: %s", prefix, v.Level, v.Reason, v.Command)
}
