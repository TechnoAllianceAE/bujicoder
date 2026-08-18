package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// TestEnterRejectsEscapingBranchName guards against a branch name that would
// place the worktree — and therefore a later removal — outside
// .buji-worktrees/.
func TestEnterRejectsEscapingBranchName(t *testing.T) {
	requireGit(t)
	repo := initGitRepo(t)

	for _, branch := range []string{
		"../escaped",
		"../../escaped",
		"a/../../escaped",
		"..",
	} {
		t.Run(branch, func(t *testing.T) {
			info, err := Enter(repo, branch)
			if err == nil {
				t.Fatalf("Enter accepted escaping branch %q (path %s)", branch, info.Path)
			}
			if !strings.Contains(err.Error(), "escapes") && !strings.Contains(err.Error(), "invalid") {
				t.Errorf("unexpected error for %q: %v", branch, err)
			}
		})
	}
}

func TestWorktreePathFor(t *testing.T) {
	base := filepath.Join("/tmp", "repo", worktreeDirName)

	tests := []struct {
		name    string
		branch  string
		want    string
		wantErr bool
	}{
		{"simple", "feature", filepath.Join(base, "feature"), false},
		{"slashed", "feature/login", filepath.Join(base, "feature", "login"), false},
		{"parent", "../evil", "", true},
		{"nested parent", "a/../../evil", "", true},
		{"dotdot", "..", "", true},
		{"empty", "", "", true},
		{"padded", " feature ", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := worktreePathFor(base, tc.branch)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("worktreePathFor(%q) = %q, want error", tc.branch, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("worktreePathFor(%q): %v", tc.branch, err)
			}
			if got != tc.want {
				t.Errorf("worktreePathFor(%q) = %q, want %q", tc.branch, got, tc.want)
			}
		})
	}
}

// TestExitReportsRemovalFailure checks that Exit never claims a worktree was
// removed when the removal actually failed.
func TestExitReportsRemovalFailure(t *testing.T) {
	requireGit(t)

	// A directory that is not a git worktree at all: resolving the main repo
	// must fail and Exit must not report success.
	notAWorktree := t.TempDir()
	removed, err := Exit(notAWorktree, true)
	if err == nil {
		t.Fatal("Exit succeeded on a path that is not a worktree")
	}
	if removed {
		t.Error("Exit reported removed=true for a failed removal")
	}
}

// TestExitRefusesDirtyWorktree protects uncommitted agent work.
func TestExitRefusesDirtyWorktree(t *testing.T) {
	requireGit(t)
	repo := initGitRepo(t)

	info, err := Enter(repo, "dirty-branch")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(info.Path)) })

	if err := os.WriteFile(filepath.Join(info.Path, "work.txt"), []byte("unsaved\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	removed, err := Exit(info.Path, true)
	if err == nil {
		t.Fatal("Exit removed a worktree with uncommitted changes")
	}
	if removed {
		t.Error("Exit reported removed=true for a dirty worktree")
	}
	if _, statErr := os.Stat(filepath.Join(info.Path, "work.txt")); statErr != nil {
		t.Errorf("uncommitted work was destroyed: %v", statErr)
	}
}

// TestEnterCleansUpAfterFailure checks that a failed Enter leaves no orphaned
// worktree registration behind.
func TestEnterCleansUpAfterFailure(t *testing.T) {
	requireGit(t)
	repo := initGitRepo(t)

	before, err := ListActive(repo)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}

	// A branch name git refuses (a trailing ".lock" suffix is invalid).
	if _, err := Enter(repo, "bad.lock"); err == nil {
		t.Skip("git accepted the branch name; cannot exercise the failure path")
	}

	after, err := ListActive(repo)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("failed Enter left %d worktrees registered, want %d: %v", len(after), len(before), after)
	}
}

// TestEnterReusesExistingBranch covers the fallback path: the branch already
// exists, so `worktree add -b` fails and the retry must succeed.
func TestEnterReusesExistingBranch(t *testing.T) {
	requireGit(t)
	repo := initGitRepo(t)

	run(t, repo, "git", "branch", "existing")

	info, err := Enter(repo, "existing")
	if err != nil {
		t.Fatalf("Enter on existing branch: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(info.Path)) })

	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err != nil {
		t.Errorf("worktree not checked out: %v", err)
	}

	removed, err := Exit(info.Path, true)
	if err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if !removed {
		t.Error("Exit reported removed=false for a clean worktree")
	}
	if _, err := os.Stat(filepath.Join(info.Path, "README.md")); err == nil {
		t.Error("worktree directory still present after removal")
	}
}
