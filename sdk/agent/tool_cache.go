package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Abbreviation bounds for the git-style short cache hashes.
const (
	// minAbbrevLen is the initial short-hash length. 4 hex chars give a 65 536
	// prefix space — ample for a session-scoped cache (unlike git, which starts
	// at 7 for repos with millions of objects). Short hashes grow only when a
	// collision forces it.
	minAbbrevLen = 4
	// fullHashLen is the length of a hex-encoded SHA256 (64 chars) — the
	// unambiguous maximum for an abbreviated hash.
	fullHashLen = sha256.Size * 2
)

// ToolResultCache caches raw tool outputs keyed by a git-style SHORT hash.
//
// Each entry's key is the shortest prefix (starting at minAbbrevLen hex chars)
// of the full SHA256 that is unique among all hashes already known in the
// session. Collisions are resolved per-entry: when a new entry's minAbbrevLen
// prefix is already taken by a known session hash, the prefix length is
// incremented one character at a time until it is unique — exactly like git
// expanding an abbreviated object id. Identical content (same full hash) always
// maps to the same short hash, so dedup and history-mutation references stay
// stable.
//
// Internally the full hash is retained on the entry (Hash field) plus a
// fullToShort index, so resolution accepts the issued short hash, the full
// hash, or any unique prefix.
//
// Short hashes are immutable once issued: when an entry is evicted (MCP TTL),
// its short key is retired (retiredShort) and never reused or aliased, so stale
// history references resolve to not-found rather than to unrelated content.
//
// Cache entries live for the duration of a session (per-orchestrator lifetime).
// MCP tool entries are subject to TTL-based expiry.
type ToolResultCache struct {
	mu           sync.RWMutex
	entries      map[string]*ToolResultCacheEntry // keyed by short hash
	fullToShort  map[string]string                // full hash → short hash (dedup + reverse lookup)
	retiredShort map[string]struct{}              // short keys of evicted entries — never reused, never aliased
	ttl          time.Duration                    // default TTL for MCP tools (0 = no expiry for non-MCP)
	storeCount   int64                            // incremented on every Store; periodic eviction on every 100th call
}

// ToolResultCacheEntry holds a cached tool output with metadata.
type ToolResultCacheEntry struct {
	Hash      string    // full SHA256 of Content (or file metadata for file-backed entries); used for dedup/coherence, NOT as the map key
	Content   string    // full raw tool output (empty for file-backed entries)
	ToolName  string    // e.g. "read_file", "ripgrep"
	CreatedAt time.Time // when the entry was cached

	// File-tool coherence metadata (zero-value if not a file tool).
	FilePath  string // absolute path to the file (only for file-based tools)
	FileMtime int64  // mod time at cache time (nanoseconds since epoch)
	FileSize  int64  // file size in bytes at cache time

	// FileBacked indicates that the entry's backing store is the file on disk
	// (Content is empty). tool_result_read streams fragments directly from
	// FilePath instead of splitting Content.
	FileBacked bool

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

	// FileBacked indicates that the tool's backing store is the file on disk.
	// When true, Store does not retain Content in memory; the cache entry
	// references the file via FilePath + mtime + size, and tool_result_read
	// streams fragments from disk on demand.
	FileBacked bool
}

// NewToolResultCache creates a new cache with the given default MCP TTL.
func NewToolResultCache(ttl time.Duration) *ToolResultCache {
	return &ToolResultCache{
		entries:      make(map[string]*ToolResultCacheEntry),
		fullToShort:  make(map[string]string),
		retiredShort: make(map[string]struct{}),
		ttl:          ttl,
	}
}

// ComputeToolResultHash returns the hex-encoded SHA256 hash for a tool result.
// This uses the same formula as Store: SHA256(toolName + "\x00" + content).
// Use this when you need the full hash before/without calling Store. Note that
// Store returns an ABBREVIATED (short) hash, not this full value.
func ComputeToolResultHash(toolName, content string) string {
	return sha256hex(toolName + "\x00" + content)
}

// ComputeFileBackedHash returns the hex-encoded SHA256 hash for a file-backed
// cache entry. The hash is derived from tool name, file path, mtime, and size
// (not from file content), so it is stable for an unchanged file and changes
// when the file is modified.
func ComputeFileBackedHash(toolName, filePath string, mtime, size int64) string {
	return sha256hex(fmt.Sprintf("%s\x00%s\x00%d\x00%d", toolName, filePath, mtime, size))
}

