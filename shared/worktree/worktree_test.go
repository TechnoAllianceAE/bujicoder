package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("test"), 0644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "init")
	return dir
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, string(output))
	}
}

func TestEnter_CreatesWorktree(t *testing.T) {
	repo := initGitRepo(t)

	info, err := Enter(repo, "test-branch")
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(info.Path)) // cleanup .buji-worktrees

	if info.Branch != "test-branch" {
		t.Errorf("Branch = %q", info.Branch)
	}
	if info.Path == "" {
		t.Error("Path should not be empty")
	}

	// Verify the worktree directory exists
	if _, err := os.Stat(info.Path); os.IsNotExist(err) {
		t.Error("worktree directory not created")
	}
}

func TestEnter_AutoBranch(t *testing.T) {
	repo := initGitRepo(t)

	info, err := Enter(repo, "")
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(info.Path))

	if info.Branch == "" {
		t.Error("auto-generated branch should not be empty")
	}
}

func TestEnter_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := Enter(dir, "test")
	if err == nil {
		t.Error("expected error for non-git directory")
	}
}

func TestExit_CleansUp(t *testing.T) {
	repo := initGitRepo(t)

	info, err := Enter(repo, "cleanup-test")
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(info.Path))

	removed, err := Exit(info.Path, true)
	if err != nil {
		t.Fatalf("Exit failed: %v", err)
	}
	if !removed {
		t.Error("should have been removed (no changes)")
	}
}

func TestExit_PreservesWithChanges(t *testing.T) {
	repo := initGitRepo(t)

	info, err := Enter(repo, "changes-test")
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(info.Path))

	// Make uncommitted changes
	os.WriteFile(filepath.Join(info.Path, "new-file.txt"), []byte("content"), 0644)

	_, err = Exit(info.Path, true)
	if err == nil {
		t.Error("expected error when cleaning up with uncommitted changes")
	}
}

func TestHasChanges(t *testing.T) {
	repo := initGitRepo(t)

	if HasChanges(repo) {
		t.Error("clean repo should have no changes")
	}

	os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0644)
	if !HasChanges(repo) {
		t.Error("repo with new file should have changes")
	}
}

func TestListActive(t *testing.T) {
	repo := initGitRepo(t)

	paths, err := ListActive(repo)
	if err != nil {
		t.Fatalf("ListActive failed: %v", err)
	}
	// At minimum, the main worktree should be listed
	if len(paths) < 1 {
		t.Error("expected at least 1 worktree (main)")
	}
}
