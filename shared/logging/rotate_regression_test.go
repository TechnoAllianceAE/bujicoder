package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A reopened writer must account for the bytes already in the file. Resetting
// the tracked size to zero on reopen lets the log grow past maxBytes forever.
func TestRotatingWriterCountsExistingBytesOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 90)), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &rotatingWriter{path: path, maxBytes: 100, maxBackups: 2}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("y", 20))); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("writer did not rotate the pre-existing 90-byte file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 20 {
		t.Fatalf("current log holds %d bytes, want 20 (only the new record)", info.Size())
	}
}

// A single record larger than maxBytes must not rotate on every write, which
// would shred all backups after a handful of oversized log lines.
func TestRotatingWriterDoesNotSpinOnOversizedRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w := &rotatingWriter{path: path, maxBytes: 32, maxBackups: 3}
	defer w.Close()

	big := []byte(strings.Repeat("z", 200) + "\n")
	for range 3 {
		if _, err := w.Write(big); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// 3 oversized records => 2 rotations (the first write lands in an empty
	// file), so .1 and .2 exist and each holds exactly one record.
	for _, suffix := range []string{".1", ".2"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatalf("expected %s: %v", path+suffix, err)
		}
		if info.Size() != int64(len(big)) {
			t.Fatalf("%s holds %d bytes, want %d: rotation split or duplicated a record", suffix, info.Size(), len(big))
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Fatal("rotated more than once per oversized record")
	}
}

// Writes must degrade to an error when the log directory is unwritable, never
// panic and never take the app down.
func TestRotatingWriterDegradesWhenDirectoryIsUnwritable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w := &rotatingWriter{path: path, maxBytes: 50, maxBackups: 2}
	if _, err := w.Write([]byte(strings.Repeat("a", 40))); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Make the directory unwritable so rotation cannot recreate the log file.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// A fresh writer over a read-only directory: opening the existing file for
	// write fails, so Write must report an error rather than dereference a nil
	// file handle.
	w2 := &rotatingWriter{path: filepath.Join(dir, "other.log"), maxBytes: 50, maxBackups: 2}
	if w2.file != nil {
		t.Skip("directory permissions are not enforced here")
	}
	if _, err := w2.Write([]byte("hello\n")); err == nil {
		t.Fatal("expected an error writing into an unwritable directory")
	}
}

func TestRotatingWriterRejectsWritesAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	w := newRotatingWriter(path, 1, 2)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("late\n")); err == nil {
		t.Fatal("writing to a closed writer must fail instead of silently reopening it")
	}
	// Close is idempotent.
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// Rotation must prune backups left behind by a previous run configured with a
// larger maxBackups, otherwise disk usage is not actually bounded.
func TestRotatingWriterPrunesStaleBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	for i := 1; i <= 5; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s.%d", path, i), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := &rotatingWriter{path: path, maxBytes: 10, maxBackups: 2}
	defer w.Close()
	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatal(err)
	}

	for i := 3; i <= 5; i++ {
		stale := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(stale); err == nil {
			t.Fatalf("stale backup %s was not pruned", stale)
		}
	}
}

// Concurrent writers must not lose or interleave-corrupt records across a
// rotation boundary.
func TestRotatingWriterConcurrentWritesKeepEveryRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	const writers = 8
	const perWriter = 40
	record := strings.Repeat("q", 60) + "\n"

	w := &rotatingWriter{path: path, maxBytes: 512, maxBackups: 50}

	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				if _, err := w.Write([]byte(record)); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			if line != strings.TrimSuffix(record, "\n") {
				t.Fatalf("corrupted record in %s: %q", e.Name(), line)
			}
			total++
		}
	}
	if total != writers*perWriter {
		t.Fatalf("wrote %d records, found %d across %d files", writers*perWriter, total, len(entries))
	}
}