// Store caches raw tool output and returns its git-style SHORT hash.
//
// The full hash includes both toolName and content so that identical content
// from different tools gets different hashes. The returned short hash is the
// shortest prefix (starting at minAbbrevLen) of the full hash that does not
// collide with a hash already known in the session; the prefix grows one
// character at a time until it is unique. Repeated identical calls (same full
// hash) return the same short hash — no duplicate entries are created.
//
// For file-backed entries (meta.FileBacked == true), Content is NOT stored in
// memory — the file on disk is the backing store. The hash is derived from
// file metadata (path + mtime + size) instead of content.
func (c *ToolResultCache) Store(toolName, content string, meta ToolCacheMeta) string {
	var full string
	if meta.FileBacked {
		full = ComputeFileBackedHash(toolName, meta.FilePath, meta.FileMtime, meta.FileSize)
	} else {
		full = ComputeToolResultHash(toolName, content)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.storeCount++

	// Dedup: identical content (same full hash) maps to the same short hash,
	// keeping history-mutation references stable.
	if short, ok := c.fullToShort[full]; ok {
		return short
	}

	short := c.abbreviateLocked(full)

	entry := &ToolResultCacheEntry{
		Hash:       full,
		ToolName:   toolName,
		CreatedAt:  time.Now(),
		FilePath:   meta.FilePath,
		FileMtime:  meta.FileMtime,
		FileSize:   meta.FileSize,
		IsMCP:      meta.IsMCP,
		FileBacked: meta.FileBacked,
	}
	if !meta.FileBacked {
		entry.Content = content
	}
	if meta.IsMCP && c.ttl > 0 {
		entry.TTL = c.ttl
	}

	c.entries[short] = entry
	c.fullToShort[full] = short

	// Periodic eviction: sweep expired MCP entries every 100th Store.
	if c.storeCount%100 == 0 {
		c.evictExpiredLocked()
	}

	return short
}

// abbreviateLocked returns the shortest prefix of full (starting at
// minAbbrevLen hex chars) that is neither a live short-hash key nor a retired
// one. Retired keys (from evicted entries) are skipped so a short hash, once
// issued, is never handed to a different entry — preventing history references
// from silently aliasing new content. The prefix length is incremented one
// character at a time on each collision. The loop runs through fullHashLen,
// where full itself is always free (dedup guarantees it is not a live key, and
// a 64-char retired key cannot exist), so it always terminates. Caller must
// hold c.mu.
func (c *ToolResultCache) abbreviateLocked(full string) string {
	for n := minAbbrevLen; n <= fullHashLen; n++ {
		prefix := full[:n]
		if _, exists := c.entries[prefix]; exists {
			continue
		}
		if _, retired := c.retiredShort[prefix]; retired {
			continue
		}
		return prefix
	}
	return full // unreachable: full is free at n == fullHashLen
}

// resolveLocked maps a user-provided hash to its cache entry and short key.
// Resolution order:
//  1. exact short-hash key (the common path — the caller copies the hash from a
//     nudge);
//  2. retired short-hash keys resolve to not-found: an evicted entry must not
//     alias a different live entry that happens to share its prefix;
//  3. exact full-hash match (entry.Hash == hash);
//  4. unique prefix: exactly one entry whose full hash starts with hash.
//
// Returns ok=false when the hash is unknown, retired, or the prefix is
// ambiguous. Caller must hold at least c.mu.RLock.
func (c *ToolResultCache) resolveLocked(hash string) (entry *ToolResultCacheEntry, shortKey string, ok bool) {
	// An empty hash is not a valid lookup key: strings.HasPrefix(e.Hash, "") is
	// true for every entry, so it must be rejected explicitly rather than
	// resolving to an arbitrary single entry.
	if hash == "" {
		return nil, "", false
	}

	if e, found := c.entries[hash]; found {
		return e, hash, true
	}

	// A retired (evicted) short hash must not fall through to the prefix scan:
	// another live entry may share this prefix, which would silently alias it.
	if _, retired := c.retiredShort[hash]; retired {
		return nil, "", false
	}

	var match *ToolResultCacheEntry
	var matchKey string
	count := 0
	for short, e := range c.entries {
		if e.Hash == hash || strings.HasPrefix(e.Hash, hash) {
			match = e
			matchKey = short
			count++
			if count > 1 {
				return nil, "", false // ambiguous prefix
			}
		}
	}
	if count == 1 {
		return match, matchKey, true
	}
	return nil, "", false
}

// Get returns a cache entry by hash. The hash may be the issued short hash, the
// full hash, or any unique prefix. Returns nil, false if not found, expired, or
// the prefix is ambiguous.
// Uses RLock for the common (non-expired) path; only upgrades to Lock when
// an expired entry needs deletion.
func (c *ToolResultCache) Get(hash string) (*ToolResultCacheEntry, bool) {
	c.mu.RLock()
	entry, _, ok := c.resolveLocked(hash)
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

	// Re-resolve after acquiring write lock (entry may have changed).
	entry, key, ok := c.resolveLocked(hash)
	if !ok {
		return nil, false
	}
	if entry.TTL == 0 || time.Since(entry.CreatedAt) <= entry.TTL {
		return entry, true
	}

	delete(c.entries, key)
	delete(c.fullToShort, entry.Hash)
	c.retiredShort[key] = struct{}{} // retire the freed short key — never reuse/alias it
	return nil, false
}

// CheckCoherence verifies that a cached file-tool result is still valid.
// Returns false if the cache entry has expired (MCP TTL) or the file has
// changed (mtime or size) since caching. Non-file entries always pass.
func (c *ToolResultCache) CheckCoherence(hash string) (valid bool, reason string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, _, ok := c.resolveLocked(hash)
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
	for short, entry := range c.entries {
		if entry.TTL > 0 && time.Since(entry.CreatedAt) > entry.TTL {
			delete(c.entries, short)
			delete(c.fullToShort, entry.Hash)
			c.retiredShort[short] = struct{}{} // retire the freed short key — never reuse/alias it
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
