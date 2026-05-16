package session

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/user/agent/core/tools"
)

func testTracker() *FileCoherenceTracker {
	return NewFileCoherenceTracker(func(id string) string {
		return "Session " + id
	})
}

func ctxWithSession(sessionID string) context.Context {
	return ContextWithSessionID(context.Background(), sessionID)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileCoherenceTracker_FirstRead_NoConflict(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "hello")

	ctx := ctxWithSession("sess-1")
	tracker.Lock(path)
	conflict := tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	if conflict != nil {
		t.Fatalf("expected no conflict on first read, got: %+v", conflict)
	}
}

func TestFileCoherenceTracker_UnchangedRead_NoConflict(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "hello")

	ctx := ctxWithSession("sess-1")

	tracker.Lock(path)
	tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	// Second read without modification
	tracker.Lock(path)
	conflict := tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	if conflict != nil {
		t.Fatalf("expected no conflict on unchanged re-read, got: %+v", conflict)
	}
}

func TestFileCoherenceTracker_ReadAfterExternalChange_Conflict(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "hello")

	ctx := ctxWithSession("sess-1")

	// First read
	tracker.Lock(path)
	tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	// Modify file externally (simulate another process or manual edit)
	time.Sleep(10 * time.Millisecond) // ensure mtime differs
	writeTestFile(t, path, "hello world — modified externally")

	// Second read should detect conflict
	tracker.Lock(path)
	conflict := tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	if conflict == nil {
		t.Fatal("expected conflict after external modification, got nil")
	}
	if conflict.ModifiedBy != "external" {
		t.Errorf("expected ModifiedBy='external', got %q", conflict.ModifiedBy)
	}
	if conflict.Path != path {
		t.Errorf("expected Path=%q, got %q", path, conflict.Path)
	}
}

func TestFileCoherenceTracker_ReadAfterOtherSessionWrite_Conflict(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "original content")

	ctxA := ctxWithSession("sess-A")
	ctxB := ctxWithSession("sess-B")

	// Session A reads
	tracker.Lock(path)
	tracker.CheckRead(ctxA, path)
	tracker.Unlock(path)

	// Session B writes (simulating a write_file call)
	time.Sleep(10 * time.Millisecond)
	writeTestFile(t, path, "modified by session B")
	tracker.Lock(path)
	tracker.RecordWrite(ctxB, path)
	tracker.Unlock(path)

	// Session A re-reads — should see conflict from B
	tracker.Lock(path)
	conflict := tracker.CheckRead(ctxA, path)
	tracker.Unlock(path)

	if conflict == nil {
		t.Fatal("expected conflict after other session write, got nil")
	}
	if conflict.ModifiedBy != "Session sess-B" {
		t.Errorf("expected ModifiedBy='Session sess-B', got %q", conflict.ModifiedBy)
	}
}

func TestFileCoherenceTracker_WriteWithNoRead_NoConflict(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "new-file.txt")
	writeTestFile(t, path, "content")

	ctx := ctxWithSession("sess-1")

	// Write without prior read — should not conflict
	tracker.Lock(path)
	conflict := tracker.CheckWrite(ctx, path)
	tracker.Unlock(path)

	if conflict != nil {
		t.Fatalf("expected no conflict on write without prior read, got: %+v", conflict)
	}
}

func TestFileCoherenceTracker_WriteAfterExternalChange_Conflict(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "original")

	ctx := ctxWithSession("sess-1")

	// Read the file
	tracker.Lock(path)
	tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	// Modify externally
	time.Sleep(10 * time.Millisecond)
	writeTestFile(t, path, "modified externally")

	// Try to write — should get conflict
	tracker.Lock(path)
	conflict := tracker.CheckWrite(ctx, path)
	tracker.Unlock(path)

	if conflict == nil {
		t.Fatal("expected conflict on write after external change, got nil")
	}
}

func TestFileCoherenceTracker_RecordWrite_UpdatesSnapshot(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "v1")

	ctx := ctxWithSession("sess-1")

	// Read
	tracker.Lock(path)
	tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	// Simulate write: modify file, then record
	time.Sleep(10 * time.Millisecond)
	writeTestFile(t, path, "v2 written by session")
	tracker.Lock(path)
	tracker.RecordWrite(ctx, path)
	tracker.Unlock(path)

	// Next CheckWrite should pass (snapshot updated)
	tracker.Lock(path)
	conflict := tracker.CheckWrite(ctx, path)
	tracker.Unlock(path)

	if conflict != nil {
		t.Fatalf("expected no conflict after RecordWrite updated snapshot, got: %+v", conflict)
	}
}

