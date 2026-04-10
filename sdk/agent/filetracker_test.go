package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func trackerCtx(stepID string) context.Context {
	return WithStepID(context.Background(), stepID)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFileChangeTracker_WriteAndGetStepChanges(t *testing.T) {
	dir := t.TempDir()
	tracker := NewFileChangeTracker(dir)
	ctx := trackerCtx("step-1")

	// Create a new file.
	absPath := filepath.Join(dir, "hello.txt")
	tracker.RecordBeforeWrite(ctx, absPath)
	writeFile(t, absPath, "hello world\n")
	tracker.RecordAfterWrite(ctx, absPath)

	changes := tracker.GetStepChanges("step-1")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != "hello.txt" {
		t.Errorf("expected path hello.txt, got %s", changes[0].Path)
	}
	if changes[0].Operation != "CREATE" {
		t.Errorf("expected CREATE, got %s", changes[0].Operation)
	}
	if changes[0].SizeBytes == 0 {
		t.Error("expected non-zero SizeBytes for CREATE")
	}
}

func TestFileChangeTracker_CreateNewFile(t *testing.T) {
	dir := t.TempDir()
	tracker := NewFileChangeTracker(dir)
	ctx := trackerCtx("step-1")

	absPath := filepath.Join(dir, "new.txt")
	tracker.RecordBeforeWrite(ctx, absPath)
	writeFile(t, absPath, "brand new\n")
	tracker.RecordAfterWrite(ctx, absPath)

	changes := tracker.GetStepChanges("step-1")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Operation != "CREATE" {
		t.Errorf("expected CREATE, got %s", c.Operation)
	}
	if c.Diff != "" {
		t.Errorf("expected empty diff for CREATE, got %q", c.Diff)
	}
}

func TestFileChangeTracker_ModifyExistingFile(t *testing.T) {
	dir := t.TempDir()
	absPath := filepath.Join(dir, "existing.txt")
	writeFile(t, absPath, "original content\n")

	tracker := NewFileChangeTracker(dir)
	ctx := trackerCtx("step-1")

	tracker.RecordBeforeWrite(ctx, absPath)
	writeFile(t, absPath, "modified content\n")
	tracker.RecordAfterWrite(ctx, absPath)

	changes := tracker.GetStepChanges("step-1")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Operation != "MODIFY" {
		t.Errorf("expected MODIFY, got %s", c.Operation)
	}
	if c.Diff == "" {
		t.Error("expected non-empty diff for MODIFY")
	}
	if !strings.Contains(c.Diff, "-original content") {
		t.Errorf("diff should contain removed line, got: %s", c.Diff)
	}
	if !strings.Contains(c.Diff, "+modified content") {
		t.Errorf("diff should contain added line, got: %s", c.Diff)
	}
	if c.SizeBytes == 0 {
		t.Error("expected non-zero SizeBytes for MODIFY")
	}
}

func TestFileChangeTracker_DeleteFile(t *testing.T) {
	dir := t.TempDir()
	absPath := filepath.Join(dir, "todelete.txt")
	writeFile(t, absPath, "delete me\n")

	tracker := NewFileChangeTracker(dir)
	ctx := trackerCtx("step-1")

	tracker.RecordDelete(ctx, absPath)
	_ = os.Remove(absPath)

	changes := tracker.GetStepChanges("step-1")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Operation != "DELETE" {
		t.Errorf("expected DELETE, got %s", c.Operation)
	}
	if c.SizeBytes != 0 {
		t.Errorf("expected 0 SizeBytes for DELETE, got %d", c.SizeBytes)
	}
}

func TestFileChangeTracker_RollbackStep(t *testing.T) {
	dir := t.TempDir()

	// Pre-existing file that will be modified.
	existingPath := filepath.Join(dir, "existing.txt")
	writeFile(t, existingPath, "original\n")

	// File that will be created.
	newPath := filepath.Join(dir, "new.txt")

	tracker := NewFileChangeTracker(dir)
	ctx := trackerCtx("step-1")

	// Modify existing file.
	tracker.RecordBeforeWrite(ctx, existingPath)
	writeFile(t, existingPath, "changed\n")
	tracker.RecordAfterWrite(ctx, existingPath)

	// Create new file.
	tracker.RecordBeforeWrite(ctx, newPath)
	writeFile(t, newPath, "new content\n")
	tracker.RecordAfterWrite(ctx, newPath)

	// Rollback.
	if err := tracker.RollbackStep("step-1"); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	// Existing file should be restored.
	got := readFileContent(t, existingPath)
	if got != "original\n" {
		t.Errorf("expected original content after rollback, got %q", got)
	}

	// New file should be deleted.
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Error("expected new file to be deleted after rollback")
	}
}

