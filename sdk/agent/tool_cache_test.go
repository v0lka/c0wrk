package agent

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestToolResultCache_NewToolResultCache(t *testing.T) {
	c := NewToolResultCache(0)
	if c == nil {
		t.Fatal("NewToolResultCache returned nil")
	}
	if c.Len() != 0 {
		t.Errorf("new cache should be empty, got %d entries", c.Len())
	}
}

func TestToolResultCache_StoreAndGet(t *testing.T) {
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:  "/test/file.go",
		FileMtime: 1234567890,
		FileSize:  100,
		IsMCP:     false,
	}
	hash := c.Store("test_tool", "cached content", meta)
	if hash == "" {
		t.Fatal("Store returned empty hash")
	}
	entry, ok := c.Get(hash)
	if !ok {
		t.Fatal("Get returned false for stored hash")
	}
	if entry.Content != "cached content" {
		t.Errorf("Get.Content = %q, want %q", entry.Content, "cached content")
	}
}

func TestToolResultCache_GetMissing(t *testing.T) {
	c := NewToolResultCache(0)
	_, ok := c.Get("nonexistent_hash")
	if ok {
		t.Error("Get returned true for missing hash")
	}
}

func TestToolResultCache_CheckCoherence(t *testing.T) {
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{}
	hash := c.Store("test_tool", "content", meta)
	valid, _ := c.CheckCoherence(hash)
	if !valid {
		t.Error("CheckCoherence returned false for valid entry")
	}
}

func TestToolResultCache_CheckCoherenceMissing(t *testing.T) {
	c := NewToolResultCache(0)
	valid, reason := c.CheckCoherence("nonexistent_hash")
	if valid {
		t.Error("CheckCoherence returned true for missing hash")
	}
	if reason == "" {
		t.Error("CheckCoherence should return a reason for missing hash")
	}
}

func TestToolResultCache_Expiry(t *testing.T) {
	c := NewToolResultCache(1 * time.Millisecond)
	meta := ToolCacheMeta{IsMCP: true}
	hash := c.Store("mcp_tool", "content", meta)
	time.Sleep(5 * time.Millisecond)
	valid, reason := c.CheckCoherence(hash)
	if valid {
		t.Error("CheckCoherence should return false for expired MCP entry")
	}
	if reason == "" {
		t.Error("CheckCoherence should return a reason for expired entry")
	}
}

func TestToolResultCache_Len(t *testing.T) {
	c := NewToolResultCache(0)
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
	c.Store("t1", "c1", ToolCacheMeta{})
	c.Store("t2", "c2", ToolCacheMeta{})
	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}
}

func TestToolResultCache_PeriodicEviction(t *testing.T) {
	c := NewToolResultCache(1 * time.Millisecond)
	meta := ToolCacheMeta{IsMCP: true}
	for i := range 150 {
		c.Store("t", "content_"+string(rune('0'+i%10)), meta)
	}
	time.Sleep(5 * time.Millisecond)
	count := c.Len()
	if count > 100 {
		t.Logf("Cache has %d entries after expiry", count)
	}
}

func TestSha256hex(t *testing.T) {
	h1 := sha256hex("hello")
	h2 := sha256hex("hello")
	h3 := sha256hex("world")
	if h1 != h2 {
		t.Error("sha256hex should be deterministic")
	}
	if h1 == h3 {
		t.Error("different inputs should produce different hashes")
	}
	if len(h1) != 64 {
		t.Errorf("sha256hex length = %d, want 64", len(h1))
	}
}

func TestToolResultCache_CheckCoherence_FileStatError(t *testing.T) {
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:  "/nonexistent/path/that/does/not/exist.txt",
		FileMtime: 1234567890,
		FileSize:  100,
	}
	hash := c.Store("read_file", "file content", meta)
	valid, reason := c.CheckCoherence(hash)
	if valid {
		t.Error("CheckCoherence should return false when file does not exist")
	}
	if reason == "" {
		t.Error("CheckCoherence should return a reason for stat error")
	}
	if !strings.Contains(reason, "no longer accessible") {
		t.Errorf("expected 'no longer accessible' in reason, got %q", reason)
	}
}

