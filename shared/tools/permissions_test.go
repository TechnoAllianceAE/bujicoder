package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjectPermissions_NoFile(t *testing.T) {
	dir := t.TempDir()
	perms := LoadProjectPermissions(dir)
	if perms != nil {
		t.Fatal("expected nil when no .bujicoderrc exists")
	}
}

func TestLoadProjectPermissions_BasicLoad(t *testing.T) {
	dir := t.TempDir()
	rc := `
mode: yolo
tools:
  write_file: allow
  run_terminal_command: ask
commands:
  - pattern: "npm test*"
    action: allow
  - pattern: "git push --force*"
    action: deny
restricted_paths:
  - ".env"
  - "**/*.pem"
`
	if err := os.WriteFile(filepath.Join(dir, ".bujicoderrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}

	perms := LoadProjectPermissions(dir)
	if perms == nil {
		t.Fatal("expected non-nil permissions")
	}
	if perms.Mode != ModeYolo {
		t.Fatalf("expected mode yolo, got %s", perms.Mode)
	}
	if perms.CommandRuleCount() != 2 {
		t.Fatalf("expected 2 command rules, got %d", perms.CommandRuleCount())
	}
	if len(perms.RestrictedPaths) != 2 {
		t.Fatalf("expected 2 restricted paths, got %d", len(perms.RestrictedPaths))
	}
}

func TestLoadProjectPermissions_WalksUp(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	rc := "mode: strict\n"
	if err := os.WriteFile(filepath.Join(root, ".bujicoderrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}

	perms := LoadProjectPermissions(sub)
	if perms == nil {
		t.Fatal("expected to find .bujicoderrc by walking up")
	}
	if perms.Mode != ModeStrict {
		t.Fatalf("expected strict, got %s", perms.Mode)
	}
}

func TestCheckCommand(t *testing.T) {
	perms := &ProjectPermissions{
		Commands: []CommandRule{
			{Pattern: "npm test*", Action: ActionAllow},
			{Pattern: "go test*", Action: ActionAllow},
			{Pattern: "git push --force*", Action: ActionDeny},
			{Pattern: "git push", Action: ActionAsk},
		},
	}

	tests := []struct {
		cmd    string
		expect PermissionAction
	}{
		{"npm test", ActionAllow},
		{"npm test ./...", ActionAllow},
		{"go test -v ./...", ActionAllow},
		{"git push --force origin main", ActionDeny},
		{"git push", ActionAsk},
		{"ls -la", ""},
		{"echo hello", ""},
	}

	for _, tt := range tests {
		got := perms.CheckCommand(tt.cmd)
		if got != tt.expect {
			t.Errorf("CheckCommand(%q) = %q, want %q", tt.cmd, got, tt.expect)
		}
	}
}

func TestCheckCommand_Nil(t *testing.T) {
	var perms *ProjectPermissions
	if action := perms.CheckCommand("anything"); action != "" {
		t.Fatalf("expected empty action for nil perms, got %q", action)
	}
}

func TestCheckToolPermission(t *testing.T) {
	perms := &ProjectPermissions{
		Tools: map[string]PermissionAction{
			"write_file":           ActionAllow,
			"run_terminal_command": ActionAsk,
		},
	}

	if got := perms.CheckToolPermission("write_file"); got != ActionAllow {
		t.Fatalf("expected allow, got %q", got)
	}
	if got := perms.CheckToolPermission("run_terminal_command"); got != ActionAsk {
		t.Fatalf("expected ask, got %q", got)
	}
	if got := perms.CheckToolPermission("read_files"); got != "" {
		t.Fatalf("expected empty for unset tool, got %q", got)
	}
}

func TestIsPathRestricted(t *testing.T) {
	perms := &ProjectPermissions{
		RestrictedPaths: []string{
			".env",
			".env.*",
			"**/*.pem",
			"**/*.key",
			"secrets/",
		},
	}

	tests := []struct {
		path     string
		expected bool
	}{
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		{"src/main.go", false},
		{"certs/server.pem", true},
		{"deep/path/to/file.key", true},
		{"README.md", false},
	}

	for _, tt := range tests {
		got := perms.IsPathRestricted(tt.path)
		if got != tt.expected {
			t.Errorf("IsPathRestricted(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestIsPathRestricted_Nil(t *testing.T) {
	var perms *ProjectPermissions
	if perms.IsPathRestricted(".env") {
		t.Fatal("expected false for nil perms")
	}
}

func TestDefaultMode(t *testing.T) {
	dir := t.TempDir()
	// No mode specified — should default to "ask".
	rc := "commands:\n  - pattern: \"ls\"\n    action: allow\n"
	if err := os.WriteFile(filepath.Join(dir, ".bujicoderrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}

	perms := LoadProjectPermissions(dir)
	if perms == nil {
		t.Fatal("expected non-nil")
	}
	if perms.Mode != ModeAsk {
		t.Fatalf("expected default mode ask, got %s", perms.Mode)
	}
}

func TestInvalidMode_DefaultsToAsk(t *testing.T) {
	dir := t.TempDir()
	rc := "mode: banana\n"
	if err := os.WriteFile(filepath.Join(dir, ".bujicoderrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}

	perms := LoadProjectPermissions(dir)
	if perms == nil {
		t.Fatal("expected non-nil")
	}
	if perms.Mode != ModeAsk {
		t.Fatalf("expected default mode ask for invalid input, got %s", perms.Mode)
	}
}