func TestFileChangeTracker_RollbackAll(t *testing.T) {
	dir := t.TempDir()

	file1 := filepath.Join(dir, "file1.txt")
	writeFile(t, file1, "v1\n")

	tracker := NewFileChangeTracker(dir)

	// Step 1: modify file1.
	ctx1 := trackerCtx("step-1")
	tracker.RecordBeforeWrite(ctx1, file1)
	writeFile(t, file1, "v2\n")
	tracker.RecordAfterWrite(ctx1, file1)

	// Step 2: create file2.
	file2 := filepath.Join(dir, "file2.txt")
	ctx2 := trackerCtx("step-2")
	tracker.RecordBeforeWrite(ctx2, file2)
	writeFile(t, file2, "new\n")
	tracker.RecordAfterWrite(ctx2, file2)

	// Rollback all.
	if err := tracker.RollbackAll(); err != nil {
		t.Fatalf("rollback all failed: %v", err)
	}

	// file1 should be restored.
	got := readFileContent(t, file1)
	if got != "v1\n" {
		t.Errorf("file1: expected v1, got %q", got)
	}

	// file2 should be removed.
	if _, err := os.Stat(file2); !os.IsNotExist(err) {
		t.Error("expected file2 to be removed after rollback all")
	}
}

func TestFileChangeTracker_GetSessionChanges(t *testing.T) {
	dir := t.TempDir()

	file1 := filepath.Join(dir, "file1.txt")
	writeFile(t, file1, "original\n")

	tracker := NewFileChangeTracker(dir)

	// Step 1: modify file1.
	ctx1 := trackerCtx("step-1")
	tracker.RecordBeforeWrite(ctx1, file1)
	writeFile(t, file1, "step1\n")
	tracker.RecordAfterWrite(ctx1, file1)

	// Step 2: modify file1 again.
	ctx2 := trackerCtx("step-2")
	tracker.RecordBeforeWrite(ctx2, file1) // no-op, baseline already captured
	writeFile(t, file1, "step2\n")
	tracker.RecordAfterWrite(ctx2, file1)

	changes := tracker.GetSessionChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 aggregated change, got %d", len(changes))
	}
	c := changes[0]
	if c.Operation != "MODIFY" {
		t.Errorf("expected MODIFY, got %s", c.Operation)
	}
	// Diff should be from original baseline to current "step2".
	if !strings.Contains(c.Diff, "-original") {
		t.Errorf("diff should reference original baseline, got: %s", c.Diff)
	}
	if !strings.Contains(c.Diff, "+step2") {
		t.Errorf("diff should reference current content, got: %s", c.Diff)
	}
}

func TestFileChangeTracker_GetSessionChanges_CreateThenDelete(t *testing.T) {
	dir := t.TempDir()
	tracker := NewFileChangeTracker(dir)

	absPath := filepath.Join(dir, "ephemeral.txt")

	// Step 1: create file.
	ctx1 := trackerCtx("step-1")
	tracker.RecordBeforeWrite(ctx1, absPath)
	writeFile(t, absPath, "temp\n")
	tracker.RecordAfterWrite(ctx1, absPath)

	// Step 2: delete file.
	ctx2 := trackerCtx("step-2")
	tracker.RecordDelete(ctx2, absPath)
	_ = os.Remove(absPath)

	changes := tracker.GetSessionChanges()
	// File was created then deleted — should be omitted.
	for _, c := range changes {
		if c.Path == "ephemeral.txt" {
			t.Errorf("expected ephemeral.txt to be omitted from session changes, got %+v", c)
		}
	}
}

func TestFileChangeTracker_SnapshotAndDetectChanges(t *testing.T) {
	dir := t.TempDir()
	tracker := NewFileChangeTracker(dir)

	// Create initial file.
	file1 := filepath.Join(dir, "file1.txt")
	writeFile(t, file1, "original\n")

	// Take snapshot.
	before := tracker.SnapshotWorkspace()
	if _, ok := before["file1.txt"]; !ok {
		t.Fatal("expected file1.txt in snapshot")
	}

	// Make changes: modify file1, create file2, delete nothing.
	writeFile(t, file1, "changed\n")
	file2 := filepath.Join(dir, "file2.txt")
	writeFile(t, file2, "new file\n")

	// Detect changes.
	ctx := trackerCtx("step-1")
	tracker.DetectChanges(ctx, before)

	changes := tracker.GetStepChanges("step-1")
	if len(changes) < 2 {
		t.Fatalf("expected at least 2 changes, got %d", len(changes))
	}

	ops := make(map[string]string)
	for _, c := range changes {
		ops[c.Path] = c.Operation
	}
	if ops["file1.txt"] != "MODIFY" {
		t.Errorf("expected file1.txt MODIFY, got %s", ops["file1.txt"])
	}
	if ops["file2.txt"] != "CREATE" {
		t.Errorf("expected file2.txt CREATE, got %s", ops["file2.txt"])
	}
}

