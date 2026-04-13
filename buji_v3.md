# BujiCoder v3 — Remaining Feature Roadmap

> Trimmed from original analysis. Features already shipped are listed below.
> Only pending/partial features remain in this document.

## Completed Features (Shipped)

| # | Feature | Package | Status |
|---|---------|---------|--------|
| 0 | Database Choice (bbolt+Bleve) | `shared/store/` | Shipped |
| 1 | Fuzzy Edit Matching (7-Strategy) | `shared/tools/editmatch/` | Shipped |
| 2 | Git Snapshot & Revert | `shared/snapshot/` | Shipped |
| 3 | Embedded Storage (bbolt+Bleve) | `shared/store/` | Shipped |
| 4 | LSP Diagnostics After Edits | `shared/lsp/` | Shipped |
| 5a | Error Logging System | `shared/logging/` | Shipped |
| 8 | Plugin System | `shared/plugins/` | Shipped |
| 11 | Skill System | `shared/skills/` | Shipped |
| 12 | Worktree Isolation | `shared/worktree/` | Shipped |
| — | Retry with Backoff | `shared/llm/retry.go` | Shipped |
| — | Hooks System | `shared/hooks/` | Shipped |
| — | Memory Management | `shared/memory/` | Shipped |
| — | Permissions | `shared/permissions/` | Shipped |
| — | Feature Flags | `shared/features/` | Shipped |
| — | Settings Hierarchy | `shared/settings/` | Shipped |
| — | Cron Scheduler | `shared/cron/` | Shipped |
| — | Non-interactive CLI | `cli/app/noninteractive.go` | Shipped |
| — | Agent Orchestrator | `cli/app/orchestrator.go` | Shipped |

---

## Remaining Features

## Table of Contents

1. [Code Quality Improvements](#code-quality-improvements)
2. [Batch Parallel Tool Execution](#5-batch-parallel-tool-execution)
3. [MCP OAuth + HTTP/SSE Transports](#6-mcp-oauth--httpsse-transports)
4. [MultiEdit & ApplyPatch Tools](#7-multiedit--applypatch-tools)
5. [Additional LLM Providers](#9-additional-llm-providers)
6. [Todo Tracking Tools](#10-todo-tracking-tools)
7. [tree-sitter Code Intelligence](#13-tree-sitter-code-intelligence)
8. [Per-Model Prompt Variants](#14-per-model-prompt-variants)
9. [Structured Output Enforcement](#15-structured-output-enforcement)
10. [Smart Context Assembly](#16-smart-context-assembly)
11. [Agent Workflow Composer](#17-agent-workflow-composer)
12. [Live File Watcher & Auto-Context](#18-live-file-watcher--auto-context)

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
