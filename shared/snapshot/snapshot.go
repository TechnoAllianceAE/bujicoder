// Package snapshot manages a shadow git repository for tracking file changes
// per agent step, allowing safe revert without touching the user's real git history.
package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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
	snapshotDir := filepath.Join(projectRoot, ".bujicoder", "snapshots")

	m := &Manager{
		projectRoot: projectRoot,
		snapshotDir: snapshotDir,
	}

	// Initialize shadow repo if needed.
	if err := m.initRepo(); err != nil {
		return nil, fmt.Errorf("init snapshot repo: %w", err)
	}

	// Ensure .bujicoder/ is in .gitignore.
	m.ensureGitignore()

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
		srcPath := filepath.Join(m.projectRoot, f)
		dstPath := filepath.Join(m.snapshotDir, f)

		// Create parent dirs.
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			continue
		}

		data, err := os.ReadFile(srcPath)
		if err != nil {
			// File was deleted — remove from snapshot tree too.
			os.Remove(dstPath)
			m.git("rm", "--force", "--quiet", f)
			continue
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			continue
		}
	}

	// Stage all changes.
	if _, err := m.git("add", "-A"); err != nil {
		return nil, fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit.
	status, _ := m.git("status", "--porcelain")
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

		// Get files changed in this commit.
		filesOut, _ := m.git("diff-tree", "--no-commit-id", "--name-only", "-r", parts[0])
		if filesOut != "" {
			for _, f := range strings.Split(strings.TrimSpace(filesOut), "\n") {
				if f != "" {
					snap.Files = append(snap.Files, f)
				}
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

	// Get list of files in the snapshot's tree.
	filesOut, err := m.git("ls-tree", "-r", "--name-only", fullHash)
	if err != nil {
		return fmt.Errorf("ls-tree: %w", err)
	}

	// For each file, extract from the snapshot commit and write to project root.
	for _, f := range strings.Split(strings.TrimSpace(filesOut), "\n") {
		if f == "" {
			continue
		}
		content, err := m.git("show", fullHash+":"+f)
		if err != nil {
			continue
		}
		dstPath := filepath.Join(m.projectRoot, f)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(dstPath, []byte(content), 0o644); err != nil {
			continue
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

// Cleanup removes snapshots older than the given duration, keeping at most maxKeep.
func (m *Manager) Cleanup(olderThan time.Duration, maxKeep int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if maxKeep <= 0 {
		maxKeep = 100
	}

	// Count total commits.
	countOut, _ := m.git("rev-list", "--count", "HEAD")
	count, _ := strconv.Atoi(strings.TrimSpace(countOut))

	if count <= maxKeep {
		return nil
	}

	// Truncate history to maxKeep commits by rebasing.
	// Use filter-branch or orphan approach.
	// Simplest: reset to keep only last N commits via shallow clone technique.
	keepHash, err := m.git("rev-parse", fmt.Sprintf("HEAD~%d", maxKeep))
	if err != nil {
		return nil // not enough commits
	}
	keepHash = strings.TrimSpace(keepHash)

	// Create a new orphan branch from the keep point and force-replace.
	m.git("checkout", "--orphan", "cleanup-temp", keepHash)
	m.git("commit", "-m", "cleanup: truncated history", "--allow-empty", "--quiet")
	m.git("cherry-pick", keepHash+"..main")
	m.git("branch", "-M", "main")
	m.git("checkout", "main")

	return nil
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

	// Configure the repo to avoid user identity issues.
	m.git("config", "user.email", "bujicoder@local")
	m.git("config", "user.name", "BujiCoder Snapshots")

	// Initial commit so we have a HEAD.
	readmePath := filepath.Join(m.snapshotDir, ".snapshot-metadata")
	os.WriteFile(readmePath, []byte("BujiCoder snapshot repository\n"), 0o644)
	m.git("add", "-A")
	m.git("commit", "-m", "init", "--quiet")

	return nil
}

func (m *Manager) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = m.snapshotDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 5-second timeout to avoid hanging.
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			return stdout.String(), fmt.Errorf("%s: %s", err, stderr.String())
		}
		return stdout.String(), nil
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return "", fmt.Errorf("git %s timed out", args[0])
	}
}

func (m *Manager) ensureGitignore() {
	gitignorePath := filepath.Join(m.projectRoot, ".gitignore")

	data, _ := os.ReadFile(gitignorePath)
	content := string(data)

	if strings.Contains(content, ".bujicoder/") {
		return
	}

	// Append the entry.
	entry := "\n# BujiCoder local data\n.bujicoder/\n"
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(entry)
}