func TestToolResultCache_CheckCoherence_FileModified(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := tmpDir + "/test.txt"
	if err := os.WriteFile(testFile, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatal(err)
	}
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:  testFile,
		FileMtime: info.ModTime().UnixNano(),
		FileSize:  info.Size(),
	}
	hash := c.Store("read_file", "original content", meta)
	if err := os.WriteFile(testFile, []byte("modified content that is longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	valid, reason := c.CheckCoherence(hash)
	if valid {
		t.Error("CheckCoherence should return false when file has been modified")
	}
	if !strings.Contains(reason, "has been modified") {
		t.Errorf("expected 'has been modified' in reason, got %q", reason)
	}
}

func TestToolResultCache_CheckCoherence_FileUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := tmpDir + "/test.txt"
	if err := os.WriteFile(testFile, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatal(err)
	}
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:  testFile,
		FileMtime: info.ModTime().UnixNano(),
		FileSize:  info.Size(),
	}
	hash := c.Store("read_file", "original content", meta)
	valid, reason := c.CheckCoherence(hash)
	if !valid {
		t.Errorf("CheckCoherence should return true for unchanged file: %s", reason)
	}
}

func TestToolResultCache_EvictExpiredLocked_MixedEntries(t *testing.T) {
	c := NewToolResultCache(1 * time.Millisecond)
	nonMCPMeta := ToolCacheMeta{IsMCP: false}
	mcpMeta := ToolCacheMeta{IsMCP: true}
	c.Store("tool1", "content1", nonMCPMeta)
	c.Store("mcp1", "content2", mcpMeta)
	c.Store("tool2", "content3", nonMCPMeta)
	c.Store("mcp2", "content4", mcpMeta)
	time.Sleep(5 * time.Millisecond)
	c.mu.Lock()
	c.evictExpiredLocked()
	c.mu.Unlock()
	count := c.Len()
	if count != 2 {
		t.Errorf("Len = %d, want 2 (non-MCP entries should survive)", count)
	}
}

func TestToolResultCache_Get_ExpiredEntry(t *testing.T) {
	c := NewToolResultCache(1 * time.Millisecond)
	meta := ToolCacheMeta{IsMCP: true}
	hash := c.Store("mcp_tool", "content", meta)
	time.Sleep(5 * time.Millisecond)
	_, ok := c.Get(hash)
	if ok {
		t.Error("Get should return false for expired MCP entry")
	}
	c.mu.RLock()
	_, exists := c.entries[hash]
	c.mu.RUnlock()
	if exists {
		t.Error("expired entry should be deleted from map after Get")
	}
}

func TestToolResultCache_Get_RecheckAfterLockUpgrade(t *testing.T) {
	c := NewToolResultCache(1 * time.Millisecond)
	meta := ToolCacheMeta{IsMCP: true}
	hash := c.Store("mcp_tool", "content", meta)
	c.mu.Lock()
	c.entries[hash].TTL = 0
	c.mu.Unlock()
	entry, ok := c.Get(hash)
	if !ok {
		t.Error("Get should return true when entry has TTL=0 (recheck path)")
	}
	if entry == nil {
		t.Error("entry should not be nil")
	}
}

// --- ComputeToolResultHash tests ---

func TestComputeToolResultHash(t *testing.T) {
	// Determinism: same inputs → same hash.
	h1 := ComputeToolResultHash("read_file", "some content")
	h2 := ComputeToolResultHash("read_file", "some content")
	if h1 != h2 {
		t.Errorf("ComputeToolResultHash should be deterministic: %s != %s", h1, h2)
	}
	if h1 == "" {
		t.Error("ComputeToolResultHash returned empty string")
	}
	if len(h1) != 64 {
		t.Errorf("ComputeToolResultHash length = %d, want 64 (SHA256 hex)", len(h1))
	}

	// Different tool names → different hashes.
	h3 := ComputeToolResultHash("ripgrep", "some content")
	if h1 == h3 {
		t.Error("different tool names should produce different hashes")
	}

	// Different content → different hashes.
	h4 := ComputeToolResultHash("read_file", "different content")
	if h1 == h4 {
		t.Error("different content should produce different hashes")
	}

	// Different content AND different tool → different hashes.
	h5 := ComputeToolResultHash("ripgrep", "different content")
	if h1 == h5 {
		t.Error("different tool+content should produce different hashes")
	}
}

// --- File-backed cache entry tests ---

