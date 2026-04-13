package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoader_LoadsFromDir(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	os.WriteFile(filepath.Join(skillsDir, "greet.md"), []byte(`---
name: greet
description: Say hello
when-to-use: When greeting users
allowed-tools: read_files, web_search
---

Greet the user professionally.
`), 0644)

	sl := &Loader{skills: make(map[string]*Skill)}
	sl.loadFromDir(skillsDir, "user")

	if len(sl.skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(sl.skills))
	}

	s := sl.GetSkill("greet")
	if s == nil {
		t.Fatal("skill 'greet' not found")
	}
	if s.Description != "Say hello" {
		t.Errorf("Description = %q", s.Description)
	}
	if s.Content != "Greet the user professionally." {
		t.Errorf("Content = %q", s.Content)
	}
	if len(s.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want 2 items", s.AllowedTools)
	}
	if s.Source != "user" {
		t.Errorf("Source = %q, want 'user'", s.Source)
	}
}

func TestLoader_DirectorySkill(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	skillDir := filepath.Join(skillsDir, "review")
	os.MkdirAll(skillDir, 0755)

	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: review
description: Code review
---

Review the code carefully.
`), 0644)

	sl := &Loader{skills: make(map[string]*Skill)}
	sl.loadFromDir(skillsDir, "project")

	s := sl.GetSkill("review")
	if s == nil {
		t.Fatal("directory skill 'review' not found")
	}
	if s.Source != "project" {
		t.Errorf("Source = %q, want 'project'", s.Source)
	}
}

func TestLoader_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0755)

	os.WriteFile(filepath.Join(skillsDir, "simple.md"), []byte("Just do the thing."), 0644)

	sl := &Loader{skills: make(map[string]*Skill)}
	sl.loadFromDir(skillsDir, "user")

	s := sl.GetSkill("simple")
	if s == nil {
		t.Fatal("skill 'simple' not found")
	}
	if s.Name != "simple" {
		t.Errorf("Name = %q, want 'simple'", s.Name)
	}
	if s.Content != "Just do the thing." {
		t.Errorf("Content = %q", s.Content)
	}
}

func TestSkill_FilterTools(t *testing.T) {
	agentTools := []string{"read_files", "write_file", "web_search", "run_terminal_command"}

	t.Run("empty AllowedTools passes through", func(t *testing.T) {
		s := &Skill{}
		result := s.FilterTools(agentTools)
		if len(result) != len(agentTools) {
			t.Errorf("expected %d tools, got %d", len(agentTools), len(result))
		}
	})

	t.Run("filters to intersection", func(t *testing.T) {
		s := &Skill{AllowedTools: []string{"read_files", "web_search"}}
		result := s.FilterTools(agentTools)
		if len(result) != 2 {
			t.Errorf("expected 2 tools, got %d: %v", len(result), result)
		}
	})

	t.Run("empty intersection returns nil", func(t *testing.T) {
		s := &Skill{AllowedTools: []string{"nonexistent_tool"}}
		result := s.FilterTools(agentTools)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})
}

func TestNewLoader_MissingDirs(t *testing.T) {
	sl := NewLoader("/nonexistent/config", "/nonexistent/project")
	if len(sl.GetSkills()) != 0 {
		t.Errorf("expected 0 skills from nonexistent dirs, got %d", len(sl.GetSkills()))
	}
}
