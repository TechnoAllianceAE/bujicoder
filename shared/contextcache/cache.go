// Package contextcache provides a local file content cache with TTL-based
// invalidation, import graph analysis, and git-diff integration. It avoids
// redundant disk reads when an agent accesses the same files repeatedly
// within a session.
package contextcache

import (
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
	root    string
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]*Entry // keyed by relative path
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
	c.mu.RLock()
	entry, ok := c.entries[relPath]
	c.mu.RUnlock()

	if ok && !entry.stale(c.ttl) {
		// Quick mtime check to catch writes within TTL.
		absPath := filepath.Join(c.root, relPath)
		if info, err := os.Stat(absPath); err == nil && info.ModTime().Equal(entry.ModTime) {
			c.mu.Lock()
			entry.AccessedAt = time.Now()
			c.mu.Unlock()
			return entry.Content, nil
		}
	}

	// Cache miss or stale — read from disk.
	return c.refresh(relPath)
}

// Prefetch reads the given paths into the cache in one batch.
func (c *Cache) Prefetch(paths []string) {
	for _, p := range paths {
		_, _ = c.refresh(p)
	}
}

// Invalidate removes entries for the given paths.
func (c *Cache) Invalidate(paths ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range paths {
		delete(c.entries, p)
	}
}

// InvalidateAll clears the entire cache.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*Entry)
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

// refresh reads a file from disk and updates the cache.
func (c *Cache) refresh(relPath string) (string, error) {
	absPath := filepath.Join(c.root, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", os.ErrInvalid
	}
	if info.Size() > MaxFileSize {
		return "", nil // skip large files silently
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	content := string(data)
	lang := detectLanguage(relPath)
	imports := extractImports(content, lang)

	entry := &Entry{
		Path:       relPath,
		Content:    content,
		Size:       info.Size(),
		ModTime:    info.ModTime(),
		Language:   lang,
		Imports:    imports,
		CachedAt:   time.Now(),
		AccessedAt: time.Now(),
	}

	c.mu.Lock()
	c.entries[relPath] = entry
	c.mu.Unlock()

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
