// Package worktree provides git worktree management for isolated branch work.
// Worktrees allow agents to experiment with changes without affecting the
// main working directory.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info holds metadata about an active worktree.
type Info struct {
	Path   string // absolute path to the worktree directory
	Branch string // branch name
	GitRoot string // original repo root
}

// Enter creates a new git worktree for isolated work. If branch is empty,
// a unique branch name is generated. Returns the worktree info.
func Enter(repoRoot, branch string) (*Info, error) {
	// Verify we're in a git repo
	gitRoot, err := gitRevParse(repoRoot, "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not in a git repository")
	}

	if branch == "" {
		branch = fmt.Sprintf("buji-worktree-%d", os.Getpid())
	}

	// Create worktree directory alongside the repo
	worktreePath := filepath.Join(filepath.Dir(gitRoot), ".buji-worktrees", branch)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return nil, fmt.Errorf("create worktree dir: %w", err)
	}

	// Try creating with new branch
	output, err := gitCmd(gitRoot, "worktree", "add", "-b", branch, worktreePath)
	if err != nil {
		// Branch might already exist — try without -b
		output, err = gitCmd(gitRoot, "worktree", "add", worktreePath, branch)
		if err != nil {
			return nil, fmt.Errorf("create worktree: %s", strings.TrimSpace(output))
		}
	}

	return &Info{
		Path:    worktreePath,
		Branch:  branch,
		GitRoot: gitRoot,
	}, nil
}

// Exit leaves a worktree and optionally removes it.
// If cleanup is true and there are no uncommitted changes, the worktree is removed.
// Returns whether the worktree was removed.
func Exit(worktreePath string, cleanup bool) (removed bool, err error) {
	// Check for uncommitted changes
	statusOut, _ := gitCmd(worktreePath, "status", "--porcelain")
	hasChanges := strings.TrimSpace(statusOut) != ""

	if cleanup && hasChanges {
		return false, fmt.Errorf("worktree has uncommitted changes — commit or discard before cleanup")
	}

	if cleanup && !hasChanges {
		// Find the main repo via the common git dir
		commonDir, err := gitRevParse(worktreePath, "--git-common-dir")
		if err != nil {
			return false, fmt.Errorf("find main repo: %w", err)
		}
		mainRepo := filepath.Dir(commonDir)

		if _, err := gitCmd(mainRepo, "worktree", "remove", worktreePath); err != nil {
			// Force remove if standard remove fails
			gitCmd(mainRepo, "worktree", "remove", "--force", worktreePath)
		}
		return true, nil
	}

	return false, nil
}

// ListActive returns all active worktrees for a repository.
func ListActive(repoRoot string) ([]string, error) {
	output, err := gitCmd(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimPrefix(line, "worktree "))
		}
	}
	return paths, nil
}

// HasChanges checks if a worktree has uncommitted changes.
func HasChanges(worktreePath string) bool {
	output, _ := gitCmd(worktreePath, "status", "--porcelain")
	return strings.TrimSpace(output) != ""
}

func gitRevParse(dir, arg string) (string, error) {
	output, err := gitCmd(dir, "rev-parse", arg)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func gitCmd(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
