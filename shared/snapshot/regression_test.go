package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	requireGit(t)
	mgr, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

// TestTakeRejectsPathOutsideProject guards the snapshot tree against a
// tool-reported path that escapes the project root.
func TestTakeRejectsPathOutsideProject(t *testing.T) {
	mgr := newTestManager(t)

	victim := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(victim, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	for _, p := range []string{
		"../escape.txt",
		filepath.Join("sub", "..", "..", "escape.txt"),
		victim,
	} {
		t.Run(p, func(t *testing.T) {
			if _, err := mgr.Take(1, "editor", "write_file", []string{p}); err == nil {
				t.Fatalf("Take accepted escaping path %q", p)
			}
			outside := filepath.Join(filepath.Dir(mgr.projectRoot), "escape.txt")
			if _, err := os.Stat(outside); err == nil {
				t.Fatalf("Take wrote outside the snapshot tree: %s", outside)
			}
		})
	}
}

// TestTakeSurfacesUnreadableFile checks that a present-but-unreadable file is
// reported instead of being recorded as a deletion, which used to drop the last
// good copy from the snapshot tree.
func TestTakeSurfacesUnreadableFile(t *testing.T) {
	mgr := newTestManager(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions are not enforced")
	}

	path := filepath.Join(mgr.projectRoot, "secret.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := mgr.Take(1, "editor", "write_file", []string{"secret.txt"}); err != nil {
		t.Fatalf("first Take: %v", err)
	}

	// Make it unreadable but keep it present.
	if err := os.WriteFile(path, []byte("v2\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if _, err := mgr.Take(2, "editor", "write_file", []string{"secret.txt"}); err == nil {
		t.Fatal("Take silently accepted an unreadable file")
	}

	// The previously snapshotted copy must still be there.
	shadow := filepath.Join(mgr.snapshotDir, "secret.txt")
	data, err := os.ReadFile(shadow)
	if err != nil {
		t.Fatalf("snapshot copy was removed: %v", err)
	}
	if string(data) != "v1\n" {
		t.Errorf("snapshot copy = %q, want %q", data, "v1\n")
	}
}

// TestRevertDoesNotWriteSnapshotMetadata checks that reverting restores only the
// user's files and does not leak the shadow repo's bookkeeping file into the
// project.
func TestRevertDoesNotWriteSnapshotMetadata(t *testing.T) {
	mgr := newTestManager(t)

	path := filepath.Join(mgr.projectRoot, "app.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := mgr.Take(1, "editor", "write_file", []string{"app.txt"})
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if snap == nil {
		t.Fatal("Take returned no snapshot")
	}

	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := mgr.Revert(snap.ID); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after revert: %v", err)
	}
	if string(data) != "v1\n" {
		t.Errorf("app.txt = %q, want %q", data, "v1\n")
	}

	if _, err := os.Stat(filepath.Join(mgr.projectRoot, metadataFile)); err == nil {
		t.Errorf("Revert wrote %s into the project root", metadataFile)
	}
}

// TestRevertDoesNotDeleteUntrackedFiles is the safety-net contract: revert only
// restores what it recorded and never removes files it did not create.
func TestRevertDoesNotDeleteUntrackedFiles(t *testing.T) {
	mgr := newTestManager(t)

	tracked := filepath.Join(mgr.projectRoot, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := mgr.Take(1, "editor", "write_file", []string{"tracked.txt"})
	if err != nil || snap == nil {
		t.Fatalf("Take: %v", err)
	}

	// The user creates a file the snapshot has never seen.
	untracked := filepath.Join(mgr.projectRoot, "notes.md")
	if err := os.WriteFile(untracked, []byte("my notes\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	if err := mgr.Revert(snap.ID); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	data, err := os.ReadFile(untracked)
	if err != nil {
		t.Fatalf("revert deleted an untracked user file: %v", err)
	}
	if string(data) != "my notes\n" {
		t.Errorf("notes.md = %q, want %q", data, "my notes\n")
	}
}

// TestRevertPreservesFileMode checks that restoring a script does not strip its
// execute bit.
func TestRevertPreservesFileMode(t *testing.T) {
	mgr := newTestManager(t)

	path := filepath.Join(mgr.projectRoot, "run.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho v1\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := mgr.Take(1, "editor", "write_file", []string{"run.sh"})
	if err != nil || snap == nil {
		t.Fatalf("Take: %v", err)
	}

	if err := os.WriteFile(path, []byte("#!/bin/sh\necho v2\n"), 0o755); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := mgr.Revert(snap.ID); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("revert stripped the execute bit: mode = %v", info.Mode().Perm())
	}
}

// TestRevertReportsMissingSnapshot checks that a bad snapshot id is an error
// rather than a silent no-op.
func TestRevertReportsMissingSnapshot(t *testing.T) {
	mgr := newTestManager(t)
	if err := mgr.Revert("deadbeef"); err == nil {
		t.Fatal("Revert accepted an unknown snapshot id")
	}
}

// TestTakeHandlesSpacesAndUnicodePaths covers file names that a shell-based
// implementation would mangle.
func TestTakeHandlesSpacesAndUnicodePaths(t *testing.T) {
	mgr := newTestManager(t)

	names := []string{"my notes.txt", "日本語ファイル.txt", "dir with space/nested file.txt"}
	for _, name := range names {
		full := filepath.Join(mgr.projectRoot, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("content of "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	snap, err := mgr.Take(1, "editor", "write_file", names)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if snap == nil {
		t.Fatal("Take reported no changes for new files")
	}

	// Overwrite everything, then revert and confirm each file came back.
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(mgr.projectRoot, name), []byte("clobbered\n"), 0o644); err != nil {
			t.Fatalf("clobber %s: %v", name, err)
		}
	}
	if err := mgr.Revert(snap.ID); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(mgr.projectRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if want := "content of " + name + "\n"; string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}
}

// TestTakeIsSerialized runs concurrent snapshots and verifies every one either
// commits or reports an error — a lost git index lock must not corrupt history.
func TestTakeIsSerialized(t *testing.T) {
	mgr := newTestManager(t)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			name := filepath.Join(mgr.projectRoot, "f"+string(rune('a'+i))+".txt")
			if err := os.WriteFile(name, []byte("content\n"), 0o644); err != nil {
				t.Errorf("write: %v", err)
				return
			}
			if _, err := mgr.Take(i, "editor", "write_file", []string{filepath.Base(name)}); err != nil {
				t.Errorf("Take: %v", err)
			}
		}(i)
	}
	wg.Wait()

	snaps, err := mgr.List(50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) == 0 {
		t.Fatal("no snapshots recorded")
	}
}

// TestGitReportsStderr checks that a failing git command surfaces its stderr
// rather than an empty error.
func TestGitReportsStderr(t *testing.T) {
	mgr := newTestManager(t)

	_, err := mgr.git("rev-parse", "definitely-not-a-ref")
	if err == nil {
		t.Fatal("expected an error for an unknown ref")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-ref") {
		t.Errorf("error does not include git stderr: %v", err)
	}
}

// TestGitRejectsEmptyArgs guards the args[0] index used in error messages.
func TestGitRejectsEmptyArgs(t *testing.T) {
	mgr := &Manager{projectRoot: t.TempDir(), snapshotDir: t.TempDir()}
	if _, err := mgr.git(); err == nil {
		t.Fatal("expected an error for an empty git invocation")
	}
}

// TestMissingGitBinaryIsClearError checks that a missing git degrades with a
// message instead of a panic or an empty failure.
func TestMissingGitBinaryIsClearError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", filepath.Join(dir, "empty-bin"))

	_, err := NewManager(dir)
	if err == nil {
		t.Fatal("NewManager succeeded without a git binary")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Errorf("error does not mention git: %v", err)
	}
}

func TestRelInProject(t *testing.T) {
	root := t.TempDir()
	m := &Manager{projectRoot: root, snapshotDir: filepath.Join(root, ".bujicoder", "snapshots")}

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"relative", "a/b.txt", filepath.Join("a", "b.txt"), false},
		{"dot prefixed", "./a.txt", "a.txt", false},
		{"absolute inside", filepath.Join(root, "a.txt"), "a.txt", false},
		{"parent escape", "../a.txt", "", true},
		{"nested escape", "a/../../b.txt", "", true},
		{"root itself", ".", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.relInProject(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("relInProject(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("relInProject(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("relInProject(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
