// Package snapshot manages a shadow git repository for tracking file changes
// per agent step, allowing safe revert without touching the user's real git history.
package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// gitTimeout bounds every git invocation in the shadow repo.
	gitTimeout = 5 * time.Second
	// metadataFile marks the shadow repo. It lives only in the snapshot tree
	// and must never be restored into the user's project.
	metadataFile = ".snapshot-metadata"
)

// Manager manages snapshots in a shadow git repository.
type Manager struct {
	projectRoot string
	snapshotDir string // .bujicoder/snapshots/
	mu          sync.Mutex
}

// Snapshot represents a recorded state after a tool execution.
type Snapshot struct {
	ID        string    // Short commit hash
	StepNum   int       // Agent step number
	AgentID   string    // Which agent made the change
	ToolName  string    // Which tool was used
	Timestamp time.Time // When the snapshot was taken
	Files     []string  // Files included in this snapshot
}

// NewManager creates a snapshot manager for the given project.
// It initializes the shadow git repo if it doesn't exist.
func NewManager(projectRoot string) (*Manager, error) {
	// An absolute root is required so snapshot paths can be validated against
	// it even after the process changes directory.
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}

	m := &Manager{
		projectRoot: absRoot,
		snapshotDir: filepath.Join(absRoot, ".bujicoder", "snapshots"),
	}

	// Initialize shadow repo if needed.
	if err := m.initRepo(); err != nil {
		return nil, fmt.Errorf("init snapshot repo: %w", err)
	}

	// Ensure .bujicoder/ is in .gitignore. Snapshots still work without it, so
	// this is reported and not fatal.
	if err := m.ensureGitignore(); err != nil {
		log.Warn().Err(err).Str("project", absRoot).Msg("snapshot: could not update .gitignore")
	}

	return m, nil
}

// Take records a snapshot of the given files after a tool execution.
func (m *Manager) Take(stepNum int, agentID, toolName string, files []string) (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(files) == 0 {
		return nil, nil
	}

	// Copy files to snapshot working tree.
	for _, f := range files {
		rel, err := m.relInProject(f)
		if err != nil {
			return nil, err
		}
		srcPath := filepath.Join(m.projectRoot, rel)
		dstPath := filepath.Join(m.snapshotDir, rel)

		data, readErr := os.ReadFile(srcPath)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				// An unreadable-but-present file must not be recorded as a
				// deletion: that would drop the last good copy from the
				// snapshot tree and silently lose the user's revert target.
				return nil, fmt.Errorf("snapshot %s: %w", rel, readErr)
			}
			// File was deleted — remove from snapshot tree too.
			if err := os.Remove(dstPath); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("snapshot remove %s: %w", rel, err)
			}
			// Ignore the error: the path may never have been tracked.
			_, _ = m.git("rm", "--force", "--quiet", "--ignore-unmatch", "--", rel)
			continue
		}

		// Create parent dirs.
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
			return nil, fmt.Errorf("snapshot mkdir %s: %w", rel, err)
		}
		if err := os.WriteFile(dstPath, data, 0o600); err != nil {
			return nil, fmt.Errorf("snapshot write %s: %w", rel, err)
		}
	}

	// Stage all changes.
	if _, err := m.git("add", "-A"); err != nil {
		return nil, fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit.
	status, err := m.git("status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return nil, nil // no changes
	}

	// Commit with metadata.
	msg := fmt.Sprintf("step:%d agent:%s tool:%s", stepNum, agentID, toolName)
	if _, err := m.git("commit", "-m", msg, "--quiet"); err != nil {
		return nil, fmt.Errorf("git commit: %w", err)
	}

	// Get commit hash.
	hash, err := m.git("rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse: %w", err)
	}

	return &Snapshot{
		ID:        strings.TrimSpace(hash),
		StepNum:   stepNum,
		AgentID:   agentID,
		ToolName:  toolName,
		Timestamp: time.Now().UTC(),
		Files:     files,
	}, nil
}