func TestToolResultCache_FileBacked_StoreAndGet(t *testing.T) {
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:   "/test/file.go",
		FileMtime:  1234567890,
		FileSize:   100,
		FileBacked: true,
	}
	hash := c.Store("read_file", "ignored content", meta)
	if hash == "" {
		t.Fatal("Store returned empty hash")
	}
	entry, ok := c.Get(hash)
	if !ok {
		t.Fatal("Get returned false for stored file-backed hash")
	}
	if entry.Content != "" {
		t.Errorf("file-backed entry Content = %q, want empty", entry.Content)
	}
	if !entry.FileBacked {
		t.Error("file-backed entry should have FileBacked = true")
	}
	if entry.FilePath != "/test/file.go" {
		t.Errorf("FilePath = %q, want %q", entry.FilePath, "/test/file.go")
	}
}

func TestToolResultCache_FileBacked_StableHash(t *testing.T) {
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:   "/test/file.go",
		FileMtime:  1234567890,
		FileSize:   100,
		FileBacked: true,
	}
	hash1 := c.Store("read_file", "window content 1", meta)
	hash2 := c.Store("read_file", "window content 2", meta)
	if hash1 != hash2 {
		t.Errorf("same file metadata should produce same hash: %s != %s", hash1, hash2)
	}
}

func TestToolResultCache_FileBacked_DifferentMetadataDifferentHash(t *testing.T) {
	c := NewToolResultCache(0)
	meta1 := ToolCacheMeta{
		FilePath:   "/test/file.go",
		FileMtime:  1234567890,
		FileSize:   100,
		FileBacked: true,
	}
	meta2 := ToolCacheMeta{
		FilePath:   "/test/file.go",
		FileMtime:  1234567891, // different mtime
		FileSize:   100,
		FileBacked: true,
	}
	hash1 := c.Store("read_file", "content", meta1)
	hash2 := c.Store("read_file", "content", meta2)
	if hash1 == hash2 {
		t.Error("different file metadata should produce different hashes")
	}
}

func TestToolResultCache_FileBacked_CheckCoherence(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := tmpDir + "/test.txt"
	if err := os.WriteFile(testFile, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatal(err)
	}
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:   testFile,
		FileMtime:  info.ModTime().UnixNano(),
		FileSize:   info.Size(),
		FileBacked: true,
	}
	hash := c.Store("read_file", "", meta)
	valid, _ := c.CheckCoherence(hash)
	if !valid {
		t.Error("CheckCoherence should return true for unchanged file-backed entry")
	}
}

