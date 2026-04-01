package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxMemoryFileSize caps the BUJI.md file size to prevent unbounded growth
// that would consume too much system prompt context.
const maxMemoryFileSize = 32000 // ~32KB

// memoryMu serializes read-modify-write operations on the memory file
// to prevent corruption from concurrent agent writes.
var memoryMu sync.Mutex

// memoryRead returns a tool that reads the project memory file (BUJI.md).
func memoryRead(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		wd := effectiveWorkDir(ctx, workDir)
		content := ReadProjectMemory(wd)
		if content == "" {
			return "No project memory found. Create one with memory_write.", nil
		}
		return content, nil
	}
}

// memoryWrite returns a tool that appends or updates the project memory file (BUJI.md).
func memoryWrite(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Section string `json:"section"` // Section header (e.g. "Architecture", "Conventions")
			Content string `json:"content"` // Content to write under the section
			Replace bool   `json:"replace"` // If true, replace the section; if false, append
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		if params.Section == "" || params.Content == "" {
			return "", fmt.Errorf("both 'section' and 'content' are required")
		}

		if IsPlanMode(ctx) {
			return "", fmt.Errorf("BLOCKED (plan mode): memory_write is not allowed in plan mode")
		}

		wd := effectiveWorkDir(ctx, workDir)
		memDir := filepath.Join(wd, ".bujicoder")
		memFile := filepath.Join(memDir, "BUJI.md")

		// Validate path stays within project root (#10 — safePath consistency).
		if _, err := safePath(wd, filepath.Join(".bujicoder", "BUJI.md")); err != nil {
			return "", fmt.Errorf("memory path validation failed: %w", err)
		}

		// Ensure .bujicoder directory exists.
		if err := os.MkdirAll(memDir, 0o755); err != nil {
			return "", fmt.Errorf("create memory directory: %w", err)
		}

		// Serialize read-modify-write to prevent corruption (#23).
		memoryMu.Lock()
		defer memoryMu.Unlock()

		// Read existing content or start fresh.
		existing := ""
		if data, err := os.ReadFile(memFile); err == nil {
			existing = string(data)
		}

		if existing == "" {
			// Create new memory file with header.
			existing = "# BujiCoder Project Memory\n\n" +
				"> This file is automatically maintained by BujiCoder.\n" +
				"> It stores project-specific knowledge across sessions.\n" +
				fmt.Sprintf("> Last updated: %s\n\n", time.Now().Format("2006-01-02 15:04"))
		}

		sectionHeader := "## " + params.Section
		newEntry := fmt.Sprintf("\n%s\n\n%s\n", sectionHeader, params.Content)

		if params.Replace {
			existing = replaceSection(existing, sectionHeader, params.Content)
		} else {
			if sectionExistsAtLineStart(existing, sectionHeader) {
				existing = appendToSection(existing, sectionHeader, params.Content)
			} else {
				existing += newEntry
			}
		}

		// Update the "Last updated" line.
		existing = updateTimestamp(existing)

		// Enforce size limit (#34).
		if len(existing) > maxMemoryFileSize {
			existing = existing[:maxMemoryFileSize] + "\n\n> [Memory file truncated — consider cleaning up old sections]\n"
		}

		if err := os.WriteFile(memFile, []byte(existing), 0o644); err != nil {
			return "", fmt.Errorf("write memory file: %w", err)
		}

		return fmt.Sprintf("Memory updated: section '%s' in %s", params.Section, memFile), nil
	}
}

// ReadProjectMemory reads the BUJI.md file from the project's .bujicoder directory
// or falls back to the project root. Returns empty string if not found.
// Note: memory_write always writes to .bujicoder/BUJI.md, so the root fallback
// is for manually created files or repos that pre-date the .bujicoder convention.
func ReadProjectMemory(projectRoot string) string {
	// Priority 1: .bujicoder/BUJI.md
	bujiPath := filepath.Join(projectRoot, ".bujicoder", "BUJI.md")
	if data, err := os.ReadFile(bujiPath); err == nil {
		content := strings.TrimSpace(string(data))
		if content != "" {
			return content
		}
	}

	// Priority 2: BUJI.md in project root (read-only fallback).
	rootPath := filepath.Join(projectRoot, "BUJI.md")
	if data, err := os.ReadFile(rootPath); err == nil {
		content := strings.TrimSpace(string(data))
		if content != "" {
			return content
		}
	}

	return ""
}

// sectionExistsAtLineStart checks if a section header exists at the start of a line.
// This avoids substring matches like "## API" matching "## API Endpoints".
func sectionExistsAtLineStart(doc, sectionHeader string) bool {
	// Check start of document.
	if strings.HasPrefix(doc, sectionHeader+"\n") || doc == sectionHeader {
		return true
	}
	// Check after a newline — require the header to be followed by newline or EOF.
	needle := "\n" + sectionHeader
	idx := strings.Index(doc, needle)
	for idx != -1 {
		afterIdx := idx + len(needle)
		if afterIdx >= len(doc) || doc[afterIdx] == '\n' || doc[afterIdx] == '\r' {
			return true
		}
		// Keep searching.
		next := strings.Index(doc[afterIdx:], needle)
		if next == -1 {
			break
		}
		idx = afterIdx + next
	}
	return false
}

// findSectionStart finds the start index of a section header at a line boundary.
// Returns -1 if not found.
func findSectionStart(doc, sectionHeader string) int {
	// Check start of document.
	if strings.HasPrefix(doc, sectionHeader+"\n") || doc == sectionHeader {
		return 0
	}
	needle := "\n" + sectionHeader
	idx := strings.Index(doc, needle)
	for idx != -1 {
		afterIdx := idx + len(needle)
		if afterIdx >= len(doc) || doc[afterIdx] == '\n' || doc[afterIdx] == '\r' {
			return idx + 1 // +1 to skip the leading \n
		}
		next := strings.Index(doc[afterIdx:], needle)
		if next == -1 {
			break
		}
		idx = afterIdx + next
	}
	return -1
}

// replaceSection replaces the content under a section header.
func replaceSection(doc, sectionHeader, newContent string) string {
	idx := findSectionStart(doc, sectionHeader)
	if idx == -1 {
		return doc + fmt.Sprintf("\n%s\n\n%s\n", sectionHeader, newContent)
	}

	// Find the end of this section (next ## header or end of document).
	afterHeader := idx + len(sectionHeader)
	nextSection := strings.Index(doc[afterHeader:], "\n## ")
	var end int
	if nextSection == -1 {
		end = len(doc)
	} else {
		end = afterHeader + nextSection
	}

	return doc[:idx] + sectionHeader + "\n\n" + newContent + "\n" + doc[end:]
}

// appendToSection appends content to an existing section.
func appendToSection(doc, sectionHeader, content string) string {
	idx := findSectionStart(doc, sectionHeader)
	if idx == -1 {
		return doc + fmt.Sprintf("\n%s\n\n%s\n", sectionHeader, content)
	}

	// Find the end of this section.
	afterHeader := idx + len(sectionHeader)
	nextSection := strings.Index(doc[afterHeader:], "\n## ")
	var insertPoint int
	if nextSection == -1 {
		insertPoint = len(doc)
	} else {
		insertPoint = afterHeader + nextSection
	}

	return doc[:insertPoint] + "\n" + content + "\n" + doc[insertPoint:]
}

// updateTimestamp updates the "Last updated" line in the memory file.
func updateTimestamp(doc string) string {
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "> Last updated:") {
			lines[i] = fmt.Sprintf("> Last updated: %s", time.Now().Format("2006-01-02 15:04"))
			return strings.Join(lines, "\n")
		}
	}
	return doc
}