// List returns recent snapshots (newest first).
func (m *Manager) List(limit int) ([]Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}

	// Format: hash|timestamp|message
	format := "%h|%aI|%s"
	out, err := m.git("log", fmt.Sprintf("--max-count=%d", limit), fmt.Sprintf("--format=%s", format))
	if err != nil {
		return nil, nil // no commits yet
	}

	var snapshots []Snapshot
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}

		ts, _ := time.Parse(time.RFC3339, parts[1])
		snap := Snapshot{
			ID:        parts[0],
			Timestamp: ts,
		}

		// Parse commit message: "step:N agent:ID tool:NAME"
		msgParts := strings.Fields(parts[2])
		for _, p := range msgParts {
			kv := strings.SplitN(p, ":", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "step":
				snap.StepNum, _ = strconv.Atoi(kv[1])
			case "agent":
				snap.AgentID = kv[1]
			case "tool":
				snap.ToolName = kv[1]
			}
		}

		// Get files changed in this commit. -z keeps non-ASCII paths raw.
		filesOut, err := m.git("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", parts[0])
		if err != nil {
			return nil, fmt.Errorf("list snapshot %s files: %w", parts[0], err)
		}
		for _, f := range strings.Split(filesOut, "\x00") {
			if f != "" && f != metadataFile {
				snap.Files = append(snap.Files, f)
			}
		}

		snapshots = append(snapshots, snap)
	}
	return snapshots, nil
}

// Revert restores project files to the state at the given snapshot.
// It copies files from the snapshot's commit back to the project root.
// It does NOT modify the user's real git repo.
func (m *Manager) Revert(snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Resolve the snapshot commit.
	fullHash, err := m.git("rev-parse", snapshotID)
	if err != nil {
		return fmt.Errorf("snapshot %q not found", snapshotID)
	}
	fullHash = strings.TrimSpace(fullHash)

	// Get list of files in the snapshot's tree. -z keeps paths raw: without it
	// git C-quotes any path with non-ASCII or special characters, and feeding
	// the quoted form back to `git show` fails — so revert used to skip every
	// file with a unicode name.
	filesOut, err := m.git("ls-tree", "-r", "--name-only", "-z", fullHash)
	if err != nil {
		return fmt.Errorf("ls-tree: %w", err)
	}

	// For each file, extract from the snapshot commit and write to project root.
	// A silently skipped file means the user believes work was restored when it
	// was not, so every failure is surfaced.
	for _, f := range strings.Split(filesOut, "\x00") {
		if f == "" || f == metadataFile {
			continue
		}
		content, err := m.git("show", fullHash+":"+f)
		if err != nil {
			return fmt.Errorf("revert %s: %w", f, err)
		}
		dstPath := filepath.Join(m.projectRoot, f)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return fmt.Errorf("revert mkdir %s: %w", f, err)
		}
		// Preserve the existing mode so reverting does not strip the execute
		// bit off scripts.
		mode := os.FileMode(0o644)
		if info, statErr := os.Stat(dstPath); statErr == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(dstPath, []byte(content), mode); err != nil {
			return fmt.Errorf("revert write %s: %w", f, err)
		}
	}

	return nil
}

// Diff returns a unified diff between two snapshots.
func (m *Manager) Diff(fromID, toID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	diff, err := m.git("diff", fromID, toID)
	if err != nil {
		return "", fmt.Errorf("diff: %w", err)
	}
	return diff, nil
}

