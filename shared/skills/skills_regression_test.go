package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// Frontmatter must parse regardless of line endings and colon spacing:
// dropping it silently discards allowed-tools, which turns a tool-restricted
// skill into an unrestricted one.
func TestParseSkillFileFrontmatterVariants(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantName      string
		wantTools     []string
		wantContent   string
		wantWhenToUse string
	}{
		{
			name:        "unix line endings",
			content:     "---\nname: writer\nallowed-tools: read_files, write_file\n---\nBody here",
			wantName:    "writer",
			wantTools:   []string{"read_files", "write_file"},
			wantContent: "Body here",
		},
		{
			name:        "windows line endings",
			content:     "---\r\nname: writer\r\nallowed-tools: read_files, write_file\r\n---\r\nBody here",
			wantName:    "writer",
			wantTools:   []string{"read_files", "write_file"},
			wantContent: "Body here",
		},
		{
			name:        "no space after colon",
			content:     "---\nname:writer\nallowed-tools:read_files\n---\nBody",
			wantName:    "writer",
			wantTools:   []string{"read_files"},
			wantContent: "Body",
		},
		{
			name:          "value containing a colon",
			content:       "---\nname: writer\nwhen-to-use: use when: editing\n---\nBody",
			wantName:      "writer",
			wantWhenToUse: "use when: editing",
			wantContent:   "Body",
		},
		{
			name:        "no frontmatter",
			content:     "Just a body",
			wantContent: "Just a body",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := parseSkillFile(tc.content, "/tmp/x.md", "user")
			if s.Name != tc.wantName {
				t.Fatalf("Name = %q, want %q", s.Name, tc.wantName)
			}
			if len(s.AllowedTools) != len(tc.wantTools) {
				t.Fatalf("AllowedTools = %v, want %v", s.AllowedTools, tc.wantTools)
			}
			for i, want := range tc.wantTools {
				if s.AllowedTools[i] != want {
					t.Fatalf("AllowedTools = %v, want %v", s.AllowedTools, tc.wantTools)
				}
			}
			if s.Content != tc.wantContent {
				t.Fatalf("Content = %q, want %q", s.Content, tc.wantContent)
			}
			if tc.wantWhenToUse != "" && s.WhenToUse != tc.wantWhenToUse {
				t.Fatalf("WhenToUse = %q, want %q", s.WhenToUse, tc.wantWhenToUse)
			}
		})
	}
}

// A CRLF skill must actually restrict tools once its frontmatter is parsed.
func TestFilterToolsAppliesToCRLFSkill(t *testing.T) {
	s := parseSkillFile("---\r\nname: reader\r\nallowed-tools: read_files\r\n---\r\nBody", "/tmp/x.md", "user")
	got := s.FilterTools([]string{"read_files", "run_terminal_command"})
	if len(got) != 1 || got[0] != "read_files" {
		t.Fatalf("FilterTools = %v; a CRLF skill silently lost its tool restriction", got)
	}
}

// Project skills must deterministically override user skills of the same name.
func TestProjectSkillsOverrideUserSkills(t *testing.T) {
	configDir := t.TempDir()
	projectRoot := t.TempDir()

	writeSkill := func(dir, body string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dup.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(filepath.Join(configDir, "skills"), "---\nname: dup\n---\nuser body")
	writeSkill(filepath.Join(projectRoot, ".bujicoder", "skills"), "---\nname: dup\n---\nproject body")

	for range 5 {
		sl := NewLoader(configDir, projectRoot)
		s := sl.GetSkill("dup")
		if s == nil {
			t.Fatal("skill not loaded")
		}
		if s.Content != "project body" || s.Source != "project" {
			t.Fatalf("got %q from %q, want the project skill to win", s.Content, s.Source)
		}
	}
}

func TestLoaderIgnoresMissingDirsAndNonMarkdown(t *testing.T) {
	configDir := t.TempDir()
	skillsDir := filepath.Join(configDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	sl := NewLoader(configDir, filepath.Join(configDir, "does-not-exist"))
	if got := len(sl.GetSkills()); got != 0 {
		t.Fatalf("loaded %d skills, want 0", got)
	}
}
