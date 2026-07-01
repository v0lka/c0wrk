package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
)

// ToolResultCache caches raw tool outputs indexed by SHA256(toolName + "\x00" + content).
// Cache entries live for the duration of a session (per-orchestrator lifetime).
// MCP tool entries are subject to TTL-based expiry.
type ToolResultCache struct {
	mu         sync.RWMutex
	entries    map[string]*ToolResultCacheEntry
	ttl        time.Duration // default TTL for MCP tools (0 = no expiry for non-MCP)
	storeCount int64         // incremented on every Store; periodic eviction on every 100th call
}

// ToolResultCacheEntry holds a cached tool output with metadata.
type ToolResultCacheEntry struct {
	Hash      string    // SHA256 of Content
	Content   string    // full raw tool output
	ToolName  string    // e.g. "read_file", "ripgrep"
	CreatedAt time.Time // when the entry was cached

	// File-tool coherence metadata (zero-value if not a file tool).
	FilePath  string // absolute path to the file (only for file-based tools)
	FileMtime int64  // mod time at cache time (nanoseconds since epoch)
	FileSize  int64  // file size in bytes at cache time

	// MCP expiry.
	TTL   time.Duration // >0 for MCP tools (copied from cache default at store time)
	IsMCP bool
}

// ToolCacheMeta carries file/metadata extracted by the executor at cache time.
type ToolCacheMeta struct {
	FilePath  string
	FileMtime int64
	FileSize  int64
	IsMCP     bool
}

// NewToolResultCache creates a new cache with the given default MCP TTL.
func NewToolResultCache(ttl time.Duration) *ToolResultCache {
	return &ToolResultCache{
		entries: make(map[string]*ToolResultCacheEntry),
		ttl:     ttl,
	}
}

// ComputeToolResultHash returns the hex-encoded SHA256 hash for a tool result.
// This uses the same formula as Store: SHA256(toolName + "\x00" + content).
// Use this when you need the hash before/without calling Store.
func ComputeToolResultHash(toolName, content string) string {
	return sha256hex(toolName + "\x00" + content)
}

// Store caches raw tool output and returns its SHA256 hash.
// The hash includes both toolName and content so that identical content from
// different tools gets different hashes.
// Repeated identical calls produce the same hash (no duplicate entries).
func (c *ToolResultCache) Store(toolName, content string, meta ToolCacheMeta) string {
	hash := ComputeToolResultHash(toolName, content)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.storeCount++

	// Avoid overwriting an existing entry (same content → same hash).
	if _, ok := c.entries[hash]; ok {
		return hash
	}

	entry := &ToolResultCacheEntry{
		Hash:      hash,
		Content:   content,
		ToolName:  toolName,
		CreatedAt: time.Now(),
		FilePath:  meta.FilePath,
		FileMtime: meta.FileMtime,
		FileSize:  meta.FileSize,
		IsMCP:     meta.IsMCP,
	}
	if meta.IsMCP && c.ttl > 0 {
		entry.TTL = c.ttl
	}

	c.entries[hash] = entry

	// Periodic eviction: sweep expired MCP entries every 100th Store.
	if c.storeCount%100 == 0 {
		c.evictExpiredLocked()
	}

	return hash
}

// Get returns a cache entry by hash. Returns nil, false if not found or expired.
// Uses RLock for the common (non-expired) path; only upgrades to Lock when
// an expired entry needs deletion.
func (c *ToolResultCache) Get(hash string) (*ToolResultCacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[hash]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}

	// Fast path: check TTL under read lock for non-expired entries.
	if entry.TTL == 0 || time.Since(entry.CreatedAt) <= entry.TTL {
		defer c.mu.RUnlock()
		return entry, true
	}

	// Slow path: entry expired — upgrade to write lock for deletion.
	c.mu.RUnlock()
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check after acquiring write lock (entry may have been refreshed).
	entry, ok = c.entries[hash]
	if !ok {
		return nil, false
	}
	if entry.TTL == 0 || time.Since(entry.CreatedAt) <= entry.TTL {
		return entry, true
	}

	delete(c.entries, hash)
	return nil, false
}

// CheckCoherence verifies that a cached file-tool result is still valid.
// Returns false if the cache entry has expired (MCP TTL) or the file has
// changed (mtime or size) since caching. Non-file entries always pass.
func (c *ToolResultCache) CheckCoherence(hash string) (valid bool, reason string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[hash]
	if !ok {
		return false, "cache entry not found"
	}

	// TTL expiry check for MCP entries.
	if entry.TTL > 0 && time.Since(entry.CreatedAt) > entry.TTL {
		return false, "cache entry expired"
	}

	if entry.FilePath == "" {
		return true, "" // non-file tool, no coherence check needed
	}

	info, err := os.Stat(entry.FilePath)
	if err != nil {
		return false, fmt.Sprintf("file '%s' no longer accessible: %v", entry.FilePath, err)
	}
	if info.ModTime().UnixNano() != entry.FileMtime || info.Size() != entry.FileSize {
		return false, fmt.Sprintf("file '%s' has been modified since the result was cached", entry.FilePath)
	}

	return true, ""
}

// evictExpiredLocked removes all expired MCP entries. Caller must hold c.mu.
func (c *ToolResultCache) evictExpiredLocked() {
	for hash, entry := range c.entries {
		if entry.TTL > 0 && time.Since(entry.CreatedAt) > entry.TTL {
			delete(c.entries, hash)
		}
	}
}

// Len returns the current number of cached entries.
func (c *ToolResultCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// sha256hex returns the hex-encoded SHA256 hash of s.
func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