func TestFileCoherenceTracker_RecordDelete_PurgesAllSessions(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "content")

	ctxA := ctxWithSession("sess-A")
	ctxB := ctxWithSession("sess-B")

	// Both sessions read the file
	tracker.Lock(path)
	tracker.CheckRead(ctxA, path)
	tracker.Unlock(path)

	tracker.Lock(path)
	tracker.CheckRead(ctxB, path)
	tracker.Unlock(path)

	// Session A deletes the file
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	tracker.Lock(path)
	tracker.RecordDelete(ctxA, path)
	tracker.Unlock(path)

	// Verify snapshots are purged: session B writing should not conflict
	// (since there's no snapshot left)
	writeTestFile(t, path, "new content")
	tracker.Lock(path)
	conflict := tracker.CheckWrite(ctxB, path)
	tracker.Unlock(path)

	if conflict != nil {
		t.Fatalf("expected no conflict after RecordDelete purged snapshots, got: %+v", conflict)
	}
}

func TestFileCoherenceTracker_PurgeSession(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "content")

	ctx := ctxWithSession("sess-1")

	// Read to create snapshot
	tracker.Lock(path)
	tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	// Purge session
	tracker.PurgeSession("sess-1")

	// After purge, write should not conflict (no snapshot)
	time.Sleep(10 * time.Millisecond)
	writeTestFile(t, path, "modified")
	tracker.Lock(path)
	conflict := tracker.CheckWrite(ctx, path)
	tracker.Unlock(path)

	if conflict != nil {
		t.Fatalf("expected no conflict after PurgeSession, got: %+v", conflict)
	}
}

func TestFileCoherenceTracker_Concurrent(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()

	const numFiles = 5
	const numSessions = 4
	const iterations = 20

	// Create files
	paths := make([]string, numFiles)
	for i := range numFiles {
		paths[i] = filepath.Join(dir, "file"+string(rune('A'+i))+".txt")
		writeTestFile(t, paths[i], "initial")
	}

	var wg sync.WaitGroup
	for s := range numSessions {
		wg.Add(1)
		go func(sessionIdx int) {
			defer wg.Done()
			sessionID := "sess-" + string(rune('0'+sessionIdx))
			ctx := ctxWithSession(sessionID)

			for range iterations {
				for _, p := range paths {
					tracker.Lock(p)
					tracker.CheckRead(ctx, p)
					tracker.Unlock(p)

					tracker.Lock(p)
					conflict := tracker.CheckWrite(ctx, p)
					if conflict == nil {
						// Simulate write
						_ = os.WriteFile(p, []byte("written by "+sessionID), 0o644)
						tracker.RecordWrite(ctx, p)
					}
					tracker.Unlock(p)
				}
			}
		}(s)
	}
	wg.Wait()
	// If we reach here without panic/race, test passes.
}

func TestFileCoherenceTracker_ActivityRingBuffer_Cap(t *testing.T) {
	tracker := NewFileCoherenceTracker(func(id string) string { return id })
	tracker.activityCap = 5 // small cap for testing

	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "content")

	ctx := ctxWithSession("sess-1")

	// Record more writes than cap
	for i := range 10 {
		time.Sleep(time.Millisecond)
		writeTestFile(t, path, "v"+string(rune('0'+i)))
		tracker.Lock(path)
		tracker.RecordWrite(ctx, path)
		tracker.Unlock(path)
	}

	// Verify activity is capped
	tracker.mu.RLock()
	actLen := len(tracker.activity)
	tracker.mu.RUnlock()

	if actLen > 5 {
		t.Errorf("expected activity length <= 5, got %d", actLen)
	}
}

func TestFileCoherenceTracker_CheckWrite_FileDeletedExternally(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "content")

	ctx := ctxWithSession("sess-1")

	// Read
	tracker.Lock(path)
	tracker.CheckRead(ctx, path)
	tracker.Unlock(path)

	// Delete file externally
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// CheckWrite should return conflict (file gone)
	tracker.Lock(path)
	conflict := tracker.CheckWrite(ctx, path)
	tracker.Unlock(path)

	if conflict == nil {
		t.Fatal("expected conflict when file deleted externally, got nil")
	}
	if conflict.CurrentSig != (tools.FileSig{}) {
		t.Errorf("expected zero CurrentSig for deleted file, got: %+v", conflict.CurrentSig)
	}
}

func TestFileCoherenceTracker_NoSessionID_NoOp(t *testing.T) {
	tracker := testTracker()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeTestFile(t, path, "content")

	// Context without session ID
	ctx := context.Background()

	tracker.Lock(path)
	conflict := tracker.CheckRead(ctx, path)
	tracker.Unlock(path)
	if conflict != nil {
		t.Fatal("expected nil when no session ID in context")
	}

	tracker.Lock(path)
	conflict = tracker.CheckWrite(ctx, path)
	tracker.Unlock(path)
	if conflict != nil {
		t.Fatal("expected nil when no session ID in context")
	}
}
