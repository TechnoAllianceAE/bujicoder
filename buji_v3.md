# BujiCoder v3 — Feature Upgrade Plan

> Based on source-level analysis of Kilocode's codebase. Each feature includes
> the exact files to modify, data structures, algorithms, and integration logic.
>
> **Goal**: Not just parity with Kilocode — superiority.

---

## Code Quality Improvements

> **Quick wins** — These 5 improvements address code quality, security, and maintainability issues found during source analysis. They can be implemented independently of the feature roadmap.

### 1. Fix Race Condition in Context Cache

**File:** [`shared/contextcache/cache.go`](shared/contextcache/cache.go:62)

**Problem:** The [`Cache.Get()`](shared/contextcache/cache.go:62) method has a race condition. It reads the entry under a read lock, checks if it's stale, then releases the lock before calling [`refresh()`](shared/contextcache/cache.go:117). Another goroutine could modify the cache between these operations.

**Solution:** Use double-checked locking with a write lock for refresh:

```go
func (c *Cache) Get(relPath string) (string, error) {
    c.mu.RLock()
    entry, ok := c.entries[relPath]
    c.mu.RUnlock()

    if ok && !entry.stale(c.ttl) {
        absPath := filepath.Join(c.root, relPath)
        if info, err := os.Stat(absPath); err == nil && info.ModTime().Equal(entry.ModTime) {
            c.mu.Lock()
            entry.AccessedAt = time.Now()
            c.mu.Unlock()
            return entry.Content, nil
        }
    }

    // Use write lock for refresh to prevent concurrent refreshes
    c.mu.Lock()
    defer c.mu.Unlock()
    
    // Double-check after acquiring write lock
    if entry, ok := c.entries[relPath]; ok && !entry.stale(c.ttl) {
        return entry.Content, nil
    }
    
    return c.refreshLocked(relPath)
}
```

**Impact:** Prevents data races in concurrent tool execution scenarios.

---

### 2. Improve Hash Function for Tool Call Loop Detection

**File:** [`shared/agentruntime/step.go`](shared/agentruntime/step.go:472)

**Problem:** The [`hashArgs()`](shared/agentruntime/step.go:472) function uses a simple polynomial hash (`h = h*31 + uint64(c)`) that's prone to collisions, especially with similar JSON arguments. This could cause false positives in loop detection.

**Solution:** Use FNV-1a hash for better collision resistance:

```go
import "hash/fnv"

func hashArgs(argsJSON string) string {
    h := fnv.New64a()
    h.Write([]byte(argsJSON))
    return fmt.Sprintf("%016x", h.Sum64())
}
```

**Impact:** More reliable loop detection, preventing false positives that could break legitimate tool call sequences.

---

### 3. Extract Tool Schema Definitions to Reduce Duplication

**File:** [`shared/agentruntime/step.go`](shared/agentruntime/step.go:311)

**Problem:** The [`toolInputSchema()`](shared/agentruntime/step.go:311) function is a 150+ line switch statement with repetitive schema definitions. This is hard to maintain and violates DRY principles.

**Solution:** Define schemas as a map:

```go
var toolSchemas = map[string]map[string]any{
    "read_files": {
        "type": "object",
        "properties": map[string]any{
            "paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
        },
        "required": []string{"paths"},
    },
    "write_file": {
        "type": "object",
        "properties": map[string]any{
            "path":    map[string]any{"type": "string"},
            "content": map[string]any{"type": "string"},
        },
        "required": []string{"path", "content"},
    },
    // ... other schemas
}

func toolInputSchema(toolName string) map[string]any {
    if schema, ok := toolSchemas[toolName]; ok {
        return schema
    }
    return map[string]any{"type": "object", "properties": map[string]any{}}
}
```

**Impact:** Easier to maintain, reduces code by ~100 lines, and makes adding new tools simpler.

---

### 4. Add Context Cancellation to Web Search

**File:** [`shared/tools/tools.go`](shared/tools/tools.go:675)

**Problem:** The [`webSearch()`](shared/tools/tools.go:675) function creates a new HTTP client with a fixed timeout but doesn't properly respect the context's cancellation. If the context is cancelled, the HTTP request may continue running.

**Solution:** Use context-aware HTTP request:

```go
func webSearch() func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        var params struct {
            Query string `json:"query"`
        }
        if err := json.Unmarshal(args, &params); err != nil {
            return "", err
        }

        searchURL := "https://html.duckduckgo.com/html/?q=" + strings.ReplaceAll(params.Query, " ", "+")

        req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
        if err != nil {
            return "", fmt.Errorf("create search request: %w", err)
        }
        req.Header.Set("User-Agent", "BujiCoder/1.0 (CLI)")

        client := &http.Client{Timeout: 10 * time.Second}
        resp, err := client.Do(req)
        if err != nil {
            // Check if context was cancelled
            if ctx.Err() != nil {
                return "", ctx.Err()
            }
            return "", fmt.Errorf("web search: %w", err)
        }
        defer resp.Body.Close()

        // ... rest of the function
    }
}
```

**Impact:** Proper cancellation support prevents resource leaks when agent runs are cancelled or timeout.

---

### 5. Add Input Validation for File Paths

**File:** [`shared/tools/tools.go`](shared/tools/tools.go:781)

**Problem:** The [`safePath()`](shared/tools/tools.go:781) function validates paths but doesn't check for null bytes or other dangerous characters that could cause issues. Additionally, the function doesn't validate path length.

**Solution:** Add comprehensive path validation:

```go
func safePath(workDir, path string) (string, error) {
    // Check for null bytes (path traversal attack vector)
    if strings.ContainsRune(path, '\x00') {
        return "", fmt.Errorf("access denied: path contains null byte")
    }

    // Check path length (prevent extremely long paths)
    const maxPathLength = 4096
    if len(path) > maxPathLength {
        return "", fmt.Errorf("access denied: path exceeds maximum length of %d characters", maxPathLength)
    }

    // Check for suspicious patterns
    suspiciousPatterns := []string{"..", "~", "$", "`", "\\", "|", "<", ">", ":", "*", "?", "\""}
    for _, pattern := range suspiciousPatterns {
        if strings.Contains(path, pattern) {
            return "", fmt.Errorf("access denied: path contains suspicious character %q", pattern)
        }
    }

    resolved := path
    if !filepath.IsAbs(path) {
        resolved = filepath.Join(workDir, path)
    }
    resolved = filepath.Clean(resolved)

    // Canonicalize the workDir for comparison (follow symlinks)
    canonicalRoot, err := filepath.EvalSymlinks(workDir)
    if err != nil {
        canonicalRoot = filepath.Clean(workDir)
    }

    // Canonicalize the target if it exists (follow symlinks)
    canonicalResolved, err := filepath.EvalSymlinks(resolved)
    if err != nil {
        canonicalResolved = resolved
    }

    if canonicalResolved != canonicalRoot &&
        !strings.HasPrefix(canonicalResolved, canonicalRoot+string(filepath.Separator)) {
        return "", fmt.Errorf("access denied: path %q is outside the project directory", path)
    }
    return resolved, nil
}
```

**Impact:** Enhanced security against path traversal attacks and malformed path inputs.

---

### Summary of Code Improvements

| # | Issue | File | Impact |
|---|-------|------|--------|
| 1 | Race condition in cache | `shared/contextcache/cache.go` | Concurrency safety |
| 2 | Weak hash function | `shared/agentruntime/step.go` | Loop detection reliability |
| 3 | Code duplication | `shared/agentruntime/step.go` | Maintainability |
| 4 | Missing context cancellation | `shared/tools/tools.go` | Resource leak prevention |
| 5 | Insufficient path validation | `shared/tools/tools.go` | Security hardening |

All changes are backward-compatible and can be implemented independently.

---

## Top 5 Priority Improvements

> **Recommended starting point** — these 5 improvements offer the highest impact-to-effort ratio
> and should be prioritized for the first development cycle.

### 1. Fuzzy Edit Matching (7-Strategy str_replace)
- **Problem**: Current `str_replace` uses exact `strings.Contains()` matching, causing silent failures when LLMs produce wrong whitespace/indentation
- **Solution**: Cascading matcher chain in `shared/tools/editmatch/` with 7 strategies from strict to lenient
- **Impact**: Immediate improvement in agent reliability, reduced token waste from failed edits
- **Effort**: 3-4 days | **Phase**: A (v3.0)
- **See**: [Section 1](#1-fuzzy-edit-matching)

### 2. Error Logging System
- **Problem**: `zerolog.Nop()` everywhere — no persistent logs, silent failures, no post-mortem capability
- **Solution**: Structured JSON logging to `~/.bujicoder/logs/` with lumberjack rotation, `--verbose` flag
- **Impact**: Enables debugging, user support, performance monitoring, and metrics insight
- **Effort**: 2-3 days | **Phase**: A (v3.0)
- **See**: [Section 5a](#5a-error-logging-system)

### 3. Batch Parallel Tool Execution
- **Problem**: All tool calls execute sequentially, even independent read operations
- **Solution**: Safe/Unsafe classification with concurrent dispatch for read-only tools (max 10 parallel)
- **Impact**: Significant latency reduction for multi-file reads and searches
- **Effort**: 1-2 days | **Phase**: A (v3.0)
- **See**: [Section 5](#5-batch-parallel-tool-execution)

### 4. Git Snapshot & Revert System
- **Problem**: No way to undo agent changes — wrong edits require manual identification and revert
- **Solution**: Shadow git repo in `.bujicoder/snapshots/` with auto-snapshot after writes, `revert_snapshot` tool
- **Impact**: Safety net for experimental runs, builds user trust, never pollutes user's git history
- **Effort**: 4-5 days | **Phase**: A (v3.0)
- **See**: [Section 2](#2-git-snapshot--revert-system)

### 5. Smart Context Assembly (Relevance-Ranked Files)
- **Problem**: Sends top 200 files as flat list, wasting context tokens on irrelevant files
- **Solution**: Multi-factor relevance scoring (keyword match, git changes, symbols, imports, recency) → top 50 ranked
- **Impact**: Better agent responses with less token waste — a **competitive superiority feature**
- **Effort**: 3 days | **Phase**: C (v3.9)
- **See**: [Section 16](#16-smart-context-assembly)

---

## Table of Contents

### Foundation
0. [Database Choice: Why bbolt+Bleve Over SQLite](#0-database-choice-why-bboltbleve-over-sqlite)

### Phase A — Core Quality (v3.0)
1. [Fuzzy Edit Matching (7-Strategy str_replace)](#1-fuzzy-edit-matching)
2. [Git Snapshot & Revert System](#2-git-snapshot--revert-system)
3. [Embedded Storage (bbolt + Bleve)](#3-embedded-storage-bbolt--bleve)
4. [LSP Diagnostics After Edits](#4-lsp-diagnostics-after-edits)
5. [Batch Parallel Tool Execution](#5-batch-parallel-tool-execution)
5a. [Error Logging System](#5a-error-logging-system)

### Phase B — Full Parity (v3.5)
6. [MCP OAuth + HTTP/SSE Transports](#6-mcp-oauth--httpsse-transports)
7. [MultiEdit & ApplyPatch Tools](#7-multiedit--applypatch-tools)
8. [Plugin System](#8-plugin-system)
9. [Additional LLM Providers](#9-additional-llm-providers)
10. [Todo Tracking Tools](#10-todo-tracking-tools)
11. [Skill System](#11-skill-system)

### Phase C — Superiority (v3.9)
12. [Worktree Isolation](#12-worktree-isolation)
13. [tree-sitter Code Intelligence](#13-tree-sitter-code-intelligence)
14. [Per-Model Prompt Variants](#14-per-model-prompt-variants)
15. [Structured Output Enforcement](#15-structured-output-enforcement)
16. [Smart Context Assembly](#16-smart-context-assembly)
17. [Agent Workflow Composer](#17-agent-workflow-composer)
18. [Live File Watcher & Auto-Context](#18-live-file-watcher--auto-context)

### Ordering & Summary
19. [Dependency & Ordering](#19-dependency--ordering)

---

## 0. Database Choice: Why bbolt+Bleve Over SQLite

### The Question

Kilocode (TypeScript) uses SQLite via Drizzle ORM. Should BujiCoder (Go) follow
the same path, or use something more Go-native?

### Options Evaluated

| Database | Type | Go-native | CGo needed | Binary size | Best for |
|----------|------|-----------|------------|-------------|----------|
| **SQLite** via `modernc.org/sqlite` | Relational | Pure Go port | No | +8MB | SQL queries, joins, FTS5 |
| **SQLite** via `mattn/go-sqlite3` | Relational | CGo wrapper | **Yes** | +2MB | SQL, but breaks single-binary |
| **bbolt** (`go.etcd.io/bbolt`) | Key-value (B+tree) | Pure Go | No | +200KB | Fast reads, simple schemas |
| **BadgerDB** | Key-value (LSM) | Pure Go | No | +3MB | High write throughput |
| **Pebble** | Key-value (LSM) | Pure Go | No | +5MB | CockroachDB's engine, heavy |

### Recommendation: bbolt + Bleve

**Primary storage: bbolt** (etcd's embedded database)
- Used by etcd, Consul, InfluxDB — battle-tested at scale
- Pure Go, zero CGo, tiny binary impact (~200KB vs SQLite's ~8MB)
- B+tree gives fast reads (conversation listing is the hot path)
- ACID transactions with MVCC
- Single-file database (`~/.bujicoder/bujicoder.db`)
- Memory-mapped I/O — performs well on large datasets
- Simpler than SQL — BujiCoder's data model doesn't need joins

**Full-text search: Bleve** (`github.com/blevesearch/bleve`)
- Pure Go full-text search engine (like Lucene for Go)
- Replaces SQLite's FTS5
- Supports stemming, fuzzy matching, phrase queries
- Index stored alongside bbolt db (`~/.bujicoder/search.bleve/`)
- Used by CouchDB, Minio — proven at scale

### Why NOT SQLite

| Concern | Details |
|---------|---------|
| **Binary bloat** | `modernc.org/sqlite` adds ~8MB to the binary. bbolt adds ~200KB. |
| **Not Go-idiomatic** | SQL strings embedded in Go code feel foreign. bbolt uses native Go types. |
| **Schema migrations** | SQL needs migration tooling. bbolt uses versioned bucket names — no migrations. |
| **CGo alternative is worse** | `mattn/go-sqlite3` requires CGo, breaking cross-compilation and single-binary distribution. |
| **Overkill** | BujiCoder's data model is simple key-value (conversation → messages). No relational joins needed. |

### Why NOT BadgerDB

- Designed for write-heavy workloads (LSM tree). BujiCoder is read-heavy.
- Compaction can spike CPU. bbolt's B+tree has no compaction.
- Larger binary and memory footprint than bbolt.

### Data Model with bbolt

```
Bucket: "conversations"
  Key: <conversation-id>        → Value: JSON{id, title, created_at, updated_at, parent_id, cost_cents}

