package agentruntime

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Shared memory limits.
const (
	MaxSharedMemoryEntries  = 200   // Maximum number of entries in shared memory.
	MaxSharedMemoryValueLen = 10000 // Maximum value length per entry (bytes).
	MaxSummaryLen           = 8000  // Maximum total summary length injected into prompts.
)

// SharedMemory provides a namespaced, thread-safe key-value store for
// inter-agent knowledge sharing during a single run. Each agent writes
// under its own namespace (agentID/key), and all agents can read all entries.
type SharedMemory struct {
	mu      sync.RWMutex
	entries map[string]*MemoryEntry // key format: "agentID/key"
	dirty   bool                    // true if entries changed since last Summary() call
	cached  string                  // cached Summary() result
}

// MemoryEntry holds a single piece of shared knowledge.
type MemoryEntry struct {
	AgentID   string
	Key       string
	Value     string
	Timestamp time.Time
}

// NewSharedMemory creates an empty shared memory store.
func NewSharedMemory() *SharedMemory {
	return &SharedMemory{
		entries: make(map[string]*MemoryEntry),
	}
}

// Write stores a value under the agent's namespace.
// Validates that key is non-empty and truncates oversized values.
func (sm *SharedMemory) Write(agentID, key, value string) {
	if key == "" {
		return
	}
	// Truncate oversized values.
	if len(value) > MaxSharedMemoryValueLen {
		value = value[:MaxSharedMemoryValueLen] + "... [truncated]"
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Enforce entry limit: if at capacity and this is a new key, skip.
	fullKey := agentID + "/" + key
	if _, exists := sm.entries[fullKey]; !exists && len(sm.entries) >= MaxSharedMemoryEntries {
		return // at capacity, reject new entries
	}

	sm.entries[fullKey] = &MemoryEntry{
		AgentID:   agentID,
		Key:       key,
		Value:     value,
		Timestamp: time.Now(),
	}
	sm.dirty = true
}

// Read returns a specific entry, or empty string if not found.
func (sm *SharedMemory) Read(agentID, key string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	fullKey := agentID + "/" + key
	if entry, ok := sm.entries[fullKey]; ok {
		return entry.Value
	}
	return ""
}

// ReadAll returns copies of all entries (safe for concurrent use by callers).
func (sm *SharedMemory) ReadAll() []*MemoryEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*MemoryEntry, 0, len(sm.entries))
	for _, entry := range sm.entries {
		cp := *entry
		result = append(result, &cp)
	}
	return result
}

// ListByAgent returns copies of all entries written by a specific agent.
func (sm *SharedMemory) ListByAgent(agentID string) []*MemoryEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	prefix := agentID + "/"
	var result []*MemoryEntry
	for key, entry := range sm.entries {
		if strings.HasPrefix(key, prefix) {
			cp := *entry
			result = append(result, &cp)
		}
	}
	return result
}

// Summary returns a markdown-formatted summary of all shared memory entries,
// grouped by agent. This is injected into agent prompts to share context.
// Results are cached and only regenerated when entries change.
func (sm *SharedMemory) Summary() string {
	sm.mu.RLock()
	if !sm.dirty && sm.cached != "" {
		cached := sm.cached
		sm.mu.RUnlock()
		return cached
	}
	sm.mu.RUnlock()

	// Need write lock to update cache.
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Double-check after acquiring write lock.
	if !sm.dirty && sm.cached != "" {
		return sm.cached
	}

	if len(sm.entries) == 0 {
		sm.cached = ""
		sm.dirty = false
		return ""
	}

	// Group by agent.
	byAgent := make(map[string][]*MemoryEntry)
	for _, entry := range sm.entries {
		byAgent[entry.AgentID] = append(byAgent[entry.AgentID], entry)
	}

	var sb strings.Builder
	sb.WriteString("# Shared Agent Memory\n\nThe following knowledge was discovered by other agents during this session:\n\n")

	for agentID, entries := range byAgent {
		if sb.Len() >= MaxSummaryLen {
			sb.WriteString("\n... [additional entries omitted]\n")
			break
		}
		sb.WriteString(fmt.Sprintf("## From %s\n\n", agentID))
		for _, entry := range entries {
			if sb.Len() >= MaxSummaryLen {
				break
			}
			value := entry.Value
			if len(value) > 500 {
				value = value[:500] + "... [truncated]"
			}
			sb.WriteString(fmt.Sprintf("**%s:** %s\n\n", entry.Key, value))
		}
	}

	sm.cached = sb.String()
	sm.dirty = false
	return sm.cached
}

// Size returns the number of entries in shared memory.
func (sm *SharedMemory) Size() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.entries)
}