// Cleanup truncates snapshot history to at most maxKeep commits.
//
// The olderThan argument is accepted for API compatibility but is not used:
// truncation is purely count-based.
//
// History is rewritten on a temporary branch and only swapped in once every
// step has succeeded, so a failure part-way through leaves the existing
// snapshot history — the user's only revert path — untouched.
func (m *Manager) Cleanup(olderThan time.Duration, maxKeep int) error {
	_ = olderThan

	m.mu.Lock()
	defer m.mu.Unlock()

	if maxKeep <= 0 {
		maxKeep = 100
	}

	// Count total commits.
	countOut, err := m.git("rev-list", "--count", "HEAD")
	if err != nil {
		return fmt.Errorf("count snapshots: %w", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(countOut))
	if err != nil {
		return fmt.Errorf("parse snapshot count %q: %w", strings.TrimSpace(countOut), err)
	}

	if count <= maxKeep {
		return nil
	}

	branch, err := m.currentBranch()
	if err != nil {
		return err
	}

	keepHash, err := m.git("rev-parse", fmt.Sprintf("HEAD~%d", maxKeep))
	if err != nil {
		return nil // not enough commits
	}
	keepHash = strings.TrimSpace(keepHash)

	const temp = "buji-cleanup-temp"
	// Start a fresh root at the keep point on a throwaway branch. The original
	// branch is not modified until the final rename.
	if _, err := m.git("checkout", "--quiet", "--orphan", temp, keepHash); err != nil {
		return fmt.Errorf("cleanup: start truncated history: %w", err)
	}

	abort := func(cause error) error {
		if _, err := m.git("checkout", "--quiet", "--force", branch); err != nil {
			return fmt.Errorf("cleanup failed (%w) and could not restore branch %s: %w", cause, branch, err)
		}
		_, _ = m.git("branch", "-D", temp)
		return cause
	}

	if _, err := m.git("commit", "-m", "cleanup: truncated history", "--allow-empty", "--quiet"); err != nil {
		return abort(fmt.Errorf("cleanup: root commit: %w", err))
	}
	if _, err := m.git("cherry-pick", keepHash+".."+branch); err != nil {
		_, _ = m.git("cherry-pick", "--abort")
		return abort(fmt.Errorf("cleanup: replay snapshots: %w", err))
	}
	// Renaming the current branch moves HEAD with it.
	if _, err := m.git("branch", "-M", branch); err != nil {
		return abort(fmt.Errorf("cleanup: swap branch: %w", err))
	}

	return nil
}

// currentBranch returns the checked-out branch of the shadow repo. The default
// branch name depends on the user's git configuration, so it must never be
// assumed to be "main".
func (m *Manager) currentBranch() (string, error) {
	out, err := m.git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve snapshot branch: %w", err)
	}
	branch := strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("snapshot repo has no checked-out branch")
	}
	return branch, nil
}

// --- Internal helpers ---

func (m *Manager) initRepo() error {
	gitDir := filepath.Join(m.snapshotDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return nil // already initialized
	}

	if err := os.MkdirAll(m.snapshotDir, 0o700); err != nil {
		return err
	}

	if _, err := m.git("init", "--quiet"); err != nil {
		return err
	}

	// Configure the repo to avoid user identity issues. Without this the
	// initial commit — and every later snapshot — fails on machines with no
	// global git identity.
	if _, err := m.git("config", "user.email", "bujicoder@local"); err != nil {
		return err
	}
	if _, err := m.git("config", "user.name", "BujiCoder Snapshots"); err != nil {
		return err
	}

	// Initial commit so we have a HEAD. A missing HEAD makes every subsequent
	// snapshot operation fail, so this must not be best-effort.
	readmePath := filepath.Join(m.snapshotDir, metadataFile)
	if err := os.WriteFile(readmePath, []byte("BujiCoder snapshot repository\n"), 0o600); err != nil {
		return err
	}
	if _, err := m.git("add", "-A"); err != nil {
		return err
	}
	if _, err := m.git("commit", "-m", "init", "--quiet"); err != nil {
		return err
	}

	return nil
}

// relInProject validates that p refers to a location inside the project root
// and returns it as a clean relative path. Without this a tool-reported path
// such as "../../.ssh/config" would make the snapshot tree write outside
// .bujicoder/snapshots/.
func (m *Manager) relInProject(p string) (string, error) {
	rel := p
	if filepath.IsAbs(p) {
		var err error
		rel, err = filepath.Rel(m.projectRoot, p)
		if err != nil {
			return "", fmt.Errorf("snapshot path %q outside project: %w", p, err)
		}
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("snapshot path %q escapes project root", p)
	}
	return rel, nil
}

// git runs a git command inside the shadow repo. stderr is folded into the
// returned error so a failing snapshot is never silent.
func (m *Manager) git(args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("git: no arguments")
	}

	// Bounded so a hung git (for example one prompting for credentials) cannot
	// stall the agent. CommandContext owns the kill, which avoids racing on
	// cmd.Process while cmd.Run is still starting the process.
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = m.snapshotDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.String(), fmt.Errorf("git %s timed out after %s", args[0], gitTimeout)
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("git executable not found: snapshots require git in PATH")
		}
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (m *Manager) ensureGitignore() error {
	gitignorePath := filepath.Join(m.projectRoot, ".gitignore")

	data, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	if strings.Contains(string(data), ".bujicoder/") {
		return nil
	}

	// Append the entry.
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	if _, err := f.WriteString("\n# BujiCoder local data\n.bujicoder/\n"); err != nil {
		f.Close()
		return fmt.Errorf("write .gitignore: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close .gitignore: %w", err)
	}
	return nil
}
