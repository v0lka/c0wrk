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