Bucket: "messages"
  Key: <conversation-id>/<seq>  → Value: JSON{role, content, tool_calls_json, step_number, snapshot_id, created_at}

Bucket: "metadata"
  Key: "schema_version"         → Value: "1"
  Key: "last_cleanup"           → Value: RFC3339 timestamp
```

**Why this works:**
- `ListConversations`: Iterate `conversations` bucket in reverse (bbolt maintains key order). No full-table scan.
- `GetMessages`: Prefix scan on `messages` bucket with `<conversation-id>/` prefix. Returns messages in insertion order.
- `ForkConversation`: Copy keys from source conversation with new ID prefix. Single transaction.
- `Search`: Bleve index on message content, returns conversation IDs + snippets.

### Impact on Plan

The SQLite section (Section 3) is updated to use bbolt+Bleve. All other sections
that reference SQLite (`snapshot_id` in messages, session forking, cost tracking)
work identically — the storage layer is an implementation detail behind the
same `Store` interface.

---

## 1. Fuzzy Edit Matching

### Problem

`str_replace` uses exact `strings.Contains()` matching. LLMs frequently produce
replacements with wrong whitespace, indentation, or escape characters. This
causes silent failures that waste agent steps.

### Current Code

**File:** `shared/tools/tools.go:279-322`

```go
if !strings.Contains(content, params.OldStr) {
    return "", fmt.Errorf("old_str not found in %s", params.Path)
}
newContent := strings.Replace(content, params.OldStr, params.NewStr, 1)
```

Single strategy: exact substring → first-occurrence replace.

### Solution: Cascading Matcher Chain

Create a new package `shared/tools/editmatch/` with a chain of matchers that
run in order. The first matcher to find exactly one match wins.

#### File: `shared/tools/editmatch/matcher.go`

```go
package editmatch

// MatchResult holds a successful match location in the file content.
type MatchResult struct {
    Start    int    // byte offset of match start
    End      int    // byte offset of match end
    Matched  string // the actual text that was matched
    Strategy string // which strategy found it
}

// Matcher is a single matching strategy.
type Matcher interface {
    Name() string
    FindMatch(content, oldStr string) ([]MatchResult, error)
}

// Chain runs matchers in order. Returns the first single-match result.
func Chain(content, oldStr string, matchers []Matcher) (*MatchResult, error) {
    for _, m := range matchers {
        results, err := m.FindMatch(content, oldStr)
        if err != nil {
            continue
        }
        if len(results) == 1 {
            return &results[0], nil
        }
        // len > 1 means ambiguous match, try next strategy
    }
    return nil, fmt.Errorf("old_str not found (tried %d strategies)", len(matchers))
}
```

#### The 7 Strategies (ordered from strictest to most lenient)

Each strategy is a struct implementing `Matcher`:

**Strategy 1 — ExactMatcher** (current behavior)
```
strings.Contains(content, oldStr) → byte offsets
```
Fastest. No transformation. Preserves current behavior as first attempt.

**Strategy 2 — LineTrimmedMatcher**
```
Trim leading/trailing whitespace from each line of both content and oldStr.
Match on trimmed versions, but return the byte offsets from the original content.
```
Handles LLM adding/removing trailing spaces on lines.

**Strategy 3 — WhitespaceNormalizedMatcher**
```
Collapse all runs of whitespace (spaces, tabs) within lines to single spaces.
Match on normalized versions. Return original byte offsets.
```
Handles tab-vs-spaces and multi-space differences.

**Strategy 4 — IndentationFlexibleMatcher**
```
Strip all leading whitespace from each line.
Match on de-indented versions. Return original byte offsets.
Require at least 3 non-empty lines to prevent false matches.
```
Handles LLM producing wrong indentation level.

**Strategy 5 — EscapeNormalizedMatcher**
```
Normalize escape sequences: \" → ", \' → ', \n → \n, \t → \t.
Match on normalized versions. Return original byte offsets.
```
Handles LLM escaping/unescaping quotes differently.

**Strategy 6 — BlockAnchorMatcher** (Levenshtein-scored)
```
Split oldStr into lines. Take first 2 and last 2 lines as anchors.
Find anchor matches in content with Levenshtein distance ≤ 2 per line.
Extract candidate block between anchors.
Score full block similarity. Accept if similarity > 0.85.
```
Handles minor edits within the block while anchoring on boundaries.
This is the most powerful strategy for large replacements.

**Strategy 7 — MultiOccurrenceContextMatcher**
```
If exact match finds multiple occurrences:
  - Expand oldStr context: take 3 lines before and after from the original
    file content surrounding the *intended* match
  - Re-run exact match with expanded context
  - If exactly one match found → return it
```
Disambiguates when the same string appears multiple times.

#### Levenshtein Helper

```go
// shared/tools/editmatch/levenshtein.go
func Distance(a, b string) int {
    // Standard dynamic programming O(m*n) implementation
    // For lines (short strings), this is fast enough
}

func Similarity(a, b string) float64 {
    maxLen := max(len(a), len(b))
    if maxLen == 0 { return 1.0 }
    return 1.0 - float64(Distance(a, b))/float64(maxLen)
}
```

#### Integration into str_replace

**File:** `shared/tools/tools.go` — modify `strReplace()`:

```go
import "bujicoder/shared/tools/editmatch"

func strReplace(workDir string, perms *ProjectPermissions) func(...) (string, error) {
    matchers := editmatch.DefaultChain() // returns []Matcher in order

    return func(ctx context.Context, args json.RawMessage) (string, error) {
        // ... existing param parsing, plan mode check, path check ...

        content := string(data)
        match, err := editmatch.Chain(content, params.OldStr, matchers)
        if err != nil {
            return "", fmt.Errorf("old_str not found in %s: %w", params.Path, err)
        }

        // Replace the matched region with new string
        newContent := content[:match.Start] + params.NewStr + content[match.End:]
        os.WriteFile(absPath, []byte(newContent), 0o644)

        // Cache invalidation (unchanged)
        if cache := getContextCache(ctx); cache != nil {
            cache.Invalidate(params.Path)
        }

        result := fmt.Sprintf("Replacement applied (strategy: %s)", match.Strategy)
        return result, nil
    }
}
```

Same change applies to `proposeEdit()` in `proposal.go`.

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/tools/editmatch/matcher.go` | Chain runner + MatchResult type |
| Create | `shared/tools/editmatch/exact.go` | Strategy 1 |
| Create | `shared/tools/editmatch/linetrimmed.go` | Strategy 2 |
| Create | `shared/tools/editmatch/whitespace.go` | Strategy 3 |
| Create | `shared/tools/editmatch/indent.go` | Strategy 4 |
| Create | `shared/tools/editmatch/escape.go` | Strategy 5 |
| Create | `shared/tools/editmatch/blockanchor.go` | Strategy 6 + Levenshtein |
| Create | `shared/tools/editmatch/multioccurrence.go` | Strategy 7 |
| Create | `shared/tools/editmatch/matcher_test.go` | Test suite with real-world LLM failure cases |
| Modify | `shared/tools/tools.go` | Use `editmatch.Chain()` in `strReplace()` |
| Modify | `shared/tools/proposal.go` | Use `editmatch.Chain()` in `proposeEdit()` |

#### Testing Strategy

Create test cases from real LLM failures:

```go
// matcher_test.go
func TestLineTrimmedMatch(t *testing.T) {
    content := "  func hello() {\n    fmt.Println(\"hi\")  \n  }\n"
    oldStr  := "func hello() {\n    fmt.Println(\"hi\")\n  }"  // no trailing spaces
    // Should match via LineTrimmedMatcher
}

func TestIndentationMismatch(t *testing.T) {
    content := "\t\tfunc hello() {\n\t\t\tfmt.Println(\"hi\")\n\t\t}\n"
    oldStr  := "func hello() {\n  fmt.Println(\"hi\")\n}"  // spaces vs tabs
    // Should match via IndentationFlexibleMatcher
}

func TestBlockAnchorWithMinorDiff(t *testing.T) {
    content := "func process(x int) error {\n\tif x < 0 {\n\t\treturn errors.New(\"negative\")\n\t}\n\treturn nil\n}\n"
    oldStr  := "func process(x int) error {\n\tif x < 0 {\n\t\treturn errors.New(\"neg value\")\n\t}\n\treturn nil\n}\n"
    // Minor diff in middle line. Should match via BlockAnchorMatcher.
}
```

---

## 2. Git Snapshot & Revert System

### Problem

No way to undo agent changes. If an agent makes a wrong edit at step 5 of 20,
the user must manually identify and revert changes. Kilocode solves this with
a shadow git repo that tracks snapshots per step.

### Architecture

Create a new package `shared/snapshot/` that manages a **shadow `.bujicoder/snapshots/`
git repository** inside the project root. This repo tracks file states independently
of the user's actual git repo.

#### Core Data Structures

```go
// shared/snapshot/snapshot.go

package snapshot

type Manager struct {
    projectRoot  string           // user's project root
    snapshotDir  string           // .bujicoder/snapshots/
    repo         *git.Repository  // shadow repo (go-git or exec)
    mu           sync.Mutex
}

type Snapshot struct {
    ID        string    // short hash (first 8 of tree hash)
    TreeHash  string    // full git tree hash
    StepNum   int       // agent step number
    AgentID   string    // which agent made changes
    ToolName  string    // which tool triggered this
    Timestamp time.Time
    Files     []string  // files modified in this step
}
```

#### Workflow

**1. Initialization (per agent run)**

```go
func NewManager(projectRoot string) (*Manager, error) {
    snapshotDir := filepath.Join(projectRoot, ".bujicoder", "snapshots")
    // If dir doesn't exist: git init --bare-like structure
    // Copy current project files as initial commit
    return &Manager{...}, nil
}
```

Uses `git init` + `git add` + `git commit` via exec (not a library dependency).
The `.bujicoder/snapshots/` directory should be added to `.gitignore`.

**2. Take Snapshot (after each write tool)**

```go
func (m *Manager) Take(stepNum int, agentID, toolName string, modifiedFiles []string) (*Snapshot, error) {
    m.mu.Lock()
    defer m.mu.Unlock()

    // 1. Copy modified files into snapshot repo working tree
    for _, f := range modifiedFiles {
        src := filepath.Join(m.projectRoot, f)
        dst := filepath.Join(m.snapshotDir, "work", f)
        copyFile(src, dst)
    }

    // 2. git add + git commit with metadata
    //    Commit message: "step:{stepNum} agent:{agentID} tool:{toolName}"
    //    This gives us a tree hash we can revert to

    // 3. Get tree hash
    treeHash := execGit(m.snapshotDir, "rev-parse", "HEAD^{tree}")

    return &Snapshot{
        ID:        treeHash[:8],
        TreeHash:  treeHash,
        StepNum:   stepNum,
        AgentID:   agentID,
        ToolName:  toolName,
        Timestamp: time.Now().UTC(),
        Files:     modifiedFiles,
    }, nil
}
```

**3. List Snapshots (for user display)**

```go
func (m *Manager) List() ([]Snapshot, error) {
    // Parse git log with custom format
    // git log --format="%H %s" → parse step/agent/tool from commit message
    // Return newest first
}
```

**4. Revert to Snapshot**

```go
func (m *Manager) Revert(snapshotID string) ([]FileChange, error) {
    // 1. Find commit by tree hash prefix
    // 2. Get list of files in that snapshot's tree
    // 3. For each file: copy from snapshot working tree → project root
    // 4. Return list of files restored

    // This does NOT touch the user's git repo
    // It only restores file contents to the state at that snapshot
}
```

**5. Diff Between Snapshots**

```go
func (m *Manager) Diff(fromID, toID string) (string, error) {
    // git diff fromHash toHash
    // Returns unified diff
}
```

**6. Cleanup**

```go
func (m *Manager) Cleanup(olderThan time.Duration) error {
    // Delete snapshots older than threshold
    // Keep at most 100 snapshots per run
}
```

#### Integration Points

**A. Tool Dispatch — Auto-Snapshot After Writes**

**File:** `shared/agentruntime/dispatch.go`

After executing `write_file` or `str_replace`, take a snapshot:

```go
func dispatchToolCalls(ctx context.Context, rt *Runtime, toolCalls []llm.ToolCallEvent,
                      cfg RunConfig) ([]llm.ContentPart, error) {
    // ... existing code ...

    for _, tc := range toolCalls {
        result, err := tool.Execute(ctx, tc.ArgumentsJSON)

        // NEW: After write tools, take snapshot
        if isWriteTool(tc.Name) && err == nil {
            modifiedFiles := extractPaths(tc.Name, tc.ArgumentsJSON)
            if cfg.SnapshotManager != nil {
                snap, _ := cfg.SnapshotManager.Take(stepNum, cfg.AgentDef.ID, tc.Name, modifiedFiles)
                if snap != nil {
                    cfg.OnEvent(Event{Type: EventStatus, Text: fmt.Sprintf("Snapshot %s taken", snap.ID)})
                }
            }
        }
    }
}

func isWriteTool(name string) bool {
    return name == "write_file" || name == "str_replace"
}

func extractPaths(toolName string, args json.RawMessage) []string {
    var p struct{ Path string `json:"path"` }
    json.Unmarshal(args, &p)
    return []string{p.Path}
}
```

**B. RunConfig — Add SnapshotManager**

**File:** `shared/agentruntime/runtime.go`

```go
type RunConfig struct {
    // ... existing fields ...
    SnapshotManager *snapshot.Manager // NEW
}
```

**C. New Tool: `revert_snapshot`**

**File:** `shared/tools/tools.go` — register new tool

```go
r.Register(&Tool{
    Name:        "revert_snapshot",
    Description: "Revert project files to a previous snapshot state",
    Execute:     revertSnapshot(workDir),
})
```

Parameters: `{"snapshot_id": "abc12345"}` or `{"step": 5}`

This tool requires approval (like `run_terminal_command`).

**D. New Slash Command: `/snapshots`**

**File:** `cli/app/model.go` — add handler

Shows list of snapshots with step numbers, timestamps, modified files.
User can select one to revert.

**E. .gitignore Entry**

