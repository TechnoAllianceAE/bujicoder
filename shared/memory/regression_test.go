package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestWriteIsAtomic checks that no partially written memory file is ever visible
// under the destination name: writing directly to the destination truncates the
// previous content before the new content lands.
func TestWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, "/project")

	if err := s.Write("notes", "d1", TypeUser, "original body"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// A reader that runs concurrently with a rewrite must see either the old or
	// the new content, never an empty or half-written file.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, e := range s.List() {
				if e.Content != "original body" && e.Content != strings.Repeat("x", 4096) {
					t.Errorf("observed torn memory content (%d bytes)", len(e.Content))
					return
				}
			}
		}
	}()

	for range 50 {
		if err := s.Write("notes", "d1", TypeUser, strings.Repeat("x", 4096)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := s.Write("notes", "d1", TypeUser, "original body"); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

// TestWriteLeavesNoTempFiles checks that the atomic write does not leak temp
// files into the memory directory or into List output.
func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, "/project")

	if err := s.Write("kept", "desc", TypeProject, "body"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(got))
	}
}

// TestWriteRejectsUnusableName keeps a record that cannot be keyed out of the
// store instead of creating a hidden ".md" file.
func TestWriteRejectsUnusableName(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, "/project")

	for _, name := range []string{"", "   ", "///", "!!!"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			err := s.Write(name, "d", TypeUser, "body")
			if name == "!!!" || name == "///" || name == "   " {
				// These sanitize to underscores, which is a usable name.
				if err != nil {
					t.Fatalf("Write(%q): %v", name, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Write accepted unusable name %q", name)
			}
		})
	}

	if _, err := os.Stat(filepath.Join(s.baseDir, ".md")); err == nil {
		t.Error("an unnamed memory file was created")
	}
}

// TestWriteSanitizesFrontmatter checks that a multi-line name or description
// cannot terminate or inject into the YAML frontmatter block.
func TestWriteSanitizesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, "/project")

	if err := s.Write("evil", "line one\n---\ninjected: yes", TypeUser, "the real body"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries := s.List()
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.Content != "the real body" {
		t.Errorf("Content = %q, want %q", got.Content, "the real body")
	}
	if strings.Contains(got.Description, "\n") {
		t.Errorf("Description still contains a newline: %q", got.Description)
	}
	if got.Name != "evil" {
		t.Errorf("Name = %q, want %q", got.Name, "evil")
	}
}

// TestConcurrentWritesDistinctNames exercises the store from several goroutines.
func TestConcurrentWritesDistinctNames(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, "/project")

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("mem-%d", i)
			if err := s.Write(name, "desc", TypeUser, fmt.Sprintf("body %d", i)); err != nil {
				t.Errorf("Write(%s): %v", name, err)
			}
		}(i)
	}
	wg.Wait()

	got := s.List()
	if len(got) != n {
		t.Fatalf("List returned %d entries, want %d", len(got), n)
	}
}

// TestConcurrentWriteSameName checks that racing writers to one name leave a
// well-formed file rather than a mixture.
func TestConcurrentWriteSameName(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, "/project")

	bodies := []string{strings.Repeat("a", 2048), strings.Repeat("b", 2048)}
	var wg sync.WaitGroup
	for range 20 {
		for _, body := range bodies {
			wg.Add(1)
			go func(body string) {
				defer wg.Done()
				if err := s.Write("shared", "desc", TypeUser, body); err != nil {
					t.Errorf("Write: %v", err)
				}
			}(body)
		}
	}
	wg.Wait()

	got := s.List()
	if len(got) != 1 {
		t.Fatalf("List returned %d entries, want 1", len(got))
	}
	if got[0].Content != bodies[0] && got[0].Content != bodies[1] {
		t.Errorf("content is neither writer's value (%d bytes)", len(got[0].Content))
	}
}
