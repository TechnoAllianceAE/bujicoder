// Package memory implements persistent cross-session project memories.
// Memories are stored as .md files with YAML frontmatter under
// ~/.bujicoder/projects/<sanitized-path>/memory/.
package memory

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EntryType classifies a memory entry.
type EntryType string

const (
	TypeUser      EntryType = "user"
	TypeFeedback  EntryType = "feedback"
	TypeProject   EntryType = "project"
	TypeReference EntryType = "reference"
)

// Entry represents a single memory record.
type Entry struct {
	Name        string
	Description string
	Type        EntryType
	Content     string
	FilePath    string
}

// Store manages persistent project memories on disk.
type Store struct {
	baseDir string // e.g. ~/.bujicoder/projects/<hash>/memory/
}

// NewStore creates a memory store for the given project root.
// The store directory is derived by hashing the project path.
func NewStore(configDir, projectRoot string) *Store {
	hash := sha256.Sum256([]byte(projectRoot))
	sanitized := fmt.Sprintf("%x", hash[:8])
	baseDir := filepath.Join(configDir, "projects", sanitized, "memory")
	return &Store{baseDir: baseDir}
}

// GetPrompt formats all memories for injection into the system prompt.
// Returns an empty string if no memories exist.
func (s *Store) GetPrompt() string {
	entries := s.List()
	if len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Persistent Memories (cross-session)\n\n")
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("## %s\n", e.Name))
		if e.Description != "" {
			sb.WriteString(fmt.Sprintf("*%s*\n\n", e.Description))
		}
		sb.WriteString(e.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// List returns all memory entries from disk.
func (s *Store) List() []Entry {
	if _, err := os.Stat(s.baseDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil
	}

	var result []Entry
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") || de.Name() == "MEMORY.md" {
			continue
		}
		fp := filepath.Join(s.baseDir, de.Name())
		entry := parseMemoryFile(fp)
		if entry != nil {
			result = append(result, *entry)
		}
	}
	return result
}

// Write saves a memory entry to disk.
func (s *Store) Write(name, description string, memType EntryType, content string) error {
	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return err
	}

	fileName := sanitizeFileName(name) + ".md"
	fp := filepath.Join(s.baseDir, fileName)

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", name))
	sb.WriteString(fmt.Sprintf("description: %s\n", description))
	sb.WriteString(fmt.Sprintf("type: %s\n", memType))
	sb.WriteString("---\n\n")
	sb.WriteString(content)
	sb.WriteString("\n")

	return os.WriteFile(fp, []byte(sb.String()), 0644)
}

// Delete removes a memory entry by name.
func (s *Store) Delete(name string) error {
	fileName := sanitizeFileName(name) + ".md"
	fp := filepath.Join(s.baseDir, fileName)
	return os.Remove(fp)
}

// parseMemoryFile reads a .md file with YAML frontmatter and returns an Entry.
func parseMemoryFile(fp string) *Entry {
	data, err := os.ReadFile(fp)
	if err != nil {
		return nil
	}

	content := string(data)
	entry := &Entry{FilePath: fp}

	// Parse YAML frontmatter (between --- delimiters)
	if strings.HasPrefix(content, "---\n") {
		endIdx := strings.Index(content[4:], "\n---")
		if endIdx >= 0 {
			frontmatter := content[4 : 4+endIdx]
			entry.Content = strings.TrimSpace(content[4+endIdx+4:])

			for _, line := range strings.Split(frontmatter, "\n") {
				key, val, ok := parseYAMLLine(line)
				if !ok {
					continue
				}
				switch key {
				case "name":
					entry.Name = val
				case "description":
					entry.Description = val
				case "type":
					entry.Type = EntryType(val)
				}
			}
		} else {
			entry.Content = content
		}
	} else {
		entry.Content = content
		entry.Name = strings.TrimSuffix(filepath.Base(fp), ".md")
	}

	if entry.Name == "" {
		entry.Name = strings.TrimSuffix(filepath.Base(fp), ".md")
	}

	return entry
}

func parseYAMLLine(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	return key, value, true
}

func sanitizeFileName(name string) string {
	name = strings.ToLower(name)
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}