Auto-append `.bujicoder/` to project `.gitignore` if not present.

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/snapshot/snapshot.go` | Manager, Snapshot types, Take/List/Revert/Diff/Cleanup |
| Create | `shared/snapshot/snapshot_test.go` | Tests with temp git repos |
| Modify | `shared/agentruntime/runtime.go` | Add `SnapshotManager` to `RunConfig` |
| Modify | `shared/agentruntime/dispatch.go` | Auto-snapshot after write tools |
| Modify | `shared/tools/tools.go` | Register `revert_snapshot` tool |
| Modify | `cli/app/model.go` | `/snapshots` command, revert UI |
| Modify | `cli/cmd/buji/main.go` | Initialize `SnapshotManager` |

#### Design Decisions

- **Shadow repo, not user's repo**: Never pollute the user's git history with
  automated snapshots. The shadow repo is disposable.
- **exec-based git, not go-git library**: Keeps dependencies minimal. Git is
  universally available. Shell out to `git` with 5-second timeouts.
- **Per-step granularity**: One snapshot per write tool call, not per LLM step.
  A step with 3 write tools produces 3 snapshots.
- **Approval required for revert**: Revert modifies files, so it goes through
  the same approval system as `run_terminal_command`.

---

## 3. Embedded Storage (bbolt + Bleve)

### Problem

JSON files are read entirely for listing (wasteful), can't be searched, don't
support forking, and have no transactional safety.

### Current Code

**File:** `cli/localstore/store.go`

```go
type ConversationFile struct {
    ID        string          `json:"id"`
    Title     string          `json:"title"`
    CreatedAt time.Time       `json:"created_at"`
    UpdatedAt time.Time       `json:"updated_at"`
    Messages  []StoredMessage `json:"messages"`
}

// API: SaveConversation, AppendMessages, ListConversations, GetMessages, DeleteConversation
```

### Solution: bbolt + Bleve

See [Section 0](#0-database-choice-why-bboltbleve-over-sqlite) for why
bbolt+Bleve over SQLite.

#### Storage Layout

```
~/.bujicoder/
├── bujicoder.db          # bbolt database (conversations + messages)
├── search.bleve/         # Bleve full-text search index
├── bujicoder.yaml        # config (unchanged)
├── conversations.bak/    # backup of old JSON files after migration
└── logs/                 # error logs (see Section 5a)
```

#### bbolt Bucket Design

```
Bucket: "conversations"
  Key:   <uuid>
  Value: JSON {
    "id":         "550e8400-...",
    "title":      "Fix auth bug",
    "created_at": "2025-03-11T15:30:45Z",
    "updated_at": "2025-03-11T16:02:10Z",
    "parent_id":  null,           // for forking
    "cost_cents": 0.42,
    "summary":    null            // compaction summary
  }

Bucket: "messages"
  Key:   <uuid>/<seq-number-zero-padded-8>   e.g. "550e8400.../00000001"
  Value: JSON {
    "role":            "assistant",
    "content":         "Here's the fix...",
    "tool_calls_json": null,
    "step_number":     3,
    "snapshot_id":     "abc12345",
    "created_at":      "2025-03-11T15:31:02Z"
  }

Bucket: "metadata"
  Key: "schema_version" → "1"
  Key: "last_cleanup"   → "2025-03-11T00:00:00Z"
```

**Key design:** Message keys use `<conversation-id>/<zero-padded-sequence>`.
bbolt stores keys in sorted byte order, so prefix scans on a conversation ID
return all messages in insertion order — no index needed.

#### Store Interface

```go
// shared/store/store.go

package store

type Store struct {
    db     *bbolt.DB
    index  bleve.Index
}

func Open(dbPath, indexPath string) (*Store, error)
func (s *Store) Close() error

// --- Backward-compatible API (same signatures as localstore) ---
func (s *Store) SaveConversation(id, title string, msgs []StoredMessage) error
func (s *Store) AppendMessages(id, title string, msgs ...StoredMessage) error
func (s *Store) ListConversations(limit, offset int) ([]ConversationSummary, error)
func (s *Store) GetMessages(id string) ([]StoredMessage, error)
func (s *Store) DeleteConversation(id string) error

// --- New capabilities ---
func (s *Store) SearchMessages(query string, limit int) ([]SearchResult, error)
func (s *Store) ForkConversation(fromID string, atMessageSeq int) (string, error)
func (s *Store) UpdateCost(id string, costCents float64) error
```

#### Key Implementation Logic

**ListConversations** — Reverse iteration (newest first):
```
1. Open read-only tx on "conversations" bucket
2. Create cursor, seek to end
3. Iterate backward with cursor.Prev()
4. Skip `offset` entries, collect `limit` entries
5. Unmarshal only id, title, created_at, updated_at (lightweight)
```

**AppendMessages** — Single write transaction:
```
1. Begin read-write tx
2. Get or create conversation in "conversations" bucket
3. Count existing messages for this conversation (prefix count)
4. Write new messages with incrementing sequence keys
5. Update conversation's updated_at
6. Index message content in Bleve
7. Commit tx
```

**SearchMessages** — Bleve query:
```
1. bleve.NewQueryStringQuery(query)
2. Search returns document IDs (which are message keys)
3. Parse conversation IDs from message keys
4. Fetch conversation titles from "conversations" bucket
5. Return results with snippets (Bleve provides highlights)
```

**ForkConversation** — Prefix copy:
```
1. Begin read-write tx
2. Copy conversation entry with new ID and parent_id = source ID
3. Iterate messages with prefix scan on source conversation
4. Copy messages up to atMessageSeq with new conversation ID prefix
5. Commit tx
```

#### Migration from JSON

```go
// shared/store/migrate.go

func MigrateFromJSON(jsonDir string, store *Store) error {
    // 1. Glob all *.json files in jsonDir
    // 2. For each file: unmarshal ConversationFile, call SaveConversation
    // 3. Index all messages in Bleve
    // 4. Rename jsonDir to jsonDir + ".bak"
}
```

Auto-migrate on first launch if `~/.bujicoder/conversations/` exists but
`~/.bujicoder/bujicoder.db` does not.

#### Integration

**File:** `cli/app/setup.go` — replace `localstore.NewStore()`:
```go
dbPath := filepath.Join(configDir, "bujicoder.db")
indexPath := filepath.Join(configDir, "search.bleve")
s, err := store.Open(dbPath, indexPath)
```

**File:** `cli/app/model.go` — add `/search` command:
```go
case "/search":
    query := strings.TrimPrefix(input, "/search ")
    results, _ := m.store.SearchMessages(query, 20)
    // Display results with conversation title + highlighted snippet
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/store/store.go` | bbolt+Bleve store implementation |
| Create | `shared/store/migrate.go` | JSON → bbolt migration |
| Create | `shared/store/store_test.go` | Tests |
| Modify | `cli/app/setup.go` | Use new store |
| Modify | `cli/app/model.go` | Use new store API, add `/search`, update `/history` |
| Modify | `go.mod` | Add `go.etcd.io/bbolt`, `github.com/blevesearch/bleve/v2` |
| Delete | `cli/localstore/` | Remove after migration period (v4) |

#### Dependencies

```
go get go.etcd.io/bbolt
go get github.com/blevesearch/bleve/v2
```

Pure Go, no CGo. Binary size increase: ~200KB (bbolt) + ~4MB (Bleve) = **~4.2MB**
total (vs ~8MB for SQLite). No system dependencies.

---

## 4. LSP Diagnostics After Edits

### Problem

Agent edits can introduce syntax errors that cascade into further failures.
Without immediate feedback, the agent continues building on broken code.

### Architecture

Create a lightweight LSP client that connects to language servers and requests
diagnostics after file edits. Not a full LSP implementation — only
`textDocument/didOpen`, `textDocument/didChange`, and `textDocument/publishDiagnostics`.

#### File: `shared/lsp/client.go`

```go
package lsp

import (
    "encoding/json"
    "os/exec"
    "bufio"
)

type Client struct {
    cmd    *exec.Cmd
    stdin  io.WriteCloser
    stdout *bufio.Reader
    nextID int
    mu     sync.Mutex
}

type Diagnostic struct {
    File     string `json:"file"`
    Line     int    `json:"line"`
    Column   int    `json:"column"`
    Severity string `json:"severity"` // "error", "warning", "info", "hint"
    Message  string `json:"message"`
    Source   string `json:"source"`   // e.g., "gopls", "typescript"
}

// Start launches a language server process.
func Start(command string, args []string, rootDir string) (*Client, error) {
    cmd := exec.Command(command, args...)
    cmd.Dir = rootDir
    stdin, _ := cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    c := &Client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
    c.initialize(rootDir)
    return c, nil
}

// DiagnoseFile opens/updates a file and collects diagnostics.
func (c *Client) DiagnoseFile(path, content string) ([]Diagnostic, error) {
    c.mu.Lock()
    defer c.mu.Unlock()

    // 1. Send textDocument/didOpen (or didChange if already open)
    c.sendNotification("textDocument/didOpen", map[string]any{
        "textDocument": map[string]any{
            "uri":        "file://" + path,
            "languageId": detectLanguage(path),
            "version":    1,
            "text":       content,
        },
    })

    // 2. Wait for textDocument/publishDiagnostics notification (with timeout)
    diags := c.waitForDiagnostics(path, 3*time.Second)

    // 3. Filter to errors only (ignore warnings/hints)
    var errors []Diagnostic
    for _, d := range diags {
        if d.Severity == "error" {
            errors = append(errors, d)
        }
    }
    return errors, nil
}

func (c *Client) Close() {
    c.sendNotification("shutdown", nil)
    c.sendNotification("exit", nil)
    c.cmd.Wait()
}
```

#### Language Server Discovery

```go
// shared/lsp/detect.go

type ServerConfig struct {
    Command string
    Args    []string
}

// Detect returns the appropriate LSP server for a file extension.
func DetectServer(filePath string) (*ServerConfig, bool) {
    ext := filepath.Ext(filePath)
    switch ext {
    case ".go":
        if _, err := exec.LookPath("gopls"); err == nil {
            return &ServerConfig{Command: "gopls", Args: []string{"serve"}}, true
        }
    case ".ts", ".tsx", ".js", ".jsx":
        if _, err := exec.LookPath("typescript-language-server"); err == nil {
            return &ServerConfig{Command: "typescript-language-server", Args: []string{"--stdio"}}, true
        }
    case ".py":
        if _, err := exec.LookPath("pylsp"); err == nil {
            return &ServerConfig{Command: "pylsp"}, true
        }
    case ".rs":
        if _, err := exec.LookPath("rust-analyzer"); err == nil {
            return &ServerConfig{Command: "rust-analyzer"}, true
        }
    }
    return nil, false
}
```

#### LSP Manager (Caches Server Connections)

```go
// shared/lsp/manager.go

type Manager struct {
    clients map[string]*Client // keyed by language server command
    rootDir string
    mu      sync.Mutex
}

func NewManager(rootDir string) *Manager {
    return &Manager{clients: make(map[string]*Client), rootDir: rootDir}
}

func (m *Manager) Diagnose(filePath, content string) ([]Diagnostic, error) {
    cfg, ok := DetectServer(filePath)
    if !ok {
        return nil, nil // No LSP available, skip silently
    }

    m.mu.Lock()
    client, exists := m.clients[cfg.Command]
    if !exists {
        var err error
        client, err = Start(cfg.Command, cfg.Args, m.rootDir)
        if err != nil {
            m.mu.Unlock()
            return nil, nil // LSP unavailable, degrade gracefully
        }
        m.clients[cfg.Command] = client
    }
    m.mu.Unlock()

    return client.DiagnoseFile(filePath, content)
}

func (m *Manager) CloseAll() {
    for _, c := range m.clients {
        c.Close()
    }
}
```

#### Integration into str_replace and write_file

**File:** `shared/tools/tools.go`

After a successful write, run diagnostics and append to result:

```go
func strReplace(workDir string, perms *ProjectPermissions) func(...) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        // ... existing edit logic ...

        result := "Replacement applied"

        // NEW: Run LSP diagnostics if manager available
        if lspMgr := getLSPManager(ctx); lspMgr != nil {
            diags, _ := lspMgr.Diagnose(absPath, newContent)
            if len(diags) > 0 {
                result += "\n\nSyntax errors detected after edit:"
                for i, d := range diags {
                    if i >= 10 { // cap at 10 errors
                        result += fmt.Sprintf("\n... and %d more errors", len(diags)-10)
                        break
                    }
                    result += fmt.Sprintf("\n  Line %d:%d: %s", d.Line, d.Column, d.Message)
                }
            }
        }

        return result, nil
    }
}
```

Add context key for LSP manager:

```go
const lspMgrCtxKey contextKey = "tools_lsp_manager"

func WithLSPManager(ctx context.Context, mgr *lsp.Manager) context.Context {
    return context.WithValue(ctx, lspMgrCtxKey, mgr)
}

func getLSPManager(ctx context.Context) *lsp.Manager {
    v, _ := ctx.Value(lspMgrCtxKey).(*lsp.Manager)
    return v
}
```

**File:** `shared/agentruntime/dispatch.go` — inject LSP manager into context:

```go
if cfg.LSPManager != nil {
    ctx = tools.WithLSPManager(ctx, cfg.LSPManager)
}
```

#### Design Decisions

- **Graceful degradation**: If no LSP server is installed, tool works exactly
  as before. No errors, no dependencies.
- **Lazy start**: LSP servers only start when the first file of that language
  is edited.
- **Error-only**: Only report errors, not warnings/hints. Keep the signal clean.
- **Cap at 10 errors**: Prevent flooding the LLM context with diagnostic noise.
- **3-second timeout**: Don't block tool execution waiting for slow LSP servers.

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/lsp/client.go` | LSP JSON-RPC client (minimal) |
| Create | `shared/lsp/detect.go` | Language server discovery |
| Create | `shared/lsp/manager.go` | Connection pool + caching |
| Create | `shared/lsp/types.go` | LSP protocol types (initialize, didOpen, diagnostics) |
| Create | `shared/lsp/client_test.go` | Tests with mock server |
| Modify | `shared/tools/tools.go` | Add LSP context key, diagnostics in str_replace/write_file |
| Modify | `shared/agentruntime/dispatch.go` | Inject LSP manager into context |
| Modify | `shared/agentruntime/runtime.go` | Add `LSPManager` to `RunConfig` |
| Modify | `cli/cmd/buji/main.go` | Initialize LSP manager |

---

## 5. Batch Parallel Tool Execution

### Problem

Currently, tool calls execute sequentially in `dispatchToolCalls()`. When an
LLM emits multiple independent tool calls (e.g., 3 `read_files`), they run
one after another. Parallel execution would cut latency significantly.

### Current Code

**File:** `shared/agentruntime/dispatch.go`