func TestFileChangeTracker_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	tracker := NewFileChangeTracker(dir)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			stepID := "step-" + strings.Repeat("x", n)
			ctx := trackerCtx(stepID)
			fname := filepath.Join(dir, "concurrent_"+strings.Repeat("x", n)+".txt")
			tracker.AcquireFileLock(fname)
			defer tracker.ReleaseFileLock(fname)
			tracker.RecordBeforeWrite(ctx, fname)
			writeFile(t, fname, "content\n")
			tracker.RecordAfterWrite(ctx, fname)
		}(i)
	}
	wg.Wait()

	// Verify no panic and session changes are accessible.
	changes := tracker.GetSessionChanges()
	if len(changes) != 10 {
		t.Errorf("expected 10 session changes, got %d", len(changes))
	}
}

func TestFileChangeTracker_ContextHelpers(t *testing.T) {
	t.Run("FileTracker", func(t *testing.T) {
		tracker := NewFileChangeTracker("/tmp")
		ctx := WithFileTracker(context.Background(), tracker)
		got := FileTrackerFromContext(ctx)
		if got != tracker {
			t.Error("expected same tracker from context")
		}
		// Nil context.
		if FileTrackerFromContext(context.Background()) != nil {
			t.Error("expected nil from empty context")
		}
	})

	t.Run("StepID", func(t *testing.T) {
		ctx := WithStepID(context.Background(), "abc-123")
		got := StepIDFromContext(ctx)
		if got != "abc-123" {
			t.Errorf("expected abc-123, got %s", got)
		}
		// Empty context.
		if StepIDFromContext(context.Background()) != "" {
			t.Error("expected empty string from empty context")
		}
	})
}

func TestFileChangeTracker_FileLock(t *testing.T) {
	dir := t.TempDir()
	tracker := NewFileChangeTracker(dir)

	absPath := filepath.Join(dir, "locked.txt")

	// Acquire and release should not deadlock.
	tracker.AcquireFileLock(absPath)
	tracker.ReleaseFileLock(absPath)

	// Acquire again — should succeed (not still locked).
	tracker.AcquireFileLock(absPath)
	tracker.ReleaseFileLock(absPath)

	// Concurrent lock contention — verify serialization.
	var wg sync.WaitGroup
	counter := 0
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.AcquireFileLock(absPath)
			defer tracker.ReleaseFileLock(absPath)
			// Non-atomic increment protected by file lock.
			v := counter
			counter = v + 1
		}()
	}
	wg.Wait()
	if counter != 5 {
		t.Errorf("expected counter=5 (serialized), got %d", counter)
	}
}

func TestFileChangeTracker_SnapshotSkipsDirs(t *testing.T) {
	dir := t.TempDir()

	// Create files in skip directories.
	writeFile(t, filepath.Join(dir, ".git", "config"), "git config\n")
	writeFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"), "module\n")
	writeFile(t, filepath.Join(dir, "src", "main.go"), "package main\n")

	tracker := NewFileChangeTracker(dir)
	snapshot := tracker.SnapshotWorkspace()

	if _, ok := snapshot[filepath.Join(".git", "config")]; ok {
		t.Error("snapshot should skip .git")
	}
	if _, ok := snapshot[filepath.Join("node_modules", "pkg", "index.js")]; ok {
		t.Error("snapshot should skip node_modules")
	}
	if _, ok := snapshot[filepath.Join("src", "main.go")]; !ok {
		t.Error("snapshot should include src/main.go")
	}
}

func TestFileChangeTracker_RollbackDeletedFile(t *testing.T) {
	dir := t.TempDir()
	absPath := filepath.Join(dir, "willdelete.txt")
	writeFile(t, absPath, "precious data\n")

	tracker := NewFileChangeTracker(dir)
	ctx := trackerCtx("step-1")

	tracker.RecordDelete(ctx, absPath)
	_ = os.Remove(absPath)

	// Verify file is gone.
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Fatal("file should be deleted")
	}

	// Rollback should restore it.
	if err := tracker.RollbackStep("step-1"); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	got := readFileContent(t, absPath)
	if got != "precious data\n" {
		t.Errorf("expected restored content, got %q", got)
	}
}
