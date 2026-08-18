// Package worktree provides git worktree management for isolated branch work.
// Worktrees allow agents to experiment with changes without affecting the
// main working directory.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTimeout bounds every git invocation so a git that waits on input (for
// example a credential prompt) cannot hang the agent indefinitely.
const gitTimeout = 60 * time.Second

// worktreeDirName is the directory holding all managed worktrees.
const worktreeDirName = ".buji-worktrees"

// Info holds metadata about an active worktree.
type Info struct {
	Path    string // absolute path to the worktree directory
	Branch  string // branch name
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

	baseDir := filepath.Join(filepath.Dir(gitRoot), worktreeDirName)
	worktreePath, err := worktreePathFor(baseDir, branch)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return nil, fmt.Errorf("create worktree dir: %w", err)
	}

	// Try creating with new branch.
	newBranchOut, err := gitCmd(gitRoot, "worktree", "add", "-b", branch, worktreePath)
	if err != nil {
		// Branch might already exist — try without -b. The first attempt can
		// leave a registration or a directory behind, so clear it first;
		// otherwise the retry fails with "already exists" and the caller is
		// left with an orphaned worktree entry.
		cleanupFailedAdd(gitRoot, worktreePath)

		existingOut, retryErr := gitCmd(gitRoot, "worktree", "add", worktreePath, branch)
		if retryErr != nil {
			cleanupFailedAdd(gitRoot, worktreePath)
			return nil, fmt.Errorf("create worktree: %s; %s",
				strings.TrimSpace(newBranchOut), strings.TrimSpace(existingOut))
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
	if !cleanup {
		return false, nil
	}

	// Check for uncommitted changes. A git failure here must not be read as
	// "clean", otherwise a broken worktree gets removed with work in it.
	statusOut, err := gitCmd(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("check worktree status: %s", strings.TrimSpace(statusOut))
	}
	if strings.TrimSpace(statusOut) != "" {
		return false, fmt.Errorf("worktree has uncommitted changes — commit or discard before cleanup")
	}

	// Find the main repo via the common git dir
	commonDir, err := gitRevParse(worktreePath, "--git-common-dir")
	if err != nil {
		return false, fmt.Errorf("find main repo: %w", err)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}
	mainRepo := filepath.Dir(commonDir)

	if _, err := gitCmd(mainRepo, "worktree", "remove", worktreePath); err != nil {
		// Force remove if standard remove fails.
		out, forceErr := gitCmd(mainRepo, "worktree", "remove", "--force", worktreePath)
		if forceErr != nil {
			// Reporting removed=true here would make the caller believe the
			// worktree is gone while it is still registered on disk.
			return false, fmt.Errorf("remove worktree: %s", strings.TrimSpace(out))
		}
	}
	return true, nil
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

// worktreePathFor resolves the directory for a branch, refusing any branch name
// that would place the worktree outside baseDir. A branch such as
// "../../etc" would otherwise make Enter create — and Exit later remove — a
// directory anywhere on disk.
func worktreePathFor(baseDir, branch string) (string, error) {
	if branch != strings.TrimSpace(branch) || branch == "" {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	path := filepath.Clean(filepath.Join(baseDir, branch))
	rel, err := filepath.Rel(baseDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("branch name %q escapes the worktree directory", branch)
	}
	return path, nil
}

// cleanupFailedAdd removes the leftovers of a failed `worktree add` so a retry
// starts from a clean state and no orphaned registration is left behind.
func cleanupFailedAdd(gitRoot, worktreePath string) {
	// Only remove the directory if git does not consider it a live worktree.
	if _, err := gitCmd(gitRoot, "worktree", "remove", "--force", worktreePath); err != nil {
		// Not a registered worktree (or already gone): drop an empty leftover
		// directory. os.Remove refuses to delete non-empty directories, so user
		// content is never destroyed here.
		_ = os.Remove(worktreePath)
	}
	_, _ = gitCmd(gitRoot, "worktree", "prune")
}

func gitRevParse(dir, arg string) (string, error) {
	output, err := gitCmd(dir, "rev-parse", arg)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func gitCmd(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("git %s timed out after %s", strings.Join(args, " "), gitTimeout)
	}
	return string(output), err
}
