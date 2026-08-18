// Package skills implements markdown-based custom slash commands.
// Skills are .md files with YAML frontmatter loaded from
// ~/.bujicoder/skills/ (user) and .bujicoder/skills/ (project).
package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a loaded skill definition.
type Skill struct {
	Name         string
	Description  string
	Content      string // Markdown body injected as instruction
	WhenToUse    string
	AllowedTools []string // If non-empty, restricts available tools during execution
	FilePath     string
	Source       string // "user", "project"
}

// Loader discovers and loads skills from config directories.
type Loader struct {
	skills map[string]*Skill
}

// NewLoader creates a skill loader that reads from the given directories.
// configDir is typically ~/.bujicoder/, projectRoot is the working directory.
func NewLoader(configDir, projectRoot string) *Loader {
	sl := &Loader{
		skills: make(map[string]*Skill),
	}

	// User skills: ~/.bujicoder/skills/
	sl.loadFromDir(filepath.Join(configDir, "skills"), "user")

	// Project skills: .bujicoder/skills/
	if projectRoot != "" {
		sl.loadFromDir(filepath.Join(projectRoot, ".bujicoder", "skills"), "project")
	}

	return sl
}

// GetSkills returns all loaded skills.
func (sl *Loader) GetSkills() map[string]*Skill {
	return sl.skills
}

// GetSkill returns a skill by name, or nil if not found.
func (sl *Loader) GetSkill(name string) *Skill {
	return sl.skills[name]
}

// FilterTools returns the intersection of agentTools and skill.AllowedTools.
// If AllowedTools is empty, returns agentTools unchanged.
// Returns nil if the intersection is empty (caller should warn user).
func (s *Skill) FilterTools(agentTools []string) []string {
	if len(s.AllowedTools) == 0 {
		return agentTools
	}

	allowed := make(map[string]bool, len(s.AllowedTools))
	for _, t := range s.AllowedTools {
		allowed[t] = true
	}

	var result []string
	for _, t := range agentTools {
		if allowed[t] {
			result = append(result, t)
		}
	}
	return result
}

func (sl *Loader) loadFromDir(dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Check for SKILL.md inside directory
			skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
			if data, err := os.ReadFile(skillFile); err == nil {
				skill := parseSkillFile(string(data), skillFile, source)
				if skill.Name == "" {
					skill.Name = entry.Name()
				}
				sl.skills[skill.Name] = skill
			}
		} else if strings.HasSuffix(entry.Name(), ".md") {
			path := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			skill := parseSkillFile(string(data), path, source)
			if skill.Name == "" {
				skill.Name = strings.TrimSuffix(entry.Name(), ".md")
			}
			sl.skills[skill.Name] = skill
		}
	}
}

func parseSkillFile(content, path, source string) *Skill {
	skill := &Skill{
		FilePath: path,
		Source:   source,
	}

	// Normalise line endings first: a skill authored on Windows starts with
	// "---\r\n", which used to miss the frontmatter check entirely — the whole
	// file became the body and name/allowed-tools were silently dropped, so a
	// skill that meant to restrict tools ran with no restriction at all.
	content = strings.ReplaceAll(content, "\r\n", "\n")

	if strings.HasPrefix(content, "---\n") {
		parts := strings.SplitN(content[4:], "\n---\n", 2)
		if len(parts) == 2 {
			for _, line := range strings.Split(parts[0], "\n") {
				key, val, ok := splitKV(line)
				if !ok {
					continue
				}
				switch key {
				case "name":
					skill.Name = val
				case "description":
					skill.Description = val
				case "when-to-use":
					skill.WhenToUse = val
				case "allowed-tools":
					for _, t := range strings.Split(val, ",") {
						t = strings.TrimSpace(t)
						if t != "" {
							skill.AllowedTools = append(skill.AllowedTools, t)
						}
					}
				}
			}
			skill.Content = strings.TrimSpace(parts[1])
		} else {
			skill.Content = content
		}
	} else {
		skill.Content = content
	}

	return skill
}

// splitKV splits a frontmatter line on its first colon. A missing space after
// the colon ("name:foo") is accepted: rejecting it silently discarded the whole
// key, including allowed-tools.
func splitKV(line string) (key, value string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(line[idx+1:]), true
}