```go
func dispatchToolCalls(ctx context.Context, rt *Runtime, toolCalls []llm.ToolCallEvent,
                      cfg RunConfig) ([]llm.ContentPart, error) {
    var results []llm.ContentPart
    for _, tc := range toolCalls {
        // execute sequentially
        result, err := executeTool(ctx, rt, tc, cfg)
        results = append(results, result)
    }
    return results, nil
}
```

### Solution: Concurrent Dispatch with Safety Classification

Not all tools can run in parallel. Write tools must be serialized to prevent
race conditions on the same file. Read-only tools can run freely in parallel.

#### Tool Safety Classification

```go
// shared/tools/tools.go

type ToolSafety int

const (
    SafeParallel  ToolSafety = iota // read-only, no side effects
    UnsafeParallel                   // writes files, runs commands
)

type Tool struct {
    Name        string
    Description string
    InputSchema map[string]any
    Execute     func(ctx context.Context, args json.RawMessage) (string, error)
    Safety      ToolSafety // NEW
}
```

Classification:

| Tool | Safety |
|------|--------|
| `read_files` | SafeParallel |
| `list_directory` | SafeParallel |
| `glob` | SafeParallel |
| `find_files` | SafeParallel |
| `code_search` | SafeParallel |
| `web_search` | SafeParallel |
| `think_deeply` | SafeParallel |
| `ask_user` | UnsafeParallel |
| `write_file` | UnsafeParallel |
| `str_replace` | UnsafeParallel |
| `run_terminal_command` | UnsafeParallel |
| `propose_edit` | SafeParallel (proposal collector is mutex-protected) |
| `propose_write_file` | SafeParallel (same) |

#### New Dispatch Logic

```go
// shared/agentruntime/dispatch.go

func dispatchToolCalls(ctx context.Context, rt *Runtime, toolCalls []llm.ToolCallEvent,
                      cfg RunConfig) ([]llm.ContentPart, error) {

    // Phase 1: Classify tool calls
    type classified struct {
        index int
        tc    llm.ToolCallEvent
        safe  bool
    }

    var safeCalls, unsafeCalls []classified
    for i, tc := range toolCalls {
        tool, ok := rt.toolRegistry.Get(tc.Name)
        if !ok || tool.Safety == UnsafeParallel {
            unsafeCalls = append(unsafeCalls, classified{i, tc, false})
        } else {
            safeCalls = append(safeCalls, classified{i, tc, true})
        }
    }

    // Phase 2: Execute safe calls in parallel
    results := make([]llm.ContentPart, len(toolCalls))
    var wg sync.WaitGroup

    for _, sc := range safeCalls {
        wg.Add(1)
        go func(c classified) {
            defer wg.Done()
            result := executeSingleTool(ctx, rt, c.tc, cfg)
            results[c.index] = result
            cfg.OnEvent(Event{Type: EventToolResult, ToolCallID: c.tc.ID, Text: resultText(result)})
        }(sc)
    }
    wg.Wait()

    // Phase 3: Execute unsafe calls sequentially
    for _, uc := range unsafeCalls {
        result := executeSingleTool(ctx, rt, uc.tc, cfg)
        results[uc.index] = result
        cfg.OnEvent(Event{Type: EventToolResult, ToolCallID: uc.tc.ID, Text: resultText(result)})
    }

    return results, nil
}
```

#### Concurrency Cap

Add a semaphore to prevent spawning too many goroutines:

```go
const maxParallelTools = 10

func dispatchToolCalls(...) {
    sem := make(chan struct{}, maxParallelTools)

    for _, sc := range safeCalls {
        wg.Add(1)
        sem <- struct{}{} // acquire
        go func(c classified) {
            defer wg.Done()
            defer func() { <-sem }() // release
            // ... execute ...
        }(sc)
    }
    wg.Wait()
}
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Modify | `shared/tools/tools.go` | Add `Safety` field to `Tool`, classify each tool |
| Modify | `shared/agentruntime/dispatch.go` | Parallel dispatch for safe tools |
| Create | `shared/agentruntime/dispatch_test.go` | Concurrency tests |

---

## 5a. Error Logging System

### Problem

BujiCoder currently initialises zerolog as `zerolog.Nop()` everywhere — both in the
CLI (`cli/app/model.go:2206`) and in tests. This means:

- **No persistent logs**: errors are displayed in the TUI but never written to disk
- **Silent failures**: LLM provider errors, network timeouts, model-not-found, and
  sub-agent spawn failures are wrapped and returned but never recorded
- **No post-mortem**: when users report bugs, there's no log trail to diagnose them
- **No metrics insight**: can't tell which models fail most, which tools timeout, etc.

Kilocode writes structured logs to its SQLite database. BujiCoder needs an equivalent
persistent error trail — but simpler, using structured log files.

### Design

**Log file location**: `~/.bujicoder/logs/`

```
~/.bujicoder/logs/
├── bujicoder.log           # Current session log (structured JSON, one line per event)
├── bujicoder.log.1         # Previous rotation (kept by lumberjack)
├── bujicoder.log.2
└── ...                     # Up to 5 rotated files, 10 MB each
```

**Log levels** (zerolog levels, mapped to BujiCoder concerns):

| Level | What gets logged |
|-------|-----------------|
| `error` | LLM API failures, network errors, tool execution crashes, permission denials, invalid model IDs |
| `warn` | Model fallback triggered, slow responses (>30s), deprecated config fields, MCP server restart |
| `info` | Session start/end, cost mode changes, agent spawns, tool invocations (name only, no content) |
| `debug` | Full request/response bodies (opt-in via `BUJICODER_LOG_LEVEL=debug`) |

### Implementation

#### 1. Create `shared/logging/logging.go`

New package that initialises a zerolog logger writing to both a file (JSON) and
optionally to stderr (human-readable, for `--verbose` flag):

```go
package logging

import (
    "os"
    "path/filepath"
    "github.com/rs/zerolog"
    "gopkg.in/natefinished/lumberjack.v2"
)

type Config struct {
    Dir        string // default: ~/.bujicoder/logs
    MaxSizeMB  int    // default: 10
    MaxBackups int    // default: 5
    MaxAgeDays int    // default: 30
    Level      string // default: "info", override with BUJICODER_LOG_LEVEL
    Verbose    bool   // if true, also write human-readable to stderr
}

