package contextcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return p
}

// TestSameSizeRewriteWithSameMtimeIsNotServedStale is the correctness contract:
// mtime alone is not enough to prove a cached entry is current, because the
// filesystem's mtime resolution is coarser than the interval between two agent
// writes. The cached size must be compared too.
func TestSameSizeRewriteWithSameMtimeIsNotServedStale(t *testing.T) {
	root := t.TempDir()
	c := New(root)

	p := writeFile(t, root, "a.txt", "aaaa")
	got, err := c.Get("a.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "aaaa" {
		t.Fatalf("Get = %q, want %q", got, "aaaa")
	}

	// Rewrite with different content and a different length, then force the
	// recorded mtime back to what the cache saw.
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := os.WriteFile(p, []byte("bbbbbb"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got, err = c.Get("a.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "bbbbbb" {
		t.Fatalf("served stale content %q after a same-mtime rewrite", got)
	}
}

// TestTTLIsEnforced checks the documented TTL actually expires an entry.
func TestTTLIsEnforced(t *testing.T) {
	root := t.TempDir()
	c := New(root, 20*time.Millisecond)

	p := writeFile(t, root, "a.txt", "v1")
	if _, err := c.Get("a.txt"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Rewrite behind the cache's back, keeping the same mtime AND size so only
	// the TTL can invalidate it.
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := os.WriteFile(p, []byte("v2"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	time.Sleep(40 * time.Millisecond)

	got, err := c.Get("a.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v2" {
		t.Errorf("TTL did not expire the entry: got %q, want %q", got, "v2")
	}
}

// TestOversizedFileIsAnError checks that a file above MaxFileSize is reported as
// an error, not as empty content. Returning ("", nil) made read_file present a
// large file to the agent as if it were empty.
func TestOversizedFileIsAnError(t *testing.T) {
	root := t.TempDir()
	c := New(root)

	writeFile(t, root, "big.txt", strings.Repeat("x", MaxFileSize+1))

	got, err := c.Get("big.txt")
	if err == nil {
		t.Fatalf("Get returned (%q, nil) for an oversized file", got)
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("error is not ErrTooLarge: %v", err)
	}
	if got != "" {
		t.Errorf("content = %q, want empty", got)
	}

	n, _ := c.Stats()
	if n != 0 {
		t.Errorf("oversized file was cached: %d entries", n)
	}
}

// TestCacheIsBoundedByEntryCount checks that the total cache size is bounded: a
// 1 MB per-file limit with an unbounded entry count is still an unbounded cache.
func TestCacheIsBoundedByEntryCount(t *testing.T) {
	root := t.TempDir()
	c := New(root)

	total := MaxEntries + 50
	paths := make([]string, 0, total)
	for i := range total {
		rel := fmt.Sprintf("f%04d.txt", i)
		writeFile(t, root, rel, fmt.Sprintf("content %d", i))
		paths = append(paths, rel)
	}

	for _, p := range paths {
		if _, err := c.Get(p); err != nil {
			t.Fatalf("Get(%s): %v", p, err)
		}
	}

	n, _ := c.Stats()
	if n > MaxEntries {
		t.Errorf("cache holds %d entries, above the %d bound", n, MaxEntries)
	}

	// Eviction must not break correctness: an evicted path still reads.
	got, err := c.Get(paths[0])
	if err != nil {
		t.Fatalf("Get after eviction: %v", err)
	}
	if got != "content 0" {
		t.Errorf("Get after eviction = %q, want %q", got, "content 0")
	}
}

// TestCacheIsBoundedByBytes checks the byte bound with large-but-cacheable files.
func TestCacheIsBoundedByBytes(t *testing.T) {
	root := t.TempDir()
	c := New(root)

	// Each file is just under the per-file limit; enough of them to exceed the
	// total byte bound.
	const each = MaxFileSize - 1
	count := (MaxTotalBytes / each) + 3
	body := strings.Repeat("y", each)

	for i := range count {
		rel := fmt.Sprintf("big%02d.txt", i)
		writeFile(t, root, rel, body)
		if _, err := c.Get(rel); err != nil {
			t.Fatalf("Get(%s): %v", rel, err)
		}
	}

	if got := c.Bytes(); got > MaxTotalBytes {
		t.Errorf("cache retains %d bytes, above the %d bound", got, MaxTotalBytes)
	}
}

// TestGetRejectsEscapingPath checks the cache cannot serve a file outside the
// project root.
func TestGetRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c := New(project)

	for _, p := range []string{
		filepath.Join("..", "outside", "secret.txt"),
		filepath.Join("sub", "..", "..", "outside", "secret.txt"),
		filepath.Join(outside, "secret.txt"),
		"",
	} {
		t.Run(p, func(t *testing.T) {
			if got, err := c.Get(p); err == nil {
				t.Fatalf("Get(%q) returned %q, want an error", p, got)
			}
		})
	}
}

// TestInvalidateKeepsByteAccountingConsistent guards the byte counter that the
// eviction bound depends on.
func TestInvalidateKeepsByteAccountingConsistent(t *testing.T) {
	root := t.TempDir()
	c := New(root)

	writeFile(t, root, "a.txt", strings.Repeat("a", 100))
	writeFile(t, root, "b.txt", strings.Repeat("b", 200))
	if _, err := c.Get("a.txt"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := c.Get("b.txt"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := c.Bytes(); got != 300 {
		t.Fatalf("Bytes = %d, want 300", got)
	}

	// Repeated reads must not double-count.
	for range 5 {
		if _, err := c.Get("a.txt"); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}
	if got := c.Bytes(); got != 300 {
		t.Errorf("Bytes = %d after repeated reads, want 300", got)
	}

	c.Invalidate("a.txt")
	if got := c.Bytes(); got != 200 {
		t.Errorf("Bytes = %d after Invalidate, want 200", got)
	}

	c.InvalidateAll()
	if got := c.Bytes(); got != 0 {
		t.Errorf("Bytes = %d after InvalidateAll, want 0", got)
	}
}

// TestConcurrentGetAndInvalidate exercises the lock discipline under -race.
func TestConcurrentGetAndInvalidate(t *testing.T) {
	root := t.TempDir()
	c := New(root, time.Millisecond)

	for i := range 20 {
		writeFile(t, root, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("body %d", i))
	}

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range 50 {
				rel := fmt.Sprintf("f%d.txt", (w*7+i)%20)
				if _, err := c.Get(rel); err != nil {
					t.Errorf("Get(%s): %v", rel, err)
					return
				}
				if i%5 == 0 {
					c.Invalidate(rel)
				}
				if i%17 == 0 {
					c.Stats()
					c.Bytes()
				}
			}
		}(w)
	}
	wg.Wait()

	n, _ := c.Stats()
	if n > MaxEntries {
		t.Errorf("cache exceeded its bound under concurrency: %d entries", n)
	}
}

// TestRelevantFilesRespectsMaxFiles checks the documented cap is honoured even
// when the focus list alone exceeds it.
func TestRelevantFilesRespectsMaxFiles(t *testing.T) {
	root := t.TempDir()
	c := New(root)

	focus := make([]string, 0, 30)
	for i := range 30 {
		rel := fmt.Sprintf("f%02d.go", i)
		writeFile(t, root, rel, "package main\n")
		focus = append(focus, rel)
	}

	got := c.RelevantFiles(focus, 10)
	if len(got) > 10 {
		t.Errorf("RelevantFiles returned %d paths, want at most 10", len(got))
	}
}
