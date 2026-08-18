// Package contextcache provides a local file content cache with TTL-based
// invalidation, import graph analysis, and git-diff integration. It avoids
// redundant disk reads when an agent accesses the same files repeatedly
// within a session.
package contextcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultTTL is how long cached file content stays valid before re-checking mtime.
const DefaultTTL = 30 * time.Second

// MaxFileSize is the largest file we'll cache (1 MB).
const MaxFileSize = 1 << 20

// MaxEntries and MaxTotalBytes bound the whole cache. A per-file limit alone
// leaves the cache unbounded: prefetching a large repository would otherwise
// retain every file for the lifetime of the session.
const (
	MaxEntries    = 512
	MaxTotalBytes = 32 << 20 // 32 MB
)

// ErrTooLarge is returned for files above MaxFileSize. Callers must fall back to
// a direct read: reporting such a file as empty content would silently hide it.
var ErrTooLarge = errors.New("file too large to cache")

// Entry holds cached metadata and content for a single file.
type Entry struct {
	Path       string
	Content    string
	Size       int64
	ModTime    time.Time
	Language   string
	Imports    []string
	CachedAt   time.Time
	AccessedAt time.Time
}

// stale returns true if the entry has expired based on the given TTL.
func (e *Entry) stale(ttl time.Duration) bool {
	return time.Since(e.CachedAt) > ttl
}

// Cache is a concurrency-safe in-memory file content cache scoped to a project.
type Cache struct {
	root       string
	ttl        time.Duration
	mu         sync.RWMutex
	entries    map[string]*Entry // keyed by relative path
	totalBytes int64
}

// New creates a Cache rooted at the given project directory.
func New(projectRoot string, ttl ...time.Duration) *Cache {
	t := DefaultTTL
	if len(ttl) > 0 && ttl[0] > 0 {
		t = ttl[0]
	}
	return &Cache{
		root:    projectRoot,
		ttl:     t,
		entries: make(map[string]*Entry),
	}
}

// Get returns the cached content for relPath. If the cache entry is stale or
// missing it reads from disk, updates the cache, and returns fresh content.
// Returns ("", error) if the file cannot be read.
func (c *Cache) Get(relPath string) (string, error) {
	if err := c.checkPath(relPath); err != nil {
		return "", err
	}

	c.mu.RLock()
	entry, ok := c.entries[relPath]
	c.mu.RUnlock()

	if ok && !entry.stale(c.ttl) {
		// Quick freshness check to catch writes within TTL.
		if content, fresh := c.validate(entry); fresh {
			c.mu.Lock()
			entry.AccessedAt = time.Now()
			c.mu.Unlock()
			return content, nil
		}
	}

	// Cache miss or stale — acquire write lock and double-check before refresh
	// to prevent concurrent goroutines from refreshing the same entry.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check: another goroutine may have refreshed while we waited for the lock.
	if entry, ok := c.entries[relPath]; ok && !entry.stale(c.ttl) {
		if content, fresh := c.validate(entry); fresh {
			entry.AccessedAt = time.Now()
			return content, nil
		}
	}

	return c.refreshLocked(relPath)
}

// validate reports whether a cached entry still matches the file on disk.
// Both mtime and size are compared: mtime resolution is coarse on some
// filesystems, so a rewrite within the same tick would otherwise serve stale
// content for the rest of the TTL.
func (c *Cache) validate(entry *Entry) (string, bool) {
	info, err := os.Stat(filepath.Join(c.root, entry.Path))
	if err != nil {
		return "", false
	}
	if !info.ModTime().Equal(entry.ModTime) || info.Size() != entry.Size {
		return "", false
	}
	return entry.Content, true
}

// Prefetch reads the given paths into the cache in one batch.
func (c *Cache) Prefetch(paths []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range paths {
		if c.checkPath(p) != nil {
			continue
		}
		_, _ = c.refreshLocked(p)
	}
}

// Invalidate removes entries for the given paths.
func (c *Cache) Invalidate(paths ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range paths {
		c.dropLocked(p)
	}
}

// InvalidateAll clears the entire cache.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*Entry)
	c.totalBytes = 0
}

// Stats returns current cache size and a snapshot of entry paths.
func (c *Cache) Stats() (int, []string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	paths := make([]string, 0, len(c.entries))
	for p := range c.entries {
		paths = append(paths, p)
	}
	return len(c.entries), paths
}

// Bytes returns the total number of content bytes currently retained.
func (c *Cache) Bytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalBytes
}

// checkPath rejects paths that resolve outside the cache root, so a traversing
// path can never be served from the cache.
func (c *Cache) checkPath(relPath string) error {
	if relPath == "" {
		return fmt.Errorf("empty path")
	}
	if filepath.IsAbs(relPath) {
		return fmt.Errorf("path %q must be relative to the project root", relPath)
	}
	clean := filepath.Clean(relPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes the project root", relPath)
	}
	return nil
}

// dropLocked removes an entry and its byte accounting.
// Caller must hold c.mu write lock.
func (c *Cache) dropLocked(relPath string) {
	if e, ok := c.entries[relPath]; ok {
		c.totalBytes -= int64(len(e.Content))
		delete(c.entries, relPath)
	}
}

// evictLocked drops least-recently-accessed entries until the cache is within
// its entry and byte bounds.
// Caller must hold c.mu write lock.
func (c *Cache) evictLocked() {
	for len(c.entries) > MaxEntries || c.totalBytes > MaxTotalBytes {
		var oldestPath string
		var oldest time.Time
		for p, e := range c.entries {
			if oldestPath == "" || e.AccessedAt.Before(oldest) {
				oldestPath, oldest = p, e.AccessedAt
			}
		}
		if oldestPath == "" {
			return
		}
		c.dropLocked(oldestPath)
	}
}

// refreshLocked reads a file from disk and updates the cache.
// Caller must hold c.mu write lock.
func (c *Cache) refreshLocked(relPath string) (string, error) {
	absPath := filepath.Join(c.root, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", os.ErrInvalid
	}
	if info.Size() > MaxFileSize {
		// Not cached, and reported as an error so callers read it directly
		// instead of treating the file as empty.
		c.dropLocked(relPath)
		return "", fmt.Errorf("%s (%d bytes): %w", relPath, info.Size(), ErrTooLarge)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	content := string(data)
	lang := detectLanguage(relPath)
	imports := extractImports(content, lang)

	now := time.Now()
	entry := &Entry{
		Path:       relPath,
		Content:    content,
		Size:       int64(len(data)),
		ModTime:    info.ModTime(),
		Language:   lang,
		Imports:    imports,
		CachedAt:   now,
		AccessedAt: now,
	}

	c.dropLocked(relPath)
	c.entries[relPath] = entry
	c.totalBytes += int64(len(content))
	c.evictLocked()
	return content, nil
}

// detectLanguage infers the programming language from the file extension.
func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".c", ".h":
		return "c"
	case ".cpp", ".hpp", ".cc":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".kt", ".kts":
		return "kotlin"
	case ".md":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".sql":
		return "sql"
	case ".sh", ".bash":
		return "shell"
	case ".proto":
		return "protobuf"
	default:
		return ""
	}
}