func New(cfg Config) zerolog.Logger {
    // 1. Ensure log dir exists
    logDir := cfg.Dir
    if logDir == "" {
        home, _ := os.UserHomeDir()
        logDir = filepath.Join(home, ".bujicoder", "logs")
    }
    os.MkdirAll(logDir, 0o755)

    // 2. File writer with rotation (lumberjack)
    fileWriter := &lumberjack.Logger{
        Filename:   filepath.Join(logDir, "bujicoder.log"),
        MaxSize:    cfg.MaxSizeMB,  // MB
        MaxBackups: cfg.MaxBackups,
        MaxAge:     cfg.MaxAgeDays,
        Compress:   true,
    }

    // 3. Multi-writer: file (always) + stderr (if verbose)
    var writers []io.Writer
    writers = append(writers, fileWriter) // JSON format for file
    if cfg.Verbose {
        writers = append(writers, zerolog.ConsoleWriter{Out: os.Stderr})
    }

    // 4. Parse log level
    level, err := zerolog.ParseLevel(cfg.Level)
    if err != nil {
        level = zerolog.InfoLevel
    }

    return zerolog.New(zerolog.MultiLevelWriter(writers...)).
        Level(level).
        With().
        Timestamp().
        Str("version", buildinfo.Version).
        Logger()
}
```

**Dependency**: [`gopkg.in/natefinished/lumberjack.v2`](https://github.com/natefinished/lumberjack)
— single-file log rotation, no external deps, ~300 lines. Already widely used in Go
CLI tools (Docker, Kubernetes).

#### 2. Replace `zerolog.Nop()` calls

| File | Current | Change to |
|------|---------|-----------|
| `cli/app/model.go:2206` | `log := zerolog.Nop()` | `log := logging.New(logging.Config{...})` — initialised once at TUI startup, passed to all subsystems |
| `shared/agentruntime/runtime.go:85` | Accepts `log zerolog.Logger` param | No change — already parameterised, just receives real logger now |
| `shared/llm/catalog.go:158` | Accepts `log zerolog.Logger` param | No change — already parameterised |
| `shared/llm/pricing.go:38` | Accepts `log zerolog.Logger` param | No change — already parameterised |

The key change is in `model.go` — the TUI entry point creates one real logger and
threads it through to the agent runtime, LLM registry, and tool registry.

#### 3. Instrument error paths

Add structured log calls at these critical failure points:

**LLM Provider errors** (`shared/llm/*.go`):
```go
log.Error().
    Str("provider", provider.Name()).
    Str("model", modelID).
    Int("status", resp.StatusCode).
    Str("error_type", "api_error").    // api_error | network | timeout | rate_limit
    Dur("latency", elapsed).
    Msg("LLM request failed")
```

**Tool execution errors** (`shared/tools/tools.go`):
```go
log.Error().
    Str("tool", toolName).
    Str("agent", agentName).
    Err(err).
    Msg("tool execution failed")
```

**Sub-agent spawn failures** (`shared/agentruntime/runtime.go`):
```go
log.Error().
    Str("parent_agent", parentName).
    Str("child_agent", childName).
    Str("model", modelID).
    Err(err).
    Msg("sub-agent spawn failed")
```

**Model validation errors** (`shared/costmode/costmode.go`):
```go
log.Warn().
    Str("mode", costMode).
    Str("role", role).
    Str("model", modelID).
    Msg("model not found in catalog, using fallback")
```

**Network/timeout errors** (`cli/sdk/client.go`):
```go
log.Error().
    Str("endpoint", url).
    Dur("timeout", timeout).
    Err(err).
    Msg("server request failed")
```

**MCP server errors** (MCP integration files):
```go
log.Error().
    Str("mcp_server", serverName).
    Str("tool", toolName).
    Err(err).
    Msg("MCP tool call failed")
```

#### 4. Add `--verbose` flag and `BUJICODER_LOG_LEVEL` env var

| Interface | Mechanism |
|-----------|-----------|
| `buji --verbose` | Sets `logging.Config.Verbose = true`, writes human-readable to stderr alongside TUI |
| `BUJICODER_LOG_LEVEL=debug` | Sets zerolog level to debug, logs full request/response payloads |
| `BUJICODER_LOG_DIR=/tmp/buji-logs` | Override log directory |

Add to `cli/cmd/cli/main.go`:
```go
verbose := flag.Bool("verbose", false, "Enable verbose logging to stderr")
```

#### 5. Add `/logs` slash command

A TUI command that shows the last N log entries in a scrollable view:
```
/logs            — Show last 50 log entries
/logs errors     — Show only error-level entries
/logs tail       — Live-tail the log file
```

Implementation: read `~/.bujicoder/logs/bujicoder.log`, parse JSON lines, render in
the TUI's output pane with colour-coded levels.

### Structured Log Schema

Every log line is a JSON object with these base fields:

```json
{
    "level": "error",
    "time": "2026-03-11T14:30:00Z",
    "version": "0.31.0",
    "session_id": "abc123",
    "message": "LLM request failed",
    "provider": "openrouter",
    "model": "x-ai/grok-code-fast-1",
    "error_type": "rate_limit",
    "status": 429,
    "latency_ms": 1250
}
```

This structure enables:
- `jq '.level == "error"' bujicoder.log` — filter errors
- `jq 'select(.provider == "openrouter")' bujicoder.log` — provider-specific debugging
- `jq 'select(.latency_ms > 30000)' bujicoder.log` — find slow requests
- Future: feed into bbolt for searchable error history (see Section 3)

### Files to Create/Modify

| Action | File | Purpose |
|--------|------|---------|
| Create | `shared/logging/logging.go` | Logger initialisation with lumberjack rotation |
| Create | `shared/logging/logging_test.go` | Test file creation, rotation, level filtering |
| Modify | `cli/cmd/cli/main.go` | Add `--verbose` flag, init logger |
| Modify | `cli/app/model.go` | Replace `zerolog.Nop()` with real logger |
| Modify | `shared/llm/*.go` | Add error/warn log calls to all providers |
| Modify | `shared/tools/tools.go` | Log tool execution failures |
| Modify | `shared/agentruntime/runtime.go` | Log agent spawn and step errors |
| Modify | `shared/costmode/costmode.go` | Log model resolution warnings |
| Modify | `cli/sdk/client.go` | Log network/timeout errors |
| Modify | `go.mod` | Add `gopkg.in/natefinished/lumberjack.v2` dependency |

### Estimated Effort

~400 lines new code, ~150 lines modifications. **2–3 days**.

---

## 6. MCP OAuth + HTTP/SSE Transports

### Problem

BujiCoder's MCP only supports stdio (local process) servers. Kilocode supports
HTTP, SSE, and OAuth — enabling remote MCP servers, cloud-hosted tools, and
authenticated endpoints. This is increasingly important as the MCP ecosystem
grows.

### Current Code

**File:** `shared/mcp/mcp.go`

Current MCP integration uses the official Go SDK but only stdio transport:
- Server launched as subprocess via `command` + `args`
- Communication over stdin/stdout pipes
- No network transport, no authentication

### Solution: Multi-Transport MCP Client

#### A. StreamableHTTP Transport

```go
// shared/mcp/transport_http.go

package mcp

import (
    "net/http"
    "encoding/json"
)

type HTTPTransport struct {
    baseURL    string
    httpClient *http.Client
    headers    map[string]string
    sessionID  string
}

func NewHTTPTransport(baseURL string, headers map[string]string) *HTTPTransport {
    return &HTTPTransport{
        baseURL:    baseURL,
        httpClient: &http.Client{Timeout: 30 * time.Second},
        headers:    headers,
    }
}

// Send sends a JSON-RPC request and returns the response.
func (t *HTTPTransport) Send(ctx context.Context, method string, params any) (json.RawMessage, error) {
    body := map[string]any{
        "jsonrpc": "2.0",
        "id":      nextID(),
        "method":  method,
        "params":  params,
    }

    req, _ := http.NewRequestWithContext(ctx, "POST", t.baseURL, marshalBody(body))
    req.Header.Set("Content-Type", "application/json")
    for k, v := range t.headers {
        req.Header.Set(k, v)
    }
    if t.sessionID != "" {
        req.Header.Set("Mcp-Session-Id", t.sessionID)
    }

    resp, err := t.httpClient.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()

    // Capture session ID from response
    if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
        t.sessionID = sid
    }

    var result jsonRPCResponse
    json.NewDecoder(resp.Body).Decode(&result)
    return result.Result, nil
}
```

#### B. SSE Transport (with HTTP fallback)

```go
// shared/mcp/transport_sse.go

type SSETransport struct {
    eventURL   string
    postURL    string
    httpClient *http.Client
    headers    map[string]string
    events     chan json.RawMessage
}

func NewSSETransport(baseURL string, headers map[string]string) *SSETransport {
    t := &SSETransport{
        eventURL: baseURL + "/sse",
        postURL:  baseURL,
        headers:  headers,
        events:   make(chan json.RawMessage, 100),
    }
    go t.listenSSE() // background SSE listener
    return t
}

func (t *SSETransport) listenSSE() {
    req, _ := http.NewRequest("GET", t.eventURL, nil)
    req.Header.Set("Accept", "text/event-stream")
    for k, v := range t.headers {
        req.Header.Set(k, v)
    }

    resp, err := t.httpClient.Do(req)
    if err != nil { return }
    defer resp.Body.Close()

    scanner := bufio.NewScanner(resp.Body)
    var dataBuf strings.Builder
    for scanner.Scan() {
        line := scanner.Text()
        if strings.HasPrefix(line, "data: ") {
            dataBuf.WriteString(strings.TrimPrefix(line, "data: "))
        } else if line == "" && dataBuf.Len() > 0 {
            // Complete event
            t.events <- json.RawMessage(dataBuf.String())
            dataBuf.Reset()
        }
    }
}

func (t *SSETransport) Send(ctx context.Context, method string, params any) (json.RawMessage, error) {
    // POST the request
    body := map[string]any{"jsonrpc": "2.0", "id": nextID(), "method": method, "params": params}
    req, _ := http.NewRequestWithContext(ctx, "POST", t.postURL, marshalBody(body))
    // ... send request ...

    // Wait for matching response on SSE channel
    select {
    case event := <-t.events:
        return event, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(30 * time.Second):
        return nil, fmt.Errorf("SSE response timeout")
    }
}
```

#### C. OAuth Authentication Flow

```go
// shared/mcp/oauth.go

type OAuthConfig struct {
    ClientID     string `yaml:"client_id"`
    ClientSecret string `yaml:"client_secret"`
    AuthURL      string `yaml:"auth_url"`
    TokenURL     string `yaml:"token_url"`
    Scopes       []string `yaml:"scopes"`
}

type OAuthManager struct {
    config     OAuthConfig
    tokenStore string // path to token cache file
}

// StartAuth initiates the OAuth flow by opening a browser.
func (o *OAuthManager) StartAuth() (string, error) {
    // 1. Generate state parameter (CSRF protection)
    state := generateRandomState()

    // 2. Build authorization URL
    authURL := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&scope=%s&state=%s&response_type=code",
        o.config.AuthURL, o.config.ClientID,
        url.QueryEscape("http://localhost:19836/callback"),
        url.QueryEscape(strings.Join(o.config.Scopes, " ")),
        state)

    // 3. Start local HTTP server to receive callback
    // 4. Open browser to authURL
    return authURL, nil
}

// FinishAuth exchanges authorization code for access token.
func (o *OAuthManager) FinishAuth(code, state string) (*Token, error) {
    // POST to token URL with authorization code
    // Store token in tokenStore path
    // Return access token
}

// GetToken returns a valid token, refreshing if expired.
func (o *OAuthManager) GetToken() (string, error) {
    token, err := o.loadToken()
    if err != nil || token.Expired() {
        return o.refreshToken(token)
    }
    return token.AccessToken, nil
}
```

#### D. Config Extension

**File:** `bujicoder.yaml` — extended MCP server config:

```yaml
mcp_servers:
  # Local stdio server (existing)
  - name: filesystem
    command: /usr/local/bin/mcp-filesystem
    args: ["/home/user/projects"]
    lazy: true

  # Remote HTTP server (NEW)
  - name: cloud-tools
    url: "https://mcp.example.com/v1"
    transport: http          # "stdio" | "http" | "sse"
    headers:
      X-API-Key: "sk-..."
    timeout: 30s

  # OAuth-authenticated server (NEW)
  - name: github-mcp
    url: "https://mcp.github.com"
    transport: sse
    oauth:
      client_id: "abc123"
      auth_url: "https://github.com/login/oauth/authorize"
      token_url: "https://github.com/login/oauth/access_token"
      scopes: ["repo", "read:org"]
```

#### E. Transport Factory

```go
// shared/mcp/transport.go

type Transport interface {
    Send(ctx context.Context, method string, params any) (json.RawMessage, error)
    Close() error
}

func NewTransport(cfg ServerConfig) (Transport, error) {
    switch cfg.Transport {
    case "http":
        headers := cfg.Headers
        if cfg.OAuth != nil {
            mgr := NewOAuthManager(cfg.OAuth, tokenCachePath(cfg.Name))
            token, err := mgr.GetToken()
            if err != nil { return nil, fmt.Errorf("OAuth required: %w", err) }
            headers["Authorization"] = "Bearer " + token
        }
        return NewHTTPTransport(cfg.URL, headers), nil
    case "sse":
        headers := cfg.Headers
        // same OAuth logic
        return NewSSETransport(cfg.URL, headers), nil
    default: // "stdio"
        return NewStdioTransport(cfg.Command, cfg.Args), nil
    }
}
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/mcp/transport.go` | Transport interface + factory |
| Create | `shared/mcp/transport_http.go` | StreamableHTTP transport |
| Create | `shared/mcp/transport_sse.go` | SSE transport with event parsing |
| Create | `shared/mcp/oauth.go` | OAuth flow (browser redirect + local callback server) |
| Create | `shared/mcp/token.go` | Token storage, refresh, expiry |
| Modify | `shared/mcp/mcp.go` | Use transport factory instead of hardcoded stdio |
| Modify | `cli/config/config.go` | Parse new MCP config fields (url, transport, oauth, headers) |
| Create | `shared/mcp/transport_test.go` | Tests with mock HTTP/SSE servers |

---

## 7. MultiEdit & ApplyPatch Tools

### Problem

BujiCoder's `str_replace` modifies one location per call. For large refactors
the LLM must issue dozens of sequential tool calls. Kilocode has `MultiEdit`
(batch edits on one file) and `ApplyPatch` (unified diff application).

### A. MultiEdit Tool

```go
// shared/tools/tools.go — new tool

func multiEdit(workDir string, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        var params struct {
            Edits []struct {
                Path   string `json:"path"`
                OldStr string `json:"old_str"`
                NewStr string `json:"new_str"`
            } `json:"edits"`
        }
        if err := json.Unmarshal(args, &params); err != nil {
            return "", err
        }

        // Group edits by file to minimize disk I/O
        byFile := make(map[string][]edit)
        for _, e := range params.Edits {
            byFile[e.Path] = append(byFile[e.Path], edit{e.OldStr, e.NewStr})
        }

        var results []string
        matchers := editmatch.DefaultChain()

        for path, edits := range byFile {
            absPath, err := safePath(effectiveWorkDir(ctx, workDir), path)
            if err != nil { return "", err }

            if perms.IsPathRestricted(path) {
                results = append(results, fmt.Sprintf("SKIP %s: restricted", path))
                continue
            }

            data, err := os.ReadFile(absPath)
            if err != nil { return "", err }
            content := string(data)

            // Apply edits sequentially within the file (order matters)
            applied := 0
            for i, e := range edits {
                match, err := editmatch.Chain(content, e.OldStr, matchers)
                if err != nil {
                    results = append(results, fmt.Sprintf("FAIL %s edit %d: %v", path, i+1, err))
                    continue
                }
                content = content[:match.Start] + e.NewStr + content[match.End:]
                applied++
            }

            os.WriteFile(absPath, []byte(content), 0o644)

            if cache := getContextCache(ctx); cache != nil {
                cache.Invalidate(path)
            }

            results = append(results, fmt.Sprintf("OK %s: %d/%d edits applied", path, applied, len(edits)))
        }

        return strings.Join(results, "\n"), nil
    }
}
```

Register as:
```go
r.Register(&Tool{
    Name:        "multi_edit",
    Description: "Apply multiple str_replace edits in a single call. Edits within a file are applied sequentially.",
    Execute:     multiEdit(workDir, o.Permissions),
    Safety:      UnsafeParallel,
})
```

### B. ApplyPatch Tool

Accepts a unified diff and applies it to the project files.

```go
// shared/tools/patch.go

package tools

import (
    "strings"
)

type PatchOp struct {
    Action  string // "add", "update", "move", "delete"
    Path    string
    NewPath string // for "move" only
    Content string // for "add" / "update"
}

// ParsePatch parses a unified diff into patch operations.
func ParsePatch(patchText string) ([]PatchOp, error) {
    var ops []PatchOp
    lines := strings.Split(patchText, "\n")
    i := 0

    for i < len(lines) {
        line := lines[i]

        // --- a/path or +++ b/path headers
        if strings.HasPrefix(line, "--- a/") || strings.HasPrefix(line, "--- /dev/null") {
            fromPath := ""
            if strings.HasPrefix(line, "--- a/") {
                fromPath = strings.TrimPrefix(line, "--- a/")
            }
            i++
            if i >= len(lines) { break }

            toLine := lines[i]
            toPath := ""
            if strings.HasPrefix(toLine, "+++ b/") {
                toPath = strings.TrimPrefix(toLine, "+++ b/")
            }
            i++

            // Determine operation type
            if fromPath == "" && toPath != "" {
                // New file: collect all '+' lines
                content := collectAddedLines(lines, &i)
                ops = append(ops, PatchOp{Action: "add", Path: toPath, Content: content})
            } else if fromPath != "" && toPath == "" {
                // Deleted file
                ops = append(ops, PatchOp{Action: "delete", Path: fromPath})
                skipHunks(lines, &i)
            } else if fromPath != toPath {
                // Move/rename
                content := collectFullContent(lines, &i)
                ops = append(ops, PatchOp{Action: "move", Path: fromPath, NewPath: toPath, Content: content})
            } else {
                // Update: apply hunks
                content := collectFullContent(lines, &i)
                ops = append(ops, PatchOp{Action: "update", Path: toPath, Content: content})
            }
        } else {
            i++
        }
    }

    return ops, nil
}

// applyPatch is the tool executor.
func applyPatch(workDir string, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        var params struct {
            Patch string `json:"patch"` // unified diff text
        }
        json.Unmarshal(args, &params)

        ops, err := ParsePatch(params.Patch)
        if err != nil { return "", err }

        var results []string
        for _, op := range ops {
            absPath, err := safePath(effectiveWorkDir(ctx, workDir), op.Path)
            if err != nil {
                results = append(results, fmt.Sprintf("SKIP %s: %v", op.Path, err))
                continue
            }

            switch op.Action {
            case "add":
                os.MkdirAll(filepath.Dir(absPath), 0o755)
                os.WriteFile(absPath, []byte(op.Content), 0o644)
                results = append(results, fmt.Sprintf("ADD %s", op.Path))

            case "update":
                // Use the system `patch` command for robust hunk application
                cmd := exec.CommandContext(ctx, "patch", "-p1", "--no-backup-if-mismatch")
                cmd.Dir = effectiveWorkDir(ctx, workDir)
                cmd.Stdin = strings.NewReader(params.Patch)
                output, err := cmd.CombinedOutput()
                if err != nil {
                    results = append(results, fmt.Sprintf("FAIL %s: %s", op.Path, string(output)))
                } else {
                    results = append(results, fmt.Sprintf("UPDATE %s", op.Path))
                }

            case "move":
                newAbs, _ := safePath(effectiveWorkDir(ctx, workDir), op.NewPath)
                os.MkdirAll(filepath.Dir(newAbs), 0o755)
                os.Rename(absPath, newAbs)
                results = append(results, fmt.Sprintf("MOVE %s → %s", op.Path, op.NewPath))

            case "delete":
                os.Remove(absPath)
                results = append(results, fmt.Sprintf("DELETE %s", op.Path))
            }

            if cache := getContextCache(ctx); cache != nil {
                cache.Invalidate(op.Path)
            }
        }

        return strings.Join(results, "\n"), nil
    }
}
```

Register:
```go
r.Register(&Tool{
    Name:        "apply_patch",
    Description: "Apply a unified diff/patch to the project. Supports add, update, move, and delete operations.",
    Execute:     applyPatch(workDir, o.Permissions),
    Safety:      UnsafeParallel,
})
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/tools/patch.go` | ParsePatch + applyPatch tool |
| Create | `shared/tools/patch_test.go` | Tests with various diff formats |
| Modify | `shared/tools/tools.go` | Register `multi_edit` and `apply_patch` tools |
| Modify | agent YAMLs | Add new tools to relevant agents' `tools` lists |

---

## 8. Plugin System

### Problem

BujiCoder can only extend tools via MCP servers. No way to add custom Go tool
logic, hook into agent events, or distribute reusable tool packages.

### Architecture

A lightweight plugin system based on **executable plugins** (not Go's `plugin`
package, which has severe limitations). Plugins are executables that communicate
over stdin/stdout using a simple JSON protocol — same approach as MCP but with
BujiCoder-specific lifecycle hooks.

#### Plugin Manifest

Plugins declare themselves via a `buji-plugin.yaml` in a directory:

```yaml
# ~/.bujicoder/plugins/my-plugin/buji-plugin.yaml
name: my-plugin
version: 1.0.0
description: "Custom tools for my workflow"
command: ./my-plugin-binary    # or "node index.js", "python plugin.py"
args: []

# What this plugin provides
tools:
  - name: my_custom_tool
    description: "Does something custom"

# Lifecycle hooks (optional)
hooks:
  on_step_start: true      # called before each agent step
  on_step_end: true        # called after each agent step
  on_tool_result: true     # called after any tool execution
  on_conversation_start: true
  on_conversation_end: true
```

#### Plugin Manager

```go
// shared/plugin/manager.go

package plugin

type Manager struct {
    plugins  map[string]*Plugin
    hookSubs map[HookType][]*Plugin
    mu       sync.RWMutex
}

type Plugin struct {
    Name    string
    Dir     string
    Config  PluginConfig
    process *exec.Cmd
    stdin   io.WriteCloser
    stdout  *bufio.Reader
    mu      sync.Mutex
}

type HookType string

const (
    HookStepStart          HookType = "on_step_start"
    HookStepEnd            HookType = "on_step_end"
    HookToolResult         HookType = "on_tool_result"
    HookConversationStart  HookType = "on_conversation_start"
    HookConversationEnd    HookType = "on_conversation_end"
)

func NewManager() *Manager {
    return &Manager{
        plugins:  make(map[string]*Plugin),
        hookSubs: make(map[HookType][]*Plugin),
    }
}

// LoadDir scans a directory for plugin manifests.
func (m *Manager) LoadDir(dir string) error {
    entries, _ := os.ReadDir(dir)
    for _, e := range entries {
        if !e.IsDir() { continue }
        manifest := filepath.Join(dir, e.Name(), "buji-plugin.yaml")
        if _, err := os.Stat(manifest); err != nil { continue }

        cfg, err := loadPluginConfig(manifest)
        if err != nil { continue }

        p := &Plugin{Name: cfg.Name, Dir: filepath.Join(dir, e.Name()), Config: cfg}
        m.plugins[cfg.Name] = p

        // Register hook subscriptions
        if cfg.Hooks.OnStepStart { m.hookSubs[HookStepStart] = append(m.hookSubs[HookStepStart], p) }
        if cfg.Hooks.OnStepEnd { m.hookSubs[HookStepEnd] = append(m.hookSubs[HookStepEnd], p) }
        if cfg.Hooks.OnToolResult { m.hookSubs[HookToolResult] = append(m.hookSubs[HookToolResult], p) }
        // ... more hooks
    }
    return nil
}

// Start launches a plugin process.
func (p *Plugin) Start() error {
    cmd := exec.Command(filepath.Join(p.Dir, p.Config.Command), p.Config.Args...)
    cmd.Dir = p.Dir
    p.stdin, _ = cmd.StdinPipe()
    stdout, _ := cmd.StdoutPipe()
    p.stdout = bufio.NewReader(stdout)
    return cmd.Start()
}

// CallTool invokes a tool provided by this plugin.
func (p *Plugin) CallTool(toolName string, args json.RawMessage) (string, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    req := map[string]any{"type": "tool_call", "tool": toolName, "args": args}
    json.NewEncoder(p.stdin).Encode(req)

    var resp struct {
        Result string `json:"result"`
        Error  string `json:"error"`
    }
    json.NewDecoder(p.stdout).Decode(&resp)

    if resp.Error != "" { return "", errors.New(resp.Error) }
    return resp.Result, nil
}

// FireHook notifies all plugins subscribed to a hook type.
func (m *Manager) FireHook(hookType HookType, data map[string]any) {
    m.mu.RLock()
    subs := m.hookSubs[hookType]
    m.mu.RUnlock()

    for _, p := range subs {
        go func(plug *Plugin) {
            plug.mu.Lock()
            defer plug.mu.Unlock()
            req := map[string]any{"type": "hook", "hook": string(hookType), "data": data}
            json.NewEncoder(plug.stdin).Encode(req)
            // Fire-and-forget for hooks (don't block agent)
        }(p)
    }
}

// RegisterTools adds plugin tools to the main tool registry.
func (m *Manager) RegisterTools(registry *tools.Registry) {
    for _, p := range m.plugins {
        for _, toolDef := range p.Config.Tools {
            plug := p // capture
            registry.Register(&tools.Tool{
                Name:        toolDef.Name,
                Description: toolDef.Description,
                Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
                    if plug.process == nil { plug.Start() }
                    return plug.CallTool(toolDef.Name, args)
                },
                Safety: tools.UnsafeParallel, // conservative default
            })
        }
    }
}
```

#### Integration

**File:** `cli/cmd/buji/main.go`:
```go
pluginMgr := plugin.NewManager()
pluginMgr.LoadDir(filepath.Join(configDir, "plugins"))
pluginMgr.RegisterTools(toolRegistry)
```

**File:** `shared/agentruntime/runtime.go` — fire hooks:
```go
// In main loop:
cfg.PluginManager.FireHook(plugin.HookStepStart, map[string]any{"step": step, "agent": cfg.AgentDef.ID})
// ... execute step ...
cfg.PluginManager.FireHook(plugin.HookStepEnd, map[string]any{"step": step, "result": result})
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/plugin/manager.go` | Plugin manager, lifecycle, hooks |
| Create | `shared/plugin/plugin.go` | Plugin process management, JSON protocol |
| Create | `shared/plugin/config.go` | Manifest parsing |
| Create | `shared/plugin/manager_test.go` | Tests with mock plugin |
| Modify | `shared/agentruntime/runtime.go` | Add `PluginManager` to RunConfig, fire hooks |
| Modify | `cli/cmd/buji/main.go` | Load plugins, register tools |
| Modify | `cli/config/config.go` | Plugin directory config |

---

## 9. Additional LLM Providers

### Problem

BujiCoder has 11 providers. Kilocode has 25+. Key missing providers:
Azure OpenAI, Amazon Bedrock, Google Vertex AI, GitHub Copilot, GitLab AI,
Cloudflare Workers AI, Mistral, DeepInfra, Cohere, Perplexity.

### Architecture

BujiCoder's `Provider` interface is clean:

```go
type Provider interface {
    StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error)
    Name() string
}
```

Each provider is a struct implementing this interface. Adding providers is
straightforward — mostly HTTP client boilerplate.

### Priority Providers to Add

#### 1. Azure OpenAI (high demand in enterprise)

```go
// shared/llm/azure.go

type AzureProvider struct {
    endpoint   string // https://{resource}.openai.azure.com
    apiKey     string
    apiVersion string // "2024-12-01-preview"
    deployment string // model deployment name
}

// Key difference from OpenAI:
// URL: {endpoint}/openai/deployments/{deployment}/chat/completions?api-version={version}
// Header: api-key (not Authorization: Bearer)
```

#### 2. Amazon Bedrock (multi-region)

```go
// shared/llm/bedrock.go

type BedrockProvider struct {
    region    string // us-east-1, eu-west-1, etc.
    accessKey string
    secretKey string
}

// Uses AWS Signature V4 for authentication
// Endpoint: https://bedrock-runtime.{region}.amazonaws.com
// Model IDs prefixed by region: us.anthropic.claude-3-5-sonnet, eu.meta.llama3
// Supports converse API (unified) or invoke-model (provider-specific)
```

#### 3. Google Vertex AI

```go
// shared/llm/vertex.go

type VertexProvider struct {
    projectID string
    location  string // us-central1
    token     string // from gcloud auth or service account
}

// URL: https://{location}-aiplatform.googleapis.com/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:streamGenerateContent
```

#### 4. Mistral

```go
// shared/llm/mistral.go

type MistralProvider struct {
    apiKey string
}

// Standard OpenAI-compatible API at https://api.mistral.ai/v1/chat/completions
// Can reuse OpenAI provider with different base URL
```

#### 5. GitHub Copilot

```go
// shared/llm/copilot.go

type CopilotProvider struct {
    token string // GitHub PAT or Copilot token
}

// Two auth modes:
// 1. Standard: GitHub PAT with copilot scope
// 2. Enterprise: OAuth device flow
// Endpoint: https://api.githubcopilot.com/chat/completions
```

### OpenAI-Compatible Shortcut

Many providers (Mistral, DeepInfra, Perplexity, Cloudflare) use the OpenAI
API format. Create a generic adapter:

```go
// shared/llm/openai_compat.go

type OpenAICompatProvider struct {
    name    string
    baseURL string
    apiKey  string
    headers map[string]string
}

func NewOpenAICompat(name, baseURL, apiKey string) *OpenAICompatProvider {
    return &OpenAICompatProvider{name: name, baseURL: baseURL, apiKey: apiKey}
}
```

Then register providers as config:
```yaml
providers:
  mistral:
    type: openai_compat
    base_url: https://api.mistral.ai/v1
    api_key_env: MISTRAL_API_KEY
  deepinfra:
    type: openai_compat
    base_url: https://api.deepinfra.com/v1/openai
    api_key_env: DEEPINFRA_API_KEY
  perplexity:
    type: openai_compat
    base_url: https://api.perplexity.ai
    api_key_env: PERPLEXITY_API_KEY
  cloudflare:
    type: openai_compat
    base_url: https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1
    api_key_env: CLOUDFLARE_API_KEY
```

This approach means most new providers need **zero new code** — just config.

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/llm/openai_compat.go` | Generic OpenAI-compatible provider |
| Create | `shared/llm/azure.go` | Azure OpenAI with SigV4 |
| Create | `shared/llm/bedrock.go` | AWS Bedrock with SigV4 |
| Create | `shared/llm/vertex.go` | Google Vertex AI |
| Create | `shared/llm/copilot.go` | GitHub Copilot |
| Modify | `shared/llm/registry.go` | Register new providers |
| Modify | `cli/config/config.go` | Parse `openai_compat` provider type |
| Modify | `cli/app/setup.go` | Add new providers to setup wizard |

---

## 10. Todo Tracking Tools

### Problem

Agents lose track of multi-step plans. Kilocode has `TodoWrite`/`TodoRead`
tools that let the agent maintain a structured task list during execution.

### Implementation

```go
// shared/tools/todo.go

package tools

type TodoItem struct {
    ID     string `json:"id"`
    Task   string `json:"task"`
    Status string `json:"status"` // "pending", "in_progress", "done", "blocked"
    Note   string `json:"note,omitempty"`
}

// In-memory per-conversation todo list (not persisted across runs)
type TodoList struct {
    mu    sync.Mutex
    items []TodoItem
}

func todoWrite() func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        var params struct {
            Items []TodoItem `json:"items"`
        }
        json.Unmarshal(args, &params)

        list := getTodoList(ctx)
        list.mu.Lock()
        defer list.mu.Unlock()
        list.items = params.Items

        // Format for display
        var sb strings.Builder
        for _, item := range list.items {
            icon := map[string]string{"pending": "[ ]", "in_progress": "[~]", "done": "[x]", "blocked": "[!]"}[item.Status]
            sb.WriteString(fmt.Sprintf("%s %s\n", icon, item.Task))
        }
        return sb.String(), nil
    }
}

func todoRead() func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        list := getTodoList(ctx)
        list.mu.Lock()
        defer list.mu.Unlock()

        if len(list.items) == 0 {
            return "No todos set.", nil
        }

        data, _ := json.MarshalIndent(list.items, "", "  ")
        return string(data), nil
    }
}
```

Register:
```go
r.Register(&Tool{Name: "todo_write", Description: "Set or update the task list", Execute: todoWrite(), Safety: SafeParallel})
r.Register(&Tool{Name: "todo_read", Description: "Read the current task list", Execute: todoRead(), Safety: SafeParallel})
```

Add context key and injection in dispatch, same pattern as other context values.

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/tools/todo.go` | TodoList, todoWrite, todoRead |
| Modify | `shared/tools/tools.go` | Register todo tools, add context key |
| Modify | `shared/agentruntime/dispatch.go` | Inject todo list into context |
| Modify | agent YAMLs | Add todo tools to base agent |

---

## 11. Skill System

### Problem

No way to package reusable prompt+tool combinations as discoverable skills.
Kilocode uses `SKILL.md` files with frontmatter metadata.

### Implementation

```go
// shared/skill/skill.go

package skill

type Skill struct {
    Name        string   `yaml:"name"`
    Description string   `yaml:"description"`
    Tags        []string `yaml:"tags"`
    Content     string   // markdown body (instructions)
    SourcePath  string   // where it was found
}

type Registry struct {
    skills map[string]*Skill
}

// Discover scans directories for SKILL.md files.
func (r *Registry) Discover(dirs ...string) error {
    for _, dir := range dirs {
        filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
            if d.Name() == "SKILL.md" || strings.HasSuffix(d.Name(), ".skill.md") {
                skill, err := parseSkillFile(path)
                if err != nil { return nil }
                r.skills[skill.Name] = skill
            }
            return nil
        })
    }
    return nil
}

// parseSkillFile reads a SKILL.md with YAML frontmatter.
func parseSkillFile(path string) (*Skill, error) {
    data, _ := os.ReadFile(path)
    content := string(data)

    // Parse YAML frontmatter between --- markers
    if !strings.HasPrefix(content, "---") {
        return nil, fmt.Errorf("no frontmatter")
    }
    parts := strings.SplitN(content[3:], "---", 2)
    if len(parts) < 2 { return nil, fmt.Errorf("invalid frontmatter") }

    var skill Skill
    yaml.Unmarshal([]byte(parts[0]), &skill)
    skill.Content = strings.TrimSpace(parts[1])
    skill.SourcePath = path
    return &skill, nil
}
```

#### Skill Tool

```go
func skillTool(registry *skill.Registry) func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        var params struct {
            Name string `json:"name"`
        }
        json.Unmarshal(args, &params)

        s, ok := registry.Get(params.Name)
        if !ok {
            // List available skills
            names := registry.List()
            return fmt.Sprintf("Skill %q not found. Available: %s", params.Name, strings.Join(names, ", ")), nil
        }

        return fmt.Sprintf("## Skill: %s\n\n%s", s.Name, s.Content), nil
    }
}
```

#### Discovery Paths

```
1. .bujicoder/skills/         (project-local)
2. ~/.bujicoder/skills/       (global user skills)
3. Agents directory            (bundled skills)
```

#### Slash Command: `/skill`

```go
case "/skill":
    skillName := strings.TrimPrefix(input, "/skill ")
    if skillName == "" {
        // list all skills
    } else {
        // inject skill content into next agent prompt
    }
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/skill/skill.go` | Skill type, Registry, Discover, Parse |
| Create | `shared/skill/skill_test.go` | Tests |
| Modify | `shared/tools/tools.go` | Register `skill` tool |
| Modify | `cli/app/model.go` | `/skill` slash command |
| Modify | `cli/cmd/buji/main.go` | Initialize skill registry |

---

## 12. Worktree Isolation

### Problem

When experimenting with risky changes, there's no way to isolate work from
the main branch. Kilocode creates git worktrees for sandboxed editing.

### Implementation

```go
// shared/worktree/worktree.go

package worktree

type Manager struct {
    projectRoot string
    worktrees   map[string]*Worktree
}

type Worktree struct {
    Name       string
    Path       string
    BranchName string
    CreatedAt  time.Time
}

// Create makes a new worktree with a random name.
func (m *Manager) Create(baseBranch string) (*Worktree, error) {
    name := randomName() // "brave-cabin", "swift-oak"
    branchName := "buji-wt-" + name
    wtPath := filepath.Join(m.projectRoot, ".bujicoder", "worktrees", name)

    // git worktree add -b {branch} {path} {base}
    cmd := exec.Command("git", "worktree", "add", "-b", branchName, wtPath, baseBranch)
    cmd.Dir = m.projectRoot
    if err := cmd.Run(); err != nil { return nil, err }

    // Initialize submodules if present
    subCmd := exec.Command("git", "submodule", "update", "--init", "--recursive")
    subCmd.Dir = wtPath
    subCmd.Run() // best-effort

    wt := &Worktree{Name: name, Path: wtPath, BranchName: branchName, CreatedAt: time.Now()}
    m.worktrees[name] = wt
    return wt, nil
}

// Remove cleans up a worktree.
func (m *Manager) Remove(name string) error {
    wt, ok := m.worktrees[name]
    if !ok { return fmt.Errorf("worktree %q not found", name) }

    exec.Command("git", "worktree", "remove", wt.Path).Run()
    exec.Command("git", "branch", "-d", wt.BranchName).Run()
    delete(m.worktrees, name)
    return nil
}

// Reset restores a worktree to its base branch state.
func (m *Manager) Reset(name string) error {
    wt := m.worktrees[name]
    cmd := exec.Command("git", "checkout", ".", "--")
    cmd.Dir = wt.Path
    return cmd.Run()
}

// List returns all active worktrees.
func (m *Manager) List() []*Worktree {
    var result []*Worktree
    for _, wt := range m.worktrees {
        result = append(result, wt)
    }
    return result
}
```

#### Random Name Generator

```go
var adjectives = []string{"brave", "swift", "calm", "bold", "keen", "warm", "wise", "fair"}
var nouns = []string{"cabin", "river", "stone", "maple", "eagle", "flame", "ridge", "brook"}

func randomName() string {
    return adjectives[rand.Intn(len(adjectives))] + "-" + nouns[rand.Intn(len(nouns))]
}
```

#### Integration

- `/worktree create` — creates worktree, switches agent's `ProjectRoot`
- `/worktree list` — shows active worktrees
- `/worktree remove <name>` — cleans up
- Agent's `ProjectRoot` in `RunConfig` points to worktree path

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/worktree/worktree.go` | Manager, Create/Remove/Reset/List |
| Create | `shared/worktree/names.go` | Random name generator |
| Modify | `cli/app/model.go` | `/worktree` commands |
| Modify | `shared/agentruntime/runtime.go` | Support dynamic ProjectRoot switching |

---

## 13. tree-sitter Code Intelligence

### Problem

BujiCoder treats code as plain text. tree-sitter parsing enables:
- Symbol extraction (functions, classes, variables)
- Scope-aware search ("find all methods of class X")
- Smarter context building (send relevant code, not entire files)

### Implementation

Use `smacker/go-tree-sitter` — Go bindings for tree-sitter.

```go
// shared/treesitter/parser.go

package treesitter

import (
    sitter "github.com/smacker/go-tree-sitter"
    "github.com/smacker/go-tree-sitter/golang"
    "github.com/smacker/go-tree-sitter/javascript"
    "github.com/smacker/go-tree-sitter/python"
    "github.com/smacker/go-tree-sitter/typescript/typescript"
    "github.com/smacker/go-tree-sitter/rust"
)

type Symbol struct {
    Name      string `json:"name"`
    Kind      string `json:"kind"`      // "function", "class", "method", "variable", "type"
    StartLine int    `json:"start_line"`
    EndLine   int    `json:"end_line"`
    Signature string `json:"signature"` // first line of the symbol
}

type Parser struct {
    parsers map[string]*sitter.Parser // keyed by extension
}

func NewParser() *Parser {
    p := &Parser{parsers: make(map[string]*sitter.Parser)}

    // Register languages
    langs := map[string]*sitter.Language{
        ".go":   golang.GetLanguage(),
        ".js":   javascript.GetLanguage(),
        ".ts":   typescript.GetLanguage(),
        ".py":   python.GetLanguage(),
        ".rs":   rust.GetLanguage(),
    }

    for ext, lang := range langs {
        parser := sitter.NewParser()
        parser.SetLanguage(lang)
        p.parsers[ext] = parser
    }
    return p
}

// ExtractSymbols returns all top-level symbols from a file.
func (p *Parser) ExtractSymbols(filePath string, content []byte) ([]Symbol, error) {
    ext := filepath.Ext(filePath)
    parser, ok := p.parsers[ext]
    if !ok { return nil, nil } // unsupported language, skip

    tree, err := parser.ParseCtx(context.Background(), nil, content)
    if err != nil { return nil, err }
    defer tree.Close()

    root := tree.RootNode()
    return extractSymbolsFromNode(root, content, ext), nil
}

func extractSymbolsFromNode(node *sitter.Node, content []byte, ext string) []Symbol {
    var symbols []Symbol
    for i := 0; i < int(node.NamedChildCount()); i++ {
        child := node.NamedChild(i)
        nodeType := child.Type()

        var sym *Symbol
        switch ext {
        case ".go":
            sym = extractGoSymbol(child, content, nodeType)
        case ".py":
            sym = extractPythonSymbol(child, content, nodeType)
        case ".ts", ".js":
            sym = extractTSSymbol(child, content, nodeType)
        case ".rs":
            sym = extractRustSymbol(child, content, nodeType)
        }

        if sym != nil {
            symbols = append(symbols, *sym)
        }
    }
    return symbols
}
```

#### New Tool: `symbols`

```go
func symbolsTool(parser *treesitter.Parser) func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        var params struct {
            Path string `json:"path"`
        }
        json.Unmarshal(args, &params)

        content, _ := os.ReadFile(params.Path)
        symbols, err := parser.ExtractSymbols(params.Path, content)
        if err != nil { return "", err }

        var sb strings.Builder
        for _, s := range symbols {
            sb.WriteString(fmt.Sprintf("%s %s (L%d-%d): %s\n", s.Kind, s.Name, s.StartLine, s.EndLine, s.Signature))
        }
        return sb.String(), nil
    }
}
```

#### Integration with Dynamic Context

Instead of sending the full file tree (up to 200 files), send a **symbol index**
of the most relevant files. This gives the LLM structural understanding without
consuming context tokens on file contents.

```go
// In buildDynamicContext():
symbolIndex := parser.IndexProject(projectRoot, topFiles)
// Output:
// main.go: func main(), func setupRouter(), func handleRequest()
// models/user.go: type User struct, func (u *User) Save(), func FindUserByID()
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/treesitter/parser.go` | Multi-language parser, symbol extraction |
| Create | `shared/treesitter/go.go` | Go-specific symbol extraction |
| Create | `shared/treesitter/python.go` | Python-specific |
| Create | `shared/treesitter/typescript.go` | TS/JS-specific |
| Create | `shared/treesitter/rust.go` | Rust-specific |
| Create | `shared/treesitter/index.go` | Project-wide symbol indexing |
| Modify | `shared/tools/tools.go` | Register `symbols` tool |
| Modify | `shared/agentruntime/context.go` | Use symbol index in dynamic context |
| Modify | `go.mod` | Add `github.com/smacker/go-tree-sitter` |

---

## 14. Per-Model Prompt Variants

### Problem

Different LLM families respond better to different prompt styles. Claude
prefers XML tags, GPT prefers markdown headers, Gemini has its own quirks.
Currently BujiCoder uses a single prompt per agent for all models.

### Implementation

```go
// shared/agent/prompt.go

package agent

type PromptVariant string

const (
    PromptAnthropic PromptVariant = "anthropic" // XML tags, thinking blocks
    PromptOpenAI    PromptVariant = "openai"    // Markdown headers, concise
    PromptGemini    PromptVariant = "gemini"    // Structured, explicit
    PromptGeneric   PromptVariant = "generic"   // Fallback for unknowns
)

// DetectVariant determines the best prompt style for a model ID.
func DetectVariant(modelID string) PromptVariant {
    lower := strings.ToLower(modelID)
    switch {
    case strings.Contains(lower, "claude") || strings.Contains(lower, "anthropic"):
        return PromptAnthropic
    case strings.Contains(lower, "gpt") || strings.Contains(lower, "o1") || strings.Contains(lower, "o3"):
        return PromptOpenAI
    case strings.Contains(lower, "gemini"):
        return PromptGemini
    default:
        return PromptGeneric
    }
}

// WrapSystemPrompt adapts the system prompt to the model's preferred format.
func WrapSystemPrompt(base string, variant PromptVariant) string {
    switch variant {
    case PromptAnthropic:
        // Wrap sections in XML tags
        return wrapAnthropicStyle(base)
    case PromptOpenAI:
        // Use ## headers, be concise
        return wrapOpenAIStyle(base)
    case PromptGemini:
        // Explicit instruction blocks
        return wrapGeminiStyle(base)
    default:
        return base
    }
}

func wrapAnthropicStyle(base string) string {
    // Convert markdown headers to XML sections
    // ## Tools → <tools>...</tools>
    // ## Rules → <rules>...</rules>
    // Add thinking encouragement
    return base // + XML wrapper logic
}

func wrapOpenAIStyle(base string) string {
    // Keep markdown but make it terser
    // Remove verbose explanations
    // Add "Be concise" directive
    return base
}
```

#### Integration

**File:** `shared/agentruntime/step.go` — in request building:

```go
variant := agent.DetectVariant(cfg.AgentDef.Model)
systemPrompt := agent.WrapSystemPrompt(cfg.AgentDef.SystemPrompt, variant)
```

No changes needed to agent YAML files — the wrapping is automatic.

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/agent/prompt.go` | Variant detection + prompt wrapping |
| Create | `shared/agent/prompt_test.go` | Tests per variant |
| Modify | `shared/agentruntime/step.go` | Apply prompt variant before completion |

---

## 15. Structured Output Enforcement

### Problem

When agents need JSON output (judge decisions, structured plans), LLMs sometimes
return markdown or mixed content. No way to force schema-compliant output.

### Implementation

```go
// shared/tools/structured.go

func structuredOutput() func(ctx context.Context, args json.RawMessage) (string, error) {
    return func(ctx context.Context, args json.RawMessage) (string, error) {
        var params struct {
            Schema map[string]any `json:"schema"` // JSON Schema
            Prompt string         `json:"prompt"` // What to generate
        }
        json.Unmarshal(args, &params)

        // The LLM calling this tool must return JSON matching the schema.
        // We validate the output here.
        // This tool acts as a contract: the agent writes the JSON as the
        // tool result, and the calling agent receives validated data.

        // For providers that support native structured output (OpenAI, Gemini),
        // pass the schema in the API call.
        // For others, include the schema in the prompt and validate post-hoc.

        schemaJSON, _ := json.MarshalIndent(params.Schema, "", "  ")
        return fmt.Sprintf("Output must conform to this JSON Schema:\n%s\n\nGenerate: %s",
            string(schemaJSON), params.Prompt), nil
    }
}
```

For providers with native support, extend `CompletionRequest`:

```go
type CompletionRequest struct {
    // ... existing fields ...
    ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type ResponseFormat struct {
    Type   string         `json:"type"`   // "json_schema"
    Schema map[string]any `json:"schema"` // JSON Schema object
}
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/tools/structured.go` | Structured output tool |
| Modify | `shared/llm/provider.go` | Add ResponseFormat to CompletionRequest |
| Modify | `shared/llm/openai.go` | Pass response_format in API call |
| Modify | `shared/llm/anthropic.go` | JSON mode via tool_choice |
| Modify | `shared/tools/tools.go` | Register tool |

---

## 16. Smart Context Assembly

### Problem

BujiCoder sends the top 200 files as a flat list. This wastes context tokens
on irrelevant files. Kilocode doesn't have this either — this is a
**superiority feature**.

### Concept: Relevance-Ranked Context

Instead of dumping the file tree, score files by relevance to the current task
and send only the most relevant ones.

```go
// shared/context/ranker.go

package context

type FileRelevance struct {
    Path  string
    Score float64
    Reason string // "recently_modified", "name_match", "import_graph", "git_changed"
}

// RankFiles scores files by relevance to the user's query.
func RankFiles(projectRoot, query string, symbols []treesitter.Symbol) []FileRelevance {
    var scored []FileRelevance

    filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, err error) error {
        if shouldSkip(path) { return filepath.SkipDir }
        if d.IsDir() { return nil }

        relPath, _ := filepath.Rel(projectRoot, path)
        score := 0.0
        var reasons []string

        // Factor 1: File name matches query keywords
        nameScore := keywordMatch(relPath, query)
        if nameScore > 0 {
            score += nameScore * 3.0
            reasons = append(reasons, "name_match")
        }

        // Factor 2: Recently modified (git)
        if isRecentlyModified(relPath) {
            score += 2.0
            reasons = append(reasons, "recently_modified")
        }

        // Factor 3: In git diff (currently changed)
        if isInGitDiff(relPath) {
            score += 5.0
            reasons = append(reasons, "git_changed")
        }

        // Factor 4: Contains symbols referenced in query
        for _, sym := range symbols {
            if strings.Contains(strings.ToLower(query), strings.ToLower(sym.Name)) {
                score += 4.0
                reasons = append(reasons, "symbol_match:"+sym.Name)
                break
            }
        }

        // Factor 5: Import proximity (files imported by changed files)
        if isImportedByChangedFile(relPath) {
            score += 1.5
            reasons = append(reasons, "import_graph")
        }

        if score > 0 {
            scored = append(scored, FileRelevance{
                Path:   relPath,
                Score:  score,
                Reason: strings.Join(reasons, ", "),
            })
        }
        return nil
    })

    // Sort by score descending, take top 50
    sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
    if len(scored) > 50 { scored = scored[:50] }
    return scored
}
```

#### Integration

Replace the flat file tree in `buildDynamicContext()`:

```go
// Before: list top 200 files
// After: rank and include top 50 with relevance scores

rankedFiles := context.RankFiles(projectRoot, userMessage, symbols)
fileTreeSection := formatRankedFiles(rankedFiles)
// Output:
// ## Relevant Files (ranked)
// [5.0] src/auth/login.go (git_changed, symbol_match:Login)
// [4.0] src/auth/middleware.go (symbol_match:AuthMiddleware)
// [3.0] src/models/user.go (name_match)
```

This gives the LLM a focused view of what matters, not a dump of everything.

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/context/ranker.go` | File relevance scoring |
| Create | `shared/context/keywords.go` | Keyword extraction from queries |
| Create | `shared/context/imports.go` | Import graph analysis (per language) |
| Modify | `shared/agentruntime/context.go` | Use ranker instead of flat file list |

---

## 17. Agent Workflow Composer

### Problem

Kilocode has fixed agent modes. BujiCoder already has superior YAML agent
definitions. Push this further by letting users **compose multi-agent workflows**
as YAML pipelines — something neither tool has.

### Concept: Workflow YAML

```yaml
# workflows/refactor.yaml
id: refactor_workflow
display_name: "Safe Refactor Pipeline"
description: "Analyze, plan, implement, review, and commit a refactor"

steps:
  - agent: researcher
    task: "Analyze the codebase area related to: {{user_task}}"
    output_var: analysis

  - agent: planner
    task: |
      Based on this analysis, create a step-by-step refactoring plan:
      {{analysis}}
    output_var: plan
    require_approval: true  # pause for user confirmation

  - agent: editor
    task: |
      Execute this refactoring plan:
      {{plan}}
    output_var: changes

  - parallel:  # run these concurrently
    - agent: reviewer
      task: "Review these changes for correctness:\n{{changes}}"
      output_var: review
    - agent: ui_reviewer
      task: "Review these changes for UI impact:\n{{changes}}"
      output_var: ui_review

  - agent: editor
    task: |
      Address this feedback:
      Code review: {{review}}
      UI review: {{ui_review}}
    condition: "{{review}} contains 'NEEDS_CHANGES' OR {{ui_review}} contains 'NEEDS_CHANGES'"

  - agent: git_committer
    task: "Commit the changes with a descriptive message"
```

#### Workflow Engine

```go
// shared/workflow/engine.go

package workflow

type Workflow struct {
    ID          string `yaml:"id"`
    DisplayName string `yaml:"display_name"`
    Description string `yaml:"description"`
    Steps       []Step `yaml:"steps"`
}

type Step struct {
    Agent           string `yaml:"agent"`
    Task            string `yaml:"task"`
    OutputVar       string `yaml:"output_var"`
    RequireApproval bool   `yaml:"require_approval"`
    Condition       string `yaml:"condition"`
    Parallel        []Step `yaml:"parallel"` // for concurrent steps
}

type Engine struct {
    runtime     *agentruntime.Runtime
    agentReg    *agent.Registry
    variables   map[string]string
}

func (e *Engine) Execute(ctx context.Context, wf *Workflow, userTask string, cfg agentruntime.RunConfig) error {
    e.variables["user_task"] = userTask

    for _, step := range wf.Steps {
        // Check condition
        if step.Condition != "" && !e.evaluateCondition(step.Condition) {
            continue
        }

        if len(step.Parallel) > 0 {
            // Execute parallel steps concurrently
            e.executeParallel(ctx, step.Parallel, cfg)
        } else {
            // Execute single step
            task := e.interpolate(step.Task)

            if step.RequireApproval {
                // Pause and show plan to user
                approved := cfg.OnApproval(task)
                if !approved { return fmt.Errorf("user rejected step") }
            }

            result, err := e.runtime.Run(ctx, agentruntime.RunConfig{
                AgentDef:    e.agentReg.MustGet(step.Agent),
                UserMessage: task,
                ProjectRoot: cfg.ProjectRoot,
                // ... propagate other config ...
            })
            if err != nil { return err }

            if step.OutputVar != "" {
                e.variables[step.OutputVar] = result.FinalText
            }
        }
    }
    return nil
}

func (e *Engine) interpolate(template string) string {
    result := template
    for k, v := range e.variables {
        result = strings.ReplaceAll(result, "{{"+k+"}}", v)
    }
    return result
}
```

#### Slash Command: `/workflow`

```go
case "/workflow":
    workflowName := strings.TrimPrefix(input, "/workflow ")
    wf, _ := workflowRegistry.Get(workflowName)
    engine := workflow.NewEngine(runtime, agentRegistry)
    engine.Execute(ctx, wf, userTask, runConfig)
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/workflow/engine.go` | Workflow engine, step execution, variable interpolation |
| Create | `shared/workflow/workflow.go` | Workflow + Step types, YAML loading |
| Create | `shared/workflow/condition.go` | Simple condition evaluator |
| Create | `workflows/refactor.yaml` | Example workflow |
| Create | `workflows/review.yaml` | Example workflow |
| Modify | `cli/app/model.go` | `/workflow` command |

---

## 18. Live File Watcher & Auto-Context

### Problem

Neither BujiCoder nor Kilocode watches for external file changes during a
session. If the user edits a file in their IDE while chatting, the agent
doesn't know. This is a **superiority feature**.

### Implementation

Use `fsnotify` to watch the project root for changes:

```go
// shared/filewatcher/watcher.go

package filewatcher

import "github.com/fsnotify/fsnotify"

type Watcher struct {
    fsWatcher  *fsnotify.Watcher
    changes    []FileChange
    mu         sync.Mutex
    projectRoot string
}

type FileChange struct {
    Path      string
    Operation string // "modified", "created", "deleted"
    Timestamp time.Time
}

func New(projectRoot string) (*Watcher, error) {
    fsw, _ := fsnotify.NewWatcher()
    w := &Watcher{fsWatcher: fsw, projectRoot: projectRoot}

    // Watch project root recursively (skip .git, node_modules, etc.)
    filepath.WalkDir(projectRoot, func(path string, d fs.DirEntry, err error) error {
        if shouldSkipDir(d.Name()) { return filepath.SkipDir }
        if d.IsDir() { fsw.Add(path) }
        return nil
    })

    go w.listen()
    return w, nil
}

func (w *Watcher) listen() {
    for {
        select {
        case event := <-w.fsWatcher.Events:
            if isIgnored(event.Name) { continue }

            w.mu.Lock()
            op := "modified"
            if event.Op&fsnotify.Create != 0 { op = "created" }
            if event.Op&fsnotify.Remove != 0 { op = "deleted" }

            w.changes = append(w.changes, FileChange{
                Path:      event.Name,
                Operation: op,
                Timestamp: time.Now(),
            })
            w.mu.Unlock()

        case <-w.fsWatcher.Errors:
            // log and continue
        }
    }
}

// FlushChanges returns accumulated changes since last flush and clears them.
func (w *Watcher) FlushChanges() []FileChange {
    w.mu.Lock()
    defer w.mu.Unlock()
    changes := w.changes
    w.changes = nil
    return changes
}
```

#### Integration into Agent Runtime

At the start of each step, check for external changes and inject them as
context:

```go
// shared/agentruntime/step.go — in executeStep()

if cfg.FileWatcher != nil {
    changes := cfg.FileWatcher.FlushChanges()
    if len(changes) > 0 {
        notice := "## External File Changes Detected\n"
        for _, c := range changes {
            notice += fmt.Sprintf("- %s: %s\n", c.Operation, c.Path)
        }
        // Prepend as a system message so the LLM is aware
        state.prependNotice(notice)
    }
}
```

#### Files to Create/Modify

| Action | File | Description |
|--------|------|-------------|
| Create | `shared/filewatcher/watcher.go` | fsnotify-based file watcher |
| Modify | `shared/agentruntime/runtime.go` | Add `FileWatcher` to RunConfig |
| Modify | `shared/agentruntime/step.go` | Check for external changes each step |
| Modify | `cli/cmd/buji/main.go` | Initialize watcher |
| Modify | `go.mod` | Add `github.com/fsnotify/fsnotify` |

---

## 19. Dependency & Ordering

### Full Feature Dependency Graph

```
Phase A (v3.0) — Core Quality
═══════════════════════════════════════════════════════════

  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
  │ 1. Fuzzy     │   │ 3. bbolt+    │   │ 5. Batch     │
  │    Edit      │   │    Bleve     │   │    Tools     │
  └──────┬───────┘   └──────┬───────┘   └──────────────┘
         │                  │
         ▼                  ▼            ┌──────────────┐
  ┌──────────────┐   ┌──────────────┐   │ 5a. Error    │
  │ 4. LSP       │   │ 2. Git       │   │     Logging  │
  │    Diagnostics│   │    Snapshot  │   └──────────────┘
  └──────────────┘   └──────────────┘   (no deps, can start anytime)

Phase B (v3.5) — Full Parity
═══════════════════════════════════════════════════════════

  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
  │ 6. MCP       │   │ 7. MultiEdit │   │ 9. LLM       │
  │    Transports│   │    ApplyPatch│   │    Providers  │
  └──────────────┘   └──────┬───────┘   └──────────────┘
                            │ (uses editmatch from #1)
  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
  │ 8. Plugin    │   │ 10. Todo     │   │ 11. Skill    │
  │    System    │   │     Tools    │   │     System   │
  └──────────────┘   └──────────────┘   └──────────────┘

Phase C (v3.9) — Superiority
═══════════════════════════════════════════════════════════

  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
  │ 12. Worktree │   │ 13. tree-    │   │ 14. Prompt   │
  │    Isolation │   │     sitter   │   │     Variants │
  └──────────────┘   └──────┬───────┘   └──────────────┘
                            │
                            ▼
                     ┌──────────────┐
                     │ 16. Smart    │  ← uses tree-sitter symbols
                     │    Context   │
                     └──────────────┘

  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
  │ 15. Struct.  │   │ 17. Workflow │   │ 18. File     │
  │    Output    │   │    Composer  │   │    Watcher   │
  └──────────────┘   └──────────────┘   └──────────────┘
```

### Implementation Schedule

| Phase | # | Feature | Effort | Cumulative Parity |
|-------|---|---------|--------|-------------------|
| **A** | 5a | Error Logging System | 2-3 days | 59% |
| **A** | 1 | Fuzzy Edit Matching | 3-4 days | 63% |
| **A** | 5 | Batch Tool Execution | 1-2 days | 66% |
| **A** | 3 | Embedded Storage (bbolt+Bleve) | 4-5 days | 71% |
| **A** | 2 | Git Snapshot & Revert | 4-5 days | 77% |
| **A** | 4 | LSP Diagnostics | 3-4 days | 81% |
| | | **Phase A Total** | **~19 days** | |
| **B** | 7 | MultiEdit + ApplyPatch | 3 days | 83% |
| **B** | 10 | Todo Tracking Tools | 1 day | 84% |
| **B** | 9 | Additional LLM Providers | 4 days | 88% |
| **B** | 6 | MCP OAuth + Transports | 4 days | 91% |
| **B** | 11 | Skill System | 2 days | 93% |
| **B** | 8 | Plugin System | 4 days | 96% |
| | | **Phase B Total** | **~18 days** | |
| **C** | 14 | Per-Model Prompt Variants | 2 days | 97% |
| **C** | 15 | Structured Output | 2 days | 98% |
| **C** | 12 | Worktree Isolation | 2 days | 99% |
| **C** | 13 | tree-sitter Intelligence | 4 days | 100% |
| **C** | 16 | Smart Context Assembly | 3 days | **>100%** |
| **C** | 17 | Agent Workflow Composer | 4 days | **>100%** |
| **C** | 18 | Live File Watcher | 2 days | **>100%** |
| | | **Phase C Total** | **~19 days** | |
| | | **GRAND TOTAL** | **~56 days** | |

### Total New Code Estimate

| Package | New Files | Lines (est.) |
|---------|-----------|-------------|
| `shared/tools/editmatch/` | 9 | ~800 |
| `shared/logging/` | 2 | ~400 |
| `shared/snapshot/` | 2 | ~400 |
| `shared/store/` | 3 | ~500 |
| `shared/lsp/` | 5 | ~700 |
| `shared/mcp/` (extensions) | 5 | ~600 |
| `shared/plugin/` | 4 | ~500 |
| `shared/llm/` (new providers) | 5 | ~400 |
| `shared/tools/` (new tools) | 4 | ~400 |
| `shared/skill/` | 2 | ~200 |
| `shared/worktree/` | 2 | ~250 |
| `shared/treesitter/` | 6 | ~700 |
| `shared/agent/` (prompts) | 2 | ~300 |
| `shared/context/` | 3 | ~400 |
| `shared/workflow/` | 3 | ~500 |
| `shared/filewatcher/` | 1 | ~150 |
| Modifications to existing | 15 | ~500 |
| Tests | 15 | ~1500 |
| Workflows/examples | 3 | ~100 |
| **Total** | **~91** | **~8800** |

### Go Module Dependencies to Add

| Dependency | Purpose | Size Impact |
|-----------|---------|-------------|
| `go.etcd.io/bbolt` | Embedded key-value store | ~200KB |
| `github.com/blevesearch/bleve/v2` | Full-text search | ~4MB |
| `gopkg.in/natefinished/lumberjack.v2` | Log rotation | Tiny |
| `github.com/smacker/go-tree-sitter` | Code parsing | ~3MB (includes grammars) |
| `github.com/fsnotify/fsnotify` | File system watching | Tiny |
| None others | All features use stdlib + existing deps | — |

### Risk Mitigation

| Risk | Mitigation |
|------|-----------|
| Fuzzy matching false positives | Single-match requirement per strategy |
| bbolt data loss | Auto-backup before migration; bbolt is crash-safe (single-writer, mmap) |
| Log file disk usage | lumberjack rotation: 5 files × 10MB max = 50MB ceiling |
| LSP server crashes | 3-second timeouts, nil-safe, graceful degradation |
| Snapshot disk bloat | 7-day auto-cleanup, git object dedup |
| Batch race conditions | Strict safe/unsafe classification |
| MCP OAuth complexity | Local callback server, token caching |
| Plugin security | Plugins run as separate processes (sandboxed) |
| tree-sitter binary size | Only include grammars for top 5 languages |
| File watcher performance | Skip `.git`, `node_modules`; debounce events |

### BujiCoder Competitive Position After v3.9

| Capability | Kilocode | BujiCoder v3.9 |
|------------|:--------:|:--------------:|
| LLM Providers | 25+ | 25+ |
| Built-in Tools | 25 | 28 |
| Edit Robustness | 9 strategies | 7 strategies |
| MCP Transports | Stdio + HTTP + SSE + OAuth | Stdio + HTTP + SSE + OAuth |
| Git Integration | Snapshots, revert, worktrees | Snapshots, revert, worktrees |
| Storage | SQLite | bbolt + Bleve |
| Agents | 10 | 12 |
| Plugin System | npm + hooks | Executable + hooks |
| Skill System | SKILL.md | SKILL.md |
| Distribution | 5 surfaces | 1 (CLI) |
| Local Models | No | Ollama |
| Parallel Evolution | No | **Yes** |
| Extended Reasoning | No | **Yes (think_deeply)** |
| Workflow Composer | No | **Yes** |
| Smart Context Ranking | No | **Yes** |
| Live File Watcher | No | **Yes** |
| tree-sitter Intelligence | Yes | Yes |
| YAML Agent Definitions | No | **Yes** |
| Cost Mode Switching | No | **Yes** |
| Single Binary | No (Bun runtime) | **Yes (Go)** |

**Superiority features** (things BujiCoder will have that Kilocode does not):
1. Parallel Evolution (implementor → judge → apply)
2. Extended Reasoning (think_deeply)
3. Agent Workflow Composer (YAML pipelines)
4. Smart Context Assembly (relevance-ranked file context)
5. Live File Watcher (detect external IDE changes)
6. Ollama / fully local inference
7. YAML agent definitions (zero-code agent creation)
8. Cost mode switching (normal/heavy/max)
9. Single Go binary (zero runtime deps)

**Remaining Kilocode advantage** (not addressed):
- Multi-surface delivery (VS Code, Zed, Web, Desktop) — this is a v4 architectural project