func TestToolResultCache_FileBacked_CheckCoherenceModified(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := tmpDir + "/test.txt"
	if err := os.WriteFile(testFile, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatal(err)
	}
	c := NewToolResultCache(0)
	meta := ToolCacheMeta{
		FilePath:   testFile,
		FileMtime:  info.ModTime().UnixNano(),
		FileSize:   info.Size(),
		FileBacked: true,
	}
	hash := c.Store("read_file", "", meta)
	if err := os.WriteFile(testFile, []byte("modified content that is longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	valid, reason := c.CheckCoherence(hash)
	if valid {
		t.Error("CheckCoherence should return false for modified file-backed entry")
	}
	if !strings.Contains(reason, "has been modified") {
		t.Errorf("expected 'has been modified' in reason, got %q", reason)
	}
}

func TestComputeFileBackedHash(t *testing.T) {
	h1 := ComputeFileBackedHash("read_file", "/path/file.go", 123, 456)
	h2 := ComputeFileBackedHash("read_file", "/path/file.go", 123, 456)
	if h1 != h2 {
		t.Error("ComputeFileBackedHash should be deterministic")
	}
	h3 := ComputeFileBackedHash("read_file", "/path/file.go", 124, 456)
	if h1 == h3 {
		t.Error("different mtime should produce different hash")
	}
	h4 := ComputeFileBackedHash("ripgrep", "/path/file.go", 123, 456)
	if h1 == h4 {
		t.Error("different tool name should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64", len(h1))
	}
}

// --- Short-hash abbreviation tests ---

func TestToolResultCache_StoreReturnsShortHash(t *testing.T) {
	c := NewToolResultCache(0)
	short := c.Store("read_file", "some content", ToolCacheMeta{})
	if short == "" {
		t.Fatal("Store returned empty hash")
	}
	// No collision on the first entry → minimal abbreviation length.
	if len(short) != minAbbrevLen {
		t.Errorf("first short hash length = %d, want %d (%q)", len(short), minAbbrevLen, short)
	}
	if len(short) >= fullHashLen {
		t.Errorf("short hash should be abbreviated, got full-length %q", short)
	}
}

func TestToolResultCache_AbbreviateLocked(t *testing.T) {
	c := NewToolResultCache(0)
	target := ComputeToolResultHash("target_tool", "target content")

	// No collisions yet → minimal length.
	if got := c.abbreviateLocked(target); len(got) != minAbbrevLen {
		t.Errorf("abbreviateLocked = %d chars, want %d", len(got), minAbbrevLen)
	}

	// Seed a known short hash that collides with target's 4-char prefix.
	c.entries[target[:minAbbrevLen]] = &ToolResultCacheEntry{Hash: ComputeToolResultHash("other", "x")}
	got := c.abbreviateLocked(target)
	if got == target[:minAbbrevLen] {
		t.Error("abbreviateLocked should extend past a colliding 4-char prefix")
	}
	if !strings.HasPrefix(target, got) {
		t.Errorf("short %q must be a prefix of the full hash", got)
	}
	if len(got) != minAbbrevLen+1 {
		t.Errorf("expected one-character extension to %d, got %d (%q)", minAbbrevLen+1, len(got), got)
	}
}

func TestToolResultCache_AbbreviateLocked_ChainedCollisions(t *testing.T) {
	c := NewToolResultCache(0)
	target := ComputeToolResultHash("chained_tool", "content")

	// Occupy the first three prefix lengths (4, 5, 6) so the resolver must
	// walk to length 7.
	for n := minAbbrevLen; n <= minAbbrevLen+2; n++ {
		c.entries[target[:n]] = &ToolResultCacheEntry{Hash: strings.Repeat("z", fullHashLen)}
	}
	got := c.abbreviateLocked(target)
	if len(got) != minAbbrevLen+3 {
		t.Errorf("expected length %d after three collisions, got %d (%q)", minAbbrevLen+3, len(got), got)
	}
	if _, exists := c.entries[got]; exists {
		t.Errorf("resolved short %q should be free", got)
	}
}

func TestToolResultCache_StoreCollisionExtendsShortHash(t *testing.T) {
	c := NewToolResultCache(0)
	target := ComputeToolResultHash("collide_tool", "content")

	// Pre-seed the cache so target's 4-char prefix is already a known key.
	c.entries[target[:minAbbrevLen]] = &ToolResultCacheEntry{Hash: ComputeToolResultHash("existing", "x")}
	c.fullToShort[c.entries[target[:minAbbrevLen]].Hash] = target[:minAbbrevLen]

	short := c.Store("collide_tool", "content", ToolCacheMeta{})
	if short == target[:minAbbrevLen] {
		t.Error("Store should return an extended short hash when the 4-char prefix collides")
	}
	if !strings.HasPrefix(target, short) {
		t.Errorf("returned short %q must be a prefix of the full hash", short)
	}
	// The stored entry must be retrievable by the issued short hash.
	if _, ok := c.Get(short); !ok {
		t.Errorf("Get should resolve the issued short hash %q", short)
	}
}

func TestToolResultCache_GetByFullHash(t *testing.T) {
	c := NewToolResultCache(0)
	short := c.Store("read_file", "payload", ToolCacheMeta{})
	full := ComputeToolResultHash("read_file", "payload")

	// The full hash is not the map key, but prefix resolution must find it.
	entry, ok := c.Get(full)
	if !ok {
		t.Fatal("Get should resolve the full hash via prefix fallback")
	}
	if entry.Content != "payload" {
		t.Errorf("Content = %q, want %q", entry.Content, "payload")
	}
	if _, ok := c.Get(short); !ok {
		t.Error("Get should still resolve the issued short hash")
	}
}

func TestToolResultCache_StableShortHashForSameContent(t *testing.T) {
	c := NewToolResultCache(0)
	first := c.Store("read_file", "same content", ToolCacheMeta{})
	second := c.Store("read_file", "same content", ToolCacheMeta{})
	if first != second {
		t.Errorf("identical content should map to the same short hash: %q != %q", first, second)
	}
	if c.Len() != 1 {
		t.Errorf("dedup should keep a single entry, got Len = %d", c.Len())
	}
}

func TestToolResultCache_DistinctContentDistinctShortHash(t *testing.T) {
	c := NewToolResultCache(0)
	a := c.Store("read_file", "content A", ToolCacheMeta{})
	b := c.Store("read_file", "content B", ToolCacheMeta{})
	if a == b {
		t.Errorf("distinct content must produce distinct short hashes, both = %q", a)
	}
	if c.Len() != 2 {
		t.Errorf("expected 2 entries, got %d", c.Len())
	}
}

func TestToolResultCache_Get_AmbiguousPrefix(t *testing.T) {
	c := NewToolResultCache(0)
	// Two distinct full hashes sharing the first two characters.
	fullA := "ab" + strings.Repeat("a", fullHashLen-2)
	fullB := "ab" + strings.Repeat("b", fullHashLen-2)
	c.entries[fullA[:minAbbrevLen]] = &ToolResultCacheEntry{Hash: fullA}
	c.entries[fullB[:minAbbrevLen]] = &ToolResultCacheEntry{Hash: fullB}

	// "ab" matches both full hashes → ambiguous → not resolved.
	if _, ok := c.Get("ab"); ok {
		t.Error("Get should not resolve an ambiguous prefix")
	}
	// Each full hash resolves unambiguously on its own.
	if _, ok := c.Get(fullA); !ok {
		t.Error("Get should resolve fullA by exact full-hash match")
	}
}

func TestToolResultCache_Get_EmptyHashNotResolved(t *testing.T) {
	c := NewToolResultCache(0)
	c.Store("read_file", "only entry", ToolCacheMeta{})

	// An empty hash matches every entry via HasPrefix; it must NOT resolve to
	// the single entry (ambiguous by definition), even when only one exists.
	if _, ok := c.Get(""); ok {
		t.Error("Get(\"\") should not resolve even with a single cached entry")
	}
}

// --- Short-hash retirement (anti-aliasing after eviction) tests ---

func TestToolResultCache_AbbreviateLocked_SkipsRetiredKeys(t *testing.T) {
	c := NewToolResultCache(0)
	target := ComputeToolResultHash("target_tool", "content")

	// Retire target's 4-char prefix (as if an entry that held it was evicted).
	c.retiredShort[target[:minAbbrevLen]] = struct{}{}

	got := c.abbreviateLocked(target)
	if got == target[:minAbbrevLen] {
		t.Fatal("abbreviateLocked must skip retired short keys")
	}
	if !strings.HasPrefix(target, got) {
		t.Errorf("result %q must be a prefix of the full hash", got)
	}
	if len(got) <= minAbbrevLen {
		t.Errorf("expected extension past the retired %d-char key, got %d (%q)", minAbbrevLen, len(got), got)
	}
	if _, retired := c.retiredShort[got]; retired {
		t.Errorf("resolved short %q must not itself be retired", got)
	}
}

func TestToolResultCache_Resolve_RetiredHashDoesNotAlias(t *testing.T) {
	c := NewToolResultCache(0)
	short := "abcd"
	full := short + strings.Repeat("1", fullHashLen-len(short))

	// A live entry whose full hash SHARES the retired prefix, but was forced to
	// a longer key because the prefix was retired.
	c.entries[short+"1"] = &ToolResultCacheEntry{Hash: full}
	// Retire the prefix (as if an entry with key `short` had been evicted).
	c.retiredShort[short] = struct{}{}

	// Resolving the retired short hash must NOT alias the live entry via the
	// prefix fallback.
	if _, ok := c.Get(short); ok {
		t.Error("a retired short hash must not resolve (no aliasing via prefix fallback)")
	}
	// The live entry still resolves by its own issued key.
	if _, ok := c.Get(short + "1"); !ok {
		t.Error("live entry should still resolve by its own short key")
	}
}

func TestToolResultCache_EvictionRetiresShortHash(t *testing.T) {
	c := NewToolResultCache(10 * time.Millisecond)
	short := c.Store("mcp_tool", "payload", ToolCacheMeta{IsMCP: true})

	time.Sleep(20 * time.Millisecond)

	// Trigger eviction through Get's slow path (entry now past its TTL).
	if _, ok := c.Get(short); ok {
		t.Fatal("expired MCP entry should not resolve")
	}

	// The short key is now retired — a future entry must not reuse it, and an
	// old reference must resolve to not-found rather than to new content.
	c.mu.RLock()
	_, retired := c.retiredShort[short]
	c.mu.RUnlock()
	if !retired {
		t.Fatal("evicted entry's short key should be retired to prevent reuse")
	}

	// Store unrelated content; its short hash must differ from the retired one.
	other := c.Store("mcp_tool", "different payload", ToolCacheMeta{IsMCP: true})
	if other == short {
		t.Errorf("new entry reused retired short hash %q", short)
	}
	// The retired reference still resolves to not-found.
	if _, ok := c.Get(short); ok {
		t.Error("retired short hash must not resolve after a new entry is stored")
	}
}
